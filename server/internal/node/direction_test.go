package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// The local CLI's typed request crosses the real Unix-socket handler and reaches the manager under the same kind and
// input. Neither the socket handler nor the typed client invents a conversation id or retries the write.
func TestDirectionWritesCrossTheNodeSocket(t *testing.T) {
	h := newScriptHub(t, answerWith(proto.Resolution{}), notifyOK)
	h.direction = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		return map[string]any{"status": "ok", "leader": map[string]any{"id": "dir_socket", "name": method}}, nil
	}
	created, err := nodeclient.CreateDirectionNode(context.Background(), h.dial, nodeclient.DirectionCreateRequest{
		Name: "Release", Project: "release", MachineID: "box", SessionID: "agent-1",
	})
	if err != nil || created.Status != nodeclient.DirectionOK || !json.Valid(created.Leader) {
		t.Fatalf("create: %+v %v", created, err)
	}
	updated, err := nodeclient.WriteDirectionProgressNode(context.Background(), h.dial, nodeclient.DirectionProgressRequest{
		ID: "dir_socket", Doing: "Checking", Links: []string{"https://example.com/evidence"},
	})
	if err != nil || updated.Status != nodeclient.DirectionOK {
		t.Fatalf("progress: %+v %v", updated, err)
	}
	h.mu.Lock()
	calls := append([]scriptDirectionCall(nil), h.directionCalls...)
	h.mu.Unlock()
	if len(calls) != 2 || calls[0].method != nodeclient.DirectionCreateKind || calls[1].method != nodeclient.DirectionProgressKind {
		t.Fatalf("forwarded calls: %+v", calls)
	}
	var createInput, progressInput map[string]any
	if json.Unmarshal(calls[0].params, &createInput) != nil || createInput["name"] != "Release" || createInput["machine_id"] != "box" || createInput["session_id"] != "agent-1" {
		t.Fatalf("forwarded create input: %s", calls[0].params)
	}
	if json.Unmarshal(calls[1].params, &progressInput) != nil || progressInput["id"] != "dir_socket" || progressInput["doing"] != "Checking" {
		t.Fatalf("forwarded progress input: %s", calls[1].params)
	}
}

func TestDirectionSocketReportsOldNodeAndManagerFailure(t *testing.T) {
	old := newScriptHub(t, answerWith(proto.Resolution{}), notifyOK)
	rep, err := nodeclient.CreateDirectionNode(context.Background(), old.dial, nodeclient.DirectionCreateRequest{Name: "Release", Project: "release"})
	if err != nil || rep.Status != nodeclient.DirectionUnavailable || rep.Error != "unknown kind" {
		t.Fatalf("old manager reply: %+v %v", rep, err)
	}

	failed := newScriptHub(t, answerWith(proto.Resolution{}), notifyOK)
	failed.direction = func(context.Context, string, json.RawMessage) (any, error) { return nil, errors.New("manager offline") }
	rep, err = nodeclient.WriteDirectionProgressNode(context.Background(), failed.dial, nodeclient.DirectionProgressRequest{ID: "dir_1", Doing: "Checking"})
	if err != nil || rep.Status != nodeclient.DirectionUnavailable || !strings.Contains(rep.Error, "manager offline") {
		t.Fatalf("manager failure reply: %+v %v", rep, err)
	}
}
