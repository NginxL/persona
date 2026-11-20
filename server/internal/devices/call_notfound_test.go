package devices

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A node's not_found for a session's history goes back to the hub with its data as the node gave it: the reason the
// hub words for the apps (#11388) is not lost in the manager.
func TestCallKeepsANotFoundReason(t *testing.T) {
	h := newTestDevices(t)
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	const words = "transcript for session s1 not found"
	nodeSide.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "session.items" {
			return nil, &rpc.Error{Code: proto.CodeNotFound, Message: words, Data: json.RawMessage(`{"reason":"no_transcript"}`)}
		}
		return map[string]any{}, nil
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: "box", Name: "box"})
	m.peer, m.online = hubSide, true
	m.info = proto.NodeInfo{OS: "linux", Version: "test", Protocol: nodeproto.Version, Capabilities: nodeproto.Announced}
	h.addEntry(m)

	_, err := h.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: "session.items",
		Params: json.RawMessage(`{"session_id":"s1","turns":2}`)})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeNotFound || re.Message != words || string(re.Data) != `{"reason":"no_transcript"}` {
		t.Fatalf("the node's not_found as the hub gets it: %#v (%v)", re, err)
	}
}
