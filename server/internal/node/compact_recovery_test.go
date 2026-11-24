package node

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestCompactRecoveryDeliveryRetryAndRestart(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	n.state.put(id, sessionState{Launcher: "claude", Cwd: "/example"})
	n.contextCompacted(id)
	st, _ := n.state.get(id)
	if st.RecoveryPending == "" {
		t.Fatal("compaction did not create a checkpoint")
	}
	// Serialize the durable node record: a restart must retain an undelivered checkpoint.
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var loaded sessionState
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	n.state = newState()
	n.state.put(id, loaded)
	answer := contextAnswer{RecoveryID: loaded.RecoveryPending, Recovery: `<persona-context checkpoint="test">recovery</persona-context>`}
	first, _ := n.turnContext(id, answer) // failed delivery: no commit
	second, commit := n.turnContext(id, answer)
	if first != second || !strings.Contains(second, "recovery") {
		t.Fatal("failed delivery consumed recovery")
	}
	commit()
	st, _ = n.state.get(id)
	if st.RecoveryPending != "" {
		t.Fatal("confirmed delivery did not consume recovery")
	}
	if duplicate, _ := n.turnContext(id, answer); strings.Contains(duplicate, "recovery") {
		t.Fatal("consumed checkpoint was delivered twice")
	}
	n.contextCompacted(id)
	fresh, _ := n.state.get(id)
	if fresh.RecoveryPending == answer.RecoveryID {
		t.Fatal("new compaction reused its checkpoint")
	}
	_, staleCommit := n.turnContext(id, answer)
	staleCommit()
	freshAfter, _ := n.state.get(id)
	if freshAfter.RecoveryPending != fresh.RecoveryPending {
		t.Fatal("stale delivery consumed a newer checkpoint")
	}
}

func TestCompactRecoverySummaryInstructionsReachRuntimes(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	instructions := prompts.RenderCompactInstructions("zh-Hans")
	n.state.put(id, sessionState{CompactInstructions: instructions})
	if got := n.assistantCompactCommand(id); got != "/compact "+instructions {
		t.Fatalf("Claude command: %q", got)
	}
	args := codexCompactPromptArgs(instructions)
	var decoded string
	if len(args) != 2 || args[0] != "-c" {
		t.Fatalf("args=%q", args)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(args[1], "compact_prompt=")), &decoded); err != nil || decoded != instructions {
		t.Fatalf("Codex compact prompt: %v %q", err, decoded)
	}
}

func TestCompactRecoveryDoesNotFetchIntentionallyOmittedContext(t *testing.T) {
	n, asked := closeoutHookNode(t, func(string, json.RawMessage) (any, error) { return contextAnswer{Text: "PRIVATE_OWNER_STATE"}, nil })
	const id = "11111111-2222-3333-4444-555555555555"
	n.state.put(id, sessionState{Launcher: "claude", Cwd: "/example"})
	n.contextCompacted(id)
	block, commit := n.turnContext(id, contextAnswer{}) // guest/public delivery intentionally supplied no context
	commit()
	if block != "" || asked.Load() != 0 {
		t.Fatalf("omitted context fetched owner state: %q (%d asks)", block, asked.Load())
	}
	st, _ := n.state.get(id)
	if st.RecoveryPending == "" {
		t.Fatal("omitted context consumed a future authorized recovery")
	}
}

func TestCompactRecoveryRefreshesOnlyAPrefetchFromBeforeItsCheckpoint(t *testing.T) {
	n, asked := closeoutHookNode(t, func(method string, raw json.RawMessage) (any, error) {
		var p struct {
			RecoveryID string `json:"recovery_id"`
		}
		if method != "assistant.context" {
			return nil, &rpc.Error{Code: "unexpected", Message: method}
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		out := contextAnswer{Text: "snapshot", RecoveryID: p.RecoveryID}
		if p.RecoveryID != "" {
			out.Recovery = "recovered checkpoint"
		}
		return out, nil
	})
	const id = "11111111-2222-3333-4444-555555555555"
	n.state.put(id, sessionState{Launcher: "claude", Cwd: "/example"})
	prefetched := n.assistantContext(id)
	n.contextCompacted(id)
	block, commit := n.turnContext(id, prefetched)
	if !strings.Contains(block, "recovered checkpoint") || asked.Load() != 2 {
		t.Fatalf("prefetch not refreshed: %q (%d asks)", block, asked.Load())
	}
	commit()
	// A request already made for this checkpoint must not be fetched again after an empty/unsupported answer.
	n.contextCompacted(id)
	current := n.assistantContext(id)
	current.Recovery, current.RecoveryID = "", ""
	_, _ = n.turnContext(id, current)
	if asked.Load() != 3 {
		t.Fatalf("same-checkpoint answer retried immediately: %d asks", asked.Load())
	}
}
