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

// claudeLikeCLI behaves as Claude Code 2.1.311 does on the seams recovery uses: no background_tasks_changed at start,
// a control response with the request's own id, and the current task set (empty) only behind a repeated initialize.
// The launcher's model list is a separate process asked with request_id persona-models.
const claudeLikeCLI = `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init"}'
model=primary-model
case "$*" in *'--model fallback-model'*) model=fallback-model ;; esac
inits=0
while IFS= read -r line; do
  case "$line" in
    *'"subtype":"initialize"'*)
      rid=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      if [ "$rid" = persona-models ]; then
        echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"default","displayName":"Default"},{"value":"primary-model","resolvedModel":"primary-model"},{"value":"fallback-model","resolvedModel":"fallback-model"}]}}}'
      else
        inits=$((inits+1))
        printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}\n' "$rid"
        if [ "$inits" -ge 2 ]; then
          echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
        fi
      fi ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
      count=$(cat "$CLI_FAKE_DIR/attempts" 2>/dev/null || echo 0)
      count=$((count+1))
      echo "$count" > "$CLI_FAKE_DIR/attempts"
      if [ "$count" -eq 2 ] && [ -n "$CLI_FAKE_RETRY_RESULT_GATE" ]; then
        while [ ! -f "$CLI_FAKE_RETRY_RESULT_GATE" ]; do sleep 0.01; done
      fi
      printf '{"type":"stream_event","event":{"type":"message_start","message":{"model":"%s","usage":{"input_tokens":5}}},"parent_tool_use_id":null}\n' "$model"
      if [ -n "$CLI_FAKE_RETRY_RESULT_GATE" ] && [ "$count" -le 2 ]; then
        echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 503 no eligible upstream account is currently available"}'
      elif [ "$count" -le 1 ]; then
        echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: safeguards flagged"}'
      else
        echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"answered by fallback"}},"parent_tool_use_id":null}'
        echo '{"type":"result","subtype":"success","is_error":false,"result":"answered by fallback"}'
      fi ;;
  esac
done
`

// A Claude conversation that never ran a background task still moves to the next model: Claude Code reports its task
// set only when it changes, so the node asks for it before the restart (#3495).
func TestAssistantFallbackAsksClaudeForItsTasks(t *testing.T) {
	f := cliTest(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+claudeLikeCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "original request", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	cliWait(t, "the second refusal to resume on the next launcher model", func() bool {
		return strings.Count(f.file("user.jsonl"), `"type":"user"`) == 2
	})
	f.idle(t, id)
	if p := f.proc(id); p == nil || p.model.Model != "fallback-model" {
		t.Fatalf("the next model did not take the turn: %+v", p)
	}
	if got := f.file("cli-argv"); !strings.Contains(got, "--model fallback-model") {
		t.Fatalf("no process was started on the fallback model: %s", got)
	}
}

// A user's message while a delivered recovery turn answers must not find the process idle: it would restart it
// mid-answer. Only a recovery that still holds the failed turn gives its busy back (#3495).
func TestAssistantRecoveryCancelKeepsADeliveredTurnBusy(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		name     string
		recovery *assistantTurnRecovery
		busy     bool
	}{
		{"delivered and answering", &assistantTurnRecovery{retried: map[string]bool{}, used: map[string]bool{}}, true},
		{"still sending", &assistantTurnRecovery{running: true}, false},
		{"carried, not resumed yet", &assistantTurnRecovery{pending: &assistantPendingFailure{text: "API Error: 503"}}, false},
	} {
		n := assistantNode(t)
		p := &assistantProc{id: id, busy: true}
		n.procs[id] = p
		n.recoveries = map[string]*assistantTurnRecovery{id: tc.recovery}
		n.cancelAssistantRecovery(id)
		p.mu.Lock()
		busy := p.busy
		p.mu.Unlock()
		if busy != tc.busy {
			t.Errorf("%s: busy %v after a newer message, want %v", tc.name, busy, tc.busy)
		}
		if n.recoveries[id] != nil {
			t.Errorf("%s: the newer message did not cancel the recovery", tc.name)
		}
	}
}

// A failure a handover carried goes on only once the session state is in, and one that cannot go on, or comes to a
// connection whose state did not load, ends with its idle state instead of keeping the conversation Working (#3495).
func TestAssistantAdoptedFailureEndsWhenItCannotResume(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		name   string
		loaded bool
		manual bool
	}{
		{"state not loaded", false, false},
		{"refused (a manual compaction)", true, true},
	} {
		n := assistantNode(t)
		p := &assistantProc{id: id, agent: "claude", busy: true, manual: tc.manual}
		n.procs[id] = p
		failure := &assistantPendingFailure{text: "API Error: 503 no eligible upstream account is currently available"}
		r := &assistantTurnRecovery{retried: map[string]bool{}, used: map[string]bool{}, pending: failure, proc: p}
		n.recoveries = map[string]*assistantTurnRecovery{id: r}
		n.adoptedFailures = []adoptedFailure{{p, r, failure}}
		n.resumeAdoptedFailures(tc.loaded)
		p.mu.Lock()
		busy := p.busy
		p.mu.Unlock()
		if busy || n.recoveries[id] != nil {
			t.Errorf("%s: the conversation stays Working (busy %v, recovery %v)", tc.name, busy, n.recoveries[id] != nil)
		}
	}
}

// A model switch whose new start is refused leaves the conversation its live process: it stops only once the new start
// is checked (#3495).
func TestAssistantFallbackRefusedKeepsTheProcess(t *testing.T) {
	n := codexTestNode(t)
	withFakeHub(t, n)
	const id = "11111111-2222-3333-4444-555555555555"
	cwd := t.TempDir()
	old := keptProc(t, n, id, "claude")
	old.launcher, old.cwd, old.tasksObserved = "claude", cwd, true
	r := &assistantTurnRecovery{retried: map[string]bool{}, used: map[string]bool{}, running: true}
	n.recoveries = map[string]*assistantTurnRecovery{id: r}
	_, err := n.assistantSend(withAssistantRecovery(context.Background(), r, true), id, cwd, "claude", "retry", nil, "not-a-mode", "",
		modelExecution{Model: "fallback-model"})
	if err == nil {
		t.Fatal("an invalid start was accepted")
	}
	n.mu.Lock()
	current := n.procs[id]
	n.mu.Unlock()
	if current != old || !old.alive() {
		t.Fatal("the refused switch stopped the conversation's process")
	}
}

// The structured kind wins over the words, and a 4xx other than 429 is never replayed (#3495).
func TestAssistantRecoverableKinds(t *testing.T) {
	for _, tc := range []struct {
		text  string
		kinds []string
		want  bool
	}{
		{"API Error: 503 no eligible upstream", nil, true},
		{"API Error: 429 Too Many Requests", nil, true},
		{"API Error: Rate limit reached", nil, true},
		{"API Error: 403 Forbidden: upstream 502", nil, false},
		{"API Error: 401 Unauthorized, retry in 500 ms", nil, false},
		{"API Error: Rate limit reached", []string{proto.TurnErrorUsageLimit}, false},
		{"unexpected status 403 Forbidden: upstream 502", []string{proto.TurnErrorUser}, false},
		{"stream failed", []string{proto.TurnErrorUser, assistantModelServiceFailure}, false},
		{"API Error: safeguards flagged this message", []string{proto.TurnErrorUser}, true},
		{"stream failed", []string{"", assistantModelServiceFailure}, true},
	} {
		if got := assistantRecoverable(true, tc.text, tc.kinds); got != tc.want {
			t.Errorf("%q %v: recoverable %v, want %v", tc.text, tc.kinds, got, tc.want)
		}
	}
}

// A Claude usage limit is told by the transcript, not the result's words (#3495).
func TestClaudeResultKindsReadTheTranscript(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	line := func(extra string) string {
		return `{"type":"assistant","uuid":"e","timestamp":"2026-01-01T12:00:00Z","isSidechain":false,"isApiErrorMessage":true,` + extra +
			`"message":{"id":"m-e","model":"<synthetic>","role":"assistant","usage":{"input_tokens":0,"output_tokens":0},` +
			`"content":[{"type":"text","text":"API Error: Rate limit reached"}]}}` + "\n"
	}
	for _, tc := range []struct {
		name, extra, want string
	}{
		{"subscription limit", `"error":"rate_limit","apiErrorStatus":429,"quotaLimits":{"status":"rejected","resetsAt":1767284400},`, proto.TurnErrorUsageLimit},
		{"authentication", `"error":"authentication_failed","apiErrorStatus":401,`, proto.TurnErrorUser},
		{"throttled", `"error":"rate_limit","apiErrorStatus":429,`, ""},
	} {
		n := assistantNode(t)
		path := filepath.Join(t.TempDir(), id+".jsonl")
		if err := os.WriteFile(path, []byte(line(tc.extra)), 0o600); err != nil {
			t.Fatal(err)
		}
		n.paths[id] = path
		got := ""
		if kinds := n.claudeResultKinds(id); len(kinds) > 0 {
			got = kinds[0]
		}
		if got != tc.want {
			t.Errorf("%s: kind %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A default that failed before naming its model is never selected again under another alias (#3495).
func TestAssistantFallbackMarksTheResolvedDefaultUsed(t *testing.T) {
	list := proto.LauncherModels{Models: []proto.AgentModel{{ID: "default", Default: true, Resolved: "sonnet-x"}, {ID: "opus"},
		{ID: "sonnet", Resolved: "sonnet-x"}}}
	models, send := assistantRecoveryModels, assistantRecoverySend
	selected := ""
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) { return list, nil }
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		selected = execution.Model
		return nil
	}
	t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend = models, send })
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	p := &assistantProc{id: id, agent: "claude"}
	n.procs[id] = p
	r := &assistantTurnRecovery{retried: map[string]bool{"": true}, used: map[string]bool{}, running: true}
	n.recoveries = map[string]*assistantTurnRecovery{id: r}
	n.runAssistantRecovery(p, r, "claude", "", modelExecution{}, "", false)
	if selected != "opus" {
		t.Fatalf("selected %q, want opus", selected)
	}
	if next, ok := nextAssistantModel(list, "opus", r.used, assistantFit{}); ok {
		t.Fatalf("the failed default is selected again as %q", next)
	}
}

// Stop cancels a recovery that has not started sending at once, not after the send in flight (#3495).
func TestAssistantInterruptCancelsBeforeWaiting(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	n.recoveries = map[string]*assistantTurnRecovery{id: {running: true}}
	unlock := n.typing.lock(id) // another send holds the conversation
	done := make(chan struct{})
	go func() {
		_ = n.assistantInterrupt(id)
		close(done)
	}()
	waitWithin(t, time.Second, "Stop cancels the recovery before the send lock is released", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.recoveries[id] == nil
	})
	unlock()
	<-done
}

// A turn's first failure while the connection hands over is carried to the next one, not published as final (#3495).
func TestAssistantFirstFailureInTheHandoverGapIsCarried(t *testing.T) {
	k := newAssistantKeeper()
	a, b := assistantNode(t), assistantNode(t)
	a.keeper, b.keeper = k, k
	const id = "11111111-2222-3333-4444-555555555555"
	p := keptProc(t, a, id, "claude")
	p.launcher, p.model, p.busy = "claude", modelExecution{Model: "primary-model"}, true
	a.handOverAssistants()
	if !a.recoverAssistantResult(p, true, "API Error: safeguards flagged") {
		t.Fatal("the first failure in the handover gap was published as final")
	}
	stubHandoverRecovery(t, a, b)
	selected := make(chan string, 1)
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		selected <- execution.Model
		return nil
	}
	if adopted := k.adopt(b); len(adopted) != 1 || adopted[0] != p {
		t.Fatalf("the live conversation was not adopted: %+v", adopted)
	}
	b.resumeAdoptedFailures(true)
	select {
	case got := <-selected:
		if got != "fallback-model" {
			t.Fatalf("the carried safeguards failure did not select the fixture's fallback: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the carried first failure did not resume on the new Node")
	}
}

// A Claude safeguards refusal goes straight to the next model: Claude Code has already continued once on the same
// one. A Codex refusal still gets its same-model retry (#4283).
func TestAssistantClaudeRefusalSwitchesAtOnce(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	models, send := assistantRecoveryModels, assistantRecoverySend
	t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend = models, send })
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}}}, nil
	}
	for _, agent := range []string{"claude", "codex"} {
		type sent struct {
			model   string
			restart bool
		}
		got := make(chan sent, 1)
		assistantRecoverySend = func(ctx context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
			_, restart := assistantRecoveryOf(ctx)
			got <- sent{execution.Model, restart}
			return nil
		}
		n := assistantNode(t)
		p := &assistantProc{id: id, agent: agent, launcher: agent, model: modelExecution{Model: "primary-model"}}
		n.procs[id] = p
		if !n.recoverAssistantResult(p, true, "API Error: Opus 5.5's safeguards flagged this message") {
			t.Fatalf("%s: the refusal was not recovered", agent)
		}
		select {
		case s := <-got:
			if switched := agent == "claude"; s.restart != switched || switched && s.model != "fallback-model" {
				t.Errorf("%s: recovery sent on %q (restart %v)", agent, s.model, s.restart)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no recovery send", agent)
		}
	}
}

// A fallback the gateway does not have is the recovery's own pick: it is spent and the next model takes the turn. The
// same failure on the model the user asked for stays theirs (#4283).
func TestAssistantUnavailableFallbackMovesOn(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	const missing = "There's an issue with the selected model (ccone/claude-fable-5-2). It may not exist or you may not have access to it."
	models, send := assistantRecoveryModels, assistantRecoverySend
	t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend = models, send })
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{{ID: "default", Default: true, Resolved: "claude-opus-5-5"},
			{ID: "fable", Resolved: "ccone/claude-fable-5-2"}, {ID: "haiku", Resolved: "gemini-3.8-flash-tiered"}}}, nil
	}
	got := make(chan string, 1)
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		got <- execution.Model
		return nil
	}
	n := assistantNode(t)
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "fable"}}
	n.procs[id] = p
	n.recoveries = map[string]*assistantTurnRecovery{id: {retried: map[string]bool{"": true}, switched: true,
		used: map[string]bool{"claude-opus-5-5": true, "fable": true}}}
	if !n.recoverAssistantResult(p, true, missing, proto.TurnErrorUser, assistantRecordKind) {
		t.Fatal("an unavailable fallback ended the recovery")
	}
	select {
	case m := <-got:
		if m != "haiku" {
			t.Fatalf("the chain went on to %q, want haiku", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no recovery send after the unavailable fallback")
	}
	own := assistantNode(t)
	q := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "fable"}}
	own.procs[id] = q
	if own.recoverAssistantResult(q, true, missing, proto.TurnErrorUser, assistantRecordKind) {
		t.Fatal("a model the user asked for that does not exist was replayed")
	}
}

// A fallback the gateway does not serve is remembered for its launcher: the next chain, in this conversation or
// another, does not pick it again until it expires (#5234).
func TestAssistantUnavailableFallbackIsRemembered(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	const missing = "There's an issue with the selected model (ccone/claude-fable-5-2). It may not exist or you may not have access to it."
	list := proto.LauncherModels{Models: []proto.AgentModel{{ID: "default", Default: true, Resolved: "claude-opus-5-5"},
		{ID: "fable", Resolved: "ccone/claude-fable-5-2"}, {ID: "haiku", Resolved: "gemini-3.8-flash-tiered"}}}
	models, send := assistantRecoveryModels, assistantRecoverySend
	t.Cleanup(func() { assistantRecoveryModels, assistantRecoverySend = models, send })
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) { return list, nil }
	got := make(chan string, 4)
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		got <- execution.Model
		return nil
	}
	n := assistantNode(t)
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "fable"}}
	n.procs[id] = p
	n.recoveries = map[string]*assistantTurnRecovery{id: {retried: map[string]bool{"": true}, switched: true, launcher: "claude",
		used: map[string]bool{"claude-opus-5-5": true, "fable": true}}}
	if !n.recoverAssistantResult(p, true, missing, proto.TurnErrorUser, assistantRecordKind) {
		t.Fatal("an unavailable fallback ended the recovery")
	}
	if m := <-got; m != "haiku" {
		t.Fatalf("went on to %q, want haiku", m)
	}

	// The next message's chain, from the configured model: refused on it, it must not offer fable again.
	const other = "22222222-3333-4444-5555-666666666666"
	q := &assistantProc{id: other, agent: "claude", launcher: "claude", model: modelExecution{}}
	n.mu.Lock()
	n.procs[other] = q
	n.mu.Unlock()
	if !n.recoverAssistantResult(q, true, "API Error: Opus 5.5's safeguards flagged this message") {
		t.Fatal("the refusal was not recovered")
	}
	if m := <-got; m != "haiku" {
		t.Fatalf("a later chain picked %q, want haiku (fable is unavailable)", m)
	}

	// Once it expires the model is offered again.
	n.mu.Lock()
	for k := range n.unavailable {
		n.unavailable[k] = time.Now().Add(-time.Minute)
	}
	used := map[string]bool{}
	n.markUnavailableUsed("claude", used)
	n.mu.Unlock()
	if len(used) != 0 {
		t.Fatalf("an expired unavailable model is still skipped: %v", used)
	}
}

// A model that answered here within the hour and now "does not exist" is the gateway's passing failure: the turn is
// recovered. One that never answered here stays the user's to fix (#5234).
func TestAssistantTransient404OnAModelThatJustAnswered(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	const gone = "There's an issue with the selected model (claude-opus-5-5). It may not exist or you may not have access to it."
	send := assistantRecoverySend
	t.Cleanup(func() { assistantRecoverySend = send })
	got := make(chan string, 1)
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		got <- execution.Model
		return nil
	}
	for _, c := range []struct {
		name      string
		repliedAt time.Time
		want      bool
	}{
		{"answered two minutes ago", time.Now().Add(-2 * time.Minute), true},
		{"answered yesterday", time.Now().Add(-26 * time.Hour), false},
		{"never answered here", time.Time{}, false},
	} {
		n := assistantNode(t)
		p := &assistantProc{id: id, agent: "claude", launcher: "claude", model: modelExecution{Model: "claude-opus-5-5"}}
		if !c.repliedAt.IsZero() {
			p.replyModel, p.repliedAt = "claude-opus-5-5", c.repliedAt
		}
		n.procs[id] = p
		if got := n.recoverAssistantResult(p, true, gone, proto.TurnErrorUser, assistantRecordKind); got != c.want {
			t.Errorf("%s: recovered %v, want %v", c.name, got, c.want)
			continue
		}
		if c.want {
			select {
			case m := <-got:
				if m != "claude-opus-5-5" {
					t.Errorf("%s: retried on %q, want the same model first", c.name, m)
				}
			case <-time.After(2 * time.Second):
				t.Errorf("%s: no retry", c.name)
			}
		}
	}
}
