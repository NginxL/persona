package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// #10400: the relay rate queues instead of refusing, the owner's verified words don't count, and loops are cut by
// round trips rather than by a small per-pair count.

// fillRate counts n messages from → to now, as n sends that went.
func fillRate(t *testing.T, s *Service, from, to string, n int) {
	t.Helper()
	for i := range n {
		if _, err := s.relayAdmit(relayRoute{}, from, to, time.Now()); err != nil {
			t.Fatalf("message %d of the fill: %v", i+1, err)
		}
	}
}

// The owner's words go free once per quoted message and target, whatever the rate and the sender's hop; the same
// quote to the same target again counts as any message.
func TestRelayQuoteFreeOncePerTarget(t *testing.T) {
	s := newTestService(t, newFakeHub())
	fillRate(t, s, "a", "b", relayPairMax)
	s.relays.mu.Lock()
	s.relays.st.Hops["a"] = relayHop{Hop: relayMaxHops, At: time.Now().UTC()}
	s.relays.mu.Unlock()
	quoted := relayRoute{quote: "owner/m1", queue: true}
	a, err := s.relayAdmit(quoted, "a", "b", time.Now())
	if err != nil || !a.free || a.hop != 1 || !a.due.IsZero() {
		t.Fatalf("the owner's words past the rate and the hops: %+v, %v (want free at hop 1)", a, err)
	}
	if _, err := s.relayAdmit(quoted, "a", "b", time.Now()); err == nil || !strings.Contains(err.Error(), "hop 4") {
		t.Fatalf("the same quote again was not counted: %v", err)
	}
	s.relays.mu.Lock()
	delete(s.relays.st.Hops, "a")
	s.relays.mu.Unlock()
	a, err = s.relayAdmit(quoted, "a", "b", time.Now())
	if err != nil || a.free || a.due.IsZero() {
		t.Fatalf("the same quote again past the rate: %+v, %v (want counted and queued)", a, err)
	}
	if a, err := s.relayAdmit(quoted, "a", "c", time.Now()); err != nil || !a.free {
		t.Fatalf("the quote to another conversation: %+v, %v (want free)", a, err)
	}
}

// One-way messages go up to the loose cap without a refusal; past it a queue route waits, any other is refused.
func TestRelayOneWayWithinTheLooseCap(t *testing.T) {
	s := newTestService(t, newFakeHub())
	fillRate(t, s, "a", "b", relayPairMax)
	if _, err := s.relayAdmit(relayRoute{}, "a", "b", time.Now()); err == nil || !strings.Contains(err.Error(), "messages to that conversation") {
		t.Fatalf("past the cap on a route that does not queue: %v", err)
	}
	now := time.Now()
	a, err := s.relayAdmit(relayRoute{queue: true}, "a", "b", now)
	if err != nil || !a.due.After(now) || a.due.After(now.Add(relayWindow+time.Second)) {
		t.Fatalf("past the cap on a queue route: %+v, %v (want a due time within the window)", a, err)
	}
	for i := 1; i < relayQueueMax; i++ {
		s.queueRelay("b", relayMsg{From: "a", Tool: "conversation_send", Text: "waits", At: now.UTC(), Due: a.due.Add(time.Duration(i) * time.Second)})
	}
	s.queueRelay("b", relayMsg{From: "a", Tool: "conversation_send", Text: "waits", At: now.UTC(), Due: a.due})
	if _, err := s.relayAdmit(relayRoute{queue: true}, "a", "b", now); err == nil || !strings.Contains(err.Error(), "already wait for the rate") {
		t.Fatalf("past the queue's limit: %v", err)
	}
}

// Two conversations that keep turning back to each other are cut after relayRoundsMax round trips, even the main
// conversation whose hops are capped; the owner speaking in one of them starts the count again.
func TestRelayRoundTripsCut(t *testing.T) {
	s, send := routedChain(t)
	sent := 0
	var err error
	for ; sent < 100; sent++ {
		from, to := "main", "lead-1"
		if sent%2 == 1 {
			from, to = to, from
		}
		if _, err = send(from, to); err != nil {
			break
		}
	}
	if err == nil || !strings.Contains(err.Error(), "back and forth") {
		t.Fatalf("cut by %v, not the round trips", err)
	}
	if sent != 2*relayRoundsMax+1 {
		t.Fatalf("%d messages went before the cut, want %d", sent, 2*relayRoundsMax+1)
	}
	s.userSpoke("main")
	if _, err := send("main", "lead-1"); err != nil {
		t.Fatalf("after the owner spoke: %v", err)
	}
}

// End to end: past the rate a send is queued and the sender told when it goes in; the owner's verified words go
// straight in, once; the same quote again queues.
func TestConversationSendQueuesPastTheRate(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	stopHolds(t, d.s)
	fillRate(t, d.s, d.mainID, d.sideID, relayPairMax)
	res := d.tool(t, d.mainID, "conversation_send", `{"id":"`+d.sideID+`","text":"one more thing"}`)
	if res.IsError {
		t.Fatalf("past the rate: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "Queued for") || !strings.Contains(text, "goes in at about") || !strings.Contains(text, "once") {
		t.Fatalf("the sender is not told it is queued and when: %q", text)
	}
	if n := d.s.queuedFor(d.sideID); n != 1 {
		t.Fatalf("%d queued, want 1", n)
	}
	before := d.f.sentTo(d.sideID)
	d.f.setItems(proto.Item{ID: "m-owner", Seq: 1, TS: time.Now().UTC(), Role: "user", Kind: proto.KindText,
		Text: "tell the side conversation to stop the export", ClientMsgID: "c-m-owner"})
	res = d.tool(t, d.mainID, "conversation_send", `{"id":"`+d.sideID+`","text":"from the owner","quote":"latest"}`)
	if res.IsError || strings.Contains(res.Content[0].Text, "Queued for") {
		t.Fatalf("the owner's words were held by the rate: %+v", res)
	}
	waitFor(t, "the owner's words to go in", func() bool { return d.f.sentTo(d.sideID) == before+1 })
	res = d.tool(t, d.mainID, "conversation_send", `{"id":"`+d.sideID+`","text":"from the owner again","quote":"latest"}`)
	if res.IsError || !strings.Contains(res.Content[0].Text, "Queued for") {
		t.Fatalf("the same quote again was not counted: %+v", res)
	}
}

// Messages waiting for the rate go in when their time comes, in the order they were sent, each once.
func TestRelayRateQueueGoesInOrderOnce(t *testing.T) {
	s, f := newQuietService(t)
	stopHolds(t, s)
	conv := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	now := time.Now()
	s.queueRelay("conv1", relayMsg{From: "c2", FromTitle: "Research", Tool: "conversation_send", Text: "the first", At: now.UTC(), Due: now.Add(150 * time.Millisecond).UTC()})
	s.queueRelay("conv1", relayMsg{From: "c2", FromTitle: "Research", Tool: "conversation_send", Text: "the second", At: now.UTC(), Due: now.Add(300 * time.Millisecond).UTC()})
	s.drainRelays("server", []proto.Session{conv})
	if n := f.sentTo("conv1"); time.Since(now) < 150*time.Millisecond && n != 0 {
		t.Fatalf("a message went before its time: %d", n)
	}
	waitFor(t, "the first to go in", func() bool { return f.sentTo("conv1") == 1 && s.queuedFor("conv1") == 1 })
	s.drainRelays("server", []proto.Session{conv}) // its turn ended: the list shows it idle again
	waitFor(t, "the second to go in", func() bool { return f.sentTo("conv1") == 2 && s.queuedFor("conv1") == 0 })
	s.drainRelays("server", []proto.Session{conv})
	if n := f.sentTo("conv1"); n != 2 {
		t.Fatalf("%d sends, want each message once", n)
	}
	got := f.notices()
	if len(got) != 2 || !strings.Contains(got[0], "the first") || !strings.Contains(got[1], "the second") {
		t.Fatalf("delivered %q, want the first then the second", got)
	}
}
