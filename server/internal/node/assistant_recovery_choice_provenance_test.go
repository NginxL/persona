package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const recoveryChoiceProvenanceID = "11111111-2222-3333-4444-555555555555"

func recoveryChoiceExecution(model, effort, fingerprint string) modelExecution {
	return modelExecution{Model: model, Effort: effort, UseEffort: true, Fingerprint: fingerprint, IsMain: true}
}

func recoveryChoiceRecord(n *Node, returning bool) *proto.ModelRecovery {
	serialized, err := json.Marshal(map[string]any{
		"from": "primary-model", "to": "fallback-model", "reason": proto.ModelRecoveryReasonServer,
		"at": time.Now().UTC(), "returning": returning,
		"choice": map[string]any{
			"launcher": "claude", "model": "primary-model", "effort": "high", "use_effort": true, "fingerprint": "fp-high",
		},
	})
	if err != nil {
		panic(err)
	}
	var record proto.ModelRecovery
	if err := json.Unmarshal(serialized, &record); err != nil {
		panic(err)
	}
	return &record
}

func recoveryChoiceHold(n *Node, from, to modelExecution, at, until time.Time) {
	n.fallbacks = map[string]*assistantFallback{recoveryChoiceProvenanceID: {
		launcher: "claude", from: from, to: to, at: at, until: until,
	}}
}

// recoveryChoiceClassify is the ordinary-send classification seam, before process selection or delivery.
func recoveryChoiceClassify(n *Node, launcher string, asked modelExecution) modelExecution {
	execution, expired := n.heldFallbackWithExpiry(recoveryChoiceProvenanceID, launcher, asked)
	return n.applyModelRecoveryReturn(context.Background(), recoveryChoiceProvenanceID, asked.Model, execution, expired)
}

func recoveryChoiceFailure(p *assistantProc) string {
	p.replyModel = p.model.Model
	if p.replyModel == "" {
		p.replyModel = "primary-model"
	}
	p.repliedAt = time.Now()
	return "There is an issue with the selected model (primary-model). It may not exist or you may not have access to it."
}

func recoveryChoiceFail(t *testing.T, n *Node, sent chan modelExecution, p *assistantProc) modelExecution {
	t.Helper()
	if !n.recoverAssistantResult(p, true, recoveryChoiceFailure(p), "", assistantRecordKind) {
		t.Fatal("the model failure did not enter recovery")
	}
	return waitRecoverySend(t, sent)
}

// recoveryChoiceSwitch runs a real next-model step with a per-turn request whose full identity differs only as asked.
func recoveryChoiceSwitch(t *testing.T, n *Node, sent chan modelExecution, from modelExecution) modelExecution {
	t.Helper()
	p := &assistantProc{id: recoveryChoiceProvenanceID, agent: "claude", launcher: "claude", model: from, busy: true}
	r := &assistantTurnRecovery{
		retried: map[string]bool{from.Model: true}, used: map[string]bool{from.Model: true},
		launcher: "claude", from: from, lastResult: "API Error: 503 no eligible upstream account is currently available",
		triedOrder: []string{from.Model},
	}
	n.mu.Lock()
	if n.procs == nil {
		n.procs = map[string]*assistantProc{}
	}
	if n.recoveries == nil {
		n.recoveries = map[string]*assistantTurnRecovery{}
	}
	n.procs[recoveryChoiceProvenanceID] = p
	n.recoveries[recoveryChoiceProvenanceID] = r
	n.mu.Unlock()
	n.runAssistantRecovery(p, r, "claude", "", from, from.Model, false)
	return waitRecoverySend(t, sent)
}

func recoveryChoiceJSON(t *testing.T, n *Node) string {
	t.Helper()
	st, ok := n.state.get(recoveryChoiceProvenanceID)
	if !ok || st.ModelRecovery == nil {
		t.Fatal("the recovery was not persisted")
	}
	b, err := json.Marshal(st.ModelRecovery)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	choice, ok := record["choice"]
	if !ok || len(choice) == 0 || string(choice) == "null" {
		t.Fatalf("persisted recovery has no choice identity: %s", b)
	}
	return string(choice)
}

func TestRecoveryChoiceProvenanceMatrix(t *testing.T) {
	from := recoveryChoiceExecution("primary-model", "high", "fp-high")
	to := recoveryChoiceExecution("fallback-model", "high", "fp-high")
	now := time.Now()

	t.Run("fresh hold unchanged request stays on fallback", func(t *testing.T) {
		n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		n.putModelRecovery(recoveryChoiceProvenanceID, recoveryChoiceRecord(n, false))
		recoveryChoiceHold(n, from, to, now, now.Add(assistantFallbackHold))
		if got := recoveryChoiceClassify(n, "claude", from); got.Model != "fallback-model" {
			t.Fatalf("fresh exact request ran on %q, want active fallback", got.Model)
		}
	})

	t.Run("expired hold exact request expires naturally", func(t *testing.T) {
		n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		n.putModelRecovery(recoveryChoiceProvenanceID, recoveryChoiceRecord(n, false))
		recoveryChoiceHold(n, from, to, now.Add(-assistantFallbackHold-time.Minute), now.Add(-time.Minute))
		if got := recoveryChoiceClassify(n, "claude", from); got.Model != "primary-model" {
			t.Fatalf("expired exact request ran on %q, want configured model", got.Model)
		}
		if n.modelRecovery(recoveryChoiceProvenanceID) != nil {
			t.Fatal("natural expiry left the old recovery marker")
		}
	})

	t.Run("aged hold exact request does not become a cold fallback", func(t *testing.T) {
		n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		n.putModelRecovery(recoveryChoiceProvenanceID, recoveryChoiceRecord(n, false))
		recoveryChoiceHold(n, from, to, now.Add(-assistantRefusalSticky-time.Minute), now.Add(-assistantRefusalSticky))
		if got := recoveryChoiceClassify(n, "claude", from); got.Model != "primary-model" {
			t.Fatalf("request past the two-hour sticky window ran on %q, want natural expiry to primary-model", got.Model)
		}
	})

	t.Run("fresh cold record unchanged request stays on fallback", func(t *testing.T) {
		n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		n.putModelRecovery(recoveryChoiceProvenanceID, recoveryChoiceRecord(n, false))
		n.recoveryMarks = nil
		if got := recoveryChoiceClassify(n, "claude", from); got.Model != "fallback-model" {
			t.Fatalf("fresh cold exact request ran on %q, want persisted fallback", got.Model)
		}
	})

	t.Run("aged cold record exact request expires", func(t *testing.T) {
		n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		marker := recoveryChoiceRecord(n, false)
		marker.At = now.Add(-assistantRefusalSticky - time.Minute)
		n.putModelRecovery(recoveryChoiceProvenanceID, marker)
		n.recoveryMarks = nil
		if got := recoveryChoiceClassify(n, "claude", from); got.Model != "primary-model" {
			t.Fatalf("aged cold exact request ran on %q, want natural expiry to primary-model", got.Model)
		}
	})

	// Full execution identity, not just model text, decides continuation. Exercise every record age, including records
	// with no in-memory hold, because those previously fell through the cold-restart branch.
	for _, record := range []struct {
		name string
		age  time.Duration
		cold bool
	}{
		{name: "fresh hold"},
		{name: "expired hold", age: assistantFallbackHold + time.Minute},
		{name: "aged hold", age: assistantRefusalSticky + time.Minute},
		{name: "fresh cold record", cold: true},
		{name: "stale cold record", age: assistantRefusalSticky + time.Minute, cold: true},
	} {
		for _, change := range []struct {
			name     string
			launcher string
			asked    modelExecution
		}{
			{name: "effort", launcher: "claude", asked: recoveryChoiceExecution("primary-model", "low", "fp-high")},
			{name: "fingerprint", launcher: "claude", asked: recoveryChoiceExecution("primary-model", "high", "fp-new")},
			{name: "full execution", launcher: "claude", asked: modelExecution{Model: "primary-model", Effort: "high", Fingerprint: "fp-high", IsMain: true}},
			{name: "launcher", launcher: "codex", asked: from},
		} {
			t.Run(record.name+"/"+change.name+" change is a new choice", func(t *testing.T) {
				n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
				marker := recoveryChoiceRecord(n, false)
				if record.age > 0 {
					marker.At = now.Add(-record.age)
				}
				n.putModelRecovery(recoveryChoiceProvenanceID, marker)
				if record.cold {
					n.recoveryMarks = nil
				} else {
					at := now.Add(-record.age)
					recoveryChoiceHold(n, from, to, at, at.Add(assistantFallbackHold))
				}
				got := recoveryChoiceClassify(n, change.launcher, change.asked)
				if got.Model != change.asked.Model || !sameModelChoice(got, change.asked) {
					t.Fatalf("changed execution was classified as old-chain continuation: got %+v, want %+v", got, change.asked)
				}
			})
		}
	}

	for _, tc := range []struct {
		name      string
		returning bool
		reset     bool
		probe     bool
	}{
		{name: "Returning", returning: true},
		{name: "reset", reset: true},
		{name: "probe", probe: true},
	} {
		t.Run("explicit pick of old fallback wins over "+tc.name, func(t *testing.T) {
			n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
			marker := recoveryChoiceRecord(n, tc.returning)
			if tc.reset {
				past := now.Add(-time.Minute)
				marker.ResetsAt = &past
			}
			n.putModelRecovery(recoveryChoiceProvenanceID, marker)
			recoveryChoiceHold(n, from, to, now, now.Add(assistantFallbackHold))
			assistantModelProbe = func(context.Context, *Node, string, string) bool { return tc.probe }
			picked := recoveryChoiceExecution("fallback-model", "low", "fp-explicit-fallback")
			if got := recoveryChoiceClassify(n, "claude", picked); !sameModelChoice(got, picked) {
				t.Fatalf("automatic %s recovery overrode explicit old-fallback pick: got %+v, want %+v", tc.name, got, picked)
			}
		})
	}

	t.Run("rejected delivery preserves the old chain", func(t *testing.T) {
		n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		n.putModelRecovery(recoveryChoiceProvenanceID, recoveryChoiceRecord(n, false))
		recoveryChoiceHold(n, from, to, now, now.Add(assistantFallbackHold))
		before := *n.fallbacks[recoveryChoiceProvenanceID]
		sentinel := errors.New("reject the explicit pick before delivery")
		probeKey := n.home + "\x00claude\x00last-model"
		modelProbeCache.Lock()
		modelProbeCache.results[probeKey] = modelProbeResult{at: now, agent: "claude", err: sentinel}
		modelProbeCache.Unlock()
		t.Cleanup(func() {
			modelProbeCache.Lock()
			delete(modelProbeCache.results, probeKey)
			modelProbeCache.Unlock()
		})
		st, _ := n.state.get(recoveryChoiceProvenanceID)
		picked := recoveryChoiceExecution("last-model", "low", "fp-new-pick")
		_, err := n.assistantSend(context.Background(), recoveryChoiceProvenanceID, st.Cwd, "claude", "not delivered", nil, "", "", picked)
		if !errors.Is(err, sentinel) {
			t.Fatalf("assistantSend did not reach the deterministic delivery rejection: %v", err)
		}
		if n.modelRecovery(recoveryChoiceProvenanceID) == nil {
			t.Fatal("a rejected new pick erased the old recovery before delivery")
		}
		after := n.fallbacks[recoveryChoiceProvenanceID]
		if after == nil || !after.at.Equal(before.at) || !after.until.Equal(before.until) || !sameModelChoice(after.from, before.from) || !sameModelChoice(after.to, before.to) {
			t.Fatalf("a rejected new pick changed the old fallback chain: before %+v, after %+v", before, after)
		}
	})

	t.Run("exhausted chain then new chain cold and stale unchanged choice", func(t *testing.T) {
		n, sent := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		recoveryChoiceSwitch(t, n, sent, from)
		n.recordRecoveryExhausted(recoveryChoiceProvenanceID, proto.ModelRecoveryReasonServer, nil, []string{"primary-model", "fallback-model", "last-model"})
		low := recoveryChoiceExecution("primary-model", "low", "fp-low")
		recoveryChoiceSwitch(t, n, sent, low)
		for _, stale := range []bool{false, true} {
			n.recoveryMarks = nil // persisted-record classification, with no live recovery
			if stale {
				// An inherited stale record must not turn the new chain's still-active hold into a new choice.
				marker := *n.modelRecovery(recoveryChoiceProvenanceID)
				marker.At = now.Add(-assistantRefusalSticky - time.Minute)
				n.putModelRecovery(recoveryChoiceProvenanceID, &marker)
				n.recoveryMarks = nil
				recoveryChoiceHold(n, low, to, now, now.Add(assistantFallbackHold))
			}
			if got := recoveryChoiceClassify(n, "claude", low); got.Model != "fallback-model" {
				t.Fatalf("unchanged new-chain choice (stale=%v) ran on %q, want its own active fallback", stale, got.Model)
			}
		}
	})

	t.Run("unavailable evidence remains launcher scoped", func(t *testing.T) {
		n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		n.putModelRecovery(recoveryChoiceProvenanceID, recoveryChoiceRecord(n, false))
		n.unavailable = map[string]time.Time{
			unavailableKey("claude", "primary-model"): now.Add(time.Hour),
			unavailableKey("codex", "primary-model"):  now.Add(time.Hour),
		}
		n.releaseModelRecovery(recoveryChoiceProvenanceID, "primary-model")
		n.mu.Lock()
		_, claude := n.unavailable[unavailableKey("claude", "primary-model")]
		codexUntil, codex := n.unavailable[unavailableKey("codex", "primary-model")]
		n.mu.Unlock()
		if claude {
			t.Fatal("the recovered launcher's unavailable evidence was not released")
		}
		if !codex || !codexUntil.After(now) {
			t.Fatal("releasing Claude recovery erased Codex's independent unavailable evidence")
		}
	})
}

func TestRecoveryChoiceProvenanceRetries(t *testing.T) {
	from := recoveryChoiceExecution("primary-model", "high", "fp-high")
	to := recoveryChoiceExecution("fallback-model", "high", "fp-high")
	now := time.Now()

	t.Run("explicit new pick gets its own first retry", func(t *testing.T) {
		n, sent := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		recoveryChoiceHold(n, from, to, now, now.Add(assistantFallbackHold))
		picked := recoveryChoiceExecution("last-model", "low", "fp-new-pick")
		p := &assistantProc{id: recoveryChoiceProvenanceID, agent: "claude", launcher: "claude", model: picked}
		n.procs[recoveryChoiceProvenanceID] = p
		if got := recoveryChoiceFail(t, n, sent, p); !sameModelChoice(got, picked) {
			t.Fatalf("explicit pick's first failure selected %+v, want one same-choice retry %+v", got, picked)
		}
		n.mu.Lock()
		r := n.recoveries[recoveryChoiceProvenanceID]
		n.mu.Unlock()
		if r == nil || !sameModelChoice(r.from, picked) {
			t.Fatalf("the new turn inherited the old chain's provenance: %+v", r)
		}
	})

	t.Run("explicit pick of old fallback gets its own first retry", func(t *testing.T) {
		n, sent := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		n.putModelRecovery(recoveryChoiceProvenanceID, recoveryChoiceRecord(n, false))
		recoveryChoiceHold(n, from, to, now, now.Add(assistantFallbackHold))
		picked := recoveryChoiceExecution("fallback-model", "low", "fp-explicit-fallback")
		if got := recoveryChoiceClassify(n, "claude", picked); !sameModelChoice(got, picked) {
			t.Fatalf("old-fallback pick was rewritten before its turn: %+v", got)
		}
		p := &assistantProc{id: recoveryChoiceProvenanceID, agent: "claude", launcher: "claude", model: picked}
		n.procs[recoveryChoiceProvenanceID] = p
		if got := recoveryChoiceFail(t, n, sent, p); !sameModelChoice(got, picked) {
			t.Fatalf("explicit old-fallback pick did not get its own first retry: got %+v, want %+v", got, picked)
		}
	})

	for _, tc := range []struct {
		name       string
		from, to   modelExecution
		legacyJSON bool
	}{
		{
			name: "new record exact held to identity",
			from: from, to: to,
		},
		{
			name:       "legacy unfingerprinted exact held to identity",
			from:       modelExecution{Model: "primary-model", IsMain: true},
			to:         modelExecution{Model: "fallback-model", IsMain: true},
			legacyJSON: true,
		},
	} {
		t.Run(tc.name+" gets its own first retry after delivery", func(t *testing.T) {
			f := cliTest(t)
			script := `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init","claude_code_version":"2.1.322"}'
echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
while IFS= read -r line; do
  case "$line" in
    *'"subtype":"initialize"'*)
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"default","displayName":"Default"},{"value":"primary-model","resolvedModel":"primary-model","displayName":"Primary"},{"value":"fallback-model","resolvedModel":"fallback-model","displayName":"Fallback"},{"value":"last-model","resolvedModel":"last-model","displayName":"Last"}]}}}' ;;
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s"}}\n' "$id" ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
      echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"fallback-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
      echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"answered on explicit fallback"}},"parent_tool_use_id":null}'
      echo '{"type":"result","subtype":"success","is_error":false,"result":"answered on explicit fallback"}' ;;
  esac
done
`
			if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
				t.Fatal(err)
			}
			models, send, probe := assistantRecoveryModels, assistantRecoverySend, assistantModelProbe
			t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend, assistantModelProbe = models, send, probe })
			assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
				return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}, {ID: "last-model"}}}, nil
			}
			sent := make(chan modelExecution, 1)
			assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
				sent <- execution
				return nil
			}
			assistantModelProbe = func(context.Context, *Node, string, string) bool { return false }

			cwd := t.TempDir()
			f.n.state.put(recoveryChoiceProvenanceID, sessionState{
				Launcher: "claude", Cwd: cwd, Model: tc.from.Model, ModelEffort: tc.from.Effort,
				ModelFingerprint: tc.from.Fingerprint, IsMain: true,
			})
			marker := recoveryChoiceRecord(f.n, false)
			if tc.legacyJSON {
				marker = &proto.ModelRecovery{From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonServer, At: now.UTC()}
			}
			f.n.putModelRecovery(recoveryChoiceProvenanceID, marker)
			recoveryChoiceHold(f.n, tc.from, tc.to, now, now.Add(assistantFallbackHold))

			// This is an ordinary explicit send, not a recovery resend: its execution is byte-for-byte the held to choice.
			id, err := f.n.assistantSend(context.Background(), recoveryChoiceProvenanceID, cwd, "claude", "explicit fallback pick", nil, "", "", tc.to)
			if err != nil {
				t.Fatal(err)
			}
			if id != recoveryChoiceProvenanceID {
				t.Fatalf("delivered conversation %q, want %q", id, recoveryChoiceProvenanceID)
			}
			f.idle(t, id)
			p := f.proc(id)
			if p == nil || !sameModelChoice(p.model, tc.to) {
				t.Fatalf("the delivered process did not retain the exact explicit execution: %+v", p)
			}
			if got := recoveryChoiceFail(t, f.n, sent, p); !sameModelChoice(got, tc.to) {
				t.Fatalf("delivered exact old-fallback pick did not get its own first retry: got %+v, want %+v", got, tc.to)
			}
			f.n.mu.Lock()
			turn := f.n.recoveries[id]
			f.n.mu.Unlock()
			if turn == nil || !sameModelChoice(turn.from, tc.to) {
				t.Fatalf("delivered exact old-fallback pick kept conversation-wide instead of per-turn provenance: %+v", turn)
			}
			if f.n.modelRecovery(id) != nil {
				t.Fatal("the delivered explicit pick left the old recovery chain active")
			}
		})
	}

	t.Run("unchanged old request keeps bounded sticky fallback", func(t *testing.T) {
		n, sent := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		recoveryChoiceHold(n, from, to, now, now.Add(-time.Minute))
		p := &assistantProc{id: recoveryChoiceProvenanceID, agent: "claude", launcher: "claude", model: from}
		n.procs[recoveryChoiceProvenanceID] = p
		if got := recoveryChoiceFail(t, n, sent, p); got.Model != "fallback-model" {
			t.Fatalf("unchanged request inside sticky window used %q, want direct bounded fallback", got.Model)
		}
	})

	t.Run("aged exact request earns a fresh same-model retry", func(t *testing.T) {
		n, sent := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		recoveryChoiceHold(n, from, to, now.Add(-assistantRefusalSticky-time.Minute), now.Add(-assistantRefusalSticky))
		p := &assistantProc{id: recoveryChoiceProvenanceID, agent: "claude", launcher: "claude", model: from}
		n.procs[recoveryChoiceProvenanceID] = p
		if got := recoveryChoiceFail(t, n, sent, p); !sameModelChoice(got, from) {
			t.Fatalf("aged request used %+v, want a fresh same-choice retry", got)
		}
	})

	t.Run("older busy held to real newer delivery exhausts original used chain", func(t *testing.T) {
		f := cliTest(t)
		script := `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init","claude_code_version":"2.1.322"}'
echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
while IFS= read -r line; do
  case "$line" in
    *'"subtype":"initialize"'*)
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"primary-model","resolvedModel":"primary-model","displayName":"Primary"},{"value":"fallback-model","resolvedModel":"fallback-model","displayName":"Fallback"}]}}}' ;;
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s"}}\n' "$id" ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl" ;;
  esac
done
`
		if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
			t.Fatal(err)
		}
		models, send, probe := assistantRecoveryModels, assistantRecoverySend, assistantModelProbe
		t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend, assistantModelProbe = models, send, probe })
		// Both models were used by the older chain. Its next step must exhaust, never wrap to primary or retry fallback.
		assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
			return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}}}, nil
		}
		sent := make(chan modelExecution, 2)
		assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
			sent <- execution
			return nil
		}
		assistantModelProbe = func(context.Context, *Node, string, string) bool { return false }

		cwd := t.TempDir()
		f.n.state.put(recoveryChoiceProvenanceID, sessionState{
			Launcher: "claude", Cwd: cwd, Model: from.Model, ModelEffort: from.Effort,
			ModelFingerprint: from.Fingerprint, IsMain: true,
		})
		f.n.putModelRecovery(recoveryChoiceProvenanceID, recoveryChoiceRecord(f.n, false))
		recoveryChoiceHold(f.n, from, to, now, now.Add(assistantFallbackHold))
		old := &assistantTurnRecovery{
			retried:  map[string]bool{"primary-model": true, "fallback-model": true},
			used:     map[string]bool{"primary-model": true, "fallback-model": true},
			launcher: "claude", from: from, switched: true,
			triedOrder: []string{"primary-model", "fallback-model"}, lastResult: "API Error: 503 no eligible upstream account is currently available",
		}
		f.n.recoveries = map[string]*assistantTurnRecovery{recoveryChoiceProvenanceID: old}
		id, err := f.n.assistantSend(withAssistantRecovery(context.Background(), old, false), recoveryChoiceProvenanceID, cwd, "claude", "older held fallback turn", nil, "", "", to)
		if err != nil {
			t.Fatal(err)
		}
		p := f.proc(id)
		if p == nil || !sameModelChoice(p.model, to) {
			t.Fatalf("the older held fallback turn was not delivered: %+v", p)
		}

		newer := recoveryChoiceExecution("primary-model", "low", "fp-low")
		if _, err := f.n.assistantSend(context.Background(), id, cwd, "claude", "newer explicit primary low", nil, "", "", newer); err != nil {
			t.Fatal(err)
		}
		cliWait(t, "both inputs on the retained busy process", func() bool {
			return strings.Count(f.file("user.jsonl"), `"type":"user"`) == 2
		})
		if got := f.proc(id); got != p || !sameModelChoice(got.model, to) {
			t.Fatalf("the newer send replaced the older busy process or rewrote its execution: %+v", got)
		}

		if !f.n.recoverAssistantResult(p, true, recoveryChoiceFailure(p), "", assistantRecordKind) {
			t.Fatal("the older fallback failure did not enter recovery")
		}
		var unexpected *modelExecution
		waitRecovery(t, "the original used primary+fallback chain to exhaust", func() bool {
			select {
			case got := <-sent:
				unexpected = &got
				return true
			default:
			}
			f.n.mu.Lock()
			active := f.n.recoveries[id]
			f.n.mu.Unlock()
			marker := f.n.modelRecovery(id)
			return active == nil && marker != nil && marker.ChainExhausted
		})
		if unexpected != nil {
			t.Fatalf("older used-up chain sent another attempt on %q instead of exhausting: %+v", unexpected.Model, *unexpected)
		}
		if marker := f.n.modelRecovery(id); marker == nil || !marker.ChainExhausted {
			t.Fatalf("the original two-model chain did not publish exhaustion: %+v", marker)
		}
		select {
		case got := <-sent:
			t.Fatalf("exhaustion was followed by an unexpected send on %q", got.Model)
		default:
		}
	})

	// A newer primary/low pick is delivered while the original primary/high turn is still busy. Cancelling the
	// conversation-wide chain must not rewrite the older turn's own budget or the model its failure advances to.
	for _, tc := range []struct {
		name         string
		execution    modelExecution
		retried      map[string]bool
		used         map[string]bool
		switched     bool
		want         string
		wantSameTurn bool
	}{
		{
			name: "older busy from first failure", execution: from,
			retried: map[string]bool{}, used: map[string]bool{"primary-model": true},
			want: "primary-model", wantSameTurn: true,
		},
		{
			name: "older busy from after its retry", execution: from,
			retried: map[string]bool{"primary-model": true}, used: map[string]bool{"primary-model": true},
			want: "fallback-model",
		},
		{
			name: "older busy held to after its retry", execution: to,
			retried: map[string]bool{"primary-model": true, "fallback-model": true},
			used:    map[string]bool{"primary-model": true, "fallback-model": true}, switched: true,
			want: "last-model",
		},
	} {
		t.Run(tc.name+" survives newer pick", func(t *testing.T) {
			n, sent := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
			marker := recoveryChoiceRecord(n, true)
			n.putModelRecovery(recoveryChoiceProvenanceID, marker)
			recoveryChoiceHold(n, from, to, now, now.Add(assistantFallbackHold))
			p := &assistantProc{id: recoveryChoiceProvenanceID, agent: "claude", launcher: "claude", model: tc.execution, busy: true}
			old := &assistantTurnRecovery{
				retried: tc.retried, used: tc.used, launcher: "claude", from: from, switched: tc.switched,
				proc: p, triedOrder: []string{"primary-model"},
			}
			n.procs[recoveryChoiceProvenanceID] = p
			n.recoveries = map[string]*assistantTurnRecovery{recoveryChoiceProvenanceID: old}

			n.cancelAssistantRecovery(recoveryChoiceProvenanceID) // the ordinary newer send's takeover
			newer := recoveryChoiceExecution("primary-model", "low", "fp-newer")
			newer = recoveryChoiceClassify(n, "claude", newer)
			st, _ := n.state.get(recoveryChoiceProvenanceID)
			st.Model, st.ModelEffort, st.ModelFingerprint = newer.Model, newer.Effort, newer.Fingerprint
			n.state.put(recoveryChoiceProvenanceID, st)

			got := recoveryChoiceFail(t, n, sent, p)
			if got.Model != tc.want || tc.wantSameTurn && !sameModelChoice(got, tc.execution) {
				t.Fatalf("older turn after newer pick selected %+v, want model %q with its original retry budget", got, tc.want)
			}
		})
	}
}

func TestRecoveryChoiceProvenancePersistence(t *testing.T) {
	from := recoveryChoiceExecution("primary-model", "high", "fp-high")

	t.Run("identity survives later switches exhaustion and restart", func(t *testing.T) {
		n, sent := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		recoveryChoiceSwitch(t, n, sent, from)
		choice := recoveryChoiceJSON(t, n)

		// Conversation-wide request state is not recovery's identity and must not overwrite it.
		st, _ := n.state.get(recoveryChoiceProvenanceID)
		st.Model, st.ModelEffort, st.ModelFingerprint = "last-model", "low", "fp-unrelated"
		n.state.put(recoveryChoiceProvenanceID, st)
		n.recordRecoverySwitch(recoveryChoiceProvenanceID, "claude", "primary-model", "", "fallback-model", proto.ModelRecoveryReasonServer, nil, []string{"primary-model", "fallback-model"})
		if got := recoveryChoiceJSON(t, n); got != choice {
			t.Fatalf("newer request state rewrote recovery choice: before %s, after %s", choice, got)
		}
		n.recordRecoveryExhausted(recoveryChoiceProvenanceID, proto.ModelRecoveryReasonServer, nil, []string{"primary-model", "fallback-model", "last-model"})
		if got := recoveryChoiceJSON(t, n); got != choice {
			t.Fatalf("chain exhaustion changed its choice identity: before %s, after %s", choice, got)
		}

		st, _ = n.state.get(recoveryChoiceProvenanceID)
		b, err := json.Marshal(map[string]sessionState{recoveryChoiceProvenanceID: st})
		if err != nil {
			t.Fatal(err)
		}
		var loaded map[string]sessionState
		if err := json.Unmarshal(b, &loaded); err != nil {
			t.Fatal(err)
		}
		restarted, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		restarted.state.put(recoveryChoiceProvenanceID, loaded[recoveryChoiceProvenanceID])
		restarted.recoveryMarks = nil
		if got := recoveryChoiceJSON(t, restarted); got != choice {
			t.Fatalf("restart changed recovery choice: before %s, after %s", choice, got)
		}
	})

	t.Run("new chain does not inherit exhausted chain choice", func(t *testing.T) {
		n, sent := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		recoveryChoiceSwitch(t, n, sent, from)
		oldChoice := recoveryChoiceJSON(t, n)
		n.recordRecoveryExhausted(recoveryChoiceProvenanceID, proto.ModelRecoveryReasonServer, nil, []string{"primary-model", "fallback-model", "last-model"})
		if got := recoveryChoiceJSON(t, n); got != oldChoice {
			t.Fatalf("exhaustion changed the old chain's choice: before %s, after %s", oldChoice, got)
		}
		low := recoveryChoiceExecution("primary-model", "low", "fp-low")
		recoveryChoiceSwitch(t, n, sent, low)
		newChoice := recoveryChoiceJSON(t, n)
		if newChoice == oldChoice {
			t.Fatalf("new primary/low chain inherited exhausted primary/high choice %s", oldChoice)
		}
		b, err := json.Marshal(map[string]sessionState{recoveryChoiceProvenanceID: mustRecoveryChoiceState(t, n)})
		if err != nil {
			t.Fatal(err)
		}
		var loaded map[string]sessionState
		if err := json.Unmarshal(b, &loaded); err != nil {
			t.Fatal(err)
		}
		restarted, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		restarted.state.put(recoveryChoiceProvenanceID, loaded[recoveryChoiceProvenanceID])
		restarted.recoveryMarks = nil
		if got := recoveryChoiceJSON(t, restarted); got != newChoice {
			t.Fatalf("restart changed the new chain's choice: before %s, after %s", newChoice, got)
		}
	})

	t.Run("legacy record without choice remains compatible", func(t *testing.T) {
		legacyJSON := []byte(`{"11111111-2222-3333-4444-555555555555":{"launcher":"claude","cwd":"/tmp/legacy","model":"primary-model","model_effort":"high","model_fingerprint":"fp-high","is_main":true,"model_recovery":{"from":"primary-model","to":"fallback-model","reason":"server_error"}}}`)
		var loaded map[string]sessionState
		if err := json.Unmarshal(legacyJSON, &loaded); err != nil {
			t.Fatal(err)
		}
		n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		n.state.put(recoveryChoiceProvenanceID, loaded[recoveryChoiceProvenanceID])
		n.recoveryMarks = nil
		asked := recoveryChoiceExecution("primary-model", "high", "fp-high")
		if got := recoveryChoiceClassify(n, "claude", asked); got.Model != "fallback-model" {
			t.Fatalf("legacy unchanged cold request ran on %q, want its persisted fallback", got.Model)
		}
		changed := recoveryChoiceExecution("primary-model", "low", "fp-low")
		if got := recoveryChoiceClassify(n, "claude", changed); !sameModelChoice(got, changed) {
			t.Fatalf("legacy record forced a changed full execution onto its fallback: got %+v, want %+v", got, changed)
		}
	})

	t.Run("legacy cold post recovery state keeps unchanged primary high on fallback", func(t *testing.T) {
		n, _ := recoveryTestNode(t, recoveryChoiceProvenanceID, true, false)
		st, _ := n.state.get(recoveryChoiceProvenanceID)
		st.Model = "fallback-model"
		st.ModelEffort = effortAuto // fallback normalized the original unsupported high effort
		st.ModelFingerprint = "fp-high"
		n.state.put(recoveryChoiceProvenanceID, st)
		// Older records have no Choice. From/To and the actual post-recovery session row are all that survive restart.
		n.putModelRecovery(recoveryChoiceProvenanceID, &proto.ModelRecovery{
			From: "primary-model", To: "fallback-model", Reason: proto.ModelRecoveryReasonServer, At: time.Now().UTC(),
		})
		n.recoveryMarks = nil
		unchanged := recoveryChoiceExecution("primary-model", "high", "fp-high")
		if got := recoveryChoiceClassify(n, "claude", unchanged); got.Model != "fallback-model" {
			t.Fatalf("legacy cold unchanged primary/high request ran on %q, want persisted fallback until return/probe; execution %+v", got.Model, got)
		}
	})
}

func mustRecoveryChoiceState(t *testing.T, n *Node) sessionState {
	t.Helper()
	st, ok := n.state.get(recoveryChoiceProvenanceID)
	if !ok {
		t.Fatal("missing recovery session state")
	}
	return st
}
