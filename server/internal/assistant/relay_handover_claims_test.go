package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// handoverNow is hand-over id as s keeps it (the zero hand-over when none).
func handoverNow(s *Service, id string) handover {
	for _, e := range handoverList(s) {
		if e.ID == id {
			return e
		}
	}
	return handover{}
}

// queuedUnwritten puts msg in conversation to's queue as queueRelay does under the relay lock, before it writes the
// hand-over's queued.
func queuedUnwritten(s *Service, to string, msg relayMsg) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	msg.QueuedAt = time.Now().UTC()
	b.st.Queue[to] = append(b.st.Queue[to], msg)
	s.saveRelayLocked()
}

// An answer goes to a delivered hand-over before an older one whose try is still in doubt: the lead answers what it has
// read, and the one in doubt may never have gone in.
func TestADeliveredHandoverIsAnsweredBeforeOneInDoubt(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	stopHolds(t, d.s)
	d.f.setItems(ownerItem("m-owner", quotedWords), ownerItem("m-owner-2", "and the import as well"))
	refuseSendsTo(d.f, d.leaderID, context.DeadlineExceeded) // the first try: written, then no answer in time
	d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"m-owner"`)
	d.setStatus(d.leaderID, proto.StatusWorking) // queued behind the one in doubt, and no drain of the send's own
	d.send(t, d.mainID, d.leaderID, `"text":"and the import","quote":"m-owner-2"`)
	d.setStatus("", "")
	list := handoverList(d.s)
	if len(list) != 2 || list[0].State != handoverQueued || list[1].State != handoverQueued {
		t.Fatalf("hand-overs %+v", list)
	}
	inDoubt, newer := list[0].ID, list[1].ID
	drainRelayTest(t, d.s, d.idle(d.leaderID)) // the look misses: still in doubt
	refuseSendsTo(d.f, d.leaderID, nil)
	drainRelayTest(t, d.s, d.idle(d.leaderID)) // the newer one goes in meanwhile
	if s := handoverState(d.s, newer); s != handoverDelivered {
		t.Fatalf("the newer hand-over: %s", s)
	}
	d.send(t, d.leaderID, d.mainID, `"text":"the import is fixed"`)
	if s := handoverState(d.s, newer); s != handoverAnswered {
		t.Fatalf("the delivered hand-over after the answer: %s", s)
	}
	if e := handoverNow(d.s, inDoubt); e.State != handoverQueued || !e.AnswerAt.IsZero() {
		t.Fatalf("the hand-over in doubt took the answer: %+v", e)
	}
}

// A hand-over sent again after its looks missed voids every answer named before then: one still queued in the asking
// conversation, and one on its way, go in after it without answering it. An answer named after it does.
func TestAResendVoidsAnAnswerNamedBefore(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	stopHolds(t, d.s)
	d.f.setItems(ownerItem("m-owner", quotedWords))
	refuseSendsTo(d.f, d.leaderID, context.DeadlineExceeded)
	d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"latest"`)
	e := onlyHandover(t, d.s)
	d.setStatus(d.mainID, proto.StatusWorking)
	d.send(t, d.leaderID, d.mainID, `"text":"on it"`) // names it while it is in doubt; waits in the main conversation
	if q := queuedRelays(d.s, d.mainID); len(q) != 1 || q[0].Answers != e.ID {
		t.Fatalf("queued %+v", q)
	}
	named := time.Now().UTC() // an answer on its way, named before the send again
	for looks := 0; looks < relayLookLimit; looks++ {
		if looks > 0 {
			advanceRelayLook(t, d.s, d.leaderID, looks)
		}
		if looks == relayLookLimit-1 {
			refuseSendsTo(d.f, d.leaderID, nil) // the last look misses, and the message is sent again
		}
		drainRelayTest(t, d.s, d.idle(d.leaderID))
	}
	if got := handoverNow(d.s, e.ID); got.State != handoverDelivered || got.ResentAt.IsZero() {
		t.Fatalf("sent again: %+v", got)
	}
	d.setStatus("", "")
	drainRelayTest(t, d.s, d.idle(d.mainID))
	if s := handoverState(d.s, e.ID); s != handoverDelivered {
		t.Fatalf("a queued answer named before the send again: %s", s)
	}
	d.s.handoverWentIn(d.mainID, relayMsg{From: d.leaderID, Tool: "conversation_send", Text: "on it", At: named, Answers: e.ID})
	if s := handoverState(d.s, e.ID); s != handoverDelivered {
		t.Fatalf("an answer on its way, named before the send again: %s", s)
	}
	d.send(t, d.leaderID, d.mainID, `"text":"on it, now that I have it"`)
	if s := handoverState(d.s, e.ID); s != handoverAnswered {
		t.Fatalf("an answer named after the send again: %s", s)
	}
}

// A hand-over whose message moves to another receiver drops an answer the old receiver gave while it was in doubt:
// going into the new receiver it is delivered, not answered.
func TestAMovedHandoverDropsItsAnswer(t *testing.T) {
	ctx := context.Background()
	d := newDispatchPolicyHarness(t)
	stopHolds(t, d.s)
	quote := &relayRequest{BriefRequest: BriefRequest{Conversation: d.mainID, MessageID: "m-owner", Text: quotedWords}}
	id, done := d.s.handoverFor(ctx, d.leaderID, d.bgID, "Background", quote)
	d.s.queueRelay(d.bgID, relayMsg{From: d.leaderID, FromTitle: "Export lead", Tool: "conversation_send", Text: "check this",
		At: time.Now().UTC(), Handover: id, Quote: quote})
	done()
	d.s.handoverWentIn(d.leaderID, relayMsg{From: d.bgID, Tool: "conversation_send", Text: "looked", At: time.Now().UTC(), Answers: id})
	if e := handoverNow(d.s, id); e.State != handoverQueued || e.AnswerAt.IsZero() {
		t.Fatalf("an answer while in doubt: %+v", e)
	}
	d.s.backgroundLost(ctx, d.bgID, d.mainID, d.s.webBase(ctx))
	if e := handoverNow(d.s, id); e.To != d.mainID || !e.AnswerAt.IsZero() {
		t.Fatalf("moved: %+v", e)
	}
	drainRelayTest(t, d.s, d.idle(d.mainID))
	if s := handoverState(d.s, id); s != handoverDelivered {
		t.Fatalf("in the new receiver: %s", s)
	}
}

// A hand-over whose message is in the queue before queueRelay wrote its queued, when backgroundLost moves or drops that
// queue, is queued and then moved there (with the new receiver's title) or not delivered; the late queued changes
// nothing.
func TestAHandoverQueuedWhileItsQueueMoves(t *testing.T) {
	for _, moves := range []bool{true, false} {
		name := "moved"
		if !moves {
			name = "dropped"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			d := newDispatchPolicyHarness(t)
			stopHolds(t, d.s)
			quote := &relayRequest{BriefRequest: BriefRequest{Conversation: d.mainID, MessageID: "m-owner", Text: quotedWords}}
			id, done := d.s.handoverFor(ctx, d.leaderID, d.bgID, "Background", quote)
			t.Cleanup(done)
			queuedUnwritten(d.s, d.bgID, relayMsg{From: d.leaderID, FromTitle: "Export lead", Tool: "conversation_send",
				Text: "check this", At: time.Now().UTC(), Handover: id, Quote: quote})
			to := d.mainID
			if !moves {
				to = ""
			}
			d.s.backgroundLost(ctx, d.bgID, to, d.s.webBase(ctx))
			e := onlyHandover(t, d.s)
			if moves && (e.State != handoverQueued || e.To != d.mainID || e.ToTitle != conversationName(d.idle(d.mainID), d.mainID)) ||
				!moves && (e.State != handoverNotDelivered || e.Code != handoverGone) {
				t.Fatalf("%s: %+v", name, e)
			}
			if d.s.handoverSet(id, handoverQueued, "", nil) { // queueRelay's own write, late
				t.Fatalf("the late queued changed it: %+v", onlyHandover(t, d.s))
			}
		})
	}
}

// An answer named before its hand-over was sent again holds nothing while it still waits: a later answer, made after
// the send again, names that hand-over and answers it, and the earlier one, going in first, answers nothing.
func TestAVoidAnswerDoesNotHoldItsHandover(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	stopHolds(t, d.s)
	d.f.setItems(ownerItem("m-owner", quotedWords))
	refuseSendsTo(d.f, d.leaderID, context.DeadlineExceeded)
	d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"latest"`)
	e := onlyHandover(t, d.s)
	d.setStatus(d.mainID, proto.StatusWorking)
	d.send(t, d.leaderID, d.mainID, `"text":"on it"`) // R1, while it is in doubt
	for looks := 0; looks < relayLookLimit; looks++ {
		if looks > 0 {
			advanceRelayLook(t, d.s, d.leaderID, looks)
		}
		if looks == relayLookLimit-1 {
			refuseSendsTo(d.f, d.leaderID, nil) // the last look misses, and the message is sent again
		}
		drainRelayTest(t, d.s, d.idle(d.leaderID))
	}
	if got := handoverNow(d.s, e.ID); got.State != handoverDelivered || got.ResentAt.IsZero() {
		t.Fatalf("sent again: %+v", got)
	}
	d.send(t, d.leaderID, d.mainID, `"text":"read it now: the export fix lands today"`) // R2, while R1 still waits
	q := queuedRelays(d.s, d.mainID)
	if len(q) != 2 || q[0].Answers != e.ID || q[1].Answers != e.ID {
		t.Fatalf("queued answers %+v: want the later one to name %s too", q, e.ID)
	}
	d.setStatus("", "")
	drainRelayTest(t, d.s, d.idle(d.mainID)) // R1 goes in: void
	if s := handoverState(d.s, e.ID); s != handoverDelivered {
		t.Fatalf("after the void answer went in: %s", s)
	}
	drainRelayTest(t, d.s, d.idle(d.mainID)) // R2 goes in
	if s := handoverState(d.s, e.ID); s != handoverAnswered {
		t.Fatalf("after the later answer went in: %s", s)
	}
}

// An answer while a hand-over waits in the queue untried answers nothing: the lead has not read it. When the hand-over
// then goes in, it is delivered, not answered.
func TestAnAnswerToAHandoverNeverTriedAnswersNothing(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	stopHolds(t, d.s)
	d.f.setItems(ownerItem("m-owner", quotedWords))
	d.setStatus(d.leaderID, proto.StatusWorking)
	d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"latest"`)
	e := onlyHandover(t, d.s)
	d.send(t, d.leaderID, d.mainID, `"text":"about something else"`)
	if got := handoverNow(d.s, e.ID); got.State != handoverQueued || !got.AnswerAt.IsZero() {
		t.Fatalf("a hand-over never tried took an answer: %+v", got)
	}
	d.setStatus("", "")
	drainRelayTest(t, d.s, d.idle(d.leaderID))
	if s := handoverState(d.s, e.ID); s != handoverDelivered {
		t.Fatalf("gone in: %s", s)
	}
}
