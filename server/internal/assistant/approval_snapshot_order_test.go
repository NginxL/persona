package assistant

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

// startOlderApprovalSnapshotPublisher starts a real publish before the prepared
// card exists and holds it immediately before askOnPhone. The old snapshot has
// to remove this seeded task link, so it waits on quiet's leaf mutex. A newer
// snapshot sees that the link is already gone and can pass this cleanup without
// taking that mutex. Thus the newer publisher can apply its interactions first,
// after which release applies the genuinely older interaction list.
func startOlderApprovalSnapshotPublisher(t *testing.T, ft *fleetTest) (<-chan struct{}, func()) {
	t.Helper()
	key := [2]string{"mac", "left_before_approval_snapshot"}
	r := &ft.s.fleet.rep
	r.mu.Lock()
	r.links = map[[2]string]TodoLink{key: {
		MachineID: key[0],
		SessionID: key[1],
		TodoID:    "old_approval_snapshot_link",
	}}
	r.mu.Unlock()

	quiet := &ft.s.tasks.quiet
	quiet.mu.Lock()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(quiet.mu.Unlock) }
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()

	done := make(chan struct{})
	go func() {
		ft.s.publish(context.Background())
		close(done)
	}()
	for {
		r.mu.RLock()
		reached := r.got && len(r.links) == 0
		r.mu.RUnlock()
		if reached {
			break
		}
		// A mutex-blocked goroutine is not durably blocked for synctest's
		// virtual clock. Yield instead of using a sleep-based polling helper
		// until the older publisher has reached this gate.
		runtime.Gosched()
	}
	handedOff = true
	return done, release
}

// startPreparedInteractionAtSnapshotGate is startPreparedInteraction with a
// scheduler handoff instead of its sleep-based waitPending: the older publisher
// is still parked on an ordinary mutex, so virtual time cannot advance yet.
func startPreparedInteractionAtSnapshotGate(t *testing.T, ft *fleetTest, ix proto.Interaction, op approvalops.Operation) <-chan preparedTestResult {
	t.Helper()
	// The snapshot gate holds quiet.mu, which a cache-miss session lookup also
	// needs. Register against the already-observed complete session list so the
	// prepared caller can reach its card without passing through that gate.
	ft.s.fleetKeepPrevious(ix.MachineID, []proto.Session{{SessionID: ix.SessionID, Status: proto.StatusIdle}})
	done := make(chan preparedTestResult, 1)
	go func() {
		out, err := ft.s.waitPreparedCard(context.Background(), ix, op)
		done <- preparedTestResult{out: out, err: err}
	}()
	for {
		if got := len(ft.s.pendingInteractions()); got == 1 {
			break
		} else if got > 1 {
			t.Fatalf("pending interactions during gated setup = %d, want one", got)
		}
		runtime.Gosched()
	}
	t.Cleanup(func() {
		ft.s.fleet.mu.Lock()
		p := ft.s.fleet.interactions[ix.ID]
		ft.s.fleet.mu.Unlock()
		if p != nil {
			ft.s.endPrepared(p, "cancelled", "test cleanup")
		}
	})
	return done
}

func TestPreparedSnapshotOutOfOrderDoesNotCancelOriginal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		olderDone, releaseOlder := startOlderApprovalSnapshotPublisher(t, ft)
		defer releaseOlder()

		const id = "ix_out_of_order_snapshot"
		var applies atomic.Int32
		originalDone := startPreparedInteractionAtSnapshotGate(t, ft, proto.Interaction{
			ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
			ToolName: "persona_settings", Summary: "Change the example setting",
			ExpiresAt: time.Now().Add(time.Hour),
		}, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
			applies.Add(1)
			return "setting applied", nil
		}})

		freshDone := make(chan struct{})
		go func() {
			ft.s.publish(context.Background())
			close(freshDone)
		}()
		<-freshDone
		select {
		case <-olderDone:
			t.Fatal("the older publisher passed its application gate before the fresh publisher")
		default:
		}

		// Sequence 2 is now fully applied. Releasing sequence 1 makes its empty
		// interaction list the last one applied, reproducing the publication race.
		releaseOlder()
		<-olderDone
		synctest.Wait()

		select {
		case got := <-originalDone:
			t.Fatalf("a stale snapshot ended the original prepared call before the owner answered: %+v, %v", got.out, got.err)
		default:
		}
		if _, ok := ft.s.pendingIx(id); !ok {
			t.Fatal("a stale snapshot removed the still-unanswered owner card")
		}
		if got := applies.Load(); got != 0 {
			t.Fatalf("a stale snapshot applied the operation %d times", got)
		}

		allow := true
		if err := ft.s.resolve(id, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatalf("the owner's fresh Allow was rejected after a stale snapshot: %v", err)
		}
		got := awaitPreparedTest(t, originalDone)
		if got.err != nil || got.out.Clarifying || got.out.Text != "setting applied" {
			t.Fatalf("original prepared call after owner Allow = %+v, %v", got.out, got.err)
		}
		select {
		case duplicate := <-originalDone:
			t.Fatalf("owner Allow returned the original prepared call twice: %+v", duplicate)
		default:
		}
		synctest.Wait()
		if got := applies.Load(); got != 1 {
			t.Fatalf("owner Allow applied %d times, want exactly one", got)
		}
		ft.waitPending(0)
	})
}

func TestPreparedSnapshotConcurrentPublishersAllowOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		const id = "ix_concurrent_snapshot_publishers"
		var applies atomic.Int32
		originalDone := startPreparedInteraction(t, ft, proto.Interaction{
			ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
			ToolName: "persona_settings", Summary: "Change the example setting",
			ExpiresAt: time.Now().Add(time.Hour),
		}, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
			applies.Add(1)
			return "setting applied", nil
		}})

		const publisherCount = 16
		start := make(chan struct{})
		var publishers sync.WaitGroup
		publishers.Add(publisherCount)
		for range publisherCount {
			go func() {
				defer publishers.Done()
				<-start
				ft.s.publish(context.Background())
			}()
		}
		close(start)
		publishers.Wait()
		synctest.Wait()

		select {
		case got := <-originalDone:
			t.Fatalf("concurrent current snapshots ended the unanswered prepared call: %+v, %v", got.out, got.err)
		default:
		}
		allow := true
		if err := ft.s.resolve(id, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatalf("owner Allow after concurrent publishers: %v", err)
		}
		got := awaitPreparedTest(t, originalDone)
		if got.err != nil || got.out.Clarifying || got.out.Text != "setting applied" {
			t.Fatalf("original prepared call = %+v, %v", got.out, got.err)
		}
		synctest.Wait()
		if got := applies.Load(); got != 1 {
			t.Fatalf("concurrent publishers made owner Allow apply %d times, want exactly one", got)
		}
		ft.waitPending(0)
	})
}
