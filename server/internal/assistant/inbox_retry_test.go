package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func failInboxForRetry(h *inboxHub, it *InboxItem) {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	it.Recap = "The current quote is ready"
	h.inbox.failHandling(it, "The previous attempt timed out", time.Now().Add(-time.Hour))
}

func retryInboxCall(t *testing.T, h *inboxHub, id string, role ...string) (int, map[string]any) {
	t.Helper()
	return inboxCall(t, h.routes(), http.MethodPost, "/api/v1/inbox/"+id+"/retry", "", role...)
}

func inboxProcessingStarts(h *inboxHub) int {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	return h.f.made
}

func TestInboxRetryReusesConversationOnce(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	failInboxForRetry(h, it)
	starts := inboxProcessingStarts(h)
	var wg sync.WaitGroup
	ready := make(chan struct{})
	statuses := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			<-ready
			code, out := retryInboxCall(t, h, it.ID)
			switch code {
			case http.StatusOK:
				handling, _ := out["handling"].(map[string]any)
				agent, _ := out["agent"].(map[string]any)
				if handling["state"] != HandlingLooking || handling["session_id"] != "s1" || handling["error"] != nil || agent["state"] != AgentRunning {
					t.Errorf("retry returned %v", out)
				}
			case http.StatusConflict:
				if out["code"] != "inbox_retry_stale" || out["item"] == nil {
					t.Errorf("duplicate returned %v", out)
				}
			}
			statuses <- code
		})
	}
	close(ready)
	wg.Wait()
	first, second := <-statuses, <-statuses
	if min(first, second) != http.StatusOK || max(first, second) != http.StatusConflict {
		t.Fatalf("concurrent retries returned %d and %d", first, second)
	}
	h.deliverPending(context.Background())
	h.deliverPending(context.Background())
	if sent := h.f.sentToAgents(); len(sent) != 1 || !strings.Contains(sent[0], "The previous attempt timed out") || inboxProcessingStarts(h) != starts {
		t.Fatalf("retry did not wake the existing conversation once: %v", sent)
	}
}

func TestInboxRetryDeliversPendingNotes(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	ctx := context.Background()
	cfg := InboxAgentConfig{Enabled: true}
	now := time.Now()
	const source = "The quote should include delivery"
	const instruction = "Check delivery costs before drafting"
	const first = "Use the original item number"
	const readNotice = "已经在来源里看过这件事的最新消息"

	h.inbox.mu.Lock()
	h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: first}, true, now)
	for range 16 {
		h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: "Keep checking the quote"}, true, now)
	}
	it.Last, it.Context = "101", source
	h.inbox.mu.Unlock()
	h.saveWrites(ctx, h.inbox.escalate(now.Add(-time.Hour), now), "its escalation")
	h.inbox.mu.Lock()
	h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: instruction}, true, now)
	h.inbox.mu.Unlock()

	h.f.mu.Lock()
	h.f.failSend = errors.New("delivery refused")
	h.f.mu.Unlock()
	h.deliverPending(ctx)
	failedAt := now.Add(agentTimeout(cfg) + time.Second)
	h.sweepInboxAgents(ctx, cfg, failedAt)
	// A source read can join the deferred queue after handling has failed.
	h.inbox.mu.Lock()
	h.inbox.queueInboxWake(it, inboxNote{Name: prompts.NoteSelfRead}, false, failedAt)
	h.inbox.mu.Unlock()
	row := inboxAgentStoppedSnapshot(t, h, it)
	if row.Handling.State != HandlingFailed || row.Agent.Of != row.Last || len(row.Agent.Pending) != 20 || len(h.f.sentToAgents()) != 0 {
		t.Fatalf("failed delivery did not leave a full pending queue: %s, of=%q, last=%q, pending=%d", row.Handling.State, row.Agent.Of, row.Last, len(row.Agent.Pending))
	}

	h.f.mu.Lock()
	h.f.failSend = nil
	h.f.mu.Unlock()
	starts := inboxProcessingStarts(h)
	if code, out := retryInboxCall(t, h, it.ID); code != http.StatusOK {
		t.Fatalf("retry: %d %v", code, out)
	}
	h.deliverPending(ctx)
	h.deliverPending(ctx)
	sent := h.f.sentToAgents()
	row = inboxAgentStoppedSnapshot(t, h, it)
	if len(sent) != 1 || row.Agent.SessionID != "s1" || len(row.Agent.Pending) != 0 || inboxProcessingStarts(h) != starts {
		t.Fatalf("retry did not drain the existing conversation's queue once: session=%q, pending=%d, sends=%d", row.Agent.SessionID, len(row.Agent.Pending), len(sent))
	}
	for _, want := range []string{first, source, instruction, readNotice} {
		if strings.Count(sent[0], want) != 1 {
			t.Errorf("expected exactly one copy of %q in the retry delivery: %s", want, sent[0])
		}
	}
}

func TestInboxRetryStartsWithRecapAndChain(t *testing.T) {
	for _, previous := range []string{"without conversation", "with missing conversation"} {
		t.Run(previous, func(t *testing.T) {
			h, _, it := handledHub(t)
			h.inbox.handing.Lock()
			t.Cleanup(h.inbox.handing.Unlock)
			if previous == "without conversation" {
				h.inbox.mu.Lock()
				it.Agent = nil
				it.Handling = nil
				h.inbox.mu.Unlock()
			} else {
				// Reserve s1 for the old processing conversation before the fake allocates a new ID.
				h.f.mu.Lock()
				h.f.made = 1
				h.f.mu.Unlock()
				h.originSet(context.Background(), "server", "s1", OriginInbox, it.ID, "")
			}
			failInboxForRetry(h, it)
			starts := inboxProcessingStarts(h)
			if code, out := retryInboxCall(t, h, it.ID); code != http.StatusOK {
				t.Fatalf("retry: %d %v", code, out)
			}
			if previous == "with missing conversation" {
				h.f.setSessions("server")
				h.f.mu.Lock()
				h.f.failSend = errors.New("no such session")
				h.f.mu.Unlock()
				h.deliverPending(context.Background())
				h.f.mu.Lock()
				h.f.failSend = nil
				h.f.mu.Unlock()
			}
			h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
			row := inboxAgentStoppedSnapshot(t, h, it)
			if row.Handling.State != HandlingLooking || row.Agent.SessionID == "" || inboxProcessingStarts(h) != starts+1 ||
				previous == "with missing conversation" && row.Agent.SessionID == "s1" {
				t.Fatalf("retry did not start a fresh conversation: %+v, %+v", row.Handling, row.Agent)
			}
			prompt, _ := h.f.lastSend(t)["text"].(string)
			for _, want := range []string{row.Recap, "The previous attempt timed out", "处理链条"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("fresh conversation prompt lacks %q: %s", want, prompt)
				}
			}
		})
	}
}

func TestInboxRetryUsesLinkedAgentStop(t *testing.T) {
	for _, scenario := range []string{"still working", "stop already recorded", "processing conversation absent", "agent missing"} {
		t.Run(scenario, func(t *testing.T) {
			h, it, sess := newInboxAgentStoppedHub(t)
			ctx := context.Background()
			cfg := InboxAgentConfig{Enabled: true}
			failInboxForRetry(h, it)
			switch scenario {
			case "still working":
				working := sess
				working.Status = proto.StatusWorking
				h.f.setSessions(sess.MachineID, working)
			case "stop already recorded":
				h.recordInboxAgentStops(ctx, sess)
			case "processing conversation absent":
				h.inbox.mu.Lock()
				it.Agent.MachineID, it.Agent.SessionID = "", ""
				it.Handling.MachineID, it.Handling.SessionID = "", ""
				h.inbox.mu.Unlock()
			case "agent missing":
				h.f.setSessions(sess.MachineID)
			}
			starts := inboxProcessingStarts(h)
			agentStarts := h.f.startCount()
			if code, out := retryInboxCall(t, h, it.ID); code != http.StatusOK || out["handling"].(map[string]any)["state"] != HandlingAgent {
				t.Fatalf("retry: %d %v", code, out)
			}
			h.sweepInboxAgents(ctx, cfg, time.Now())
			if scenario == "still working" {
				h.deliverPending(ctx)
				h.startInboxAgents(ctx, cfg)
				if row := inboxAgentStoppedSnapshot(t, h, it); row.Handling.State != HandlingAgent || len(h.f.sentToAgents()) != 0 || inboxProcessingStarts(h) != starts {
					t.Fatalf("retry restarted work while the Agent was still working: %+v", row.Handling)
				}
				h.f.setSessions(sess.MachineID, sess)
				h.sweepInboxAgents(ctx, cfg, time.Now())
			}
			h.deliverPending(ctx)
			h.startInboxAgents(ctx, cfg)
			row := inboxAgentStoppedSnapshot(t, h, it)
			if row.Handling.State != HandlingLooking {
				t.Fatalf("retry did not resume handling: %+v", row.Handling)
			}
			if h.f.startCount() != agentStarts {
				t.Fatal("retry started another sub-session")
			}
			if scenario == "agent missing" {
				if inboxAgentStoppedCount(&row) != 0 || len(h.f.sentToAgents()) != 1 {
					t.Fatalf("missing Agent did not wake processing once: %v", h.f.sentToAgents())
				}
				return
			}
			if inboxAgentStoppedCount(&row) != 1 {
				t.Fatalf("retry recorded the Agent stop %d times", inboxAgentStoppedCount(&row))
			}
			if scenario == "processing conversation absent" {
				if prompt, _ := h.f.lastSend(t)["text"].(string); inboxProcessingStarts(h) != starts+1 || !strings.Contains(prompt, sess.LastText) {
					t.Fatalf("fresh processing did not receive the linked Agent's stop: %s", prompt)
				}
			} else if sent := h.f.sentToAgents(); len(sent) != 1 || !strings.Contains(sent[0], sess.LastText) || inboxProcessingStarts(h) != starts {
				t.Fatalf("processing did not receive the linked Agent's stop once: %v", sent)
			}
		})
	}
}

func TestInboxRetryRefusesIneligibleItemsAndRollsBackFailedSave(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	if code, out := retryInboxCall(t, h, it.ID); code != http.StatusConflict || out["code"] != "inbox_retry_stale" {
		t.Fatalf("running item: %d %v", code, out)
	}
	if code, out := retryInboxCall(t, h, "missing"); code != http.StatusNotFound {
		t.Fatalf("unknown item: %d %v", code, out)
	}
	failInboxForRetry(h, it)
	if code, out := retryInboxCall(t, h, it.ID, "answer"); code != http.StatusForbidden {
		t.Fatalf("answer-only device: %d %v", code, out)
	}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	if code, out := retryInboxCall(t, h, it.ID); code != http.StatusConflict || out["code"] != "inbox_retry_disabled" {
		t.Fatalf("disabled AI twin: %d %v", code, out)
	}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	h.st.imu.Lock()
	h.st.down = errors.New("store unavailable")
	h.st.imu.Unlock()
	if code, out := retryInboxCall(t, h, it.ID); code != http.StatusServiceUnavailable {
		t.Fatalf("failed save: %d %v", code, out)
	}
	h.deliverPending(context.Background())
	if row := inboxAgentStoppedSnapshot(t, h, it); row.Handling.State != HandlingFailed || len(row.Agent.Pending) != 0 || len(h.f.sentToAgents()) != 0 {
		t.Fatalf("failed write left retry work behind: %+v, %+v", row.Handling, row.Agent)
	}
	h.st.imu.Lock()
	h.st.down = nil
	h.st.imu.Unlock()
	if code, out := retryInboxCall(t, h, it.ID); code != http.StatusOK {
		t.Fatalf("retry after store recovered: %d %v", code, out)
	}
}
