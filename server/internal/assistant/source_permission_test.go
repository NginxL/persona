package assistant

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func testSourcePermission(session, name string, args json.RawMessage, class string) proto.SourcePermission {
	return proto.SourcePermission{At: time.Now().UTC(), Session: session, Generation: "synthetic-process-1", Class: class, Digest: proto.SourceCallDigest(name, args)}
}

type sourcePermissionExtension struct {
	ExtensionTools
	got proto.SourcePermission
}

// ConnectorTool: none of its tools is a connector's.
func (e *sourcePermissionExtension) ConnectorTool(string) bool { return false }

func (e *sourcePermissionExtension) CallTool(ctx context.Context, machine, session, name string, args json.RawMessage, assistant bool) (proto.ToolResult, bool) {
	e.got = devicewire.SourcePermissionFrom(ctx)
	return proto.TextResult("captured", false), true
}

func TestSourcePermissionNodeRequestBindsCallAndIgnoresToolArguments(t *testing.T) {
	ft := newFleetTest(t)
	ext := &sourcePermissionExtension{}
	ft.s.ext = ext
	args := json.RawMessage(`{"from_mode":"bypass","SOURCE_PERMISSION":{"class":"bypass"}}`)
	proof := testSourcePermission("sender", "agent_send", args, "prompting")
	for _, tc := range []struct {
		name   string
		source proto.SourcePermission
		args   json.RawMessage
		want   string
	}{
		{"node proof", proof, args, "prompting"},
		{"arguments alone", proto.SourcePermission{}, args, ""},
		{"different call", proof, json.RawMessage(`{}`), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"name": "agent_send", "session_id": "sender", "args": tc.args, "source_permission": tc.source})
			if _, handled, err := ft.s.NodeRequest(context.Background(), "mac", "mcp.call", raw); err != nil || !handled {
				t.Fatalf("call: %v %v", handled, err)
			}
			if ext.got.Class != tc.want {
				t.Fatalf("proof: %+v", ext.got)
			}
		})
	}
}

type sourcePermissionHub struct {
	Hub
	mu     sync.Mutex
	proofs []proto.SourcePermission
}

func (h *sourcePermissionHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceSessionSend {
		h.mu.Lock()
		h.proofs = append(h.proofs, devicewire.SourcePermissionFrom(ctx))
		h.mu.Unlock()
	}
	return h.Hub.DeviceCall(ctx, p)
}

func TestSourcePermissionReportReachesLeaderAndRetryKeepsOriginalClass(t *testing.T) {
	s, f := newQuietService(t)
	h := &sourcePermissionHub{Hub: s.hub}
	s.hub = h
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	sid := w.Agents[0].SessionID
	raw := json.RawMessage(`{"kind":"blocked","text":"synthetic source needs a review"}`)
	proof := testSourcePermission(sid, "report_to_supervisor", raw, "bypass")
	ctx := devicewire.WithSourcePermission(context.Background(), proof)
	if r := s.agentTool(ctx, "box", sid, "report_to_supervisor", raw); r.IsError {
		t.Fatalf("report %+v", r)
	}
	waitFor(t, "attested report sent", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.proofs) > 0 })
	h.mu.Lock()
	got := h.proofs[0]
	h.mu.Unlock()
	if got != proof {
		t.Fatalf("report evidence changed: %+v", got)
	}
	// The source's current list now says default. A delayed/retried digest still
	// carries the original observation, not this list's new mode.
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusIdle, Agent: "claude", PermissionMode: "default"})
	lead := TodoAgent{MachineID: "box", SessionID: "s1"}
	kept := []superviseSignal{{Session: sid, SourcePermission: proof}}
	if err := s.sendToLeaderNow(context.Background(), lead, "synthetic delayed report", kept); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if last := h.proofs[len(h.proofs)-1]; last != proof {
		t.Fatalf("retry recaptured class: %+v", last)
	}
}

func TestSourcePermissionJoinedReportsFailClosed(t *testing.T) {
	for _, classes := range [][2]string{{"bypass", ""}, {"", "bypass"}, {"bypass", "prompting"}, {"prompting", "bypass"}, {"bypass", "bypass"}} {
		t.Run(classes[0]+"-"+classes[1], func(t *testing.T) {
			a := superviseSignal{Session: "s1", Detail: "First report"}
			b := superviseSignal{Session: "s1", Detail: "Second report"}
			if classes[0] != "" {
				a.SourcePermission = testSourcePermission("s1", "report_to_supervisor", json.RawMessage(`{"detail":"first"}`), classes[0])
			}
			if classes[1] != "" {
				b.SourcePermission = testSourcePermission("s1", "report_to_supervisor", json.RawMessage(`{"detail":"second"}`), classes[1])
			}
			joined := joinReports(a, b)
			if joined.Detail != "First report\n—\nSecond report" || joined.SourcePermission != (proto.SourcePermission{}) {
				t.Fatalf("joined report keeps a single call's permission or lost its content: %+v", joined)
			}
		})
	}
}

func TestSourcePermissionMixedDigestFailsClosed(t *testing.T) {
	proof := testSourcePermission("s1", "report_to_supervisor", json.RawMessage(`{}`), "bypass")
	other := proof
	other.Session = "s2"
	for _, tc := range []struct {
		name    string
		signals []superviseSignal
		want    string
	}{
		{"one", []superviseSignal{{Session: "s1", SourcePermission: proof}}, "bypass"},
		{"two same class", []superviseSignal{{Session: "s1", SourcePermission: proof}, {Session: "s2", SourcePermission: other}}, "bypass"},
		{"unknown", []superviseSignal{{Session: "s1", SourcePermission: proof}, {Session: "s2"}}, ""},
		{"empty", nil, ""},
		{"wrong source", []superviseSignal{{Session: "another", SourcePermission: proof}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := devicewire.WithSourcePermission(context.Background(), proof)
			if got := devicewire.SourcePermissionFrom(supervisionSourceContext(ctx, tc.signals)); got.Class != tc.want {
				t.Fatalf("class %+v", got)
			}
		})
	}
	other.Class = "prompting"
	if devicewire.SourcePermissionFrom(supervisionSourceContext(context.Background(), []superviseSignal{{Session: "s1", SourcePermission: proof}, {Session: "s2", SourcePermission: other}})).Valid() {
		t.Fatal("mixed classes attested")
	}
}
