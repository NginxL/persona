package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

type detachedTestResult struct {
	res proto.Resolution
	err error
}

func detachedTestCard(id string) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Cwd: "/home/u/.persona/assistant",
		ExecutionCwd: "/home/u/.persona/assistant", Kind: proto.IxPermission,
		ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"printf approved"}`), Detachable: true, Resumable: true,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
}

func startDetachedTest(t *testing.T, ft *fleetTest, ix proto.Interaction) <-chan detachedTestResult {
	t.Helper()
	done := make(chan detachedTestResult, 1)
	go func() {
		res, err := ft.s.nodeInteraction(context.Background(), ix)
		done <- detachedTestResult{res: res, err: err}
	}()
	ft.waitPending(1)
	return done
}

func awaitDetachedTest(t *testing.T, done <-chan detachedTestResult) detachedTestResult {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("the detached command call did not return")
		return detachedTestResult{}
	}
}

func shortDetachedTimers(t *testing.T) {
	t.Helper()
	oldInitial, oldWaiting := approvalInitialWait, approvalWaitingWindow
	approvalInitialWait, approvalWaitingWindow = 20*time.Millisecond, 80*time.Millisecond
	t.Cleanup(func() { approvalInitialWait, approvalWaitingWindow = oldInitial, oldWaiting })
}

// The original decision deadline can be fifteen minutes away. The turn is
// nevertheless released at the short initial wait, and a late Allow applies once.
func TestPreparedApprovalReleasesBeforeItsDecisionDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		var applies atomic.Int32
		done := startShortPrepared(t, ft, "ix_short_initial", time.Hour, func(context.Context, proto.Resolution) (string, error) {
			applies.Add(1)
			return "applied after the owner answered", nil
		})
		elapse(30 * time.Millisecond)
		first := awaitPreparedTest(t, done)
		if first.err != nil || !first.out.Clarifying || first.out.Text != approvalops.WaitingText {
			t.Fatalf("initial result = %+v, %v; want the non-final waiting result", first.out, first.err)
		}
		if !strings.Contains(first.out.Text, "End this turn now") {
			t.Fatalf("waiting result does not release the turn explicitly: %q", first.out.Text)
		}
		if got := len(ft.s.pendingInteractions()); got != 1 {
			t.Fatalf("pending cards = %d, want the retained approval", got)
		}
		allow := true
		if err := ft.s.resolve("ix_short_initial", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		if applies.Load() != 1 {
			t.Fatalf("Apply ran %d times, want once", applies.Load())
		}
		if result := waitApprovalResult(t, ft, "ix_short_initial"); !strings.Contains(result, "applied after the owner answered") {
			t.Fatalf("terminal result = %q", result)
		}
	})
}

// A command approved after its initial hook returned is allowed once for the
// identical retry. A second identical call and a changed input both need their
// own decision, and the original conversation gets the continuation message.
func TestDetachedCommandApprovalContinuesExactlyOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		done := startDetachedTest(t, ft, detachedTestCard("ix_detached_once"))
		elapse(30 * time.Millisecond)
		first := awaitDetachedTest(t, done)
		if first.err != nil || !first.res.Pending || !first.res.Dismiss || first.res.Allow != nil ||
			first.res.ApprovalID != "ix_detached_once" || !strings.Contains(first.res.Message, "The command was not executed") {
			t.Fatalf("initial hook result = %+v, %v", first.res, first.err)
		}
		allow := true
		if err := ft.s.resolve("ix_detached_once", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		result := waitApprovalResult(t, ft, "ix_detached_once")
		for _, want := range []string{"ix_detached_once", "approved", "Nothing has run yet", `{"command":"printf approved"}`} {
			if !strings.Contains(result, want) {
				t.Errorf("continuation message = %q, want %q", result, want)
			}
		}

		// Replaying the original resumable request is not the requested retry and
		// must leave the unused grant available for a new tool invocation.
		replayed, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_detached_once"))
		if err != nil || (replayed.Allow != nil && *replayed.Allow) || !replayed.Dismiss {
			t.Fatalf("replaying the original resumable card consumed its own grant: %+v, %v", replayed, err)
		}

		retry := detachedTestCard("ix_retry_once")
		got, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || got.Allow == nil || !*got.Allow || got.ApprovalID != "ix_detached_once" {
			t.Fatalf("identical retry = %+v, %v; want the original one-time grant", got, err)
		}

		second := startDetachedTest(t, ft, detachedTestCard("ix_retry_second"))
		deny := false
		if err := ft.s.resolve("ix_retry_second", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		if got := awaitDetachedTest(t, second); (got.res.Allow != nil && *got.res.Allow) || got.res.Pending {
			t.Fatalf("a second identical call reused the consumed grant: %+v", got.res)
		}
	})
}

func TestDetachedCommandGrantRejectsChangedInputAndConcurrentReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		done := startDetachedTest(t, ft, detachedTestCard("ix_detached_replay"))
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, done)
		allow := true
		if err := ft.s.resolve("ix_detached_replay", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, "ix_detached_replay")

		changed := detachedTestCard("ix_changed_input")
		changed.ToolInput = json.RawMessage(`{"command":"printf changed"}`)
		changedDone := startDetachedTest(t, ft, changed)
		deny := false
		if err := ft.s.resolve("ix_changed_input", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		awaitDetachedTest(t, changedDone)

		first := make(chan detachedTestResult, 1)
		second := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_race_a"))
			first <- detachedTestResult{res, err}
		}()
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_race_b"))
			second <- detachedTestResult{res, err}
		}()
		ft.waitPending(1) // the loser becomes a fresh card; the winner consumed the only grant
		if err := ft.s.resolve("ix_race_b", proto.Resolution{Allow: &deny}); err != nil {
			// The calls have distinct ids; the pending one is whichever remains.
			if err := ft.s.resolve("ix_race_a", proto.Resolution{Allow: &deny}); err != nil {
				t.Fatal(err)
			}
		}
		allowed := 0
		for _, ch := range []<-chan detachedTestResult{first, second} {
			got := awaitDetachedTest(t, ch)
			if got.err == nil && got.res.Allow != nil && *got.res.Allow {
				allowed++
			}
		}
		if allowed != 1 {
			t.Fatalf("concurrent retries allowed %d calls, want exactly one", allowed)
		}
	})
}

func TestDetachedGrantConsumptionFailureDoesNotAllow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		done := startDetachedTest(t, ft, detachedTestCard("ix_detached_durable"))
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, done)
		allow := true
		if err := ft.s.resolve("ix_detached_durable", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, "ix_detached_durable")
		ft.st.setFailPut(errors.New("synthetic write failure"))
		if got, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_failed_consume")); err == nil || got.Allow != nil {
			t.Fatalf("failed consumption = %+v, %v; want no execution", got, err)
		}
		ft.st.setFailPut(nil)
		got, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_successful_consume"))
		if err != nil || got.Allow == nil || !*got.Allow {
			t.Fatalf("retry after the durable write recovered = %+v, %v", got, err)
		}

		// A fresh manager on the same store cannot consume that grant again. This
		// also proves the successful grant was persisted, not only held in memory.
		restarted := New(Options{Hub: ft.f, Store: func() Store { return ft.st }, Logf: testLogf(t), Devices: ft.d})
		pending := make(chan detachedTestResult, 1)
		go func() {
			res, err := restarted.nodeInteraction(context.Background(), detachedTestCard("ix_after_restart"))
			pending <- detachedTestResult{res, err}
		}()
		waitFor(t, "the restarted manager's fresh pending card", func() bool {
			restarted.fleet.mu.Lock()
			defer restarted.fleet.mu.Unlock()
			return len(restarted.fleet.interactions) == 1
		})
		restarted.fleet.mu.Lock()
		p := restarted.fleet.interactions["ix_after_restart"]
		restarted.fleet.mu.Unlock()
		restarted.endPrepared(p, "cancelled", "test cleanup")
		awaitDetachedTest(t, pending)
	})
}

func TestDetachedApprovalCarriesEditedTextAndTerminalOutcomes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_detached_edit")
		ix.EditableText = "original body"
		done := startDetachedTest(t, ft, ix)
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, done)
		allow := true
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow, EditedText: "edited body"}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, ix.ID)
		retry := ix
		retry.ID = "ix_detached_edit_retry"
		got, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || got.Allow == nil || !*got.Allow || got.EditedText != "edited body" || got.ApprovalID != ix.ID {
			t.Fatalf("edited continuation = %+v, %v", got, err)
		}

		expiring := startDetachedTest(t, ft, detachedTestCard("ix_detached_expiry"))
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, expiring)
		elapse(100 * time.Millisecond)
		ft.waitPending(0)
		if result := waitApprovalResult(t, ft, "ix_detached_expiry"); !strings.Contains(result, "expired") || !strings.Contains(result, "nothing was executed") {
			t.Fatalf("expiry result = %q", result)
		}
		fresh := startDetachedTest(t, ft, detachedTestCard("ix_after_expiry"))
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, fresh) // denial after the caller returned is journalled too
		deny := false
		if err := ft.s.resolve("ix_after_expiry", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		if result := waitApprovalResult(t, ft, "ix_after_expiry"); !strings.Contains(result, "denied") {
			t.Fatalf("denial result = %q", result)
		}
	})
}

func TestImmediateDetachedAllowLeavesNoSecondGrant(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	done := startDetachedTest(t, ft, detachedTestCard("ix_immediate"))
	allow := true
	if err := ft.s.resolve("ix_immediate", proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	if got := awaitDetachedTest(t, done); got.res.Allow == nil || !*got.res.Allow || got.res.Pending {
		t.Fatalf("immediate answer = %+v, %v", got.res, got.err)
	}
	second := startDetachedTest(t, ft, detachedTestCard("ix_immediate_second"))
	deny := false
	if err := ft.s.resolve("ix_immediate_second", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	awaitDetachedTest(t, second)
}

func TestDetachedFingerprintUsesTheExecutionDirectory(t *testing.T) {
	a := detachedTestCard("ix_cwd_a")
	b := a
	b.ID, b.ExecutionCwd = "ix_cwd_b", "/home/u/.persona/assistant/subdir"
	fa, err := detachedFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := detachedFingerprint(b)
	if err != nil {
		t.Fatal(err)
	}
	if fa == fb {
		t.Fatal("identical command text in different execution directories has the same grant fingerprint")
	}
}

func TestOriginalReplayDoesNotSpendGrantBeforeTheRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_original_always")
		ix.AlwaysKeys = []string{"lark:exact-test"}
		done := startDetachedTest(t, ft, ix)
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, done)
		allow := true
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow, Always: true}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, ix.ID)

		replayed, err := ft.s.nodeInteraction(context.Background(), ix)
		if err != nil || (replayed.Allow != nil && *replayed.Allow) || !replayed.Dismiss {
			t.Fatalf("the original request spent its continuation through replay or Always: %+v, %v", replayed, err)
		}
		retry := ix
		retry.ID = "ix_original_always_retry"
		got, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || got.Allow == nil || !*got.Allow || got.ApprovalID != ix.ID {
			t.Fatalf("the new exact retry did not receive the preserved grant: %+v, %v", got, err)
		}
	})
}

func TestInterruptedApprovedGrantIsRevokedDuringRecovery(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_interrupted_apply")
	if err := ft.s.approvalJournalStarted(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.approvalJournalApplying(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}

	restarted := New(Options{Hub: ft.f, Store: func() Store { return ft.st }, Logf: testLogf(t), Devices: ft.d})
	restarted.recoverApprovalJournals(context.Background())
	if got, ok, err := restarted.consumeDetachedGrant(context.Background(), ix); err != nil || ok || got.Allow != nil {
		t.Fatalf("interrupted approval left a spendable grant: %+v, %v, %v", got, ok, err)
	}
	if result := waitFakeApprovalResult(t, ft.f, ix.ID); !strings.Contains(result, "uncertain") {
		t.Fatalf("recovered outcome = %q, want the interrupted-apply warning", result)
	}
}

func TestDetachedDisconnectKeepsResumableCardForReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_disconnect")
		ctx, cancel := context.WithCancel(context.Background())
		first := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(ctx, ix)
			first <- detachedTestResult{res, err}
		}()
		ft.waitPending(1)
		cancel()
		if got := awaitDetachedTest(t, first); got.err != nil || !got.res.Dismiss {
			t.Fatalf("disconnected original = %+v, %v, want reconnectable dismissal", got.res, got.err)
		}
		elapse(30 * time.Millisecond)
		if got := len(ft.s.pendingInteractions()); got != 1 {
			t.Fatalf("the initial timer cancelled the disconnected card; pending = %d", got)
		}

		reconnected := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), ix)
			reconnected <- detachedTestResult{res, err}
		}()
		elapse(30 * time.Millisecond)
		got := awaitDetachedTest(t, reconnected)
		if got.err != nil || !got.res.Pending || got.res.ApprovalID != ix.ID {
			t.Fatalf("reconnected initial wait = %+v, %v", got.res, got.err)
		}
		deny := false
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestConsumedContinuationDoesNotAskForAnotherRetry(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_stale_continuation")
	allow := true
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	retry := ix
	retry.ID = "ix_stale_continuation_retry"
	got, err := ft.s.nodeInteraction(context.Background(), retry)
	if err != nil || got.Allow == nil || !*got.Allow {
		t.Fatalf("matching call did not consume the grant: %+v, %v", got, err)
	}
	entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, Status: "approved",
		Result: approvalResultText(ix.ID, "approved", detachedContinuationText(ix, proto.Resolution{Allow: &allow})), Continuation: true}
	if err := ft.s.deliverApprovalResult(entry); err != nil {
		t.Fatal(err)
	}
	result := waitFakeApprovalResult(t, ft.f, ix.ID)
	if !strings.Contains(result, "Do not retry") || strings.Contains(result, "Retry the same tool now") {
		t.Fatalf("consumed continuation instructed a duplicate retry: %q", result)
	}
}

func TestAlwaysAllowClosesLateSiblingWithoutAnotherContinuation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		source := detachedTestCard("ix_always_source")
		source.AlwaysKeys = []string{"lark:sibling-test"}
		sourceDone := startDetachedTest(t, ft, source)
		sibling := detachedTestCard("ix_always_sibling")
		sibling.AlwaysKeys = []string{"lark:sibling-test"}
		siblingDone := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), sibling)
			siblingDone <- detachedTestResult{res, err}
		}()
		ft.waitPending(2)
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, sourceDone)
		awaitDetachedTest(t, siblingDone)

		allow := true
		if err := ft.s.resolve(source.ID, proto.Resolution{Allow: &allow, Always: true}); err != nil {
			t.Fatal(err)
		}
		if result := waitApprovalResult(t, ft, sibling.ID); !strings.Contains(result, "newer Always allow") || !strings.Contains(result, "no automatic retry") {
			t.Fatalf("covered sibling result = %q", result)
		}
		if got := len(ft.s.pendingInteractions()); got != 0 {
			t.Fatalf("the covered sibling stayed actionable; pending = %d", got)
		}
		ft.s.detached.mu.Lock()
		_, siblingGrant := ft.s.detached.grants[sibling.ID]
		_, sourceGrant := ft.s.detached.grants[source.ID]
		ft.s.detached.mu.Unlock()
		if siblingGrant || !sourceGrant {
			t.Fatalf("grants after Always: sibling=%v source=%v, want no sibling grant and the source's one retry", siblingGrant, sourceGrant)
		}
	})
}
