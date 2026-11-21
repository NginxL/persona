package assistant

import (
	"context"
	"time"
)

// A direction's lead that a hand-over of the owner's quoted words was sent into waits for the owner's Start before the
// writable tasks it plans (#11699, docs/assistant/unified-entry.md "Across a relay"). The main conversation picks which
// lead gets the owner's words, and it can pick the wrong one: a writable task the lead plans on them with task_plan,
// marked user_requested, would start in the wrong repository before the owner sees it. So from the moment such a
// hand-over is sent into the lead, task_plan there ignores user_requested, whatever opened the turn, until the owner's
// next message to that conversation is submitted (userSpoke). The mark is saved with the relay state
// (relayState.Handed) before the send leaves (holdForHandover), since a try that ends in doubt, or a manager restart
// during it, has opened the lead's turn before any answer or look says so; it is taken back only when the try was
// definitely not taken and the lead had no mark before. It has no expiry and holds after the conversation stops being
// a lead. Only task_plan reads it: agent_start, agent_send with resume and the lead's own tools are unchanged, and so is
// everything that is no such hand-over (an unquoted message, an answer, a hand-over still queued and never tried, a
// conversation that is no lead, the main conversation). Read-only tasks and kinds the owner trusts start as before.

// holdForHandover: message msg is about to be sent into conversation to (deliverRelay). When it is a hand-over of the
// owner's quoted words, still kept, and to is a direction's lead, the lead waits from now, saved before the send
// leaves. done takes the try's outcome: a try definitely not taken (sendNotTaken) takes back a mark this try set.
func (s *Service) holdForHandover(to string, msg relayMsg) (done func(err error)) {
	if msg.Handover == "" || !s.isLeader(context.Background(), to) || !s.handoverKept(msg.Handover) {
		return func(error) {}
	}
	now := time.Now().UTC()
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) {
		b.mu.Unlock()
		return func(error) {}
	}
	_, had := b.st.Handed[to]
	if b.st.Handed == nil {
		b.st.Handed = map[string]time.Time{}
	}
	b.st.Handed[to] = now
	s.saveRelayLocked()
	b.mu.Unlock()
	if !had {
		s.logf("lead %s: a hand-over of the owner's words is sent; its writable plans wait for Start until the owner writes there", short(to))
	}
	return func(err error) {
		if err == nil || had || !sendNotTaken(err) {
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if at, ok := b.st.Handed[to]; ok && at.Equal(now) { // not set again, nor ended by the owner, meanwhile
			delete(b.st.Handed, to)
			s.saveRelayLocked()
			s.logf("lead %s: the hand-over did not go in; its plans start as before", short(to))
		}
	}
}

// noteHandedOver: message msg went into conversation to (relayDelivered). A hand-over that went into a lead marks it,
// also when its try was sent by a version that did not mark it before the send.
func (s *Service) noteHandedOver(to string, msg relayMsg) {
	if msg.Handover == "" || !s.isLeader(context.Background(), to) {
		return
	}
	if e, ok := s.handoverEntry(msg.Handover); !ok || e.To != to || e.State != handoverDelivered && e.State != handoverAnswered {
		return
	}
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	if b.st.Handed == nil {
		b.st.Handed = map[string]time.Time{}
	}
	if _, ok := b.st.Handed[to]; !ok {
		b.st.Handed[to] = time.Now().UTC()
		s.saveRelayLocked()
	}
}

// handedOver: a hand-over of the owner's quoted words was sent into conversation id since the owner last wrote there.
func (s *Service) handedOver(id string) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false
	}
	_, ok := b.st.Handed[id]
	return ok
}

// handoverEntry is hand-over id as kept now; false when there is none.
func (s *Service) handoverEntry(id string) (handover, bool) {
	b := &s.handovers
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loadLocked(context.Background(), s) {
		return handover{}, false
	}
	e, ok := b.m[id]
	return e, ok
}

// handoverKept: hand-over id is pending (its send not accepted yet) or kept and not past handoverMemory.
func (s *Service) handoverKept(id string) bool {
	b := &s.handovers
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.pending[id]; ok {
		return true
	}
	if !b.loadLocked(context.Background(), s) {
		return false
	}
	e, ok := b.m[id]
	return ok && time.Since(e.At) <= handoverMemory
}
