//go:build roundlive

package node

// A live check of the background supervisor's rounds (#7315, assistant_round.go) with the machine's real claude against
// a local mock of the model API: no model, account or network is used, and it gets a made-up home, config directory
// and environment in a scratch directory, removed after, none of this machine's own. Never run by CI: it needs the roundlive
// build tag and ROUNDLIVE=1 (ROUNDLIVE_CLAUDE: another claude than the one on PATH; ROUNDLIVE_TOOLS: other --tools for
// the rounds, to compare; ROUNDLIVE_DEBUG: log each round's output).
//
//	cd server && ROUNDLIVE=1 go test -tags roundlive -run TestAssistantRoundLive -v -count=1 ./internal/node
//
// It re-runs what the rounds rest on: a round resumed at the end of the first turn keeps the session id and the file,
// sends the model only the first turn and itself, a later round does not see the one before, a plain resume follows
// the latest round, an anchor that is not on the chain is refused, the round's tool flags leave only what it lists,
// and how long a round takes to start with a transcript at backgroundTranscriptLimit (the manager's, 32 MiB).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// mockModel is the model API's mock: it answers every message request with a short text that ends the turn, and keeps
// what each request asked (its messages' text and its tools' names) with when it came.
type mockModel struct {
	mu   sync.Mutex
	asks []mockAsk
}

type mockAsk struct {
	at       time.Time
	messages []string // each message's role and text, "user: …"
	tools    []string
}

func (m *mockModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/v1/messages") || strings.Contains(r.URL.Path, "count_tokens") {
		if strings.Contains(r.URL.Path, "count_tokens") {
			_ = json.NewEncoder(w).Encode(map[string]any{"input_tokens": 1})
			return
		}
		http.NotFound(w, r)
		return
	}
	var req struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ask := mockAsk{at: time.Now()}
	for _, msg := range req.Messages {
		ask.messages = append(ask.messages, msg.Role+": "+contentText(msg.Content))
	}
	for _, t := range req.Tools {
		ask.tools = append(ask.tools, t.Name)
	}
	m.mu.Lock()
	m.asks = append(m.asks, ask)
	n := len(m.asks)
	m.mu.Unlock()
	reply := fmt.Sprintf("answer %d", n)
	message := map[string]any{"id": fmt.Sprintf("msg_%d", n), "type": "message", "role": "assistant", "model": "mock-model",
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}
	if !req.Stream {
		message["content"] = []any{map[string]any{"type": "text", "text": reply}}
		message["stop_reason"] = "end_turn"
		_ = json.NewEncoder(w).Encode(message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range []map[string]any{
		{"type": "message_start", "message": message},
		{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": reply}},
		{"type": "content_block_stop", "index": 0},
		{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}},
		{"type": "message_stop"},
	} {
		raw, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], raw)
	}
}

// contentText is a message content's text: the string, or its text blocks joined.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// lastAsk is the latest request one of whose messages says mark (the conversation's own, not a side request).
func (m *mockModel) lastAsk(t *testing.T, mark string) mockAsk {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.asks) - 1; i >= 0; i-- {
		if a := m.asks[i]; strings.Contains(strings.Join(a.messages, "\n"), mark) {
			return a
		}
	}
	t.Fatalf("no request says %q", mark)
	return mockAsk{}
}

// answers counts a request's assistant messages.
func (a mockAsk) answers() int {
	n := 0
	for _, msg := range a.messages {
		if strings.HasPrefix(msg, "assistant: ") {
			n++
		}
	}
	return n
}

type roundLive struct {
	t       *testing.T
	claude  string
	scratch string
	cwd     string
	config  string
	env     []string
	model   *mockModel
	stdout  string // the last run's standard output alone
}

func roundLiveSetup(t *testing.T) *roundLive {
	t.Helper()
	if os.Getenv("ROUNDLIVE") != "1" {
		t.Skip("set ROUNDLIVE=1 to run the machine's claude against a local mock of the model API")
	}
	claude := os.Getenv("ROUNDLIVE_CLAUDE")
	if claude == "" {
		var err error
		if claude, err = exec.LookPath("claude"); err != nil {
			t.Skip("no claude on PATH")
		}
	}
	scratch, err := os.MkdirTemp("", "roundlive-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	l := &roundLive{t: t, claude: claude, scratch: scratch, cwd: filepath.Join(scratch, "work"), config: filepath.Join(scratch, "config"), model: &mockModel{}}
	home := filepath.Join(scratch, "home")
	for _, d := range []string{l.cwd, l.config, home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: l.model}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	// A made-up environment: no gateway, token or setting of this machine's reaches the process.
	l.env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "CLAUDE_CONFIG_DIR=" + l.config, "TMPDIR=" + scratch,
		"ANTHROPIC_BASE_URL=http://" + ln.Addr().String(), "ANTHROPIC_API_KEY=mock-key-not-a-secret",
		"NO_PROXY=127.0.0.1,localhost", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "DISABLE_AUTOUPDATER=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1"}
	return l
}

// run runs claude -p with args on one stream-json message text, until it answers or fails; its output and how long it
// took from the start to the model's request (zero when none came).
func (l *roundLive) run(text string, args ...string) (string, time.Duration, error) {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	base := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--model", "mock-model"}
	cmd := exec.CommandContext(ctx, l.claude, append(base, args...)...)
	cmd.Dir, cmd.Env = l.cwd, l.env
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}})
	cmd.Stdin = bytes.NewReader(append(line, '\n'))
	var out, stdout bytes.Buffer
	both := &lockedWriter{w: &out} // two pipes now: their copies must not write to out at once
	cmd.Stdout, cmd.Stderr = io.MultiWriter(both, &stdout), both
	l.model.mu.Lock()
	before := len(l.model.asks)
	l.model.mu.Unlock()
	start := time.Now()
	err := cmd.Run()
	l.stdout = stdout.String()
	var took time.Duration
	l.model.mu.Lock()
	if len(l.model.asks) > before {
		took = l.model.asks[before].at.Sub(start)
	}
	l.model.mu.Unlock()
	return out.String(), took, err
}

// lockedWriter is w for writers on several goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// transcriptOf is the conversation's file: the one transcript in the project directory, which must be the session's.
func (l *roundLive) transcriptOf(id string) string {
	l.t.Helper()
	files, _ := filepath.Glob(filepath.Join(l.config, "projects", "*", "*.jsonl"))
	if len(files) != 1 || filepath.Base(files[0]) != id+".jsonl" {
		l.t.Fatalf("transcripts %v, want one for %s", files, id)
	}
	return files[0]
}

func TestAssistantRoundLive(t *testing.T) {
	l := roundLiveSetup(t)
	id := newUUID()
	tools := roundBuiltinTools
	if v := os.Getenv("ROUNDLIVE_TOOLS"); v != "" {
		tools = v
	}
	roundArgs := func(anchor string) []string {
		return []string{"--resume", id, "--resume-session-at", anchor, "--tools", tools, "--strict-mcp-config",
			"--mcp-config", `{"mcpServers":{}}`}
	}
	const start = "这是后台督办对话（测试）。回一句「好」就结束，不要调用工具。"
	if out, _, err := l.run(start, "--session-id", id); err != nil {
		t.Fatalf("the start: %v\n%s", err, out)
	}
	path := l.transcriptOf(id)
	tr, err := roundAnchor(path)
	if err != nil || tr.anchor == "" || !versionAtLeast(tr.version, roundMinClaude) {
		t.Fatalf("after the start: %+v %v", tr, err)
	}
	anchor := tr.anchor
	t.Logf("claude %s, anchor %s", tr.version, anchor)

	round := func(n int) string {
		return prompts.RenderSupervise(prompts.Supervise{Leader: true, Background: true, Fresh: true,
			Entries: []prompts.SuperviseEntry{{ID: fmt.Sprintf("t%d", n), Title: fmt.Sprintf("round %d task", n), Kind: "idle"}}})
	}
	for n := 1; n <= 2; n++ {
		out, took, err := l.run(round(n), roundArgs(anchor)...)
		if err != nil {
			t.Fatalf("round %d: %v\n%s", n, err, out)
		}
		if os.Getenv("ROUNDLIVE_DEBUG") != "" {
			t.Logf("round %d output:\n%s", n, out)
		}
		ask := l.model.lastAsk(t, fmt.Sprintf("round %d task", n))
		all := strings.Join(ask.messages, "\n")
		if ask.answers() != 1 || !strings.Contains(all, "这是后台督办对话（测试）") || !strings.Contains(all, prompts.SuperviseRoundMarker) {
			t.Fatalf("round %d asked the model with %q", n, ask.messages)
		}
		if n == 2 && strings.Contains(all, "round 1 task") {
			t.Fatalf("round 2 saw round 1: %q", ask.messages)
		}
		t.Logf("round %d: the model was asked %v after the start; tools %v", n, took.Round(time.Millisecond), ask.tools)
		for _, tool := range ask.tools {
			if !slices.Contains(strings.Split(roundBuiltinTools, ","), tool) {
				t.Errorf("round %d lists %q", n, tool)
			}
		}
		if path2 := l.transcriptOf(id); path2 != path {
			t.Fatalf("round %d wrote %s", n, path2)
		}
		if tr, err := roundAnchor(path); err != nil || tr.anchor != anchor {
			t.Fatalf("after round %d the anchor is %+v (%v), want %s", n, tr, err, anchor)
		}
	}
	// The owner writing there: a plain resume goes on from the latest round, not an earlier one.
	if out, _, err := l.run("owner question", "--resume", id); err != nil {
		t.Fatalf("plain resume: %v\n%s", err, out)
	}
	ask := l.model.lastAsk(t, "owner question")
	all := strings.Join(ask.messages, "\n")
	if !strings.Contains(all, "round 2 task") || strings.Contains(all, "round 1 task") {
		t.Fatalf("the plain resume asked with %q", ask.messages)
	}
	// Every round is in view in the app, in file order.
	f := transcript.NewFile(path)
	if _, _, err := f.RefreshSpan(); err != nil {
		t.Fatal(err)
	}
	items, _, _ := f.Page(0, 1000)
	var said []string
	for _, it := range items {
		if it.Kind == proto.KindText && it.Role == "user" {
			for _, w := range []string{"round 1 task", "round 2 task", "owner question"} {
				if strings.Contains(it.Text, w) {
					said = append(said, w)
				}
			}
		}
	}
	if strings.Join(said, "|") != "round 1 task|round 2 task|owner question" {
		t.Fatalf("the reader shows %q", said)
	}
	// An anchor that is not on the chain is refused, and the conversation is as it was. Its standard output is only a
	// result with no turn, no init before it: what the node takes for a round's process that did not start (readAssistant).
	if out, _, err := l.run(round(3), roundArgs(newUUID())...); err == nil || !strings.Contains(out, "No message found with message.uuid of") {
		t.Fatalf("a made-up anchor: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(l.stdout), "\n") {
		var ev struct {
			Type     string `json:"type"`
			Subtype  string `json:"subtype"`
			IsError  bool   `json:"is_error"`
			NumTurns int    `json:"num_turns"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "result" || !ev.IsError || ev.NumTurns != 0 {
			t.Fatalf("a made-up anchor printed %q", l.stdout)
		}
	}
	if tr, err := roundAnchor(path); err != nil || tr.anchor != anchor {
		t.Fatalf("after a made-up anchor the anchor is %+v (%v)", tr, err)
	}

	// How long a round takes to start as the file grows to backgroundTranscriptLimit (the manager's, 32 MiB) and past
	// it: copies of round 1's lines, hanging from the anchor as rounds do, about 4 KiB a line.
	_, small, _ := l.run(round(8), roundArgs(anchor)...)
	t.Logf("at %d KiB: the model was asked %v after the round's start", fileSize(path)>>10, small.Round(time.Millisecond))
	for _, size := range []int64{32 << 20, 128 << 20} {
		padTranscript(t, path, anchor, size)
		out, took, err := l.run(round(9), roundArgs(anchor)...)
		if err != nil {
			t.Fatalf("a round at %d MiB: %v\n%s", size>>20, err, finalLine(out))
		}
		begun := time.Now()
		tr, err := roundAnchor(path)
		scan := time.Since(begun)
		if err != nil || tr.anchor != anchor {
			t.Fatalf("the anchor at %d MiB: %+v %v", size>>20, tr, err)
		}
		begun = time.Now()
		if _, _, err := transcript.NewFile(path).RefreshSpan(); err != nil {
			t.Fatal(err)
		}
		read := time.Since(begun)
		t.Logf("at %d MiB: the model was asked %v after the round's start; roundAnchor took %v, the app's reader's cold read %v",
			tr.size>>20, took.Round(time.Millisecond), scan.Round(time.Millisecond), read.Round(time.Millisecond))
	}

	// The ways that do not keep the conversation: an existing id cannot be started again, and /clear moves on to
	// another id.
	if out, _, err := l.run("again", "--session-id", id); err == nil || !strings.Contains(out, "already in use") {
		t.Fatalf("--session-id of an existing conversation: %v\n%s", err, finalLine(out))
	}
	if out, _, err := l.run("/clear", "--resume", id); err != nil {
		t.Fatalf("/clear: %v\n%s", err, out)
	} else if !strings.Contains(out, "conversation_reset") || strings.Count(out, `"session_id":"`+id+`"`) == strings.Count(out, `"session_id":"`) {
		t.Fatalf("/clear kept the id or said nothing of it:\n%s", out)
	}
	// Anchored at the first message instead of the end of its turn, the round merges into the start message (and the
	// first turn's end is off the latest chain from then on: last, as it ends the rounds of this conversation).
	first := ""
	if raw, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			var l roundLine
			if json.Unmarshal([]byte(line), &l) == nil && l.prompt() {
				first = l.UUID
				break
			}
		}
	}
	if out, _, err := l.run(round(4), roundArgs(first)...); err != nil {
		t.Fatalf("anchored at the first message: %v\n%s", err, finalLine(out))
	}
	merged := l.model.lastAsk(t, "round 4 task")
	if merged.answers() != 0 {
		t.Fatalf("anchored at the first message, the model was asked with %q", merged.messages)
	}
	t.Logf("anchored at the first message: %d messages, no answer of the start's between them", len(merged.messages))
}

// padTranscript appends copies of the transcript's round lines (a user message and its answer hanging from anchor)
// with new ids and a long text until the file is size bytes.
func padTranscript(t *testing.T, path, anchor string, size int64) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var user, reply map[string]any
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var l map[string]any
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l["type"] == "user" && l["parentUuid"] == anchor && user == nil {
			user = l
		} else if user != nil && reply == nil && l["type"] == "assistant" {
			reply = l
		}
	}
	if user == nil || reply == nil {
		t.Fatal("no round to copy")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	text := strings.Repeat("padding ", 256) // a line of about 4 KiB with its fields
	w := bufio.NewWriter(f)
	have := int64(len(raw))
	for i := 0; have < size; i++ {
		u, a := newUUID(), newUUID()
		user["uuid"], user["parentUuid"] = u, anchor
		user["message"] = map[string]any{"role": "user", "content": text}
		reply["uuid"], reply["parentUuid"] = a, u
		reply["message"] = map[string]any{"id": fmt.Sprintf("msg_pad_%d", i), "type": "message", "role": "assistant", "model": "mock-model",
			"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}
		for _, l := range []map[string]any{user, reply} {
			line, _ := json.Marshal(l)
			n, _ := w.Write(append(line, '\n'))
			have += int64(n)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func finalLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return lines[len(lines)-1]
}
