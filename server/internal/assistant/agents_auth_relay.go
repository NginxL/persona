package assistant

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The auth relay (docs/operations/auth-relay.md): before an authorization link goes on a card, the extension whose auth_relay
// contribution names it is asked to complete it with the account it is signed in with (internal/extmgr, in this
// process). Done, the agent is told and no card goes up; otherwise the card goes up as before, saying why Persona did
// not do it. Where the link came from is checked here first: one of the user's own computers, a session known there,
// and a command of that session's last turn that printed the link as it is. The link is never logged.

// authRelayer is what the triage asks of the extensions (extmgr.Manager); without it there is no relay.
type authRelayer interface {
	// AuthRelays: an extension would be asked to complete link (one names its host and path).
	AuthRelays(link string) bool
	// CompleteAuth hands link to it; false when none takes it.
	CompleteAuth(ctx context.Context, link string) (wire.AuthRelayResult, bool)
}

// The manager's extensions are one (main.go hands them over as ExtensionTools).
var _ authRelayer = (*extmgr.Manager)(nil)

// authRelayWait bounds the checks and the extension's try (extmgr bounds the try at a minute).
const authRelayWait = 75 * time.Second

// The manager's own reasons for leaving a link to the user, next to the extensions' (wire.AuthRelayReason*).
const (
	relayNotOwnMachine  = "not_own_machine" // not one of the user's own computers (a member hub's, or none)
	relayUnknownSession = "unknown_session" // no such session on it
	relayNotInSession   = "not_in_session"  // no command of the session's last turn printed the link
)

// memberKind is the kind the devices give a computer only a member hub has (devices/paths.go).
const memberKind = "hub"

// relayAuth asks the extensions to complete w's link for c. done: authorized and the agent told (w says so, no card).
// why is the line the card adds when it was tried and left to the user ("" when nothing was tried).
func (s *Service) relayAuth(ctx context.Context, machine string, c proto.Session, w *proto.WorkState) (done bool, why string) {
	ar, ok := s.ext.(authRelayer)
	if !ok || w.Link == nil {
		return false, ""
	}
	link := strings.TrimSpace(w.Link.URL)
	if !ar.AuthRelays(link) {
		return false, "" // no extension completes this one: the card as before
	}
	l := s.lang(ctx)
	cctx, cancel := context.WithTimeout(ctx, authRelayWait)
	defer cancel()
	if reason := s.authRelaySource(cctx, machine, c.SessionID, link); reason != "" {
		s.workEvent(machine, c.SessionID, "auth_relay", map[string]any{"status": wire.AuthRelayStatusManual, "reason": reason})
		return false, relayWhy(l, wire.AuthRelayStatusManual, reason)
	}
	res, ok := ar.CompleteAuth(cctx, link)
	if !ok {
		return false, ""
	}
	s.workEvent(machine, c.SessionID, "auth_relay", map[string]any{"status": res.Status, "reason": res.Reason}) // never the link
	if res.Status != wire.AuthRelayStatusDone {
		return false, relayWhy(l, res.Status, res.Reason)
	}
	authorized := func() {
		app := res.App
		if app == "" {
			app = workT(l, "relay_app_unknown")
		}
		w.On, w.Link, w.Questions = proto.WaitExternal, nil, nil
		w.Line = clip(oneLine(workT(l, "relayed_line", i18n.Args{"app": app})), 60)
		w.Wait = w.Line
	}
	if now, ok := s.session(ctx, machine, c.SessionID); ok && (!sameTurn(now, c.UpdatedAt) || now.Status == proto.StatusWorking) {
		authorized() // it went on while the page was being authorized: nothing to tell it
		return true, ""
	}
	if err := s.sendToSession(ctx, machine, c.SessionID, renderAuthRelayed(res.App, answerLanguage(s.hello(ctx).Locale))); err != nil {
		// Authorized, but it did not hear so: the card, whose "Authorized, go on" tells it.
		s.logf("auth relay %s/%s: authorized, the agent was not told: %v", machine, short(c.SessionID), err)
		return false, workT(l, "relay_why_done")
	}
	authorized()
	return true, ""
}

// authRelaySource is why link may not be handed to an extension for machine's session sid ("" when it may): only
// the user's own computers, a session known there, and a link one of its own commands printed in its last turn.
func (s *Service) authRelaySource(ctx context.Context, machine, sid, link string) string {
	if !s.ownComputer(ctx, machine) {
		return relayNotOwnMachine
	}
	if _, ok := s.session(ctx, machine, sid); !ok {
		return relayUnknownSession
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": 1}, &res); err != nil {
		return relayNotInSession
	}
	full := func(id string) (string, bool) {
		var it proto.Item
		err := s.call(ctx, machine, "session.item", map[string]any{"session_id": sid, "item_id": id}, &it)
		return it.Text, err == nil && it.ID == id
	}
	if !commandPrinted(res.Items, link, full) {
		return relayNotInSession
	}
	return ""
}

// ownComputer: machine is one of the user's computers this manager connects itself, not one a member hub brought.
func (s *Service) ownComputer(ctx context.Context, machine string) bool {
	if s.devs != nil {
		id := s.devs.Canonical(machine)
		for _, d := range s.devs.List() {
			if d.ID == id {
				return d.Kind != memberKind
			}
		}
		return false
	}
	d, ok := s.device(ctx, machine)
	return ok && d.Kind != memberKind
}

// commandPrinted says whether a command of items printed link as it is: the result of a tool that runs a command on
// the agent's machine, not an error. A link the agent only wrote, or a page it fetched had, is not one. full is a
// result's whole text when a page cut it.
func commandPrinted(items []proto.Item, link string, full func(id string) (string, bool)) bool {
	if link == "" {
		return false
	}
	tools := map[string]string{} // tool_use_id → its call's tool
	for _, it := range items {
		if it.Kind == proto.KindToolUse {
			tools[it.ToolUseID] = it.ToolName
		}
	}
	for _, it := range items {
		if it.Kind != proto.KindToolResult || it.IsError || !commandTool(tools[it.ToolUseID]) {
			continue
		}
		text := it.Text
		if it.FullSize > 0 {
			if t, ok := full(it.ID); ok {
				text = t
			}
		}
		text = strings.NewReplacer(`\u0026`, "&", `\/`, "/").Replace(text) // a JSON printer's escapes
		if containsWhole(text, link) {
			return true
		}
	}
	return false
}

// commandTool: name is a tool that runs a command on the agent's machine (Claude Code's Bash and the output of one in
// the background; Codex's commands are Bash in its transcript too). A page an agent fetched (WebFetch, a browser or
// MCP tool) is not where its own program speaks.
func commandTool(name string) bool {
	switch name {
	case "Bash", "BashOutput":
		return true
	}
	return false
}

// containsWhole says whether text has link with nothing of a longer link right after it: the end, a space, a quote
// or bracket, or a sentence's punctuation before one.
func containsWhole(text, link string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], link)
		if j < 0 {
			return false
		}
		rest := text[i+j+len(link):]
		r, n := utf8.DecodeRuneInString(rest)
		switch {
		case rest == "" || unicode.IsSpace(r) || strings.ContainsRune(`"'<>()[]{}\`+"`", r):
			return true
		case strings.ContainsRune(".,;:!?", r):
			if r2, _ := utf8.DecodeRuneInString(rest[n:]); rest[n:] == "" || unicode.IsSpace(r2) {
				return true
			}
		}
		i += j + 1
	}
}

// relayWhy is the card's line for why a link was left to the user ("" when there is nothing to explain).
func relayWhy(l i18n.Lang, status, reason string) string {
	switch reason {
	case wire.AuthRelayReasonNewPermissions, wire.AuthRelayReasonAppNotAllowed, wire.AuthRelayReasonNotSignedIn,
		wire.AuthRelayReasonExpired, relayNotInSession:
		return workT(l, "relay_why_"+reason)
	}
	if status == wire.AuthRelayStatusFailed {
		return workT(l, "relay_why_failed")
	}
	return "" // not one it completes at all (another computer's, another kind of page): nothing to explain
}
