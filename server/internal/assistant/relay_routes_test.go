package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// #10099: the relay limits keep loops and spread in check without cutting off the main conversation's dispatching.

// routedChain is relayChain with the main conversation "main" and the leader conversations "lead-1" … "lead-12":
// every message goes on the route the settings give it, as conversation_send does.
func routedChain(t *testing.T) (*Service, func(from, to string) (int, error)) {
	t.Helper()
	s := newTestService(t, newFakeHub())
	leaders := []string{}
	for _, n := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"} {
		leaders = append(leaders, "lead-"+n)
	}
	route := func(from string) relayRoute {
		if from == "main" {
			return relayRoute{fromMain: true, leaders: leaders}
		}
		return relayRoute{}
	}
	send := func(from, to string) (int, error) {
		hop, answer, err := s.relayAllowedOn(route(from), from, to, time.Now())
		if err == nil {
			s.relayDelivered(context.Background(), to, relayMsg{From: from, Tool: "conversation_send", Hop: hop, Answer: answer, At: time.Now().UTC()})
		}
		return hop, err
	}
	return s, send
}

// A leader's report reaches the main conversation at the last hop: the main conversation still reaches the leaders
// and opens new conversations, at the last hop, so what it asks can be answered but goes no further.
func TestRelayMainNotCutOffAtTheLastHop(t *testing.T) {
	s, send := routedChain(t)
	for i, want := range []int{1, 1, 2, 2, 3} {
		from, to := "lead-1", "main"
		if i%2 == 1 {
			from, to = to, from
		}
		forgetRate(s)
		if hop, err := send(from, to); err != nil || hop != want {
			t.Fatalf("message %d, %s → %s: hop %d, %v (want hop %d)", i+1, from, to, hop, err, want)
		}
	}
	// main works on hop 3 from lead-1 and owes it the answer: to another leader it is a request past the limit.
	if hop, err := send("main", "lead-2"); err != nil || hop != relayMaxHops {
		t.Fatalf("main → lead-2 after a hop-3 report: hop %d, %v (want hop %d)", hop, err, relayMaxHops)
	}
	if hop, _, err := s.relayAllowedOn(relayRoute{fromMain: true}, "main", relayNewKey, time.Now()); err != nil || hop != relayMaxHops {
		t.Fatalf("main's conversation_new after a hop-3 report: hop %d, %v", hop, err)
	}
	// lead-2 may answer main, but can't go further.
	if hop, err := send("lead-2", "main"); err != nil || hop != relayMaxHops {
		t.Fatalf("lead-2's answer: hop %d, %v", hop, err)
	}
	if _, err := send("lead-2", "other"); err == nil || !strings.Contains(err.Error(), "hop 4") {
		t.Fatalf("lead-2 went further than the last hop: %v", err)
	}
	// Any other conversation is still cut at the last hop: lead-1 sent at hop 3, main answers, lead-1's next is hop 4.
	if hop, err := send("main", "lead-1"); err != nil || hop != relayMaxHops {
		t.Fatalf("main's answer to lead-1: hop %d, %v", hop, err)
	}
	if _, err := send("lead-1", "main"); err == nil || !strings.Contains(err.Error(), "hop 4") {
		t.Fatalf("lead-1's fourth request to main went: %v", err)
	}
}

// A leader can't keep the main conversation going: its requests cost hops as anyone's do, and the main
// conversation's own messages to it stay under the per-pair cap.
func TestRelayMainAndLeaderStillCutOff(t *testing.T) {
	s, send := routedChain(t)
	sent := 0
	for ; sent < 100; sent++ {
		from, to := "lead-1", "main"
		if sent%2 == 1 {
			from, to = to, from
		}
		forgetRate(s)
		if _, err := send(from, to); err != nil {
			if !strings.Contains(err.Error(), "hop") {
				t.Fatalf("cut by %v, not the hops", err)
			}
			break
		}
	}
	if sent != 2*relayMaxHops {
		t.Fatalf("%d messages went before the cut, want %d", sent, 2*relayMaxHops)
	}
	forgetRate(s)
	for i := range relayPairMax {
		if _, err := send("main", "lead-3"); err != nil {
			t.Fatalf("main → lead-3, message %d: %v", i+1, err)
		}
	}
	if _, err := send("main", "lead-3"); err == nil || !strings.Contains(err.Error(), "messages to that conversation") {
		t.Fatalf("main went past the per-pair cap to a leader: %v", err)
	}
}

// Messages from the main conversation to its leaders do not use up its per-sender cap: it reaches twelve leaders in a
// round and still has its cap for everyone else. Anyone else keeps the cap.
func TestRelayMainToLeadersSkipsThePerSenderCap(t *testing.T) {
	_, send := routedChain(t)
	for i := 1; i <= 12; i++ {
		s := "lead-" + []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"}[i-1]
		if _, err := send("main", s); err != nil {
			t.Fatalf("main → %s: %v", s, err)
		}
		if _, err := send(s, "main"); err != nil { // the answer brings main back near the user for the next one
			t.Fatalf("%s → main: %v", s, err)
		}
	}
	for i := range relayFromMax {
		if _, err := send("main", "other-"+string(rune('a'+i))); err != nil {
			t.Fatalf("main → other conversation %d: %v", i+1, err)
		}
	}
	if _, err := send("main", "other-z"); err == nil || !strings.Contains(err.Error(), "other conversations") {
		t.Fatalf("main went past its per-sender cap to non-leaders: %v", err)
	}
	_, send2 := routedChain(t)
	for i := range relayFromMax {
		if _, err := send2("lead-1", "x-"+string(rune('a'+i))); err != nil && !strings.Contains(err.Error(), "hop") {
			t.Fatalf("lead-1 message %d: %v", i+1, err)
		}
	}
}

// conversation_new: three per sender in ten minutes, ten for the main conversation; a start that fails gives its
// place back.
func TestConversationNewBudgetAndFailedStarts(t *testing.T) {
	h := newSpawnHarness(t)
	open := func(self string) (string, bool) {
		res := h.s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: self, Name: "conversation_new",
			Arguments: json.RawMessage(`{"text":"look into it"}`)})
		return res.Content[0].Text, !res.IsError
	}
	h.f.mu.Lock()
	h.f.failAssistant = errors.New("the node is away")
	h.f.mu.Unlock()
	for i := range 5 {
		if text, ok := open("parent-1"); ok {
			t.Fatalf("start %d went with a failing node: %s", i+1, text)
		}
	}
	h.f.mu.Lock()
	h.f.failAssistant = nil
	h.f.mu.Unlock()
	for i := range relayNewMax {
		if text, ok := open("parent-1"); !ok {
			t.Fatalf("start %d after failed ones was refused: %s", i+1, text)
		}
	}
	if text, ok := open("parent-1"); ok || !strings.Contains(text, "conversations started from this one") {
		t.Fatalf("a fourth start in ten minutes: %v %s", ok, text)
	}

	// The main conversation is the dispatch center: it gets relayNewMainMax.
	h2 := newSpawnHarness(t)
	setSettings(t, h2.s, func(v *Settings) { v.Main = "parent-1" })
	for i := range relayNewMainMax {
		res := h2.s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: "parent-1", Name: "conversation_new",
			Arguments: json.RawMessage(`{"text":"look into it"}`)})
		if res.IsError {
			t.Fatalf("main's start %d: %+v", i+1, res)
		}
	}
	res := h2.s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: "parent-1", Name: "conversation_new",
		Arguments: json.RawMessage(`{"text":"look into it"}`)})
	if !res.IsError {
		t.Fatal("main went past its conversation_new budget")
	}
}

// A follow-up that wakes a conversation starts a new chain, but only once per conversation in the relay window: a
// conversation cut off by the hops can't schedule itself a minute ahead again and again to keep a loop going.
func TestScheduledWakeStartsANewChain(t *testing.T) {
	s, send := relayChain(t)
	for i := range 2 * relayMaxHops {
		from, to := "a", "b"
		if i%2 == 1 {
			from, to = to, from
		}
		forgetRate(s)
		if _, err := send(from, to); err != nil {
			t.Fatalf("message %d: %v", i+1, err)
		}
	}
	forgetRate(s)
	if _, err := send("a", "b"); err == nil || !strings.Contains(err.Error(), "hop 4") {
		t.Fatalf("a's fourth request went: %v", err)
	}
	now := time.Now()
	s.scheduledWake("a", now)
	if hop, err := send("a", "b"); err != nil || hop != 1 {
		t.Fatalf("after a's follow-up woke it: hop %d, %v (want a new chain at hop 1)", hop, err)
	}
	// A second wake in the same window does not start another chain.
	for i := range 2 {
		forgetRate(s)
		if _, err := send("b", "a"); err != nil && i == 0 {
			t.Fatal(err)
		}
		forgetRate(s)
		_, _ = send("a", "b")
	}
	s.relays.mu.Lock()
	before := s.relays.st.Hops["a"]
	s.relays.mu.Unlock()
	s.scheduledWake("a", now.Add(time.Minute))
	s.relays.mu.Lock()
	after, kept := s.relays.st.Hops["a"]
	s.relays.mu.Unlock()
	if !kept || after.Hop != before.Hop {
		t.Fatalf("a second wake within the window reset the chain: %+v → %+v", before, after)
	}
	s.scheduledWake("a", now.Add(relayWindow+time.Second))
	s.relays.mu.Lock()
	_, kept = s.relays.st.Hops["a"]
	s.relays.mu.Unlock()
	if kept {
		t.Fatal("a wake after the window did not start a new chain")
	}
}

// The scheduler calls it: a follow-up run into its own conversation (new_conversation=false) clears the hop.
func TestFollowupRunIntoItsConversationStartsANewChain(t *testing.T) {
	h := newSpawnHarness(t)
	h.s.relays.mu.Lock()
	if !h.s.relayLocked(context.Background()) {
		t.Fatal("relay state")
	}
	h.s.relays.st.Hops["parent-1"] = relayHop{Hop: relayMaxHops, At: time.Now().UTC(), From: "lead-1"}
	h.s.relays.mu.Unlock()
	task := Task{ID: "fu_1", Title: "check on the leaders", Prompt: "check", Repeat: repeatOnce, Enabled: true,
		MachineID: "server", SessionID: "parent-1", At: time.Now()}
	setTasks(h.s, task)
	sess, err := h.s.runTask(task, time.Now(), time.Now(), false, false)
	if err != nil || sess.SessionID != "parent-1" {
		t.Fatalf("follow-up run: %+v %v", sess, err)
	}
	if hop, _, err := h.s.relayAllowed("parent-1", "lead-1", time.Now()); err != nil || hop != 1 {
		t.Fatalf("after the follow-up woke it: hop %d, %v (want hop 1)", hop, err)
	}
}
