package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// snapshotHookHub runs a hook while the card is being announced: the window
// between registration and the original tool call reaching awaitPrepared.
type snapshotHookHub struct {
	*fakeHub
	onCard func()
	once   sync.Once
}

func (h *snapshotHookHub) Tell(event string, params any) error {
	if event == assistantwire.EventFleetInteraction && h.onCard != nil {
		h.once.Do(h.onCard)
	}
	return h.fakeHub.Tell(event, params)
}

func TestPreparedSnapshotWaiterCannotStealOriginalResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeHub()
		d := newFleetDevs()
		d.add("mac", "Mac")
		h := &snapshotHookHub{fakeHub: f}
		s := New(Options{Hub: h, Logf: testLogf(t), Devices: d})
		ft := &fleetTest{t: t, s: s, d: d, f: f}
		preparedTestConversation(ft)
		ix := proto.Interaction{ID: "ix_snapshot_first", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
			ToolName: "persona_settings", Summary: "Change the example setting", ExpiresAt: time.Now().Add(time.Hour)}
		type waiterResult struct {
			r   proto.Resolution
			err error
		}
		snapshot := make(chan waiterResult, 1)
		announced := make(chan struct{})
		h.onCard = func() {
			go func() {
				r, err := s.waitOwnedQuestion(context.Background(), ix)
				snapshot <- waiterResult{r, err}
			}()
			// The original caller is still inside Tell, so only the snapshot waiter can
			// be waiting here. This is the order that used to consume the one-shot done.
			time.Sleep(200 * time.Millisecond) // sleep-ok: virtual synctest time orders the snapshot waiter first
			synctest.Wait()
			close(announced)
		}
		var applies atomic.Int32
		done := startPreparedInteraction(t, ft, ix, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
			applies.Add(1)
			return "setting applied", nil
		}})
		<-announced
		allow := true
		if err := s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		got := awaitPreparedTest(t, done)
		if got.err != nil || got.out.Clarifying || got.out.Text != "setting applied" {
			t.Fatalf("original prepared call = %+v, %v", got.out, got.err)
		}
		select {
		case other := <-snapshot:
			if other.err != nil || other.r.Allow == nil || !*other.r.Allow {
				t.Fatalf("snapshot waiter result = %+v, %v", other.r, other.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the snapshot waiter did not receive the broadcast result")
		}
		if applies.Load() != 1 {
			t.Fatalf("owner Allow applied %d times", applies.Load())
		}
	})
}

func TestPreparedSnapshotAfterOriginalWaitsSharesResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		id := "ix_snapshot_after_original"
		var applies atomic.Int32
		done := startPreparedInteraction(t, ft, proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
			ToolName: "persona_settings", Summary: "Change the example setting", ExpiresAt: time.Now().Add(time.Hour)},
			approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
				applies.Add(1)
				return "setting applied", nil
			}})
		time.Sleep(20 * time.Millisecond) // sleep-ok: virtual synctest time lets the original caller await first
		synctest.Wait()
		ft.s.publish(context.Background())
		time.Sleep(20 * time.Millisecond) // sleep-ok: virtual synctest time lets the snapshot waiter attach second
		synctest.Wait()
		allow := true
		if err := ft.s.resolve(id, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		got := awaitPreparedTest(t, done)
		if got.err != nil || got.out.Clarifying || got.out.Text != "setting applied" || applies.Load() != 1 {
			t.Fatalf("original prepared call = %+v, %v, applies=%d", got.out, got.err, applies.Load())
		}
	})
}

func TestPreparedSnapshotWithdrawalDoesNotCancelOriginal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		id := "ix_snapshot_withdrawn"
		ix := proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
			ToolName: "persona_settings", Summary: "Change the example setting", ExpiresAt: time.Now().Add(time.Hour)}
		var applies atomic.Int32
		done := startPreparedInteraction(t, ft, ix, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
			applies.Add(1)
			return "setting applied", nil
		}})
		synctest.Wait()

		// A snapshot computed before registration can omit the card even when no
		// newer sequence supersedes it. A snapshot that lists the card starts no
		// waiter of its own (#13308: the original caller owns a prepared card), and
		// withdrawing it must not cancel the original operation or its still-pending
		// owner card.
		ft.s.askOnPhone([]proto.Interaction{ix})
		synctest.Wait()
		ft.s.fleet.askMu.Lock()
		waiter := ft.s.fleet.asked[id]
		ft.s.fleet.askMu.Unlock()
		if waiter != nil {
			t.Fatal("the snapshot started a waiter for a prepared card its original caller owns")
		}
		ft.s.askOnPhone(nil)
		synctest.Wait()

		select {
		case got := <-done:
			t.Fatalf("snapshot withdrawal ended the original prepared call: %+v, %v", got.out, got.err)
		default:
		}
		if _, ok := ft.s.pendingIx(id); !ok {
			t.Fatal("snapshot withdrawal removed the still-unanswered owner card")
		}
		if got := applies.Load(); got != 0 {
			t.Fatalf("snapshot withdrawal applied the operation %d times", got)
		}

		allow := true
		if err := ft.s.resolve(id, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatalf("owner Allow after snapshot withdrawal: %v", err)
		}
		got := awaitPreparedTest(t, done)
		if got.err != nil || got.out.Clarifying || got.out.Text != "setting applied" {
			t.Fatalf("original prepared call after owner Allow = %+v, %v", got.out, got.err)
		}
		synctest.Wait()
		if got := applies.Load(); got != 1 {
			t.Fatalf("owner Allow applied %d times, want exactly one", got)
		}
		ft.waitPending(0)
	})
}

func TestPreparedSnapshotWaiters150RoundsNeverHangOriginal(t *testing.T) {
	const rounds = 150
	for i := 0; i < rounds; i++ {
		synctest.Test(t, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			preparedTestConversation(ft)
			id := fmt.Sprintf("ix_snapshot_round_%d", i)
			var applies atomic.Int32
			done := startPreparedInteraction(t, ft, proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
				ToolName: "persona_settings", Summary: "Change the example setting", ExpiresAt: time.Now().Add(time.Hour)},
				approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
					applies.Add(1)
					return "setting applied", nil
				}})
			// Let the original caller reach awaitPrepared before repeated snapshots start.
			synctest.Wait()
			stop := make(chan struct{})
			ticker := time.NewTicker(time.Millisecond)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
						ft.s.publish(context.Background())
					}
				}
			}()
			var stopOnce sync.Once
			stopPublisher := func() {
				stopOnce.Do(func() {
					ticker.Stop()
					close(stop)
				})
				wg.Wait()
			}
			t.Cleanup(stopPublisher)
			time.Sleep(10 * time.Millisecond) // sleep-ok: virtual synctest time overlaps the original and repeated snapshot waiters
			synctest.Wait()
			allow := true
			if err := ft.s.resolve(id, proto.Resolution{Allow: &allow}); err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
			select {
			case got := <-done:
				if got.err != nil || got.out.Text != "setting applied" {
					t.Fatalf("round %d: original result = %+v, %v", i, got.out, got.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("round %d: owner Allow applied %d times but the original tool call hung", i, applies.Load())
			}
			stopPublisher()
			if applies.Load() != 1 {
				t.Fatalf("round %d applied %d times", i, applies.Load())
			}
		})
	}
}

func TestUnansweredPreparedSnapshotsDoNotOwnItsLifecycle(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	id := "ix_unanswered_snapshot_lifecycle"
	var applies atomic.Int32
	done := startPreparedTest(t, ft, id, "", func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "setting applied", nil
	})
	ix, ok := ft.s.pendingIx(id)
	if !ok {
		t.Fatal("the unanswered prepared card did not stay pending")
	}
	ft.s.askOnPhone([]proto.Interaction{ix})
	ft.s.fleet.askMu.Lock()
	waiter := ft.s.fleet.asked[id]
	ft.s.fleet.askMu.Unlock()
	if waiter != nil {
		t.Fatal("an unanswered prepared card spawned another snapshot waiter")
	}
	ft.s.askOnPhone(nil)

	if err := ft.s.clarify(context.Background(), id, "Why?", "owner-"+id, nil); err != nil {
		t.Fatalf("the snapshot watcher cancelled the original prepared card: %v", err)
	}
	if got := awaitPreparedTest(t, done); got.err != nil || !got.out.Clarifying {
		t.Fatalf("original prepared call = %+v, %v, want clarification", got.out, got.err)
	}
	allow := true
	if err := ft.s.resolve(id, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	if got := applies.Load(); got != 1 {
		t.Fatalf("owner Allow applied %d times, want exactly once", got)
	}
	ft.waitPending(0)
	if result := waitApprovalResult(t, ft, id); !strings.Contains(result, "setting applied") {
		t.Fatalf("terminal result = %q, want the applied result", result)
	}
}

func TestClarifiedSnapshotsDoNotRestartWaiters(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	id := "ix_clarified_snapshot_noise"
	done := startPreparedTest(t, ft, id, "", nil)
	if err := ft.s.clarify(context.Background(), id, "Why?", "owner-"+id, nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	ix, ok := ft.s.pendingIx(id)
	if !ok {
		t.Fatal("the clarified card did not stay pending")
	}
	for range 3 {
		ft.s.askOnPhone([]proto.Interaction{ix})
	}
	ft.s.fleet.askMu.Lock()
	waiter := ft.s.fleet.asked[id]
	ft.s.fleet.askMu.Unlock()
	if waiter != nil {
		t.Fatal("a clarified card spawned another snapshot waiter")
	}
	allow := true
	if err := ft.s.resolve(id, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
}

type preparedLifecycleEnd struct {
	name   string
	status string
	end    func(*fleetTest, string)
}

var preparedLifecycleEnds = []preparedLifecycleEnd{
	{name: "disconnect", status: "cancelled", end: func(ft *fleetTest, _ string) { ft.s.NodeDetached("mac") }},
	{name: "session close", status: "cancelled", end: func(ft *fleetTest, _ string) { ft.s.endSessionCards("mac", "s1") }},
	{name: "dismiss", status: "dismissed", end: func(ft *fleetTest, id string) { ft.event("mac", "interaction.dismiss", map[string]any{"id": id}) }},
	{name: "cancel", status: "cancelled", end: func(ft *fleetTest, id string) { ft.event("mac", "interaction.cancel", map[string]any{"id": id}) }},
	{name: "TTL expiry", status: "expired", end: func(ft *fleetTest, id string) {
		ft.s.fleet.mu.Lock()
		p := ft.s.fleet.interactions[id]
		ft.s.fleet.mu.Unlock()
		if p == nil {
			return
		}
		ft.s.fleet.mu.Lock()
		paused := p.prepared != nil && p.prepared.paused
		ft.s.fleet.mu.Unlock()
		if paused {
			// The pause-budget timer uses this same guarded terminal path once its
			// cumulative budget is spent; the timer itself has a separate synctest.
			ft.s.endPreparedIf(p, "expired", "", func(a *preparedApproval) bool { return a.paused })
		} else {
			ft.s.expirePrepared(p)
		}
	}},
}

func TestPreparedLifecycleEndReleasesOriginalCallExactlyOnce(t *testing.T) {
	for _, tc := range preparedLifecycleEnds {
		if tc.status == "expired" {
			// #10132 makes the first unanswered window non-final. Its real
			// waiting-window terminal path is covered with the journal instead.
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			preparedTestConversation(ft)
			var applies atomic.Int32
			id := "ix_end_unclarified_" + strings.ReplaceAll(tc.name, " ", "_")
			done := startPreparedInteraction(t, ft, proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
				ToolName: "persona_settings", ExpiresAt: time.Now().Add(time.Hour)}, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
				applies.Add(1)
				return "unexpected", nil
			}})
			tc.end(ft, id)
			got := awaitPreparedTest(t, done)
			if got.err != nil || got.out.Text == "" || got.out.Clarifying {
				t.Fatalf("original tool outcome = %+v, %v", got.out, got.err)
			}
			if tc.status == "expired" {
				for _, want := range []string{id, "expired", "nothing was executed"} {
					if !strings.Contains(got.out.Text, want) {
						t.Errorf("expired original tool outcome = %q, want %q", got.out.Text, want)
					}
				}
			}
			select {
			case duplicate := <-done:
				t.Fatalf("original tool returned twice: %+v", duplicate)
			default:
			}
			tc.end(ft, id) // a repeated node event is not another outcome
			ft.waitPending(0)
			if applies.Load() != 0 {
				t.Fatalf("lifecycle ending applied %d times", applies.Load())
			}
		})
	}
}

func TestClarifiedLifecycleEndReportsOriginalConversationExactlyOnce(t *testing.T) {
	for _, tc := range preparedLifecycleEnds {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			preparedTestConversation(ft)
			ft.f.mu.Lock()
			ft.f.known["s1"] = true // the fake accepts resume_only delivery as a real node does
			ft.f.mu.Unlock()
			id := "ix_end_clarified_" + strings.ReplaceAll(tc.name, " ", "_")
			done := startPreparedTest(t, ft, id, "", nil)
			if err := ft.s.clarify(context.Background(), id, "Why?", "owner-"+id, nil); err != nil {
				t.Fatal(err)
			}
			awaitPreparedTest(t, done)
			tc.end(ft, id)
			ft.waitPending(0)
			result := waitApprovalResult(t, ft, id)
			if !strings.Contains(result, tc.status) {
				t.Fatalf("terminal result = %q, want status %q", result, tc.status)
			}
			tc.end(ft, id)
			ft.s.deliverApprovalResults()
			waitApprovalResult(t, ft, id) // zero and two deliveries are both failures
		})
	}
}
