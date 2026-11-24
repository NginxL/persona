package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func syntheticWaterNotification() assistantwire.Notification {
	return assistantwire.Notification{ID: "synthetic-water-reminder", Kind: assistantwire.NotifyHealthWater,
		Title: "Time for some water", Body: "Today 250 / 2000 ml", Level: assistantwire.LevelP1,
		Health: &assistantwire.NotifyHealth{Reminder: "synthetic-water-reminder"}}
}

func TestHealthWaterPayloadPolicyAndFixedCollapse(t *testing.T) {
	h, f := gradingHub(t)
	// This is a user-requested reminder, so quiet hours must not silence it.
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneOff, Quiet: QuietHours{Enabled: true, Start: "00:00", End: "23:59", TZ: "UTC"}}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		n := syntheticWaterNotification()
		if i == 1 {
			n.ID, n.Health.Reminder = "synthetic-next-reminder", "synthetic-next-reminder"
		}
		out, err := h.deliverPushFrom(assistantwire.AssistantID, n)
		if err != nil || out == nil || out.devices != 1 || out.held != "" {
			t.Fatalf("delivery: %+v %v", out, err)
		}
		calls := f.calls(t, i+1)
		var payload map[string]any
		if err := json.Unmarshal(calls[i].Body, &payload); err != nil {
			t.Fatal(err)
		}
		aps := payload["aps"].(map[string]any)
		if aps["category"] != "HEALTH_WATER" || aps["sound"] != "default" || aps["interruption-level"] != "active" || calls[i].Collapse != "health-water" || payload["health"].(map[string]any)["reminder"] != n.ID {
			t.Fatalf("payload: %s; collapse %s", calls[i].Body, calls[i].Collapse)
		}
		for _, key := range []string{"todo", "extension_notification", "session_id", "machine_id"} {
			if _, ok := payload[key]; ok {
				t.Fatalf("water reminder exposed %s", key)
			}
		}
	}
}

func TestHealthWaterHeldForBriefingWithoutRelatedKey(t *testing.T) {
	h, f := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	out, err := h.deliverPushFrom(assistantwire.AssistantID, syntheticWaterNotification())
	if err != nil || out.held != interceptReasonP1 || *attempts != 0 || len(f.calls(t, 0)) != 0 {
		t.Fatalf("not held: %+v %v", out, err)
	}
	view := h.push.heldPushesView("", 0)
	if len(view) != 1 || view[0].Level != assistantwire.LevelP1 || view[0].RelatedKey != "" {
		t.Fatalf("briefing: %+v", view)
	}
}

func TestHealthWaterWSPreferencesAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prefs NotifyPrefs
		want  bool
	}{
		{"input on", NotifyPrefs{Input: true, Done: DoneOff}, true},
		{"input off", NotifyPrefs{Input: false, Done: DoneAlways}, false},
		{"muted manager", NotifyPrefs{Input: true, MutedManagers: []string{assistantwire.AssistantID}}, false},
		{"phone muted while desktop present", NotifyPrefs{Input: true, MuteWhileDesktop: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			c := &client{device: "synthetic-device", out: make(chan []byte, 4)}
			h.clients = map[*client]struct{}{c: {}}
			if tc.prefs.MuteWhileDesktop {
				addTestClient(h, &client{desktop: true})
			}
			h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
			<-c.out
			if err := h.push.setPrefs(c.device, tc.prefs); err != nil {
				t.Fatal(err)
			}
			out, err := h.deliverPushFrom(assistantwire.AssistantID, syntheticWaterNotification())
			if err != nil || out == nil {
				t.Fatal(err)
			}
			select {
			case raw := <-c.out:
				var msg map[string]any
				json.Unmarshal(raw, &msg)
				if !tc.want || msg["kind"] != "alert" || msg["id"] != "health-water" || msg["health"].(map[string]any)["reminder"] != "synthetic-water-reminder" {
					t.Fatalf("WS payload: %s", raw)
				}
			default:
				if tc.want {
					t.Fatal("missing WS alert")
				}
			}
		})
	}
	h := newTestHub(t)
	for _, change := range []func(*assistantwire.Notification){
		func(n *assistantwire.Notification) { n.Health = nil },
		func(n *assistantwire.Notification) { n.Health.Reminder = "" },
		func(n *assistantwire.Notification) { n.Health.Reminder = strings.Repeat("r", 129) },
		func(n *assistantwire.Notification) { n.Level = assistantwire.LevelP0 },
		func(n *assistantwire.Notification) { n.RelatedKey = "synthetic-key" },
		func(n *assistantwire.Notification) { n.SessionID = "synthetic-session" },
	} {
		n := syntheticWaterNotification()
		change(&n)
		if _, err := h.deliverPushFrom(assistantwire.AssistantID, n); err == nil {
			t.Fatalf("invalid reminder accepted: %+v", n)
		}
	}
	if _, err := h.deliverPushFrom("synthetic-other-manager", syntheticWaterNotification()); err == nil {
		t.Fatal("another manager forged the default manager's water buttons")
	}
}
