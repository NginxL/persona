package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// A card's push carries the manager's level: with the switch on a P1 escalation is held (recorded, no alert) while a
// P0 one and a card that names no level (P0) alert.
func TestQuietCardPushHonoursTheManagersLevel(t *testing.T) {
	h, _ := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	card := func(id string) proto.Interaction {
		return proto.Interaction{ID: id, MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "git push"}
	}
	h.notifyInteractionAt("", card("ix_p1"), "box", false, assistantwire.LevelP1, "")
	if *attempts != 0 {
		t.Fatalf("a P1 escalation alerted %d times", *attempts)
	}
	view := h.push.heldPushesView("", 0)
	if len(view) != 1 || view[0].Reason != interceptReasonP1 || view[0].RelatedKey != "ix:ix_p1" {
		t.Fatalf("intercepts %+v, want the held card", view)
	}
	h.notifyInteractionAt("", card("ix_p0"), "box", false, assistantwire.LevelP0, "")
	h.notifyInteraction("", card("ix_plain"), "box", false)
	h.notifyInteractionAt("", card("ix_bad"), "box", false, "P9", "") // a level this hub does not know: a card interrupts
	if *attempts != 3 {
		t.Fatalf("got %d alerts for P0, plain and unknown-level cards, want 3", *attempts)
	}
}

// With the switch off every card alerts, whatever level the manager named.
func TestQuietOffCardPushIgnoresLevel(t *testing.T) {
	h, _ := gradingHub(t)
	attempts := gradingAttempts(h)
	h.notifyInteractionAt("", proto.Interaction{ID: "ix1", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "ls"},
		"box", false, assistantwire.LevelP1, "")
	if *attempts != 1 {
		t.Fatalf("got %d alerts, want 1", *attempts)
	}
	if view := h.push.heldPushesView("", 0); len(view) != 0 {
		t.Fatalf("held with the switch off: %+v", view)
	}
}

// Reply reminders merge per question, not per agent: with the switch on, one agent's second question within 12 hours
// still rings, while a repeat of the same question does not (an older manager without a key is keyed by the text).
func TestQuietReplyReminderKeyedByQuestion(t *testing.T) {
	h, _ := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	h.notifyReplyKeyed("", "m1", "box", "s1", "Agent", "Which branch?", "reply:s1@1")
	h.notifyReplyKeyed("", "m1", "box", "s1", "Agent", "Deploy now?", "reply:s1@2")
	if *attempts != 2 {
		t.Fatalf("got %d alerts for two questions of one agent, want 2", *attempts)
	}
	h.notifyReply("", "m1", "box", "s2", "Agent", "Which branch?")
	h.notifyReply("", "m1", "box", "s2", "Agent", "Deploy now?")
	if *attempts != 4 {
		t.Fatalf("got %d alerts, want 4: an older manager's two questions are two keys", *attempts)
	}
	if replyKey("s2", "Which branch?", "") == replyKey("s2", "Deploy now?", "") || replyKey("s", "x", "k") != "k" {
		t.Fatal("replyKey")
	}
}

// The switch reaches the manager over the bus, end to end: the hub's hello carries it (Hello.QuietPush), and turning
// it on or off through its route sends manager.updated, which the connected assistant takes. The manager never reads
// the hub's settings itself.
func TestQuietSwitchReachesTheManagerOverTheHello(t *testing.T) {
	h := newTestHub(t)
	h.push.store = newGradingStore()
	a := connectAssistant(t, h)
	quietPush := func() bool { hello, _ := a.bh.Hello(context.Background()); return hello.QuietPush }
	if quietPush() {
		t.Fatal("the manager's hello says on before the switch was turned on")
	}
	mux := http.NewServeMux()
	h.pushGradingRoutes(mux)
	put := func(on bool) {
		t.Helper()
		body := `{"enabled":false}`
		if on {
			body = `{"enabled":true}`
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/push/ln-mode", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
		}
	}
	put(true)
	if hello, _ := (assistantLink{h}).Hello(context.Background()); !hello.QuietPush {
		t.Fatal("the hub's hello does not carry the switch")
	}
	eventually(t, 5*time.Second, "the manager told the switch is on", quietPush)
	put(false)
	eventually(t, 5*time.Second, "the manager told the switch is off", func() bool { return !quietPush() })
}

// Held pushes (P1, P2, and 12h dedup repeats) must skip WS notify, offline notifyring, and closed-browser Web Push
// while grading is on; with grading off, all levels deliver through WS notify and Web Push as before.
func TestPushGradingGatesWebSocketAndWebPush(t *testing.T) {
	f := newFakePushService(t)
	h := webPushHub(t, f)
	h.push.store = newGradingStore()
	mux := h.routes()
	b := f.browser(t, "/push/grading")
	subscribe(t, h, mux, "web-closed", b, "")
	if err := h.push.setPrefs("web-closed", NotifyPrefs{Input: true, Done: DoneAlways}); err != nil {
		t.Fatal(err)
	}
	wsClient := &client{device: "web-open", out: make(chan []byte, 16), meta: clientMeta{features: map[string]bool{"ws_notify": true}}}
	addTestClient(h, wsClient)
	if err := h.push.setPrefs("web-open", NotifyPrefs{Input: true, Done: DoneAlways}); err != nil {
		t.Fatal(err)
	}
	offlineDevice := &Device{DeviceID: "web-offline", Name: "Offline Web"}
	h.push.register(*offlineDevice)
	if err := h.push.setPrefs("web-offline", NotifyPrefs{Input: true, Done: DoneAlways}); err != nil {
		t.Fatal(err)
	}
	h.rings.remember("web-offline", i18n.Default, map[string]bool{"ws_notify": true}, time.Now())

	readWS := func() (map[string]any, bool) {
		select {
		case raw := <-wsClient.out:
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			return m, true
		default:
			return nil, false
		}
	}

	if err := h.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	// Drain push_grading broadcast frame emitted by setPushGrading.
	if msg, ok := readWS(); !ok || msg["type"] != "push_grading" || msg["enabled"] != true {
		t.Fatalf("expected push_grading enabled frame, got %v %v", ok, msg)
	}

	// P1 card: held -> no WS notify, no notifyring, no Web Push; recorded in held pushes.
	p1Card := proto.Interaction{ID: "ix_p1_ws", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "git status"}
	h.notifyInteractionAt("manager-a", p1Card, "box", false, assistantwire.LevelP1, "")
	if msg, ok := readWS(); ok {
		t.Fatalf("P1 card leaked over WS notify: %+v", msg)
	}
	if kept := h.rings.devices["web-offline"].events; len(kept) != 0 {
		t.Fatalf("P1 card buffered in notifyring: %+v", kept)
	}
	f.none(t)

	// P2 done turn: silent -> no WS notify, no Web Push.
	doneOut := h.notifyDoneUnder("manager-a", "done", "m1", "box", "s1", "Task", "Finished", pushWants{Kind: "done", Took: time.Hour, Long: time.Minute}, "", "", pushPolicy{level: levelFor("done", "")})
	if doneOut.held != interceptReasonP2 || doneOut.devices != 0 {
		t.Fatalf("done outcome = %+v, want held P2 with 0 devices", doneOut)
	}
	if msg, ok := readWS(); ok {
		t.Fatalf("P2 done leaked over WS notify: %+v", msg)
	}
	f.none(t)

	// POST /api/v1/notify P1 and P2: held and gated on WS + Web Push; P0 delivers.
	for _, tc := range []struct {
		level  string
		reason string
	}{
		{assistantwire.LevelP1, interceptReasonP1},
		{assistantwire.LevelP2, interceptReasonP2},
	} {
		out, err := h.deliverNotify(notifyRequest{Title: "Held " + tc.level, Body: "b", Level: levelActive, PushLevel: tc.level}, "box", "", "manager-a")
		if err != nil || out["sent"] != false || out["held"] != true || out["reason"] != tc.reason {
			t.Fatalf("deliverNotify %s = %+v, %v", tc.level, out, err)
		}
		if msg, ok := readWS(); ok {
			t.Fatalf("%s notify leaked over WS: %+v", tc.level, msg)
		}
		f.none(t)
	}
	p0Out, err := h.deliverNotify(notifyRequest{Title: "Urgent", Body: "act now", ID: "k1", Level: levelActive, PushLevel: assistantwire.LevelP0}, "box", "", "manager-a")
	if err != nil || p0Out["sent"] != true {
		t.Fatalf("deliverNotify P0 = %+v, %v", p0Out, err)
	}
	if msg, ok := readWS(); !ok || msg["type"] != "notify" || msg["title"] != "Urgent" {
		t.Fatalf("P0 notify missing from WS: %v %+v", ok, msg)
	}
	if got := webOpened(t, b, f.next(t)); got["title"] != "Urgent" {
		t.Fatalf("P0 Web Push = %+v", got)
	}

	// 12h dedup: repeated P1 under same relatedKey skips WS + Web Push.
	dupOut, err := h.deliverNotify(notifyRequest{Title: "Urgent repeat", Body: "later", ID: "k1", Level: levelActive, PushLevel: assistantwire.LevelP1}, "box", "", "manager-a")
	if err != nil || dupOut["sent"] != false || dupOut["held"] != true || dupOut["reason"] != interceptReasonDedup {
		t.Fatalf("dedup deliverNotify = %+v, %v", dupOut, err)
	}
	if msg, ok := readWS(); ok {
		t.Fatalf("dedup notify leaked over WS: %+v", msg)
	}
	f.none(t)

	// Reply reminder + decision: P0 alerts immediately via WS + Web Push; any P1 follow-up under the same key is deduped.
	h.notifyReplyKeyed("manager-a", "m1", "box", "s1", "Agent", "Pick branch", "reply:s1@1")
	if msg, ok := readWS(); !ok || msg["kind"] != "reply" {
		t.Fatalf("reply P0 missing from WS: %v %+v", ok, msg)
	}
	if got := webOpened(t, b, f.next(t)); got["kind"] != "reply" {
		t.Fatalf("reply Web Push = %+v", got)
	}
	repDedup := h.notifyDoneUnder("manager-a", "assistant", "m1", "box", "s1", "Follow-up", "still waiting", pushWants{Kind: "input"}, "", "", pushPolicy{level: assistantwire.LevelP1, relatedKey: "reply:s1@1"})
	if repDedup.held != interceptReasonDedup || repDedup.devices != 0 {
		t.Fatalf("reply follow-up = %+v, want ln_dedup_12h", repDedup)
	}
	if msg, ok := readWS(); ok {
		t.Fatalf("dedup follow-up leaked over WS: %+v", msg)
	}
	f.none(t)

	askedAt := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	todo := assistantwire.NotifyTodo{ID: "td1", AskedAt: askedAt, Recommend: "yes"}
	h.notifyDecide("manager-a", "m1", "box", "s1", "Approve", "Ship?", todo)
	if msg, ok := readWS(); !ok || msg["kind"] != "assistant" {
		t.Fatalf("decide P0 missing from WS: %v %+v", ok, msg)
	}
	if got := webOpened(t, b, f.next(t)); got["kind"] != "assistant" {
		t.Fatalf("decide Web Push = %+v", got)
	}
	decideKey := "todo:" + todo.ID + "@" + askedAt.UTC().Format(time.RFC3339Nano)
	decDedup := h.notifyDoneUnder("manager-a", "assistant", "m1", "box", "s1", "Approve", "note", pushWants{Kind: "input"}, "", "", pushPolicy{level: assistantwire.LevelP1, relatedKey: decideKey})
	if decDedup.held != interceptReasonDedup || decDedup.devices != 0 {
		t.Fatalf("decide follow-up = %+v, want ln_dedup_12h", decDedup)
	}
	if msg, ok := readWS(); ok {
		t.Fatalf("dedup decide leaked over WS: %+v", msg)
	}
	f.none(t)

	// Turn grading OFF: P1 card and done turn notify through WS and Web Push again.
	if err := h.setPushGrading(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if msg, ok := readWS(); !ok || msg["type"] != "push_grading" || msg["enabled"] != false {
		t.Fatalf("expected push_grading disabled frame, got %v %v", ok, msg)
	}
	h.notifyInteractionAt("manager-a", proto.Interaction{ID: "ix_p1_off", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "git push"}, "box", false, assistantwire.LevelP1, "")
	if msg, ok := readWS(); !ok || msg["kind"] != "interaction" {
		t.Fatalf("grading off P1 card missing from WS: %v %+v", ok, msg)
	}
	if got := webOpened(t, b, f.next(t)); got["interaction_id"] != "ix_p1_off" {
		t.Fatalf("grading off Web Push = %+v", got)
	}
}

func TestPushGradingHelloAndLiveBroadcast(t *testing.T) {
	h := newTestHub(t)
	h.push.store = newGradingStore()
	c := &client{device: "web-1", out: make(chan []byte, 4)}
	addTestClient(h, c)
	if got, _ := h.hello()["push_grading"].(bool); got {
		t.Fatal("hello push_grading = true before enabling")
	}
	mux := http.NewServeMux()
	h.pushGradingRoutes(mux)
	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/push/grading", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	put(`{"enabled":true}`)
	if got, _ := h.hello()["push_grading"].(bool); !got {
		t.Fatal("hello push_grading = false after enabling")
	}
	var msg map[string]any
	if err := json.Unmarshal(<-c.out, &msg); err != nil || msg["type"] != "push_grading" || msg["enabled"] != true {
		t.Fatalf("broadcast = %+v, err=%v", msg, err)
	}
	put(`{"enabled":false}`)
	if got, _ := h.hello()["push_grading"].(bool); got {
		t.Fatal("hello push_grading = true after disabling")
	}
	if err := json.Unmarshal(<-c.out, &msg); err != nil || msg["type"] != "push_grading" || msg["enabled"] != false {
		t.Fatalf("broadcast = %+v, err=%v", msg, err)
	}
}
