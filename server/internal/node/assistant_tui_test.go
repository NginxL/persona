package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The transcript an interactive Claude Code writes reads as what claude -p prints: one init as a turn starts (also a
// turn it starts on its own), one message_start per message with its usage, the text a block at a time, the result at
// turn_duration (is_error after an API error), at an interruption, and nothing for a subagent's lines.
func TestTUITranslatorReadsATranscriptAsStreamJSON(t *testing.T) {
	rows := []string{
		`{"type":"permission-mode"}`,
		`{"type":"user","version":"2.1.325","message":{"role":"user","content":"<persona-context>x</persona-context>\n\nhi"}}`,
		`{"type":"assistant","version":"2.1.325","message":{"id":"m1","model":"claude-haiku","content":[{"type":"thinking","thinking":""}],"usage":{"input_tokens":2,"cache_read_input_tokens":100,"cache_creation_input_tokens":5}}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"Hello"}]}}`,
		`{"type":"assistant","isSidechain":true,"message":{"id":"s1","content":[{"type":"text","text":"sub"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"system","subtype":"turn_duration","durationMs":10}`,
		`{"type":"system","subtype":"turn_duration","durationMs":10}`,
		`{"type":"user","message":{"content":"<task-notification>done</task-notification>"}}`,
		`{"type":"assistant","isApiErrorMessage":true,"message":{"id":"m2","content":[{"type":"text","text":"API Error: 429"}]}}`,
		`{"type":"system","subtype":"turn_duration"}`,
		`{"type":"user","message":{"content":"again"}}`,
		`{"type":"system","subtype":"compact_boundary","compactMetadata":{"postTokens":1234}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
	}
	tr := newTUITranslator()
	var got []string
	for _, r := range rows {
		for _, ev := range tr.row([]byte(r)) {
			b, _ := json.Marshal(ev)
			got = append(got, string(b))
		}
	}
	want := []string{
		`{"claude_code_version":"2.1.325","subtype":"init","type":"system"}`,
		`{"event":{"message":{"model":"claude-haiku","usage":{"cache_creation_input_tokens":5,"cache_read_input_tokens":100,"input_tokens":2}},"type":"message_start"},"type":"stream_event"}`,
		`{"event":{"delta":{"text":"Hello","type":"text_delta"},"type":"content_block_delta"},"type":"stream_event"}`,
		`{"event":{"type":"message_stop"},"type":"stream_event"}`,
		`{"is_error":false,"result":"Hello","subtype":"success","type":"result"}`,
		`{"claude_code_version":"2.1.325","subtype":"init","type":"system"}`,
		`{"event":{"message":{"model":"","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":0}},"type":"message_start"},"type":"stream_event"}`,
		`{"event":{"delta":{"text":"API Error: 429","type":"text_delta"},"type":"content_block_delta"},"type":"stream_event"}`,
		`{"event":{"type":"message_stop"},"type":"stream_event"}`,
		`{"is_error":true,"result":"API Error: 429","subtype":"error_during_execution","type":"result"}`,
		`{"claude_code_version":"2.1.325","subtype":"init","type":"system"}`,
		`{"compact_metadata":{"post_tokens":1234},"subtype":"compact_boundary","type":"system"}`,
		`{"is_error":false,"result":"Request interrupted by user","subtype":"success","type":"result"}`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ev := tr.idle(); ev != nil {
		t.Fatalf("an idle screen after the turn ended ended it again: %v", ev)
	}
}

// An interactive start goes without claude -p's flags and takes its permission mode and effort as flags.
func TestTUIArgsDropPrintFlagsAndCarryTheStartModes(t *testing.T) {
	print := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--append-system-prompt", "be kind", "--settings", "{}", "--session-id", "s1"}
	full := withStartModes(print, "acceptEdits", "high")
	got := tuiArgs(full)
	want := []string{"--append-system-prompt", "be kind", "--settings", "{}", "--session-id", "s1",
		"--permission-mode", "acceptEdits", "--effort", "high"}
	if !slices.Equal(got, want) {
		t.Fatalf("tui args %q", got)
	}
	if !slices.Contains(full, "-p") || !slices.Contains(full, "--effort") {
		t.Fatalf("the print-mode fallback lost its flags or modes: %q", full)
	}
	if got := withStartModes([]string{"--permission-mode", "bypassPermissions"}, "bypassPermissions", effortAuto); len(got) != 2 {
		t.Fatalf("a mode already given or auto effort was added again: %q", got)
	}
}

// An interactive process takes its permission mode and effort at start only: another one restarts it, as
// bypassPermissions does for claude -p. One whose bridge fell back to claude -p takes control requests again.
func TestTUIProcessRestartsForModeAndEffort(t *testing.T) {
	p := &assistantProc{agent: "claude", tui: true, mode: "bypassPermissions", effort: "high", canBypass: true}
	if p.needsRestartFor("bypassPermissions") || p.needsRestartFor("") {
		t.Fatal("the mode it started in restarts it")
	}
	if !p.needsRestartFor("acceptEdits") {
		t.Fatal("another mode does not restart it")
	}
	p.mu.Lock()
	same, other := p.effortNeedsRestartLocked("high"), p.effortNeedsRestartLocked("low")
	p.mu.Unlock()
	if same || !other {
		t.Fatalf("effort restart: same %v other %v", same, other)
	}
	p.tuiNote = "tmux is not installed on this computer"
	if p.needsRestartFor("acceptEdits") {
		t.Fatal("a fallen-back process restarts for a mode claude -p sets by control request")
	}
}

// fakeTUIBridge makes n's own binary a script that records its spec and argv, says the interactive start fell back, and
// then answers each message as claude -p would.
func fakeTUIBridge(t *testing.T, n *Node) (specFile string) {
	t.Helper()
	dir := t.TempDir()
	specFile = filepath.Join(dir, "spec.json")
	script := "#!/bin/sh\necho \"$@\" > " + filepath.Join(dir, "argv") + "\nIFS= read -r spec\nprintf '%s\\n' \"$spec\" > " + specFile + "\n" +
		`echo '{"type":"system","subtype":"persona_tui_fallback","reason":"tmux is not installed on this computer"}'` + "\n" +
		"while IFS= read -r line; do echo '{\"type\":\"system\",\"subtype\":\"init\"}'; echo '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok\"}'; done\n"
	bin := filepath.Join(dir, "personad")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	n.bin = bin
	return specFile
}

func resetTUICooldown(t *testing.T) {
	t.Helper()
	reset := func() {
		tuiCooldown.Lock()
		tuiCooldown.until, tuiCooldown.reason = time.Time{}, ""
		tuiCooldown.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// With claude_interactive a conversation starts through the bridge, which gets both commands; its fallback to
// claude -p is visible in the execution report and holds the next starts in print mode for a while.
func TestInteractiveStartGoesThroughTheBridgeAndItsFallbackIsVisible(t *testing.T) {
	fakeClaudeHome(t, "read line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	resetTUICooldown(t)
	n := codexTestNode(t)
	withFakeHub(t, n)
	specFile := fakeTUIBridge(t, n)
	id, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "", "", modelExecution{Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer n.stopAssistant(id)
	var spec tuiSpec
	waitFor(t, "the bridge's spec", func() bool {
		raw, _ := os.ReadFile(specFile)
		return json.Unmarshal(bytes.TrimSpace(raw), &spec) == nil
	})
	if spec.Session != id || len(spec.TUI) == 0 || len(spec.Print) == 0 {
		t.Fatalf("spec %+v", spec)
	}
	joined := func(a []string) string { return " " + strings.Join(a, " ") + " " }
	if strings.Contains(joined(spec.TUI), " -p ") || strings.Contains(joined(spec.TUI), " stream-json ") {
		t.Fatalf("the interactive command keeps print flags: %q", spec.TUI)
	}
	if !strings.Contains(joined(spec.Print), " -p ") || !strings.Contains(joined(spec.TUI), " --session-id "+id+" ") {
		t.Fatalf("commands: tui %q print %q", spec.TUI, spec.Print)
	}
	var exec proto.AssistantExecution
	waitFor(t, "the print-mode execution report", func() bool {
		exec = n.assistantExecution(context.Background(), id)
		return exec.Mode == executionModePrint
	})
	if exec.Mode != executionModePrint || !strings.Contains(exec.ModeNote, "tmux is not installed") {
		t.Fatalf("the fallback is not in the execution report: %+v", exec)
	}
	if reason, cooling := tuiCoolingDown(time.Now()); !cooling || !strings.Contains(reason, "tmux") {
		t.Fatalf("no cooldown after a fallback: %q %v", reason, cooling)
	}
	if want, why := tuiWanted(modelExecution{Interactive: true}, false, false, spawnRound{}, ""); want || why == "" {
		t.Fatalf("a start during the cooldown: %v %q", want, why)
	}
}

// Without the setting nothing changes: claude -p itself, never the bridge.
func TestPrintModeStartNeverRunsTheBridge(t *testing.T) {
	fakeClaudeHome(t, "read line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	resetTUICooldown(t)
	n := codexTestNode(t)
	withFakeHub(t, n)
	specFile := fakeTUIBridge(t, n)
	id, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer n.stopAssistant(id)
	if _, err := os.Stat(specFile); err == nil {
		t.Fatal("the bridge ran without claude_interactive")
	}
	if exec := n.assistantExecution(context.Background(), id); exec.Mode != "" {
		t.Fatalf("a print-mode conversation reports a mode: %+v", exec)
	}
}

// A bridge whose interactive Claude Code cannot start (here: no tmux on PATH) says why first, then hands what it was
// sent to the print-mode command and is that command from then on: its output, its exit status.
func TestBridgeFallsBackToPrintModeVisibly(t *testing.T) {
	dir := t.TempDir()
	printer := filepath.Join(dir, "print")
	// claude -p stand-in: echoes each line back and exits 7 when stdin ends
	if err := os.WriteFile(printer, []byte("#!/bin/sh\nwhile IFS= read -r l; do printf 'got %s\\n' \"$l\"; done\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir) // no tmux
	spec, _ := json.Marshal(tuiSpec{Session: "s1", Cwd: dir, TUI: []string{"claude"}, Print: []string{printer}})
	in := string(spec) + "\n" + `{"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	r, w := io.Pipe()
	out, errb := tuiTestOut{closeAt: "got ", closer: w}, tuiTestOut{} // stdin stays open while the start fails, as the node's does
	defer time.AfterFunc(10*time.Second, func() { _ = w.Close() }).Stop()
	go func() { _, _ = io.WriteString(w, in) }()
	status := AssistantTUI(nil, r, &out, &errb)
	if status != 7 {
		t.Fatalf("status %d, stderr %s", status, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], tuiFallbackSubtype) || !strings.Contains(lines[0], "tmux is not installed") ||
		lines[1] != `got {"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}` {
		t.Fatalf("output:\n%s", out.String())
	}
	if !strings.Contains(errb.String(), "running claude -p instead") {
		t.Fatalf("stderr: %s", errb.String())
	}
}

// What is typed for a message: its texts, an image as a file Claude Code can read, and no mode switch from its first
// character.
func TestTUIMessageText(t *testing.T) {
	dir := t.TempDir()
	text, err := tuiMessageText([]map[string]any{
		{"type": "text", "text": "!rm -rf is not a command here"},
		{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "aGk="}},
	}, dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "image-1-1.png")
	if text != " !rm -rf is not a command here\n\n[Image attached: "+path+"]" {
		t.Fatalf("text %q", text)
	}
	if b, _ := os.ReadFile(path); string(b) != "hi" {
		t.Fatalf("image %q", b)
	}
}

// Background work is read off the mode line, so the node keeps a process whose shell still runs.
func TestBackgroundTasksFromTheScreen(t *testing.T) {
	plain := "  ◐ Bash: sleep 40\n  ⏵⏵ bypass permissions on · 1 shell · ← 2 agents\n"
	if got := backgroundTasks(plain); !slices.Equal(got, []string{"Bash: sleep 40"}) {
		t.Fatalf("tasks %q", got)
	}
	if got := backgroundTasks("  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"); len(got) != 0 {
		t.Fatalf("tasks on an idle screen %q", got)
	}
	if !tuiSpinnerRe.MatchString("· Zesting…\n") || tuiSpinnerRe.MatchString("✻ Brewed for 4s · done 2:14 AM\n") {
		t.Fatal("spinner")
	}
}

// The /compact a compaction writes has a plain string for content (assistant_compact.go, assistant_reap.go); it is
// typed like any message, never dropped, and a line that is no user message is refused.
func TestTUIUserContentTakesStringAndBlocks(t *testing.T) {
	for _, line := range []string{
		`{"type":"user","message":{"role":"user","content":"/compact"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"/compact"}]}}`,
	} {
		content, ok := tuiUserContent([]byte(line))
		if !ok {
			t.Fatalf("%s: not a user message", line)
		}
		if text, err := tuiMessageText(content, t.TempDir(), 1); err != nil || text != "/compact" {
			t.Fatalf("%s: typed %q %v", line, text, err)
		}
	}
	for _, line := range []string{`{"type":"control_request"}`, `{"type":"user","message":{"content":42}}`, `not json`} {
		if _, ok := tuiUserContent([]byte(line)); ok {
			t.Fatalf("%s taken as a user message", line)
		}
	}
}

// A message the bridge cannot type (here: an image that is no base64) fails its turn with why, and the messages already
// waiting behind it fail with it rather than reach Claude Code out of order. One that cannot be typed while a turn
// runs fails that turn as it ends.
func TestUndeliveredMessagesFailTheirTurn(t *testing.T) {
	var out, errb bytes.Buffer
	b := &tuiBridge{out: &tuiOut{w: &out}, stderr: &errb, dir: t.TempDir(), tr: newTUITranslator()}
	var typedMu sync.Mutex
	var typed []string
	typedCount := func() int { typedMu.Lock(); defer typedMu.Unlock(); return len(typed) }
	b.typeText = func(_ context.Context, text string) error {
		typedMu.Lock()
		defer typedMu.Unlock()
		typed = append(typed, text)
		return errors.New("the input box did not take a message")
	}
	inputs := make(chan []byte, 4)
	inputs <- []byte(`{"type":"user","message":{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"%%%"}}]}}`)
	inputs <- []byte(`{"type":"user","message":{"content":"after it"}}`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.submitLoop(ctx, inputs); close(done) }()
	await := func(what string) string {
		t.Helper()
		var s string
		waitFor(t, what, func() bool {
			b.mu.Lock()
			defer b.mu.Unlock()
			s = out.String()
			return strings.Contains(s, what)
		})
		return s
	}
	got := await(`"type":"result"`)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"subtype":"init"`) || !strings.Contains(lines[1], `"is_error":true`) ||
		!strings.Contains(lines[1], "last 2 messages") || !strings.Contains(lines[1], "image") {
		t.Fatalf("output:\n%s", got)
	}
	if typedCount() != 0 {
		t.Fatalf("a message behind the undelivered one was typed: %q", typed)
	}

	// during a turn: no result until the turn ends, then the turn's result is the failure
	b.mu.Lock()
	out.Reset()
	for _, ev := range b.tr.row([]byte(`{"type":"user","message":{"content":"first"}}`)) {
		b.out.emit(ev)
	}
	b.mu.Unlock()
	inputs <- []byte(`{"type":"user","message":{"content":"meanwhile"}}`)
	waitFor(t, "the running turn to hold the undelivered message", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.tr.undelivered != ""
	})
	b.mu.Lock()
	if strings.Contains(out.String(), `"type":"result"`) {
		b.mu.Unlock()
		t.Fatalf("the running turn ended at an undelivered message:\n%s", out.String())
	}
	for _, ev := range b.tr.row([]byte(`{"type":"system","subtype":"turn_duration"}`)) {
		b.out.emit(ev)
	}
	b.mu.Unlock()
	got = await(`"type":"result"`)
	if !strings.Contains(got, `"is_error":true`) || !strings.Contains(got, "did not get your message (the input box did not take a message)") {
		t.Fatalf("output:\n%s", got)
	}
	cancel()
	<-done
}

// A control request that comes while interactive Claude Code starts is answered there and never reaches the print-mode
// command the bridge falls back to; the messages that came meanwhile do.
func TestFallbackForwardsOnlyMessages(t *testing.T) {
	dir := t.TempDir()
	printer := filepath.Join(dir, "print")
	if err := os.WriteFile(printer, []byte("#!/bin/sh\nwhile IFS= read -r l; do printf 'got %s\\n' \"$l\"; done\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// a tmux that starts and then fails every command a second later: the start fails after the lines below came in
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\n/bin/sleep 1\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	spec, _ := json.Marshal(tuiSpec{Session: "s1", Cwd: dir, TUI: []string{"claude"}, Print: []string{printer}})
	control := `{"type":"control_request","request_id":"r1","request":{"subtype":"set_permission_mode","mode":"acceptEdits"}}`
	message := `{"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}`
	r, w := io.Pipe()
	out, errb := tuiTestOut{closeAt: "got ", closer: w}, tuiTestOut{}
	defer time.AfterFunc(10*time.Second, func() { _ = w.Close() }).Stop()
	go func() { _, _ = io.WriteString(w, string(spec)+"\n"+control+"\n"+message+"\n") }()
	if status := AssistantTUI(nil, r, &out, &errb); status != 7 {
		t.Fatalf("status %d, stderr %s", status, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"request_id":"r1"`) || !strings.Contains(lines[1], tuiFallbackSubtype) ||
		lines[2] != "got "+message {
		t.Fatalf("output:\n%s", out.String())
	}
}

// A compaction that starts a stopped conversation starts it as a send would: through the bridge while the hub says
// claude_interactive (assistant.model), not in print mode.
func TestCompactingAStoppedConversationKeepsInteractive(t *testing.T) {
	fakeClaudeHome(t, "read line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	resetTUICooldown(t)
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	cwd := t.TempDir()
	id, err := n.assistantSend(context.Background(), "", cwd, "claude", "hello", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.stopAssistant(id) })
	st, _ := n.state.get(id)
	transcript := filepath.Join(n.assistantConfigPath(st.ConfigDir), "projects", claudeProjectName(cwd), id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.setPath(id, transcript)
	n.stopAssistant(id)
	specFile := fakeTUIBridge(t, n)
	fh.mu.Lock()
	fh.model = map[string]any{"launcher": "claude", "claude_interactive": true}
	fh.mu.Unlock()
	if err := n.assistantCompact(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	var spec tuiSpec
	waitFor(t, "the bridge's spec", func() bool {
		raw, _ := os.ReadFile(specFile)
		return json.Unmarshal(bytes.TrimSpace(raw), &spec) == nil
	})
	if spec.Session != id || !spec.Resume || len(spec.TUI) == 0 {
		t.Fatalf("the compaction's start did not go through the bridge: %+v", spec)
	}
}

// A fallback whose claude -p prints nothing and exits failed to start, as claude -p without the bridge would: the
// bridge's notice is no start, so the send is refused instead of taken.
func TestFallbackThatDiesIsAFailedStart(t *testing.T) {
	fakeClaudeHome(t, "read line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	resetTUICooldown(t)
	n := codexTestNode(t)
	withFakeHub(t, n)
	dir := t.TempDir()
	bin := filepath.Join(dir, "personad")
	script := "#!/bin/sh\nIFS= read -r spec\n" +
		`echo '{"reason":"tmux is not installed on this computer","subtype":"persona_tui_fallback","type":"system"}'` + "\n" +
		"echo 'the launcher refused to start' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	n.bin = bin
	if id, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "", "", modelExecution{Interactive: true}); err == nil {
		n.stopAssistant(id)
		t.Fatal("a fallback that exited at once was taken as a start")
	} else if !strings.Contains(err.Error(), "the launcher refused to start") {
		t.Fatalf("the start error does not say why: %v", err)
	}
}

// A print-mode command that cannot even be run gets no fallback notice: the bridge exits 1 having printed nothing.
func TestFallbackThatCannotRunSaysNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir) // no tmux
	spec, _ := json.Marshal(tuiSpec{Session: "s1", Cwd: dir, TUI: []string{"claude"}, Print: []string{filepath.Join(dir, "missing")}})
	r, w := io.Pipe()
	defer w.Close() // stdin stays open while the start fails, as the node's does
	go func() { _, _ = io.WriteString(w, string(spec)+"\n") }()
	var out, errb tuiTestOut
	if status := AssistantTUI(nil, r, &out, &errb); status != 1 || out.Len() != 0 {
		t.Fatalf("status %d, output %q", status, out.String())
	}
	if !strings.Contains(errb.String(), "start claude -p") {
		t.Fatalf("stderr: %s", errb.String())
	}
}

// tuiTestOut collects what a bridge prints, from its own writes and from the print-mode command's: a bytes.Buffer is
// filled by exec's copy through ReadFrom, which writes over what the bridge emits meanwhile (os.Stdout, a file, is not).
type tuiTestOut struct {
	mu  sync.Mutex
	buf bytes.Buffer
	// once the output holds closeAt, closer is closed: the bridge's stdin, so the print-mode stand-in ends once it
	// answered, and not before (a bridge whose stdin ends before its start failed has nothing to hand over)
	closeAt string
	closer  io.Closer
}

func (o *tuiTestOut) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.buf.Write(p)
	if o.closer != nil && strings.Contains(o.buf.String(), o.closeAt) {
		_ = o.closer.Close()
		o.closer = nil
	}
	return n, err
}

func (o *tuiTestOut) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (o *tuiTestOut) Len() int { return len(o.String()) }

// An assistant's interactive start is often longer than the 16 KB tmux takes for a new session's command (its system
// prompt and tool rules): tmux refused it ("command too long") and every interactive start fell back to claude -p.
func TestALongInteractiveCommandStartsInTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	long := strings.Repeat("a'b ", 10000) // 40 KB, quotes included
	b := &tuiBridge{spec: tuiSpec{Cwd: t.TempDir(),
		TUI: []string{"/bin/sh", "-c", `printf 'persona-long-ok %s\n' "${#1}"; exec sleep 30`, "sh", long}}}
	if err := b.launch(); err != nil {
		t.Fatal(err)
	}
	defer b.shutdown()
	var plain string
	defer func() {
		if t.Failed() {
			t.Logf("screen %q, tmux printed %q", plain, b.clientOut.lines(3))
		}
	}()
	waitWithin(t, 10*time.Second, "the long command to run in tmux", func() bool {
		plain, _, _ = b.screens(context.Background())
		return strings.Contains(plain, "persona-long-ok 40000")
	})
}

// A tmux that refuses to start says why only on its own output: that is the reason the start fails with, the one the
// node's log and the conversation's execution report show, not an empty "exited (status 1)".
func TestATmuxThatRefusesToStartSaysWhy(t *testing.T) {
	dir := t.TempDir()
	fake := "#!/bin/sh\ncase \"$1\" in\n-D) echo 'tmux: unknown option -- D' >&2; exit 1;;\n" +
		"*) echo \"no server running on $2\" >&2; exit 1;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	b := &tuiBridge{spec: tuiSpec{Cwd: dir, TUI: []string{"claude"}}}
	defer b.shutdown()
	err := b.launch()
	if err == nil || !strings.Contains(err.Error(), "tmux exited before Claude Code was ready (status 1): tmux: unknown option -- D") {
		t.Fatalf("start failed with %v", err)
	}
}

// The pane's script runs the command word for word, whatever its arguments hold.
func TestTUIScriptQuotesEveryArgument(t *testing.T) {
	args := []string{"printf", `%s|`, "it's", `$HOME`, "a b", "`id`", "", "line\nbreak"}
	out, err := exec.Command("/bin/sh", "-c", tuiScript(args, "", false)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "it's|$HOME|a b|`id`||line\nbreak|"; string(out) != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

// A message that picks no model (the hub's own: a supervision message, a relay, a digest) runs the conversation on what
// it ran; claude_interactive is the manager's switch, not a model pick, and stays with it. It was dropped with the
// pick, so a conversation such messages start (a background supervisor's) never ran interactive Claude Code.
func TestAMessageWithNoModelPickKeepsInteractive(t *testing.T) {
	fakeClaudeHome(t, "while read line; do echo '{\"type\":\"system\",\"subtype\":\"init\"}'; done\n")
	resetTUICooldown(t)
	n := codexTestNode(t)
	withFakeHub(t, n)
	cwd := t.TempDir()
	id, err := n.assistantSend(context.Background(), "", cwd, "claude", "hello", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	n.stopAssistant(id)
	specFile := fakeTUIBridge(t, n)
	if _, err := n.assistantSend(context.Background(), id, cwd, "", "a supervision message", nil, "", "", modelExecution{Interactive: true}); err != nil {
		t.Fatal(err)
	}
	defer n.stopAssistant(id)
	var spec tuiSpec
	waitFor(t, "the bridge's spec", func() bool {
		raw, _ := os.ReadFile(specFile)
		return json.Unmarshal(bytes.TrimSpace(raw), &spec) == nil
	})
	if spec.Session != id {
		t.Fatalf("spec %+v", spec)
	}
}

// Interactive Claude Code and what it runs (its MCP server, its hooks) descend from the bridge, the process the node
// runs for the conversation, as claude -p's do: that is how the node takes the assistant's MCP server for the
// assistant's (mcpAssistantOf). Under a tmux server that ran as a daemon of its own they descended from no process the
// node runs, and the main conversation got an Agent's four tools. Also when the bridge's server is slow to listen:
// new-session then started a daemon of its own, and the bridge's server exited (1 start in 4 without the delay).
func TestInteractiveClaudeCodeDescendsFromTheBridge(t *testing.T) {
	real, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	slow := t.TempDir() // a tmux whose server listens 300 ms late
	if err := os.WriteFile(filepath.Join(slow, "tmux"), []byte("#!/bin/sh\n[ \"$1\" = -D ] && /bin/sleep 0.3\nexec "+real+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	n := codexTestNode(t)
	n.mu.Lock()
	n.procs["main-chat"] = &assistantProc{id: "main-chat", cmd: &exec.Cmd{Process: self}}
	n.mu.Unlock()
	for _, path := range []string{os.Getenv("PATH"), slow + string(os.PathListSeparator) + os.Getenv("PATH")} {
		t.Setenv("PATH", path)
		dir := t.TempDir()
		child := filepath.Join(dir, "child")
		b := &tuiBridge{spec: tuiSpec{Cwd: dir, TUI: []string{"/bin/sh", "-c", `sleep 30 & echo $! > "$1.tmp"; mv "$1.tmp" "$1"; wait`, "sh", child}}}
		if err := b.launch(); err != nil {
			b.shutdown()
			t.Fatal(err)
		}
		var pid int
		waitFor(t, "the pane's child", func() bool {
			raw, err := os.ReadFile(child)
			return err == nil && json.Unmarshal(bytes.TrimSpace(raw), &pid) == nil
		})
		line := lineage(pid, parentPID)
		id, _, ok := n.mcpAssistantOf(pid, nil, parentPID, "main-chat")
		b.shutdown()
		if !slices.Contains(line, os.Getpid()) {
			t.Fatalf("tmux %s: the pane's child (pid %d) does not descend from the bridge (pid %d): %v", b.tm.Bin, pid, os.Getpid(), line)
		}
		if !ok || id != "main-chat" {
			t.Fatalf("tmux %s: the node does not take the pane's MCP server for the assistant's: %q %v", b.tm.Bin, id, ok)
		}
	}
}

// Interactive Claude Code gets the PWD claude -p gets, not the folder as tmux was told it: Claude Code takes its
// working directory from PWD, and the folder written another way (a symlinked home) became a second working directory
// of the conversation.
func TestTheInteractivePaneGetsThePrintModePWD(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		pwd  string
		set  bool
		want string
	}{{"", false, physical}, {link, true, link}} {
		t.Setenv("PWD", c.pwd)
		if !c.set {
			os.Unsetenv("PWD") // claude -p from a node with no PWD: its shell takes the folder's real path
		}
		got := filepath.Join(real, "pwd")
		b := &tuiBridge{spec: tuiSpec{Cwd: link, TUI: []string{"/bin/sh", "-c", `printf '%s' "$PWD" > "$1.tmp"; mv "$1.tmp" "$1"; exec sleep 30`, "sh", got}}}
		if err := b.launch(); err != nil {
			t.Fatal(err)
		}
		var raw []byte
		waitFor(t, "the pane's PWD", func() bool { raw, err = os.ReadFile(got); return err == nil })
		b.shutdown()
		if string(raw) != c.want {
			t.Fatalf("PWD set %v (%q): the pane has %q, claude -p %q", c.set, c.pwd, raw, c.want)
		}
		os.Remove(got)
	}
}

// The print-mode command a bridge falls back to gets the environment claude -p gets from the node, PWD included
// (none here): not the folder as written, which exec adds for a command with no environment of its own.
func TestFallbackGetsThePrintModePWD(t *testing.T) {
	dir := t.TempDir()
	real := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	printer := filepath.Join(dir, "print")
	if err := os.WriteFile(printer, []byte("#!/bin/sh\nprintf 'pwd %s\\n' \"$PWD\"\nwhile IFS= read -r l; do :; done\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir) // no tmux
	t.Setenv("PWD", "")
	os.Unsetenv("PWD")
	spec, _ := json.Marshal(tuiSpec{Session: "s1", Cwd: link, TUI: []string{"claude"}, Print: []string{printer}})
	r, w := io.Pipe()
	out, errb := tuiTestOut{closeAt: "pwd ", closer: w}, tuiTestOut{}
	defer time.AfterFunc(10*time.Second, func() { _ = w.Close() }).Stop()
	go func() { _, _ = io.WriteString(w, string(spec)+"\n"+`{"type":"user","message":{"content":"hi"}}`+"\n") }()
	if status := AssistantTUI(nil, r, &out, &errb); status != 7 {
		t.Fatalf("status %d, stderr %s", status, errb.String())
	}
	if !strings.Contains(out.String(), "pwd "+physical+"\n") {
		t.Fatalf("the fallback's PWD, want %q:\n%s", physical, out.String())
	}
}
