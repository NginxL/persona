package assistant

import (
	"context"
	"maps"
	"time"
)

// An Agent's closeout (or the done judge) can hold a supervised task's delivery for the owner (unblock: user) when
// nothing is up for the owner to answer: often the Agent only announces a later step. currentDelivery reads such a
// hold as the supervisor's (unblockSupervisor), so it stays out of the owner's Needs you, and deliveryHoldSignal hands
// it to the supervisor once to open a real needs_user decision, park the wait, or send the work on (#13191).

const sigDeliveryHold = "delivery_hold" // a supervised owner hold with no owner card: open the decision, park, or go on

// deliveryHoldSettle: how long an unreviewed owner hold waits for its supervisor's own review before the hub hands it
// over (the same settle as an undecidable needs_user).
var deliveryHoldSettle = decisionSettle

// deliveryHoldSignal: supervised task t's delivery is held for the owner by an Agent or the judge, no review came since,
// and its supervisor was not told yet: the signal that hands it over, once (kept on the hold, so a restart does not
// tell it again; a newer hold after a review is told anew).
func (s *Service) deliveryHoldSignal(ctx context.Context, t Todo, now time.Time) (superviseSignal, bool) {
	d := t.Delivery
	if t.DoneAt != nil || !d.held() || !d.SupervisorTold.IsZero() || now.Sub(d.At) < deliveryHoldSettle {
		return superviseSignal{}, false
	}
	if c := currentDelivery(t); c == nil || c.Unblock != unblockSupervisor {
		return superviseSignal{}, false
	}
	if s.reporterStopped(ctx, t.Origin) {
		return superviseSignal{}, false // signal would drop it under /stop (#3308): keep it untold until the user speaks
	}
	if !s.markDeliveryHoldTold(ctx, t.ID, d.At, now) {
		return superviseSignal{}, false
	}
	s.logf("task %s: its delivery waits for the owner with no card: to its supervisor to decide", t.ID)
	return superviseSignal{Todo: t.ID, Kind: sigDeliveryHold, Key: "delivery_hold@" + d.At.UTC().Format(time.RFC3339Nano),
		Minutes: int(now.Sub(d.At).Minutes()), Detail: clip(oneLine(d.Gate), 300)}, true
}

// deliveryHoldStands: the hold signal g told is still the supervisor's to decide (checked again before it goes out).
func deliveryHoldStands(t Todo, g superviseSignal) bool {
	c := currentDelivery(t)
	return t.DoneAt == nil && t.Delivery != nil && c != nil && c.Unblock == unblockSupervisor &&
		"delivery_hold@"+t.Delivery.At.UTC().Format(time.RFC3339Nano) == g.Key
}

// markDeliveryHoldTold keeps when task id's hold recorded at was handed to its supervisor; false when it was told
// already, replaced meanwhile, or could not be kept (then it is not told: the next pass tries again).
func (s *Service) markDeliveryHoldTold(ctx context.Context, id string, at, now time.Time) bool {
	b := &s.reports
	b.stageWriting.Lock()
	defer b.stageWriting.Unlock()
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		return false
	}
	cur := b.stages[id]
	if cur.Delivery == nil || !cur.Delivery.At.Equal(at) || !cur.Delivery.SupervisorTold.IsZero() {
		b.mu.Unlock()
		return false
	}
	d := *cur.Delivery
	d.SupervisorTold = now.UTC()
	cur.Delivery = &d
	next := maps.Clone(b.stages)
	next[id] = cur
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, reportStagesKey, next); err != nil {
			s.logf("task %s: delivery hold: %v", id, err)
			b.mu.Unlock()
			return false
		}
	}
	b.stages = next
	b.mu.Unlock()
	s.todos.mu.Lock()
	if t := s.todos.todos[id]; t != nil {
		t.Delivery = &d
	}
	s.todos.mu.Unlock()
	return true
}
