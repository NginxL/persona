package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestPlanAnswerUsesTrustedModeActor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		trusted  *devicewire.Actor
		answered string
		want     string
	}{
		{"device", &devicewire.Actor{Kind: "device", DeviceID: "d1", DeviceName: "Phone"}, "", "device"},
		{"assistant", nil, proto.AnsweredByAssistant, "assistant"},
		{"older hub", nil, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			s := ft.s
			ix := proto.Interaction{ID: "ix_mode", MachineID: "mac", SessionID: "s1", Kind: proto.IxPlan}
			done := make(chan proto.Resolution, 1)
			go func() {
				r, err := s.waitInteraction(context.Background(), ix)
				if err != nil {
					t.Errorf("wait: %v", err)
				}
				done <- r
			}()
			ft.waitPending(1)
			ctx := context.Background()
			if tc.trusted != nil {
				ctx = devicewire.WithModeActor(ctx, tc.trusted)
			}
			yes := true
			err := s.resolveApproved(ctx, ix.ID, proto.Resolution{Approve: &yes, AnsweredBy: tc.answered, ModeActor: &proto.ModeActor{Kind: "device", DeviceID: "forged"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			r := <-done
			if tc.want == "" {
				if r.ModeActor != nil {
					t.Fatalf("forged provenance survived: %+v", r.ModeActor)
				}
				return
			}
			if r.ModeActor == nil || r.ModeActor.Kind != tc.want || r.ModeActor.DeviceID == "forged" {
				t.Fatalf("actor: %+v", r.ModeActor)
			}
		})
	}
}

type modeActorDevices struct {
	fakeDevices
	actors []*devicewire.Actor
}

func (d *modeActorDevices) CallNode(ctx context.Context, id, method string, params json.RawMessage) (json.RawMessage, error) {
	d.actors = append(d.actors, devicewire.ModeActorOf(ctx))
	return json.RawMessage(`{}`), nil
}

func TestAssistantLocalCallsCarryModeActor(t *testing.T) {
	d := &modeActorDevices{}
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "fixture", Devices: d})
	if _, err := b.DeviceCall(context.Background(), DeviceCall{Device: "box", Method: DeviceSessionModes, Params: json.RawMessage(`{"session_id":"s1","permission_mode":"plan"}`)}); err != nil {
		t.Fatal(err)
	}
	s := newFleetTestNoStore(t).s
	s.devs = d
	if err := s.nodeCall(context.Background(), "box", DeviceSessionModes, map[string]any{"session_id": "s1", "permission_mode": "plan"}, nil); err != nil {
		t.Fatal(err)
	}
	phone := &devicewire.Actor{Kind: "device", DeviceID: "d1", DeviceName: "Phone"}
	if err := s.nodeCall(devicewire.WithModeActor(context.Background(), phone), "box", DeviceSessionModes, map[string]any{"session_id": "s1", "permission_mode": "plan"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(d.actors) != 3 || d.actors[0] == nil || d.actors[0].Kind != "assistant" || d.actors[1] == nil || d.actors[1].Kind != "assistant" || d.actors[2] != phone {
		t.Fatalf("actors: %+v", d.actors)
	}
}
