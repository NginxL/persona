package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// handoverList is the hand-overs s keeps, oldest first.
func handoverList(s *Service) []handover {
	b := &s.handovers
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loadLocked(context.Background(), s) {
		return nil
	}
	out := make([]handover, 0, len(b.m))
	for _, e := range b.m {
		out = append(out, e)
	}
	slices.SortFunc(out, func(x, y handover) int { return x.At.Compare(y.At) })
	return out
}

// handoverState is hand-over id's state ("" when s keeps none by that id).
func handoverState(s *Service, id string) string {
	for _, e := range handoverList(s) {
		if e.ID == id {
			return e.State
		}
	}
	return ""
}

// onlyHandover is the one hand-over s keeps.
func onlyHandover(t *testing.T, s *Service) handover {
	t.Helper()
	list := handoverList(s)
	if len(list) != 1 {
		t.Fatalf("%d hand-overs, want one: %+v", len(list), list)
	}
	return list[0]
}

// pendingHandovers counts the hand-overs of sends not accepted yet.
func pendingHandovers(s *Service) int {
	s.handovers.mu.Lock()
	defer s.handovers.mu.Unlock()
	return len(s.handovers.pending)
}

// setStatus shows conversation id with status in the harness's list, every other one idle.
func (d *dispatchPolicyHarness) setStatus(id, status string) {
	var list []proto.Session
	for _, c := range []string{d.mainID, d.leaderID, d.bgID, d.patrolID, d.sideID} {
		st := proto.StatusIdle
		if c == id {
			st = status
		}
		list = append(list, proto.Session{MachineID: "server", SessionID: c, Assistant: true, Status: st})
	}
	d.f.setSessions("server", list...)
}

// idle is conversation id as the harness lists it when it is idle.
func (d *dispatchPolicyHarness) idle(id string) proto.Session {
	return proto.Session{MachineID: "server", SessionID: id, Assistant: true, Status: proto.StatusIdle}
}

// send is conversation_send from → to with args (JSON members after id), which must be accepted; its result.
func (d *dispatchPolicyHarness) send(t *testing.T, from, to, args string) string {
	t.Helper()
	res := d.tool(t, from, "conversation_send", `{"id":"`+to+`",`+args+`}`)
	if res.IsError {
		t.Fatalf("conversation_send %s → %s: %s", from, to, res.Content[0].Text)
	}
	return res.Content[0].Text
}

// handoverLogs are the log lines about hand-over id.
func (d *dispatchPolicyHarness) handoverLogs(id string) []string {
	var out []string
	for _, line := range strings.Split(d.log(), "\n") {
		if strings.HasPrefix(line, "hand-over "+id+" ") {
			out = append(out, line)
		}
	}
	return out
}

// A quoted conversation_send with a direction's lead on either end is one hand-over: from the main conversation to the
// lead, and from the lead to the main conversation. A send without a quote, a quoted one between two conversations
// neither of which is a lead, and one refused at once are none.
func TestHandoverEntriesAreMadeForQuotedSendsWithALead(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	var titled []proto.Session // the lead named, so its title and the main conversation's differ from the sender's
	for _, id := range []string{d.mainID, d.leaderID, d.bgID, d.patrolID, d.sideID} {
		c := d.idle(id)
		if id == d.leaderID {
			c.Title = "Export lead"
		}
		titled = append(titled, c)
	}
	d.f.setSessions("server", titled...)
	mainTitle := conversationName(d.idle(d.mainID), d.mainID)
	d.f.setItems(ownerItem("m-owner", quotedWords))
	d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"latest"`)
	e := onlyHandover(t, d.s)
	if e.ID == "" || e.From != d.mainID || e.To != d.leaderID || e.ToTitle != "Export lead" || e.Conversation != d.mainID ||
		e.MessageID != "m-owner" || e.State != handoverDelivered || e.Code != "" || e.At.IsZero() || e.ChangedAt.Before(e.At) {
		t.Fatalf("main → lead: %+v", e)
	}
	if text, _ := d.f.lastSend(t)["text"].(string); strings.Contains(text, e.ID) {
		t.Fatalf("the receiver sees the hand-over's id:\n%s", text)
	}

	d.f.setItems(received(t, d.f, d.mainID)) // the lead passes the owner's words back with what it found
	d.send(t, d.leaderID, d.mainID, `"text":"done: the export keeps the old format","quote":"latest"`)
	list := handoverList(d.s)
	if len(list) != 2 {
		t.Fatalf("lead → main: %d hand-overs, want two: %+v", len(list), list)
	}
	back := list[1]
	if back.From != d.leaderID || back.To != d.mainID || back.ToTitle != mainTitle || back.Conversation != d.mainID ||
		back.MessageID != "m-owner" || back.State != handoverDelivered {
		t.Fatalf("lead → main: %+v", back)
	}

	d.f.setItems(ownerItem("m-owner", quotedWords))
	d.send(t, d.mainID, d.leaderID, `"text":"and the docs, please"`)                // no quote
	d.send(t, d.mainID, d.sideID, `"text":"look into the export","quote":"latest"`) // no lead on either end
	refuseSendsTo(d.f, d.leaderID, &rpc.Error{Code: "unheard_of", Message: "this conversation will not take it"})
	if res := d.tool(t, d.mainID, "conversation_send", `{"id":"`+d.leaderID+`","text":"one more","quote":"latest"}`); !res.IsError {
		t.Fatalf("a refused send went: %s", res.Content[0].Text)
	}
	if n := len(handoverList(d.s)); n != 2 || pendingHandovers(d.s) != 0 {
		t.Fatalf("%d hand-overs and %d pending after sends that are none", n, pendingHandovers(d.s))
	}
}

// A hand-over goes queued → delivered → answered as its message waits, goes in, and the lead's first message back goes
// into the asking conversation: sent at once (never queued), from the queue, and a try in doubt that is found in the
// transcript later (queued until then). Each change is one log line.
func TestHandoverGoesQueuedDeliveredAnswered(t *testing.T) {
	for _, way := range []string{"sent at once", "from the queue", "in doubt, found later"} {
		t.Run(way, func(t *testing.T) {
			d := newDispatchPolicyHarness(t)
			stopHolds(t, d.s)
			d.f.setItems(ownerItem("m-owner", quotedWords))
			var tried string // the text of the try in doubt
			switch way {
			case "from the queue":
				d.setStatus(d.leaderID, proto.StatusWorking)
			case "in doubt, found later":
				d.f.mu.Lock()
				d.f.onAssistantSend = func(f *fakeHub, p map[string]any) {
					if p["session_id"] == d.leaderID {
						tried, _ = p["text"].(string)
					}
				}
				d.f.mu.Unlock()
				refuseSendsTo(d.f, d.leaderID, context.DeadlineExceeded) // written, then no answer in time
			}
			out := d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"latest"`)
			e := onlyHandover(t, d.s)
			want := []string{"delivered"}
			switch way {
			case "sent at once":
				if e.State != handoverDelivered {
					t.Fatalf("sent at once: %s (%s)", e.State, out)
				}
			case "from the queue":
				if e.State != handoverQueued || d.s.queuedFor(d.leaderID) != 1 {
					t.Fatalf("while the lead works: %s, %d queued (%s)", e.State, d.s.queuedFor(d.leaderID), out)
				}
				d.setStatus("", "")
				drainRelayTest(t, d.s, d.idle(d.leaderID))
				want = []string{"queued", "delivered"}
			case "in doubt, found later":
				if e.State != handoverQueued || !strings.Contains(out, "queued") {
					t.Fatalf("a try in doubt: %s (%s)", e.State, out)
				}
				drainRelayTest(t, d.s, d.idle(d.leaderID)) // not in the transcript yet: still in doubt
				if s := handoverState(d.s, e.ID); s != handoverQueued || d.s.queuedFor(d.leaderID) != 1 {
					t.Fatalf("a try in doubt not found: %s, %d queued", s, d.s.queuedFor(d.leaderID))
				}
				d.f.mu.Lock()
				d.f.items = append(d.f.items, proto.Item{Seq: len(d.f.items) + 1, Role: "user", Kind: proto.KindText, Text: tried, TS: time.Now()})
				d.f.mu.Unlock()
				advanceRelayLook(t, d.s, d.leaderID, 1)
				drainRelayTest(t, d.s, d.idle(d.leaderID))
				want = []string{"queued", "delivered"}
			}
			if s := handoverState(d.s, e.ID); s != handoverDelivered || d.s.queuedFor(d.leaderID) != 0 {
				t.Fatalf("gone in: %s, %d queued", s, d.s.queuedFor(d.leaderID))
			}

			d.send(t, d.leaderID, d.mainID, `"text":"on it; the fix lands today"`)
			if s := handoverState(d.s, e.ID); s != handoverAnswered {
				t.Fatalf("after the lead's answer went in: %s", s)
			}
			want = append(want, "answered")
			logs := d.handoverLogs(e.ID)
			if len(logs) != len(want) {
				t.Fatalf("log lines %q, want one per change %q", logs, want)
			}
			for i, w := range want {
				if !strings.HasSuffix(logs[i], ": "+w) {
					t.Errorf("log line %d is %q, want the change to %s", i, logs[i], w)
				}
			}
		})
	}
}

// The lead's answer is matched on the entries, not on the relay's hop record: it marks its hand-over answered more than
// an hour later, after the owner spoke in the lead's conversation, after a third conversation's request reached the
// lead, and when the answer itself carries the owner's words.
func TestHandoverAnswerIsMatchedOnTheEntries(t *testing.T) {
	for _, after := range []string{"more than an hour", "the owner spoke to the lead", "a third conversation's request", "a quote in the answer"} {
		t.Run(after, func(t *testing.T) {
			d := newDispatchPolicyHarness(t)
			stopHolds(t, d.s)
			owner := ownerItem("m-owner", quotedWords)
			if after == "a quote in the answer" {
				owner.TS = time.Now() // within the hour a quote of the owner's goes free (relayAdmit)
			}
			d.f.setItems(owner)
			d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"latest"`)
			e := onlyHandover(t, d.s)
			args := `"text":"on it; the fix lands today"`
			switch after {
			case "more than an hour": // the hand-over and the hop record both an hour and more old
				ago := relayHopMemory + time.Minute
				h := &d.s.handovers
				h.mu.Lock()
				aged := h.m[e.ID]
				aged.At, aged.ChangedAt = aged.At.Add(-ago), aged.ChangedAt.Add(-ago)
				h.m[e.ID] = aged
				h.mu.Unlock()
				b := &d.s.relays
				b.mu.Lock()
				hp := b.st.Hops[d.leaderID]
				hp.At = hp.At.Add(-ago)
				b.st.Hops[d.leaderID] = hp
				b.mu.Unlock()
			case "the owner spoke to the lead":
				d.s.UserSpoke(context.Background(), SessionRef{MachineID: "server", SessionID: d.leaderID})
			case "a third conversation's request":
				d.send(t, d.sideID, d.leaderID, `"text":"how far is the export fix?"`)
			case "a quote in the answer":
				d.f.setItems(received(t, d.f, d.mainID))
				args += `,"quote":"latest"`
			}
			d.send(t, d.leaderID, d.mainID, args)
			if s := handoverState(d.s, e.ID); s != handoverAnswered {
				t.Fatalf("after %s: %s", after, s)
			}
			if after == "a quote in the answer" { // it went free, so the hop record took it for a request of its own
				b := &d.s.relays
				b.mu.Lock()
				_, free := b.quoted[d.mainID+"|"+d.mainID+"/m-owner"]
				from := b.st.Hops[d.mainID].From
				b.mu.Unlock()
				if !free || from != d.leaderID {
					t.Fatalf("the answer was not admitted free with the owner's words (free %v, the hop record's request from %q)", free, from)
				}
			}
		})
	}
}

// An answer that goes in while its hand-over's own try is still in doubt (the lead's turn that try started answers before
// any look) answers it once the look finds the try went in, and the next answer goes to the newer hand-over. If the
// looks never find it and the message is sent again, that answer does not count.
func TestAnAnswerWhileItsHandoverIsInDoubt(t *testing.T) {
	for _, found := range []bool{true, false} {
		name := "found by the look"
		if !found {
			name = "never found, sent again"
		}
		t.Run(name, func(t *testing.T) {
			d := newDispatchPolicyHarness(t)
			stopHolds(t, d.s)
			d.f.setItems(ownerItem("m-owner", quotedWords), ownerItem("m-owner-2", "and the import as well"))
			var tried string
			d.f.mu.Lock()
			d.f.onAssistantSend = func(f *fakeHub, p map[string]any) {
				if p["session_id"] == d.leaderID && tried == "" {
					tried, _ = p["text"].(string)
				}
			}
			d.f.mu.Unlock()
			refuseSendsTo(d.f, d.leaderID, context.DeadlineExceeded) // written, then no answer in time
			d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"m-owner"`)
			e := onlyHandover(t, d.s)
			d.send(t, d.leaderID, d.mainID, `"text":"on it; the fix lands today"`) // before any look
			h := handoverList(d.s)[0]
			if h.State != handoverQueued || h.AnswerAt.IsZero() {
				t.Fatalf("an answer while in doubt: %+v", h)
			}
			if found {
				d.f.mu.Lock()
				d.f.items = append(d.f.items, proto.Item{Seq: len(d.f.items) + 1, Role: "user", Kind: proto.KindText, Text: tried, TS: time.Now()})
				d.f.mu.Unlock()
				drainRelayTest(t, d.s, d.idle(d.leaderID))
				if s := handoverState(d.s, e.ID); s != handoverAnswered {
					t.Fatalf("found by the look: %s", s)
				}
				logs := d.handoverLogs(e.ID)
				if len(logs) != 4 || !strings.HasSuffix(logs[2], ": delivered") || !strings.HasSuffix(logs[3], ": answered") {
					t.Fatalf("log lines %q", logs)
				}
			} else {
				for looks := 0; looks < relayLookLimit; looks++ {
					if looks > 0 {
						advanceRelayLook(t, d.s, d.leaderID, looks)
					}
					if looks == relayLookLimit-1 {
						refuseSendsTo(d.f, d.leaderID, nil) // the last look misses, and the message is sent again
					}
					drainRelayTest(t, d.s, d.idle(d.leaderID))
				}
				if got := handoverList(d.s)[0]; got.State != handoverDelivered || !got.AnswerAt.IsZero() {
					t.Fatalf("sent again after the looks: %+v", got)
				}
			}
			refuseSendsTo(d.f, d.leaderID, nil)
			d.send(t, d.mainID, d.leaderID, `"text":"and the import","quote":"m-owner-2"`)
			newer := handoverList(d.s)[1]
			d.send(t, d.leaderID, d.mainID, `"text":"the import is fixed too"`)
			want := handoverAnswered
			if !found { // the next answer takes the older one, delivered and not answered yet
				want = handoverDelivered
			}
			if s := handoverState(d.s, newer.ID); s != want {
				t.Fatalf("the newer hand-over after the next answer: %s, want %s", s, want)
			}
		})
	}
}

// A hand-over past its 14 days is not answered: the answer names none, and its state no longer changes.
func TestAnExpiredHandoverIsNotAnswered(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	stopHolds(t, d.s)
	d.f.setItems(ownerItem("m-owner", quotedWords))
	d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"latest"`)
	e := onlyHandover(t, d.s)
	h := &d.s.handovers
	h.mu.Lock()
	old := h.m[e.ID]
	old.At = old.At.Add(-handoverMemory - time.Hour)
	h.m[e.ID] = old
	h.mu.Unlock()
	d.setStatus(d.mainID, proto.StatusWorking)
	d.send(t, d.leaderID, d.mainID, `"text":"on it"`)
	if q := queuedRelays(d.s, d.mainID); len(q) != 1 || q[0].Answers != "" {
		t.Fatalf("an answer named an expired hand-over: %+v", q)
	}
	d.setStatus("", "")
	drainRelayTest(t, d.s, d.idle(d.mainID))
	if s := handoverState(d.s, e.ID); s == handoverAnswered {
		t.Fatal("an expired hand-over was answered")
	}
	if d.s.handoverSet(e.ID, handoverAnswered, "", nil) {
		t.Fatal("an expired hand-over changed state")
	}
}

// A hand-over's state never goes back: not from delivered to queued (queueRelay writes queued after it let go of the
// relay lock, and a drain may have delivered the message meanwhile), not from answered, and nothing comes out of
// not_delivered.
func TestAHandoverStateNeverGoesBack(t *testing.T) {
	ctx := context.Background()
	s, _ := newQuietService(t)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	quote := &relayRequest{BriefRequest: BriefRequest{Conversation: "conv1", MessageID: "m1", Text: quotedWords}}
	made := func() string {
		t.Helper()
		id, done := s.handoverFor(ctx, "conv1", "lead1", "Lead", quote)
		t.Cleanup(done)
		return id
	}
	// queueRelay's queued comes after a drain delivered the message.
	late := made()
	if !s.handoverSet(late, handoverDelivered, "", nil) {
		t.Fatal("not delivered")
	}
	s.queueRelay("lead1", relayMsg{From: "conv1", FromTitle: "Main", Tool: "conversation_send", Text: "take this", At: time.Now().UTC(), Handover: late})
	if got := handoverState(s, late); got != handoverDelivered {
		t.Fatalf("a late queued took a delivered hand-over back to %s", got)
	}
	answered := made()
	for _, state := range []string{handoverQueued, handoverDelivered, handoverAnswered} {
		if !s.handoverSet(answered, state, "", nil) {
			t.Fatalf("not %s", state)
		}
	}
	ended := made()
	if !s.handoverSet(ended, handoverQueued, "", nil) || !s.handoverSet(ended, handoverNotDelivered, "unheard_of", nil) {
		t.Fatal("not ended")
	}
	for _, tc := range []struct {
		id, from string
		to       []string
	}{
		{late, handoverDelivered, []string{handoverQueued}},
		{answered, handoverAnswered, []string{handoverQueued, handoverDelivered, handoverNotDelivered}},
		{ended, handoverNotDelivered, []string{handoverQueued, handoverDelivered, handoverAnswered}},
	} {
		for _, to := range tc.to {
			if s.handoverSet(tc.id, to, "", nil) || handoverState(s, tc.id) != tc.from {
				t.Errorf("%s went back to %s", tc.from, to)
			}
		}
	}
}

// A hand-over dropped because its receiver is gone is not delivered before the queue without it is saved: a manager
// that stops in between leaves no hand-over saying queued for a message that is gone.
func TestAGoneHandoverEndsBeforeItsMessageLeavesTheQueue(t *testing.T) {
	ctx := context.Background()
	st := &handoverOrderStore{memStore: &memStore{}}
	s, f := newQuietServiceOn(t, st)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	f.mu.Lock()
	f.known["lead1"] = true
	f.mu.Unlock()
	lead := proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}, lead)
	id, done := s.handoverFor(ctx, "conv1", "lead1", "Lead", &relayRequest{BriefRequest: BriefRequest{Conversation: "conv1", MessageID: "m1"}})
	s.queueRelay("lead1", relayMsg{From: "conv1", FromTitle: "Main", Tool: "conversation_send", Text: "take this", At: time.Now().UTC(), Handover: id})
	done()
	st.watch(id)
	refuseSendsTo(f, "lead1", errors.New("conversation lead1 is gone"))
	drainSettled(t, s, lead)
	if got := handoverState(s, id); got != handoverNotDelivered {
		t.Fatalf("gone: %s", got)
	}
	if seen := st.seen(); len(seen) == 0 || seen[0] != handoverNotDelivered {
		t.Fatalf("when the queue without it was saved, the saved hand-over said %q", seen)
	}
}

// handoverOrderStore is memStore that notes, each time the relay queue is saved without the watched hand-over's message,
// what the hand-overs saved before say of it.
type handoverOrderStore struct {
	*memStore
	mu    sync.Mutex
	id    string
	saved []string
}

func (o *handoverOrderStore) watch(id string) {
	o.mu.Lock()
	o.id = id
	o.mu.Unlock()
}

func (o *handoverOrderStore) seen() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.saved)
}

func (o *handoverOrderStore) PutSetting(ctx context.Context, key string, v any) error {
	o.mu.Lock()
	id := o.id
	o.mu.Unlock()
	if rs, ok := v.(relayState); ok && id != "" && !slices.ContainsFunc(rs.Queue["lead1"], func(m relayMsg) bool { return m.Handover == id }) {
		var m map[string]handover
		if _, err := o.memStore.Setting(ctx, handoversKey, &m); err != nil {
			return err
		}
		o.mu.Lock()
		o.saved = append(o.saved, m[id].State)
		o.mu.Unlock()
	}
	return o.memStore.PutSetting(ctx, key, v)
}

// A hand-over queued for a background supervisor that is gone follows its message to where it waits now, the main
// conversation, and goes on from there; with nowhere to move it is not delivered.
func TestAHandoverQueuedForALostBackgroundSupervisor(t *testing.T) {
	for _, moves := range []bool{true, false} {
		name := "moved to the main conversation"
		if !moves {
			name = "nowhere to move"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			d := newDispatchPolicyHarness(t)
			stopHolds(t, d.s)
			quote := &relayRequest{BriefRequest: BriefRequest{Conversation: d.mainID, MessageID: "m-owner", Text: quotedWords}}
			id, done := d.s.handoverFor(ctx, d.leaderID, d.bgID, "Background", quote)
			d.s.queueRelay(d.bgID, relayMsg{From: d.leaderID, FromTitle: "Export lead", Tool: "conversation_send", Text: "check this",
				At: time.Now().UTC(), Handover: id, Quote: quote})
			done()
			to := d.mainID
			if !moves {
				to = ""
			}
			d.s.backgroundLost(ctx, d.bgID, to, d.s.webBase(ctx))
			e := onlyHandover(t, d.s)
			if !moves {
				if e.State != handoverNotDelivered || e.Code != handoverGone || d.s.queuedFor(d.bgID) != 0 {
					t.Fatalf("dropped: %+v", e)
				}
				return
			}
			if e.State != handoverQueued || e.To != d.mainID || e.ToTitle != conversationName(d.idle(d.mainID), d.mainID) {
				t.Fatalf("moved: %+v", e)
			}
			drainRelayTest(t, d.s, d.idle(d.mainID))
			if s := handoverState(d.s, id); s != handoverDelivered || d.f.sentTo(d.mainID) != 1 {
				t.Fatalf("after it went into the main conversation: %s, %d sent", s, d.f.sentTo(d.mainID))
			}
		})
	}
}

// Two hand-overs to one lead are answered oldest first, each by one answer. An answer that waits in the asking
// conversation's queue answers nothing until it goes in, and a later answer meanwhile takes the next hand-over.
func TestTwoHandoversAreAnsweredOldestFirst(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	stopHolds(t, d.s)
	d.f.setItems(ownerItem("m-owner", quotedWords), ownerItem("m-owner-2", "and the import as well"))
	d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"m-owner"`)
	d.send(t, d.mainID, d.leaderID, `"text":"and the import","quote":"m-owner-2"`)
	list := handoverList(d.s)
	if len(list) != 2 || list[0].MessageID != "m-owner" || list[1].MessageID != "m-owner-2" {
		t.Fatalf("hand-overs %+v", list)
	}
	d.setStatus(d.mainID, proto.StatusWorking)
	d.send(t, d.leaderID, d.mainID, `"text":"the export fix is in"`)
	d.send(t, d.leaderID, d.mainID, `"text":"the import fix is in"`)
	q := queuedRelays(d.s, d.mainID)
	if len(q) != 2 || q[0].Answers != list[0].ID || q[1].Answers != list[1].ID {
		t.Fatalf("queued answers %+v, want the first for %s and the second for %s", q, list[0].ID, list[1].ID)
	}
	for _, e := range list {
		if s := handoverState(d.s, e.ID); s != handoverDelivered {
			t.Fatalf("answered before the answer went in: %s %s", e.ID, s)
		}
	}
	d.setStatus("", "")
	drainRelayTest(t, d.s, d.idle(d.mainID))
	if a, b := handoverState(d.s, list[0].ID), handoverState(d.s, list[1].ID); a != handoverAnswered || b != handoverDelivered {
		t.Fatalf("after the first answer went in: %s, %s", a, b)
	}
	drainRelayTest(t, d.s, d.idle(d.mainID))
	if b := handoverState(d.s, list[1].ID); b != handoverAnswered {
		t.Fatalf("after the second answer went in: %s", b)
	}
}

// An answer that is refused, at once or for good from the queue, leaves its hand-over delivered; the next answer that
// goes in answers it.
func TestARefusedAnswerLeavesItsHandoverDelivered(t *testing.T) {
	for _, way := range []string{"at once", "from the queue"} {
		t.Run(way, func(t *testing.T) {
			d := newDispatchPolicyHarness(t)
			stopHolds(t, d.s)
			d.f.setItems(ownerItem("m-owner", quotedWords))
			d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"latest"`)
			e := onlyHandover(t, d.s)
			refusal := &rpc.Error{Code: "unheard_of", Message: "this conversation will not take it"}
			if way == "at once" {
				refuseSendsTo(d.f, d.mainID, refusal)
				if res := d.tool(t, d.leaderID, "conversation_send", `{"id":"`+d.mainID+`","text":"on it"}`); !res.IsError {
					t.Fatalf("a refused answer went: %s", res.Content[0].Text)
				}
			} else {
				d.setStatus(d.mainID, proto.StatusWorking)
				d.send(t, d.leaderID, d.mainID, `"text":"on it"`)
				if q := queuedRelays(d.s, d.mainID); len(q) != 1 || q[0].Answers != e.ID {
					t.Fatalf("queued %+v", q)
				}
				d.setStatus("", "")
				refuseSendsTo(d.f, d.mainID, refusal)
				drainSettled(t, d.s, d.idle(d.mainID))
				if n := d.s.queuedFor(d.mainID); n != 0 {
					t.Fatalf("a refused answer is still queued (%d)", n)
				}
			}
			if s := handoverState(d.s, e.ID); s != handoverDelivered {
				t.Fatalf("after a refused answer: %s", s)
			}
			refuseSendsTo(d.f, d.mainID, nil)
			d.send(t, d.leaderID, d.mainID, `"text":"on it, second try"`)
			if s := handoverState(d.s, e.ID); s != handoverAnswered {
				t.Fatalf("after the next answer went in: %s", s)
			}
		})
	}
}

// A queued hand-over that is refused for good is not delivered, with the refusal's code, and its sender still gets the
// line saying so; one whose lead is gone is not delivered, with "gone".
func TestQueuedHandoverThatWillNotGoIsNotDelivered(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"refused for good", &rpc.Error{Code: "unheard_of", Message: "this conversation will not take it"}, "unheard_of"},
		{"gone", errors.New("conversation leader-dp-1 is gone"), handoverGone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDispatchPolicyHarness(t)
			stopHolds(t, d.s)
			d.f.setItems(ownerItem("m-owner", quotedWords))
			d.setStatus(d.leaderID, proto.StatusWorking)
			d.send(t, d.mainID, d.leaderID, `"text":"take this one: the export fix","quote":"latest"`)
			e := onlyHandover(t, d.s)
			if e.State != handoverQueued {
				t.Fatalf("queued: %+v", e)
			}
			d.setStatus("", "")
			refuseSendsTo(d.f, d.leaderID, tc.err)
			drainSettled(t, d.s, d.idle(d.leaderID))
			got := onlyHandover(t, d.s)
			if got.State != handoverNotDelivered || got.Code != tc.code || d.s.queuedFor(d.leaderID) != 0 {
				t.Fatalf("%s: %+v, %d queued", tc.name, got, d.s.queuedFor(d.leaderID))
			}
			if tc.name == "refused for good" {
				waitFor(t, "the line to the sender", func() bool { return d.f.sentTo(d.mainID) == 1 })
				if line := d.f.sentTexts(d.mainID)[0]; !strings.Contains(line, "unheard_of") || strings.Contains(line, e.ID) {
					t.Fatalf("the sender's line:\n%s", line)
				}
			}
			drainSettled(t, d.s, d.idle(d.leaderID))
			if logs := d.handoverLogs(e.ID); len(logs) != 2 || !strings.HasSuffix(logs[1], ": not_delivered ("+tc.code+")") {
				t.Fatalf("log lines %q", logs)
			}
		})
	}
}

// The hand-overs survive a manager restart. They are kept 14 days and at most 64, the oldest dropped first.
func TestHandoversAreKeptLikeTheQuotes(t *testing.T) {
	ctx := context.Background()
	store := &memStore{}
	s, _ := newQuietServiceOn(t, store)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	quote := &relayRequest{BriefRequest: BriefRequest{Conversation: "conv1", MessageID: "m1", Text: quotedWords}}
	if id, done := s.handoverFor(ctx, "conv1", "conv2", "Research", quote); id != "" {
		done()
		t.Fatalf("a hand-over between two conversations neither of which is a lead: %s", id)
	}
	id, done := s.handoverFor(ctx, "conv1", "lead1", "Lead", quote)
	if !s.handoverSet(id, handoverQueued, "", nil) {
		t.Fatal("not queued")
	}
	done()
	if s.handoverSet(id, handoverAnswered, "", nil) || s.handoverSet(id, handoverQueued, "", nil) {
		t.Fatal("a queued hand-over was answered, or queued again")
	}

	restarted, _ := newQuietServiceOn(t, store)
	if e := onlyHandover(t, restarted); e.ID != id || e.State != handoverQueued || e.To != "lead1" || e.ToTitle != "Lead" || e.MessageID != "m1" {
		t.Fatalf("after a restart: %+v", e)
	}
	if !restarted.handoverSet(id, handoverDelivered, "", nil) {
		t.Fatal("not delivered after a restart")
	}

	// What is kept once one more hand-over is made, from the hand-overs ages (each so long ago) a restart reads.
	keptAfterOneMore := func(ages []time.Duration) []string {
		t.Helper()
		now := time.Now().UTC()
		m := map[string]handover{}
		for i, age := range ages {
			hid := fmt.Sprintf("handover_%02d", i)
			m[hid] = handover{ID: hid, From: "conv1", To: "lead1", State: handoverDelivered, At: now.Add(-age), ChangedAt: now.Add(-age)}
		}
		if err := store.PutSetting(ctx, handoversKey, m); err != nil {
			t.Fatal(err)
		}
		again, _ := newQuietServiceOn(t, store)
		setSettings(t, again, func(v *Settings) { v.Leaders = []string{"lead1"} })
		newest, done := again.handoverFor(ctx, "conv1", "lead1", "Lead", quote)
		again.handoverSet(newest, handoverQueued, "", nil)
		done()
		var ids []string
		for _, e := range handoverList(again) {
			if e.ID == newest {
				e.ID = "newest"
			}
			ids = append(ids, e.ID)
		}
		var saved map[string]handover
		if ok, err := store.Setting(ctx, handoversKey, &saved); !ok || err != nil || len(saved) != len(ids) {
			t.Fatalf("saved %d of %d (%v, %v)", len(saved), len(ids), ok, err)
		}
		return ids
	}
	if ids := keptAfterOneMore([]time.Duration{handoverMemory + time.Hour, handoverMemory - time.Hour}); !slices.Equal(ids, []string{"handover_01", "newest"}) {
		t.Fatalf("kept %v: want the one past %s dropped", ids, handoverMemory)
	}
	ages := make([]time.Duration, handoversKept)
	for i := range ages {
		ages[i] = time.Duration(handoversKept-i) * time.Hour
	}
	ids := keptAfterOneMore(ages)
	if len(ids) != handoversKept || ids[0] != "handover_01" || ids[len(ids)-1] != "newest" {
		t.Fatalf("kept %d from %s to %s: want %d, the oldest dropped", len(ids), ids[0], ids[len(ids)-1], handoversKept)
	}
}
