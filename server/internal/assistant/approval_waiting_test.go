package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// startShortPrepared starts a prepared card whose decision window is `window`, as persona_settings does with its
// fifteen minutes, and returns the original tool call's result channel.
func startShortPrepared(t *testing.T, ft *fleetTest, id string, window time.Duration, apply func(context.Context, proto.Resolution) (string, error)) <-chan preparedTestResult {
	t.Helper()
	ix := proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
		ToolName: "persona_settings", Summary: "Change the example setting", ExpiresAt: time.Now().Add(window)}
	ft.f.mu.Lock()
	if ft.f.known == nil {
		ft.f.known = map[string]bool{}
	}
	ft.f.known["s1"] = true // the conversation exists: its result is delivered once
	ft.f.mu.Unlock()
	done := make(chan preparedTestResult, 1)
	go func() {
		out, err := ft.s.waitPreparedCard(context.Background(), ix, approvalops.Operation{Apply: apply})
		done <- preparedTestResult{out: out, err: err}
	}()
	ft.waitPending(1)
	return done
}

// toldCard is the last card told to the hub (fleet.interaction) under id.
func toldCard(ft *fleetTest, id string) (proto.Interaction, int) {
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	var last proto.Interaction
	n := 0
	for _, e := range ft.f.told {
		if e.event != assistantwire.EventFleetInteraction {
			continue
		}
		var p assistantwire.FleetInteraction
		if json.Unmarshal(e.params, &p) == nil && p.Interaction.ID == id {
			last, n = p.Interaction, n+1
		}
	}
	return last, n
}

// An approval card nobody answered within its decision window stays on the apps instead of disappearing: the owner
// may have had no notification (push down, the app in the background) and opens the app or the web later. The tool
// call returns at the window with a non-final result, and a later owner Allow still applies, once, and is reported in
// the conversation.
func TestUnansweredApprovalStaysPendingAfterItsCallReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := approvalWaitingWindow
		approvalWaitingWindow = time.Hour
		t.Cleanup(func() { approvalWaitingWindow = old })
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		var applies atomic.Int32
		done := startShortPrepared(t, ft, "ix_unanswered", 100*time.Millisecond, func(_ context.Context, r proto.Resolution) (string, error) {
			applies.Add(1)
			return "setting applied", nil
		})
		elapse(150 * time.Millisecond)
		first := awaitPreparedTest(t, done)
		if first.err != nil || !first.out.Clarifying || first.out.Text != approvalops.WaitingText {
			t.Fatalf("original tool result = %+v, %v; want the non-final waiting result", first.out, first.err)
		}
		if !strings.Contains(first.out.Text, "do not retry the tool or create another approval") {
			t.Fatalf("the waiting result must forbid a second card: %q", first.out.Text)
		}
		// The owner opens the app or the web: the card is still among the pending ones, with its new deadline.
		pending := ft.s.pendingInteractions()
		if len(pending) != 1 || pending[0].ID != "ix_unanswered" {
			t.Fatalf("pending cards after the decision window = %+v, want the unanswered card", pending)
		}
		if left := time.Until(pending[0].ExpiresAt); left < 50*time.Minute {
			t.Fatalf("the waiting card expires in %v, want about the waiting window", left)
		}
		if card, n := toldCard(ft, "ix_unanswered"); n < 2 || !card.ExpiresAt.Equal(pending[0].ExpiresAt) {
			t.Fatalf("the apps were not told the card's new deadline: told %d times, last %v", n, card.ExpiresAt)
		}
		if applies.Load() != 0 {
			t.Fatal("the decision window ending applied the change")
		}
		allow := true
		if err := ft.s.resolve("ix_unanswered", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatalf("a late owner Allow was refused: %v", err)
		}
		if applies.Load() != 1 {
			t.Fatalf("apply count = %d, want exactly one", applies.Load())
		}
		ft.waitPending(0)
		if result := waitApprovalResult(t, ft, "ix_unanswered"); !strings.Contains(result, "setting applied") {
			t.Fatalf("terminal result in the conversation = %q", result)
		}
	})
}

// A waiting card still ends: past the waiting window it expires, nothing applies, and the conversation is told.
func TestWaitingApprovalExpiresAfterTheWaitingWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := approvalWaitingWindow
		approvalWaitingWindow = 300 * time.Millisecond
		t.Cleanup(func() { approvalWaitingWindow = old })
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		done := startShortPrepared(t, ft, "ix_waiting_expiry", 100*time.Millisecond, func(context.Context, proto.Resolution) (string, error) {
			t.Error("an expired card applied")
			return "", nil
		})
		elapse(150 * time.Millisecond)
		if got := awaitPreparedTest(t, done); got.out.Text != approvalops.WaitingText {
			t.Fatalf("original tool result = %+v", got.out)
		}
		elapse(200 * time.Millisecond)
		if got := len(ft.s.pendingInteractions()); got != 1 {
			t.Fatalf("the waiting card ended before its waiting window: pending=%d", got)
		}
		elapse(200 * time.Millisecond)
		ft.waitPending(0)
		if result := waitApprovalResult(t, ft, "ix_waiting_expiry"); !strings.Contains(result, "expired") {
			t.Fatalf("terminal result = %q", result)
		}
	})
}

// A card whose caller already went away is not kept waiting: nobody would be told its outcome.
func TestApprovalWithoutItsCallerExpiresAtItsWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		ctx, cancel := context.WithCancel(context.Background())
		ix := proto.Interaction{ID: "ix_no_caller", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
			ToolName: "persona_settings", ExpiresAt: time.Now().Add(100 * time.Millisecond)}
		done := make(chan preparedTestResult, 1)
		go func() {
			out, err := ft.s.waitPreparedCard(ctx, ix, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
				return "unexpected", nil
			}})
			done <- preparedTestResult{out: out, err: err}
		}()
		ft.waitPending(1)
		cancel()
		awaitPreparedTest(t, done)
		ft.waitPending(0)
	})
}
