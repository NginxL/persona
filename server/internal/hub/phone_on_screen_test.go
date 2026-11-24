package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The clipboard tools work only with Persona on the phone's screen (#4192): with no phone connected that offers them,
// the hub answers "open Persona" at once and sends no silent push, which would only wake Persona in the background
// for a call it has to refuse. Other tools still wake the phone.
func TestPhoneOnScreenToolsAnswerWithoutWaking(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, pushes, _ := phoneWakeTestHub(t, true)
		h.setPhoneTools(db.PhoneToolRegistration{DeviceID: "phone", Platform: "ios", Tools: []proto.Tool{
			{Name: "phone_clipboard_get"}, {Name: "phone_clipboard_set"}, {Name: "phone_health_request_access"},
			{Name: "phone_device_info"},
		}})
		for _, call := range []struct{ name, args string }{
			{"phone_clipboard_get", `{}`},
			{"phone_clipboard_set", `{"text":"hello"}`},
			{"phone_health_request_access", `{"types":["heart_rate"]}`},
		} {
			start := time.Now()
			got := h.callPhone(context.Background(), call.name, json.RawMessage(call.args))
			if !got.IsError || !strings.Contains(textOf(got), "open Persona") {
				t.Fatalf("%s = %+v", call.name, got)
			}
			if waited := time.Since(start); waited != 0 {
				t.Fatalf("%s waited %v for the phone", call.name, waited)
			}
		}
		synctest.Wait()
		if got := pushes.count("phone", "background", ""); got != 0 || len(h.wokeAt) != 0 {
			t.Fatalf("a clipboard call woke the phone: pushes=%d, attempts=%v", got, h.wokeAt)
		}

		// The same setup does wake the phone for a tool that works in the background.
		if got := h.callPhone(context.Background(), "phone_device_info", json.RawMessage(`{}`)); !got.IsError ||
			!strings.Contains(textOf(got), "did not wake up") {
			t.Fatalf("phone_device_info = %+v", got)
		}
		synctest.Wait()
		if got := pushes.count("phone", "background", ""); got != 1 {
			t.Fatalf("phone_device_info sent %d wake pushes, want 1", got)
		}
	})
}

// A phone connected while Persona is in the background (left open by an earlier wake) still gets a clipboard call:
// it tells by itself whether Persona is on screen, and its refusal reaches the agent as it is.
func TestPhoneOnScreenToolsReachAConnectedPhone(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone := &client{hub: h, device: "credential-ios", role: roleFull, out: make(chan []byte, 8)}
	addTestClient(h, phone)
	h.onClientMessage(ctx, phone, []byte(`{"type":"register_phone","tools":[{"name":"phone_clipboard_set"}]}`))

	done := make(chan proto.ToolResult, 1)
	go func() { done <- h.callPhone(ctx, "phone_clipboard_set", json.RawMessage(`{"text":"hello"}`)) }()
	var call struct {
		Name   string `json:"name"`
		CallID string `json:"call_id"`
	}
	select {
	case data := <-phone.out:
		if err := json.Unmarshal(data, &call); err != nil || call.Name != "phone_clipboard_set" {
			t.Fatalf("sent %s (%v)", data, err)
		}
	case <-ctx.Done():
		t.Fatal("the connected phone did not get the call")
	}
	const refusal = "Persona is not open on the iPhone, so it cannot use the clipboard."
	reply, _ := json.Marshal(map[string]any{"type": "tool_result", "call_id": call.CallID, "is_error": true,
		"content": []proto.Content{{Type: "text", Text: refusal}}})
	h.onClientMessage(ctx, phone, reply)
	select {
	case r := <-done:
		if !r.IsError || textOf(r) != refusal {
			t.Fatalf("result = %+v", r)
		}
	case <-ctx.Done():
		t.Fatal("the phone's answer did not reach the agent")
	}
}
