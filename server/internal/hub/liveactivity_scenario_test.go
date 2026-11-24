package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

// TestFleetOneCardScenario is #12982's acceptance scenario on the hub's side, in one run: a real permission request,
// a second activity started on the phone, several updates, then the hub going away and coming back. It proves that
// only the newest activity is ever pushed (the one it replaced is ended), that a phone without Lock Screen details gets
// no Allow / Deny while one with details keeps them, and that every pushed count is the fleet's own (GET /api/v1/fleet).
// The phone's side (one card kept, ending without a push route) is LiveActivityStepTests / FleetStateTests.
func TestFleetOneCardScenario(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, sink, stopLoop := localFleetLoop(t)
		stop := sync.OnceFunc(stopLoop)
		defer stop()
		putFleetPrefs(t, h, "pad", `{"lock_details":true}`)
		ask := FleetItem{ID: "m/lead", Title: "Infra lead", Place: "M", Kind: "permission",
			Detail: "Mark the to-do \"Fix the NAS drift\" done (its agent closes when it stops)", Interaction: "ix_1", Since: 1}
		working := []FleetItem{{ID: "m/w1", Title: "w1", Kind: "working"}, {ID: "m/w2", Title: "w2", Kind: "working"}}
		st := FleetState{Phase: "needsYou", NeedsYou: 1, Working: 3, Unread: 6, Items: append([]FleetItem{ask}, working...)}
		reportLive(t, h, st, FleetStats{})
		synctest.Wait()

		last := func(device string) fleetPushCall {
			t.Helper()
			sink.mu.Lock()
			defer sink.mu.Unlock()
			for i := len(sink.got) - 1; i >= 0; i-- {
				if c := sink.got[i]; c.device == device && c.kind == "liveactivity" && c.event == "update" {
					return c
				}
			}
			t.Fatalf("no update to %s: %+v", device, sink.got)
			return fleetPushCall{}
		}
		fleet := func() FleetState {
			t.Helper()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet", nil)
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			w := httptest.NewRecorder()
			h.routes().ServeHTTP(w, req)
			var body struct{ Fleet FleetState }
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
				t.Fatalf("fleet: %d %v", w.Code, err)
			}
			return body.Fleet
		}
		sameCounts := func(c fleetPushCall) {
			t.Helper()
			f := fleet()
			if c.state.NeedsYou != f.NeedsYou || c.state.Working != f.Working || c.state.Unread != f.Unread || c.state.Phase != f.Phase {
				t.Fatalf("pushed %d/%d/%d %s, the fleet says %d/%d/%d %s", c.state.NeedsYou, c.state.Working, c.state.Unread,
					c.state.Phase, f.NeedsYou, f.Working, f.Unread, f.Phase)
			}
		}

		// 1. The request: no buttons where the Lock Screen hides who asks; buttons and the words where it shows them.
		phone, pad := last("phone"), last("pad")
		if phone.token != "phone-update" || phone.state.Items[0].Interaction != "" {
			t.Fatalf("details off: %+v", phone)
		}
		if pad.state.Items[0].Interaction != "ix_1" || pad.state.Items[0].Title != "Infra lead" || pad.state.Items[0].Detail == "" {
			t.Fatalf("details on: %+v", pad.state.Items[0])
		}
		sameCounts(phone)
		t.Logf("step 1: phone %d/%d/%d no buttons; pad buttons for %q: %q", phone.state.NeedsYou, phone.state.Working,
			phone.state.Unread, pad.state.Items[0].Title, pad.state.Items[0].Detail)

		// 2. A second activity on the phone (the app started one beside a card iOS ended): the old one is ended.
		post := func(body string) {
			t.Helper()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/phone/live-activity", strings.NewReader(body))
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			w := httptest.NewRecorder()
			h.routes().ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("report: %d %s", w.Code, w.Body.String())
			}
			synctest.Wait()
		}
		post(`{"activity_id":"phone-activity-2","push_token":"phone-update-2"}`)
		ends := 0
		sink.mu.Lock()
		for _, c := range sink.got {
			if c.kind == "liveactivity" && c.event == "end" && c.token == "phone-update" {
				ends++
			}
		}
		sink.mu.Unlock()
		if ends != 1 {
			t.Fatalf("the replaced activity got %d ends, want 1", ends)
		}
		t.Log("step 2: the replaced activity was ended")

		// 3. Updates: the request answered, then a new one, then one more agent done. Only the new activity gets them,
		// each with the fleet's counts.
		mark := func() int { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.got) }
		from := mark()
		updates := []FleetState{
			{Phase: "working", Working: 3, Unread: 6, Items: working},
			{Phase: "needsYou", NeedsYou: 1, Working: 3, Unread: 6, Items: append([]FleetItem{{ID: "m/x", Title: "x", Kind: "permission",
				Detail: "Run the migration", Interaction: "ix_2", Since: 2}}, working...)},
			{Phase: "working", Working: 2, Unread: 7, Items: working[:1]},
		}
		for i, u := range updates {
			reportLive(t, h, u, FleetStats{})
			synctest.Wait()
			if u.NeedsYou == 0 && i == len(updates)-1 {
				h.live.flush(u, false, true) // an ordinary change goes after the gap; forced here, as its timer would
				synctest.Wait()
			}
			c := last("phone")
			if c.token != "phone-update-2" {
				t.Fatalf("update %d went to %q", i, c.token)
			}
			sameCounts(c)
		}
		sink.mu.Lock()
		for _, c := range sink.got[from:] {
			if c.token == "phone-update" {
				t.Errorf("the replaced activity still gets pushes: %+v", c)
			}
		}
		sink.mu.Unlock()
		t.Logf("step 3: %d updates, all to the newest activity with the fleet's counts", len(updates))

		// 4. The hub goes away and comes back (a restart reads the activities from the store): the phone's card is
		// brought up to date at once, on its newest activity only.
		stop()
		h.live = newLiveActivities(h)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); h.live.run(ctx) }()
		defer func() { cancel(); <-done }()
		synctest.Wait()
		from = mark()
		final := FleetState{Phase: "working", Working: 1, Unread: 8, Items: working[:1]}
		reportLive(t, h, final, FleetStats{})
		synctest.Wait()
		h.live.flush(final, false, true)
		synctest.Wait()
		c := last("phone")
		if c.token != "phone-update-2" {
			t.Fatalf("after the restart the push went to %q", c.token)
		}
		sameCounts(c)
		sink.mu.Lock()
		for _, c := range sink.got[from:] {
			if c.token == "phone-update" {
				t.Errorf("after the restart the replaced activity got %+v", c)
			}
		}
		sink.mu.Unlock()
		t.Logf("step 4: after the restart the newest activity shows %d/%d/%d", c.state.NeedsYou, c.state.Working, c.state.Unread)
	})
}
