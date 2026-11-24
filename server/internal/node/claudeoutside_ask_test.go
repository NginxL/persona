//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// multiAsk is an AskUserQuestion of four questions, one of them multi-select, the shape Claude Code writes (made-up
// words, the keys in the order the desktop app's Claude Code writes them).
const multiAsk = `{"questions":[` +
	`{"header":"Source","multiSelect":false,"options":[{"description":"The app's own setting","label":"App"},{"description":"The account's","label":"Account"}],"question":"Which setting wins?"},` +
	`{"header":"Folding","multiSelect":false,"options":[{"description":"One line","label":"Fold"},{"description":"All of it","label":"Expand"}],"question":"How are long rows shown?"},` +
	`{"header":"Badges","multiSelect":true,"options":[{"label":"Unread"},{"label":"Mentions"},{"label":"Drafts"}],"question":"Which counts show?"},` +
	`{"header":"Alerts","multiSelect":false,"options":[{"label":"Always"},{"label":"Never"}],"question":"Alert on a mention?"}]}`

func askCall(id, toolUse, input string) string {
	return `{"type":"assistant","uuid":"a-` + toolUse + `","timestamp":"2026-11-04T09:00:09Z","sessionId":"` + id + `","message":{"role":"assistant",` +
		`"content":[{"type":"tool_use","id":"` + toolUse + `","name":"AskUserQuestion","input":` + input + `}]}}`
}

func toolResult(id, toolUse, text string) string {
	return `{"type":"user","uuid":"r-` + toolUse + `","timestamp":"2026-11-04T09:01:00Z","sessionId":"` + id + `","message":{"role":"user",` +
		`"content":[{"type":"tool_result","tool_use_id":"` + toolUse + `","content":"` + text + `"}]}}`
}

// A Claude session in the desktop app (no hook of Persona's runs there) that asks several questions at once: before,
// the apps got only the row's "reply to answer" line and never saw the questions or their options. Now its question is
// a card listed with the Codex cards, every question with its options and multi-select, the row says the apps may
// answer it, and the card's answer goes through the session's inbox at priority now as the user's answers.
func TestAnOutsideClaudesQuestionIsACard(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := modeTranscript(t, home, home, "bypassPermissions")
	appendLines(t, p,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-04T09:00:05Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"A few things first:"}]}}`,
		askCall(sidA, "toolu_multi", multiAsk))
	inbox := newFakeInbox(t, p)
	inbox.setMode(peerModeBypass)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	s := byID(listSessions(t, n))[sidA]
	if s.Status != proto.StatusBlocked || s.Capabilities == nil || !s.Capabilities.Approve || !s.Capabilities.Send {
		t.Fatalf("asking: row %+v caps %+v", s, s.Capabilities)
	}
	cards := n.outsideAskCards()
	if len(cards) != 1 {
		t.Fatalf("cards %+v", cards)
	}
	ix := cards[0]
	if ix.Kind != proto.IxQuestion || ix.SessionID != sidA || ix.Agent != "claude" || ix.ToolName != askTool || ix.ToolUseID != "toolu_multi" ||
		!strings.HasPrefix(ix.ID, outsideAskPrefix) || ix.CanCancel {
		t.Fatalf("card %+v", ix)
	}
	if len(ix.Questions) != 4 || ix.Questions[0].Question != "Which setting wins?" || ix.Questions[0].Header != "Source" ||
		len(ix.Questions[0].Options) != 2 || ix.Questions[0].Options[1].Label != "Account" || ix.Questions[0].Options[1].Description != "The account's" ||
		!ix.Questions[2].MultiSelect || len(ix.Questions[2].Options) != 3 || ix.Questions[3].MultiSelect {
		t.Fatalf("questions %+v", ix.Questions)
	}
	n.syncCodexCards(context.Background())
	if n.cx.sent != ix.ID {
		t.Fatalf("listed %q, want %q", n.cx.sent, ix.ID)
	}

	answers := map[string]string{"Which setting wins?": "Account", "Which counts show?": "Unread, Mentions",
		"How are long rows shown?": "Only the first two lines", "Alert on a mention?": "Never"}
	params := map[string]any{"session_id": sidA, "id": ix.ID, "resolution": proto.Resolution{Answers: answers}, "owner_input": true}
	if _, err := request(t, n, "codex.answer", params); refusalCode(err) != peerHeld {
		t.Fatalf("unmarked answer: %v", err)
	}
	delete(params, "owner_input")
	if _, err := request(t, n, nodeproto.OwnerCardAnswer, params); err != nil {
		t.Fatalf("owner answer: %v", err)
	}
	if h := envelopeHead(t, inbox, 1); h != `<cross-session-message from-name="Persona" from-mode="bypass">` {
		t.Fatalf("owner answer envelope: %q", h)
	}
	pr, content := inboxPriority(t, inbox, 1)
	if pr != "now" {
		t.Fatalf("the answer went at %q", pr)
	}
	for _, want := range []string{"Which setting wins? → Account", "How are long rows shown? → Only the first two lines",
		"Which counts show? → Unread, Mentions", "Alert on a mention? → Never"} {
		if !strings.Contains(content, want) {
			t.Fatalf("the answer has no %q: %q", want, content)
		}
	}
	if strings.Index(content, "Which setting wins?") > strings.Index(content, "Alert on a mention?") {
		t.Fatalf("answers out of the card's order: %q", content)
	}
	n.syncCodexCards(context.Background())
	if n.cx.sent != "" {
		t.Fatalf("an answered card listed again at once: %q", n.cx.sent)
	}

	// Claude Code ends the question when the answer comes in at now: the call has its result, no card is left.
	appendLines(t, p, toolResult(sidA, "toolu_multi", "Tool permission request aborted"))
	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("an ended question is still a card: %+v", cards)
	}
	if err := n.codexAnswer(context.Background(), sidA, ix.ID, proto.Resolution{Answers: answers}); refusalCode(err) != screen.PromptChanged {
		t.Fatalf("an answer to an ended question: %v", err)
	}
}

func TestOutsideAnswerWithTrailingTextUsesNow(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"choose"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-04T09:00:09Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_trailing","name":"AskUserQuestion","input":{"questions":[{"question":"Which one?","header":"Pick","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}},{"type":"text","text":"Please choose one."}]}}`)
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	cards := n.outsideAskCards()
	if len(cards) != 1 {
		t.Fatalf("pending question cards = %d, want 1", len(cards))
	}
	if err := n.codexAnswer(context.Background(), sidA, cards[0].ID, proto.Resolution{Answers: map[string]string{"Which one?": "B"}}); err != nil {
		t.Fatalf("answer returned error: %v", err)
	}
	priority, _ := inboxPriority(t, inbox, 0)
	if priority != peerNow {
		t.Fatalf("card exists and answer returned nil, but priority = %q; the answer is queued behind the open question", priority)
	}
}

// No card where the phone could not answer, or nothing is asked: a claude without an inbox (the computer answers), a
// permission prompt (the latest call is not a question), a question answered on the computer, a busy claude. Handed
// back to the computer, a card stays off the phone while the claude asks it.
func TestAnOutsideClaudesQuestionCardOnlyWhileAnswerable(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"tidy the inbox"}}`,
		askCall(sidA, "toolu_one", `{"questions":[{"question":"Which one?","header":"Pick","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}`))
	inbox := newFakeInbox(t, p)

	writeOutsideClaude(t, home, sidA, "claude-desktop", "waiting", nil, nil) // no inbox
	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("no inbox, yet a card: %+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeComputer {
		t.Fatalf("no inbox: %+v %q", s.Capabilities, s.AttentionReason)
	}

	writeOutsideClaude(t, home, sidA, "claude-desktop", "busy", inbox, nil)
	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("busy, yet a card: %+v", cards)
	}

	writeOutsideClaude(t, home, sidA, "claude-desktop", "waiting", inbox, nil)
	cards := n.outsideAskCards()
	if len(cards) != 1 {
		t.Fatalf("cards %+v", cards)
	}
	id := cards[0].ID
	if err := n.codexAnswer(context.Background(), sidA, id, proto.Resolution{Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	n.syncCodexCards(context.Background())
	if n.cx.sent != "" {
		t.Fatalf("handed back, yet listed: %q", n.cx.sent)
	}
	if len(inbox.users()) != 0 {
		t.Fatalf("a hand-back sent something: %v", inbox.users())
	}
	if err := n.codexAnswer(context.Background(), sidA, id, proto.Resolution{Cancel: true}); err == nil {
		t.Fatal("Skip went through")
	}
	if err := n.codexAnswer(context.Background(), sidA, id, proto.Resolution{Answers: map[string]string{"Which one?": "  "}}); err == nil {
		t.Fatal("an empty answer went through")
	}

	// Answered on the computer, then a permission prompt: no card for either.
	appendLines(t, p, toolResult(sidA, "toolu_one", "A"),
		`{"type":"assistant","uuid":"a3","timestamp":"2026-11-04T09:02:00Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{"command":"ls"}}]}}`)
	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("a permission prompt is a card: %+v", cards)
	}
	forgetMetas(n)
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || s.Capabilities.Approve {
		t.Fatalf("a permission prompt can be approved from the phone: %+v", s.Capabilities)
	}
}

// The answer message lists only the questions answered, in the card's order, and says so when the assistant answered.
func TestOutsideAnswerText(t *testing.T) {
	ix := proto.Interaction{Questions: []proto.Question{{Question: "First\nline?"}, {Question: "Second?"}}}
	if got := outsideAnswerText(ix, proto.Resolution{Answers: map[string]string{"Second?": "yes", "First\nline?": " "}}); !strings.Contains(got, "Second? → yes") || strings.Contains(got, "First") {
		t.Fatalf("%q", got)
	}
	if got := outsideAnswerText(ix, proto.Resolution{}); got != "" {
		t.Fatalf("no answers: %q", got)
	}
	got := outsideAnswerText(ix, proto.Resolution{Answers: map[string]string{"First\nline?": "no"}, AnsweredBy: proto.AnsweredByAssistant, Reason: "your rule"})
	if !strings.Contains(got, "First line? → no") || !strings.Contains(got, "your rule") {
		t.Fatalf("by the assistant: %q", got)
	}
}

// Trailing assistant KindText clears quickMeta.LastTool, yet outsideAskCards still creates a clq_ card. claudeSessions
// and plain chat replies must use the same pendingOutsideAsk source of truth so the card stays interactive and chat
// replies still go at priority now.
func TestOutsideAskTrailingTextKeepsApproveAndAttention(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"choose"}}`,
		askCall(sidA, "toolu_trailing_text", `{"questions":[{"question":"Which one?","header":"Pick","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}`),
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-04T09:00:10Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"Please pick one."}]}}`)
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if m := n.metaNow(p, st); m.tool != "" {
		t.Fatalf("precondition failed: expected KindText to clear quickMeta.tool, got %q", m.tool)
	}

	s := byID(listSessions(t, n))[sidA]
	if s.Status != proto.StatusBlocked || s.AttentionReason != proto.AttentionClaudeAnswer ||
		s.Capabilities == nil || !s.Capabilities.Approve || !s.Capabilities.Send {
		t.Fatalf("trailing text after AskUserQuestion: row=%+v caps=%+v", s, s.Capabilities)
	}
	if cards := n.outsideAskCards(); len(cards) != 1 {
		t.Fatalf("outsideAskCards = %+v, want 1 card", cards)
	}
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "A", "client_msg_id": "m-trailing"}); err != nil {
		t.Fatalf("plain reply: %v", err)
	}
	if pr, _ := inboxPriority(t, inbox, 0); pr != peerNow {
		t.Fatalf("plain reply priority = %q, want %q", pr, peerNow)
	}
}

// Within metaMinInterval (3s), n.meta returns the cached quickMeta from before AskUserQuestion was appended.
// claudeSessions must still set Capabilities.Approve and AttentionClaudeAnswer immediately to match outsideAskCards.
func TestOutsideAskWithinMetaMinIntervalKeepsApprove(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"plan"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-04T09:00:05Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"Thinking through options."}]}}`)
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "busy", inbox, nil)

	// Warm n.metas within metaMinInterval with m.tool == "".
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || s.Capabilities.Approve {
		t.Fatalf("busy warm-up: caps=%+v, want Approve=false", s.Capabilities)
	}

	// Immediately append AskUserQuestion and flip to waiting without clearing n.metas.
	appendLines(t, p, askCall(sidA, "toolu_cached", `{"questions":[{"question":"Proceed?","header":"Confirm","options":[{"label":"Yes"},{"label":"No"}],"multiSelect":false}]}`))
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if m := n.meta(p, st); m.tool != "" {
		t.Fatalf("precondition failed: expected cached quickMeta.tool to stay empty within metaMinInterval, got %q", m.tool)
	}

	s := byID(listSessions(t, n))[sidA]
	if s.Status != proto.StatusBlocked || s.AttentionReason != proto.AttentionClaudeAnswer ||
		s.Capabilities == nil || !s.Capabilities.Approve {
		t.Fatalf("within metaMinInterval: row=%+v caps=%+v", s, s.Capabilities)
	}
	if cards := n.outsideAskCards(); len(cards) != 1 {
		t.Fatalf("outsideAskCards = %+v, want 1 card", cards)
	}
}

// When trailing data pushes AskUserQuestion beyond quickTail (384 KB), quickMeta loses LastTool while PageTurns still
// creates a clq_ card. claudeSessions must still set Capabilities.Approve and AttentionClaudeAnswer.
func TestOutsideAskPastQuickTailKeepsApprove(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	bigPad := strings.Repeat("x", quickTail+64<<10)
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"audit"}}`,
		askCall(sidA, "toolu_bigtail", `{"questions":[{"question":"Target?","header":"Scope","options":[{"label":"All"},{"label":"Changed"}],"multiSelect":false}]}`),
		`{"type":"progress","uuid":"p1","timestamp":"2026-11-04T09:00:10Z","sessionId":"`+sidA+`","data":{"blob":"`+bigPad+`"}}`)
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if m := n.metaNow(p, st); m.tool != "" {
		t.Fatalf("precondition failed: expected quickTail truncation to leave quickMeta.tool empty, got %q", m.tool)
	}

	s := byID(listSessions(t, n))[sidA]
	if s.Status != proto.StatusBlocked || s.AttentionReason != proto.AttentionClaudeAnswer ||
		s.Capabilities == nil || !s.Capabilities.Approve {
		t.Fatalf("past quickTail: row=%+v caps=%+v", s, s.Capabilities)
	}
	if cards := n.outsideAskCards(); len(cards) != 1 {
		t.Fatalf("outsideAskCards = %+v, want 1 card", cards)
	}
}

type outsideWireEvent struct {
	kind     string
	sessions []proto.Session
	cards    []proto.Interaction
}

type outsideHubRecorder struct {
	mu     sync.Mutex
	events []outsideWireEvent
}

func (r *outsideHubRecorder) snapshot() []outsideWireEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]outsideWireEvent(nil), r.events...)
}

func (r *outsideHubRecorder) clear() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

func wireOutsideHub(t *testing.T, n *Node, onRequest func(ctx context.Context, method string, params json.RawMessage) (any, error)) *outsideHubRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	rec := &outsideHubRecorder{}
	hub.OnEvent = func(event string, params json.RawMessage) {
		switch event {
		case "sessions.changed":
			var p struct {
				Sessions []proto.Session `json:"sessions"`
			}
			if json.Unmarshal(params, &p) == nil {
				rec.mu.Lock()
				rec.events = append(rec.events, outsideWireEvent{kind: event, sessions: p.Sessions})
				rec.mu.Unlock()
			}
		case "codex.interactions":
			var p struct {
				Interactions []proto.Interaction `json:"interactions"`
			}
			if json.Unmarshal(params, &p) == nil {
				rec.mu.Lock()
				rec.events = append(rec.events, outsideWireEvent{kind: event, cards: p.Interactions})
				rec.mu.Unlock()
			}
		}
	}
	if onRequest != nil {
		hub.OnRequest = onRequest
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() {
		waitFor(t, "listPush idle", func() bool {
			n.listPush.mu.Lock()
			defer n.listPush.mu.Unlock()
			return !n.listPush.building
		})
		cancel()
		_ = n2hW.Close()
		_ = h2nW.Close()
		_ = n2hR.Close()
		_ = h2nR.Close()
	})
	return rec
}

// Adding or removing a clq_ card in syncCodexCards immediately pokes/publishes sessions.changed in the right order
// (Approve=true before codex.interactions on add; codex.interactions before Approve=false on remove), and a transient
// status flicker while a clq_ card is still published never drops Capabilities.Approve.
func TestOutsideAskCardChangesPokeSessionsAndSurviveFlicker(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	n.events = newHerdrEvents()
	rec := wireOutsideHub(t, n, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"work"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-04T09:00:05Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`)
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "busy", inbox, nil)

	// Publish initial baseline (Approve=false).
	n.publish(n.sessionList(context.Background(), false))
	waitFor(t, "baseline sessions.changed", func() bool { return len(rec.snapshot()) >= 1 })
	rec.clear()
	n.events.drained()

	// New AskUserQuestion appears -> syncCodexCards must poke and push Approve=true BEFORE codex.interactions.
	appendLines(t, p, askCall(sidA, "toolu_poke", `{"questions":[{"question":"Ship now?","header":"Release","options":[{"label":"Yes"},{"label":"Later"}],"multiSelect":false}]}`))
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	n.syncCodexCards(context.Background())

	if !n.events.drained() {
		t.Fatal("adding clq_ card did not poke session watcher")
	}
	waitFor(t, "add events on wire", func() bool { return len(rec.snapshot()) >= 2 })
	evs := rec.snapshot()
	if evs[0].kind != "sessions.changed" || evs[1].kind != "codex.interactions" {
		t.Fatalf("add event order = [%s, %s], want [sessions.changed, codex.interactions]", evs[0].kind, evs[1].kind)
	}
	sAdd := byID(evs[0].sessions)[sidA]
	if sAdd.Capabilities == nil || !sAdd.Capabilities.Approve || sAdd.AttentionReason != proto.AttentionClaudeAnswer {
		t.Fatalf("sessions.changed on add: row=%+v caps=%+v", sAdd, sAdd.Capabilities)
	}
	if len(evs[1].cards) != 1 || !strings.HasPrefix(evs[1].cards[0].ID, outsideAskPrefix) {
		t.Fatalf("codex.interactions on add: %+v", evs[1].cards)
	}

	// Transient status flicker while clq_ card is still published on hub must keep Approve=true.
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "busy", inbox, nil)
	if sFlicker := byID(listSessions(t, n))[sidA]; sFlicker.Capabilities == nil || !sFlicker.Capabilities.Approve {
		t.Fatalf("status flicker while clq_ published dropped Approve: caps=%+v", sFlicker.Capabilities)
	}

	// Question ends and transitions to a computer-only Bash permission prompt -> syncCodexCards withdraws clq_ BEFORE
	// pushing Approve=false and AttentionClaudeComputer.
	rec.clear()
	n.events.drained()
	appendLines(t, p, toolResult(sidA, "toolu_poke", "Yes"),
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-04T09:02:00Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{"command":"ls"}}]}}`)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	n.syncCodexCards(context.Background())

	if !n.events.drained() {
		t.Fatal("removing clq_ card did not poke session watcher")
	}
	waitFor(t, "remove events on wire", func() bool { return len(rec.snapshot()) >= 2 })
	evs = rec.snapshot()
	if evs[0].kind != "codex.interactions" || evs[1].kind != "sessions.changed" {
		t.Fatalf("remove event order = [%s, %s], want [codex.interactions, sessions.changed]", evs[0].kind, evs[1].kind)
	}
	if len(evs[0].cards) != 0 {
		t.Fatalf("codex.interactions on remove = %+v, want empty", evs[0].cards)
	}
	sRem := byID(evs[1].sessions)[sidA]
	if sRem.Capabilities == nil || sRem.Capabilities.Approve || sRem.AttentionReason != proto.AttentionClaudeComputer {
		t.Fatalf("sessions.changed on remove: row=%+v caps=%+v", sRem, sRem.Capabilities)
	}
}

// A Claude session with an active blocking hook interaction (interaction.create in flight) that falls back into
// claudeSessions must keep Capabilities.Approve=true (even without a peer inbox), avoid AttentionClaudeComputer and
// duplicate clq_ cards, and restore Capabilities.Approve=false once the hook call finishes.
func TestFallbackHookSessionKeepsApproveWhileHookActive(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	n.events = newHerdrEvents()
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))

	started := make(chan struct{})
	release := make(chan struct{})
	rec := wireOutsideHub(t, n, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "interaction.create" {
			close(started)
			<-release
			allow := true
			return proto.Resolution{Allow: &allow}, nil
		}
		return map[string]any{"ok": true}, nil
	})

	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"run build"}}`,
		askCall(sidA, "toolu_hook_ask", `{"questions":[{"question":"Mode?","header":"Run","options":[{"label":"Fast"},{"label":"Full"}],"multiSelect":false}]}`))
	inbox := newFakeInbox(t, p)

	// Start with no inbox to verify active hook approval works without peer inbox, and establish it as outside-listed.
	writeOutsideClaude(t, home, sidA, "cli", "waiting", nil, nil)
	n.publish(n.sessionList(context.Background(), false))
	waitFor(t, "initial fallback sessions.changed", func() bool { return len(rec.snapshot()) >= 1 })
	if s := byID(rec.snapshot()[0].sessions)[sidA]; s.Capabilities == nil || s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeComputer {
		t.Fatalf("before hook: row=%+v caps=%+v", s, s.Capabilities)
	}
	rec.clear()

	hookDone := make(chan error, 1)
	rawIn, _ := json.Marshal(hookInput{
		HookEventName: "PermissionRequest",
		SessionID:     sidA,
		Cwd:           home,
		ToolName:      "Bash",
		ToolUseID:     "toolu_hook_bash",
		ToolInput:     json.RawMessage(`{"command":"go test ./..."}`),
	})
	go func() {
		_, err := n.handleHook(context.Background(), sockRequest{Kind: "hook", SessionID: sidA, Input: rawIn})
		hookDone <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for hook interaction.create")
	}

	// Entering activeAsks pushed sessions.changed with Approve=true and empty AttentionReason before interaction.create.
	waitFor(t, "approve pushed for active hook", func() bool {
		for _, ev := range rec.snapshot() {
			if ev.kind == "sessions.changed" {
				s := byID(ev.sessions)[sidA]
				if s.Capabilities != nil && s.Capabilities.Approve && s.AttentionReason == "" {
					return true
				}
			}
		}
		return false
	})

	// An unrelated Bash permission hook on the same session must NOT hide an open AskUserQuestion clq_ card.
	writeOutsideClaude(t, home, sidA, "cli", "waiting", inbox, nil)
	if cards := n.outsideAskCards(); len(cards) != 1 || cards[0].ToolUseID != "toolu_hook_ask" {
		t.Fatalf("unrelated Bash hook hid open AskUserQuestion clq_ card: %+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != "" {
		t.Fatalf("while hook active: row=%+v caps=%+v", s, s.Capabilities)
	}

	// Only an active AskUserQuestion hook covering the same tool_use_id suppresses the duplicate clq_ card, while a
	// different tool_use_id stays visible.
	_, leaveOtherAsk := n.activeAsks.enter(proto.Interaction{SessionID: sidA, Kind: proto.IxQuestion, ToolName: askTool, ToolUseID: "toolu_other_ask"})
	if cards := n.outsideAskCards(); len(cards) != 1 || cards[0].ToolUseID != "toolu_hook_ask" {
		leaveOtherAsk()
		t.Fatalf("AskUserQuestion hook for a different tool_use_id hid clq_ card: %+v", cards)
	}
	leaveOtherAsk()

	_, leaveSameAsk := n.activeAsks.enter(proto.Interaction{SessionID: sidA, Kind: proto.IxQuestion, ToolName: askTool, ToolUseID: "toolu_hook_ask"})
	if cards := n.outsideAskCards(); len(cards) != 0 {
		leaveSameAsk()
		t.Fatalf("AskUserQuestion hook for the same tool_use_id emitted duplicate clq_ card: %+v", cards)
	}
	leaveSameAsk()

	// Switch back to no-inbox before releasing so post-hook state cleanly reverts to Approve=false + computer-only.
	writeOutsideClaude(t, home, sidA, "cli", "waiting", nil, nil)
	rec.clear()
	close(release)
	if err := <-hookDone; err != nil {
		t.Fatalf("handleHook returned error: %v", err)
	}

	waitFor(t, "approve cleared after hook ends", func() bool {
		for _, ev := range rec.snapshot() {
			if ev.kind == "sessions.changed" {
				s := byID(ev.sessions)[sidA]
				if s.Capabilities != nil && !s.Capabilities.Approve && s.AttentionReason == proto.AttentionClaudeComputer {
					return true
				}
			}
		}
		return false
	})
}

// A session that starts its blocking hook while still in a herdr pane, then temporarily drops out of herdr into
// claudeSessions while two concurrent hook asks are in flight, keeps Approve=true until both asks finish.
func TestHookSessionFallingBackMidHookKeepsApproveUntilAllAsksFinish(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	n.events = newHerdrEvents()
	f.reply("session.snapshot", snapshot(nil, nil, []any{
		claudeAgent(sidA, proto.StatusBlocked, "w1:p1", "w1:t1", "w1", home, "build"),
	}, nil))

	started := make(chan string, 2)
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	rec := wireOutsideHub(t, n, func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		if method != "interaction.create" {
			return map[string]any{"ok": true}, nil
		}
		var p struct {
			Interaction proto.Interaction `json:"interaction"`
		}
		_ = json.Unmarshal(raw, &p)
		started <- p.Interaction.ToolUseID
		if p.Interaction.ToolUseID == "toolu_mid_1" {
			<-release1
		} else {
			<-release2
		}
		allow := true
		return proto.Resolution{Allow: &allow}, nil
	})

	bgTranscript(t, home, home, sidA, "build")
	writeOutsideClaude(t, home, sidA, "cli", "waiting", nil, nil)

	// Initially listed by herdr pane (not claudeSessions).
	if s := byID(listSessions(t, n))[sidA]; s.SourceKind != "" {
		t.Fatalf("expected herdr pane initially, got %+v", s)
	}

	hookDone := make(chan error, 2)
	for _, toolID := range []string{"toolu_mid_1", "toolu_mid_2"} {
		rawIn, _ := json.Marshal(hookInput{
			HookEventName: "PermissionRequest",
			SessionID:     sidA,
			Cwd:           home,
			ToolName:      "Bash",
			ToolUseID:     toolID,
			ToolInput:     json.RawMessage(`{"command":"echo hi"}`),
		})
		go func(input json.RawMessage) {
			_, err := n.handleHook(context.Background(), sockRequest{Kind: "hook", SessionID: sidA, Input: input})
			hookDone <- err
		}(rawIn)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent hook interaction.create calls")
		}
	}

	// Herdr drops the pane mid-hook -> session falls back to claudeSessions.
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	s := byID(listSessions(t, n))[sidA]
	if s.SourceKind != proto.SourceClaudeTerminal || s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != "" {
		t.Fatalf("mid-hook fallback row=%+v caps=%+v", s, s.Capabilities)
	}

	// Completing the first hook must keep Approve=true while the second hook is still active.
	close(release1)
	if err := <-hookDone; err != nil {
		t.Fatalf("first hook error: %v", err)
	}
	s = byID(listSessions(t, n))[sidA]
	if s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != "" {
		t.Fatalf("after first of two hooks finished: row=%+v caps=%+v", s, s.Capabilities)
	}

	// Completing the second hook clears Approve=false and pushes sessions.changed.
	rec.clear()
	close(release2)
	if err := <-hookDone; err != nil {
		t.Fatalf("second hook error: %v", err)
	}
	waitFor(t, "approve cleared after last concurrent hook", func() bool {
		for _, ev := range rec.snapshot() {
			if ev.kind == "sessions.changed" {
				s := byID(ev.sessions)[sidA]
				if s.Capabilities != nil && !s.Capabilities.Approve && s.AttentionReason == proto.AttentionClaudeComputer {
					return true
				}
			}
		}
		return false
	})
}

// An unanswered AskUserQuestion from an earlier turn must NOT survive into a newer turn started by a user prompt:
// otherwise a waiting dialog in the new turn would emit a stale clq_ card and send ordinary replies at peerNow
// (declining the dialog on the computer). A queued user message in the same turn keeps the open question intact.
func TestOutsideAskResetsOnNewUserTurnButSurvivesQueuedInput(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"first turn"}}`,
		askCall(sidA, "toolu_old_turn", `{"questions":[{"question":"Pick?","header":"Old","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}`),
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-04T09:00:12Z","sessionId":"`+sidA+`","content":"queued note while asking"}`)
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	// Queued input does not end the current turn: the question stays open.
	if cards := n.outsideAskCards(); len(cards) != 1 || cards[0].ToolUseID != "toolu_old_turn" {
		t.Fatalf("queued message cleared open question: %+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeAnswer {
		t.Fatalf("with queued note: row=%+v caps=%+v", s, s.Capabilities)
	}

	// A new user prompt starts a fresh turn without writing a tool_result for the earlier turn's AskUserQuestion.
	appendLines(t, p, `{"type":"user","uuid":"u2","timestamp":"2026-11-04T09:01:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"start a different task"}}`)
	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("stale previous-turn AskUserQuestion produced card in new turn: %+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeComputer {
		t.Fatalf("new turn waiting on another dialog: row=%+v caps=%+v", s, s.Capabilities)
	}
	res, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "hold on", "client_msg_id": "m-new-turn"})
	if err != nil {
		t.Fatalf("send in new turn: %v", err)
	}
	if pr, _ := inboxPriority(t, inbox, 0); pr != peerNext || res["waits_on_computer"] != true {
		t.Fatalf("reply in new turn went at %q (res=%v), want %q with waits_on_computer=true", pr, res, peerNext)
	}
}

// pendingOutsideAsk must read the transcript directly (bounded read-only) without acquiring subscriber pushLock or
// triggering watch pushes, so slow-link watch catch-ups never stall outsideAskCards, listSessions, outsideAnswer, or
// sendOutside.
func TestOutsideAskDoesNotBlockOnSubscriberPushLock(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"choose"}}`,
		askCall(sidA, "toolu_nolock", `{"questions":[{"question":"Proceed?","header":"Check","options":[{"label":"Yes"},{"label":"No"}],"multiSelect":false}]}`))
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	// Simulate an active watch holding pushLock(sidA) during a slow-link catch-up.
	n.mu.Lock()
	n.watches[sidA] = &watch{}
	n.mu.Unlock()
	pushMu := n.pushLock(sidA)
	pushMu.Lock()
	defer pushMu.Unlock()

	errCh := make(chan string, 1)
	go func() {
		cards := n.outsideAskCards()
		if len(cards) != 1 || cards[0].ToolUseID != "toolu_nolock" {
			errCh <- "outsideAskCards missed toolu_nolock"
			return
		}
		if s := byID(n.sessionList(context.Background(), false))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeAnswer {
			errCh <- "sessionList lost Approve or AttentionClaudeAnswer while pushLock held"
			return
		}
		if handled, err := n.sendOutside(context.Background(), sidA, "Yes", nil); !handled || err != nil {
			errCh <- "sendOutside failed while pushLock held"
			return
		}
		if err := n.outsideAnswer(context.Background(), sidA, cards[0].ID, proto.Resolution{Answers: map[string]string{"Proceed?": "Yes"}}); err != nil {
			errCh <- "outsideAnswer failed while pushLock held"
			return
		}
		errCh <- ""
	}()
	select {
	case msg := <-errCh:
		if msg != "" {
			t.Fatal(msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("outside ask paths blocked on subscriber pushLock")
	}
	if pr, _ := inboxPriority(t, inbox, 0); pr != peerNow {
		t.Fatalf("sendOutside priority = %q, want %q", pr, peerNow)
	}
	if pr, _ := inboxPriority(t, inbox, 1); pr != peerNow {
		t.Fatalf("outsideAnswer priority = %q, want %q", pr, peerNow)
	}
}

// Even when pollAgents has already updated n.lastList before writing sessions.changed to the wire, syncCodexCards must
// still flush sessions.changed (Approve=true) onto the wire strictly before codex.interactions, and a stale pollAgents
// snapshot captured before outsideListGen advanced must not overwrite the flushed Approve=true list.
func TestOutsideAskFlushesApproveEvenWhenLastListPreUpdated(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	n.events = newHerdrEvents()
	rec := wireOutsideHub(t, n, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"work"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-04T09:00:05Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`)
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "busy", inbox, nil)

	// Capture a stale pollAgents snapshot (Approve=false) before the question appears, and publish baseline.
	staleGen := n.outsideListGen.Load()
	staleList := n.sessionList(context.Background(), false)
	n.publish(staleList)
	waitFor(t, "baseline list", func() bool { return len(rec.snapshot()) >= 1 })
	rec.clear()

	// Append AskUserQuestion and simulate pollAgents updating n.lastList with Approve=true BEFORE writing to the wire.
	appendLines(t, p, askCall(sidA, "toolu_wireorder", `{"questions":[{"question":"Go?","header":"Run","options":[{"label":"Yes"},{"label":"No"}],"multiSelect":false}]}`))
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	preList := n.sessionList(context.Background(), false)
	n.mu.Lock()
	n.lastList = listKey(preList)
	n.mu.Unlock()

	n.syncCodexCards(context.Background())

	waitFor(t, "ordered add events on wire", func() bool { return len(rec.snapshot()) >= 2 })
	evs := rec.snapshot()
	if evs[0].kind != "sessions.changed" || evs[1].kind != "codex.interactions" {
		t.Fatalf("wire order with pre-updated lastList = [%s, %s], want [sessions.changed, codex.interactions]", evs[0].kind, evs[1].kind)
	}
	if s := byID(evs[0].sessions)[sidA]; s.Capabilities == nil || !s.Capabilities.Approve {
		t.Fatalf("flushed sessions.changed missing Approve=true: %+v", s.Capabilities)
	}

	// A stale pollAgents snapshot captured at staleGen must be dropped rather than overwriting Approve=true.
	rec.clear()
	n.publishGen(staleList, staleGen)
	if len(rec.snapshot()) != 0 {
		t.Fatalf("stale pollAgents snapshot overwrote flushed list: %+v", rec.snapshot())
	}
}

// Rewound (abandoned) branches in a Claude transcript must be excluded by branch-aware projection, even if an
// unanswered AskUserQuestion was left on the rewound branch.
func TestOutsideAskIgnoresRewoundBranch(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	// u1 -> a1 (replied anchor), then abandoned branch u2 (parent a1) -> a2 (AskUserQuestion), then rewound active
	// branch u3 (sibling of u2 under parent a1) -> a3 (text only), followed by a late abandoned line attached to a2.
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"start"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-11-04T09:00:02Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"ready"}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","timestamp":"2026-11-04T09:00:05Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"old path"}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"u2","timestamp":"2026-11-04T09:00:06Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"thinking"}]}}`,
		`{"type":"user","uuid":"u3","parentUuid":"a1","timestamp":"2026-11-04T09:00:10Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"rewound path"}}`,
		`{"type":"assistant","uuid":"a3","parentUuid":"u3","timestamp":"2026-11-04T09:00:11Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"text","text":"running new path"}]}}`,
		`{"type":"assistant","uuid":"a2b","parentUuid":"a2","timestamp":"2026-11-04T09:00:12Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_abandoned","name":"AskUserQuestion","input":{"questions":[{"question":"Abandoned?","header":"Old","options":[{"label":"Yes"},{"label":"No"}],"multiSelect":false}]}}]}}`)
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("abandoned branch AskUserQuestion emitted card: %+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeComputer {
		t.Fatalf("abandoned branch set Approve: row=%+v caps=%+v", s, s.Capabilities)
	}
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "check", "client_msg_id": "m-rewound"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if pr, _ := inboxPriority(t, inbox, 0); pr != peerNext {
		t.Fatalf("send after abandoned branch ask went at %q, want %q", pr, peerNext)
	}
}

// A concurrent tool_result returning an image carries ToolUseID != "" and must NOT reset an open AskUserQuestion in the
// same turn, whereas a genuine user image prompt (ToolUseID == "") starts a new turn and clears it.
func TestOutsideAskToolResultImageDoesNotResetOpenAsk(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"inspect"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-04T09:00:02Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_shot","name":"Bash","input":{"command":"screenshot"}},{"type":"tool_use","id":"toolu_ask_img","name":"AskUserQuestion","input":{"questions":[{"question":"Looks right?","header":"Visual","options":[{"label":"Yes"},{"label":"No"}],"multiSelect":false}]}}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-11-04T09:00:04Z","sessionId":"`+sidA+`","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_shot","content":[{"type":"text","text":"captured"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+tinyPNG+`"}}]}]}}`)
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	if cards := n.outsideAskCards(); len(cards) != 1 || cards[0].ToolUseID != "toolu_ask_img" {
		t.Fatalf("tool_result screenshot cleared open AskUserQuestion: %+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeAnswer {
		t.Fatalf("after tool_result screenshot: row=%+v caps=%+v", s, s.Capabilities)
	}

	// A genuine user image prompt (ToolUseID == "") begins a new turn and clears the earlier unanswered AskUserQuestion.
	appendLines(t, p, `{"type":"user","uuid":"u2","timestamp":"2026-11-04T09:00:10Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+tinyPNG+`"}}]}}`)
	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("user image prompt did not clear earlier AskUserQuestion: %+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeComputer {
		t.Fatalf("after user image prompt: row=%+v caps=%+v", s, s.Capabilities)
	}
}

// Even when more than 8 MiB (lookBackReach) of trailing metadata lines sit after AskUserQuestion, the private read-only
// transcript.File still recovers the open question without touching subscriber pushLock.
func TestOutsideAskBeyond8MiBTrailingMetadata(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"deep"}}`,
		askCall(sidA, "toolu_deep_8mb", `{"questions":[{"question":"Continue?","header":"Deep","options":[{"label":"Yes"},{"label":"No"}],"multiSelect":false}]}`))

	// Append ~8.5 MiB of progress metadata lines (> lookBackReach = 8 MiB).
	chunk := `{"type":"progress","uuid":"p1","timestamp":"2026-11-04T09:00:10Z","sessionId":"` + sidA + `","data":{"blob":"` + strings.Repeat("z", 512<<10) + `"}}` + "\n"
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for range 17 {
		if _, err := fh.WriteString(chunk); err != nil {
			_ = fh.Close()
			t.Fatal(err)
		}
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}

	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	pushMu := n.pushLock(sidA)
	pushMu.Lock()
	defer pushMu.Unlock()

	if cards := n.outsideAskCards(); len(cards) != 1 || cards[0].ToolUseID != "toolu_deep_8mb" {
		t.Fatalf(">8 MiB trailing metadata hid AskUserQuestion: %+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeAnswer {
		t.Fatalf(">8 MiB trailing metadata row=%+v caps=%+v", s, s.Capabilities)
	}
}

// A transient open/read error must not be cached as a negative result: once the file is readable again with the same
// stat, the open AskUserQuestion card is returned immediately.
func TestOutsideAskDoesNotCacheTransientReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses chmod 0000 read permissions")
	}
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"check"}}`,
		askCall(sidA, "toolu_transient_err", `{"questions":[{"question":"Retry?","header":"Check","options":[{"label":"Yes"},{"label":"No"}],"multiSelect":false}]}`))
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })

	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("unreadable file unexpectedly returned cards: %+v", cards)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if cards := n.outsideAskCards(); len(cards) != 1 || cards[0].ToolUseID != "toolu_transient_err" {
		t.Fatalf("transient read error was cached: cards=%+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeAnswer {
		t.Fatalf("after file readable again: row=%+v caps=%+v", s, s.Capabilities)
	}
}

// Atomically replacing a transcript with another file of identical size and mtime (same path, different inode) must
// invalidate outsideAskCache via os.SameFile.
func TestOutsideAskCacheDetectsSameSizeSameMtimeAtomicReplace(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))

	askContent := `{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"` + home + `","sessionId":"` + sidA + `","message":{"role":"user","content":"go"}}` + "\n" +
		askCall(sidA, "toolu_replaced", `{"questions":[{"question":"Ship?","header":"Go","options":[{"label":"Yes"},{"label":"No"}],"multiSelect":false}]}`) + "\n"
	bashBase := `{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"` + home + `","sessionId":"` + sidA + `","message":{"role":"user","content":"go"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-04T09:00:09Z","sessionId":"` + sidA + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_bash_pad","name":"Bash","input":{"command":"`
	bashTail := `"}}]}}` + "\n"
	padLen := len(askContent) - len(bashBase) - len(bashTail)
	if padLen < 1 {
		t.Fatalf("unexpected fixture lengths: ask=%d bash=%d", len(askContent), len(bashBase)+len(bashTail))
	}
	bashContent := bashBase + strings.Repeat("a", padLen) + bashTail
	if len(bashContent) != len(askContent) {
		t.Fatalf("fixture size mismatch: %d vs %d", len(bashContent), len(askContent))
	}

	p := writeTranscript(t, home, home, sidA, time.Now())
	fixedTime := time.Date(2026, 11, 4, 9, 5, 0, 0, time.UTC)
	if err := os.WriteFile(p, []byte(bashContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	// Warm outsideAskCache with negative result (Bash call).
	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("bash call produced card: %+v", cards)
	}

	// Atomically replace p with a new inode having identical byte length and identical mtime containing AskUserQuestion.
	other := filepath.Join(filepath.Dir(p), "replacement.jsonl")
	if err := os.WriteFile(other, []byte(askContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, p); err != nil {
		t.Fatal(err)
	}

	if cards := n.outsideAskCards(); len(cards) != 1 || cards[0].ToolUseID != "toolu_replaced" {
		t.Fatalf("same-size same-mtime atomic replacement hit stale cache: %+v", cards)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudeAnswer {
		t.Fatalf("after atomic replacement: row=%+v caps=%+v", s, s.Capabilities)
	}
}
