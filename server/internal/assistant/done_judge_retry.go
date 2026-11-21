package assistant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
)

// taskDoneDeferredRetries is how many judgment passes the supervision sweep makes after the initial pass (which already
// has one inline retry). With the 30-second sweep and turnRetryIn's 1m/2m backoff, exhaustion takes about four minutes.
// Four retries absorb short provider and parse failures without leaving a stopped task pending indefinitely or letting
// one turn generate unbounded model calls during a longer outage.
const taskDoneDeferredRetries = 4

var errTaskDoneJudgeExhausted = errors.New("judgment service failed")

// deferTaskDoneJudgment records a failed supervised judgment on its durable turn and keeps the turn unjudged while a
// deferred retry remains. A successful later pass overwrites this state with its verdict. A new turn starts at zero.
func (s *Service) deferTaskDoneJudgment(ctx context.Context, t Todo, work time.Time, judgeErr error) (deferred bool, err error) {
	if t.Supervision == nil || judgeErr == nil || ai.IsPaused(judgeErr) || ai.IsNotConfigured(judgeErr) {
		return false, judgeErr
	}
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsLocked(ctx) {
		return false, judgeErr
	}
	e, ok := b.doc.Turns[t.ID]
	if !ok || e.State != turnDone || e.Judged != nil || !e.Work.Equal(work) {
		return false, judgeErr
	}
	e.JudgeFailures++
	e.JudgeError = clip(oneLine(judgeErr.Error()), 300)
	b.doc.Turns[t.ID] = e
	saveErr := s.saveTurnsLocked(ctx)
	if e.JudgeFailures > taskDoneDeferredRetries {
		return false, fmt.Errorf("%w after %d deferred retries: %v", errTaskDoneJudgeExhausted, taskDoneDeferredRetries, judgeErr)
	}
	if saveErr != nil {
		// The turn was already read from the durable ledger. Keep this pass's count in memory rather than wake the
		// supervisor merely because persisting its retry budget failed; the next successful write includes it.
		s.logf("task %s: done judge retry %d/%d kept in memory: %v", t.ID, e.JudgeFailures, taskDoneDeferredRetries, saveErr)
	}
	s.logf("task %s: done judge: %v (deferred retry %d/%d)", t.ID, judgeErr, e.JudgeFailures, taskDoneDeferredRetries)
	return true, nil
}
