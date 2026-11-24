package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// scheduledLimitRetry is everything watchScheduledRun's post-limit tail needs. RunOf is the unexported ScheduleRun.of
// field, kept separately so JSON recovery does not turn it into a different zero value.
type scheduledLimitRetry struct {
	Run         ScheduleRun `json:"run"`
	RunOf       time.Time   `json:"run_of"`
	MachineName string      `json:"machine_name"`
	LimitTurn   time.Time   `json:"limit_turn"`
}

// runScheduledLimitRetry waits at the persisted absolute deadline, validates the live schedule, then claims once.
// A crash after the claim cannot become another send: the next manager recovers it as an unknown outcome.
func (s *Service) runScheduledLimitRetry(ctx context.Context, w delayedWait, persisted bool) {
	if w.Retry == nil || !waitForDelayedWait(ctx, w.At) {
		return
	}
	durable := persisted || s.store != nil && s.store() != nil
	if durable && !persisted {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.persistScheduledRetry(pctx, w)
		cancel()
		if err != nil {
			s.rearmScheduledLimitRetry(ctx, w, false, err)
			return
		}
		persisted = true
	}
	cur, due, err := s.scheduledLimitRetryDue(ctx, w)
	if err != nil {
		s.rearmScheduledLimitRetry(ctx, w, persisted, err)
		return
	}
	if !durable {
		if !due {
			return
		}
		s.dispatchScheduledLimitRetry(cur, w)
		return
	}
	// A durable cancellation takes the same claim as a dispatch and revalidates under it. An unclaimed waiter must
	// never delete a wait another manager is about to claim.
	token := randomHex(16)
	claimed, ok, err := s.claimDelayedWait(ctx, w.ID, token, time.Now())
	if err != nil {
		s.rearmScheduledLimitRetry(ctx, w, true, err)
		return
	}
	if !ok {
		return
	}
	// Claiming can retry CAS while the owner edits the task or the conversation advances. Validate again after the
	// claim so the earlier due answer cannot authorize a stale dispatch.
	cur, due, err = s.scheduledLimitRetryDue(ctx, claimed)
	if err != nil {
		s.releaseScheduledLimitRetry(ctx, claimed, token)
		return
	}
	if !due {
		if err := s.completeDelayedWait(context.Background(), claimed.ID, token); err != nil {
			s.logf("cancel scheduled retry %s: %v", claimed.ID, err)
		}
		return
	}
	s.dispatchScheduledLimitRetry(cur, claimed)
	if err := s.completeDelayedWait(context.Background(), claimed.ID, token); err != nil {
		s.logf("finish scheduled retry %s: %v", claimed.ID, err)
	}
}

func (s *Service) scheduledLimitRetryDue(ctx context.Context, w delayedWait) (Task, bool, error) {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	retry := w.Retry
	failed := retry.Run
	cur, ok, err := s.retryStillDueChecked(rctx, failed.RefID, failed)
	if err != nil || !ok {
		return Task{}, false, err
	}
	var res struct {
		Session proto.Session `json:"session"`
		OK      bool          `json:"ok"`
	}
	if err := s.call(rctx, failed.MachineID, DeviceSessionGet, map[string]any{"session_id": failed.SessionID}, &res); err != nil {
		return Task{}, false, err
	}
	// The conversation may have gone on during the long wait. Replaying the task then could repeat work it already did.
	if res.OK && (res.Session.Status == proto.StatusWorking || !sameTurn(res.Session, retry.LimitTurn)) {
		s.logf("task %s run %s: its conversation went on after the usage limit: not retried", failed.RefID, failed.ID)
		return Task{}, false, nil
	}
	return cur, true, nil
}

func (s *Service) dispatchScheduledLimitRetry(cur Task, w delayedWait) {
	failed := w.Retry.Run
	if _, err := s.runTask(cur, time.Now(), time.Now(), false, true); err != nil {
		s.logf("task %s retry: %v", failed.RefID, err)
	}
}

func (s *Service) releaseScheduledLimitRetry(ctx context.Context, w delayedWait, token string) {
	if ctx.Err() != nil {
		return
	}
	nextAt := delayedWaitRetryAt()
	released, err := s.releaseDelayedWait(ctx, w.ID, token, nextAt)
	if err != nil {
		s.logf("retain scheduled retry %s: %v", w.ID, err)
		time.AfterFunc(delayedWaitRecoveryDelay, func() { s.releaseScheduledLimitRetry(s.runCtx(), w, token) })
		return
	}
	if released {
		w.Claim, w.At = "", nextAt
		go s.runScheduledLimitRetry(ctx, w, true)
	}
}

func (s *Service) rearmScheduledLimitRetry(ctx context.Context, w delayedWait, persisted bool, why error) {
	if ctx.Err() != nil {
		return
	}
	s.logf("scheduled retry %s: %v", w.ID, why)
	nextAt := delayedWaitRetryAt()
	if persisted || s.store != nil && s.store() != nil {
		rescheduled, err := s.rescheduleDelayedWait(ctx, w, nextAt, !persisted)
		if err != nil {
			s.logf("rearm scheduled retry %s: %v", w.ID, err)
			time.AfterFunc(delayedWaitRecoveryDelay, func() { s.rearmScheduledLimitRetry(s.runCtx(), w, persisted, why) })
			return
		}
		if !rescheduled {
			return
		}
		persisted = true
	}
	w.At = nextAt
	go s.runScheduledLimitRetry(ctx, w, persisted)
}

// recoverScheduledLimitRetry records the conservative outcome before using the ordinary failure-notification path.
// The original failed run may have been retried or not; without a receiver idempotency contract, sending again is not safe.
func (s *Service) recoverScheduledLimitRetry(ctx context.Context, w delayedWait) bool {
	retry := w.Retry
	if retry == nil {
		return true
	}
	run := retry.Run
	run.of = retry.RunOf
	body := i18n.T(s.lang(ctx), "assistant-schedule.retry_unknown")
	streak, err := s.updateRunResultMissing(run, runFailed, body, true)
	if err != nil {
		s.logf("record recovered scheduled retry %s: %v", w.ID, err)
		return false
	}
	name := run.RefID
	space, text := "", "" // a task gone is named by its id, which says nothing
	if sc, err := s.lockedSchedule(ctx); err == nil {
		if i := sc.indexLocked(run.RefID); i >= 0 {
			t := sc.tasks[i]
			name = t.labelIn(s.lang(ctx))
			space, text = t.Space, name
		}
		sc.mu.Unlock()
	}
	if !streak {
		// the lock screen does not name a task whose name says something sensitive (usage.go, scene 4)
		if s.exitPolicy(ctx, text, run.SessionID, space).Redact {
			name, _ = s.redactedNotice(ctx, s.lang(ctx), space)
		}
		s.push(withCard(ctx, cardScheduleLimitRetry), Notification{Subtitle: retry.MachineName, DeviceID: run.MachineID, SessionID: run.SessionID,
			Title: i18n.T(s.lang(ctx), "assistant-schedule.run_failed", i18n.Args{"name": name}), Body: body})
	}
	if err := s.finishUnknownScheduledRetry(context.Background(), w.ID, w.Claim); err != nil {
		s.logf("finish recovered scheduled retry %s: %v", w.ID, err)
		return false
	}
	return true
}
