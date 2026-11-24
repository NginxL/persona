package hub

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/pushcrypto"
	"github.com/nwverse/persona/server/internal/relayproto"
)

// The pace of the widgets' wakes (#5721): Needs You at once, other changes after widgetGap, a heartbeat when nothing
// changed, and the first time always.
func TestWidgetWakePace(t *testing.T) {
	var w widgetWaker
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	c := widgetCounts{needsYou: 1, working: 2}
	if !w.due("phone", c, t0) {
		t.Fatal("first time: not due")
	}
	w.wrote("phone", c, t0)
	cases := []struct {
		name  string
		c     widgetCounts
		after time.Duration
		due   bool
	}{
		{"same counts, soon", c, time.Minute, false},
		{"needs you changed, at once", widgetCounts{needsYou: 2, working: 2}, time.Second, true},
		{"needs you back to none, at once", widgetCounts{needsYou: 0, working: 2}, time.Second, true},
		{"working changed, within the gap", widgetCounts{needsYou: 1, working: 3}, widgetGap - time.Second, false},
		{"working changed, after the gap", widgetCounts{needsYou: 1, working: 3}, widgetGap, true},
		{"finished changed, after the gap", widgetCounts{needsYou: 1, working: 2, unread: 1}, widgetGap, true},
		{"intake changed, within the gap", widgetCounts{needsYou: 1, working: 2, intake: 1}, widgetGap - time.Second, false},
		{"intake changed, at the gap", widgetCounts{needsYou: 1, working: 2, intake: 2}, widgetGap, true},
		{"needs you with intake, at once", widgetCounts{needsYou: 2, working: 2, intake: 3}, time.Second, true},
		{"nothing changed, before the heartbeat", c, widgetHeartbeat - time.Second, false},
		{"nothing changed, the heartbeat", c, widgetHeartbeat, true},
	}
	for _, tc := range cases {
		if got := w.due("phone", tc.c, t0.Add(tc.after)); got != tc.due {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.due)
		}
	}
	w.forget("phone")
	if !w.due("phone", c, t0.Add(time.Second)) {
		t.Error("forgotten: not due again")
	}
}

// Through the hub: a phone that has widgets and is not open gets one silent "widgets" wake for a change, none for the
// same counts again, another at once when Needs You changes; one without widgets gets none.
func TestWidgetWakePushes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		key, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sink := &widgetWakeSink{key: key}
		h.push.relay = &http.Client{Transport: sink}
		publicKey := base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
		for _, id := range []string{"phone", "other"} {
			if err := h.push.register(Device{DeviceID: id, Name: id, APNSToken: id + "-token",
				Topic: "app.example", PushKey: publicKey, RelayURL: "https://relay.example", RelayGrant: "grant-" + id}); err != nil {
				t.Fatal(err)
			}
		}
		prefs := devicePrefs(nil)
		prefs.Widgets = true
		if err := h.push.setPrefsBy("phone", "", prefs); err != nil {
			t.Fatal(err)
		}
		clock := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
		h.live.now = func() time.Time { return clock }
		wakes := func() (phone, other int) {
			return sink.count("phone"), sink.count("other")
		}
		checkWakes := func(want int) {
			t.Helper()
			synctest.Wait()
			if got, _ := wakes(); got != want {
				t.Fatalf("phone wakes = %d, want %d", got, want)
			}
		}

		h.live.wakeWidgets(FleetState{Phase: "working", Working: 1})
		checkWakes(1)
		clock = clock.Add(time.Minute)
		h.live.wakeWidgets(FleetState{Phase: "working", Working: 1})
		h.live.wakeWidgets(FleetState{Phase: "working", Working: 2}) // within widgetGap: held
		checkWakes(1)
		if h.live.widgets.due("phone", widgetCounts{working: 2}, clock) {
			t.Fatal("a second change bypassed the inter-wake gap")
		}
		h.live.wakeWidgets(FleetState{Phase: "needsYou", NeedsYou: 1, Working: 2})
		checkWakes(2)
		if _, other := wakes(); other != 0 {
			t.Fatalf("a phone without widgets was woken %d times", other)
		}
		// The app went to the background with these counts: it wrote them, so no wake for them after it.
		h.live.phoneLeft("phone", true)
		clock = clock.Add(widgetGap)
		h.live.wakeWidgets(h.live.shownState)
		checkWakes(2)
		if h.live.widgets.due("phone", widgetCountsOf(h.live.shownState), clock) {
			t.Fatal("the counts written when the phone left were sent again")
		}
		// Intake moves only a widget revision, not the fleet. A held revision is picked up
		// by the regular tick even if there are no more drinks after this one.
		h.live.intakeChanged()
		h.live.wakeWidgets(h.live.shownState)
		checkWakes(3)
		clock = clock.Add(time.Second)
		h.live.intakeChanged()
		h.live.wakeWidgets(h.live.shownState)
		checkWakes(3)
		clock = clock.Add(widgetGap - time.Second)
		h.live.wakeWidgets(h.live.shownState)
		checkWakes(4)
		// An open phone gets the live invalidation and does not need a silent wake. The change comes the way the hub
		// gets one (relayBroadcastFrom): the app is sent the event, so it is what the app shows when it leaves.
		phone := &client{phone: true, pushDevice: "phone", out: make(chan []byte, 8)}
		h.mu.Lock()
		h.clients[phone] = struct{}{}
		h.mu.Unlock()
		clock = clock.Add(widgetGap)
		if err := h.relayBroadcastFrom(assistantwire.AssistantID, json.RawMessage(`{"type":"health_intake"}`)); err != nil {
			t.Fatal(err)
		}
		h.live.wakeWidgets(h.live.shownState)
		checkWakes(4)
		h.live.phoneLeft("phone", true)
		if h.live.widgets.due("phone", h.live.widgetCounts(h.live.shownState), clock) {
			t.Fatal("phone leaving forgot the intake revision it displayed")
		}
		h.mu.Lock()
		delete(h.clients, phone)
		h.mu.Unlock()
	})
}

// widgetWakeSink is an in-memory relay: synctest can settle the push goroutines without a live HTTP server.
// It opens the same sealed payload as the phone and counts only the real silent "widgets" wake.
type widgetWakeSink struct {
	key *ecdh.PrivateKey
	mu  sync.Mutex
	got []string
}

func (s *widgetWakeSink) RoundTrip(r *http.Request) (*http.Response, error) {
	var req relayproto.PushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	ciphertext, err := base64.StdEncoding.DecodeString(req.Sealed)
	if err != nil {
		return nil, err
	}
	plain, err := pushcrypto.Open(s.key, ciphertext)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(plain, &body); err != nil {
		return nil, err
	}
	if req.Type == "background" && body["persona_wake"] == "widgets" {
		device := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer grant-")
		s.mu.Lock()
		s.got = append(s.got, device)
		s.mu.Unlock()
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
}

func (s *widgetWakeSink) count(device string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, got := range s.got {
		if got == device {
			n++
		}
	}
	return n
}

// An open phone writes its widgets from the health_intake events it is sent. When an intake change from elsewhere
// has moved the revision but the relay has not sent the phone the event yet, and the app goes to the background in
// that moment, its widgets still show the older revision: the next wake after widgetGap must not be held back (#13275).
func TestWidgetWakeIntakeNotSentBeforePhoneLeft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		key, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sink := &widgetWakeSink{key: key}
		h.push.relay = &http.Client{Transport: sink}
		if err := h.push.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "phone-token", Topic: "app.example",
			PushKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), RelayURL: "https://relay.example",
			RelayGrant: "grant-phone"}); err != nil {
			t.Fatal(err)
		}
		prefs := devicePrefs(nil)
		prefs.Widgets = true
		if err := h.push.setPrefsBy("phone", "", prefs); err != nil {
			t.Fatal(err)
		}
		clock := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
		h.live.now = func() time.Time { return clock }
		open := func() *client {
			c := &client{phone: true, pushDevice: "phone", out: make(chan []byte, 8)}
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
			return c
		}
		leave := func(c *client) {
			h.mu.Lock()
			delete(h.clients, c)
			h.mu.Unlock()
			h.live.phoneLeft("phone", true)
		}

		// Sent the event while open: the revision it wrote as it left needs no wake.
		phone := open()
		if err := h.relayBroadcastFrom(assistantwire.AssistantID, json.RawMessage(`{"type":"health_intake"}`)); err != nil {
			t.Fatal(err)
		}
		leave(phone)
		clock = clock.Add(widgetGap)
		h.live.wakeWidgets(h.live.shownState)
		synctest.Wait()
		if got := sink.count("phone"); got != 0 {
			t.Fatalf("woken %d times for the revision the app was sent", got)
		}

		// The revision moved (intakeChanged runs before the relay sends the event) and the app left before it was sent.
		phone = open()
		h.live.intakeChanged()
		leave(phone)
		clock = clock.Add(widgetGap)
		h.live.wakeWidgets(h.live.shownState)
		synctest.Wait()
		if got := sink.count("phone"); got != 1 {
			t.Fatalf("widgets woken %d times after the app left before the revision was sent, want 1", got)
		}
	})
}
