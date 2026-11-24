package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// A session open in Claude's desktop app (entrypoint claude-desktop-3p, as one build writes it) or in a terminal
// Persona does not drive lists as running, with its status from its sessions/<pid>.json and its transcript as for
// any session; it can be sent to when its peer inbox is one the node can use, and says why not when it is not.
func TestOutsideClaudeListedLive(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	p := bgTranscript(t, home, home, sidA, "plan the release")
	inbox := newFakeInbox(t, p)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))

	for _, c := range []struct{ entrypoint, source string }{
		{"claude-desktop", proto.SourceClaudeDesktop},
		{"claude-desktop-3p", proto.SourceClaudeDesktop},
		{"cli", proto.SourceClaudeTerminal},
	} {
		writeOutsideClaude(t, home, sidA, c.entrypoint, "busy", inbox, nil)
		s := byID(listSessions(t, n))[sidA]
		if s.SourceKind != c.source || s.Status != proto.StatusWorking || s.PaneID != "" || s.Title != "plan the release" ||
			s.LastText != "on it" || s.ReadOnlyReason != "" {
			t.Errorf("%s: %+v", c.entrypoint, s)
		}
		// Busy: the phone's Stop cuts its turn through the inbox (interruptOutside). With no pane, no queue controls (#12789).
		if cp := s.Capabilities; cp == nil || !cp.Read || !cp.Send || !cp.Interrupt || cp.Approve || cp.Terminal || cp.Attach ||
			cp.Modes == nil || *cp.Modes || cp.QueueEdit == nil || *cp.QueueEdit || cp.QueueSendNow == nil || *cp.QueueSendNow {
			t.Errorf("%s capabilities = %+v", c.entrypoint, cp)
		}
	}

	for status, want := range map[string]string{"idle": proto.StatusIdle, "waiting": proto.StatusBlocked, "later": proto.StatusUnknown} {
		writeOutsideClaude(t, home, sidA, "cli", status, inbox, nil)
		s := byID(listSessions(t, n))[sidA]
		if s.Status != want {
			t.Errorf("status %s: listed %s, want %s", status, s.Status, want)
		}
		if (status == "waiting") != (s.AttentionReason == proto.AttentionClaudeComputer) {
			t.Errorf("status %s: attention %q", status, s.AttentionReason)
		}
	}

	// No inbox (an older Claude Code, or the feature off there): listed, read-only, saying why.
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", nil, nil)
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || s.Capabilities.Send || s.ReadOnlyReason != proto.ReadOnlyClaudeNoInbox {
		t.Errorf("no inbox: %+v", s)
	}
	// An inbox whose protocol the node does not speak.
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", inbox, map[string]any{"peerProtocol": 2})
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities.Send || s.ReadOnlyReason != proto.ReadOnlyClaudeInbox {
		t.Errorf("unknown protocol: %+v", s)
	}
	// A claude run from the SDK, or one that exited: not listed live.
	writeOutsideClaude(t, home, sidA, "sdk-ts", "busy", inbox, nil)
	if s, ok := byID(listSessions(t, n))[sidA]; ok && s.Status != proto.StatusOffline {
		t.Errorf("SDK claude listed live: %+v", s)
	}
	_ = os.Remove(filepath.Join(home, ".claude", "sessions", strconv.Itoa(os.Getpid())+".json"))
	writeClaudeProc(t, home, 999999, map[string]any{"sessionId": sidA, "cwd": home, "kind": "interactive", "entrypoint": "cli", "status": "busy"})
	if s, ok := byID(listSessions(t, n))[sidA]; ok && s.Status != proto.StatusOffline {
		t.Errorf("exited claude listed live: %+v", s)
	}
}

// Claude Code's queue edit and send-now are keys on its own screen: for a session the desktop app has open (no pane)
// a call that comes anyway is refused with its reason rather than failing as not running, which the hub answers with
// 502 (#12789). A session no claude has open still fails as before.
func TestOutsideClaudeQueueCallsWithoutPane(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	p := bgTranscript(t, home, home, sidA, "plan the release")
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	writeOutsideClaude(t, home, sidA, "claude-desktop", "busy", newFakeInbox(t, p), nil)

	ctx := context.Background()
	if err := n.sendQueuedNow(ctx, sidA); codeOf(err) != "send_now_unsupported" {
		t.Errorf("send now: %v", err)
	}
	if _, err := n.editQueue(ctx, sidA, []int{0}); codeOf(err) != "queue_edit_unsupported" {
		t.Errorf("edit: %v", err)
	}
	if err := n.sendQueuedNow(ctx, sidB); err == nil || codeOf(err) != "" {
		t.Errorf("a session no claude has open: %v", err)
	}
}

// A claude in a pane its engine lists with a claude in it is that engine's, not one outside. A claude whose pane
// variable names a pane no engine in use lists as a claude (Orca's terminals on a node that enables herdr alone, a
// claude the engine's process scan missed) is listed live as a terminal claude, never left to the offline list.
func TestClaudeInEnginePaneNotOutside(t *testing.T) {
	n, f, home := herdrNode(t)
	old := paneEnvOfPID
	paneEnvOfPID = func(int) map[string]string { return map[string]string{herdrPaneEnv: "w9:p9"} }
	t.Cleanup(func() { paneEnvOfPID = old })
	p := bgTranscript(t, home, home, sidA, "inside")
	writeOutsideClaude(t, home, sidA, "cli", "busy", newFakeInbox(t, p), nil)
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent("", proto.StatusWorking, "w9:p9", "w9:t9", "w9", home, "")}, nil))
	if s, ok := byID(listSessions(t, n))[sidA]; ok && s.SourceKind != "" {
		t.Errorf("a claude in an engine pane listed as outside: %+v", s)
	}

	// An Orca pane on a node that does not enable Orca: nothing lists it. Just seen, it is left to its engine for a
	// moment (an engine reports a claude typed into its pane a little after it starts); then it is a live terminal claude.
	paneEnvOfPID = func(int) map[string]string { return map[string]string{orcaPaneEnv: "tab-1:leaf-1"} }
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	if s, ok := byID(listSessions(t, n))[sidA]; ok && s.SourceKind != "" {
		t.Errorf("a claude just seen in an engine pane listed as outside: %+v", s)
	}
	oldGrace := outsidePaneGrace
	outsidePaneGrace = 0
	t.Cleanup(func() { outsidePaneGrace = oldGrace })
	if s := byID(listSessions(t, n))[sidA]; s.SourceKind != proto.SourceClaudeTerminal || s.Status != proto.StatusWorking {
		t.Errorf("a claude in a pane no engine lists: %+v", s)
	}
	// The pane is listed but its engine saw no claude there (its scan missed it): still live.
	paneEnvOfPID = func(int) map[string]string { return map[string]string{herdrPaneEnv: "w9:p9"} }
	f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("", "", "", "w9:p9", "w9:t9", "w9", home, "")}, nil))
	if s := byID(listSessions(t, n))[sidA]; s.SourceKind != proto.SourceClaudeTerminal || s.Status != proto.StatusWorking {
		t.Errorf("a claude in a pane whose engine saw no claude: %+v", s)
	}
	// The desktop app's claude has no pane variable to look for: always outside.
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", newFakeInbox(t, p), nil)
	if s := byID(listSessions(t, n))[sidA]; s.SourceKind != proto.SourceClaudeDesktop {
		t.Errorf("desktop: %+v", s)
	}
}

// A message from the phone goes through the inbox: the auth line first (with the token from the key file), then the
// message, wrapped and signed as Persona's, for this session. It counts once the session wrote that it took it; herdr
// is never asked to start anything.
func TestOutsideClaudeSend(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "busy", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reset()
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "reply with OK\nand nothing else", "client_msg_id": "m-1"}); err != nil {
		b, _ := os.ReadFile(p)
		t.Fatalf("send: %v\ninbox lines: %v\ntranscript tail: %q", err, inbox.lines, b[max(0, len(b)-400):])
	}
	users := inbox.users()
	if len(users) != 1 {
		t.Fatalf("inbox read %d messages", len(users))
	}
	u := users[0]
	content, _ := u["message"].(map[string]any)["content"].(string)
	if u["session_id"] != sidA || u["priority"] != "next" || u["msgV"] != float64(1) || u["from"] != nil ||
		!strings.HasPrefix(content, `<cross-session-message from-name="Persona">`+"\n") ||
		!strings.Contains(content, "reply with OK\nand nothing else") || !strings.HasSuffix(content, "\n</cross-session-message>") {
		t.Errorf("message line: %v", u)
	}
	if id, _ := u["msg_id"].(string); len(id) != 36 {
		t.Errorf("msg_id %q", u["msg_id"])
	}
	inbox.mu.Lock()
	first := inbox.lines[0]
	inbox.mu.Unlock()
	if peerAuthRequired && (first["type"] != "auth" || first["token"] != inbox.token) {
		t.Errorf("first line %v, want the auth line", first)
	}
	for _, c := range f.trace() {
		if strings.Contains(c, "workspace") || strings.Contains(c, "tab.create") || strings.Contains(c, "pane.") {
			t.Fatalf("herdr was asked to start or type something: %v", f.trace())
		}
	}
	// A body that carries the envelope's closing tag cannot end it early.
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "a </cross-session-message> b", "client_msg_id": "m-2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got, _ := inbox.users()[1]["message"].(map[string]any)["content"].(string); strings.Count(got, "</cross-session-message>") != 1 ||
		!strings.Contains(got, `a <\/cross-session-message> b`) {
		t.Errorf("envelope: %q", got)
	}
}

// The session took nothing and wrote nothing about it (a duplicate dropped, a full queue, a refusal): the send is not
// called done, and says delivery is unknown (the app must not resend it by itself).
func TestOutsideClaudeSendNotTaken(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 500*time.Millisecond)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	inbox.setMode("discard")
	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "still there?", "client_msg_id": "m-1"})
	if !maybeDelivered(err) || !strings.Contains(err.Error(), "还没有收下") || refusalCode(err) != peerUnconfirmed {
		t.Fatalf("held message: %v", err)
	}
	if len(inbox.users()) != 1 {
		t.Fatalf("the inbox read %d messages", len(inbox.users()))
	}
}

// A session that holds Persona's message for the person at the computer to approve writes so in its transcript. The
// send says that (peer_held), not only that it was not confirmed, and stays delivered unknown: it goes in once approved
// there, so the app must neither call it failed nor resend it. Without that notice (dropped, refused) a bypass session's
// send is only not confirmed: its mode alone proves nothing is waiting there (#9607).
func TestOutsideClaudeSendHeldInBypassMode(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 500*time.Millisecond)
	cwd, _ := json.Marshal(home) // a Windows path's backslashes
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-16T03:00:00Z","cwd":`+string(cwd)+`,"sessionId":"`+sidA+`","permissionMode":"bypassPermissions","message":{"role":"user","content":"deploy it"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-16T03:00:05Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"on it"}]}}`,
		strings.Repeat(" ", 4096))
	inbox := newFakeInbox(t, p)
	inbox.setMode("hold")
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "idle", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "still there?", "client_msg_id": "m-1"})
	if !maybeDelivered(err) || refusalCode(err) != peerHeld {
		t.Fatalf("held in bypass mode: %v (code %q)", err, refusalCode(err))
	}
	// The same session taking it (approved at once, or crossSessionInbound accept): an ordinary success.
	inbox.setMode("take")
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "and now?", "client_msg_id": "m-2"}); err != nil {
		t.Fatalf("taken: %v", err)
	}
}

// modeTranscript is session sidA's transcript, its user line in permission mode mode, under cwd.
func modeTranscript(t *testing.T, home, cwd, mode string) string {
	t.Helper()
	c, _ := json.Marshal(cwd)
	return writeTranscript(t, home, cwd, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-16T03:00:00Z","cwd":`+string(c)+`,"sessionId":"`+sidA+`","permissionMode":"`+mode+`","message":{"role":"user","content":"deploy it"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-16T03:00:05Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"on it"}]}}`,
		strings.Repeat(" ", 4096))
}

// envelopeHead is the first line (the envelope's opening tag) of the i-th message the inbox read.
func envelopeHead(t *testing.T, inbox *fakeInbox, i int) string {
	t.Helper()
	us := inbox.users()
	if len(us) <= i {
		t.Fatalf("the inbox read %d messages, want %d", len(us), i+1)
	}
	content, _ := us[i]["message"].(map[string]any)["content"].(string)
	h, _, _ := strings.Cut(content, "\n")
	return h
}

// The node states bypass only for the owner's own message (owner_input, exactly as the devices manager writes it) to a
// session whose own transcript says bypassPermissions, which Claude Code 2.1.326 then takes at once; and nothing to one
// that prompts, which takes it (#9607). Nothing else states a class: a message with no owner mark (supervision, another
// Agent's) is held there; so is one claiming a class (from_mode) or the owner mark in another letter case (OWNER_INPUT,
// which encoding/json alone would read as owner_input), or the mark twice; so is a Stop, which carries no owner mark.
func TestOutsideClaudeStatesBypassOnlyForTheOwner(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 2*time.Second)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	send := func(params map[string]any) error {
		params["session_id"] = sidA
		_, err := request(t, n, "session.send", params)
		return err
	}
	const owner, bare = `<cross-session-message from-name="Persona" from-mode="bypass">`, `<cross-session-message from-name="Persona">`

	inbox := newFakeInbox(t, modeTranscript(t, home, home, "bypassPermissions"))
	inbox.setMode(peerModeBypass)
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", inbox, nil)
	if err := send(map[string]any{"text": "ship it", "client_msg_id": "m-1", "owner_input": true}); err != nil {
		t.Fatalf("the owner's message to a bypass session: %v", err)
	}
	if h := envelopeHead(t, inbox, 0); h != owner {
		t.Errorf("the owner's message to a bypass session: %q", h)
	}
	for i, params := range []map[string]any{
		{"text": "a worker's report"},
		{"text": "claims bypass", "from_mode": "bypass"},
		{"text": "claims bypass, upper case", "FROM_MODE": "bypass"},
		{"text": "claims the owner, upper case", "OWNER_INPUT": true},
		{"text": "claims the owner twice", "owner_input": true, "Owner_Input": true},
	} {
		params["client_msg_id"] = "n-" + strconv.Itoa(i)
		err := send(params)
		if h := envelopeHead(t, inbox, i+1); h != bare || refusalCode(err) != peerHeld {
			t.Errorf("%v: %q, %v", params, h, err)
		}
	}
	writeOutsideClaude(t, home, sidA, "claude-desktop", "busy", inbox, nil)
	_, err := request(t, n, "session.interrupt", map[string]any{"session_id": sidA, "owner_input": true})
	if h := envelopeHead(t, inbox, 6); h != bare || refusalCode(err) != peerHeld {
		t.Errorf("unmarked Stop: %q, %v", h, err)
	}
	_, err = request(t, n, nodeproto.OwnerInterrupt, map[string]any{"session_id": sidA})
	if h := envelopeHead(t, inbox, 7); h != owner || err != nil {
		t.Errorf("owner Stop: %q, %v", h, err)
	}

	for _, mode := range []string{"default", "acceptEdits", "auto", "plan"} {
		inbox := newFakeInbox(t, modeTranscript(t, home, home, mode))
		inbox.setMode(peerModePrompting)
		writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", inbox, nil)
		if err := send(map[string]any{"text": "ship it", "client_msg_id": "o-" + mode, "owner_input": true}); err != nil {
			t.Fatalf("the owner's message to a %s session: %v", mode, err)
		}
		if h := envelopeHead(t, inbox, 0); h != bare {
			t.Errorf("the owner's message to a %s session: %q", mode, h)
		}
	}
}

// The owner's message reads the session's class from the newest copy of its transcript in the live claude's own
// config directory, not from the node's cached transcript path (30 s): a copy written since counts at once.
func TestOutsideClaudeOwnerClassFromNewestCopy(t *testing.T) {
	n, _, home := herdrNode(t)
	noPaneEnv(t)
	old := modeTranscript(t, home, filepath.Join(home, "old"), "default")
	if got := n.transcriptPath(sidA); got != old {
		t.Fatalf("old path %q, got %q", old, got)
	}
	current := modeTranscript(t, home, filepath.Join(home, "current"), "bypassPermissions")
	stamp := time.Now().Add(time.Minute)
	if err := os.Chtimes(current, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", nil, nil)
	c, ok := n.outsideClaude(sidA)
	if !ok {
		t.Fatal("no outside claude")
	}
	if got := peerSessionClass(n, c, sidA); got != peerModeBypass {
		t.Errorf("class with an older copy cached: %q", got)
	}
}

// A bypass session that read the message and wrote nothing about it (dropped, refused) is not reported as holding it:
// its mode alone proves nothing waits there (#9607).
func TestOutsideClaudeDiscardedIsNotHeld(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 300*time.Millisecond)
	inbox := newFakeInbox(t, modeTranscript(t, home, home, "bypassPermissions"))
	inbox.setMode("discard")
	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "dropped there", "owner_input": true})
	if !maybeDelivered(err) || refusalCode(err) != peerUnconfirmed {
		t.Fatalf("a dropped message: %v (code %q)", err, refusalCode(err))
	}
}

// The envelope is one Claude Code 2.1.326 parses, with from-mode read back as stated, and a body carrying the closing
// tag still parses as one message.
func TestPeerEnvelopeParsedAsClaudeCodeDoes(t *testing.T) {
	for _, mode := range []string{"", peerModeBypass} {
		env := peerEnvelope("hello\na </cross-session-message> b", mode)
		m := claudeEnvelopeRe.FindStringSubmatch(env)
		if m == nil || m[4] != "Persona" || m[5] != mode || m[7] != `hello`+"\n"+`a <\/cross-session-message> b` {
			t.Errorf("from-mode %q: %q parsed as %q", mode, env, m)
		}
	}
	if !claudeTakes(peerModeBypass, peerEnvelope("x", peerModeBypass)) || claudeTakes(peerModeBypass, peerEnvelope("x", "")) ||
		!claudeTakes("prompting", peerEnvelope("x", "")) || claudeTakes("prompting", peerEnvelope("x", peerModeBypass)) {
		t.Error("claudeTakes does not follow Claude Code's rule")
	}
}

// A session waiting on a permission prompt or another dialog takes the message into its queue, but reads it only once
// that is answered at the computer: the send succeeds and says so (waits_on_computer); one that is working or idle
// does not. (One waiting on its own question takes the message as the answer: claudepeer_stop_test.go.)
func TestOutsideClaudeSendBehindDialog(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	for i, c := range []struct {
		status string
		waits  bool
	}{{"waiting", true}, {"busy", false}, {"idle", false}} {
		writeOutsideClaude(t, home, sidA, "cli", c.status, inbox, nil)
		res, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "why nothing to tap? " + c.status,
			"client_msg_id": "m-" + strconv.Itoa(i)})
		if err != nil {
			t.Fatalf("%s: %v", c.status, err)
		}
		if res["ok"] != true || (res["waits_on_computer"] == true) != c.waits {
			t.Errorf("%s: answer %v", c.status, res)
		}
	}
}

// Refusals say why and send nothing, never falling back to a second claude: no key file (Windows needs its token), an
// inbox that is not there, an unknown protocol, a slash command (the inbox runs none), a message too long for a line.
func TestOutsideClaudeSendRefusals(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 300*time.Millisecond)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	sent := 0
	send := func(text string) error {
		f.reset()
		sent++ // each its own client_msg_id: a send that went is remembered by its id
		_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": text, "client_msg_id": "m-" + strconv.Itoa(sent)})
		for _, c := range f.trace() {
			if strings.Contains(c, "tab.create") || strings.Contains(c, "pane.") {
				t.Fatalf("herdr was asked to start or type something: %v", f.trace())
			}
		}
		return err
	}

	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, map[string]any{"peerProtocol": 2})
	if err := send("hi"); !isRefusal(err, peerUnsupported) {
		t.Errorf("unknown protocol: %v", err)
	}

	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, nil)
	if err := send("/model opus"); !isRefusal(err, peerCommand) {
		t.Errorf("slash command: %v", err)
	}
	if err := send(strings.Repeat("x", peerLineMax)); !isRefusal(err, peerTooLong) {
		t.Errorf("too long: %v", err)
	}

	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, map[string]any{"messagingSocketPath": goneInboxPath(inbox.path)})
	if err := send("hi"); !isRefusal(err, peerUnreachable) {
		t.Errorf("no inbox there: %v", err)
	}

	if peerAuthRequired {
		writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, nil)
		_ = os.Remove(filepath.Join(home, ".claude", "sessions", peerKeyName(os.Getpid(), inbox.path)))
		if err := send("hi"); !isRefusal(err, peerUnreachable) {
			t.Errorf("no key: %v", err)
		}
	}
	if len(inbox.users()) != 0 {
		t.Errorf("the inbox read %d messages", len(inbox.users()))
	}

	// No inbox at all (an older Claude Code): refused as open elsewhere, as before.
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", nil, nil)
	if err := send("hi"); !isRefusal(err, openElsewhere) || !strings.Contains(err.Error(), "Claude 桌面版") {
		t.Errorf("no inbox: %v", err)
	}
}

// The key file's name is the sha256 of the canonical inbox path, as Claude Code derives it (a pipe's name in lower
// case, LOCAL\ kept); the number checked against a key file Claude Code 2.1.308 wrote for a pipe of this shape.
func TestPeerKeyName(t *testing.T) {
	const pipe = `\\.\pipe\LOCAL\cc-msg-00112233445566778899AABBCCDDEEFF`
	got := peerKeyName(42, pipe)
	if got != peerKeyName(42, strings.ToLower(pipe)) || got != peerKeyName(42, `//./pipe/LOCAL/cc-msg-00112233445566778899aabbccddeeff`) {
		t.Errorf("case or separators changed the name: %s", got)
	}
	if !strings.HasPrefix(got, "42.") || !strings.HasSuffix(got, ".key") || len(got) != len("42.")+64+len(".key") {
		t.Errorf("name %q", got)
	}
	if peerKeyName(42, `\\.\pipe\x.`) != "" || peerKeyName(42, "rel/1.sock") != "" || peerKeyName(42, "/tmp/../x.sock") != "" {
		t.Error("a path Claude Code names no key for got one")
	}
	if peerKeyName(42, `\\.\pipe\cc-msg-1`) == peerKeyName(42, `\\.\pipe\LOCAL\cc-msg-1`) {
		t.Error("LOCAL\\ must count")
	}
	if !peerPathUsable(pipeOrSocket()) {
		t.Errorf("%s is not usable here", pipeOrSocket())
	}
}

// pipeOrSocket is an inbox path of this platform's shape.
func pipeOrSocket() string {
	if peerAuthRequired {
		return `\\.\pipe\LOCAL\cc-msg-00112233445566778899aabbccddeeff`
	}
	return "/tmp/cc-socks-501/123.sock"
}

// The session takes a message: what peerTaken looks for in its transcript.
func TestPeerTaken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	_ = os.WriteFile(p, []byte(`{"type":"queue-operation","operation":"enqueue","content":"old \"hi\""}`+"\n"), 0o600)
	st, _ := os.Stat(p)
	if peerTaken(context.Background(), func() string { return p }, st.Size(), `hi`, 50*time.Millisecond) {
		t.Fatal("a line from before the send counted")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
		_, _ = f.WriteString(`{"type":"queue-operation","operation":"enqueue","content":"Another Claude session sent a message:\n<cross-session-message from-name=\"Persona\">\nsay \"hi\"\n</cross-session-message>"}` + "\n")
		_ = f.Close()
	}()
	if !peerTaken(context.Background(), func() string { return p }, st.Size(), "say \"hi\"\nsecond line", 2*time.Second) {
		t.Fatal("the queue-operation it wrote was not seen")
	}
}

// A line longer than peerSeen decodes (a user line with pasted images inline, a large tool result) is read past: the
// queue-operation after it still counts, and nothing is made up when none follows.
func TestPeerSeenPastALongLine(t *testing.T) {
	long := `{"type":"user","message":{"content":"` + strings.Repeat("A", 9<<20) + `"}}` + "\n"
	taken := `{"type":"queue-operation","operation":"enqueue","content":"say \"hi\""}` + "\n"
	for _, c := range []struct {
		name, after string
		want        bool
	}{
		{"taken after it", taken, true},
		{"nothing after it", `{"type":"assistant","message":{"content":"ok"}}` + "\n", false},
		{"taken, no newline yet", strings.TrimSuffix(taken, "\n"), true},
	} {
		p := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(p, []byte(`{"type":"summary"}`+"\n"+long+c.after), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := peerSeen(p, int64(len(`{"type":"summary"}`)+1), `say "hi"`); got != c.want {
			t.Errorf("%s: peerSeen = %v, want %v", c.name, got, c.want)
		}
	}
	// A long line that fits is still decoded: a peer's message with a long body.
	p := filepath.Join(t.TempDir(), "s.jsonl")
	body := `say "hi" ` + strings.Repeat("x", 200<<10)
	line, _ := json.Marshal(map[string]any{"type": "user", "origin": map[string]string{"kind": "peer", "body": body}})
	if err := os.WriteFile(p, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if !peerSeen(p, 0, `say "hi"`) {
		t.Error("a 200 KiB peer line was not decoded")
	}
}

// A message sent through a claude's inbox comes back in its transcript as a peer's message signed Persona (a hidden
// line when the session rested, a queued command while it worked): that line is the send's copy, tied to its
// client_msg_id, while another Claude session's message with the same words is not.
func TestEchoTiesAPeerSend(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "P", Text: "reply with OK", SentAt: t0, AfterSeq: 1})
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "Q", Text: "and then stop", SentAt: t0, AfterSeq: 1})
	peer := func(uuid, from, body string) string {
		return `{"type":"user","uuid":"` + uuid + `","timestamp":"` + t0.Add(time.Second).UTC().Format(time.RFC3339Nano) +
			`","isMeta":true,"message":{"role":"user","content":"Another Claude session sent a message:\n<cross-session-message from-name=\"` +
			from + `\">\n` + body + `\n</cross-session-message>"},"origin":{"kind":"peer","name":"` + from + `","body":"` + body + `"}}`
	}
	queued := `{"type":"attachment","uuid":"q1","timestamp":"` + t0.Add(2*time.Second).UTC().Format(time.RFC3339Nano) +
		`","attachment":{"type":"queued_command","prompt":"<cross-session-message from-name=\"Persona\">\nand then stop\n</cross-session-message>","origin":{"kind":"peer","name":"Persona","body":"and then stop"}}}`
	items := echoItems(t,
		assistantLine("a0", t0.Add(-time.Second), "working"),
		peer("o1", "release-bot", "reply with OK"), // another session, same words: not this send
		peer("p1", "Persona", "reply with OK"),
		queued,
	)
	l.match("s", echoInTranscript, items)
	sameTags(t, l.tag("s", echoInTranscript, items), map[string]string{"p1#0": "P", "q1#0": "Q"})
}

// A mode or effort change for a Claude outside Persona's terminals is refused with a reason that says where to change
// it (#1843): the peer inbox takes messages only and no message gives it a pane, so "send a message first" was wrong.
// A session nothing runs keeps the not-running refusal, now with a code the hub words in the request's language.
func TestOutsideClaudeModesRefused(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	p := bgTranscript(t, home, home, sidA, "plan the release")
	inbox := newFakeInbox(t, p)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	for _, entrypoint := range []string{"claude-desktop", "cli"} {
		writeOutsideClaude(t, home, sidA, entrypoint, "idle", inbox, nil)
		for _, m := range []map[string]any{{"permission_mode": "plan"}, {"effort": "high"}, {"model": "opus"}} {
			m["session_id"] = sidA
			if _, err := request(t, n, "session.modes", m); !isRefusal(err, proto.CodeModesOnComputer) {
				t.Errorf("%s %v: %v", entrypoint, m, err)
			}
		}
	}
	_ = os.Remove(filepath.Join(home, ".claude", "sessions", strconv.Itoa(os.Getpid())+".json"))
	if _, err := request(t, n, "session.modes", map[string]any{"session_id": sidA, "permission_mode": "plan"}); !isRefusal(err, proto.CodeModesNotRunning) {
		t.Errorf("not running: %v", err)
	}
}

func TestOwnerInputExactRejectsDuplicateKeys(t *testing.T) {
	for _, params := range []string{
		`{"owner_input":true,"owner_input":true}`,
		`{"owner_input":false,"owner_input":true}`,
		`{"owner_input":false,"owner\u005finput":true}`,
		`{"OWNER_INPUT":false,"owner_input":true}`,
		`{"owner_input":true}{}`,
	} {
		if ownerInputExact(json.RawMessage(params)) {
			t.Errorf("accepted invalid owner mark: %s", params)
		}
	}
	if !ownerInputExact(json.RawMessage(`{"owner_input":true,"nested":{"owner_input":false}}`)) {
		t.Error("a nested key counted as a top-level owner mark")
	}
}
