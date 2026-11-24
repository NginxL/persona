package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func quoteSend(t *testing.T, d *dispatchPolicyHarness, from, to, selector string) proto.ToolResult {
	t.Helper()
	return d.tool(t, from, "conversation_send", `{"id":`+jsonString(to)+`,"text":"prepare the trail map","quote":`+jsonString(selector)+`}`)
}

// Each refusal leaves the node, the durable quote book and the relay rate alone.
func checkQuoteRefusal(t *testing.T, d *dispatchPolicyHarness, selector string, hints ...string) string {
	t.Helper()
	initial := d.f.sent()
	res := quoteSend(t, d, d.mainID, d.leaderID, selector)
	msg := res.Content[0].Text
	if !res.IsError {
		t.Fatalf("quote was accepted: %s", msg)
	}
	for _, hint := range hints {
		if !strings.Contains(msg, hint) {
			t.Errorf("refusal %q does not mention %q", msg, hint)
		}
	}
	var book json.RawMessage
	found, err := d.s.store().Setting(context.Background(), relayQuotesKey, &book)
	if err != nil || found || d.f.sent() != initial {
		t.Errorf("refusal sent %d messages, saved quote book = %t, read error = %v", d.f.sent()-initial, found, err)
	}
	d.s.relays.mu.Lock()
	defer d.s.relays.mu.Unlock()
	if len(d.s.relays.sent) != 0 || len(d.s.relays.quoted) != 0 {
		t.Error("refusal consumed a relay allowance")
	}
	return msg
}

func TestQuoteRequiresCurrentTurn(t *testing.T) {
	owner := ownerItem("trail-map", "prepare the trail map")
	relay := proto.Item{ID: "trail-update", Role: "user", Kind: proto.KindText, Text: "a route update",
		Source: &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "another-chat"}}
	wake := proto.Item{Role: "user", Kind: proto.KindText, Wake: true, Task: &proto.TaskNote{}}
	notice := proto.Item{Role: "user", Kind: proto.KindText, Task: &proto.TaskNote{}}
	queued := ownerItem("trail-label", "choose the green label")
	queued.Queued = true
	for _, tc := range []struct {
		name  string
		items []proto.Item
		hint  string
	}{
		{"another conversation started this turn", []proto.Item{owner, relay}, "nothing in this turn"},
		{"the start fell outside the page", []proto.Item{queued, notice, wake}, "start of this turn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDispatchPolicyHarness(t)
			d.f.setItems(tc.items...)
			checkQuoteRefusal(t, d, "latest", tc.hint, "exact words", "without quote")
		})
	}
	t.Run("explicit earlier words", func(t *testing.T) {
		d := newDispatchPolicyHarness(t)
		d.f.setItems(owner, relay)
		q, err := d.s.resolveQuote(context.Background(), d.mainID, "trail map")
		if err != nil || q == nil || q.MessageID != owner.ID || q.Text != "trail map" {
			t.Fatalf("earlier request = %+v, error = %v", q, err)
		}
	})
	t.Run("background work continues the owner's turn", func(t *testing.T) {
		d := newDispatchPolicyHarness(t)
		d.f.setItems(owner, notice, wake)
		r, err := d.s.resolveQuote(context.Background(), d.mainID, "latest")
		if err != nil || r == nil || r.MessageID != owner.ID {
			t.Fatalf("quote = %+v, error = %v", r, err)
		}
	})
	t.Run("queued choice", func(t *testing.T) {
		d := newDispatchPolicyHarness(t)
		d.f.setItems(owner, queued, notice, wake)
		checkQuoteRefusal(t, d, "latest", owner.ID, owner.Text, queued.ID, queued.Text, "copy the id")
		for _, selector := range []string{queued.ID, queued.Text} {
			r, err := d.s.resolveQuote(context.Background(), d.mainID, selector)
			if err != nil || r == nil || r.MessageID != queued.ID || r.Text != queued.Text {
				t.Fatalf("%q: quote = %+v, error = %v", selector, r, err)
			}
		}
	})
}

func TestQuoteExactWordsAndItemIDs(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	older := ownerItem("trail-label", "old route, 四季花園")
	newer := ownerItem("trail-map", "new trail-label, 四季花園\n\twith a bridge")
	notice := proto.Item{ID: "a bridge", Role: "assistant", Kind: proto.KindText, Text: "route update"}
	d.f.setItems(older, newer, notice)
	for _, selector := range []string{"四季花", "a blue bridge", notice.ID} {
		checkQuoteRefusal(t, d, selector)
	}
	for _, tc := range []struct {
		selector, id, words string
		part                bool
	}{
		{older.ID, older.ID, older.Text, false}, // the older item's id also occurs in the newer words
		{"四季花園", newer.ID, "四季花園", true},        // newest match, four Unicode characters
		{"四季花園\u3000 with", newer.ID, "四季花園\n\twith", true},
		{"new trail-label, 四季花園 with a bridge", newer.ID, newer.Text, false},
	} {
		r, err := d.s.resolveQuote(context.Background(), d.mainID, tc.selector)
		if err != nil || r == nil || r.MessageID != tc.id || r.Text != tc.words {
			t.Errorf("%q: quote = %+v, error = %v", tc.selector, r, err)
			continue
		}
		data, err := json.Marshal(r)
		if err != nil || strings.Contains(string(data), `"part":true`) != tc.part {
			t.Errorf("%q: part flag in %s, error = %v", tc.selector, data, err)
		}
	}
}

func TestQuoteMeetingBoundary(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	d := newDispatchPolicyHarness(t)
	now := time.Now()
	recClock(d.s, func() time.Time { return now })
	public := ownerItem("trail-map", "prepare the trail map")
	question := ownerItem("meeting-question", "choose a sample color")
	question.ClientMsgID = "just-me-example"
	answer := ownerItem("card-answer", "use the sample shade")
	d.f.setItems(public, question, answer)
	d.s.justMeSent("server", d.mainID)
	for _, selector := range []string{"latest", question.ID, question.Text, answer.ID, public.ID} {
		checkQuoteRefusal(t, d, selector, "privately", "meeting", "text")
	}
	// A real Just me mark expires on the service clock. The durable question marker remains.
	now = now.Add(justMeMax + justMeAfter + time.Minute)
	for _, selector := range []string{question.ID, "sample color"} {
		checkQuoteRefusal(t, d, selector, "privately", "meeting", "text")
	}
	question.Queued = true
	answer.Queued = true
	d.f.setItems(public, question, answer)
	msg := checkQuoteRefusal(t, d, "latest", public.ID, answer.ID)
	if strings.Contains(msg, question.ID) || strings.Contains(msg, question.Text) {
		t.Errorf("the choices included a private item: %s", msg)
	}
	question.Queued = false
	d.f.setItems(question)
	checkQuoteRefusal(t, d, "latest", "privately")
	d.f.setItems(public)
	res := quoteSend(t, d, d.mainID, d.leaderID, "latest")
	if res.IsError {
		t.Fatal(res.Content[0].Text)
	}
	d.s.justMeSent("server", d.mainID)
	if res := quoteSend(t, d, d.mainID, d.sideID, ""); res.IsError {
		t.Fatal(res.Content[0].Text)
	}
}

func TestQuotePartsReachIndependentBriefs(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	const first, second = "paint the bridge", "mark the west trail"
	owner := ownerItem("park-plan", first+" and "+second)
	d.f.setItems(owner)
	send := quoteSend(t, d, d.mainID, d.leaderID, first)
	if send.IsError {
		t.Fatal(send.Content[0].Text)
	}
	bridge := received(t, d.f, d.mainID)
	start := d.tool(t, d.mainID, "conversation_new", `{"text":"update the path","quote":`+jsonString(second)+`}`)
	if start.IsError {
		t.Fatal(start.Content[0].Text)
	}
	trail := received(t, d.f, d.mainID)
	startMatch := startedConvRe.FindStringSubmatch(start.Content[0].Text)
	if len(startMatch) != 2 {
		t.Fatal(start.Content[0].Text)
	}
	ref := prompts.ParseRelayQuoteID(bridge.Text)
	if ref == "" || ref == owner.ID || ref == prompts.ParseRelayQuoteID(trail.Text) {
		t.Fatalf("distinct parts need distinct references: %q, %q", ref, prompts.ParseRelayQuoteID(trail.Text))
	}
	check := func(sid string, item proto.Item, want string, via ...string) {
		t.Helper()
		d.f.setItems(item)
		b := planBrief(t, d, sid)
		raw, err := json.Marshal(b.Request)
		if err != nil || b.Request == nil || b.Request.Text != want || b.Request.MessageID != owner.ID ||
			!b.Request.At.Equal(quotedAt) || b.Request.Conversation != d.mainID || !slices.Equal(b.Request.Via, via) ||
			!strings.Contains(string(raw), `"part":true`) || strings.Contains(string(raw), "part_ref") {
			t.Fatalf("request = %s, error = %v", raw, err)
		}
	}
	check(startMatch[1], trail, second, d.mainID)
	check(d.leaderID, bridge, first, d.mainID)
	if _, err := d.s.resolveQuote(context.Background(), d.leaderID, second); err == nil {
		t.Fatal("the receiver could select words outside its kept part")
	}
	forward := quoteSend(t, d, d.leaderID, d.sideID, ref)
	if forward.IsError {
		t.Fatal(forward.Content[0].Text)
	}
	next := received(t, d.f, d.leaderID)
	if got := prompts.ParseRelayQuoteID(next.Text); got != ref {
		t.Errorf("forwarded reference = %q, want %q", got, ref)
	}
	check(d.sideID, next, first, d.mainID, d.leaderID)
}

// The brief is bounded, but a passage can be anywhere in the original. Two passages with the same clipped
// prefix must remain distinguishable in the queue and on their next hop.
func TestQuoteLongPartsSurviveQueueAndRestart(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	ctx := context.Background()
	prefix := strings.Repeat("木", briefRequestMax)
	first, second := prefix+" east trail", prefix+" west trail"
	owner := ownerItem("park-plan", strings.Repeat("x", briefRequestMax+1)+" "+first+" / "+second)
	d.f.setItems(owner)
	var refs []string
	for _, words := range []string{first, second} {
		q, err := d.s.resolveQuote(ctx, d.mainID, words)
		if err != nil || q == nil || q.Text != prefix || !q.Part || !q.Truncated {
			t.Fatalf("selected clipped part: %v, error = %v", q, err)
		}
		d.s.keepQuote(ctx, d.mainID, *q)
		// The relay queue persists in JSON until the receiver can take it.
		msg := relayMsg{From: d.mainID, FromTitle: "Park", Tool: "conversation_send", Text: "update the map", Hop: 1, Quote: q}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		var restored relayMsg
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, prompts.ParseRelayQuoteID(relayText(restored)))
	}
	if refs[0] == "" || refs[0] == refs[1] {
		t.Fatal("the two clipped parts shared a reference")
	}
	restarted := New(Options{Hub: d.f, Store: func() Store { return d.s.store() }})
	for _, ref := range refs {
		saved := restarted.verifiedQuote(ctx, d.mainID, ref)
		if saved == nil || saved.reference() != ref || saved.MessageID != owner.ID || saved.Text != prefix || !saved.Truncated {
			t.Fatalf("the saved part with reference %s did not survive", ref)
		}
	}
	// An old whole-message queue record has no part fields and still renders with its source message id.
	var legacy relayMsg
	if err := json.Unmarshal([]byte(`{"quote":{"text":"paint the bridge","message_id":"legacy-message","conversation":"park"}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if q := relayQuoteOf(legacy.Quote); q.Part || q.ID != "legacy-message" || q.Text != "paint the bridge" {
		t.Fatalf("legacy queue quote = %+v", q)
	}
}

func TestQuotePartsShareOneRelayAllowance(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	owner := ownerItem("park-plan", "paint the bridge and mark the west trail")
	owner.TS = time.Now()
	d.f.setItems(owner)
	for _, words := range []string{"paint the bridge", "mark the west trail"} {
		if res := quoteSend(t, d, d.mainID, d.sideID, words); res.IsError {
			t.Fatal(res.Content[0].Text)
		}
	}
	d.s.relays.mu.Lock()
	defer d.s.relays.mu.Unlock()
	if len(d.s.relays.quoted) != 1 || len(d.s.relays.sent[[2]string{d.mainID, d.sideID}]) != 1 {
		t.Fatal("only the first passage from a message to this target should go free")
	}
}

func TestQuoteChecksMeetingMarkAfterTranscriptRead(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	d := newDispatchPolicyHarness(t)
	d.f.setItems(ownerItem("trail-map", "prepare the trail map"))
	d.f.hookReads(receiptHooks{after: func(read int) {
		if read == 1 {
			d.s.justMeSent("server", d.mainID)
		}
	}})
	checkQuoteRefusal(t, d, "latest", "privately", "meeting", "text")
}
