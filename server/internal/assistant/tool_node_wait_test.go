package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// silentNodeHub passes the methods in silent to a real RPC connection whose other end reads every request and never
// answers: a computer that still reads as connected while its node no longer responds (#13196).
type silentNodeHub struct {
	*fakeHub
	peer   *rpc.Peer
	silent map[string]bool
}

func (h *silentNodeHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if !h.silent[p.Method] {
		return h.fakeHub.DeviceCall(ctx, p)
	}
	var out json.RawMessage
	err := h.peer.Call(ctx, p.Method, p.Params, &out)
	return out, err
}

// connectedSilentNode is an open connection to a node that never answers: the caller's side of it.
func connectedSilentNode(t *testing.T) *rpc.Peer {
	t.Helper()
	hubEnd, nodeEnd := net.Pipe()
	peer := rpc.New("h", hubEnd)
	go func() { _ = peer.Serve(t.Context(), hubEnd) }()
	go func() { _, _ = io.Copy(io.Discard, nodeEnd) }() // takes every request in, answers none
	t.Cleanup(func() { _ = hubEnd.Close(); _ = nodeEnd.Close() })
	return peer
}

// agent_read, agent_send and agent_interrupt to a computer whose node stays connected but never answers come back
// within their bound with an error that names the computer and its last report, instead of holding the turn.
func TestToolNodeCallsToASilentNodeEnd(t *testing.T) {
	old := toolNodeWait
	toolNodeWait = map[string]time.Duration{DeviceSessionItems: 200 * time.Millisecond, DeviceSessionSend: 200 * time.Millisecond,
		"session.interrupt": 200 * time.Millisecond}
	t.Cleanup(func() { toolNodeWait = old })

	s, f := newQuietService(t)
	const sid = "ebcd53b3aa"
	f.setSessions("mini", proto.Session{SessionID: sid, MachineID: "mini", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	h := &silentNodeHub{fakeHub: f, peer: connectedSilentNode(t),
		silent: map[string]bool{DeviceSessionItems: true, DeviceSessionSend: true, "session.interrupt": true}}
	s.hub = h

	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"agent_read", map[string]any{"machine": "mini", "session_id": sid, "turns": 1}, "the transcript could not be read"},
		{"agent_send", map[string]any{"machine": "mini", "session_id": sid, "text": "status?"}, "check with agent_read before sending again"},
		{"agent_interrupt", map[string]any{"machine": "mini", "session_id": sid}, "it is not known whether it was done"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			raw, _ := json.Marshal(tc.args)
			done := make(chan proto.ToolResult, 1)
			start := time.Now()
			// The runtime's tool call: no deadline of its own.
			go func() {
				done <- s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: "conv1", Name: tc.tool, Arguments: raw})
			}()
			select {
			case res := <-done:
				text := res.Content[0].Text
				if !res.IsError || !strings.Contains(text, "did not answer within") || !strings.Contains(text, tc.want) {
					t.Fatalf("result: %q (error=%v)", text, res.IsError)
				}
				if took := time.Since(start); took > 5*time.Second {
					t.Fatalf("took %s", took)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the tool call still waits for a node that never answers")
			}
		})
	}
}

// The error says when the computer's node last reported, as the resource sample has it.
func TestNodeSilentErrorSaysTheLastReport(t *testing.T) {
	s, _ := newQuietService(t)
	d := &resourceDevs{fleetDevs: newFleetDevs(), samples: map[string]*proto.MetricSample{
		"mini": {T: time.Now().Add(-27 * time.Minute).Unix()},
	}}
	d.add("mini", "Mac mini")
	s.devs = d
	err := s.nodeSilent("mini", DeviceSessionItems, context.DeadlineExceeded)
	if got := err.Error(); !strings.Contains(got, "the node on Mac mini did not answer within 20s") || !strings.Contains(got, "last report was 27 min ago") {
		t.Fatalf("error: %q", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, rpc.ErrNotSent) || !sendUnknown(err) {
		t.Fatalf("kind: %v", err)
	}
	if sendUnknown(s.nodeSilent("mini", DeviceSessionSend, rpc.ErrNotSent)) {
		t.Fatal("a request that never went out counts as one that may have arrived")
	}
	if got := s.nodeSilent("gone", DeviceSessionItems, context.DeadlineExceeded).Error(); !strings.Contains(got, "no report from it is known") {
		t.Fatalf("error without a sample: %q", got)
	}
}

// Only a model's tool call is bounded, only for the methods listed, and never past a deadline its own code chose (a
// send that resumes a closed agent waits minutes).
func TestToolNodeContextBounds(t *testing.T) {
	plain := context.Background()
	if _, cancel, bounded := toolNodeContext(plain, DeviceSessionItems); bounded {
		cancel()
		t.Fatal("a call that is not a tool's was bounded")
	}
	tool := asToolCall(plain)
	if _, cancel, bounded := toolNodeContext(tool, DeviceSessionStart); bounded {
		cancel()
		t.Fatal("an agent's start was bounded")
	}
	ctx, cancel, bounded := toolNodeContext(tool, DeviceSessionItems)
	defer cancel()
	if d, ok := ctx.Deadline(); !bounded || !ok || time.Until(d) > toolNodeWait[DeviceSessionItems] {
		t.Fatalf("session.items: bounded=%v deadline=%v", bounded, d)
	}
	long, lcancel := context.WithTimeout(tool, 5*time.Minute)
	defer lcancel()
	if got, c, bounded := toolNodeContext(long, DeviceSessionSend); bounded || got != long {
		c()
		t.Fatal("a tool's own longer deadline was cut short")
	}
}
