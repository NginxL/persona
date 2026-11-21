package hub

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// notify.request's answer for an assistant's push says where it went (#8138): no device at all, the devices it was
// handed to, an offline device that keeps it, or devices that all turned it off. A card's push reports nothing.
func TestNotifyRequestSaysWhereThePushWent(t *testing.T) {
	h := ringHub(t)
	answer := func(n assistantwire.Notification) string {
		t.Helper()
		out, err := h.deliverPushFrom(assistantwire.AssistantID, n)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(notifyAnswer(out))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	push := func() assistantwire.Notification { return gradingNotification("", "", "Report ready") }

	if got, want := answer(push()), `{"devices":0,"ok":true,"reason":"no_device"}`; got != want {
		t.Fatalf("no device: %s, want %s", got, want)
	}

	phone := ringConnect(t, h, "phone", "ws_notify")
	if got, want := answer(push()), `{"devices":1,"ok":true}`; got != want {
		t.Fatalf("a connected phone: %s, want %s", got, want)
	}
	if sent := ringSent(phone); len(sent) != 1 {
		t.Fatalf("the phone got %q", sent)
	}

	// Offline now: it keeps the notice for its next connection, and nothing took it yet.
	ringDisconnect(h, phone)
	if got, want := answer(push()), `{"devices":0,"kept":1,"ok":true,"reason":"no_device"}`; got != want {
		t.Fatalf("an offline phone: %s, want %s", got, want)
	}

	// Connected but it turned the assistant's pushes off: it could take it and chose not to.
	if err := h.push.setPrefs("tablet", NotifyPrefs{Input: true, MutedManagers: []string{assistantwire.AssistantID}}); err != nil {
		t.Fatal(err)
	}
	ringConnect(t, h, "tablet", "ws_notify")
	if got, want := answer(push()), `{"devices":0,"kept":1,"ok":true,"reason":"filtered"}`; got != want {
		t.Fatalf("a device that muted the assistant: %s, want %s", got, want)
	}

	// What does not report its delivery answers as before.
	if got, want := string(must(json.Marshal(notifyAnswer(nil)))), `{"ok":true}`; got != want {
		t.Fatalf("not reported: %s, want %s", got, want)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
