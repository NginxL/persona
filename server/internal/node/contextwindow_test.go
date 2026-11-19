package node

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestContextWindowOf(t *testing.T) {
	for _, c := range []struct {
		name, model          string
		recorded, used, want int
	}{
		{"a 1M model needs no suffix", "claude-opus-5-5", 0, 61_000, 1_000_000},
		{"a 200k model", "claude-opus-4-6", 0, 61_000, 200_000},
		{"claude 1m suffix", "claude-opus-4-6[1m]", 0, 10_000, 1_000_000},
		{"claude over 200k is the 1m variant", "claude-sonnet-4-6", 0, 340_000, 1_000_000},
		{"codex records its window", "gpt-5", 475_000, 26_279, 475_000},
		{"recorded beats the id", "claude-opus-5-5", 123_000, 1_000, 123_000},
		{"gateway model is unknown", "some-gateway-model", 0, 5_000, 0},
		// A gateway routes a Claude model under a prefix: the model after the last "/" is the one that answers.
		{"a routed Claude 1M model", "passthrough/claude-opus-5-5", 0, 61_000, 1_000_000},
		{"a routed 200k Claude model", "passthrough/claude-opus-4-6", 0, 61_000, 200_000},
		{"a routed Claude model over 200k", "passthrough/claude-sonnet-4-6", 0, 340_000, 1_000_000},
		{"a vendor path that is not Claude", "meta/llama-3", 0, 5_000, 0},
		{"synthetic is unknown", "<synthetic>", 0, 5_000, 0},
		{"empty is unknown", "", 0, 5_000, 0},
	} {
		if got := contextWindowOf(c.model, c.recorded, c.used); got != c.want {
			t.Errorf("%s: contextWindowOf(%q, %d, %d) = %d, want %d", c.name, c.model, c.recorded, c.used, got, c.want)
		}
	}
}

func TestSetContextOmitsUnknown(t *testing.T) {
	var s proto.Session
	setContext(&s, quickMeta{model: "claude-opus-5-5"})
	if s.ContextTokens != 0 || s.ContextWindow != 0 {
		t.Fatalf("no reply yet must leave the fields empty: %+v", s)
	}
	setContext(&s, quickMeta{model: "claude-opus-4-5", contextTokens: 61_000})
	if s.ContextTokens != 61_000 || s.ContextWindow != 200_000 {
		t.Fatalf("got %d / %d", s.ContextTokens, s.ContextWindow)
	}
	// The report in #5758: 179k on claude-opus-5-5 is 18% of its 1M window, not 90% of 200k.
	setContext(&s, quickMeta{model: "claude-opus-5-5", contextTokens: 179_000})
	if s.ContextTokens != 179_000 || s.ContextWindow != 1_000_000 {
		t.Fatalf("claude-opus-5-5: got %d / %d", s.ContextTokens, s.ContextWindow)
	}
}

// The window each of Claude Code's own models runs with when no "[1m]" is chosen (its model list, 2.1.311, #5758),
// as the transcript names them: plain, dated, Vertex's "@" date, any case. Opus and Sonnet 4.6 are 1M only as "[1m]".
func TestContextWindowOfClaudeCodesModels(t *testing.T) {
	for model, want := range map[string]int{
		"claude-fable-5-2": 1_000_000, "claude-fable-5-1": 1_000_000, "claude-fable-5": 1_000_000,
		"claude-mythos-5-2": 1_000_000, "claude-mythos-5": 1_000_000,
		"claude-opus-5-5": 1_000_000, "claude-opus-5": 1_000_000, "claude-opus-4-8": 1_000_000, "claude-opus-4-7": 1_000_000,
		"claude-sonnet-5-5": 1_000_000, "claude-sonnet-5": 1_000_000, "claude-haiku-5-5": 1_000_000,
		"claude-opus-4-6": 200_000, "claude-sonnet-4-6": 200_000, "claude-opus-4-5": 200_000, "claude-sonnet-4-5": 200_000,
		"claude-opus-4-1": 200_000, "claude-opus-4-0": 200_000, "claude-sonnet-4-0": 200_000, "claude-haiku-4-5": 200_000,
		// dated and other spellings
		"claude-opus-4-7-20260416": 1_000_000, "claude-opus-5-5@20261101": 1_000_000, "claude-opus-4-7@20260416": 1_000_000, "Claude-Sonnet-5-5": 1_000_000,
		"claude-sonnet-4-5-20250929": 200_000, "claude-haiku-4-5-20251001": 200_000, "claude-opus-4-1-20250805": 200_000,
		"claude-sonnet-4-20250514": 200_000, "claude-opus-4-20250514": 200_000, "claude-3-5-sonnet-20241022": 200_000,
		"claude-3-7-sonnet-latest": 200_000, "claude-opus": 200_000, "claude-mythos-preview": 200_000,
		// an id with a version that is not a model's is not a newer one
		"claude-opus-500": 200_000, "claude-sonnet-x-5": 200_000,
	} {
		if got := contextWindowOf(model, 0, 1_000); got != want {
			t.Errorf("contextWindowOf(%q) = %d, want %d", model, got, want)
		}
	}
}

// A runtime's session runs as the model the runtime names: its "[1m]" is the 1M window, whatever id the transcript
// records for the replies (the id without the suffix; behind a gateway, the gateway's own).
func TestSetRuntimeContext(t *testing.T) {
	for _, c := range []struct {
		name, runAs string
		m           quickMeta
		want        int
	}{
		{"the 1M variant of the model that answered", "claude-fable-5-2[1m]", quickMeta{model: "claude-fable-5-2", contextTokens: 80_000}, 1_000_000},
		{"a gateway answering under its own id", "gw-model-high[1m]", quickMeta{model: "gw-model-trial", contextTokens: 80_000}, 1_000_000},
		{"no variant named", "claude-opus-4-6", quickMeta{model: "claude-opus-4-6", contextTokens: 80_000}, 200_000},
		{"a model that is 1M without the variant", "claude-fable-5-2", quickMeta{model: "claude-fable-5-2", contextTokens: 80_000}, 1_000_000},
		{"a gateway model with no variant named", "gw-model-high", quickMeta{model: "gw-model-trial", contextTokens: 80_000}, 0},
		{"a window the transcript records", "gw-model[1m]", quickMeta{model: "gw-model", contextTokens: 80_000, contextWindow: 400_000}, 400_000},
		{"answering as a fallback model", "claude-fable-5-2[1m]",
			quickMeta{model: "claude-opus-4-5", contextTokens: 80_000, modelFallback: &proto.ModelRefusal{}}, 200_000},
		{"answered by another Claude model (an overload fallback)", "claude-fable-5-2[1m]", quickMeta{model: "claude-sonnet-4-5", contextTokens: 80_000}, 200_000},
		{"the same model under its dated id", "claude-opus-5-5[1m]", quickMeta{model: "claude-opus-5-5-20261101", contextTokens: 80_000}, 1_000_000},
		{"a gateway's name answered under a Claude id", "gw-model-high[1m]", quickMeta{model: "claude-fable-5-2", contextTokens: 80_000}, 1_000_000},
		{"a transcript that names no model", "claude-fable-5-2[1m]", quickMeta{contextTokens: 80_000}, 1_000_000},
		{"no reply yet", "claude-fable-5-2[1m]", quickMeta{model: "claude-fable-5-2"}, 0},
	} {
		s := proto.Session{Model: c.runAs}
		setRuntimeContext(&s, c.m)
		if int(s.ContextWindow) != c.want || int(s.ContextTokens) != c.m.contextTokens {
			t.Errorf("%s: %d / %d, want %d / %d", c.name, s.ContextTokens, s.ContextWindow, c.m.contextTokens, c.want)
		}
	}
}

func ctxReply(id string, input, cacheRead, cacheCreate int) string {
	return `{"type":"assistant","timestamp":"2026-01-01T00:00:01.000Z","message":{"model":"claude-opus-5-5","id":"` + id +
		`","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":` + strconv.Itoa(input) +
		`,"cache_read_input_tokens":` + strconv.Itoa(cacheRead) + `,"cache_creation_input_tokens":` + strconv.Itoa(cacheCreate) + `,"output_tokens":5}}}`
}

func TestQuickMetaContextClaude(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	user := `{"type":"user","cwd":"/tmp/x","timestamp":"2026-01-01T00:00:00.000Z","message":{"role":"user","content":"hi"}}`
	body := user + "\n" + ctxReply("msg_1", 3, 1000, 200) + "\n" + ctxReply("msg_2", 2, 40000, 21000) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	m := readQuickMeta(path, st)
	if m.contextTokens != 2+40000+21000 {
		t.Fatalf("context tokens = %d, want the latest reply's input + cache read + cache creation", m.contextTokens)
	}
	if m.contextWindow != 0 {
		t.Fatalf("Claude transcripts record no window, got %d", m.contextWindow)
	}
	var s proto.Session
	setContext(&s, m)
	if s.ContextWindow != 1_000_000 { // claude-opus-5-5 runs with 1M
		t.Fatalf("window = %d", s.ContextWindow)
	}
}

// A tail that holds no reply (one huge line fills it) keeps what the last read knew.
func TestQuickMetaContextCarriedOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	user := `{"type":"user","cwd":"/tmp/x","timestamp":"2026-01-01T00:00:00.000Z","message":{"role":"user","content":"hi"}}`
	if err := os.WriteFile(path, []byte(user+"\n"+ctxReply("msg_1", 2, 5000, 100)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	prev := readQuickMeta(path, st)
	big := `{"type":"system","subtype":"x","content":"` + strings.Repeat("a", quickTail+10) + `"}` + "\n"
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(big)
	f.Close()
	st, _ = os.Stat(path)
	m := readQuickMetaSince(path, st, prev)
	if m.contextTokens != prev.contextTokens || m.contextTokens != 5102 {
		t.Fatalf("carried context = %d, prev %d", m.contextTokens, prev.contextTokens)
	}
}

// A tail that compacted with no reply after it does not carry the size from before the compaction over: the context
// is unknown until the next reply (#9207), and the next reply's stands.
func TestQuickMetaContextAfterCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	user := `{"type":"user","cwd":"/tmp/x","timestamp":"2026-01-01T00:00:00.000Z","message":{"role":"user","content":"hi"}}`
	if err := os.WriteFile(path, []byte(user+"\n"+ctxReply("msg_1", 2, 960_000, 10_000)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := func(line string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(line + "\n")
		f.Close()
	}
	st, _ := os.Stat(path)
	prev := readQuickMeta(path, st)
	if prev.contextTokens != 970_002 {
		t.Fatalf("before = %d", prev.contextTokens)
	}
	add(`{"type":"system","subtype":"compact_boundary","timestamp":"2026-01-01T00:00:02.000Z","compactMetadata":{"trigger":"auto","preTokens":970002,"postTokens":15000}}`)
	st, _ = os.Stat(path)
	m := readQuickMetaSince(path, st, prev)
	var s proto.Session
	if setContext(&s, m); m.contextTokens != 0 || s.ContextTokens != 0 || s.ContextWindow != 0 {
		t.Fatalf("after the compaction: %d, session %d / %d", m.contextTokens, s.ContextTokens, s.ContextWindow)
	}
	add(ctxReply("msg_2", 2, 50_000, 12_000))
	st, _ = os.Stat(path)
	if m = readQuickMetaSince(path, st, m); m.contextTokens != 62_002 {
		t.Fatalf("after the next reply = %d", m.contextTokens)
	}
}

func TestQuickMetaContextCodex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	meta := `{"timestamp":"2026-01-01T00:00:00.000Z","type":"session_meta","payload":{"id":"t1","cwd":"/tmp/x"}}`
	count := func(in int) string {
		return `{"timestamp":"2026-01-01T00:00:09.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":99999,"output_tokens":10},"last_token_usage":{"input_tokens":` +
			strconv.Itoa(in) + `,"output_tokens":4},"model_context_window":475000}}}`
	}
	// rate-limit-only token_count events carry no info and must not reset the numbers
	idle := `{"timestamp":"2026-01-01T00:00:10.000Z","type":"event_msg","payload":{"type":"token_count","info":null}}`
	body := meta + "\n" + count(7) + "\n" + count(6) + "\n" + idle + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	m := readQuickMeta(path, st)
	if m.contextTokens != 6 || m.contextWindow != 475_000 {
		t.Fatalf("codex context = %d / %d, want 6 / 475000", m.contextTokens, m.contextWindow)
	}
}
