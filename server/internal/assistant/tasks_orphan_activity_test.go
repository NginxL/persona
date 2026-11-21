package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Regression for #12389 (Part 3): before reporting a running task as orphaned, the hub must reconcile closed-tab
// state against fresh or advancing session transcript activity across passes.
func TestOrphanWatchReconcilesClosedTabAgainstSessionActivity(t *testing.T) {
	ctx := context.Background()

	t.Run("fresh_activity_not_orphaned_on_first_pass", func(t *testing.T) {
		s, f := newQuietService(t)
		todo := orphanTask(t, s)
		now := time.Now()

		fresh := closedAgent(now.Add(-15 * time.Second))
		f.setSessions("box", fresh)
		s.orphanWatch(ctx, now)
		if o := listedOrphan(t, s, todo.ID); o != nil {
			t.Fatalf("fresh closed-tab rollout marked orphaned prematurely: %+v", o)
		}
	})

	t.Run("advancing_entries_clear_orphan_until_activity_stops", func(t *testing.T) {
		s, f := newQuietService(t)
		todo := orphanTask(t, s)
		now := time.Now()

		s1 := closedAgent(now.Add(-10 * time.Minute))
		s1.Rev = 1
		f.setSessions("box", s1)
		s.orphanWatch(ctx, now)
		if o := listedOrphan(t, s, todo.ID); o == nil || o.Reason != orphanClosed {
			t.Fatalf("expected initial closed orphan on stale rollout, got %+v", o)
		}

		// Entries advance between passes: orphan must clear while writing continues.
		s2 := closedAgent(now.Add(50 * time.Second))
		s2.Rev = 2
		f.setSessions("box", s2)
		s.orphanWatch(ctx, now.Add(time.Minute))
		if o := listedOrphan(t, s, todo.ID); o != nil {
			t.Fatalf("orphan not cleared while entries advance: %+v", o)
		}

		s3 := closedAgent(now.Add(110 * time.Second))
		s3.Rev = 3
		f.setSessions("box", s3)
		s.orphanWatch(ctx, now.Add(2*time.Minute))
		if o := listedOrphan(t, s, todo.ID); o != nil {
			t.Fatalf("orphan returned while entries still advance: %+v", o)
		}

		// Once entries stop advancing past orphanActivityGrace, orphanClosed triggers cleanly.
		s.orphanWatch(ctx, now.Add(2*time.Minute+orphanActivityGrace+time.Second))
		if o := listedOrphan(t, s, todo.ID); o == nil || o.Reason != orphanClosed {
			t.Fatalf("expected closed orphan after activity stopped, got %+v", o)
		}
	})
}

// Regression for #12389 (Part 4 hub side): task_plan continuing a running Codex task stops the live session through
// session.stop without erroring and binds the replacement cleanly.
func TestContinuationStopsLiveCodexTaskThroughSessionStop(t *testing.T) {
	e := newThreadEnv(t, nil)
	ctx := context.Background()
	todo, err := e.s.createTaskTodo(ctx, "Continue Codex task", "Keep going cleanly.", "m1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	e.must("PUT", "/api/v1/todos/"+todo.ID+"/agents/box/agent01", "")
	if _, err := e.s.ensureTaskBrief(ctx, todoNow(e.s, todo.ID)); err != nil {
		t.Fatal(err)
	}

	e.sessions("box", proto.Session{
		SessionID:    "agent01",
		Agent:        "codex",
		SourceKind:   "app_server",
		Host:         "herdr",
		PaneID:       "w1:p1",
		Status:       proto.StatusWorking,
		Capabilities: &proto.SessionCapabilities{Send: true, Interrupt: true, Terminal: true},
	})
	var stopped bool
	e.f.answerNode(deviceSessionStop, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["session_id"] == "agent01" {
			stopped = true
		}
		return map[string]any{"stopped": true}, nil
	})

	if _, err := e.s.toolTaskPlan(ctx, "m1", continuesArgs(todo.ID)); err != nil {
		t.Fatalf("task_plan continues failed: %v", err)
	}
	if !stopped {
		t.Fatal("expected session.stop to be called for live Codex agent")
	}
	old := newAgentOn(t, e, todo.ID)
	if old.ReleasedAt == nil || old.DismissedAt != nil {
		t.Fatalf("old live agent was not released into history: %+v", old)
	}
}
