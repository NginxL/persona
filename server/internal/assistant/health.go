package assistant

import (
	"context"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/compat"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The health space (docs/assistant/health.md, #2691): the assistant's conversations about health run in a directory of their
// own next to the assistant's, so Claude Code's auto memory and the instructions are the space's own. Its
// conversations are listed apart (GET /api/v1/assistant?space=health), and the conversation tools of one space see
// only that space's conversations.

// SpaceHealth is the health space's name (Session.Space, StartParams.Space, ?space=).
const SpaceHealth = identity.SpaceHealth

// healthRel is this manager's health space directory on a machine, relative to its home (node assistant.prepare):
// health for the built-in manager, health-<id> for another (identity.HealthDirName), in the state directory, spelled as
// assistantRel is.
func (s *Service) healthRel() string { return path.Join(compat.StateDir, identity.HealthDirName(s.id)) }

// healthCwd is this manager's health space directory on d, or "" while d has not reported its home.
func (s *Service) healthCwd(d Device) string {
	return stateDirOn(d.Home, d.DataDir, identity.HealthDirName(s.id))
}

// spaceCwd is the directory of space ("" the assistant's own) on d: the health space's, or a track's whose template is
// space (spaceTrack, #13472); "" for a space that has none here, so nothing starts in the assistant's directory in
// its stead.
func (s *Service) spaceCwd(ctx context.Context, d Device, space string) string {
	switch space {
	case "":
		return s.assistantCwd(d)
	case SpaceHealth:
		return s.healthCwd(d)
	}
	if e, ok := s.spaceTrack(ctx, space); ok {
		return stateDirOn(d.Home, d.DataDir, e.DirName)
	}
	return ""
}

// healthClaudeMD is the health space's CLAUDE.md for machine here (prompts/assistant/health_claude_md.md).
func (s *Service) healthClaudeMD(ctx context.Context, here string) string {
	hello := s.hello(ctx)
	d := prompts.AssistantHealthClaudeMD{Owner: strings.TrimSpace(hello.Owner), Notes: s.notes(ctx), Language: answerLanguage(hello.Locale)}
	if name := identity.HealthDirName(s.id); name != identity.SpaceHealth {
		d.HealthDir = name // another manager's own space (the built-in's is the template's default)
	}
	devices := s.devices(ctx)
	if i := slices.IndexFunc(devices, func(d Device) bool { return d.ID == here }); i >= 0 {
		if cwd := s.assistantCwd(devices[i]); cwd != "" {
			d.StateDir = path.Dir(cwd)
		}
	}
	return prompts.RenderAssistantHealthClaudeMD(d)
}

// spaceOf is the space of an assistant conversation: its own Space when the hub set it, else its directory's.
func spaceOf(c proto.Session) string {
	if c.Space != "" {
		return c.Space
	}
	return identity.SpaceOf(c.Cwd)
}

// runSpace is the space session c works in for hiding and confirming: its own Space or its directory's, a directory
// inside a space counting as the space (identity.SpaceAt: this only hides, so a subfolder counts).
func runSpace(c proto.Session) string {
	if sp := spaceOf(c); sp != "" {
		return sp
	}
	return identity.SpaceAt(c.Cwd)
}

// What leaves a conversation (docs/assistant/health.md "Outbound", #3194, "Using information by scene", #13246): the
// user may share their health with anyone they like (a doctor, a coach, family), so nothing is refused for being
// sensitive. What must not happen is it leaving without them knowing: a push or a channel says only that there is
// news, and a hand-off is confirmed on the phone each time. A text on its way out is judged by its own categories
// and its track's default categories (exitPolicy), never by what its conversation said before; a track sensitive by
// default (the health space) asks no model and keeps every switch on.

// selfPolicy is the policy of the space conversation self runs in (the assistant's own, all off, for one that cannot
// be told: as before, the tools that reach other conversations then reach the assistant's only).
func (s *Service) selfPolicy(ctx context.Context, self string) TrackPolicy {
	space, _ := s.selfSpace(ctx, self)
	return s.trackPolicy(ctx, space)
}

// todoPolicy is the policy of the space a task was opened from (its origin conversation's); all off for none.
func (s *Service) todoPolicy(ctx context.Context, t Todo) TrackPolicy {
	if t.Origin == "" {
		return TrackPolicy{}
	}
	return s.selfPolicy(ctx, t.Origin)
}

// ixSpace is the space interaction ix belongs to: its directory's (a directory inside a space counts,
// identity.SpaceAt: this only hides, so a `cd` that left the hook in a subfolder must not show the card, #2368), else
// its conversation's space. spaceOf, which classifies, stays exact.
func (s *Service) ixSpace(ctx context.Context, ix proto.Interaction) string {
	if ix.Cwd != "" {
		return identity.SpaceAt(ix.Cwd)
	}
	if ix.SessionID != "" && ix.MachineID == s.settings(ctx).MachineID {
		space, _ := s.selfSpace(ctx, ix.SessionID)
		return space
	}
	return ""
}

// ixPolicy is the policy of the space interaction ix belongs to (ixSpace).
func (s *Service) ixPolicy(ctx context.Context, ix proto.Interaction) TrackPolicy {
	return s.trackPolicy(ctx, s.ixSpace(ctx, ix))
}

// spaceNotice is what a push or a channel says for a conversation of the health space: that there is news, not the
// news.
func spaceNotice(l i18n.Lang) (title, body string) {
	return i18n.T(l, "assistant-health.notice_title"), i18n.T(l, "assistant-health.notice_body")
}

// redactedNotice is what a push or a channel says for something redacted from track space: the health space's notice
// for a track whose default categories carry health (as it always said there), else only that there is news, not
// even which track.
func (s *Service) redactedNotice(ctx context.Context, l i18n.Lang, space string) (title, body string) {
	if floor, _ := s.trackFloor(ctx, space); slices.Contains(floor, categoryHealth) {
		return spaceNotice(l)
	}
	return i18n.T(l, "assistant-health.sensitive_notice_title"), i18n.T(l, "assistant-health.sensitive_notice_body")
}

// confirmOutbound asks the phone before conversation self hands text (said as summary) to somewhere outside it
// (tool: another agent, a new one): true when the card is allowed, at once when text needs no confirming (exitPolicy:
// its own categories and its track's default categories). outcome is how the card ended (proto.Resolution.Outcome; ""
// when none was asked): a refusal is the user's decline only when it is proto.OutcomeDeclined, and nobody answering
// otherwise (expired, withdrawn, cleared, answered for the user), which the tool must not report as a decline
// (outboundNotDone).
func (s *Service) confirmOutbound(ctx context.Context, machine, self, tool, text, summary string, input json.RawMessage) (allowed bool, outcome string, err error) {
	space, _ := s.selfSpace(ctx, self)
	if s.supervises(ctx, self) {
		// Decision rights (docs/designs/decision-rights-20261123.md): a supervisor (a direction lead or the main
		// conversation) handing work or words to the owner's own Agents, sessions and to-dos is none of the six kinds
		// of thing only the owner decides: nothing leaves the owner's computers in their name. The supervisor decides it
		// itself; no phone card, whatever the track or the words. Speaking for the owner to other people goes through
		// tools of its own, not this gate.
		return true, "", nil
	}
	if _, ok := ctx.Value(ownerConfirmedKey{}).(bool); ok {
		text = "" // the owner just confirmed these very words: only the track decides, as before tagging
	}
	if !s.exitPolicy(ctx, text, self, space).ConfirmOutbound {
		return true, "", nil
	}
	return s.outbound.confirm(ctx, outboundKey(self, tool, text, summary), func() (bool, string, error) {
		l := s.lang(ctx)
		now := time.Now().UTC()
		ix := proto.Interaction{ID: "ix_" + randomHex(6), MachineID: machine, SessionID: self, Kind: proto.IxPermission, ToolName: tool,
			Summary: s.outboundSummary(ctx, l, space, summary), ToolInput: input, SessionTitle: "Persona",
			CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
		if c, ok := s.session(ctx, machine, self); ok {
			ix.Cwd = c.Cwd
		}
		r, err := s.ask(withCard(ctx, cardConfirmOutbound), ix)
		if err != nil {
			return false, "", err
		}
		return r.Allow != nil && *r.Allow, r.Outcome(), nil
	})
}

// outboundNotDone is what a tool tells the model when confirmOutbound did not let it go (outcome: how its card ended):
// declined only when the user themselves declined it on a device; otherwise nobody answered it (it expired, was
// withdrawn or cleared, or someone else answered for the user), which is never told as the user declining.
// nothing says what did not happen ("nothing was sent to that agent").
func outboundNotDone(outcome, declined, nothing string) string {
	if outcome == proto.OutcomeDeclined {
		return declined
	}
	return prompts.RenderOutboundUnanswered(prompts.OutboundUnanswered{Outcome: outcome, Nothing: nothing})
}

// ownerConfirmedKey marks a tool call carrying words the owner confirmed on the phone just before (an Inbox
// suggestion's instruction): confirmOutbound does not ask about them again by what they say.
type ownerConfirmedKey struct{}

// ownerConfirmed is ctx for a tool call whose words the owner just confirmed.
func ownerConfirmed(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownerConfirmedKey{}, true)
}

// outboundSummary is what the phone is asked before a hand-off of what (said as summary) from track space: as the
// health space always asked for a track whose default categories carry health, else as for any sensitive content.
func (s *Service) outboundSummary(ctx context.Context, l i18n.Lang, space, what string) string {
	if floor, _ := s.trackFloor(ctx, space); slices.Contains(floor, categoryHealth) {
		return i18n.T(l, "assistant-health.outbound", i18n.Args{"what": what})
	}
	return i18n.T(l, "assistant-health.sensitive_outbound", i18n.Args{"what": what})
}
