package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// continuousHeadlessUpdateNode finishes each current turn, but admits its replacement before the next inventory can
// observe the gap. Once told to hand off, it admits no replacement and reports the current turn busy until the test
// finishes it. The handoff's wire spelling is deliberately not pinned: a non-empty sessions.list request or an update
// control call outside the transfer methods is the signal. The old manager only polls with {}, so it never hands off.
type continuousHeadlessUpdateNode struct {
	*updatingNode

	activityMu   sync.Mutex
	accepting    bool
	turnBusy     bool
	turnReleased bool
	turns        int
	executions   int
	handoff      bool
	handoffSeen  chan struct{}
}

func newContinuousHeadlessUpdateNode(n *updatingNode, busy bool) *continuousHeadlessUpdateNode {
	return &continuousHeadlessUpdateNode{updatingNode: n, accepting: true, turnBusy: busy, handoffSeen: make(chan struct{})}
}

func (n *continuousHeadlessUpdateNode) stopAdmission() {
	n.activityMu.Lock()
	if !n.handoff {
		n.handoff = true
		n.accepting = false
		close(n.handoffSeen)
	}
	n.activityMu.Unlock()
}

func (n *continuousHeadlessUpdateNode) releaseTurn() {
	n.activityMu.Lock()
	n.turnReleased = true
	n.activityMu.Unlock()
}

func (n *continuousHeadlessUpdateNode) counts() (turns, executions int, busy, handoff bool) {
	n.activityMu.Lock()
	defer n.activityMu.Unlock()
	return n.turns, n.executions, n.turnBusy, n.handoff
}

func (n *continuousHeadlessUpdateNode) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == "sessions.list" {
		if raw := bytes.TrimSpace(params); len(raw) > 0 && !bytes.Equal(raw, []byte("{}")) {
			n.stopAdmission()
		}
		n.activityMu.Lock()
		defer n.activityMu.Unlock()
		if n.turnBusy && n.accepting {
			// The answering turn finished and its replacement was admitted before this inventory. There is work every
			// time the manager looks, although no single turn runs forever.
			n.turns++
			n.executions++
		}
		busy := n.turnBusy && !(n.handoff && n.turnReleased)
		if !busy {
			n.turnBusy = false
		}
		return map[string]any{"sessions": n.sessions, "update_busy": busy}, nil
	}
	if method == "assistant.send" {
		n.activityMu.Lock()
		defer n.activityMu.Unlock()
		if !n.accepting {
			return nil, &rpc.Error{Code: nodeproto.CodeRestarting, Message: nodeproto.CodeRestarting}
		}
		n.turns++
		n.executions++
		n.turnBusy = true
		return map[string]any{"session_id": "chat"}, nil
	}
	if strings.HasPrefix(method, "node.update.") {
		switch method {
		case "node.update.begin", "node.update.chunk", "node.update.commit", "node.update.abort", "node.update.confirm":
		default:
			n.stopAdmission()
			return map[string]any{"ok": true}, nil
		}
	}
	return n.updatingNode.handle(ctx, method, params)
}

func continuousHeadlessDeployFixture(t *testing.T, busy bool) (*Machine, *continuousHeadlessUpdateNode, string) {
	t.Helper()
	_, m, base, bin := linkedDeployFixture(t, kindLink)
	m.deployRetry = time.Second
	m.info.Protocol = nodeproto.Version
	m.info.Capabilities = []string{nodeproto.Sessions, nodeproto.NodeUpdate, nodeproto.NodeUpdateIdle}
	base.sessions = []proto.Session{}
	n := newContinuousHeadlessUpdateNode(base, busy)
	base.peer.OnRequest = n.handle
	return m, n, bin
}

func TestLinkedDeployHandoffAfterContinuousHeadlessActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, n, bin := continuousHeadlessDeployFixture(t, true)
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		record := nodeDeployRecord{Target: "new-build", State: "queued"}
		outcome := make(chan error, 1)
		go func() { outcome <- m.performRPCDeployment(ctx, bin, &record) }()

		select {
		case <-n.handoffSeen:
		case err := <-outcome:
			turns, executions, _, _ := n.counts()
			t.Fatalf("continuous headless activity starved the update instead of handing off: err=%v turns=%d executions=%d", err, turns, executions)
		}

		n.mu.Lock()
		begun := n.begin
		n.mu.Unlock()
		if begun != 0 {
			t.Fatal("the update transferred before the current headless turn drained")
		}
		_, before, busy, _ := n.counts()
		if !busy {
			t.Fatal("the handoff canceled the current headless turn instead of letting it finish")
		}
		var reply map[string]any
		err := m.call(ctx, "assistant.send", map[string]any{"session_id": "chat", "text": "must not execute"}, &reply)
		var coded *rpc.Error
		if !errors.As(err, &coded) || coded.Code != nodeproto.CodeRestarting {
			t.Fatalf("a post-grace headless turn was not refused before execution: %v", err)
		}
		_, after, _, _ := n.counts()
		if after != before {
			t.Fatalf("a refused headless turn executed: executions %d -> %d", before, after)
		}

		n.releaseTurn()
		select {
		case err := <-outcome:
			if err != nil {
				t.Fatalf("the drained update did not proceed: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("the update did not proceed after the current turn finished: %v", ctx.Err())
		}
		turns, _, _, _ := n.counts()
		if turns < 2 {
			t.Fatalf("the fixture did not sustain continuous headless activity: turns=%d", turns)
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.begin != 1 || n.commits != 1 || n.confirms != 1 {
			t.Fatalf("update did not complete after the handoff: begin=%d commits=%d confirms=%d", n.begin, n.commits, n.confirms)
		}
		if n.handoffGraceSeconds != proto.DefaultNodeUpdateHandoffGraceSeconds {
			t.Fatalf("commit carried handoff grace %d, want the default %d", n.handoffGraceSeconds, proto.DefaultNodeUpdateHandoffGraceSeconds)
		}
	})
}

func TestLinkedDeployNaturalIdleSkipsHandoffWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, n, bin := continuousHeadlessDeployFixture(t, false)
		started := time.Now()
		record := nodeDeployRecord{Target: "new-build", State: "queued"}
		if err := m.performRPCDeployment(t.Context(), bin, &record); err != nil {
			t.Fatalf("a naturally idle node waited for handoff: %v", err)
		}
		_, _, _, handoff := n.counts()
		if handoff {
			t.Fatal("a naturally idle node entered the handoff path")
		}
		if elapsed := time.Since(started); elapsed >= time.Hour {
			t.Fatalf("the natural-idle fast path waited for the grace: %s", elapsed)
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.begin != 1 || n.commits != 1 || n.confirms != 1 {
			t.Fatalf("natural-idle update did not complete: begin=%d commits=%d confirms=%d", n.begin, n.commits, n.confirms)
		}
	})
}
