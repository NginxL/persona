package node

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestPlanHookReportsSelectedMode(t *testing.T) {
	for _, tc := range []struct {
		name, explicit, then, want string
		approve, actor             bool
	}{
		{"default", "", "", "bypassPermissions", true, true},
		{"plan then", "", "acceptEdits", "acceptEdits", true, true},
		{"explicit", "default", "", "default", true, true},
		{"plan then takes precedence", "default", "acceptEdits", "acceptEdits", true, true},
		{"rejected", "", "", "", false, true},
		{"older manager", "", "", "bypassPermissions", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			n.state.put(decidedSession, sessionState{PlanThen: tc.then})
			fh.answer = func(proto.Interaction) proto.Resolution {
				r := proto.Resolution{Approve: &tc.approve, Mode: tc.explicit}
				if tc.actor {
					r.ModeActor = &proto.ModeActor{Kind: "device", DeviceID: "d1", DeviceName: "Phone"}
				}
				return r
			}
			out, _ := runPermissionHook(t, n, hookInput{HookEventName: "PermissionRequest", SessionID: decidedSession, PermissionMode: "plan", ToolName: "ExitPlanMode", ToolInput: json.RawMessage(`{"plan":"Inspect the project."}`)})
			raw, _ := json.Marshal(out)
			if tc.approve && !strings.Contains(string(raw), `"mode":"`+tc.want+`"`) {
				t.Fatalf("hook choice: %s", raw)
			}
			// Notify writes synchronously into the pipe; ask one request to establish the receiver has processed earlier events.
			var barrier any
			if err := n.peer.Call(t.Context(), "audit.barrier", map[string]any{}, &barrier); err != nil {
				t.Fatal(err)
			}
			fh.mu.Lock()
			events := append([]proto.ModeSetEvent(nil), fh.modeSets...)
			fh.mu.Unlock()
			if !tc.approve || !tc.actor {
				if len(events) != 0 {
					t.Fatalf("unexpected events: %+v", events)
				}
				return
			}
			if len(events) != 1 || events[0].To != tc.want || events[0].From != "plan" || events[0].SessionID != decidedSession || events[0].Actor.DeviceID != "d1" {
				t.Fatalf("audit: %+v", events)
			}
		})
	}
}
