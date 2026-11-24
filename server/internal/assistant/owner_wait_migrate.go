package assistant

import (
	"context"
	"strings"
	"time"
)

// judgeOwnerWaitOnly: t waits on the owner only because the done judge said so, the way versions before decision
// rights recorded it (needs_user with an owner wait and no needs_user review of the supervisor's since). A needs_user
// the supervisor wrote with task_review (a review at or after the wait) and a legacy owner_priority park (latest review
// parked) are the supervisor's own and are not this.
func judgeOwnerWaitOnly(t Todo) bool {
	v := t.Supervision
	if t.DoneAt != nil || v == nil || v.State != reviewNeedsUser || v.Waits == nil {
		return false
	}
	r := triageLatestReview(v)
	return r == nil || r.Verdict != reviewParked && r.At.Before(v.Waits.At)
}

// migrateOwnerWaits moves the owner waits earlier versions put straight in front of the owner (needs_user from the
// done judge, pushed at P0) to the supervisor, as noteOwnerWait does now: the to-do leaves Needs you, the wait stays
// so the work still cannot be accepted, and the supervisor is told the user has not been asked; a decision that is
// the owner's goes back to them with task_review needs_user. Once, when the to-dos load; a migrated wait is no
// longer needs_user, so a second pass moves nothing. It returns the to-dos it moved.
func (s *Service) migrateOwnerWaits(ctx context.Context) []string {
	var moved []string
	for _, t := range s.todosOf(ctx, "*") {
		if !judgeOwnerWaitOnly(t) {
			continue
		}
		reason := t.Supervision.Waits.Reason
		_, changed, err := s.updateSupervisionGuarded(ctx, t.ID, func(current Todo) bool {
			return judgeOwnerWaitOnly(current)
		}, func(v *Supervision) bool {
			v.State = ""
			if v.Round > 0 {
				v.State = reviewRework
			}
			return true
		})
		if err != nil {
			s.logf("task %s: owner wait to the supervisor: %v", t.ID, err)
			continue
		}
		if !changed {
			continue
		}
		moved = append(moved, t.ID)
		s.todoPushOver(t.ID) // the hub's needs_user push for it is over
		s.signal(superviseSignal{Todo: t.ID, Kind: sigIdle, Key: t.Supervision.Waits.Work.UTC().Format(time.RFC3339Nano),
			Detail: ownerWaitDetail(reason)})
	}
	if len(moved) > 0 {
		s.logf("owner waits moved to the supervisor (decision rights): %d: %s", len(moved), strings.Join(moved, ", "))
	}
	return moved
}
