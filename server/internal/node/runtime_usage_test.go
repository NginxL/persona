package node

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

func TestRuntimeUsageReport(t *testing.T) {
	n, _ := testNode(t)
	var out bytes.Buffer
	n.peer = rpc.New("test", &out)
	n.runtimes = newRuntimeRegistry()
	h := n.runtimes.add(nil)
	h.ready = true
	h.hello.Runtime = "toy"
	h.hello.Agent = "grok"
	n.runtimes.setSessions(h, []proto.Session{{SessionID: "s1", Agent: "grok"}, {SessionID: "s2", Agent: "grok"}})
	host := &RuntimeHost{reg: n.runtimes, node: n}
	report := func(id, key string) map[string]any {
		return map[string]any{"session_id": id, "request_id": key, "ts": time.Now().UTC().Format(time.RFC3339Nano), "model": "model", "input": 10, "output": 20, "cache_read": 30, "cache_write": 40, "reasoning": 7}
	}
	push := func(p map[string]any) { b, _ := json.Marshal(p); host.event(h, "usage.report", b) }
	push(report("s1", "r1"))
	push(report("s1", "r1"))
	push(report("s2", "r1"))
	n.flushRuntimeUsage()
	dec := json.NewDecoder(&out)
	for range 1 {
		var ev struct {
			Event  string
			Params struct {
				Add     bool
				Full    bool
				Buckets []proto.UsageBucket
			}
		}
		if err := dec.Decode(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.Event != "usage" || !ev.Params.Add || ev.Params.Full || len(ev.Params.Buckets) != 1 {
			t.Fatalf("event %+v", ev)
		}
		b := ev.Params.Buckets[0]
		if b.Agent != "grok" || b.Input != 20 || b.Output != 40 || b.CacheRead != 60 || b.CacheWrite != 80 || b.Requests != 2 {
			t.Fatalf("bucket %+v", b)
		}
	}
	if dec.More() {
		t.Fatal("duplicate counted")
	}
	out.Reset()
	for _, agent := range tokenuse.FileAgents() {
		h.hello.Agent = agent
		n.runtimes.setSessions(h, []proto.Session{{SessionID: "s1", Agent: agent}})
		push(report("s1", agent))
	}
	h.hello.Agent = "grok"
	n.runtimes.setSessions(h, []proto.Session{{SessionID: "s1", Agent: "unregistered"}})
	push(report("s1", "unregistered"))
	n.runtimes.setSessions(h, []proto.Session{{SessionID: "s1", Agent: "grok"}})
	push(report("s2", "removed"))
	push(report("someone-elses", "foreign"))
	for _, change := range []func(map[string]any){func(p map[string]any) { p["input"] = -1 }, func(p map[string]any) { p["input"] = 1.5 }, func(p map[string]any) { p["input"] = int64(1 << 53) }, func(p map[string]any) { p["reasoning"] = 21 }, func(p map[string]any) { p["ts"] = time.Now().Add(-27 * time.Hour).Format(time.RFC3339) }, func(p map[string]any) { p["ts"] = time.Now().Add(time.Hour).Format(time.RFC3339) }, func(p map[string]any) { p["model"] = "" }} {
		p := report("s1", "invalid")
		change(p)
		push(p)
	}
	h.ready = false
	push(report("s1", "before-hello"))
	h.ready = true
	n.flushRuntimeUsage()
	if out.Len() != 0 {
		t.Fatalf("inadmissible usage: %s", out.String())
	}
	push(report("s1", ""))
	push(report("s1", ""))
	n.flushRuntimeUsage()
	if bytes.Count(out.Bytes(), []byte(`"event":"usage"`)) != 1 || !bytes.Contains(out.Bytes(), []byte(`"req":2`)) {
		t.Fatalf("reports without request ids: %s", out.String())
	}
	out.Reset()
	reconnected := n.runtimes.add(nil)
	reconnected.ready = true
	reconnected.hello = h.hello
	n.runtimes.setSessions(reconnected, []proto.Session{{SessionID: "s1", Agent: "grok"}})
	body, _ := json.Marshal(report("s1", "r1"))
	host.event(reconnected, "usage.report", body)
	n.flushRuntimeUsage()
	if !bytes.Contains(out.Bytes(), []byte(`"req":1`)) {
		t.Fatalf("request ID was retired across runtime connections: %s", out.String())
	}
}

func TestDaemonUsageReportNamespace(t *testing.T) {
	var out bytes.Buffer
	l := daemonLink{cfg: daemonConfig{ID: "remote"}, rp: rpc.New("test", &out)}
	l.fromDaemonEvent("usage.report", json.RawMessage(`{"session_id":"s1","request_id":"r1","output":3}`))
	var event struct {
		Event  string
		Params struct {
			SessionID string `json:"session_id"`
			RequestID string `json:"request_id"`
			Output    int
		}
	}
	if err := json.Unmarshal(out.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Event != "usage.report" || event.Params.SessionID != "remote.s1" || event.Params.RequestID != "r1" || event.Params.Output != 3 {
		t.Fatalf("event %+v", event)
	}
	out.Reset()
	l.fromDaemonEvent("usage.report", json.RawMessage(`{"session_id":"invalid id"}`))
	if out.Len() != 0 {
		t.Fatal("invalid id forwarded")
	}
}

func TestRuntimeUsageFileAgentBoundary(t *testing.T) {
	n, _ := testNode(t)
	got, want := n.usageAgents(), tokenuse.FileAgents()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("file meter and runtime admission disagree: %v vs %v", got, want)
	}
}
