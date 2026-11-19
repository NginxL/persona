package devices

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// continueNode is a node that answers start.continue with what the test sets and keeps the params it was asked with.
type continueNode struct {
	mu     sync.Mutex
	params []proto.StartContinueParams
	fail   error
}

// asked are the params start.continue was called with so far.
func (n *continueNode) asked() []proto.StartContinueParams {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.params)
}

func continueMachine(t *testing.T, h *Devices, id string, caps []string) *continueNode {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	n := &continueNode{}
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		switch method {
		case proto.MethodStartContinue:
			var p proto.StartContinueParams
			_ = json.Unmarshal(params, &p)
			n.params = append(n.params, p)
			if n.fail != nil {
				return nil, n.fail
			}
			return map[string]any{"session": proto.Session{SessionID: p.SessionID, Starting: true}}, nil
		case "node.configure":
			return map[string]any{}, nil
		}
		return nil, &rpc.Error{Message: "unknown method " + method}
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: id, Name: id})
	m.peer, m.online = hubSide, true
	m.info = proto.NodeInfo{OS: "darwin", Version: "test", Protocol: nodeproto.Version, Capabilities: caps}
	h.addEntry(m)
	return n
}

// The answer to a start's startup question (#10365) goes to the node's start.continue with the session and the
// choice, from a full device only; the session comes back starting, on its machine.
func TestSessionStartContinueRoute(t *testing.T) {
	h := newTestDevices(t)
	n := continueMachine(t, h, "mac", nodeproto.Announced)
	mux := h.Handler(http.NotFoundHandler())
	const path = "/api/v1/sessions/mac/0e000000-0000-4000-8000-0000000000aa/start/continue"

	code, out := clipCall(t, mux, "POST", path, `{"choice":"n"}`, "full")
	session, _ := out["session"].(map[string]any)
	if code != http.StatusOK || session["starting"] != true || session["machine_id"] != "mac" {
		t.Fatalf("%d %v", code, out)
	}
	if got := n.asked(); !slices.Equal(got, []proto.StartContinueParams{{SessionID: "0e000000-0000-4000-8000-0000000000aa", Choice: "n"}}) {
		t.Fatalf("node asked %+v", got)
	}
	for _, role := range []string{"answer", "extension", ""} {
		if code, out := clipCall(t, mux, "POST", path, `{"choice":"n"}`, role); code != http.StatusForbidden || out["code"] != proto.CodeReadOnly {
			t.Errorf("role %q: %d %v", role, code, out)
		}
	}
	for _, body := range []string{`{}`, `{"choice":"y\n"}`, `{"choice":"rm -rf"}`, `not json`} {
		if code, out := clipCall(t, mux, "POST", path, body, "full"); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}
	if got := n.asked(); len(got) != 1 {
		t.Errorf("refused requests reached the node: %+v", got)
	}
	// What the node refuses goes back coded, nothing typed.
	n.mu.Lock()
	n.fail = &rpc.Error{Code: "start_tab_gone", Message: "the tab is gone", Data: json.RawMessage(`{"delivered":"none"}`)}
	n.mu.Unlock()
	if code, out := clipCall(t, mux, "POST", path, `{"choice":"n"}`, "full"); code != http.StatusConflict || out["code"] != "start_tab_gone" || out["delivered"] != "none" {
		t.Errorf("a refusal: %d %v", code, out)
	}
}

// A node from before start.continue gets node_outdated (409), not the call.
func TestSessionStartContinueOnAnOldNode(t *testing.T) {
	h := newTestDevices(t)
	old := slices.DeleteFunc(slices.Clone(nodeproto.Announced), func(c string) bool { return c == nodeproto.StartContinue })
	n := continueMachine(t, h, "old", old)
	mux := h.Handler(http.NotFoundHandler())
	code, out := clipCall(t, mux, "POST", "/api/v1/sessions/old/0e000000-0000-4000-8000-0000000000aa/start/continue", `{"choice":"n"}`, "full")
	if code != http.StatusConflict || out["code"] != proto.CodeNodeOutdated {
		t.Fatalf("%d %v", code, out)
	}
	if got := n.asked(); len(got) != 0 {
		t.Errorf("an old node was asked: %+v", got)
	}
}
