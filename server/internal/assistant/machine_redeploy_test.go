package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// machineRedeployDevices records the shared devices-manager operation the app route and the assistant tool must use.
// An unavailable machine is refused by that operation before a deployment starts, just as Machine.startDeploy refuses it.
type machineRedeployDevices struct {
	*fleetDevs
	mu          sync.Mutex
	calls       []string
	started     []string
	results     map[string]error
	unavailable map[string]error
}

func (d *machineRedeployDevices) Redeploy(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, id)
	if err := d.unavailable[id]; err != nil {
		return err
	}
	d.started = append(d.started, id)
	return d.results[id]
}

func (d *machineRedeployDevices) calledIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

func (d *machineRedeployDevices) startedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.started)
}

func newMachineRedeployService(t *testing.T, machines ...Device) (*Service, *machineRedeployDevices) {
	t.Helper()
	f := newFakeHub()
	f.devices = machines
	f.devicesFull = true
	d := &machineRedeployDevices{
		fleetDevs:   newFleetDevs(),
		results:     map[string]error{},
		unavailable: map[string]error{},
	}
	for _, machine := range machines {
		node := d.add(machine.ID, machine.Name)
		node.away = !machine.Online || !machine.Reachable
		if !machine.Online || !machine.Reachable {
			d.unavailable[machine.ID] = errors.New("machine is offline")
		}
	}
	s := newTestService(t, f)
	s.devs = d
	return s, d
}

func callMachineRedeploy(t *testing.T, s *Service, machine string) (string, error) {
	t.Helper()
	return runJSON(t, s, "supervising-conversation", "machine_redeploy", map[string]any{"machine": machine})
}

func requireMachineRedeploySelectionError(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("machine selection succeeded, want a refusal")
	}
	if strings.Contains(err.Error(), "unknown tool machine_redeploy") {
		t.Fatalf("machine_redeploy is not dispatched: %v", err)
	}
	lower := strings.ToLower(err.Error())
	for _, want := range wants {
		if strings.Contains(lower, want) {
			return
		}
	}
	t.Fatalf("selection error %q does not contain any of %q", err, wants)
}

func TestMachineRedeployToolRegistered(t *testing.T) {
	s := newTestService(t, newFakeHub())
	for _, tool := range s.Tools(context.Background()) {
		if tool.Name != "machine_redeploy" {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("machine_redeploy schema: %v", err)
		}
		if _, ok := schema.Properties["machine"]; !ok || !slices.Contains(schema.Required, "machine") {
			t.Fatalf("machine_redeploy must require one machine selector: %s", tool.InputSchema)
		}
		return
	}
	t.Fatal("machine_redeploy is not listed as an assistant MCP tool")
}

func TestMachineRedeployByIDAndExactName(t *testing.T) {
	for _, selector := range []string{"box-1", "Studio Mac"} {
		t.Run(selector, func(t *testing.T) {
			s, d := newMachineRedeployService(t, Device{ID: "box-1", Name: "Studio Mac", Online: true, Reachable: true})
			out, err := callMachineRedeploy(t, s, selector)
			if err != nil {
				t.Fatalf("redeploy %q: %v", selector, err)
			}
			if !strings.Contains(strings.ToLower(out), "success") || !strings.Contains(out, "box-1") || !strings.Contains(out, "Studio Mac") {
				t.Fatalf("the result does not read back a successful redeploy of the selected machine: %q", out)
			}
			if got := d.startedIDs(); !slices.Equal(got, []string{"box-1"}) {
				t.Fatalf("shared manager operation started deployments on %v, want only box-1", got)
			}
		})
	}
}

func TestMachineRedeployReturnsDeploymentFailureReason(t *testing.T) {
	s, d := newMachineRedeployService(t, Device{ID: "box-1", Name: "Studio Mac", Online: true, Reachable: true})
	const reason = "copy failed: no space left"
	d.results["box-1"] = errors.New(reason)
	out, err := callMachineRedeploy(t, s, "Studio Mac")
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("result %q, error %v: the deployment failure reason was not returned", out, err)
	}
	if got := d.startedIDs(); !slices.Equal(got, []string{"box-1"}) {
		t.Fatalf("shared manager operation started deployments on %v, want only box-1", got)
	}
}

func TestMachineRedeployRejectsSelectionWithoutDeploying(t *testing.T) {
	online := []Device{
		{ID: "box-1", Name: "Studio Mac", Online: true, Reachable: true},
		{ID: "box-2", Name: "Studio Mac", Online: true, Reachable: true},
	}
	for _, tc := range []struct {
		name     string
		selector string
		machines []Device
		wants    []string
	}{
		{name: "unknown", selector: "missing", machines: online[:1], wants: []string{"no machine", "unknown machine", "not found"}},
		{name: "ambiguous exact display name", selector: "Studio Mac", machines: online, wants: []string{"ambiguous"}},
		{name: "display name is not case folded", selector: "studio mac", machines: online[:1], wants: []string{"no machine", "unknown machine", "not found"}},
		{name: "unavailable", selector: "box-1", machines: []Device{{ID: "box-1", Name: "Studio Mac"}}, wants: []string{"offline", "unavailable"}},
		{name: "current assistant machine", selector: "server", machines: []Device{{ID: "server", Name: "Studio Mac", Online: true, Reachable: true}}, wants: []string{"another machine"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, d := newMachineRedeployService(t, tc.machines...)
			out, err := callMachineRedeploy(t, s, tc.selector)
			if out != "" {
				t.Fatalf("refused selection returned a success result: %q", out)
			}
			requireMachineRedeploySelectionError(t, err, tc.wants...)
			if got := d.startedIDs(); len(got) != 0 {
				t.Fatalf("a deployment started for %q: %v", tc.selector, got)
			}
			if tc.name != "unavailable" && len(d.calledIDs()) != 0 {
				t.Fatalf("an unresolved selection reached the deployment operation: %v", d.calledIDs())
			}
		})
	}
}

func TestMachineRedeployRequiresCompleteNameInventory(t *testing.T) {
	s, d := newMachineRedeployService(t, Device{ID: "box-1", Name: "Studio Mac", Online: true, Reachable: true})
	s.hub.(*fakeHub).devicesFull = false
	out, err := callMachineRedeploy(t, s, "Studio Mac")
	if out != "" || err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("result %q, error %v: an incomplete name inventory was not refused", out, err)
	}
	if got := d.calledIDs(); len(got) != 0 {
		t.Fatalf("an incomplete name inventory reached the deployment operation: %v", got)
	}
}

func TestMachineRedeployAllowsIDWithIncompleteInventory(t *testing.T) {
	s, d := newMachineRedeployService(t, Device{ID: "box-1", Name: "Studio Mac", Online: true, Reachable: true})
	s.hub.(*fakeHub).devicesFull = false
	if _, err := callMachineRedeploy(t, s, "box-1"); err != nil {
		t.Fatalf("redeploy by an unambiguous ID: %v", err)
	}
	if got := d.startedIDs(); !slices.Equal(got, []string{"box-1"}) {
		t.Fatalf("shared manager operation started deployments on %v, want only box-1", got)
	}
}
