package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type resourceDevs struct {
	*fleetDevs
	machines map[string]*proto.Machine
	samples  map[string]*proto.MetricSample
	versions map[string]*bool
}

func (d *resourceDevs) MachineResources(id string) (*proto.Machine, *proto.MetricSample, *bool) {
	return d.machines[id], d.samples[id], d.versions[id]
}

func TestMachinesListFacts(t *testing.T) {
	f := newFakeHub()
	f.devices = []Device{
		{ID: "active", Name: "Active", Online: true, Reachable: true},
		{ID: "stale", Name: "Stale", Online: true, Reachable: true},
		{ID: "offline", Name: "Offline"},
		{ID: "pending", Name: "Pending", Online: true},
	}
	f.devicesFull = true
	f.devicesOther = []string{"excluded"}
	f.sessions["active"] = []proto.Session{{SessionID: "one", Status: proto.StatusWorking}, {SessionID: "archived", Status: proto.StatusWorking, Archived: true}, {SessionID: "idle", Status: proto.StatusIdle}}
	f.sessions["stale"] = nil
	cpu, load, outdated := 0.0, 1.5, false
	d := &resourceDevs{fleetDevs: newFleetDevs(), machines: map[string]*proto.Machine{
		"active": {System: &proto.System{Name: "Example OS", Cores: 8}},
		"stale":  {}, "offline": {Error: "connection timed out"}, "pending": {},
	}, samples: map[string]*proto.MetricSample{
		"active": {T: time.Now().Unix(), CPU: &cpu, Load: &load, MemTotal: 100, MemUsed: 40, SwapTotal: 50, SwapUsed: 10, DiskTotal: 200, DiskUsed: 150},
		"stale":  {T: time.Now().Add(-31 * time.Second).Unix(), CPU: &cpu, MemTotal: 100, MemUsed: 40},
	}, versions: map[string]*bool{"active": &outdated}}
	d.add("active", "Active")
	d.setSessions("active", f.sessions["active"])
	d.add("stale", "Stale")
	s := newTestService(t, f)
	s.devs = d
	s.health.loaded = true
	s.health.recs = map[string]*LauncherHealth{
		"active/broken": {Machine: "active", Launcher: "broken", State: healthFailing, CheckedAt: time.Now()},
		"active/old":    {Machine: "active", Launcher: "old", State: healthFailing, CheckedAt: time.Now().Add(-24 * time.Hour)},
	}
	text, err := s.runTool(context.Background(), "", "", "machines_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Machines []machineFacts `json:"machines"`
		Complete bool           `json:"complete"`
		Excluded int            `json:"excluded_count"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Complete || out.Excluded != 1 || len(out.Machines) != 4 {
		t.Fatalf("list: %s", text)
	}
	a, b, c, p := out.Machines[0], out.Machines[1], out.Machines[2], out.Machines[3]
	if a.State != "online" || a.OS.Value != "Example OS" || a.Cores.Value != float64(8) || a.CPUBusyPercent.Value != float64(0) || a.MemoryAvailableBytes.Value != float64(60) || a.DiskFreeBytes.Value != float64(50) || a.WorkingAgents.Value != float64(1) || a.NodeOutdated.Value != false {
		t.Fatalf("active: %s", text)
	}
	failed, ok := a.FailingLaunchers.Value.([]any)
	if !ok || len(failed) != 1 {
		t.Fatalf("launcher failures: %s", text)
	}
	if b.CPUBusyPercent.Value != nil || b.CPUBusyPercent.Reason != "sample_stale" || b.SampleAgeSeconds.Value == nil {
		t.Fatalf("stale: %s", text)
	}
	if c.State != "offline" || c.WorkingAgents.Value != nil || c.NodeOutdated.Value != nil {
		t.Fatalf("offline: %s", text)
	}
	reason, ok := c.StateReason.Value.(map[string]any)
	if !ok || reason["category"] != "timeout" {
		t.Fatalf("offline reason: %s", text)
	}
	if p.State != "pending" || p.StateReason.Reason != "connection_pending" {
		t.Fatalf("pending: %s", text)
	}
}

func TestMachinesListReconnectAndOSFallback(t *testing.T) {
	f := newFakeHub()
	f.devices = []Device{{ID: "box", Online: true, Reachable: true}}
	d := &resourceDevs{fleetDevs: newFleetDevs(), machines: map[string]*proto.Machine{"box": {OS: "linux", System: &proto.System{}}}, samples: map[string]*proto.MetricSample{"box": {T: time.Now().Add(-10 * time.Second).Unix(), MemTotal: 100, MemUsed: 40}}}
	node := d.add("box", "Box")
	node.connected = time.Now().Add(-2 * time.Second)
	s := newTestService(t, f)
	s.devs = d
	text, err := s.toolMachinesList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Machines []machineFacts `json:"machines"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	m := out.Machines[0]
	if m.OS.Value != "linux" || m.MemoryAvailableBytes.Value != nil || m.MemoryAvailableBytes.Reason != "sample_before_current_connection" || m.SampleAgeSeconds.Value == nil {
		t.Fatalf("reconnected machine: %s", text)
	}
}

func TestMachinesListAmbiguousAndMissing(t *testing.T) {
	f := newFakeHub()
	f.devices = []Device{{ID: "box", Online: true, Reachable: true}}
	f.sessions["box"] = nil
	f.sessionsFail["box"] = 1
	d := &resourceDevs{fleetDevs: newFleetDevs(), machines: map[string]*proto.Machine{"box": {}}, samples: map[string]*proto.MetricSample{"box": {T: time.Now().Unix(), MemTotal: 100, DiskTotal: 100}}}
	s := newTestService(t, f)
	s.devs = d
	text, err := s.toolMachinesList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Machines []machineFacts `json:"machines"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	m := out.Machines[0]
	if m.CPUBusyPercent.Reason != "metric_not_reported" || m.MemoryAvailableBytes.Reason != "memory_measurement_missing_or_ambiguous" || m.Swap.Reason != "swap_measurement_missing_or_ambiguous" || m.DiskFreeBytes.Reason != "disk_measurement_missing_or_ambiguous" || m.WorkingAgents.Reason != "sessions_unavailable" || m.NodeOutdated.Reason != "target_version_not_confirmed" {
		t.Fatalf("unexpected facts: %s", text)
	}
	if m.BatteryPercent != nil || m.BatteryCharging != nil || m.TempC != nil {
		t.Fatalf("expected nil battery/temp when absent: %s", text)
	}
}

func TestMachinesListBatteryAndTemp(t *testing.T) {
	f := newFakeHub()
	f.devices = []Device{{ID: "laptop", Online: true, Reachable: true}, {ID: "desktop", Online: true, Reachable: true}}
	batt, chg, temp := 88.0, true, 54.2
	d := &resourceDevs{
		fleetDevs: newFleetDevs(),
		machines:  map[string]*proto.Machine{"laptop": {}, "desktop": {}},
		samples: map[string]*proto.MetricSample{
			"laptop":  {T: time.Now().Unix(), MemTotal: 100, DiskTotal: 100, BatteryPercent: &batt, BatteryCharging: &chg, TempC: &temp},
			"desktop": {T: time.Now().Unix(), MemTotal: 100, DiskTotal: 100},
		},
	}
	s := newTestService(t, f)
	s.devs = d
	text, err := s.toolMachinesList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Machines []map[string]any `json:"machines"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Machines) != 2 {
		t.Fatalf("machines = %d", len(out.Machines))
	}
	laptop, desktop := out.Machines[0], out.Machines[1]
	if laptop["battery_percent"] == nil || laptop["battery_charging"] == nil || laptop["temp_c"] == nil {
		t.Fatalf("laptop missing battery/temp: %v", laptop)
	}
	if _, ok := desktop["battery_percent"]; ok {
		t.Fatalf("desktop must omit battery_percent: %v", desktop)
	}
	if _, ok := desktop["battery_charging"]; ok {
		t.Fatalf("desktop must omit battery_charging: %v", desktop)
	}
	if _, ok := desktop["temp_c"]; ok {
		t.Fatalf("desktop must omit temp_c: %v", desktop)
	}
}

func TestMachinesListSilentNodeIsNotOnline(t *testing.T) {
	f := newFakeHub()
	f.devices = []Device{
		{ID: "mini", Name: "Mini", Online: true, Reachable: true},
		{ID: "fresh", Name: "Fresh", Online: true, Reachable: true},
		{ID: "reconnected", Name: "Reconnected", Online: true, Reachable: true},
	}
	f.devicesFull = true
	d := &resourceDevs{fleetDevs: newFleetDevs(), machines: map[string]*proto.Machine{"mini": {}, "fresh": {}, "reconnected": {}},
		samples: map[string]*proto.MetricSample{
			"mini":        {T: time.Now().Add(-27 * time.Minute).Unix()},
			"fresh":       {T: time.Now().Add(-20 * time.Second).Unix()},
			"reconnected": {T: time.Now().Add(-27 * time.Minute).Unix()},
		}, versions: map[string]*bool{}}
	for _, id := range []string{"mini", "fresh", "reconnected"} {
		d.add(id, id)
	}
	d.mu.Lock()
	d.fleetDevs.machines["reconnected"].connected = time.Now().Add(-10 * time.Second)
	d.mu.Unlock()
	s := newTestService(t, f)
	s.devs = d
	s.health.loaded = true
	text, err := s.runTool(context.Background(), "", "", "machines_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Machines []machineFacts `json:"machines"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	mini, fresh, rec := out.Machines[0], out.Machines[1], out.Machines[2]
	reason, ok := mini.StateReason.Value.(map[string]any)
	if mini.State != "unresponsive" || !ok || reason["category"] != "no_recent_report" || reason["last_report_age_seconds"].(float64) < 1500 {
		t.Fatalf("silent node reported as %q: %s", mini.State, text)
	}
	if fresh.State != "online" || rec.State != "online" {
		t.Fatalf("fresh/reconnected must stay online: %s", text)
	}
	agents := s.describeAgents(context.Background(), "all", "", "")
	if !strings.Contains(agents, "unresponsive machines") || !strings.Contains(agents, "Mini") || strings.Contains(agents, "Fresh (") {
		t.Fatalf("agents_list: %s", agents)
	}
}
