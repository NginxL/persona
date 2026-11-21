package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A worker can hold the fallback that answered. Main and supervisor conversations keep their configured model (#12169).
func TestAssistantStaysOnTheFallbackThatAnswered(t *testing.T) {
	f := cliTest(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+claudeLikeCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	configured := modelExecution{Model: "primary-model", Fingerprint: "fp-main"}
	cwd := t.TempDir()
	id, err := f.n.assistantSend(context.Background(), "", cwd, "claude", "original request", nil, "", "", configured)
	if err != nil {
		t.Fatal(err)
	}
	cliWait(t, "the refusal to move to the fallback", func() bool { return strings.Count(f.file("user.jsonl"), `"type":"user"`) == 2 })
	f.idle(t, id)
	starts := strings.Count(f.file("cli-argv"), "\n")
	if _, err := f.n.assistantSend(context.Background(), id, cwd, "claude", "next message", nil, "", "", configured); err != nil {
		t.Fatal(err)
	}
	cliWait(t, "the next message to be answered", func() bool { return strings.Count(f.file("user.jsonl"), `"type":"user"`) == 3 })
	f.idle(t, id)
	if got := strings.Count(f.file("cli-argv"), "\n"); got != starts {
		t.Fatalf("the next message restarted the conversation (%d starts, was %d): %s", got, starts, f.file("cli-argv"))
	}
	if p := f.proc(id); p == nil || p.model.Model != "fallback-model" {
		t.Fatalf("the worker's next message left the fallback: %+v", p)
	}
}

func TestProtectedConversationDoesNotSwitchToTheFallback(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, role := range []struct {
		name         string
		main, leader bool
	}{{"main", true, false}, {"supervisor", false, true}} {
		t.Run(role.name, func(t *testing.T) {
			n, sent := recoveryTestNode(t, id, role.main, role.leader)
			p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}}
			n.procs[id] = p
			for attempt := 0; attempt < 3; attempt++ {
				if n.recoverAssistantResult(p, true, "API Error: 429 Too many requests", proto.TurnErrorTransient, assistantRecordKind) {
					t.Fatal("protected failure entered automatic model recovery")
				}
			}
			if p.model.Model != "primary-model" || n.modelRecovery(id) != nil || n.recoveries[id] != nil || len(sent) != 0 {
				t.Fatal("protected failure changed model or queued an immediate retry")
			}
		})
	}
}

// The hold covers only the model the switch moved away from, for its time, and ends with a compaction; another model
// picked ends it. A refusal within the sticky window skips the same-model retry.
func TestAssistantFallbackHoldBounds(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	from := modelExecution{Model: "primary-model", Fingerprint: "fp"}
	to := modelExecution{Model: "fallback-model", Fingerprint: "fp"}
	hold := func() *Node {
		n := assistantNode(t)
		n.mu.Lock()
		n.holdFallback(id, "claude", from, to)
		n.mu.Unlock()
		return n
	}
	if got := hold().heldFallback(id, "claude", modelExecution{Model: "primary-model", Fingerprint: "fp", IsMain: true}); got.Model != "fallback-model" || !got.IsMain {
		t.Fatalf("the configured model was not held on the fallback: %+v", got)
	}
	if got := hold().heldFallback(id, "codex", from); got.Model != "primary-model" {
		t.Fatalf("another launcher was held: %+v", got)
	}
	n := hold()
	other := modelExecution{Model: "other-model", Fingerprint: "fp2"}
	got := n.heldFallback(id, "claude", other)
	if got.Model != "other-model" {
		t.Fatalf("a model the user picked was overridden: %+v", got)
	}
	n.commitModelChoice(id, got)
	if got := n.heldFallback(id, "claude", from); got.Model != "primary-model" {
		t.Fatalf("the hold outlived another pick: %+v", got)
	}
	n = hold()
	n.fallbacks[id].until = time.Now().Add(-time.Second)
	if got := n.heldFallback(id, "claude", from); got.Model != "primary-model" {
		t.Fatalf("the hold outlived its time: %+v", got)
	}
	n.mu.Lock()
	r := n.newAssistantRecovery(id, "claude", from, "primary-model", "primary-model", "")
	n.mu.Unlock()
	if !r.retried["primary-model"] {
		t.Fatal("a refusal soon after a fallback answered still retries on the same model")
	}
	n = hold()
	n.contextCompacted(id)
	n.mu.Lock()
	r = n.newAssistantRecovery(id, "claude", from, "primary-model", "primary-model", "")
	n.mu.Unlock()
	if r.retried["primary-model"] || n.heldFallback(id, "claude", from).Model != "primary-model" {
		t.Fatal("a compaction left the hold or its sticky window")
	}
	n = hold()
	n.fallbacks[id].at = time.Now().Add(-assistantRefusalSticky - time.Minute)
	n.mu.Lock()
	r = n.newAssistantRecovery(id, "claude", from, "primary-model", "primary-model", "")
	n.mu.Unlock()
	if r.retried["primary-model"] {
		t.Fatal("a refusal long after the last fallback skipped the same-model retry")
	}
}

// A model fallback changes execution, not the current message's audience (#11130).
func TestAssistantFallbackKeepsPublicMessage(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	n := assistantNode(t)
	from := modelExecution{Model: "primary-model", Fingerprint: "fp"}
	n.mu.Lock()
	n.holdFallback(id, "claude", from, modelExecution{Model: "fallback-model", Fingerprint: "fp"})
	n.mu.Unlock()
	got := n.heldFallback(id, "claude", modelExecution{Model: "primary-model", Fingerprint: "fp", Public: true})
	if got.Model != "fallback-model" || !got.Public {
		t.Fatalf("the fallback lost the public marker: %+v", got)
	}
}

// Only a switched chain that answered starts a hold, on the model it answered with.
func TestAssistantHoldStartsOnlyWhenTheSwitchAnswered(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, c := range []struct {
		name               string
		switched, answered bool
		want               bool
	}{
		{"switched and answered", true, true, true},
		{"switched, failed for good", true, false, false},
		{"same-model retry answered", false, true, false},
	} {
		n := assistantNode(t)
		p := &assistantProc{id: id, launcher: "claude", model: modelExecution{Model: "fallback-model"}}
		n.procs[id] = p
		n.recoveries = map[string]*assistantTurnRecovery{id: {launcher: "claude", from: modelExecution{Model: "primary-model"}, switched: c.switched}}
		n.clearAssistantRecovery(p, c.answered)
		f := n.fallbacks[id]
		if (f != nil) != c.want || f != nil && (f.to.Model != "fallback-model" || f.from.Model != "primary-model") {
			t.Errorf("%s: hold %+v, want one: %v", c.name, f, c.want)
		}
	}
}

// A hub restart hands the holds to the next connection with the conversations.
func TestAssistantHandoverKeepsTheFallbackHold(t *testing.T) {
	k := newAssistantKeeper()
	a, b := assistantNode(t), assistantNode(t)
	a.keeper, b.keeper = k, k
	const id = "11111111-2222-3333-4444-555555555555"
	keptProc(t, a, id, "claude")
	a.mu.Lock()
	a.holdFallback(id, "claude", modelExecution{Model: "primary-model"}, modelExecution{Model: "fallback-model"})
	a.mu.Unlock()
	a.handOverAssistants()
	k.adopt(b)
	if got := b.heldFallback(id, "claude", modelExecution{Model: "primary-model"}); got.Model != "fallback-model" {
		t.Fatalf("the hold was lost over the handover: %+v", got)
	}
}

// A gateway's quota 429 says "usage limit" but its record says throttling: it is recovered. A subscription limit, by
// its record, and the words without one, are not (#3788).
func TestAssistantGatewayQuotaIsRecovered(t *testing.T) {
	const gateway = "API Error: Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at 13:30 UTC (in 23h45m)"
	for _, c := range []struct {
		name  string
		text  string
		kinds []string
		want  bool
	}{
		{"the gateway's quota, with its record", gateway, []string{"", assistantRecordKind}, true},
		{"the same words, no record", gateway, nil, false},
		{"a subscription limit, by its record", "API Error: Rate limit reached", []string{proto.TurnErrorUsageLimit, assistantRecordKind}, false},
		{"an authentication failure, by its record", "API Error: 503 upstream", []string{proto.TurnErrorUser, assistantRecordKind}, false},
	} {
		if got := assistantRecoverable(true, c.text, c.kinds); got != c.want {
			t.Errorf("%s: recoverable %v, want %v", c.name, got, c.want)
		}
	}
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	line := `{"type":"assistant","uuid":"e","timestamp":"2026-01-01T12:00:00Z","isSidechain":false,"isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,` +
		`"message":{"id":"m-e","model":"<synthetic>","role":"assistant","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"` + gateway + `"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	n.paths[id] = path
	if kinds := n.claudeResultKinds(id); !assistantRecoverable(true, gateway, kinds) {
		t.Fatalf("the transcript's record did not make the gateway's 429 recoverable: kinds %v", kinds)
	}
}
