package node

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netshape"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// orderHub is a manager's side of a resident node's framed, resumable connection that records, in the order its read
// loop meets them, the hook events and the cards (interaction.create) it gets.
type orderHub struct {
	hub, node *rpc.Peer
	mu        sync.Mutex
	order     []string
}

func (o *orderHub) note(what string) {
	o.mu.Lock()
	o.order = append(o.order, what)
	o.mu.Unlock()
}

func (o *orderHub) seen() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.order...)
}

// frameHookNode frames n's connection with resuming on over link (node to manager shaped by up), as a resident node's.
func frameHookNode(t *testing.T, n *Node, up netshape.Config) *orderHub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	nodeEnd, hubEnd := netshape.Pipe(up, netshape.Config{})
	t.Cleanup(func() { cancel(); nodeEnd.Close(); hubEnd.Close() })
	n.resumeWindow = time.Minute
	n.peer = rpc.New("n", nodeEnd)
	o := &orderHub{hub: rpc.New("h", hubEnd), node: n.peer}
	for _, p := range []*rpc.Peer{o.hub, n.peer} {
		p.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
		p.EnableResume(time.Minute)
		p.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil }
	}
	o.hub.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "interaction.create" {
			o.note("card")
			return proto.Resolution{Dismiss: true}, nil
		}
		return map[string]any{}, nil
	}
	o.hub.OnEvent = func(event string, _ json.RawMessage) {
		if event == "hook.event" {
			// The read loop takes its time over the event, as the manager's does: a card read before it would be noted
			// meanwhile, by its handler.
			time.Sleep(100 * time.Millisecond) // sleep-ok: lets a card read before the event show first
			o.note("event")
		}
	}
	go func() { _ = o.hub.Serve(ctx, hubEnd) }()
	go func() { _ = n.peer.Serve(ctx, nodeEnd) }()
	cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	defer ccancel()
	if err := o.hub.UpgradeLanes(cctx); err != nil {
		t.Fatal(err)
	}
	// A round trip each way: both sides read frames, so either can be resumed.
	if err := o.hub.Call(cctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := n.peer.Call(cctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	return o
}

// askingHook runs a hook that reports its event and then asks the phone (an MCP server's question), as Claude's hook
// process would, and says when it is done.
func askingHook(n *Node) chan struct{} {
	done := make(chan struct{})
	in := hookInput{HookEventName: "Elicitation", SessionID: "test-session", MCPServerName: "example-mcp", Message: "ok?",
		Mode: "form", RequestedSchema: json.RawMessage(`{"type":"object","properties":{"choice":{"type":"string"}}}`),
		ElicitationID: "request-example"}
	raw, _ := json.Marshal(in)
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = n.handleHook(ctx, sockRequest{Kind: "hook", Input: raw, Attempt: "attempt-example"})
	}()
	return done
}

// A hook's card reaches the manager after the hook's own event, as it did when the hook wrote the event itself, while
// the connection waits to be resumed and when the event is slow to go out on a working one (#10829's review).
func TestHookCardFollowsItsEvent(t *testing.T) {
	t.Run("lost", func(t *testing.T) {
		n, _ := testNode(t)
		o := frameHookNode(t, n, netshape.Config{})
		lost := make(chan error, 1)
		n.peer.OnLost = func(err error) { lost <- err }
		o.hub.OnLost = func(error) {}
		// The transport goes; the hook comes while the connection waits.
		o.hub.Lose(errTestCut)
		select {
		case <-lost:
		case <-time.After(5 * time.Second):
			t.Fatal("the node did not lose the transport")
		}
		done := askingHook(n)
		time.Sleep(300 * time.Millisecond) // sleep-ok: the hook queues what it would before the resume
		nodeEnd, hubEnd := netshape.Pipe(netshape.Config{}, netshape.Config{})
		t.Cleanup(func() { nodeEnd.Close(); hubEnd.Close() })
		accepted := make(chan error, 1)
		go func() {
			line, rest, err := rpc.ReadFirstLine(nodeEnd, 64<<10)
			if err != nil {
				accepted <- err
				return
			}
			req, _ := rpc.ParseResume(line)
			accepted <- n.peer.AcceptResume(req, rest, nodeEnd)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := o.hub.Resume(ctx, hubEnd, hubEnd); err != nil {
			t.Fatalf("resume: %v", err)
		}
		if err := <-accepted; err != nil {
			t.Fatalf("the node's side: %v", err)
		}
		waitHookDone(t, done)
		if got := o.seen(); strings.Join(got, ",") != "event,card" {
			t.Fatalf("the manager got %v", got)
		}
	})
	t.Run("slow", func(t *testing.T) {
		n, _ := testNode(t)
		o := frameHookNode(t, n, netshape.Config{Rate: 1 << 20})
		// Events ahead of the hook's, each a frame: 2 MiB at 1 MiB/s, past what a hook waits for its event.
		note := strings.Repeat("x", 30<<10)
		for range 64 {
			go func() { _ = n.peer.Notify("bulk.note", map[string]string{"x": note}) }()
		}
		time.Sleep(100 * time.Millisecond) // sleep-ok: they are queued before the hook's event
		start := time.Now()
		done := askingHook(n)
		waitHookDone(t, done)
		if took := time.Since(start); took < hookEventWait {
			t.Fatalf("the event went out within %v: nothing held it back", took)
		}
		if got := o.seen(); strings.Join(got, ",") != "event,card" {
			t.Fatalf("the manager got %v", got)
		}
	})
}

var errTestCut = &rpc.Error{Message: "the transport went"}

func waitHookDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the hook never returned")
	}
}
