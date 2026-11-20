package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestNotifyRequestDoesNotLosePushWhenWebSocketQueueIsFull(t *testing.T) {
	const title = "Example task result"
	h := ringHub(t)
	phone := ringConnect(t, h, "phone", "ws_notify")
	// Leave the writer blocked and fill its bounded outbound queue. This phone
	// has no platform push route. Its hello registered the reconnect buffer.
	for range cap(phone.out) {
		phone.out <- []byte(`{"type":"pending"}`)
	}
	req := assistantwire.NotifyRequest{Notification: assistantwire.Notification{
		ID: "ntf_example", Kind: assistantwire.NotifyPush, Title: title,
	}}
	call := func(method string, input any) []byte {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response, err := busMethods[method](h, context.Background(), nil, raw)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", method, wire)
		return wire
	}
	call(assistantwire.MethodNotify, req)
	if !phone.dropped.Load() {
		t.Fatal("fixture did not exercise rejection by the full WebSocket queue")
	}
	for _, msg := range ringDecoded(t, ringSent(phone)) {
		if msg["type"] != "pending" {
			t.Fatalf("full queue unexpectedly admitted the frame: %v", msg)
		}
	}
	ringDisconnect(h, phone)
	// If the manager missed the acknowledgment, its outbox repeats the ID.
	// The replay must not create a second copy.
	retry := assistantwire.Outbox{Items: []assistantwire.OutboxItem{{
		ID: req.Notification.ID, At: time.Now(), Notification: &req.Notification,
	}}}
	var handover assistantwire.OutboxResult
	if err := json.Unmarshal(call(assistantwire.MethodOutbox, retry), &handover); err != nil {
		t.Fatal(err)
	}
	if len(handover.Done) != 1 || handover.Done[0] != req.Notification.ID {
		t.Fatalf("same-ID handover not acknowledged: %v", handover.Done)
	}
	reconnected := ringConnect(t, h, "phone", "ws_notify")
	replayed := ringDecoded(t, ringSent(reconnected))
	if len(replayed) != 1 || replayed[0]["title"] != title {
		t.Errorf("want one result after reconnect, got %v", replayed)
	}
}

// A device can have overlapping sockets during reconnect. Only an accepted copy
// suppresses replay, regardless of which socket the client map visits first.
func TestNotifyBackpressureWithOverlappingSockets(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		t.Run(fmt.Sprint("healthy=", healthy), func(t *testing.T) {
			h := ringHub(t)
			sockets := []*client{ringConnect(t, h, "phone", "ws_notify"), ringConnect(t, h, "phone", "ws_notify")}
			for i, c := range sockets {
				if healthy && i == 1 {
					continue
				}
				for range cap(c.out) {
					c.out <- []byte(`{"type":"pending"}`)
				}
			}
			payload := notifyPayload(notifyRequest{Title: "result", Level: levelActive}, "", "")
			reach := h.notifyWSFrom("", kindAlert, func(i18n.Lang) map[string]any { return payload }, nil, quietNone)
			wantKept := 1
			if healthy {
				wantKept = 0
			}
			if reach.selected != 2 || reach.reachable != 2 || reach.kept != wantKept {
				t.Fatalf("reach = %+v, want selected=2 reachable=2 kept=%d", reach, wantKept)
			}
			for i, c := range sockets {
				messages := ringDecoded(t, ringSent(c))
				if healthy && i == 1 && (len(messages) != 1 || messages[0]["title"] != "result") {
					t.Fatalf("healthy socket: %v", messages)
				}
				ringDisconnect(h, c)
			}
			reconnected := ringConnect(t, h, "phone", "ws_notify")
			if got := ringDecoded(t, ringSent(reconnected)); len(got) != wantKept {
				t.Fatalf("replayed %v, want %d", got, wantKept)
			}
			ringHello(t, h, reconnected, "ws_notify")
			if got := ringSent(reconnected); len(got) != 0 {
				t.Fatalf("replayed twice: %q", got)
			}
		})
	}
}

func TestNotifyBackpressurePreservesReplayEligibility(t *testing.T) {
	for _, mode := range []string{"muted", "push-route", "missing-capability", "unremembered"} {
		t.Run(mode, func(t *testing.T) {
			h := ringHub(t)
			c := ringConnect(t, h, "phone", "ws_notify")
			for range cap(c.out) {
				c.out <- []byte(`{"type":"pending"}`)
			}
			var required []string
			switch mode {
			case "muted":
				if err := h.push.setPrefs("phone", NotifyPrefs{MutedManagers: []string{"manager"}}); err != nil {
					t.Fatal(err)
				}
			case "push-route":
				h.push.mu.Lock()
				h.push.devices["registration"] = Device{DeviceID: "registration", CredentialID: "phone", APNSToken: "token", RelayURL: "https://relay.example", RelayGrant: "grant", PushKey: "key"}
				h.push.mu.Unlock()
			case "missing-capability":
				required = []string{"elicitation"}
			case "unremembered":
				h.mu.Lock()
				delete(h.rings.devices, "phone")
				h.mu.Unlock()
			}
			payload := notifyPayload(notifyRequest{Title: "result", Level: levelActive}, "", "")
			reach := h.notifyWSFrom("manager", kindAlert, func(i18n.Lang) map[string]any { return payload }, nil, quietNone, required...)
			if reach.kept != 0 {
				t.Fatalf("ineligible device kept event: %+v", reach)
			}
			ringDisconnect(h, c)
			if got := ringSent(ringConnect(t, h, "phone", "ws_notify")); len(got) != 0 {
				t.Fatalf("ineligible replay: %q", got)
			}
		})
	}
}
