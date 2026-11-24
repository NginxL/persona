package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
)

// Keep the placement policy goldens independent of random IDs, wall time and locale.
// Candidate order, filter keys, sort inputs and the winning target are observable decisions.
func checkPlacementGolden(t *testing.T, p *placement, want string) {
	t.Helper()
	if p == nil {
		t.Fatal("missing placement")
	}
	var got strings.Builder
	fmt.Fprintf(&got, "v%d %s %s -> %s (%s)", p.PolicyVersion, p.Mode, p.Requested, p.Choice, p.ReasonKey)
	for _, c := range p.Candidates {
		fmt.Fprintf(&got, " | %s", c.Machine)
		for _, h := range c.Hits {
			fmt.Fprintf(&got, " %s", h.Key)
		}
		if c.LoadPerCore != nil {
			fmt.Fprintf(&got, " load=%g free=%d", *c.LoadPerCore, *c.FreeBytes)
		}
		if c.HealthRank != nil {
			fmt.Fprintf(&got, " health=%d", *c.HealthRank)
		}
	}
	if got.String() != want {
		t.Fatalf("placement:\n got %s\nwant %s", got.String(), want)
	}
}

// The test store keeps placement rows under their IDs. A reconciliation must
// finish the row written at bind, so a second row is also a test failure.
func onlyStoredPlacement(t *testing.T, store settingsPrefixStore) placement {
	t.Helper()
	records, err := store.SettingsPrefix(context.Background(), "placement.")
	if err != nil || len(records) != 1 {
		t.Fatalf("got %d placement records, %v", len(records), err)
	}
	var p placement
	for key, raw := range records {
		if err := json.Unmarshal(raw, &p); err != nil || key != "placement."+p.ID {
			t.Fatalf("invalid placement %s: %s, %v", key, raw, err)
		}
	}
	return p
}

func TestPlacementFixedAndFlexible(t *testing.T) {
	for _, policy := range []string{machineFixed, machineFlexible} {
		t.Run(policy, func(t *testing.T) {
			s, f, d := admissionHub(t)
			healthyIdle(d)
			busyOverloaded(d)
			task := admissionPlanTask("busy", policy)
			machine, _, err := s.startTaskAgent(context.Background(), task, false)
			if policy == machineFixed {
				if !isAdmissionDenied(err) || len(taskStartMachines(f)) != 0 || task.Placement.Recommendation != "idle" {
					t.Fatalf("fixed start: %v", err)
				}
				checkPlacementGolden(t, task.Placement, "v1 enforce busy ->  (assistant-tasks.admission_load) | busy assistant-tasks.admission_load | idle load=0.0625 free=10737418240")
				return
			}
			if err != nil || machine != "idle" {
				t.Fatalf("flexible start on %s: %v", machine, err)
			}
			defer task.admissionRelease()
			checkPlacementGolden(t, task.Placement, "v1 enforce busy -> idle (lowest_load) | busy assistant-tasks.admission_load | idle load=0.0625 free=10737418240")
		})
	}
}

type placementBusDevices struct {
	*resourceDevs
	got devicewire.NodeCall
}

func (d *placementBusDevices) Request(_ context.Context, method string, raw json.RawMessage) (any, bool, error) {
	if method != devicewire.MethodCall {
		return nil, false, nil
	}
	err := json.Unmarshal(raw, &d.got)
	return json.RawMessage(`{"session":{"session_id":"agent"}}`), true, err
}

func TestPlacementReportsClientStartsAndResumes(t *testing.T) {
	for _, tc := range []struct{ name, method, params string }{
		{"new", DeviceSessionStart, `{"cwd":"/work","launcher":"work"}`},
		{"resume", DeviceSessionStart, `{"resume_session_id":"agent","launcher":"work"}`},
		{"work", devicewire.MethodStartWorkSession, `{"launcher":"work","cwd":"/work"}`},
		{"backend", devicewire.MethodStartBackendSession, `{"launcher":"work","backend_workspace":{"use_id":"launch"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, d := admissionHub(t)
			busyOverloaded(d)
			store := &memStore{}
			s.store = func() Store { return store }
			devices := &placementBusDevices{resourceDevs: d}
			b := NewBusHub(BusOptions{Devices: devices})
			b.svc = s
			close(b.helloed)
			params := json.RawMessage(tc.params)
			request, _ := json.Marshal(devicewire.NodeCall{MachineID: "busy", Method: tc.method, Params: params})
			_, err := b.request(context.Background(), devicewire.MethodCall, request)
			if err != nil || devices.got.MachineID != "busy" || string(devices.got.Params) != string(params) || s.admission.machineClaims["busy"] != 0 {
				t.Fatalf("bind: %+v, %v", devices.got, err)
			}
			records, err := store.SettingsPrefix(context.Background(), "placement.")
			if err != nil || len(records) != 1 {
				t.Fatalf("got %d records, %v", len(records), err)
			}
			for _, raw := range records {
				var p placement
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Fatal(err)
				}
				if p.SessionID != "agent" {
					t.Fatalf("session = %q", p.SessionID)
				}
				checkPlacementGolden(t, &p, "v1 report busy -> busy (requested_machine) | busy assistant-tasks.admission_load")
			}
		})
	}
}
