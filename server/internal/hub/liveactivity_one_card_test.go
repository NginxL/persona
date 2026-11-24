package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #12982: one Fleet card per phone, and no Allow / Deny the Lock Screen cannot explain.

func TestFleetForDeviceDropsButtonsWithoutDetails(t *testing.T) {
	st := FleetState{Phase: "needsYou", NeedsYou: 1, Items: []FleetItem{{ID: "m/s", Title: "Infra lead", Kind: "permission",
		Detail: "Mark the to-do done", Interaction: "ix_1"}}}
	hidden := forDevice(st, NotifyPrefs{LiveActivity: true}, FleetStats{})
	if hidden.Items[0].Interaction != "" {
		t.Fatalf("details off keeps the buttons: %+v", hidden.Items[0])
	}
	if st.Items[0].Interaction != "ix_1" {
		t.Fatal("forDevice changed the shared fleet")
	}
	shown := forDevice(st, NotifyPrefs{LiveActivity: true, LockDetails: true}, FleetStats{})
	if shown.Items[0].Interaction != "ix_1" || hidden.NeedsYou != 1 || hidden.Items[0].Title != "Infra lead" {
		t.Fatalf("details on: %+v / %+v", shown.Items[0], hidden)
	}
}

func TestLiveActivityReportEndsTheActivityItReplaces(t *testing.T) {
	h, f := testLive(t)
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	post := func(body string) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/devices/phone/live-activity", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%v %v", err, resp)
		}
		resp.Body.Close()
	}
	post(`{"activity_id":"a1","push_token":"old"}`)
	post(`{"activity_id":"a1","push_token":"old2"}`) // a rotation is the same activity: nothing ends
	post(`{"activity_id":"a2","push_token":"new"}`)
	calls := f.calls(t, 1)
	if len(calls) != 1 || calls[0].Token != "old2" || calls[0].Event != "end" || calls[0].Type != "liveactivity" {
		t.Fatalf("want one end to the replaced activity, got %+v", calls)
	}
	if r := h.live.rows["phone"]; r.ActivityID != "a2" || r.PushToken != "new" {
		t.Fatalf("row %+v", r)
	}
}

func TestFleetRetiresAnActivityBeforeTheSystemEndsIt(t *testing.T) {
	h, f, clock := fleetClock(t)
	h.live.mu.Lock()
	h.live.last = FleetState{Phase: "working", Working: 1}
	h.live.started["phone"] = h.live.episode
	h.live.mu.Unlock()
	*clock = clock.Add(fleetMaxAge - fleetRetireBefore - time.Minute)
	h.live.retire(*clock)
	if h.live.rows["phone"].PushToken != "acttok" {
		t.Fatal("retired too early")
	}
	*clock = clock.Add(2 * time.Minute)
	h.live.retire(*clock)
	calls := f.calls(t, 1)
	if calls[0].Token != "acttok" || calls[0].Event != "end" {
		t.Fatalf("%+v", calls)
	}
	h.live.mu.Lock()
	r := h.live.rows["phone"]
	_, started := h.live.started["phone"]
	h.live.mu.Unlock()
	if r.PushToken != "" || started {
		t.Fatalf("row %+v, still started this episode: %v", r, started)
	}
}
