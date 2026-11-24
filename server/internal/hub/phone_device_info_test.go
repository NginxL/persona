package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// phone_device_info is declared by the app (apple/Persona/PhoneTools/DeviceTool.swift) and answered by it: the hub
// lists the description and schema the phone registered and relays the phone's JSON as it came (#12926). A new app adds
// "build" and "runtime" to the result; an old app has neither, and the hub must neither require nor invent them.

const deviceInfoDescription = `Describe the phone: model identifier and name, iOS version. Also "build" (commit, dirty, branch, built_at, builder, xcode, swift, configuration, bundle_id) and "runtime" (hub_host, hub_version, hub_connection, launched_at); any field may be missing.`

func registerDeviceInfo(t *testing.T, h *Hub, ctx context.Context) *client {
	t.Helper()
	phone := &client{hub: h, device: "credential-ios", role: roleFull, out: make(chan []byte, 4)}
	addTestClient(h, phone)
	tool := proto.Tool{Name: "phone_device_info", Description: deviceInfoDescription,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
	msg, err := json.Marshal(map[string]any{"type": "register_phone", "tools": []proto.Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	h.onClientMessage(ctx, phone, msg)
	return phone
}

func TestPhoneDeviceInfoIsListedAsThePhoneDeclaredIt(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registerDeviceInfo(t, h, ctx)
	for _, tool := range h.mcpTools(false) {
		if tool.Name != "phone_device_info" {
			continue
		}
		if tool.Description != deviceInfoDescription {
			t.Fatalf("description = %q", tool.Description)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil || schema["type"] != "object" || len(schema) != 2 {
			t.Fatalf("schema = %s (%v)", tool.InputSchema, err)
		}
		return
	}
	t.Fatal("phone_device_info is not listed")
}

func TestPhoneDeviceInfoResultsPassThroughWithOrWithoutBuildAndRuntime(t *testing.T) {
	for name, body := range map[string]string{
		"an old app without them": `{"model":"iPhone17,1","ios_version":"26.0","app_version":"1.0.0","app_build":"1","app_channel":"release"}`,
		"a new app with them": `{"model":"iPhone17,1","app_channel":"debug",` +
			`"build":{"commit":"3518b00185","dirty":true,"built_at":"2026-11-22T07:00:00Z"},` +
			`"runtime":{"hub_host":"hub.example.com:8443","hub_connection":"connected"}}`,
		"a new app that knows only some fields": `{"model":"iPhone17,1","build":{},"runtime":{"launched_at":"2026-11-22T07:00:00Z"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newTestHub(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			phone := registerDeviceInfo(t, h, ctx)
			done := make(chan proto.ToolResult, 1)
			go func() { done <- h.callPhoneWithWait(ctx, "phone_device_info", json.RawMessage(`{}`), nil) }()
			var call struct {
				CallID string `json:"call_id"`
			}
			select {
			case data := <-phone.out:
				if err := json.Unmarshal(data, &call); err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("the call was not sent to the phone")
			}
			reply, _ := json.Marshal(map[string]any{"type": "tool_result", "call_id": call.CallID,
				"content": []proto.Content{{Type: "text", Text: body}}})
			h.onClientMessage(ctx, phone, reply)
			select {
			case result := <-done:
				if result.IsError || textOf(result) != body {
					t.Fatalf("result = %+v, want the phone's JSON unchanged", result)
				}
			case <-ctx.Done():
				t.Fatal("the result did not come back")
			}
		})
	}
}
