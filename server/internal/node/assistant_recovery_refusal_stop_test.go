package node

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// With Claude Code's automatic refusal fallback off (CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK, a gateway-shaped provider
// whose model list lacks a fallback model, #7319) a refused turn ends on its refusal: a model_refusal_no_fallback row
// and Claude Code's synthetic reply with stop_reason "refusal", not a 404 from a fallback model the gateway does not
// serve. The assistant's recovery still takes that ending as a safeguards refusal, whichever way the record gives the
// failure.
const (
	refusalStopPrompt = `{"type":"user","uuid":"u1","timestamp":"2026-01-01T12:00:00Z","isSidechain":false,"message":{"role":"user","content":"Sample request"}}`
	refusalStopRow    = `{"type":"system","subtype":"model_refusal_no_fallback","uuid":"r1","parentUuid":"u1","timestamp":"2026-01-01T12:00:01Z","isSidechain":false,"trigger":"refusal","originalModel":"primary-model","apiRefusalCategory":"synthetic-policy","apiRefusalExplanation":"Upstream refused the request","refusedUserMessageUuid":"u1"}`
	refusalStopText   = "API Error: Opus 5.5's safeguards flagged this message"
	// The newer frontier refusal banner: it names no safeguard and asks for a new session. Its synthetic reply says
	// refusal in stop_details instead of stop_reason.
	refusalFrontierText = "API Error: Opus 5.5 can't help with this. Start a new session to continue."
)

func refusalStopReply(extra string) string {
	return refusalReplyLine(extra, refusalStopText, `"stop_reason":"refusal",`)
}

func refusalFrontierReply(extra string) string {
	return refusalReplyLine(extra, refusalFrontierText,
		`"stop_reason":"end_turn","stop_details":{"type":"refusal","category":"frontier_llm","explanation":"Upstream refused the request"},`)
}

func refusalReplyLine(extra, text, stop string) string {
	return `{"type":"assistant","uuid":"e","parentUuid":"r1","timestamp":"2026-01-01T12:00:02Z","isSidechain":false,` + extra +
		`"message":{"id":"m-e","model":"<synthetic>","role":"assistant",` + stop +
		`"usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"` + text + `"}]}}`
}

// writeRefusalTranscript puts lines as conversation id's transcript on n.
func writeRefusalTranscript(t *testing.T, n *Node, id string, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.paths[id] = path
	return path
}

func TestAssistantRecoversARefusalStopWithoutFallback(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		name, extra, text string
		reply             func(string) string
		recorded          bool // the transcript gives the failed turn (assistantRecordKind), else the words decide
	}{
		{"an API error record without a code", `"isApiErrorMessage":true,`, refusalStopText, refusalStopReply, true},
		{"an API error record with a request error", `"isApiErrorMessage":true,"error":"invalid_request","apiErrorStatus":400,`, refusalStopText, refusalStopReply, true},
		{"a plain synthetic reply", ``, refusalStopText, refusalStopReply, false},
		{"the frontier banner", `"isApiErrorMessage":true,"error":"invalid_request","apiErrorStatus":400,`, refusalFrontierText, refusalFrontierReply, true},
		{"the frontier banner, no record", ``, refusalFrontierText, refusalFrontierReply, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := assistantNode(t)
			path := writeRefusalTranscript(t, n, id, refusalStopPrompt, refusalStopRow, tc.reply(tc.extra))
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			m := readQuickMeta(path, st)
			if m.modelFallback != nil || m.turnErr.code == proto.TurnErrorModelFallbackUnavailable {
				t.Fatalf("a stop without fallback read as a fallback: %+v, code %q", m.modelFallback, m.turnErr.code)
			}
			kinds := n.claudeResultKinds(id)
			if slices.Contains(kinds, assistantRecordKind) != tc.recorded || len(kinds) > 0 && kinds[0] == proto.TurnErrorUsageLimit {
				t.Fatalf("the refusal's record read as %v", kinds)
			}
			if !assistantRecoverable(true, tc.text, kinds) {
				t.Fatalf("the refusal is not recovered (kinds %v)", kinds)
			}
		})
	}
}

// Claude Code's own fallback to a model the gateway does not serve ends in a 404 that stays the user's to fix
// (model_fallback_unavailable, #2203): what the automatic switch leaves behind a gateway that does not serve the
// fallback model where the switch stays on (a provider with no model list, or one whose list names the model anyway).
func TestAssistantLeavesAnUnservedFallbackToTheUser(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	const missing = "API Error: 404 fallback-model is not available"
	n := assistantNode(t)
	fallback := strings.ReplaceAll(refusalStopRow, "model_refusal_no_fallback", "model_refusal_fallback")
	fallback = strings.ReplaceAll(fallback, `"originalModel"`, `"scope":"session","fallbackModel":"fallback-model","originalModel"`)
	notFound := `{"type":"assistant","uuid":"e","parentUuid":"r1","timestamp":"2026-01-01T12:00:02Z","isSidechain":false,"isApiErrorMessage":true,` +
		`"error":"model_not_found","apiErrorStatus":404,"message":{"id":"m-e","model":"<synthetic>","role":"assistant",` +
		`"content":[{"type":"text","text":"` + missing + `"}]}}`
	path := writeRefusalTranscript(t, n, id, refusalStopPrompt, fallback, notFound)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := readQuickMeta(path, st); m.turnErr.code != proto.TurnErrorModelFallbackUnavailable {
		t.Fatalf("the unserved fallback's 404: code %q", m.turnErr.code)
	}
	if kinds := n.claudeResultKinds(id); assistantRecoverable(true, missing, kinds) {
		t.Fatalf("the unserved fallback's 404 is replayed (kinds %v)", kinds)
	}
}

// The whole step as the result handler takes it (assistant.go): the kinds read from the transcript, then the chain,
// which moves a Claude safeguards refusal straight to the launcher's next model (#4283).
func TestAssistantRefusalStopMovesToTheNextModel(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		reply      func(string) string
	}{
		{"a safeguards refusal", refusalStopText, refusalStopReply},
		{"the frontier banner", refusalFrontierText, refusalFrontierReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "11111111-2222-3333-4444-555555555555"
			models, send := assistantRecoveryModels, assistantRecoverySend
			t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend = models, send })
			assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
				return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}}}, nil
			}
			got := make(chan string, 1)
			assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
				got <- execution.Model
				return nil
			}
			n := assistantNode(t)
			writeRefusalTranscript(t, n, id, refusalStopPrompt, refusalStopRow, tc.reply(`"isApiErrorMessage":true,`))
			p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}}
			n.procs[id] = p
			kinds := n.claudeResultKinds(id)
			if !slices.Contains(kinds, assistantRecordKind) {
				t.Fatalf("the transcript's failed turn was not read: %v", kinds)
			}
			if !n.recoverAssistantResult(p, true, tc.text, kinds...) {
				t.Fatal("the refusal stop was not recovered")
			}
			select {
			case model := <-got:
				if model != "fallback-model" {
					t.Fatalf("recovery sent on %q, want the launcher's next model", model)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no recovery send")
			}
		})
	}
}
