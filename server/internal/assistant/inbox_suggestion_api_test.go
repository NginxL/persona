package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// An instruction at version 0 reopens the handling chat's work when the item has no suggestion, whatever it was doing.
func TestInboxSuggestionInstructWithoutSuggestion(t *testing.T) {
	for _, state := range []string{HandlingWaiting, HandlingFailed, HandlingAgent} {
		t.Run(state, func(t *testing.T) {
			h, _, it := handledHub(t)
			h.inbox.mu.Lock()
			if state == HandlingFailed {
				h.inbox.failHandling(it, "example failure", time.Now())
			} else {
				h.inbox.setHandling(it, state, "", time.Now())
			}
			h.inbox.mu.Unlock()

			code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Check the current state and try again"}`)
			handling, _ := out["handling"].(map[string]any)
			if code != 200 || out["suggestion"] != nil || handling["state"] != HandlingLooking || out["status"] != StatusWorking {
				t.Fatalf("instruct from %s: %d %v", state, code, out)
			}
			if e := lastChain(h, it); e.Kind != ChainInstructed || e.Detail != "Check the current state and try again" {
				t.Fatalf("chain from %s: %+v", state, e)
			}
			toldConv(t, h, "Check the current state and try again")
			h.inbox.mu.Lock()
			defer h.inbox.mu.Unlock()
			if it.Agent.SessionID != "s1" || it.Handling.State != HandlingLooking || it.Handling.Error != "" || len(it.Agent.Pending) != 0 {
				t.Fatalf("handling after %s: %+v %+v", state, it.Agent, it.Handling)
			}
		})
	}
}

// With no handling conversation, the same instruction creates the bookkeeping and the usual pass starts one.
func TestInboxSuggestionInstructWithoutConversation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear func(*InboxItem)
	}{
		{"no agent record", func(it *InboxItem) {
			it.Agent, it.Handling = nil, nil
		}},
		{"no session", func(it *InboxItem) {
			it.Agent.State, it.Agent.MachineID, it.Agent.SessionID = AgentSkipped, "", ""
			it.Agent.Pending, it.Agent.Wake, it.Handling = nil, false, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, _, it := handledHub(t)
				h.inbox.mu.Lock()
				tc.clear(it)
				h.inbox.mu.Unlock()
				before := len(conversationStarts(h.f))

				code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Start fresh and check the request"}`)
				handling, _ := out["handling"].(map[string]any)
				if code != 200 || out["suggestion"] != nil || handling["state"] != HandlingLooking {
					t.Fatalf("instruct without a conversation: %d %v", code, out)
				}
				synctest.Wait()

				starts := conversationStarts(h.f)
				if len(starts) != before+1 {
					t.Fatalf("starts = %d, want %d", len(starts), before+1)
				}
				text, _ := starts[len(starts)-1]["text"].(string)
				if !strings.Contains(text, "Start fresh and check the request") {
					t.Fatalf("the first message lost the instruction:\n%s", text)
				}
				h.inbox.mu.Lock()
				defer h.inbox.mu.Unlock()
				if it.Agent == nil || it.Agent.SessionID == "" || it.Agent.State != AgentRunning || it.Handling == nil ||
					it.Handling.State != HandlingLooking || len(it.Agent.Pending) != 0 {
					t.Fatalf("started handling: %+v %+v", it.Agent, it.Handling)
				}
				if e := it.Chain[len(it.Chain)-1]; e.Kind != ChainInstructed || e.Detail != "Start fresh and check the request" {
					t.Fatalf("chain: %+v", e)
				}
			})
		})
	}
}

// Version 0 does not take a suggestion that is still present, and no-suggestion calls keep their stale rules.
func TestInboxSuggestionVersionZeroStaleRules(t *testing.T) {
	t.Run("current suggestion", func(t *testing.T) {
		for _, body := range []string{
			`{"version":0,"action":"confirm"}`,
			`{"version":0,"action":"reject"}`,
			`{"version":0,"action":"instruct","text":"Change the approach"}`,
		} {
			h, _, it := handledHub(t)
			if _, err := suggestTool(t, h, `{"kind":"reply","text":"A proposed reply"}`); err != nil {
				t.Fatal(err)
			}
			code, out := suggestionCall(t, h, it.ID, body)
			if code != 409 || out["code"] != "suggestion_stale" {
				t.Fatalf("%s: %d %v", body, code, out)
			}
			h.inbox.mu.Lock()
			g := currentSuggestion(it)
			h.inbox.mu.Unlock()
			if g == nil || g.Version != 1 {
				t.Fatalf("%s consumed the current suggestion: %+v", body, g)
			}
		}
	})

	for _, tc := range []struct {
		name string
		body string
	}{
		{"instruct with a nonzero version", `{"version":1,"action":"instruct","text":"Try again"}`},
		{"confirm with a nonzero version", `{"version":1,"action":"confirm"}`},
		{"reject with a nonzero version", `{"version":1,"action":"reject"}`},
		{"confirm at version zero", `{"version":0,"action":"confirm"}`},
		{"reject at version zero", `{"version":0,"action":"reject"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, it := handledHub(t)
			h.inbox.mu.Lock()
			chain := len(it.Chain)
			h.inbox.mu.Unlock()
			code, out := suggestionCall(t, h, it.ID, tc.body)
			if code != 409 || out["code"] != "suggestion_stale" {
				t.Fatalf("stale response: %d %v", code, out)
			}
			h.inbox.mu.Lock()
			defer h.inbox.mu.Unlock()
			if len(it.Chain) != chain || currentSuggestion(it) != nil {
				t.Fatalf("a stale call changed the item: %+v", it)
			}
		})
	}
}

func TestInboxSuggestionInstructWithoutSuggestionDone(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	it.done(time.Now(), "user")
	h.inbox.mu.Unlock()
	code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Reopen this"}`)
	if code != 409 || out["code"] != "inbox_item_done" {
		t.Fatalf("done item: %d %v", code, out)
	}
}

func TestInboxSuggestionInstructWithoutSuggestionAnswerOnly(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	chain := len(it.Chain)
	h.inbox.mu.Unlock()
	code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Try again"}`, roleAnswer)
	if code != 403 {
		t.Fatalf("answer-only device: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if len(it.Chain) != chain || len(it.Agent.Pending) != 0 {
		t.Fatalf("an answer-only instruction changed the item: %+v", it)
	}
}

// With the twin off, the instruction is recorded but the item returns to Needs you without dispatching work.
func TestInboxSuggestionInstructWithoutSuggestionDisabled(t *testing.T) {
	h, _, it := handledHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	h.inbox.mu.Lock()
	it.Agent, it.Handling = nil, nil
	h.inbox.mu.Unlock()
	words := "Try again when available"
	out, err := h.actOnSuggestion(context.Background(), it.ID, suggestionReq{Version: 0, Action: suggestInstruct, Text: &words}, true)
	if err != nil {
		t.Fatal(err)
	}
	checkTwinReleased(t, &out, i18n.Default)
	checkTwinReleased(t, it, i18n.Default)
	if len(out.Chain) < 2 || out.Chain[len(out.Chain)-2].Kind != ChainInstructed || out.Chain[len(out.Chain)-2].Detail != words {
		t.Fatalf("the instruction was not recorded: %+v", out.Chain)
	}
	checkNoTwinWork(t, h)
}

func TestInboxSuggestionInstructWithoutSuggestionWriteFailure(t *testing.T) {
	h, st := newBulkFailHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	it := disabledTwinItem(h, "item", HandlingLooking, AgentRunning, "")
	before := stateOf(h, it)
	st.failAll = true
	words := "Try again"
	in := suggestionReq{Version: 0, Action: suggestInstruct, Text: &words}
	if _, err := h.actOnSuggestion(context.Background(), it.ID, in, true); !errors.Is(err, errInboxNotPersisted) {
		t.Fatalf("failed release returned %v, want a persistence error", err)
	}
	if stateOf(h, it) != before || changedTo(h, it.ID) {
		t.Fatal("the instruction survived its failed write")
	}
	checkNoTwinWork(t, h)

	st.failAll = false
	out, err := h.actOnSuggestion(context.Background(), it.ID, in, true)
	if err != nil {
		t.Fatal(err)
	}
	checkTwinReleased(t, &out, i18n.Default)
	checkNoTwinWork(t, h)
}
