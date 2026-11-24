package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// ownerTmuxPane is the observable terminal behind an outside Claude's tmux pane. A submission is recorded whether
// the node uses the host's prompt operation or types text and presses Enter. The next screen read makes it visible in
// Claude's transcript, so a send that never does the ordinary post-prompt confirmation read cannot pass as delivered.
type ownerTmuxPane struct {
	mu                  sync.Mutex
	plain, ansi         string
	draft               string
	writes              []string
	keys                []string
	submissions         []ownerTmuxSubmission
	unconfirmed         string
	confirmRead         func(string)
	readErr             error
	failReadAfterPrompt bool
}

type ownerTmuxSubmission struct {
	pane, text string
}

func (h *ownerTmuxPane) Kind() string    { return host.Tmux }
func (h *ownerTmuxPane) PaneEnv() string { return "TMUX_PANE" }
func (h *ownerTmuxPane) Version(context.Context) string {
	return ""
}
func (h *ownerTmuxPane) Snapshot(context.Context) (*host.Layout, error) { return &host.Layout{}, nil }
func (h *ownerTmuxPane) Agents(context.Context) ([]host.Agent, error)   { return nil, nil }
func (h *ownerTmuxPane) Agent(context.Context, host.PaneRef) (*host.Agent, error) {
	return nil, fmt.Errorf("%w: tmux", host.ErrNoAgent)
}
func (h *ownerTmuxPane) Read(_ context.Context, _ host.PaneRef, opts host.ReadOpts) (string, error) {
	h.mu.Lock()
	text := h.plain
	if opts.ANSI && h.ansi != "" {
		text = h.ansi
	}
	confirm := h.unconfirmed
	h.unconfirmed = ""
	readErr := h.readErr
	h.mu.Unlock()
	if readErr != nil {
		return "", readErr
	}
	if confirm != "" {
		h.confirmRead(confirm)
	}
	return text, nil
}
func (h *ownerTmuxPane) SendText(_ context.Context, _ host.PaneRef, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes = append(h.writes, "text")
	h.draft += text
	return nil
}
func (h *ownerTmuxPane) SendKeys(_ context.Context, pane host.PaneRef, keys ...string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes = append(h.writes, "keys")
	for _, key := range keys {
		h.keys = append(h.keys, pane.String()+":"+key)
		if key == "enter" && h.draft != "" {
			h.submissions = append(h.submissions, ownerTmuxSubmission{pane: pane.String(), text: h.draft})
			h.unconfirmed = h.draft
			h.draft = ""
		}
	}
	return nil
}
func (h *ownerTmuxPane) Prompt(_ context.Context, pane host.PaneRef, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes = append(h.writes, "prompt")
	h.submissions = append(h.submissions, ownerTmuxSubmission{pane: pane.String(), text: text})
	h.unconfirmed = text
	if h.failReadAfterPrompt {
		h.readErr = errors.New("capture failed after the prompt")
	}
	return nil
}
func (h *ownerTmuxPane) PaneProcesses(context.Context, host.PaneRef) ([]int, error) {
	return nil, nil
}
func (h *ownerTmuxPane) CanDriveScreen() bool { return true }

func (h *ownerTmuxPane) writeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.writes)
}

func (h *ownerTmuxPane) submitted() []ownerTmuxSubmission {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ownerTmuxSubmission(nil), h.submissions...)
}

func (h *ownerTmuxPane) sentKeys() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.keys...)
}

type ownerTmuxTestNode struct {
	n          *Node
	pane       *ownerTmuxPane
	inbox      *fakeInbox
	plugin     *fakePlugin
	transcript string
}

func newOwnerTmuxTestNode(t *testing.T, status, plain, ansi string, withInbox, withPlugin bool) *ownerTmuxTestNode {
	t.Helper()
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 2*time.Second)
	withSubmitGrace(t, 20*time.Millisecond)
	withSubmitWindow(t, 2*time.Second, 20*time.Millisecond)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))

	transcript := bgTranscript(t, home, home, sidA, "plan")
	var inbox *fakeInbox
	if withInbox {
		inbox = newFakeInbox(t, transcript)
	}
	writeOutsideClaude(t, home, sidA, "cli", status, inbox, map[string]any{"tmux": "orc:@11.%11"})

	pane := &ownerTmuxPane{plain: plain, ansi: ansi}
	pane.confirmRead = func(text string) {
		appendTranscript(t, home, home, sidA, promptLine(home, text, 0))
	}
	n.hosts.Set(append(n.hosts.All(), pane)...)

	var plugin *fakePlugin
	if withPlugin {
		shortPluginWaits(t)
		plugin = startFakePlugin(t, pluginSocket(t, n), sidA, transcript, "submit")
		waitPolling(t, n, sidA)
	}
	f.reset()
	return &ownerTmuxTestNode{n: n, pane: pane, inbox: inbox, plugin: plugin, transcript: transcript}
}

func emptyOwnerTmuxScreens(t *testing.T) (string, string) {
	t.Helper()
	return screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
}

func assertOwnerTmuxTranscript(t *testing.T, path, text string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("last transcript line is not JSON: %v", err)
	}
	message, _ := last["message"].(map[string]any)
	if last["type"] != "user" || message["content"] != text || last["origin"] != nil {
		t.Errorf("the transcript's last line is not the owner's own input %q: %v", text, last)
	}
}

// The verified owner's message is terminal input even when the outside Claude also has an inbox and Persona's plugin.
// It must use the ordinary pane path, including confirmation from the transcript, and an inbox is not required.
func TestTmuxClaudeOwnerSendUsesPane(t *testing.T) {
	for _, c := range []struct {
		name              string
		withInbox, plugin bool
	}{
		{"inbox and plugin available", true, true},
		{"no inbox", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			plain, ansi := emptyOwnerTmuxScreens(t)
			env := newOwnerTmuxTestNode(t, "idle", plain, ansi, c.withInbox, c.plugin)
			const text = "yes, go ahead and merge"
			res, err := request(t, env.n, "session.send", map[string]any{
				"session_id": sidA, "text": text, "client_msg_id": "owner-tmux", "owner_input": true,
			})
			if err != nil {
				t.Fatalf("the owner's tmux send: %v", err)
			}
			if res["ok"] != true || res["delivered_as"] != "user_input" || res["relay_reason"] != nil {
				t.Errorf("owner delivery response = %v, want ok with delivered_as=user_input", res)
			}
			got := env.pane.submitted()
			if len(got) != 1 || got[0].pane != "tmux:%11" || got[0].text != text {
				t.Errorf("pane submissions = %+v, want only the owner's message in tmux:%%11", got)
			}
			if env.inbox != nil && len(env.inbox.users()) != 0 {
				t.Errorf("the peer inbox also received %v", env.inbox.users())
			}
			if env.plugin != nil && len(env.plugin.messages()) != 0 {
				t.Errorf("the plugin also received %v", env.plugin.messages())
			}
			assertOwnerTmuxTranscript(t, env.transcript, text)
		})
	}
}

// A missing owner mark, or one the devices manager could not have verified, keeps the peer route. Without a usable
// inbox such a message is refused: the presence of a tmux pane never authorizes typing somebody else's words.
func TestTmuxClaudeOnlyVerifiedOwnerInputUsesPane(t *testing.T) {
	for _, c := range []struct {
		name      string
		ownerMark map[string]any
		withInbox bool
	}{
		{"unmarked", nil, true},
		{"owner mark with different letter case", map[string]any{"OWNER_INPUT": true}, true},
		{"non-owner without an inbox", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			plain, ansi := emptyOwnerTmuxScreens(t)
			env := newOwnerTmuxTestNode(t, "idle", plain, ansi, c.withInbox, c.withInbox)
			params := map[string]any{"session_id": sidA, "text": "a worker's report", "client_msg_id": "not-owner"}
			for key, value := range c.ownerMark {
				params[key] = value
			}
			res, err := request(t, env.n, "session.send", params)
			if !c.withInbox {
				if err == nil {
					t.Errorf("a non-owner message without an inbox succeeded: %v", res)
				}
			} else {
				if err != nil {
					t.Fatalf("peer delivery: %v", err)
				}
				if res["delivered_as"] != nil || len(env.inbox.users()) != 1 {
					t.Errorf("peer response %v, inbox messages %v", res, env.inbox.users())
				}
				if len(env.plugin.messages()) != 0 {
					t.Errorf("a non-owner message reached the plugin: %v", env.plugin.messages())
				}
			}
			if got := env.pane.submitted(); len(got) != 0 || env.pane.writeCount() != 0 {
				t.Errorf("a non-owner message was typed into tmux: submissions %+v, writes %d", got, env.pane.writeCount())
			}
		})
	}
}

// The owner's route has the same pre-typing checks as any pane session: a draft or a dialog refuses the send before
// text, Enter, the plugin or the peer inbox can take it.
func TestTmuxClaudeOwnerSendUsesPaneSafetyChecks(t *testing.T) {
	for _, c := range []struct {
		name, plain, ansi, code string
	}{
		{"draft", "claude-2.1.312-typed-162.txt", "claude-2.1.312-typed-162.ansi", screen.InputDraft},
		{"dialog", "claude-2.1.312-trust-162.txt", "claude-2.1.312-trust-162.ansi", screen.DialogOpen},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newOwnerTmuxTestNode(t, "idle", screenFixture(t, c.plain), screenFixture(t, c.ansi), true, false)
			res, err := request(t, env.n, "session.send", map[string]any{
				"session_id": sidA, "text": "yes, go ahead and merge", "client_msg_id": "owner-unsafe", "owner_input": true,
			})
			if err == nil {
				t.Errorf("send with a %s in the pane succeeded: %v; want refusal %s", c.name, res, c.code)
			} else if code, data := refusalOf(t, err); code != c.code || data["delivered"] != deliveredNone {
				t.Errorf("refusal = %s %v, want %s with delivered=none", code, data, c.code)
			}
			if got := env.pane.submitted(); len(got) != 0 || env.pane.writeCount() != 0 {
				t.Errorf("typed despite the %s: submissions %+v, writes %d", c.name, got, env.pane.writeCount())
			}
			if len(env.inbox.users()) != 0 {
				t.Errorf("the %s send bypassed the pane through the inbox: %v", c.name, env.inbox.users())
			}
		})
	}
}

// The owner route never types when neither screen read works: an unseen dialog could otherwise take the go-ahead.
func TestTmuxClaudeOwnerSendRefusesUnreadablePane(t *testing.T) {
	plain, ansi := emptyOwnerTmuxScreens(t)
	env := newOwnerTmuxTestNode(t, "idle", plain, ansi, true, false)
	env.pane.readErr = errors.New("capture failed")
	res, err := request(t, env.n, "session.send", map[string]any{
		"session_id": sidA, "text": "yes, go ahead and merge", "client_msg_id": "owner-unread", "owner_input": true,
	})
	if err == nil {
		t.Fatalf("send with an unreadable pane succeeded: %v", res)
	}
	if code, data := refusalOf(t, err); code != notDelivered || data["delivered"] != deliveredNone {
		t.Errorf("refusal = %s %v, want %s with delivered=none", code, data, notDelivered)
	}
	if got := env.pane.submitted(); len(got) != 0 || env.pane.writeCount() != 0 {
		t.Errorf("typed into an unreadable pane: submissions %+v, writes %d", got, env.pane.writeCount())
	}
	if len(env.inbox.users()) != 0 {
		t.Errorf("the unreadable pane send bypassed the route through the inbox: %v", env.inbox.users())
	}
}

// A paste accepted by tmux is not yet user input in the conversation. A post-prompt read failure or a confirmation
// window with no matching transcript row reports delivered=unknown, never delivered_as=user_input.
func TestTmuxClaudeOwnerSendRequiresTranscriptConfirmation(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*ownerTmuxPane)
	}{
		{"post-prompt read failure", func(p *ownerTmuxPane) { p.failReadAfterPrompt = true }},
		{"confirmation window expires", func(p *ownerTmuxPane) { p.confirmRead = func(string) {} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			plain, ansi := emptyOwnerTmuxScreens(t)
			env := newOwnerTmuxTestNode(t, "idle", plain, ansi, true, false)
			c.set(env.pane)
			res, err := request(t, env.n, "session.send", map[string]any{
				"session_id": sidA, "text": "yes, go ahead and merge", "client_msg_id": "owner-unconfirmed", "owner_input": true,
			})
			if err == nil {
				t.Fatalf("an unconfirmed tmux paste succeeded: %v", res)
			}
			if !maybeDelivered(err) {
				t.Errorf("unconfirmed send error = %v, want delivered=unknown", err)
			}
			if got := env.pane.submitted(); len(got) != 1 {
				t.Errorf("pane submissions = %+v, want the one unconfirmed paste", got)
			}
			if len(env.inbox.users()) != 0 {
				t.Errorf("the unconfirmed paste also reached the inbox: %v", env.inbox.users())
			}
		})
	}
}

// Slash commands keep their existing refusal: exitAgent cannot stop an outside Claude and must not record a success.
func TestTmuxClaudeOwnerSendRefusesSlashCommand(t *testing.T) {
	for _, text := range []string{"/exit", "/quit"} {
		t.Run(text, func(t *testing.T) {
			plain, ansi := emptyOwnerTmuxScreens(t)
			env := newOwnerTmuxTestNode(t, "idle", plain, ansi, true, false)
			res, err := request(t, env.n, "session.send", map[string]any{
				"session_id": sidA, "text": text, "client_msg_id": "owner-command", "owner_input": true,
			})
			if err == nil {
				t.Errorf("%s succeeded: %v", text, res)
			} else if code, data := refusalOf(t, err); code != peerCommand || data["delivered"] != deliveredNone {
				t.Errorf("refusal = %s %v, want %s with delivered=none", code, data, peerCommand)
			}
			if got := env.pane.submitted(); len(got) != 0 || env.pane.writeCount() != 0 {
				t.Errorf("%s was typed: submissions %+v, writes %d", text, got, env.pane.writeCount())
			}
			if len(env.inbox.users()) != 0 {
				t.Errorf("%s reached the inbox: %v", text, env.inbox.users())
			}
			if cause := env.n.stopCause(sidA); cause == proto.StopUser {
				t.Errorf("%s recorded a user stop although nothing stopped", text)
			}
		})
	}
}

// A tmux pane itself provides send and terminal capabilities, with or without a peer inbox. Its visible dialog is the
// attention reason, not the computer-only explanation used by a session with no pane.
func TestTmuxClaudeListsSendWithOrWithoutInbox(t *testing.T) {
	old := paneDialogEvery
	paneDialogEvery = 0
	t.Cleanup(func() { paneDialogEvery = old })
	plain := screenFixture(t, "claude-2.1.312-trust-162.txt")
	ansi := screenFixture(t, "claude-2.1.312-trust-162.ansi")
	wantAttention := screen.DialogHead(screen.Plain(plain))
	if wantAttention == "" {
		t.Fatal("the fixture shows no dialog")
	}
	for _, withInbox := range []bool{true, false} {
		t.Run(fmt.Sprintf("inbox=%v", withInbox), func(t *testing.T) {
			env := newOwnerTmuxTestNode(t, "waiting", plain, ansi, withInbox, withInbox)
			var row proto.Session
			waitFor(t, "the tmux pane's dialog", func() bool {
				row = byID(listSessions(t, env.n))[sidA]
				return row.AttentionReason == wantAttention
			})
			if row.PaneID != "tmux:%11" || row.Status != proto.StatusBlocked || row.AttentionReason != wantAttention ||
				row.AttentionReason == proto.AttentionClaudeComputer {
				t.Errorf("tmux dialog row = %+v, want attention %q", row, wantAttention)
			}
			capabilities := row.Capabilities
			if capabilities == nil || !capabilities.Read || !capabilities.Send || !capabilities.Terminal ||
				!capabilities.Approve || !capabilities.Interrupt {
				t.Errorf("tmux capabilities with inbox=%v = %+v, want read, send, terminal, approve, and interrupt", withInbox, capabilities)
			}
			if capabilities != nil && capabilities.PluginInput {
				t.Errorf("a tmux row advertises plugin input although its pane takes the owner's message: %+v", capabilities)
			}
			if row.ReadOnlyReason != "" {
				t.Errorf("tmux row with inbox=%v is read-only: %q", withInbox, row.ReadOnlyReason)
			}
		})
	}
}

// Interrupting an outside tmux Claude sends Escape straight to its tmux pane even when no peer inbox is available.
func TestTmuxClaudeInterruptWithoutInboxSendsEscToPane(t *testing.T) {
	plain, ansi := emptyOwnerTmuxScreens(t)
	env := newOwnerTmuxTestNode(t, "busy", plain, ansi, false, false)
	row := byID(listSessions(t, env.n))[sidA]
	if row.Capabilities == nil || !row.Capabilities.Approve || !row.Capabilities.Interrupt {
		t.Fatalf("busy tmux row without inbox caps = %+v, want approve and interrupt", row.Capabilities)
	}
	if _, err := request(t, env.n, "session.interrupt", map[string]any{"session_id": sidA}); err != nil {
		t.Fatalf("tmux interrupt without inbox: %v", err)
	}
	if keys := env.pane.sentKeys(); len(keys) != 1 || keys[0] != "tmux:%11:esc" {
		t.Fatalf("sent keys = %v, want [tmux:%%11:esc]", keys)
	}
}
