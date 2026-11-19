package tokenuse

import (
	"fmt"
	"testing"
	"time"
)

func claudeLine(id, model, usage string, extra string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-11-17T08:00:00Z"%s,"message":{"id":%q,"model":%q,"role":"assistant","content":[],"usage":%s}}`,
		extra, id, model, usage)
}

// A request is a record at the most its lines say: the first line's usage, then what later lines add (Claude Code up
// to 2.1.306 wrote the output so far on each content block), More; lines that add nothing, Claude Code's own error
// replies and zeros give none.
func TestClaudeParser(t *testing.T) {
	p := ClaudeParser()
	type want struct {
		ok   bool
		key  string
		more bool
		t    Tokens
		fast bool
	}
	for i, c := range []struct {
		line string
		want want
	}{
		{claudeLine("m1", "claude-opus-5-5", `{"input_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":5,"cache_creation":{"ephemeral_1h_input_tokens":20}}`, ""),
			want{true, "claude:m1", false, Tokens{Input: 3, CacheRead: 100, CacheWrite: 20, CacheWrite1h: 20, Output: 5}, false}},
		{claudeLine("m1", "claude-opus-5-5", `{"input_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":5}`, ""),
			want{}}, // the next content block, the same usage
		{claudeLine("m1", "claude-opus-5-5", `{"input_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":9}`, ""),
			want{true, "claude:m1", true, Tokens{Output: 4}, false}}, // more output on a later block
		{`{"type":"user","timestamp":"2026-11-17T08:00:01Z","message":{"role":"user","content":"the usage"}}`, want{}},
		{claudeLine("m2", "<synthetic>", `{"input_tokens":0,"output_tokens":0}`, ""), want{}},
		{claudeLine("m3", "claude-opus-5-5", `{"input_tokens":2,"output_tokens":9}`, `,"isApiErrorMessage":true`), want{}},
		{claudeLine("m4", "claude-opus-5-5", `{"input_tokens":0,"output_tokens":0}`, ""), want{}},
		{claudeLine("m4", "claude-opus-5-5", `{"input_tokens":1,"output_tokens":2}`, ""), // the request's first usage
			want{true, "claude:m4", false, Tokens{Input: 1, Output: 2}, false}},
		{claudeLine("", "claude-sonnet-5-5", `{"input_tokens":1,"output_tokens":1,"speed":"fast"}`, `,"isSidechain":true`),
			want{true, "", false, Tokens{Input: 1, Output: 1}, true}}, // no id: counted as it is
		{claudeLine("", "claude-sonnet-5-5", `{"input_tokens":1,"output_tokens":1}`, ""),
			want{true, "", false, Tokens{Input: 1, Output: 1}, false}}, // a request of its own again
	} {
		r, ok := p.Line([]byte(c.line))
		got := want{ok, r.Key, r.More, r.Tokens, r.Fast}
		if got != c.want {
			t.Errorf("line %d: %+v, want %+v", i, got, c.want)
		}
		if ok && (r.Agent != Claude || !r.Time.Equal(time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC))) {
			t.Errorf("line %d: agent %q time %v", i, r.Agent, r.Time)
		}
	}
}

func codexLine(kind, payload string) string {
	return fmt.Sprintf(`{"timestamp":"2026-11-17T08:00:00.5Z","type":%q,"payload":%s}`, kind, payload)
}

func tokenCount(total, in, cached, write, out, reasoning int) string {
	return codexLine("event_msg", fmt.Sprintf(`{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},`+
		`"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d}}}`,
		total, in, cached, write, out, reasoning))
}

// A grown running total is a request, in the four kinds: input without what was read from or written to the cache;
// a repeated token_count (a rate-limit update) and one without info are none; the model is the latest turn_context's.
func TestCodexParser(t *testing.T) {
	p := CodexParser(Traex)
	for i, c := range []struct {
		line  string
		ok    bool
		model string
		t     Tokens
	}{
		{tokenCount(100, 90, 40, 0, 10, 0), true, "codex", Tokens{Input: 50, CacheRead: 40, Output: 10}},
		{codexLine("turn_context", `{"model":"gpt-6-codex","cwd":"/w"}`), false, "", Tokens{}},
		{tokenCount(100, 90, 40, 0, 10, 0), false, "", Tokens{}}, // the same total again
		{codexLine("event_msg", `{"type":"token_count","info":null}`), false, "", Tokens{}},
		{tokenCount(1000, 800, 500, 200, 100, 60), true, "gpt-6-codex", Tokens{Input: 100, CacheRead: 500, CacheWrite: 200, Output: 100, Reasoning: 60}},
		{tokenCount(1100, 50, 80, 0, 50, 0), true, "gpt-6-codex", Tokens{CacheRead: 80, Output: 50}}, // more cached than input: none fresh
		{codexLine("event_msg", `{"type":"agent_message","message":"token_count"}`), false, "", Tokens{}},
	} {
		r, ok := p.Line([]byte(c.line))
		if ok != c.ok || ok && (r.Model != c.model || r.Tokens != c.t || r.Agent != Traex || r.More) {
			t.Errorf("line %d: %v %+v, want %v %q %+v", i, ok, r, c.ok, c.model, c.t)
		}
	}
	r, _ := CodexParser(Codex).Line([]byte(tokenCount(5, 5, 0, 0, 0, 0)))
	if r.Key != "codex:2026-11-17T08:00:00.5Z:5" {
		t.Errorf("key %q: the time and the running total", r.Key)
	}
}

// A request's lines read back from before a window: Merge raises what it gave, Saturated adds nothing more.
func TestRequest(t *testing.T) {
	var r Request
	r.Add("m1", Tokens{Output: 4})
	var o Request
	o.Add("m1", Tokens{Output: 9, Input: 1})
	r.Merge(o)
	if add, ok := r.Add("m1", Tokens{Output: 12, Input: 1}); !ok || add != (Tokens{Output: 3}) || r.ID() != "m1" {
		t.Fatalf("after merge: %+v %v", add, ok)
	}
	s := Saturated("m2")
	if _, ok := s.Add("m2", Tokens{Input: 1 << 40, Output: 1 << 40}); ok {
		t.Fatal("a saturated request adds")
	}
	if add, ok := s.Add("m3", Tokens{Input: 1}); !ok || add.Input != 1 {
		t.Fatalf("the next request: %+v %v", add, ok)
	}
	if got := (Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, CacheWrite1h: 4, Reasoning: 2}).Total(); got != 10 {
		t.Fatalf("total %d: the four kinds, never the parts of them again", got)
	}
}

// pi and omp: an assistant message's usage with its model; pi's usage entries and omp's model_usage with theirs; a
// compaction's with the session's latest model (pi's model_change, omp's "provider/model"); reasoning is part of the
// output, omp's orchestration tokens are added to their kinds; a failed request's zeros and other entries are none.
func TestPiParser(t *testing.T) {
	p := PiParser(Omp)
	for i, c := range []struct {
		line  string
		ok    bool
		model string
		key   string
		t     Tokens
	}{
		{`{"type":"session","version":3,"id":"s1","timestamp":"2026-11-17T08:00:00.000Z","cwd":"/w"}`, false, "", "", Tokens{}},
		{`{"type":"message","id":"a1b2c3d4","parentId":null,"timestamp":"2026-11-17T08:00:01.000Z","message":{"role":"user","content":"the usage"}}`, false, "", "", Tokens{}},
		{`{"type":"message","id":"e5f6a7b8","parentId":"a1b2c3d4","timestamp":"2026-11-17T08:00:02.000Z","message":{"role":"assistant","model":"model-a","provider":"p",` +
			`"usage":{"input":3,"output":40,"cacheRead":500,"cacheWrite":60,"cacheWrite1h":60,"reasoning":10,"totalTokens":603}}}`,
			true, "model-a", "pi:e5f6a7b8:2026-11-17T08:00:02.000Z", Tokens{Input: 3, Output: 40, CacheRead: 500, CacheWrite: 60, CacheWrite1h: 60, Reasoning: 10}},
		{`{"type":"message","id":"c9d0e1f2","timestamp":"2026-11-17T08:00:03.000Z","message":{"role":"assistant","model":"model-a","stopReason":"error",` +
			`"usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0}}}`, false, "", "", Tokens{}},
		{`{"type":"model_usage","id":"u1","timestamp":1763366404000,"purpose":"title","provider":"p","model":"model-s",` +
			`"usage":{"input":7,"output":2,"cacheRead":0,"cacheWrite":0,"reasoningTokens":1,"orchestration":{"input":5,"output":1}}}`,
			true, "model-s", "pi:u1:1763366404000", Tokens{Input: 12, Output: 3, Reasoning: 1}},
		{`{"type":"model_change","id":"m1","timestamp":"2026-11-17T08:00:05.000Z","model":"p/model-b"}`, false, "", "", Tokens{}},
		{`{"type":"compaction","id":"k1","timestamp":"2026-11-17T08:00:06.000Z","summary":"…","firstKeptEntryId":"a1b2c3d4","tokensBefore":9000,` +
			`"usage":{"input":900,"output":80,"cacheRead":0,"cacheWrite":0}}`, true, "model-b", "pi:k1:2026-11-17T08:00:06.000Z", Tokens{Input: 900, Output: 80}},
		{`{"type":"model_change","id":"m2","timestamp":"2026-11-17T08:00:07.000Z","provider":"p","modelId":"model-c"}`, false, "", "", Tokens{}},
		{`{"type":"usage","id":"w1","timestamp":"2026-11-17T08:00:08.000Z","kind":"cache_warm","provider":"p","model":"model-c","usage":{"input":1,"output":1,"cacheRead":2000,"cacheWrite":0}}`,
			true, "model-c", "pi:w1:2026-11-17T08:00:08.000Z", Tokens{Input: 1, Output: 1, CacheRead: 2000}},
		{`{"type":"branch_summary","id":"b1","timestamp":"2026-11-17T08:00:09.000Z","fromId":"e5f6a7b8","summary":"…"}`, false, "", "", Tokens{}},
	} {
		r, ok := p.Line([]byte(c.line))
		if ok != c.ok || ok && (r.Model != c.model || r.Key != c.key || r.Tokens != c.t || r.Agent != Omp || r.Time.IsZero()) {
			t.Errorf("line %d: %v %+v, want %v %q %q %+v", i, ok, r, c.ok, c.model, c.key, c.t)
		}
	}
}

// opencode: a finished step's tokens with the reasoning in its output; an unfinished one, a compaction still running
// and one without tokens count none.
func TestOpencodeRow(t *testing.T) {
	done := int64(1763366400500)
	step := OpencodeRow{ID: "msg_1", Session: "ses_1", Type: "assistant", Created: 1763366400000, Completed: &done, Model: "model-a",
		Input: 10, Output: 20, Reasoning: 5, CacheRead: 300, CacheWrite: 40}
	r, ok := step.Record()
	if !ok || r.Tokens != (Tokens{Input: 10, Output: 25, Reasoning: 5, CacheRead: 300, CacheWrite: 40}) || r.Key != "opencode:msg_1" ||
		r.Session != "ses_1" || r.Agent != Opencode || r.Time.UnixMilli() != 1763366400000 {
		t.Fatalf("step = %+v %v", r, ok)
	}
	for _, row := range []OpencodeRow{
		{Type: "assistant", Input: 1},                     // under way
		{Type: "compaction", Status: "running", Input: 1}, // under way
		{Type: "assistant", Completed: &done},             // no tokens
		{Type: "user", Completed: &done, Input: 1},        // not a request
	} {
		if _, ok := row.Record(); ok {
			t.Errorf("%+v counted", row)
		}
	}
	if _, ok := (OpencodeRow{Type: "compaction", Status: "failed", Output: 3}).Record(); !ok {
		t.Error("a failed compaction's request is not counted")
	}
}
