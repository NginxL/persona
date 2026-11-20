package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A runtime connector's raw arguments are part of the card: a different body or
// destination can never share the same detached grant.
func TestRuntimeGateCardKeepsTheCompleteToolInput(t *testing.T) {
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution {
		return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID, Message: "Nothing was executed. End this turn now."}
	})
	cwd := t.TempDir()
	r.processIn(cwd, testThread)
	r.n.mu.Lock()
	r.n.procs[testThread] = &assistantProc{id: testThread, agent: "claude", cwd: cwd}
	g := r.n.larkCfg.Gates[0]
	g.Tool = &proto.GateTool{Name: "example_gate"}
	r.n.mu.Unlock()
	r.n.state.put(testThread, sessionState{Cwd: cwd, Launcher: "claude"})
	raw := json.RawMessage(`{"args":["POST","/conversations/F1/messages","{\"text\":\"first body\"}"]}`)
	result := r.n.assistantGateTool(context.Background(), &g, testThread, raw)
	r.hub.mu.Lock()
	created := len(r.hub.created)
	r.hub.mu.Unlock()
	if created == 0 {
		t.Fatalf("runtime gate raised no card; tool result = %+v", result)
	}
	card := r.hub.lastCreated(t)
	if string(card.ToolInput) != string(raw) || card.ExecutionCwd != cwd {
		t.Fatalf("runtime gate lost its exact retry identity: cwd=%q input=%s, want cwd=%q input=%s", card.ExecutionCwd, card.ToolInput, cwd, raw)
	}
}

// The real PreToolUse path returns promptly with an explicit non-execution
// result when the manager releases a detachable approval. No request reaches
// the gated API, and the card remains the one to continue later.
func TestLarkGatePendingResultEndsTheToolCallWithoutExecuting(t *testing.T) {
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution {
		return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID,
			Message: "The approval is still pending. Nothing was executed. End this turn now; a later message will report the outcome."}
	})
	cwd := t.TempDir()
	const command = `ex POST '/conversations/F1/messages' '{"text":"wait for approval"}'`
	spec := runLarkEditHook(t, r, cwd, command, nil)
	if spec["permissionDecision"] != "deny" {
		t.Fatalf("pending hook decision = %#v, want no execution", spec)
	}
	reason, _ := spec["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "End this turn now") || !strings.Contains(reason, "Nothing was executed") {
		t.Fatalf("pending hook reason = %q", reason)
	}
	card := r.hub.lastCreated(t)
	if !card.Detachable || card.ID == "" {
		t.Fatalf("command-gate card did not opt into detaching: %#v", card)
	}
	if card.ExecutionCwd != cwd || len(card.ToolInput) == 0 {
		t.Fatalf("detachable card lost its exact execution identity: cwd=%q input=%s", card.ExecutionCwd, card.ToolInput)
	}
	if got := r.api.requests(); len(got) != 0 {
		t.Fatalf("the pending command reached the gated API: %+v", got)
	}
}
