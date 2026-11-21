package assistant

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestGatewayLimitsUseExistingContinuation(t *testing.T) {
	// Use a dated reset so this test remains independent of the wall clock's hour.
	reset := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Minute)
	quota := apiErrorTurn("s1", "API Error: Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at "+reset.Format("2006-01-02 15:04")+" UTC (in 2h)")
	decorateLimit(&quota) // older nodes can supply the error without its kind/reset
	if quota.TurnErrorKind != proto.TurnErrorUsageLimit || quota.TurnErrorResetsAt == nil || !quota.TurnErrorResetsAt.Equal(reset) {
		t.Fatalf("quota kind=%q reset=%v", quota.TurnErrorKind, quota.TurnErrorResetsAt)
	}
	s, f := workService(t)
	f.setSessions("box", quota)
	triage(t, s, "s1")
	entry := entryOf(t, s, "s1")
	if entry.WakeAt == nil || entry.Work.On != proto.WaitExternal || entry.InteractionID != "" || entry.Work.ResetsAt == nil {
		t.Fatalf("quota did not use reset wait: %+v", entry)
	}
	s.workTick(t.Context(), reset.Add(-time.Minute))
	if len(f.sentToAgents()) != 0 {
		t.Fatal("quota continued before the reset")
	}
	s.workTick(t.Context(), reset.Add(2*time.Minute))
	if len(f.sentToAgents()) != 1 || workEvents(s, "limit_continued") != 1 {
		t.Fatal("quota did not continue through the existing usage-limit path")
	}

	capacity := apiErrorTurn("s2", "API Error: Request rejected (429) · upstream account capacity is temporarily unavailable")
	decorateLimit(&capacity)
	w, _, _ := triageRules(capacity, time.UTC, "")
	if capacity.TurnErrorKind != proto.TurnErrorTransient || capacity.TurnErrorResetsAt != nil || w == nil || w.On != proto.WaitTransient {
		t.Fatalf("capacity kind=%q work=%+v", capacity.TurnErrorKind, w)
	}
}
