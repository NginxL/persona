package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// startJudgeRecovery takes one closed agent turn through the supervision sweep without a separate turn-ended event.
func startJudgeRecovery(t *testing.T, s *Service, f *fakeHub, todo Todo, reply string) {
	t.Helper()
	f.pageTurns = true
	f.setItems(turnPage("turn-1", reply, true)...)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, reply, 0)
	waitFor(t, "the initial judgment to start", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.judged > 0
	})
	turnsQuiet(t, s, todo.ID)
}

// A call failure is not an unjudged outcome. The existing sweep retries it with backoff and tells the supervisor once,
// with the service failure named, only after the bounded retries have all failed.
func TestSupervisedJudgeServiceErrorRetriesThenReportsOnce(t *testing.T) {
	fastJudgeRetry(t)
	shortTurnRetries(t, 0)
	s, f, todo := newSupervisedService(t)
	f.judge = "not-json" // a 200 reply that cannot be parsed as a judgment
	startJudgeRecovery(t, s, f, todo, "the work is finished")
	if got := f.judged; got != 2 {
		t.Fatalf("the initial pass made %d calls, want the existing immediate retry (2)", got)
	}
	for retry := 0; retry < 4; retry++ {
		if got := stopSignals(s, todo.ID); got != "" {
			t.Fatalf("before deferred retry %d: told %q instead of waiting", retry+1, got)
		}
		sweeps(t, s, todo.ID, 1)
	}
	waitFor(t, "one exhausted judgment-service signal", func() bool { return len(f.notices()) == 1 })
	if got := f.judged; got != 6 {
		t.Fatalf("model calls = %d, want two immediate plus four deferred (6)", got)
	}
	n := entryLine(f.notices()[0])
	if !strings.Contains(n, "signal=unjudged") || !strings.Contains(n, "判定服务出错（自动重判 4 次仍失败）") {
		t.Fatalf("the exhausted signal does not name the service failure and retries:\n%s", n)
	}
	before := f.judged
	stillTurns(t, "no duplicate after exhaustion", turnCounts{receipts: 1, judgments: 6, messages: 1}, s, todo.ID, f)
	if f.judged != before {
		t.Fatalf("the exhausted turn was judged again: %d → %d", before, f.judged)
	}
}

// A request failure that recovers before the deferred retry produces no unjudged signal. The retry reads the reply as
// it is then, rather than judging the snapshot that failed earlier.
func TestSupervisedJudgeRetryUsesLatestReply(t *testing.T) {
	fastJudgeRetry(t)
	shortTurnRetries(t, 0)
	s, f, todo := newSupervisedService(t)
	f.judgeFails = 2 // both calls of the initial pass fail; the deferred pass can answer
	startJudgeRecovery(t, s, f, todo, "the earlier reply")
	if got := stopSignals(s, todo.ID); got != "" {
		t.Fatalf("the failed initial pass told %q before its deferred retry", got)
	}
	row := turnRow(proto.StatusDone, 4, "the latest reply")
	f.setItems(turnPage("turn-1", "the latest reply", true)...)
	f.setSessions("box", row)
	s.tasks.mu.Lock()
	s.tasks.working[todo.ID] = time.Now() // a start heard while the retry was pending: judge that stretch, not e.Work
	s.tasks.mu.Unlock()
	s.watchTurn(context.Background(), todo, []proto.Session{row})
	turnsQuiet(t, s, todo.ID)
	waitFor(t, "the recovered review", func() bool { return len(f.notices()) == 1 })
	if got := f.judged; got != 3 {
		t.Fatalf("model calls = %d, want two failed calls and one deferred success", got)
	}
	f.mu.Lock()
	last := f.judgeAsked[len(f.judgeAsked)-1]
	f.mu.Unlock()
	if !strings.Contains(last, "the latest reply") || strings.Contains(last, "the earlier reply") {
		t.Fatalf("the deferred judgment did not read the latest reply:\n%s", last)
	}
	if got := entryLine(f.notices()[0]); !strings.Contains(got, "signal=review") || strings.Contains(got, "没判断出来") {
		t.Fatalf("the recovered judgment produced the wrong signal:\n%s", got)
	}
}

// An expired judge context still belongs to the service-failure path: it gets the same bounded deferred retries and
// exhausted wording rather than falling through to the generic "judgment did not land" cap.
func TestSupervisedJudgeTimeoutUsesDeferredRetry(t *testing.T) {
	oldTimeout := taskDoneTimeout
	taskDoneTimeout = 50 * time.Millisecond
	t.Cleanup(func() { taskDoneTimeout = oldTimeout })
	shortTurnRetries(t, 0)
	s, f, todo := newSupervisedService(t)
	f.mu.Lock()
	f.judgeDelay = time.Second // the caller's much shorter context expires first
	f.sessionsContextCheck = true
	f.mu.Unlock()
	startJudgeRecovery(t, s, f, todo, "the work is finished")
	if got := f.judged; got != 1 {
		t.Fatalf("a timed-out initial call was retried inline: %d calls, want 1", got)
	}
	for retry := 0; retry < 4; retry++ {
		if got := stopSignals(s, todo.ID); got != "" {
			t.Fatalf("before deferred retry %d after timeout: told %q", retry+1, got)
		}
		sweeps(t, s, todo.ID, 1)
	}
	waitFor(t, "one exhausted timeout signal", func() bool { return len(f.notices()) == 1 })
	if got := f.judged; got != 5 {
		t.Fatalf("model calls = %d, want one initial plus four deferred (5)", got)
	}
	if got := entryLine(f.notices()[0]); !strings.Contains(got, "signal=unjudged") || !strings.Contains(got, "判定服务出错（自动重判 4 次仍失败）") {
		t.Fatalf("the exhausted timeout was mislabeled:\n%s", got)
	}
}

// A valid unsure verdict is a judgment, not a service failure: it keeps the existing immediate unjudged behavior.
func TestSupervisedJudgeUnsureStillReportsImmediately(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	f.judge = `{"verdict":"unsure","reason":"not enough evidence"}`
	startJudgeRecovery(t, s, f, todo, "the work is finished")
	if got := stopSignals(s, todo.ID); got != sigUnjudged {
		t.Fatalf("a valid unsure judgment was deferred or changed: %q", got)
	}
	if got := f.judged; got != 1 {
		t.Fatalf("a valid unsure judgment made %d model calls, want 1", got)
	}
	waitFor(t, "the immediate unjudged signal", func() bool { return len(f.notices()) == 1 })
	if got := entryLine(f.notices()[0]); !strings.Contains(got, "看不出做没做完") || strings.Contains(got, "判定服务出错") {
		t.Fatalf("a valid unsure judgment was mislabeled:\n%s", got)
	}
}
