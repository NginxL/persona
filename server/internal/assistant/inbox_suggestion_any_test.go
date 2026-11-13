package assistant

// "Say anything on an event with no suggestion" (DECISIONS B-3): POST /api/v1/inbox/{id}/suggestion with
// {"version":0,"action":"instruct","text":"…"} on an open event with no current suggestion records the words, moves the
// event back to looking, and hands the work to its processing conversation (a new one, within the concurrency limit,
// when it has none); the disabled AI twin returns it to Needs you with the reason. Everything else stays stale.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// sayAnythingHub is a hub with one Feishu DM and nothing else in the inbox.
func sayAnythingHub(t *testing.T) (*inboxHub, *extensionLark) {
	t.Helper()
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	lark := &extensionLark{}
	h.addConnector(lark, true)
	return h, lark
}

// sayItem is an open Needs you event, id sayAnything:0, with no suggestion and no queued work. state is its handling
// state ("" for none); withConversation binds s2 as its processing conversation.
func sayItem(t *testing.T, h *inboxHub, id, state string, withConversation bool) *InboxItem {
	t.Helper()
	now := time.Now()
	it := &InboxItem{ID: id, Source: "lark", Kind: KindDM, Account: "feishu", Title: "张三", Sender: "张三",
		Last: "200", Count: 1, StartedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute), Tier: TierAction, TriagedAt: "200",
		Summary: "张三问报价", Context: "张三：周五前能给报价吗？", Lark: &InboxLark{AccountID: 1, FeedID: "f1", StartPosition: "200"}}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	it.Agent = &InboxAgent{Of: it.Last}
	if state != "" {
		h.inbox.setHandling(it, state, "", now)
	}
	if withConversation {
		it.Agent.State, it.Agent.MachineID, it.Agent.SessionID = AgentRunning, "server", "s2"
		it.Handling.MachineID, it.Handling.SessionID = "server", "s2"
	}
	h.inbox.mu.Unlock()
	if withConversation {
		h.f.setSessions("server", proto.Session{SessionID: "s2", Status: proto.StatusIdle})
	}
	return it
}

// Say something on an event waiting on the other side: accepted at version 0, recorded, back to looking, told and
// woken.
func TestInboxSayAnythingOnWaitingItem(t *testing.T) {
	h, _ := sayAnythingHub(t)
	it := sayItem(t, h, "say:waiting", HandlingWaiting, true)
	code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"先问清楚什么时候要"}`)
	if code != 200 {
		t.Fatalf("say something with no suggestion: %d %v", code, out)
	}
	if out["suggestion"] != nil || out["status"] != StatusWorking || out["handling"].(map[string]any)["state"] != HandlingLooking {
		t.Fatalf("listed after saying something: %v", out)
	}
	h.inbox.mu.Lock()
	e := it.Chain[len(it.Chain)-1]
	state, session, open := it.Handling.State, it.Agent.SessionID, it.open()
	h.inbox.mu.Unlock()
	if e.Kind != ChainInstructed || e.Detail != "先问清楚什么时候要" || state != HandlingLooking || session != "s2" || !open {
		t.Fatalf("chain %+v, handling %s, conversation %q, open %v", e, state, session, open)
	}
	toldConv(t, h, "先问清楚什么时候要")
}

// From a failed turn and from a running Agent too: both go back to looking on the same conversation.
func TestInboxSayAnythingFromFailedAndAgent(t *testing.T) {
	for _, state := range []string{HandlingFailed, HandlingAgent} {
		t.Run(state, func(t *testing.T) {
			h, _ := sayAnythingHub(t)
			it := sayItem(t, h, "say:"+state, state, true)
			code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"换个问法"}`)
			if code != 200 || out["status"] != StatusWorking {
				t.Fatalf("%s: %d %v", state, code, out)
			}
			h.inbox.mu.Lock()
			next, last := it.Handling.State, it.Chain[len(it.Chain)-1].Kind
			h.inbox.mu.Unlock()
			if next != HandlingLooking || last != ChainInstructed {
				t.Fatalf("%s: handling %s, chain %v", state, next, last)
			}
			toldConv(t, h, "换个问法")
		})
	}
}

// With no processing conversation at all: the words queue one (within the concurrency limit) and its first message
// carries them.
func TestInboxSayAnythingStartsAConversation(t *testing.T) {
	h, _ := sayAnythingHub(t)
	it := sayItem(t, h, "say:none", "", false)
	// The manager's own passes are held back; each one below is driven by the test.
	h.inbox.handing.Lock()
	defer h.inbox.handing.Unlock()
	code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"先问预算"}`)
	if code != 200 || out["status"] != StatusWorking {
		t.Fatalf("say something with no conversation: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	queued := it.Agent.State == AgentQueued && it.Agent.SessionID == "" && it.Agent.Wake
	pending := len(it.Agent.Pending)
	h.inbox.mu.Unlock()
	if !queued || pending == 0 {
		t.Fatalf("not queued for a conversation: %+v", it.Agent)
	}
	h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true, Concurrency: 1})
	h.inbox.mu.Lock()
	session, state := it.Agent.SessionID, it.Handling.State
	h.inbox.mu.Unlock()
	if session == "" || state != HandlingLooking {
		t.Fatalf("no conversation was started: %q %s", session, state)
	}
	first := h.f.lastSend(t)["text"].(string)
	if !strings.Contains(first, "先问预算") {
		t.Fatalf("the new conversation was not told the words:\n%s", first)
	}
}

// The concurrency limit still holds: with one conversation already looking, a second event saying something queues
// and starts nothing.
func TestInboxSayAnythingRespectsConcurrency(t *testing.T) {
	h, _ := sayAnythingHub(t)
	busy := sayItem(t, h, "say:busy", HandlingLooking, true)
	it := sayItem(t, h, "say:queued", "", false)
	h.inbox.handing.Lock()
	defer h.inbox.handing.Unlock()
	if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"同时处理"}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	starts := h.f.startCount()
	h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true, Concurrency: 1})
	h.inbox.mu.Lock()
	session, state, held := it.Agent.SessionID, it.Agent.State, busy.Agent.SessionID
	h.inbox.mu.Unlock()
	if h.f.startCount() != starts || session != "" || state != AgentQueued || held != "s2" {
		t.Fatalf("past the limit: %d starts (was %d), session %q, state %s, busy %q", h.f.startCount(), starts, session, state, held)
	}
}

// Still stale or done: with no suggestion only version 0 instruct is taken; a done event is closed.
func TestInboxSayAnythingStaleAndDone(t *testing.T) {
	h, _ := sayAnythingHub(t)
	it := sayItem(t, h, "say:stale", HandlingWaiting, true)
	for _, body := range []string{
		`{"version":3,"action":"instruct","text":"x"}`,
		`{"version":0,"action":"confirm"}`,
		`{"version":0,"action":"reject"}`,
	} {
		if code, out := suggestionCall(t, h, it.ID, body); code != 409 || out["code"] != "suggestion_stale" {
			t.Fatalf("%s: %d %v", body, code, out)
		}
	}
	h.inbox.mu.Lock()
	unchanged := it.Handling.State == HandlingWaiting && len(it.Chain) == 0 && !it.Agent.Wake
	h.inbox.mu.Unlock()
	if !unchanged {
		t.Fatalf("a refused request changed the event: %+v %v", it.Handling, chainKinds(it))
	}
	h.inbox.mu.Lock()
	it.done(time.Now(), "user")
	h.inbox.mu.Unlock()
	if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"x"}`); code != 409 || out["code"] != "inbox_item_done" {
		t.Fatalf("a done event: %d %v", code, out)
	}
}

// An old draft is listed as a version 1 suggestion: version 0 is stale on it, as any other version it does not match.
func TestInboxSayAnythingStaleOnAnOldDraft(t *testing.T) {
	h, _ := sayAnythingHub(t)
	at := time.Now().Add(-time.Hour)
	it := sayItem(t, h, "say:draft", "", false)
	h.inbox.mu.Lock()
	it.Agent.State, it.Agent.Draft, it.Agent.DraftedAt = AgentDrafted, "旧的草稿", &at
	g := currentSuggestion(it)
	h.inbox.mu.Unlock()
	if g == nil || g.Version != 1 {
		t.Fatalf("the old draft is not listed as a suggestion: %+v", g)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"换个说法"}`); code != 409 || out["code"] != "suggestion_stale" {
		t.Fatalf("version 0 over an old draft: %d %v", code, out)
	}
}

// An answer-only device (the watch) may not say anything, and with no suggestion there is nothing to read from it:
// 403, not a crash.
func TestInboxSayAnythingWatch(t *testing.T) {
	h, _ := sayAnythingHub(t)
	it := sayItem(t, h, "say:watch", HandlingWaiting, true)
	if code, _ := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"先问时间"}`, roleAnswer); code != 403 {
		t.Fatalf("the watch said something: %d", code)
	}
	h.inbox.mu.Lock()
	still := it.Handling.State == HandlingWaiting && len(it.Chain) == 0
	h.inbox.mu.Unlock()
	if !still {
		t.Fatalf("a refused watch request changed the event: %+v", it.Handling)
	}
}

// The AI twin off: saying something returns the event to Needs you with the reason, without waking or starting
// anything.
func TestInboxSayAnythingTwinOff(t *testing.T) {
	h, _ := sayAnythingHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	it := sayItem(t, h, "say:off", HandlingFailed, true)
	words := "先问时间"
	out, err := h.actOnSuggestion(context.Background(), it.ID, suggestionReq{Version: 0, Action: suggestInstruct, Text: &words}, true)
	if err != nil {
		t.Fatal(err)
	}
	checkTwinReleased(t, &out, i18n.Default)
	h.inbox.mu.Lock()
	inner := *it
	h.inbox.mu.Unlock()
	checkTwinReleased(t, &inner, i18n.Default)
	checkNoTwinWork(t, h)
}

// The words reach the conversation as the instruction note (the prompt that tells it what to do).
func TestInboxSayAnythingNote(t *testing.T) {
	if prompts.NoteInstructed == "" {
		t.Fatal("no instruction note")
	}
}
