package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

type preparedTestResult struct {
	out approvalops.Outcome
	err error
}

func preparedTestConversation(ft *fleetTest) {
	sess := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true,
		Agent: "claude", Status: proto.StatusIdle, State: proto.StateIdle}
	ft.f.setSessions("mac", sess)
	ft.d.setSessions("mac", []proto.Session{sess})
}

func startPreparedTest(t *testing.T, ft *fleetTest, id, replacement string, apply func(context.Context, proto.Resolution) (string, error)) <-chan preparedTestResult {
	t.Helper()
	if apply == nil {
		apply = func(context.Context, proto.Resolution) (string, error) { return "applied", nil }
	}
	ix := proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
		ToolName: "persona_settings", Summary: "Change the example setting", ExpiresAt: time.Now().Add(time.Hour)}
	done := make(chan preparedTestResult, 1)
	go func() {
		out, err := ft.s.waitPreparedCard(context.Background(), ix, approvalops.Operation{Replacement: replacement, Apply: apply})
		done <- preparedTestResult{out: out, err: err}
	}()
	ft.waitPending(1)
	t.Cleanup(func() {
		ft.s.fleet.mu.Lock()
		p := ft.s.fleet.interactions[id]
		ft.s.fleet.mu.Unlock()
		if p != nil {
			ft.s.endPrepared(p, "cancelled", "test cleanup")
		}
	})
	return done
}

func awaitPreparedTest(t *testing.T, done <-chan preparedTestResult) preparedTestResult {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("the original prepared tool call did not return")
		return preparedTestResult{}
	}
}

func ownerQuestionSends(ft *fleetTest) []map[string]any {
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	out := make([]map[string]any, len(ft.f.sentOnce))
	copy(out, ft.f.sentOnce)
	return out
}

func waitApprovalResult(t *testing.T, ft *fleetTest, id string) string {
	t.Helper()
	return waitFakeApprovalResult(t, ft.f, id)
}

func waitFakeApprovalResult(t *testing.T, f *fakeHub, id string) string {
	t.Helper()
	var result string
	waitFor(t, "approval "+id+" terminal result", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		var found []string
		for _, p := range f.sends {
			text, _ := p["text"].(string)
			if strings.Contains(text, "Approval "+id+" ") {
				found = append(found, text)
			}
		}
		if len(found) > 1 {
			t.Fatalf("approval %s delivered %d terminal results: %q", id, len(found), found)
		}
		if len(found) == 1 {
			result = found[0]
			return true
		}
		return false
	})
	return result
}

func TestClarifiedApprovalQuestionThenAllowOnce(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	var applies atomic.Int32
	done := startPreparedTest(t, ft, "ix_question_allow", "", func(_ context.Context, r proto.Resolution) (string, error) {
		if !r.UserAllowed() {
			t.Errorf("apply received a non-owner resolution: %+v", r)
		}
		applies.Add(1)
		return "setting applied", nil
	})
	question := "  Why is this change needed?\nPlease be specific.  "
	if err := ft.s.clarify(context.Background(), "ix_question_allow", question, "owner-message-1", nil); err != nil {
		t.Fatalf("clarify: %v", err)
	}
	first := awaitPreparedTest(t, done)
	if first.err != nil || !first.out.Clarifying || first.out.Text != approvalops.ClarifyingText {
		t.Fatalf("original tool result = %+v, %v", first.out, first.err)
	}
	if !strings.Contains(first.out.Text, "remains valid") || !strings.Contains(first.out.Text, "do not retry the tool or create another approval") {
		t.Fatalf("clarifying result must preserve the original card and forbid a replacement: %q", first.out.Text)
	}
	ft.waitPending(1)
	sent := ownerQuestionSends(ft)
	if len(sent) != 1 || sent[0]["session_id"] != "s1" || sent[0]["text"] != question || sent[0]["client_msg_id"] != "owner-message-1" {
		b, _ := json.Marshal(sent)
		t.Fatalf("question delivery = %s", b)
	}
	if applies.Load() != 0 {
		t.Fatal("a question executed the operation")
	}
	allow := true
	if err := ft.s.resolve("ix_question_allow", proto.Resolution{Allow: &allow}); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if err := ft.s.resolve("ix_question_allow", proto.Resolution{Allow: &allow}); err == nil {
		t.Fatal("a second Allow was accepted")
	}
	if applies.Load() != 1 {
		t.Fatalf("apply count = %d, want exactly one", applies.Load())
	}
	ft.waitPending(0)
	result := waitApprovalResult(t, ft, "ix_question_allow")
	if !strings.Contains(result, "applied") || !strings.Contains(result, "setting applied") {
		t.Fatalf("terminal result = %q", result)
	}
}

func TestClarifiedApprovalConcurrentAllowClaimsOnce(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	var applies atomic.Int32
	started := make(chan struct{})
	blocked := make(chan struct{})
	var unblock sync.Once
	release := func() { unblock.Do(func() { close(blocked) }) }
	defer release()
	done := startPreparedTest(t, ft, "ix_concurrent_allow", "", func(context.Context, proto.Resolution) (string, error) {
		if applies.Add(1) == 1 {
			close(started)
			<-blocked
		}
		return "setting applied", nil
	})
	if err := ft.s.clarify(context.Background(), "ix_concurrent_allow", "Question", "owner-message-concurrent", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	ft.s.fleet.mu.Lock()
	p := ft.s.fleet.interactions["ix_concurrent_allow"]
	ft.s.fleet.mu.Unlock()
	allow := true
	first := make(chan error, 1)
	go func() {
		first <- ft.s.resolvePreparedAnswer(context.Background(), p, proto.Resolution{Allow: &allow}, false, nil)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first Allow did not start its apply")
	}
	second := make(chan error, 1)
	go func() {
		second <- ft.s.resolvePreparedAnswer(context.Background(), p, proto.Resolution{Allow: &allow}, false, nil)
	}()
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("a concurrent second Allow was accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a concurrent second Allow entered apply instead of losing the final claim")
	}
	if got := applies.Load(); got != 1 {
		t.Fatalf("apply count while the first Allow is in flight = %d, want one", got)
	}
	release()
	if err := <-first; err != nil {
		t.Fatalf("first Allow: %v", err)
	}
	if got := applies.Load(); got != 1 {
		t.Fatalf("final apply count = %d, want exactly one", got)
	}
	waitApprovalResult(t, ft, "ix_concurrent_allow")
}

func TestClarifiedApprovalDenyDoesNothing(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	var applies atomic.Int32
	done := startPreparedTest(t, ft, "ix_question_deny", "", func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "unexpected", nil
	})
	if err := ft.s.clarify(context.Background(), "ix_question_deny", "What changes?", "owner-message-deny", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	deny := false
	if err := ft.s.resolve("ix_question_deny", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	if applies.Load() != 0 {
		t.Fatal("Deny executed the retained operation")
	}
	if result := waitApprovalResult(t, ft, "ix_question_deny"); !strings.Contains(result, "denied") {
		t.Fatalf("terminal result = %q", result)
	}
}

func TestClarifyRepeatedQuestionsReuseOnePendingCard(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	done := startPreparedTest(t, ft, "ix_repeated", "", nil)
	if err := ft.s.clarify(context.Background(), "ix_repeated", "First question", "owner-message-first", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	if err := ft.s.clarify(context.Background(), "ix_repeated", "Second question", "owner-message-second", nil); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.clarify(context.Background(), "ix_repeated", "Second question", "owner-message-second", nil); err != nil {
		t.Fatalf("an unchanged retry should deduplicate: %v", err)
	}
	if err := ft.s.clarify(context.Background(), "ix_repeated", "Different text", "owner-message-second", nil); err == nil {
		t.Fatal("a delivery ID was reused for different text")
	}
	if got := len(ownerQuestionSends(ft)); got != 2 {
		t.Fatalf("question sends = %d, want two distinct questions and one deduplicated retry", got)
	}
	ft.waitPending(1)
	deny := false
	if err := ft.s.resolve("ix_repeated", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_repeated")
}

func TestClarifyQuestionLengthAndBlankValidation(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	done := startPreparedTest(t, ft, "ix_lengths", "", nil)
	for _, question := range []string{"", " \n\t ", strings.Repeat("界", 1001)} {
		if err := ft.s.clarify(context.Background(), "ix_lengths", question, "owner-message-invalid", nil); err == nil {
			t.Fatalf("invalid question of %d runes was accepted", len([]rune(question)))
		}
	}
	select {
	case got := <-done:
		t.Fatalf("an invalid question released the tool: %+v", got)
	default:
	}
	if got := len(ownerQuestionSends(ft)); got != 0 {
		t.Fatalf("invalid questions produced %d sends", got)
	}
	question := "  " + strings.Repeat("界", 1000) + "  "
	if err := ft.s.clarify(context.Background(), "ix_lengths", question, "owner-message-limit", nil); err != nil {
		t.Fatalf("a 1,000-code-point question was rejected: %v", err)
	}
	awaitPreparedTest(t, done)
	if got := ownerQuestionSends(ft); len(got) != 1 || got[0]["text"] != question {
		t.Fatal("the accepted boundary question was not preserved verbatim")
	}
	deny := false
	if err := ft.s.resolve("ix_lengths", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_lengths")
}

func TestClarifiedApprovalPauseHasAbsoluteBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := clarificationPauseBudget
		clarificationPauseBudget = 100 * time.Millisecond
		t.Cleanup(func() { clarificationPauseBudget = old })
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		done := startPreparedTest(t, ft, "ix_pause_budget", "", nil)
		if err := ft.s.clarify(context.Background(), "ix_pause_budget", "Please explain", "owner-message-budget", nil); err != nil {
			t.Fatal(err)
		}
		awaitPreparedTest(t, done)
		elapse(180 * time.Millisecond)
		ft.waitPending(0)
		result := waitApprovalResult(t, ft, "ix_pause_budget")
		for _, want := range []string{"ix_pause_budget", "expired", "nothing was executed"} {
			if !strings.Contains(result, want) {
				t.Errorf("terminal result = %q, want it to contain %q", result, want)
			}
		}
	})
}

func TestClarifiedApprovalRepeatedQuestionsDoNotResetPauseBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := clarificationPauseBudget
		clarificationPauseBudget = 300 * time.Millisecond
		t.Cleanup(func() { clarificationPauseBudget = old })
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		done := startPreparedTest(t, ft, "ix_pause_repeated", "", nil)
		if err := ft.s.clarify(context.Background(), "ix_pause_repeated", "First", "owner-message-pause-1", nil); err != nil {
			t.Fatal(err)
		}
		awaitPreparedTest(t, done)
		elapse(200 * time.Millisecond)
		if err := ft.s.clarify(context.Background(), "ix_pause_repeated", "Second", "owner-message-pause-2", nil); err != nil {
			t.Fatal(err)
		}
		elapse(180 * time.Millisecond)
		// 380ms after the first question, but only 180ms after the second. Resetting
		// the budget per question would incorrectly leave this card alive.
		ft.waitPending(0)
		waitApprovalResult(t, ft, "ix_pause_repeated")
	})
}

func TestClarifiedApprovalAnswerEndRestoresRemainingDecisionTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		ix := proto.Interaction{ID: "ix_pause_resume", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
			ExpiresAt: time.Now().Add(250 * time.Millisecond)}
		done := make(chan preparedTestResult, 1)
		go func() {
			out, err := ft.s.waitPreparedCard(context.Background(), ix, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
				return "unexpected", nil
			}})
			done <- preparedTestResult{out: out, err: err}
		}()
		ft.waitPending(1)
		if err := ft.s.clarify(context.Background(), "ix_pause_resume", "Please explain", "owner-message-resume", nil); err != nil {
			t.Fatal(err)
		}
		awaitPreparedTest(t, done)
		elapse(350 * time.Millisecond)
		if got := len(ft.s.pendingInteractions()); got != 1 {
			t.Fatalf("the card expired while its answer was outstanding: pending=%d", got)
		}
		ref := SessionRef{MachineID: "mac", SessionID: "s1"}
		ft.s.approvalAnswerTurnStarted(ref)
		ft.s.approvalAnswerTurnEnded(ref)
		ft.waitPending(0)
		result := waitApprovalResult(t, ft, "ix_pause_resume")
		for _, want := range []string{"ix_pause_resume", "expired", "nothing was executed"} {
			if !strings.Contains(result, want) {
				t.Errorf("terminal result = %q, want it to contain %q", result, want)
			}
		}
	})
}

func TestEquivalentApprovalReplacesClarifiedCard(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	var oldApplies, newApplies atomic.Int32
	oldDone := startPreparedTest(t, ft, "ix_old", "same-setting", func(context.Context, proto.Resolution) (string, error) {
		oldApplies.Add(1)
		return "old applied", nil
	})
	if err := ft.s.clarify(context.Background(), "ix_old", "Is this current?", "owner-message-old", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, oldDone)
	newDone := make(chan preparedTestResult, 1)
	go func() {
		out, err := ft.s.waitPreparedCard(context.Background(), proto.Interaction{ID: "ix_new", MachineID: "mac", SessionID: "s1",
			Kind: proto.IxPermission, ExpiresAt: time.Now().Add(time.Hour)}, approvalops.Operation{Replacement: "same-setting",
			Apply: func(context.Context, proto.Resolution) (string, error) { newApplies.Add(1); return "new applied", nil }})
		newDone <- preparedTestResult{out: out, err: err}
	}()
	if got := waitApprovalResult(t, ft, "ix_old"); !strings.Contains(got, "replaced") || !strings.Contains(got, "ix_new") {
		t.Fatalf("replacement result = %q", got)
	}
	allow := true
	if err := ft.s.resolve("ix_old", proto.Resolution{Allow: &allow}); err == nil {
		t.Fatal("the replaced card still accepted Allow")
	}
	if err := ft.s.resolve("ix_new", proto.Resolution{Allow: &allow}); err != nil {
		t.Fatalf("the fresh card did not accept Allow: %v", err)
	}
	if got := awaitPreparedTest(t, newDone); got.err != nil || got.out.Text != "new applied" {
		t.Fatalf("fresh tool result = %+v, %v", got.out, got.err)
	}
	if oldApplies.Load() != 0 || newApplies.Load() != 1 {
		t.Fatalf("apply counts: old=%d new=%d", oldApplies.Load(), newApplies.Load())
	}
}

func TestClarifiedApprovalRejectsAttributedAndRememberedAllow(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    proto.Resolution
	}{
		{name: "assistant", r: proto.Resolution{AnsweredBy: proto.AnsweredByAssistant}},
		{name: "remembered", r: proto.Resolution{Remembered: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			preparedTestConversation(ft)
			var applies atomic.Int32
			done := startPreparedTest(t, ft, "ix_attributed_"+tc.name, "", func(context.Context, proto.Resolution) (string, error) {
				applies.Add(1)
				return "unexpected", nil
			})
			if err := ft.s.clarify(context.Background(), "ix_attributed_"+tc.name, "Question", "owner-message-"+tc.name, nil); err != nil {
				t.Fatal(err)
			}
			awaitPreparedTest(t, done)
			allow := true
			tc.r.Allow = &allow
			ft.s.fleet.mu.Lock()
			p := ft.s.fleet.interactions["ix_attributed_"+tc.name]
			ft.s.fleet.mu.Unlock()
			err := ft.s.resolvePreparedAnswer(context.Background(), p, tc.r, false, nil)
			if tc.name == "remembered" {
				if err == nil || applies.Load() != 0 {
					t.Fatalf("remembered Allow: err=%v applies=%d", err, applies.Load())
				}
				deny := false
				if err := ft.s.resolve(p.ix.ID, proto.Resolution{Allow: &deny}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err != nil || applies.Load() != 0 {
					t.Fatalf("attributed Allow: err=%v applies=%d", err, applies.Load())
				}
			}
			ft.waitPending(0)
			waitApprovalResult(t, ft, p.ix.ID)
		})
	}
}

func TestClarifiedApprovalRestartReportsCancellation(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	var applies atomic.Int32
	done := startPreparedTest(t, ft, "ix_restart", "", func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "unexpected", nil
	})
	if err := ft.s.clarify(context.Background(), "ix_restart", "Question", "owner-message-restart", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	ft.s.recoverApprovalJournals(context.Background())
	if applies.Load() != 0 {
		t.Fatal("restart recovery executed a process-local operation")
	}
	result := waitApprovalResult(t, ft, "ix_restart")
	if !strings.Contains(result, "cancelled") || !strings.Contains(result, "without executing") {
		t.Fatalf("restart result = %q", result)
	}
}

func TestClarifiedApprovalRestartReportsInterruptedApplyAsUncertain(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	if err := ft.s.approvalJournalStarted(context.Background(), proto.Interaction{ID: "ix_interrupted", MachineID: "mac", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.approvalJournalApplying(context.Background(), proto.Interaction{ID: "ix_interrupted", MachineID: "mac", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	ft.s.recoverApprovalJournals(context.Background())
	result := waitApprovalResult(t, ft, "ix_interrupted")
	if !strings.Contains(result, "uncertain") || strings.Contains(result, "cancelled without executing") {
		t.Fatalf("interrupted apply result = %q", result)
	}
}

func TestClarifyFailureKeepsOriginalToolWaiting(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	ft.f.mu.Lock()
	ft.f.failSendOnce = map[string]error{"mac/s1": errors.New("node refused the message")}
	ft.f.mu.Unlock()
	var applies atomic.Int32
	done := startPreparedTest(t, ft, "ix_send_failure", "", func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "unexpected", nil
	})
	if err := ft.s.clarify(context.Background(), "ix_send_failure", "Question", "owner-message-failure", nil); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("clarify error = %v", err)
	}
	select {
	case got := <-done:
		t.Fatalf("a refused question released the original tool: %+v", got)
	default:
	}
	ft.waitPending(1)
	if applies.Load() != 0 {
		t.Fatal("a refused question executed the operation")
	}
	deny := false
	if err := ft.s.resolve("ix_send_failure", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	got := awaitPreparedTest(t, done)
	if got.err != nil || got.out.Clarifying || !strings.Contains(got.out.Text, "declined") {
		t.Fatalf("denied original tool result = %+v, %v", got.out, got.err)
	}
}
