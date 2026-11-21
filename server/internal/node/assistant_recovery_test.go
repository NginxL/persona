package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A transient failure is published to the manager's durable retry scheduler.
// The node must not replay the original request or start an immediate retry.
func TestAssistantTransientErrorWaitsForManager(t *testing.T) {
	f := cliTest(t)
	script := `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  case "$line" in *'"type":"user"'*)
    printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
    count=$(cat "$CLI_FAKE_DIR/refusals" 2>/dev/null || echo 0)
    if [ "$count" -eq 0 ]; then
      echo 1 > "$CLI_FAKE_DIR/refusals"
      echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 503 no eligible upstream account is currently available"}'
    else
      echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"primary-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
      echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"answered"}},"parent_tool_use_id":null}'
      echo '{"type":"result","subtype":"success","is_error":false,"result":"answered"}'
    fi ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if got := f.file("user.jsonl"); strings.Count(got, `"type":"user"`) != 1 || strings.Count(got, "original request") != 1 {
		t.Fatalf("node started a retry instead of waiting for the manager: %s", got)
	}
}

// The user's model is not automatically a bad configuration: a gateway can call a model that answered moments ago
// missing. The real process/stream-json seam retries it once, without repeating the owner's message (#5234).
func TestAssistantTransientErrorRetriesAModelThatJustAnswered(t *testing.T) {
	f := cliTest(t)
	script := `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  case "$line" in *'"type":"user"'*)
    printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
    count=$(cat "$CLI_FAKE_DIR/failures" 2>/dev/null || echo 0)
    if [ "$count" -eq 0 ]; then
      echo 1 > "$CLI_FAKE_DIR/failures"
      echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"primary-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
      echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"first answer"}},"parent_tool_use_id":null}'
      printf '{"type":"assistant","uuid":"first","timestamp":"2026-11-10T00:00:00Z","message":{"role":"assistant","model":"primary-model","content":[{"type":"text","text":"first answer"}]}}\n' >> "$CLI_FAKE_DIR/transcript.jsonl"
      echo '{"type":"result","subtype":"success","is_error":false,"result":"first answer"}'
    elif [ "$count" -eq 1 ]; then
      echo 2 > "$CLI_FAKE_DIR/failures"
      echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"There is an issue with the selected model (primary-model). It may not exist or you may not have access to it."}'
    else
      echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"primary-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
      echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"recovered answer"}},"parent_tool_use_id":null}'
      printf '{"type":"assistant","uuid":"recovered","timestamp":"2026-11-10T00:01:00Z","message":{"role":"assistant","model":"primary-model","content":[{"type":"text","text":"recovered answer"}]}}\n' >> "$CLI_FAKE_DIR/transcript.jsonl"
      echo '{"type":"result","subtype":"success","is_error":false,"result":"recovered answer"}'
    fi ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	f.n.setPath(id, filepath.Join(f.dir, "transcript.jsonl"))
	if _, err := f.n.assistantSend(context.Background(), id, t.TempDir(), "claude", "next request", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	got := f.file("user.jsonl")
	if strings.Count(got, "next request") != 1 {
		t.Fatalf("the owner's message was not sent exactly once: %s", got)
	}
	var retryText string
	for _, raw := range strings.Split(strings.TrimSpace(got), "\n") {
		var line struct {
			Message struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatal(err)
		}
		for _, block := range line.Message.Content {
			if strings.Contains(block.Text, "因模型服务的一次临时错误中断") {
				retryText = block.Text
			}
		}
	}
	if retryText != prompts.RenderModelRetry() {
		t.Fatalf("a transient model route did not use the retry prompt: %s", got)
	}
	if p := f.proc(id); p == nil || p.replyModel != "primary-model" || p.repliedAt.IsZero() {
		t.Fatalf("the recovered model did not answer: %+v", p)
	}
}

func TestAssistantServiceFailureDoesNotSwitchLauncher(t *testing.T) {
	f := cliTest(t)
	script := `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init"}'
echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
model=primary-model
case "$*" in *'--model fallback-model'*) model=fallback-model ;; esac
while IFS= read -r line; do
  case "$line" in
    *'"subtype":"initialize"'*)
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"default","displayName":"Default"},{"value":"primary-model","resolvedModel":"primary-model","displayName":"Primary"},{"value":"fallback-model","resolvedModel":"fallback-model","displayName":"Fallback"}]}}}' ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
      count=$(cat "$CLI_FAKE_DIR/attempts" 2>/dev/null || echo 0)
      count=$((count+1))
      echo "$count" > "$CLI_FAKE_DIR/attempts"
      if [ "$count" -le 2 ]; then
        if [ "$count" -eq 2 ]; then
          echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"primary-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
        fi
        echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 503 no eligible upstream account is currently available"}'
      else
        printf '{"type":"stream_event","event":{"type":"message_start","message":{"model":"%s","usage":{"input_tokens":5}}},"parent_tool_use_id":null}\n' "$model"
        printf '{"type":"assistant","uuid":"final","timestamp":"2026-11-10T00:00:00Z","message":{"role":"assistant","model":"%s","content":[{"type":"text","text":"answered by fallback"}]}}\n' "$model" >> "$CLI_FAKE_DIR/transcript.jsonl"
        echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"answered by fallback"}},"parent_tool_use_id":null}'
        echo '{"type":"result","subtype":"success","is_error":false,"result":"answered by fallback"}'
      fi ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.n.mu.Lock()
	f.n.paths[id] = filepath.Join(f.dir, "transcript.jsonl")
	f.n.mu.Unlock()
	f.idle(t, id)
	if got := f.file("user.jsonl"); strings.Count(got, `"type":"user"`) != 1 {
		t.Fatalf("node replayed an outage instead of awaiting scheduled recovery: %s", got)
	}
	if got := f.file("cli-argv"); strings.Contains(got, "--model fallback-model") {
		t.Fatalf("temporary outage switched models before manager recovery: %s", got)
	}
}

// A recovery resend is the node continuing the failed turn, not a message the user sent while it answered. The recovery
// holds the failed turn busy, but neither its same-model retry to the running process nor the resend to the next
// model's new process carries the mid-turn note, and neither moves foreground work to the background, even when the
// conversation's messages carry interject_background (the setting on) and its Claude Code takes background_tasks
// (#10178). The fake CLI is the real node process/stream-json seam, not a model call.
func TestAssistantRecoveryResendIsNoInterjection(t *testing.T) {
	f := cliTest(t)
	script := `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init","claude_code_version":"2.1.322"}'
echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
model=primary-model
case "$*" in *'--model fallback-model'*) model=fallback-model ;; esac
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CLI_FAKE_DIR/stdin.jsonl"
  case "$line" in
    *'"subtype":"initialize"'*)
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"default","displayName":"Default"},{"value":"primary-model","resolvedModel":"primary-model","displayName":"Primary"},{"value":"fallback-model","resolvedModel":"fallback-model","displayName":"Fallback"}]}}}' ;;
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s"}}\n' "$id" ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
      count=$(cat "$CLI_FAKE_DIR/attempts" 2>/dev/null || echo 0)
      count=$((count+1))
      echo "$count" > "$CLI_FAKE_DIR/attempts"
      if [ "$count" -eq 1 ]; then
        echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"primary-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
        echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"There is an issue with the selected model (primary-model). It may not exist or you may not have access to it."}'
      elif [ "$count" -eq 2 ]; then
        # The same-model retry fails a moment later: any control the node writes after it is in reaches this process.
        echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"primary-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
        (sleep 0.5; echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"There is an issue with the selected model (primary-model). It may not exist or you may not have access to it."}') &
      else
        printf '{"type":"stream_event","event":{"type":"message_start","message":{"model":"%s","usage":{"input_tokens":5}}},"parent_tool_use_id":null}\n' "$model"
        echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"answered by fallback"}},"parent_tool_use_id":null}'
        echo '{"type":"result","subtype":"success","is_error":false,"result":"answered by fallback"}'
      fi ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	// The manager sends every message of a conversation with interject_background while the setting is on.
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "", modelExecution{Fingerprint: "fp", Interject: true})
	if err != nil {
		t.Fatal(err)
	}
	cliWait(t, "the same-model retry, then the next launcher model's resend", func() bool {
		return strings.Count(f.file("user.jsonl"), `"type":"user"`) == 3
	})
	f.idle(t, id)
	if p := f.proc(id); p == nil || p.model.Model != "fallback-model" {
		t.Fatalf("the next model did not answer: %+v", p)
	}
	notes := map[string]bool{
		prompts.RenderInterject(prompts.Interject{}):                 true,
		prompts.RenderInterject(prompts.Interject{Background: true}): true,
	}
	lines := strings.Split(strings.TrimSpace(f.file("user.jsonl")), "\n")
	for i, raw := range lines {
		var line struct {
			Message struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatal(err)
		}
		retry, note := false, false
		for _, block := range line.Message.Content {
			retry = retry || block.Text == prompts.RenderModelRetry()
			note = note || notes[block.Text]
		}
		if i > 0 && !retry {
			t.Fatalf("resend %d is not the model retry prompt: %s", i, raw)
		}
		if note {
			t.Errorf("resend %d (%s) carries the mid-turn interjection note: %s", i, map[int]string{0: "the owner's message", 1: "same-model retry", 2: "next model"}[i], raw)
		}
	}
	if got := f.file("stdin.jsonl"); strings.Contains(got, `"subtype":"background_tasks"`) {
		t.Errorf("a recovery resend moved foreground work to the background:\n%s", got)
	}
}

func TestAssistantCodexStructuredRecoveryClasses(t *testing.T) {
	send := assistantRecoverySend
	t.Cleanup(func() { assistantRecoverySend = send })
	for _, tc := range []struct {
		name string
		info any
		want bool
	}{
		{"internal server error", "internalServerError", true},
		{"server overloaded", "serverOverloaded", true},
		{"rate limit", "rateLimitExceeded", true},
		{"structured 429", map[string]any{"httpConnectionFailed": map[string]any{"httpStatusCode": 429}}, true},
		{"structured 502", map[string]any{"responseTooManyFailedAttempts": map[string]any{"httpStatusCode": 502}}, true},
		{"usage limit", "usageLimitExceeded", false},
		{"stream lost without a status", map[string]any{"responseStreamDisconnected": map[string]any{"httpStatusCode": nil}}, false},
		{"request timeout is not a 429 or 5xx", map[string]any{"httpConnectionFailed": map[string]any{"httpStatusCode": 408}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			p, c, _ := newFakeCodex(t, n, t.TempDir())
			p.id, c.threadID = testThread, testThread
			p.busy = true
			n.procs[testThread] = p
			called := make(chan struct{}, 1)
			assistantRecoverySend = func(context.Context, *Node, *assistantProc, string, string, modelExecution) error {
				called <- struct{}{}
				return nil
			}
			params, err := json.Marshal(map[string]any{"threadId": testThread, "turn": map[string]any{
				"id": "structured-error", "status": "failed",
				"error": map[string]any{"message": "Internal server error", "codexErrorInfo": tc.info},
			}})
			if err != nil {
				t.Fatal(err)
			}
			c.notification("turn/completed", params)
			select {
			case <-called:
				t.Fatal("model service error bypassed the durable scheduler")
			default:
			}
			p.mu.Lock()
			busy := p.busy
			p.mu.Unlock()
			if busy {
				t.Fatal("failed turn held the execution process busy")
			}
		})
	}
}

func TestModelRetryPromptGolden(t *testing.T) {
	goldenNode(t, "model_retry", prompts.RenderModelRetry())
}

func TestAssistantRecoverableErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		failed, want bool
	}{
		{"safeguards", "API Error: 400 safeguards flagged this message", true, true},
		{"429", "API Error: 429 Too Many Requests", true, true},
		{"subscription 429", "API Error: 429 account_quota_exhausted: Account usage limit reached", true, false},
		{"usage limit text", "API Error: 429 usage_limit_reached", true, false},
		{"rate limit", "API Error: rate_limit_error", true, true},
		{"500", "API Error: 500 internal server error", true, true},
		{"503", "API Error: 503 no eligible upstream", true, true},
		{"529", "API Error: 529 overloaded", true, true},
		{"own 400", "API Error: 400 invalid request", true, false},
		// Its own one resend in a new process, never an ordinary retry or a move to another model (#10365).
		{"tool search refused", "API Error: 400 request is not representable on this edge: ToolSearch is not supported on this endpoint (defer_loading:true, tool_search_*, tool_reference)", true, false},
		{"400 with upstream detail", "API Error: 400 rejected; upstream 500 was only diagnostic", true, false},
		{"401", "API Error: 401 invalid API key", true, false},
		{"model route", "API Error: 404 model not found", true, false},
		{"context", "Prompt is too long", true, false},
		{"4290 milliseconds is not 429", "API Error: timed out after 4290ms", true, false},
		{"successful result mentioning an error", "API Error: 503", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := assistantRecoverableError(tc.failed, tc.result, true); got != tc.want {
				t.Fatalf("assistantRecoverableError(%v, %q) = %v, want %v", tc.failed, tc.result, got, tc.want)
			}
		})
	}
}

func TestNextAssistantModelFollowsLauncherOrderOnce(t *testing.T) {
	list := proto.LauncherModels{Models: []proto.AgentModel{
		{ID: "default", Default: true},
		{ID: "primary", Resolved: "resolved-primary"},
		{ID: "fallback", Resolved: "resolved-fallback"},
		{ID: "third"},
	}}
	if got, ok := nextAssistantModel(list, "resolved-primary", map[string]bool{"resolved-primary": true, "primary": true}, assistantFit{}); !ok || got != "fallback" {
		t.Fatalf("next after a resolved alias = %q %v", got, ok)
	}
	if got, ok := nextAssistantModel(list, "fallback", map[string]bool{"fallback": true}, assistantFit{}); !ok || got != "third" {
		t.Fatalf("next after fallback = %q %v", got, ok)
	}
	if got, ok := nextAssistantModel(list, "custom", map[string]bool{"custom": true}, assistantFit{}); !ok || got != "primary" {
		t.Fatalf("a custom current model starts with the first concrete entry = %q %v", got, ok)
	}
	// Past the last entry the list goes on from the top (#10277); it is exhausted once every entry was tried.
	if got, ok := nextAssistantModel(list, "third", map[string]bool{"third": true}, assistantFit{}); !ok || got != "primary" {
		t.Fatalf("next after the last entry = %q %v, want primary", got, ok)
	}
	tried := map[string]bool{"third": true, "primary": true, "fallback": true}
	if got, ok := nextAssistantModel(list, "third", tried, assistantFit{}); ok || got != "" {
		t.Fatalf("the exhausted list selected %q", got)
	}
	list.Locked = "the launcher fixes its model"
	if got, ok := nextAssistantModel(list, "primary", nil, assistantFit{}); ok || got != "" {
		t.Fatalf("a locked launcher selected %q", got)
	}
}

func TestNextAssistantModelResolvesAnUnreportedDefault(t *testing.T) {
	list := proto.LauncherModels{Models: []proto.AgentModel{
		{ID: "default", Default: true, Resolved: "primary-model"},
		{ID: "primary-model"},
		{ID: "fallback-model"},
	}}
	if got, ok := nextAssistantModel(list, "", map[string]bool{}, assistantFit{}); !ok || got != "fallback-model" {
		t.Fatalf("an unreported default selected %q %v, want the genuinely different fallback-model", got, ok)
	}
	codex := proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model", Default: true}, {ID: "fallback-model"}}}
	if got, ok := nextAssistantModel(codex, "", nil, assistantFit{}); !ok || got != "fallback-model" {
		t.Fatalf("a native default model selected %q %v, want fallback-model", got, ok)
	}
	if got, ok := nextAssistantModel(list, "default", nil, assistantFit{}); !ok || got != "fallback-model" {
		t.Fatalf("the literal default alias selected %q %v, want fallback-model", got, ok)
	}
}

func TestAssistantFallbackDoesNotRepeatTheResolvedAlias(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "opus"}, replyModel: "resolved-opus", tasksObserved: true}
	n.procs[id] = p
	r := &assistantTurnRecovery{retried: map[string]bool{"opus": true}, used: map[string]bool{"opus": true}}
	n.recoveries = map[string]*assistantTurnRecovery{id: r}

	models := assistantRecoveryModels
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{
			{ID: "opus", Resolved: "resolved-opus"},
			{ID: "resolved-opus", Resolved: "resolved-opus"},
			{ID: "sonnet", Resolved: "resolved-sonnet"},
		}}, nil
	}
	send := assistantRecoverySend
	selected := make(chan string, 1)
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		selected <- execution.Model
		return nil
	}
	t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend = models, send })
	if !n.recoverAssistantResult(p, true, "API Error: safeguards flagged") {
		t.Fatal("the failed turn did not enter recovery")
	}
	select {
	case got := <-selected:
		if got != "sonnet" {
			t.Fatalf("the alias and its resolved model were retried as different models: selected %q, want sonnet", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the resolved alias was not followed by a genuinely different model")
	}
}

func TestAssistantFallbackEffortFollowsTheNextModel(t *testing.T) {
	list := proto.LauncherModels{Models: []proto.AgentModel{{ID: "supports", Efforts: []string{"high", "max"}}, {ID: "limited", Efforts: []string{"low", "high"}}, {ID: "unknown"}}}
	for _, tc := range []struct {
		id, effort, want string
	}{
		{"supports", "max", "max"},
		{"limited", "max", effortAuto},
		{"unknown", "max", effortAuto},
		{"limited", effortAuto, effortAuto},
	} {
		got := assistantExecutionForModel(modelExecution{Effort: tc.effort, UseEffort: true}, list, tc.id)
		if got.Model != tc.id || got.Effort != tc.want || !got.UseEffort {
			t.Errorf("%s at %s: %+v, want effort %s", tc.id, tc.effort, got, tc.want)
		}
	}
}

func TestAssistantRecoveryStopsAfterBothModelsAndTheirRetries(t *testing.T) {
	f := cliTest(t)
	script := `echo '{"type":"system","subtype":"init"}'
echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
model=primary-model
case "$*" in *'--model fallback-model'*) model=fallback-model ;; esac
while IFS= read -r line; do
  case "$line" in
    *'"subtype":"initialize"'*)
      echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"default"},{"value":"primary-model","resolvedModel":"primary-model"},{"value":"fallback-model","resolvedModel":"fallback-model"}]}}}' ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
      printf '{"type":"stream_event","event":{"type":"message_start","message":{"model":"%s","usage":{"input_tokens":5}}},"parent_tool_use_id":null}\n' "$model"
      echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: safeguards flagged"}' ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if got := strings.Count(f.file("user.jsonl"), `"type":"user"`); got != 2 {
		t.Fatalf("two safeguarded models made %d requests, want 2", got)
	}
	if p := f.proc(id); p == nil || p.model.Model != "fallback-model" || p.replyModel != "fallback-model" || p.busy {
		t.Fatalf("the exhausted recovery did not retain the final model and error state: %+v", p)
	}
}

// The model switch registers its replacement before it marks it busy and sends it the retry. The fixture's wait for
// the end of a turn must not take that gap for it, as the node does not (#3815): waiting on busy alone counted the
// requests while the fallback model had not been asked yet.
func TestCLIFixtureIdleWaitsOutTheModelSwitch(t *testing.T) {
	n := assistantNode(t)
	f := &cliFixture{n: n}
	const id = "11111111-2222-3333-4444-555555555555"
	replacement := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "fallback-model"}, tasksObserved: true}
	n.procs[id] = replacement
	n.recoveries = map[string]*assistantTurnRecovery{id: {
		retried: map[string]bool{"primary-model": true},
		used:    map[string]bool{"primary-model": true, "fallback-model": true},
		proc:    replacement,
		running: true,
	}}
	if f.turnOver(id) {
		t.Fatal("the registered replacement, not yet busy, ended a turn its recovery still holds")
	}
	if sessions := n.assistantSessions(); len(sessions) != 1 || sessions[0].Status != proto.StatusWorking {
		t.Fatalf("the node publishes the model switch as %+v, want working", sessions)
	}
	n.mu.Lock()
	delete(n.recoveries, id) // finishAssistantRecovery ends the recovery before it clears busy
	n.mu.Unlock()
	if !f.turnOver(id) {
		t.Fatal("an idle process with no recovery did not end the turn")
	}
}

func TestAssistantStartupRetryKeepsItsRecovery(t *testing.T) {
	fakeClaudeHome(t, "read line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	shortStartRetries(t, 300*time.Millisecond)
	claude := filepath.Join(os.Getenv("HOME"), "bin", "claude")
	moved := claude + ".installing"
	if err := os.Rename(claude, moved); err != nil {
		t.Fatal(err)
	}
	back := make(chan error, 1)
	// the install finishes half a second in, while the start is being retried
	time.AfterFunc(500*time.Millisecond, func() { back <- os.Rename(moved, claude) })
	n := codexTestNode(t)
	withFakeHub(t, n)
	const id = "11111111-2222-3333-4444-555555555555"
	cwd := t.TempDir()
	old := keptProc(t, n, id, "claude")
	old.launcher, old.cwd, old.tasksObserved = "claude", cwd, true
	r := &assistantTurnRecovery{retried: map[string]bool{}, used: map[string]bool{}, running: true}
	n.recoveries = map[string]*assistantTurnRecovery{id: r}
	_, err := n.assistantSend(withAssistantRecovery(context.Background(), r, true), id, cwd, "claude", "retry", nil, "", "")
	if err := <-back; err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatalf("the launcher's internal startup retry failed: %v", err)
	}
	n.mu.Lock()
	current, recovery := n.procs[id], n.recoveries[id]
	n.mu.Unlock()
	if current == nil || !current.alive() {
		t.Fatal("the launcher retry did not leave a live process")
	}
	if recovery != r {
		n.stopAssistant(id)
		t.Fatal("the failed starter's exit watcher finalized the recovery before its replacement was delivered")
	}
	n.mu.Lock()
	follows := r.proc == current
	n.mu.Unlock()
	if !follows {
		n.stopAssistant(id)
		t.Fatal("the recovery still follows the starter that exited, not its replacement (#3495)")
	}
	n.stopAssistant(id)
}

func TestAssistantProcessExitClearsItsRecovery(t *testing.T) {
	f := cliTest(t)
	script := `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init"}'
echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"default"},{"value":"fallback-model"}]}}}'
echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
while IFS= read -r line; do
  case "$line" in *'"type":"user"'*)
    printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
    count=$(cat "$CLI_FAKE_DIR/attempts" 2>/dev/null || echo 0)
    count=$((count+1))
    echo "$count" > "$CLI_FAKE_DIR/attempts"
    if [ "$count" -eq 1 ]; then
      echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 503 unavailable"}'
    else
      exit 0 # the recovered turn ends without any result event
    fi ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if got := strings.Count(f.file("user.jsonl"), `"type":"user"`); got != 1 {
		t.Fatalf("temporary failure was immediately replayed: %d sends", got)
	}
	f.n.stopAssistant(id)
	cliWait(t, "the exited process to publish its final state", func() bool {
		f.n.mu.Lock()
		defer f.n.mu.Unlock()
		return f.n.procs[id] == nil && f.n.recoveries[id] == nil
	})
}

func TestAssistantRecoveryKeepsSessionWorkingBetweenProcesses(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}, tasksObserved: true}
	n.procs[id] = p
	r := &assistantTurnRecovery{retried: map[string]bool{"primary-model": true}, used: map[string]bool{"primary-model": true}}
	n.recoveries = map[string]*assistantTurnRecovery{id: r}

	models := assistantRecoveryModels
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}}}, nil
	}
	send := assistantRecoverySend
	gap, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	assistantRecoverySend = func(_ context.Context, n *Node, p *assistantProc, _, _ string, _ modelExecution) error {
		n.mu.Lock()
		delete(n.procs, p.id) // the old process is gone; the replacement has not finished starting
		n.mu.Unlock()
		close(gap)
		<-release
		close(returned)
		return nil
	}
	t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend = models, send })
	if !n.recoverAssistantResult(p, true, "API Error: safeguards flagged") {
		t.Fatal("the failed turn did not enter recovery")
	}
	select {
	case <-gap:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery did not reach the process replacement gap")
	}
	sessions := n.assistantSessions()
	if len(sessions) != 1 || sessions[0].SessionID != id || sessions[0].Status != proto.StatusWorking {
		close(release)
		<-returned
		t.Fatalf("the recovery gap was published as a final non-working state: %+v", sessions)
	}
	n.mu.Lock()
	n.procs[id] = &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "fallback-model"}}
	n.mu.Unlock()
	sessions = n.assistantSessions()
	if len(sessions) != 1 || sessions[0].Status != proto.StatusWorking {
		close(release)
		<-returned
		t.Fatalf("the registered replacement published Done before its message was delivered: %+v", sessions)
	}
	close(release)
	<-returned
}

func TestAssistantRecoveryCancelReleasesTheHeldTurn(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	p := &assistantProc{id: id, busy: true}
	n.procs[id] = p
	n.recoveries = map[string]*assistantTurnRecovery{id: {running: true}}
	n.cancelAssistantRecovery(id)
	p.mu.Lock()
	busy := p.busy
	p.mu.Unlock()
	if busy {
		t.Fatal("a newer message's failed send would leave the cancelled turn working forever")
	}
	if _, ok := n.recoveries[id]; ok {
		t.Fatal("the newer message did not cancel the old recovery")
	}
}

func TestAssistantRecoveryIgnoresAReplacedProcessCleanup(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	old, current := &assistantProc{id: id}, &assistantProc{id: id}
	n.procs[id] = current
	n.recoveries = map[string]*assistantTurnRecovery{id: {running: true}}
	n.clearAssistantRecovery(old, true)
	if n.recoveries[id] == nil {
		t.Fatal("a replaced process cleared its replacement's recovery")
	}
	n.clearAssistantRecovery(current, true)
	if n.recoveries[id] != nil {
		t.Fatal("the current process did not clear its completed recovery")
	}
}

func TestAssistantInterruptCancelsRecovery(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	n.recoveries = map[string]*assistantTurnRecovery{id: {running: true}}
	if err := n.assistantInterrupt(id); err != nil {
		t.Fatal(err)
	}
	if _, ok := n.recoveries[id]; ok {
		t.Fatal("an explicit Stop left a recovery able to restart the conversation")
	}
}

func TestAssistantFallbackNeverKillsUnknownOrRunningBackgroundTasks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		observed bool
		tasks    []string
	}{
		{"unknown inventory", false, nil},
		{"running task", true, []string{"deploy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := assistantNode(t)
			const id = "11111111-2222-3333-4444-555555555555"
			p := &assistantProc{id: id, agent: "claude", launcher: "claude", tasksObserved: tc.observed, tasks: tc.tasks}
			n.procs[id] = p
			r := &assistantTurnRecovery{}
			n.recoveries = map[string]*assistantTurnRecovery{id: r}
			_, err := n.assistantSend(withAssistantRecovery(context.Background(), r, true), id, "", "claude", "retry", nil, "", "")
			if !errors.Is(err, errAssistantRecoveryTasks) {
				t.Fatalf("restart error = %v, want %v", err, errAssistantRecoveryTasks)
			}
			if n.procs[id] != p {
				t.Fatal("the old process was stopped without a proven empty background inventory")
			}
		})
	}
}

func TestCodexBackgroundInventoryRequiresAnExplicitDataList(t *testing.T) {
	for _, tc := range []struct {
		name     string
		result   map[string]any
		observed bool
	}{
		{"missing data", map[string]any{}, false},
		{"null data", map[string]any{"data": nil}, false},
		{"explicitly empty", map[string]any{"data": []any{}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			p, c, f := newFakeCodex(t, n, t.TempDir())
			p.id, c.threadID = testThread, testThread
			f.results["thread/backgroundTerminals/list"] = tc.result
			c.syncTasks()
			p.mu.Lock()
			observed, tasks := p.tasksObserved, len(p.tasks)
			p.mu.Unlock()
			if observed != tc.observed || tasks != 0 {
				t.Fatalf("inventory observed=%v tasks=%d, want observed=%v and no tasks", observed, tasks, tc.observed)
			}
		})
	}
}

func TestAssistantHandoverKeepsTheDeliveredRecoveryTurnWorking(t *testing.T) {
	k := newAssistantKeeper()
	a, b := assistantNode(t), assistantNode(t)
	a.keeper, b.keeper = k, k
	const id = "11111111-2222-3333-4444-555555555555"
	p := keptProc(t, a, id, "claude")
	p.busy = true
	r := &assistantTurnRecovery{retried: map[string]bool{"primary-model": true}, used: map[string]bool{"primary-model": true, "resolved-primary": true}}
	a.recoveries = map[string]*assistantTurnRecovery{id: r}
	a.handOverAssistants()
	if adopted := k.adopt(b); len(adopted) != 1 || adopted[0] != p {
		t.Fatalf("the running recovery turn was not handed over: %+v", adopted)
	}
	if b.recoveries[id] != r || !r.retried["primary-model"] || !r.used["resolved-primary"] {
		t.Fatal("handover lost the recovery's per-model retry budget")
	}
	p.mu.Lock()
	busy := p.busy
	p.mu.Unlock()
	if !busy {
		t.Fatal("handover marked a delivered, still-running model turn idle")
	}
}

func TestAssistantHandoverResumesPendingFailureWithTheSameBudget(t *testing.T) {
	k := newAssistantKeeper()
	a, b := assistantNode(t), assistantNode(t)
	a.keeper, b.keeper = k, k
	const id = "11111111-2222-3333-4444-555555555555"
	p := keptProc(t, a, id, "claude")
	p.launcher, p.model, p.busy = "claude", modelExecution{Model: "primary-model"}, true
	r := &assistantTurnRecovery{retried: map[string]bool{"primary-model": true}, used: map[string]bool{"primary-model": true}}
	a.recoveries = map[string]*assistantTurnRecovery{id: r}
	a.handOverAssistants()
	if !a.recoverAssistantResult(p, true, "API Error: safeguards flagged") {
		t.Fatal("a result in the handover gap was published instead of queued")
	}

	models := assistantRecoveryModels
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}}}, nil
	}
	send := assistantRecoverySend
	selected := make(chan string, 1)
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		selected <- execution.Model
		return nil
	}
	t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend = models, send })
	if adopted := k.adopt(b); len(adopted) != 1 || adopted[0] != p {
		t.Fatalf("the live conversation was not adopted: %+v", adopted)
	}
	select {
	case got := <-selected:
		t.Fatalf("the carried failure resumed (on %q) before the new connection's session state loaded", got)
	case <-time.After(50 * time.Millisecond):
	}
	b.resumeAdoptedFailures(true) // startState, once the state is in
	select {
	case got := <-selected:
		if got != "fallback-model" {
			t.Fatalf("the queued failure restarted or consumed the wrong budget: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the queued handover failure did not resume on the new Node")
	}
	if b.recoveries[id] != r || !r.retried["primary-model"] {
		t.Fatal("the pending handover lost its original recovery budget")
	}
}

func TestAssistantSuccessBeforeAdoptionDoesNotMigrateRecovery(t *testing.T) {
	k := newAssistantKeeper()
	a, b := assistantNode(t), assistantNode(t)
	a.keeper, b.keeper = k, k
	const id = "11111111-2222-3333-4444-555555555555"
	p := keptProc(t, a, id, "claude")
	p.busy = true
	r := &assistantTurnRecovery{retried: map[string]bool{}, used: map[string]bool{}}
	a.recoveries = map[string]*assistantTurnRecovery{id: r}
	a.handOverAssistants()
	a.clearAssistantRecovery(p, true) // success is recorded, but the result handler has not cleared busy yet
	if adopted := k.adopt(b); len(adopted) != 1 {
		t.Fatalf("the live conversation was not adopted: %+v", adopted)
	}
	p.mu.Lock()
	p.busy = false
	p.mu.Unlock()
	if b.recoveries[id] != nil {
		t.Fatal("adoption migrated a recovery whose successful result had already arrived")
	}
}

func TestAssistantSuccessOnTheOldNodeClearsTheAdoptedRecovery(t *testing.T) {
	k := newAssistantKeeper()
	a, b := assistantNode(t), assistantNode(t)
	a.keeper, b.keeper = k, k
	const id = "11111111-2222-3333-4444-555555555555"
	p := keptProc(t, a, id, "claude")
	p.busy = true
	r := &assistantTurnRecovery{retried: map[string]bool{}, used: map[string]bool{}}
	a.recoveries = map[string]*assistantTurnRecovery{id: r}
	a.handOverAssistants()
	if adopted := k.adopt(b); len(adopted) != 1 {
		t.Fatalf("the live conversation was not adopted: %+v", adopted)
	}
	a.clearAssistantRecovery(p, true) // the result handler read the old owner before adoption
	p.mu.Lock()
	p.busy = false
	p.mu.Unlock()
	if b.recoveries[id] != nil {
		t.Fatal("a successful result on the old owner left the adopted session recovering forever")
	}
}

func TestAssistantResultOnTheOldNodeForwardsAfterAdoption(t *testing.T) {
	k := newAssistantKeeper()
	a, b := assistantNode(t), assistantNode(t)
	a.keeper, b.keeper = k, k
	const id = "11111111-2222-3333-4444-555555555555"
	p := keptProc(t, a, id, "claude")
	p.launcher, p.model, p.busy = "claude", modelExecution{Model: "primary-model"}, true
	r := &assistantTurnRecovery{retried: map[string]bool{"primary-model": true}, used: map[string]bool{"primary-model": true}}
	a.recoveries = map[string]*assistantTurnRecovery{id: r}
	a.handOverAssistants()
	if adopted := k.adopt(b); len(adopted) != 1 {
		t.Fatalf("the live conversation was not adopted: %+v", adopted)
	}

	models := assistantRecoveryModels
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}}}, nil
	}
	send := assistantRecoverySend
	selected := make(chan string, 1)
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		selected <- execution.Model
		return nil
	}
	t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend = models, send })
	if !a.recoverAssistantResult(p, true, "API Error: safeguards flagged") {
		t.Fatal("a result that read the old owner was not forwarded after adoption")
	}
	select {
	case got := <-selected:
		if got != "fallback-model" {
			t.Fatalf("the forwarded result lost its recovery budget: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the old owner's delayed result did not resume on the new Node")
	}
}

func TestAssistantRecoveryCannotRestartOnTheOldNodeAfterHandover(t *testing.T) {
	k := newAssistantKeeper()
	a, b := assistantNode(t), assistantNode(t)
	a.keeper, b.keeper = k, k
	const id = "11111111-2222-3333-4444-555555555555"
	p := keptProc(t, a, id, "claude")
	p.launcher, p.model = "claude", modelExecution{Model: "primary-model"}
	r := &assistantTurnRecovery{retried: map[string]bool{}, used: map[string]bool{"primary-model": true}}
	a.recoveries = map[string]*assistantTurnRecovery{id: r}

	stubHandoverRecovery(t, a, b)
	entered, release, handed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	releaseSend := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(releaseSend)
	var spawned atomic.Int32
	assistantRecoverySend = func(_ context.Context, n *Node, p *assistantProc, _, _ string, _ modelExecution) error {
		close(entered)
		<-release // the connection starts handing over before this send enters assistantSend's session lock
		defer n.typing.lock(p.id)()
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.recoveries[p.id] == r && n.procs[p.id] == nil {
			n.procs[p.id] = &assistantProc{id: p.id, agent: "claude", launcher: "claude"}
			spawned.Add(1)
		}
		return nil
	}
	if !a.recoverAssistantResult(p, true, "API Error: safeguards flagged") {
		t.Fatal("the failed turn did not enter recovery")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery did not reach the send")
	}
	go func() {
		a.handOverAssistants()
		close(handed)
	}()
	select {
	case <-handed: // an implementation that does not wait for the active send
	case <-time.After(50 * time.Millisecond):
	}
	releaseSend()
	select {
	case <-handed:
	case <-time.After(2 * time.Second):
		t.Fatal("handover did not finish after the active send returned")
	}
	// Adoption clears the old recovery map. Join the sender first so it cannot hide an old-node restart.
	waitHandoverRecovery(t, a, r)
	if adopted := k.adopt(b); len(adopted) != 1 || adopted[0] != p {
		t.Fatalf("the live conversation was not handed over: %+v", adopted)
	}
	if spawned.Load() != 0 {
		t.Fatal("the ended connection's Node started a replacement process after the new Node took over")
	}
	a.mu.Lock()
	left := len(a.procs)
	a.mu.Unlock()
	if left != 0 {
		t.Fatal("the old Node retained a process after handover")
	}
	if b.recoveries[id] != r {
		t.Fatal("the new Node did not retain the handed-over recovery")
	}
}

func TestAssistantPendingFailureUsesTheReplacementProcess(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	old := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "primary-model"}, tasksObserved: true}
	n.procs[id] = old
	r := &assistantTurnRecovery{retried: map[string]bool{"primary-model": true}, used: map[string]bool{"primary-model": true}, running: true}
	n.recoveries = map[string]*assistantTurnRecovery{id: r}

	models := assistantRecoveryModels
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}, {ID: "second-fallback"}}}, nil
	}
	send := assistantRecoverySend
	var calls atomic.Int32
	assistantRecoverySend = func(_ context.Context, n *Node, p *assistantProc, _, _ string, execution modelExecution) error {
		call := calls.Add(1)
		if call == 1 {
			replacement := &assistantProc{id: p.id, agent: "claude", launcher: p.launcher, model: execution, busy: true, tasksObserved: true}
			n.mu.Lock()
			n.procs[p.id] = replacement
			n.mu.Unlock()
			// The replacement's first result arrives before its recovery send has returned.
			n.recoverAssistantResult(replacement, true, "API Error: safeguards flagged")
		}
		return nil
	}
	t.Cleanup(func() {
		waitWithin(t, 2*time.Second, "recovery workers finish", func() bool { n.mu.Lock(); defer n.mu.Unlock(); r := n.recoveries[id]; return r == nil || !r.running })
		assistantRecoveryModels, assistantRecoverySend = models, send
	})
	n.runAssistantRecovery(old, r, "claude", old.cwd, old.model, "primary-model", false)
	waitWithin(t, 2*time.Second, "the replacement's recovery send", func() bool { return calls.Load() >= 2 })
	if got := calls.Load(); got != 2 {
		t.Fatalf("the replacement's pending failure was dropped: %d recovery send(s), want 2", got)
	}
}

// A task's explicit execution choice keeps the same-model retry, then surfaces the error without a model switch.
func TestTaskExecutionEachTurnCanRetryWithoutModelFallback(t *testing.T) {
	f := cliTest(t)
	retryResult := filepath.Join(f.dir, "retry-result")
	t.Setenv("CLI_FAKE_RETRY_RESULT_GATE", retryResult)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+claudeLikeCLI), 0700); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	execution := modelExecution{Strict: true}
	var id string
	var process *assistantProc
	for turn := 1; turn <= 3; turn++ {
		// Every turn and its retry receive the same transient failure from the fake process.
		if err := os.WriteFile(filepath.Join(f.dir, "attempts"), []byte("0\n"), 0600); err != nil {
			t.Fatal(err)
		}
		next, err := f.n.assistantSend(t.Context(), id, cwd, "claude", "task request", nil, "", "", execution)
		if err != nil || (id != "" && next != id) {
			t.Fatalf("send turn %d: previous=%q next=%q error=%v", turn, id, next, err)
		}
		id = next
		cliWait(t, "the same-model retry", func() bool {
			return strings.Count(f.file("user.jsonl"), `"type":"user"`) >= 2*turn
		})
		// Exercise result handling after the recovery send returns. Early results take the pending-failure path.
		cliWait(t, "the recovery send to return", func() bool {
			f.n.mu.Lock()
			defer f.n.mu.Unlock()
			recovery := f.n.recoveries[id]
			return recovery != nil && !recovery.running
		})
		if err := os.WriteFile(retryResult, nil, 0600); err != nil {
			t.Fatal(err)
		}
		f.idle(t, id)
		if err := os.Remove(retryResult); err != nil {
			t.Fatal(err)
		}
		if count := strings.Count(f.file("user.jsonl"), `"type":"user"`); count != 2*turn {
			t.Fatalf("turn %d ended after %d total attempts, want %d", turn, count, 2*turn)
		}
		p := f.proc(id)
		if p == nil {
			t.Fatalf("turn %d lost its process", turn)
		}
		if turn == 1 {
			process = p
		} else if p != process {
			t.Fatalf("turn %d replaced the process", turn)
		}
		p.mu.Lock()
		model, failure, recovery := p.model, p.turnError, p.turnRecovery
		p.mu.Unlock()
		if !model.Strict || model.Model == "fallback-model" || !strings.Contains(failure, "503") {
			t.Errorf("turn %d ended with model %+v and error %q", turn, model, failure)
		}
		if recovery != nil {
			t.Errorf("turn %d left its exhausted recovery on the idle process", turn)
		}
		rows := f.n.assistantSessions()
		if len(rows) != 1 || rows[0].SessionID != id || rows[0].TurnErrorKind != proto.TurnErrorUser || !strings.Contains(rows[0].TurnError, "503") {
			t.Fatalf("turn %d exposed a failure for generic continuation: %+v", turn, rows)
		}
	}
	if strings.Contains(f.file("cli-argv"), "--model fallback-model") {
		t.Fatal("task silently changed models")
	}
	ctx := withClientMsgID(t.Context(), "model-retry:"+id+":1:1")
	if _, err := f.n.assistantSend(ctx, id, "", "", "Continue", nil, "", ""); !errors.Is(err, errAssistantRecoveryCancelled) {
		t.Fatalf("stale manager continuation was not refused: %v", err)
	}
	if count := strings.Count(f.file("user.jsonl"), `"type":"user"`); count != 6 {
		t.Fatalf("stale manager continuation reached the conversation: %d total sends", count)
	}
}
