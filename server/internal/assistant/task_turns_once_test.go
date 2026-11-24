package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A turn is judged once, whichever comes first: a sweep's look, or the list's start and end (#13711). Synthetic
// machine "box", session "s1".

func sweepLooksDone(s *Service, todo string) bool {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.recovering[todo]
}

func judgedCalls(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.judged
}

// sweepTakesUpBeforeListedTurn has a sweep take s1's turn up while the list's start (at startRev) and end wait in the
// inOrder queue, as behind a busy queue. It answers what lets that queue go on. The sweep's judgment is out until the
// fake judge answers, after f.judgeDelay.
func sweepTakesUpBeforeListedTurn(t *testing.T, s *Service, f *fakeHub, todo string, startRev int64) (release func()) {
	t.Helper()
	ctx := context.Background()
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo, 1)
	block := make(chan struct{})
	s.inOrder(func() { <-block })
	nodeListed(s, f, turnRow(proto.StatusWorking, startRev, ""))
	f.setItems(turnPage("turn-1", "修好了", true)...)
	nodeListed(s, f, turnRow(proto.StatusDone, 3, "修好了"))
	s.superviseWatch(ctx, time.Now()) // it reads the turn closed, its start not heard
	waitFor(t, "the sweep's judgment to go out", func() bool { return judgedCalls(f) >= 1 })
	return func() { close(block) }
}

// The list's start, handled after a sweep took the turn up and while that judgment is out, opened a new stretch: the
// judgment was dropped as out of date and the queued end judged the turn again, a second model call.
func TestTurnJudgedOnceWhenTheListCatchesUpWithASweep(t *testing.T) {
	s, f, todo := newSupervisedServiceOn(t, taskRecoveryDatabase(t))
	f.mu.Lock()
	f.judgeDelay = 300 * time.Millisecond
	f.mu.Unlock()
	release := sweepTakesUpBeforeListedTurn(t, s, f, todo.ID, 2)
	release()
	waitFor(t, "the turn's judgment", func() bool {
		e, ok := s.turnOf(context.Background(), todo.ID)
		return ok && e.Judged != nil
	})
	turnsQuiet(t, s, todo.ID)
	stillTurns(t, "a sweep's take-up, then the list", oneEach, s, todo.ID, f)
	if now := todoNow(s, todo.ID); now.Supervision.State != reviewPending {
		t.Fatalf("not waiting for review: %+v", now.Supervision)
	}
}

// A look that finds the same turn's judgment out leaves a follow-up rather than judge alongside it. That judgment does
// not land here (it fails, and its stretch moved on: a start with no rev to compare is a new stretch); the follow-up
// then judges, and the turn has its judgment.
func TestTurnJudgedAgainWhenTheJudgmentOutFails(t *testing.T) {
	fastJudgeRetry(t)
	s, f, todo := newSupervisedServiceOn(t, taskRecoveryDatabase(t))
	f.mu.Lock()
	f.judgeDelay = 300 * time.Millisecond
	f.judgeFails = 2 // the sweep's call and its immediate retry
	f.mu.Unlock()
	release := sweepTakesUpBeforeListedTurn(t, s, f, todo.ID, 0)
	release()
	waitFor(t, "the turn's judgment", func() bool {
		e, ok := s.turnOf(context.Background(), todo.ID)
		return ok && e.Judged != nil && e.Judged.Err == ""
	})
	turnsQuiet(t, s, todo.ID)
	if n := judgedCalls(f); n != 3 {
		t.Fatalf("judge calls = %d, want the two that failed and one after them", n)
	}
	if now := todoNow(s, todo.ID); now.Supervision.State != reviewPending {
		t.Fatalf("not waiting for review: %+v", now.Supervision)
	}
}

// The look that finds the turn's judgment out does not wait for it: it returns at once, with no second judgment
// alongside, and the turn is judged after that one (dropped here: the stretch moved on) has ended.
func TestTurnLookDoesNotWaitForAJudgmentOut(t *testing.T) {
	s, f, todo := newSupervisedServiceOn(t, taskRecoveryDatabase(t))
	const judging = time.Second
	f.mu.Lock()
	f.judgeDelay = judging
	f.mu.Unlock()
	release := sweepTakesUpBeforeListedTurn(t, s, f, todo.ID, 0)
	ctx := context.Background()
	s.taskWorking(ctx, "box", "s1", startRow{}) // a start with nothing to compare: a new stretch, as before #13711
	began := time.Now()
	got := s.settleTurn(ctx, "box", "s1", "")
	if took := time.Since(began); took >= judging/2 {
		t.Fatalf("the look waited %s for the judgment out", took)
	}
	if got != settleTaken || judgedCalls(f) != 1 {
		t.Fatalf("settle = %v with %d judge calls, want taken with only the one out", got, judgedCalls(f))
	}
	waitFor(t, "the turn's judgment", func() bool {
		e, ok := s.turnOf(ctx, todo.ID)
		return ok && e.Judged != nil
	})
	release()
	turnsQuiet(t, s, todo.ID)
}

// Rev is the transcript's size. A rewrite can make it smaller, and a real new start then has a smaller rev than the
// turn taken up: its newer time still opens a new stretch. A start older than that turn by both does not.
func TestStartAfterTheTakenTurnOpensAStretchWhateverItsRev(t *testing.T) {
	s, f, todo := newSupervisedServiceOn(t, taskRecoveryDatabase(t))
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	sweeps(t, s, todo.ID, 1)
	stale := turnRow(proto.StatusWorking, 2, "") // listed before the end below, and handled after it was taken up
	nodeListed(s, f, stale)
	f.setItems(turnPage("turn-1", "修好了", true)...)
	nodeListed(s, f, turnRow(proto.StatusDone, 30, "修好了"))
	wantTurns(t, "the turn", oneEach, s, todo.ID, f)
	turnsQuiet(t, s, todo.ID)

	before := workMark(s, todo.ID)
	s.taskWorking(context.Background(), "box", "s1", startRow{Rev: stale.Rev, At: stale.UpdatedAt})
	if !workMark(s, todo.ID).Equal(before) {
		t.Fatal("a start older than the turn taken up opened a new stretch")
	}
	rewritten := turnRow(proto.StatusWorking, 5, "") // smaller than 30: the transcript was rewritten; a newer time
	nodeListed(s, f, rewritten)
	startTaken(t, s, todo.ID, before)
	waitFor(t, "the new stretch to end the review wait", func() bool { return todoNow(s, todo.ID).Supervision.State != reviewPending })
}
