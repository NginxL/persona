package node

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestRuntimeUsageWindows(t *testing.T) {
	n, _ := testNode(t)
	n.runtimes = newRuntimeRegistry()
	h := n.runtimes.add(nil)
	h.ready = true
	h.hello.Runtime = "agy"
	host := &RuntimeHost{reg: n.runtimes, node: n}
	now := time.Now().UTC().Truncate(time.Second)
	account := func(used float64) proto.UsageAccount {
		return proto.UsageAccount{
			Provider: "agy", Name: "Antigravity", SampledAt: now.Add(-time.Minute),
			Windows: []proto.UsageWindow{
				{Label: "7d", WindowMinutes: 10080, UsedPercent: 75},
				{Label: "5h", WindowMinutes: 300, UsedPercent: used, ResetsAt: timePointer(now.Add(5 * time.Hour))},
			},
		}
	}
	push := func(value any) {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		host.event(h, "usage.windows", body)
	}
	push(account(125))
	snapshot := n.usageSnapshot()
	if !snapshot.Enabled || len(snapshot.Accounts) != 1 {
		t.Fatalf("runtime account missing: %+v", snapshot)
	}
	got := snapshot.Accounts[0]
	if got.Provider != "agy" || got.Name != "Antigravity" || got.SourceKey != "runtime:agy" || len(got.Windows) != 2 {
		t.Fatalf("runtime account: %+v", got)
	}
	if got.Windows[0].Label != "5h" || got.Windows[0].UsedPercent != 100 || got.Windows[1].Label != "7d" {
		t.Errorf("normalized windows: %+v", got.Windows)
	}

	reconnected := n.runtimes.add(nil)
	reconnected.ready = true
	reconnected.hello.Runtime = "agy"
	body, _ := json.Marshal(account(40))
	host.event(reconnected, "usage.windows", body)
	if snapshot = n.usageSnapshot(); len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Windows[0].UsedPercent != 40 {
		t.Fatalf("reconnect duplicated or did not replace the source: %+v", snapshot.Accounts)
	}

	daemon := n.runtimes.add(nil)
	daemon.ready = true
	daemon.daemon = "remote"
	daemon.hello.Runtime = "agy"
	body, _ = json.Marshal(account(90))
	host.event(daemon, "usage.windows", body)
	if snapshot = n.usageSnapshot(); len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Windows[0].UsedPercent != 40 {
		t.Fatalf("daemon report replaced the local account: %+v", snapshot.Accounts)
	}
	body, _ = json.Marshal(proto.UsageAccount{Provider: "agy", Name: "Antigravity", SampledAt: now, Windows: []proto.UsageWindow{}})
	host.event(daemon, "usage.windows", body)
	if snapshot = n.usageSnapshot(); len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Windows[0].UsedPercent != 40 {
		t.Fatalf("daemon empty report cleared the local account: %+v", snapshot.Accounts)
	}

	for _, invalid := range []proto.UsageAccount{
		{Provider: "", Name: "Antigravity", SampledAt: now, Windows: account(90).Windows},
		{Provider: "agy", Name: "Antigravity", SampledAt: now.Add(-4 * 24 * time.Hour), Windows: account(90).Windows},
		{Provider: "agy", Name: "Antigravity", SampledAt: now.Add(time.Hour), Windows: account(90).Windows},
		{Provider: "agy", Name: "Antigravity", SampledAt: now, Windows: []proto.UsageWindow{{Label: "5h", UsedPercent: 90}}},
		{Provider: "agy", Name: "Antigravity", SampledAt: now, Windows: nil},
	} {
		push(invalid)
		if snapshot = n.usageSnapshot(); len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Windows[0].UsedPercent != 40 {
			t.Fatalf("invalid report replaced the good sample: %+v", snapshot.Accounts)
		}
	}
	host.event(h, "usage.windows", json.RawMessage(`{"provider":`))
	if snapshot = n.usageSnapshot(); len(snapshot.Accounts) != 1 {
		t.Fatalf("malformed JSON cleared the good sample: %+v", snapshot.Accounts)
	}
	missing, _ := json.Marshal(map[string]any{"provider": "agy", "name": "Antigravity", "sampled_at": now.Format(time.RFC3339)})
	host.event(h, "usage.windows", missing)
	if snapshot = n.usageSnapshot(); len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Windows[0].UsedPercent != 40 {
		t.Fatalf("missing windows cleared the good sample: %+v", snapshot.Accounts)
	}
	for _, window := range []map[string]any{
		{"window_minutes": 300},
		{"window_minutes": 300, "used_percent": nil},
	} {
		malformed, _ := json.Marshal(map[string]any{
			"provider": "agy", "name": "Antigravity", "sampled_at": now.Format(time.RFC3339),
			"windows": []any{window},
		})
		host.event(h, "usage.windows", malformed)
		if snapshot = n.usageSnapshot(); len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Windows[0].UsedPercent != 40 {
			t.Fatalf("missing or null used_percent replaced the good sample: %+v", snapshot.Accounts)
		}
	}

	push(proto.UsageAccount{Provider: "agy", Name: "Antigravity", SampledAt: now, Windows: []proto.UsageWindow{}})
	if snapshot = n.usageSnapshot(); len(snapshot.Accounts) != 0 {
		t.Fatalf("empty snapshot did not clear the source: %+v", snapshot.Accounts)
	}

	n.setUsageWindows(false)
	push(account(10))
	if snapshot = n.usageSnapshot(); snapshot.Enabled || len(snapshot.Accounts) != 0 {
		t.Fatalf("disabled collection accepted a runtime report: %+v", snapshot)
	}
}

func timePointer(value time.Time) *time.Time { return &value }
