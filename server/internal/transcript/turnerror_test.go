package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// turnErr is what a Meta says of the latest turn's error, for comparing.
type turnErr struct {
	text, kind         string
	reset              time.Time
	resumes, afterWork bool
}

func errOf(m *Meta) turnErr {
	return turnErr{text: m.TurnError, kind: m.TurnErrorKind, reset: m.TurnErrorResetsAt, resumes: m.TurnErrorResumes,
		afterWork: m.TurnErrorAfterWork}
}

const fixtureLimit = "You've hit your session limit · resets 4:20pm (UTC)"

var fixtureReset = time.Date(2026, 1, 1, 16, 20, 0, 0, time.UTC)

// The usage-limit fixture read a line at a time, the way the node's quick read sees a session that is waiting for its
// limit: the limit after some work, Claude Code's word that it goes on by itself, the turns background tasks wake it
// for (each refused at once, no new notice), a subagent's lines, then the reset and the work it resumes (#1710).
func TestClaudeUsageLimitWait(t *testing.T) {
	limited := turnErr{text: fixtureLimit, kind: proto.TurnErrorUsageLimit, reset: fixtureReset}
	resuming := limited
	resuming.resumes = true
	afterWork := limited
	afterWork.afterWork = true
	afterWorkResuming := afterWork
	afterWorkResuming.resumes = true
	waking := turnErr{resumes: true}
	want := map[int]turnErr{ // line number in the fixture → the error after it
		1:  {},
		5:  afterWork,         // the limit, after thinking and a tool call
		6:  afterWorkResuming, // "continuing automatically at 4:20pm"
		7:  afterWorkResuming, // turn_duration
		10: waking,            // a background task wakes it: the error is over, the promise is not
		11: resuming,          // refused again at once: no work in that turn
		13: resuming,          // a subagent's reply in the session's transcript
		14: resuming,          // and the limit refusing the subagent
		19: resuming,
		22: resuming, // "Usage limit reset · continuing automatically": its continuation
		23: resuming, // Claude Code's own prompt (isMeta) starts no turn
		24: {},       // the first reply: the limit is over
		29: {},       // a subagent still at the limit
	}
	f, err := os.Open(filepath.Join("testdata", "claude-usage-limit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var m Meta
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		ParseLine(n, sc.Bytes(), &m)
		if w, ok := want[n]; ok {
			if got := errOf(&m); got != w {
				t.Errorf("after line %d: %+v, want %+v", n, got, w)
			}
		}
	}
}

func claudeLine(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// apiErrorLine is Claude Code's own reply for a failed request (2.1.282's shape, trimmed).
func apiErrorLine(t *testing.T, uuid, text string, extra map[string]any) string {
	t.Helper()
	fields := map[string]any{"type": "assistant", "uuid": uuid, "timestamp": "2026-01-01T12:00:00Z", "isSidechain": false,
		"isApiErrorMessage": true, "message": map[string]any{"id": "m-" + uuid, "model": "<synthetic>", "role": "assistant",
			"usage":   map[string]any{"input_tokens": 0, "output_tokens": 0},
			"content": []any{map[string]any{"type": "text", "text": text}}}}
	for k, v := range extra {
		fields[k] = v
	}
	return claudeLine(t, fields)
}

func prompt(uuid, text string) string {
	return `{"type":"user","uuid":"` + uuid + `","timestamp":"2026-01-01T12:00:00Z","message":{"role":"user","content":"` + text + `"}}`
}

func wake(uuid string) string {
	return `{"type":"user","uuid":"` + uuid + `","timestamp":"2026-01-01T12:00:00Z","origin":{"kind":"task-notification"},` +
		`"message":{"role":"user","content":"<task-notification>\n<task-id>` + uuid + `</task-id>\n<status>completed</status>\n` +
		`<summary>Background command \"sleep\" completed (exit code 0)</summary>\n</task-notification>"}}`
}

func informational(text string) string {
	return `{"type":"system","subtype":"informational","level":"notice","uuid":"n","timestamp":"2026-01-01T12:00:00Z","content":"` + text + `"}`
}

const (
	replyLine    = `{"type":"assistant","uuid":"r","timestamp":"2026-01-01T12:00:00Z","message":{"id":"mr","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"done"}]}}`
	thinkingLine = `{"type":"assistant","uuid":"k","timestamp":"2026-01-01T12:00:00Z","message":{"id":"mk","model":"claude-opus-5-5","role":"assistant","content":[{"type":"thinking","thinking":"hm"}]}}`
	turnEndLine  = `{"type":"system","subtype":"turn_duration","uuid":"d","parentUuid":"x","timestamp":"2026-01-01T12:00:00Z","durationMs":10}`
)

// Claude Code's error field says what kind of failure it is; a gateway's 429 and an older version's reply say nothing
// the hub could not read off the words.
func TestClaudeAPIErrorKinds(t *testing.T) {
	quota := func(status string, resets any) map[string]any {
		return map[string]any{"error": "rate_limit", "apiErrorStatus": 429,
			"quotaLimits": map[string]any{"status": status, "resetsAt": resets, "rateLimitType": "five_hour"}}
	}
	for _, c := range []struct {
		name, text string
		extra      map[string]any
		kind       string
		reset      time.Time
	}{
		{"subscription limit", fixtureLimit, quota("rejected", 1767284400), proto.TurnErrorUsageLimit, fixtureReset},
		{"reset in milliseconds", fixtureLimit, quota("rejected", 1767284400000), proto.TurnErrorUsageLimit, fixtureReset},
		{"no reset", fixtureLimit, quota("rejected", 0), proto.TurnErrorUsageLimit, time.Time{}},
		{"quota not rejected", "API Error: 429", quota("allowed_warning", 1767284400), "", time.Time{}},
		{"gateway 429 (2.1.318)", "API Error: Request rejected (429) · Example gateway: quota reached.",
			map[string]any{"error": "rate_limit", "apiErrorStatus": 429, "apiErrorIsTransient": false}, "", time.Time{}},
		{"gateway quota reset", "API Error: Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at 18:02 UTC (in 12h7m)",
			map[string]any{"error": "rate_limit", "apiErrorStatus": 429, "apiErrorIsTransient": false}, proto.TurnErrorUsageLimit, time.Date(2026, 1, 1, 18, 2, 0, 0, time.UTC)},
		{"gateway capacity", "API Error: Request rejected (429) · upstream account capacity is temporarily unavailable",
			map[string]any{"error": "rate_limit", "apiErrorStatus": 429, "apiErrorIsTransient": false}, proto.TurnErrorTransient, time.Time{}},
		{"older gateway quota", "Request rejected (429) · upstream account quota usage limit has been reached; an account becomes available again at 18:02 UTC (in 12h7m)",
			nil, proto.TurnErrorUsageLimit, time.Date(2026, 1, 1, 18, 2, 0, 0, time.UTC)},
		{"older gateway capacity", "Request rejected (429) · upstream account capacity is temporarily unavailable", nil, proto.TurnErrorTransient, time.Time{}},
		{"structured user error wins", "API Error: Request rejected (429) · upstream account capacity is temporarily unavailable",
			map[string]any{"error": "authentication_failed"}, proto.TurnErrorUser, time.Time{}},
		{"sign-in (2.1.263)", "Please run /login · API Error: 403 forbidden",
			map[string]any{"error": "authentication_failed", "apiErrorStatus": 403}, proto.TurnErrorUser, time.Time{}},
		{"sign-in expired (2.1.316)", "Login expired · Please run /login", map[string]any{"error": "authentication_failed"}, proto.TurnErrorUser, time.Time{}},
		{"model", "There's an issue with the selected model.", map[string]any{"error": "model_not_found", "apiErrorStatus": 404},
			proto.TurnErrorUser, time.Time{}},
		{"overloaded", "API Error: 529 Overloaded", map[string]any{"error": "server_error", "apiErrorStatus": 529}, proto.TurnErrorTransient, time.Time{}},
		{"unknown", "API Error: 400 bad request", map[string]any{"error": "unknown", "apiErrorStatus": 400}, "", time.Time{}},
		{"older Claude Code: no error field", "API Error: 500 internal error", nil, "", time.Time{}},
	} {
		var m Meta
		ParseLine(1, []byte(prompt("u", "go")), &m)
		ParseLine(2, []byte(apiErrorLine(t, "e", "  "+c.text+"\n", c.extra)), &m)
		if m.TurnError != c.text || m.TurnErrorKind != c.kind || !m.TurnErrorResetsAt.Equal(c.reset) || m.TurnErrorAfterWork {
			t.Errorf("%s: %q %q %v after work %v; want %q %q %v", c.name, m.TurnError, m.TurnErrorKind, m.TurnErrorResetsAt,
				m.TurnErrorAfterWork, c.text, c.kind, c.reset)
		}
		if m.LastText != "  "+c.text+"\n" {
			t.Errorf("%s: the reply is no longer the last words: %q", c.name, m.LastText)
		}
	}
}

func TestClaudeTurnSucceededEvidence(t *testing.T) {
	syntheticSentinel := `{"type":"assistant","uuid":"s","timestamp":"2026-01-01T12:00:00Z","message":{"id":"ms","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"BOTMUX_NOTHING_TO_SEND"}]}}`
	sentinelReply := `{"type":"assistant","uuid":"b","timestamp":"2026-01-01T12:00:01Z","message":{"id":"mb","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"BOTMUX_NOTHING_TO_SEND"}]}}`
	failedAfterWork := []string{replyLine, apiErrorLine(t, "e", "API Error: 529 Overloaded", map[string]any{"error": "server_error"}), turnEndLine}
	for _, c := range []struct {
		name  string
		lines []string
		want  bool
	}{
		{"a visible reply and its close", []string{replyLine, turnEndLine}, true},
		{"a close without a model reply", []string{turnEndLine}, false},
		{"a synthetic-only reply", []string{syntheticSentinel, turnEndLine}, false},
		{"a reply followed by a model failure", []string{replyLine, apiErrorLine(t, "e", "API Error: 529 Overloaded", map[string]any{"error": "server_error"}), turnEndLine}, false},
		{"a model error after the close", []string{replyLine, turnEndLine, apiErrorLine(t, "e2", "API Error: 529 Overloaded", map[string]any{"error": "server_error"})}, false},
		{"an unclosed continuation after a worked failure", append(append([]string{}, failedAfterWork...), sentinelReply), false},
		{"a closed continuation after a worked failure", append(append([]string{}, failedAfterWork...), sentinelReply, turnEndLine), true},
		{"the reply has not closed", []string{replyLine}, false},
		{"a newer turn is running", []string{replyLine, turnEndLine, prompt("u2", "continue")}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var m Meta
			ParseLine(0, []byte(prompt("u1", "go")), &m)
			for i, line := range c.lines {
				ParseLine(i+1, []byte(line), &m)
			}
			if got := m.TurnSucceeded(); got != c.want {
				t.Fatalf("TurnSucceeded = %v, want %v; TurnError %q, open %v, closed %v", got, c.want, m.TurnError, m.TurnOpen, m.TurnClosed)
			}
		})
	}
}

func TestTailSettleReplaysVisibleTurnOutcome(t *testing.T) {
	const failure = "API Error: 529 Overloaded"
	bEnd := strings.Replace(strings.Replace(turnEndLine, `"uuid":"d"`, `"uuid":"db"`, 1), `"parentUuid":"x"`, `"parentUuid":"b"`, 1)
	bReply := `{"type":"assistant","uuid":"b","parentUuid":"ub","timestamp":"2026-01-01T12:00:00Z","message":{"id":"mb","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"B succeeded"}]}}`
	bTool := `{"type":"assistant","uuid":"b","parentUuid":"ub","timestamp":"2026-01-01T12:00:00Z","message":{"id":"mb","model":"claude-opus-5-5","role":"assistant","content":[{"type":"tool_use","id":"tb","name":"Bash","input":{"command":"true"}}]}}`
	for _, c := range []struct {
		name, next string
		base, drop []string
	}{
		{
			name: "hidden tool-only success",
			base: []string{prompt("u1", "A"), apiErrorLine(t, "e1", failure, map[string]any{"error": "server_error"}), turnEndLine},
			next: bTool,
		},
		{
			name: "hidden success after a newer visible error",
			base: []string{prompt("u1", "A"), replyLine, turnEndLine, apiErrorLine(t, "e2", failure, map[string]any{"error": "server_error"})},
			next: bReply,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var m Meta
			var tail Tail
			feed := func(lines []string) {
				for i, line := range lines {
					tail.Line(&m, ParseLine(i, []byte(line), &m))
				}
			}
			feed(c.base)
			tail.Rewound([]string{"ub", "b", "db"})
			feed([]string{prompt("ub", "B"), c.next, bEnd})
			if !tail.Settle(&m) {
				t.Fatal("settling the abandoned turn changed nothing")
			}
			if m.TurnSucceeded() || m.LastText != failure || m.TurnError != failure || m.TurnErrorKind == "" {
				t.Fatalf("visible outcome = last %q, error %q (%q), succeeded %v; want the visible failure",
					m.LastText, m.TurnError, m.TurnErrorKind, m.TurnSucceeded())
			}
		})
	}
}

func TestTailSettleRestoresVisibleTurnState(t *testing.T) {
	const failure = "API Error: 529 Overloaded"
	bEnd := strings.Replace(strings.Replace(turnEndLine, `"uuid":"d"`, `"uuid":"db"`, 1), `"parentUuid":"x"`, `"parentUuid":"b"`, 1)
	bReply := `{"type":"assistant","uuid":"b","parentUuid":"ub","timestamp":"2026-01-01T12:00:00Z","message":{"id":"mb","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"B succeeded"}]}}`
	bTool := `{"type":"assistant","uuid":"b","parentUuid":"ub","timestamp":"2026-01-01T12:00:00Z","message":{"id":"mb","model":"claude-opus-5-5","role":"assistant","content":[{"type":"tool_use","id":"tb","name":"Bash","input":{"command":"true"}}]}}`
	localOut := `{"type":"user","uuid":"o3","timestamp":"2026-01-01T12:00:01Z","message":{"role":"user","content":"<local-command-stdout>Usage</local-command-stdout>"}}`
	for _, c := range []struct {
		name               string
		base, middle, drop []string
		last, failure      string
		open, closed       bool
		succeeded          bool
		resumes            bool
	}{
		{name: "unclosed hidden tool turn", base: []string{prompt("u1", "A"), apiErrorLine(t, "e1", failure, map[string]any{"error": "server_error"}), turnEndLine},
			drop: []string{prompt("ub", "B"), bTool}, last: failure, failure: failure, closed: true},
		{name: "unclosed hidden working turn", base: []string{prompt("u1", "A"), replyLine, turnEndLine},
			drop: []string{prompt("ub", "B"), bReply}, last: "done", closed: true, succeeded: true},
		{name: "visible wake", base: []string{prompt("u1", "A"), apiErrorLine(t, "e1", failure, map[string]any{"error": "server_error"}), turnEndLine},
			middle: []string{wake("w1")}, drop: []string{prompt("ub", "B"), bReply, bEnd}, open: true},
		{name: "visible wake preserves automatic resume", base: []string{prompt("u1", "A"),
			apiErrorLine(t, "e1", fixtureLimit, map[string]any{"error": "rate_limit", "quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}}),
			informational("Usage limit reached · continuing automatically at 4:20pm · esc to cancel"), turnEndLine},
			middle: []string{wake("w1")}, drop: []string{prompt("ub", "B"), bReply, bEnd}, open: true, resumes: true},
		{name: "visible same-state prompt after hidden prompt", base: []string{prompt("u1", "A"), apiErrorLine(t, "e1", failure, map[string]any{"error": "server_error"}), turnEndLine},
			drop: []string{prompt("ub", "B"), prompt("uc", "C")}, open: true},
		{name: "visible local command", base: []string{prompt("u1", "A"), replyLine, turnEndLine},
			middle: []string{prompt("u3", "<command-name>/usage</command-name>"), localOut},
			drop:   []string{prompt("ub", "B"), bReply, bEnd}, closed: true, succeeded: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var m Meta
			var tail Tail
			feed := func(lines []string) {
				for i, line := range lines {
					tail.Line(&m, ParseLine(i, []byte(line), &m))
				}
			}
			feed(c.base)
			feed(c.middle)
			tail.Rewound([]string{"ub", "b", "db"})
			feed(c.drop)
			if !tail.Settle(&m) {
				t.Fatal("settling the abandoned turn changed nothing")
			}
			if m.TurnOpen != c.open || m.TurnClosed != c.closed || m.TurnSucceeded() != c.succeeded || m.TurnError != c.failure {
				t.Fatalf("open %v closed %v succeeded %v error %q; want %v/%v/%v %q",
					m.TurnOpen, m.TurnClosed, m.TurnSucceeded(), m.TurnError, c.open, c.closed, c.succeeded, c.failure)
			}
			if m.TurnErrorResumes != c.resumes {
				t.Fatalf("TurnErrorResumes = %v, want %v", m.TurnErrorResumes, c.resumes)
			}
			if c.last != "" && m.LastText != c.last {
				t.Fatalf("LastText = %q, want %q", m.LastText, c.last)
			}
		})
	}
}

func TestTailSettleDoesNotInheritHiddenResume(t *testing.T) {
	var m Meta
	var tail Tail
	feed := func(lines []string) {
		for i, line := range lines {
			tail.Line(&m, ParseLine(i, []byte(line), &m))
		}
	}
	feed([]string{prompt("u1", "A"), replyLine, turnEndLine})
	tail.Rewound([]string{"ub", "eb", "n", "db"})
	feed([]string{prompt("ub", "B"),
		apiErrorLine(t, "eb", fixtureLimit, map[string]any{"error": "rate_limit", "quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}}),
		informational("Usage limit reached · continuing automatically at 4:20pm · esc to cancel"),
		strings.Replace(strings.Replace(turnEndLine, `"uuid":"d"`, `"uuid":"db"`, 1), `"parentUuid":"x"`, `"parentUuid":"eb"`, 1)})
	feed([]string{wake("w1")})
	if !tail.Settle(&m) {
		t.Fatal("settling the abandoned limited turn changed nothing")
	}
	if m.TurnErrorResumes {
		t.Fatal("a hidden turn's automatic resume leaked into the visible wake")
	}
}

func TestTailSkippedTurnStartKeepsVisibleState(t *testing.T) {
	const failure = "API Error: 529 Overloaded"
	for _, kind := range []string{"wake", "image"} {
		t.Run(kind, func(t *testing.T) {
			var m Meta
			var tail Tail
			for i, line := range []string{prompt("u1", "A"), apiErrorLine(t, "e1", failure, map[string]any{"error": "server_error"}), turnEndLine} {
				tail.Line(&m, ParseLine(i, []byte(line), &m))
			}
			if kind == "wake" {
				tail.Skipped([]byte(`{"type":"user","uuid":"bigw","timestamp":"2026-01-01T12:00:00Z","message":{"role":"user","content":"<task-notification><task-id>w</task-id><status>completed</status><summary>` + strings.Repeat("x", 9<<10) + `</summary></task-notification>"}}`))
			} else {
				tail.Skipped([]byte(`{"type":"user","uuid":"img","parentUuid":"p","timestamp":"2026-01-01T12:00:00Z","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("A", 20<<10) + `"}}]}}`))
			}
			tail.Rewound([]string{"ub", "r", "d"})
			for i, line := range []string{prompt("ub", "B"), replyLine, turnEndLine} {
				tail.Line(&m, ParseLine(i+3, []byte(line), &m))
			}
			if !tail.Settle(&m) {
				t.Fatalf("%s skipped turn start did not settle", kind)
			}
			if !m.TurnOpen || m.TurnClosed || m.TurnError != "" {
				t.Fatalf("%s: open=%v closed=%v error=%q; want a new visible turn with no inherited failure", kind, m.TurnOpen, m.TurnClosed, m.TurnError)
			}
			if got, want := m.LastTurnWake, kind == "wake"; got != want {
				t.Fatalf("%s: LastTurnWake=%v, want %v", kind, got, want)
			}
		})
	}
}

func TestTailSkippedToolResultStaysStructural(t *testing.T) {
	var m Meta
	var tail Tail
	feed := func(lines []string) {
		for i, line := range lines {
			tail.Line(&m, ParseLine(i, []byte(line), &m))
		}
	}
	const failure = "API Error: 529 Overloaded"
	feed([]string{prompt("u1", "A"), apiErrorLine(t, "e1", failure, map[string]any{"error": "server_error"}), turnEndLine})
	bEnd := strings.Replace(strings.Replace(turnEndLine, `"uuid":"d"`, `"uuid":"db"`, 1), `"parentUuid":"x"`, `"parentUuid":"r"`, 1)
	tail.Rewound([]string{"ub", "r", "db"})
	feed([]string{prompt("ub", "B"), replyLine, bEnd})
	tail.Skipped([]byte(`{"type":"user","uuid":"big","parentUuid":"p","timestamp":"2026-01-01T12:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"` + strings.Repeat("A", 20<<10) + `"}]},"toolUseResult":{"stdout":"` + strings.Repeat("A", 20<<10) + `"}}`))
	if !tail.Settle(&m) {
		t.Fatal("the hidden turn did not settle")
	}
	if m.TurnOpen || !m.TurnClosed || m.TurnError != failure {
		t.Fatalf("a large tool result became a new turn: open=%v closed=%v error=%q", m.TurnOpen, m.TurnClosed, m.TurnError)
	}
}

func TestTailSkippedImageDoesNotInheritResume(t *testing.T) {
	var m Meta
	var tail Tail
	for i, line := range []string{prompt("u1", "A"),
		apiErrorLine(t, "e1", fixtureLimit, map[string]any{"error": "rate_limit", "quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}}),
		informational("Usage limit reached · continuing automatically at 4:20pm · esc to cancel"), turnEndLine} {
		tail.Line(&m, ParseLine(i, []byte(line), &m))
	}
	bEnd := strings.Replace(strings.Replace(turnEndLine, `"uuid":"d"`, `"uuid":"db"`, 1), `"parentUuid":"x"`, `"parentUuid":"r"`, 1)
	tail.Rewound([]string{"ub", "r", "db"})
	for i, line := range []string{prompt("ub", "B"), replyLine, bEnd} {
		tail.Line(&m, ParseLine(i+3, []byte(line), &m))
	}
	tail.Skipped([]byte(`{"type":"user","uuid":"img","parentUuid":"p","timestamp":"2026-01-01T12:00:00Z","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("A", 20<<10) + `"}}]}}`))
	if !tail.Settle(&m) {
		t.Fatal("the image turn did not settle")
	}
	if m.TurnErrorResumes || m.LastTurnWake {
		t.Fatalf("image-only inherited wake/resume state: wake=%v resumes=%v", m.LastTurnWake, m.TurnErrorResumes)
	}
	if !m.TurnOpen || m.TurnClosed || m.TurnError != "" {
		t.Fatalf("image-only did not start a fresh user turn: open=%v closed=%v error=%q", m.TurnOpen, m.TurnClosed, m.TurnError)
	}
}

func TestTailSettleKeepsSessionModelFallback(t *testing.T) {
	var m Meta
	m.ModelFallback = &proto.ModelRefusal{OriginalModel: "primary-model", FallbackModel: "fallback-model", Scope: "session"}
	var tail Tail
	tail.PriorModelFallback(m.ModelFallback)
	fallbackReplyA := strings.Replace(strings.Replace(replyLine, `"uuid":"r"`, `"uuid":"a1"`, 1), `"model":"claude-opus-5-5"`, `"model":"fallback-model"`, 1)
	fallbackReplyB := strings.Replace(strings.Replace(replyLine, `"uuid":"r"`, `"uuid":"b1"`, 1), `"model":"claude-opus-5-5"`, `"model":"fallback-model"`, 1)
	for i, line := range []string{prompt("u1", "A"), fallbackReplyA, turnEndLine} {
		tail.Line(&m, ParseLine(i, []byte(line), &m))
	}
	bEnd := strings.Replace(strings.Replace(turnEndLine, `"uuid":"d"`, `"uuid":"db"`, 1), `"parentUuid":"x"`, `"parentUuid":"b1"`, 1)
	tail.Rewound([]string{"ub", "b1", "db"})
	for i, line := range []string{prompt("ub", "B"), fallbackReplyB, bEnd} {
		tail.Line(&m, ParseLine(i+3, []byte(line), &m))
	}
	if !tail.Settle(&m) {
		t.Fatal("the fallback did not settle")
	}
	if got := m.ModelFallback; got == nil || got.OriginalModel != "primary-model" || got.FallbackModel != "fallback-model" || got.Scope != "session" {
		t.Fatalf("the pre-tail session fallback was lost: %+v", got)
	}
}

// Whether Claude Code still goes on by itself once the limit resets: its notices say it and take it back, a wake
// leaves it, a message cancels it, a reply or another kind of failure ends it.
func TestClaudeLimitResumes(t *testing.T) {
	limit := apiErrorLine(t, "e", fixtureLimit, map[string]any{"error": "rate_limit", "apiErrorStatus": 429,
		"quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}})
	armed := informational("Usage limit reached · continuing automatically at 4:20pm · esc or type to cancel")
	for _, c := range []struct {
		name    string
		lines   []string
		resumes bool
		err     bool // the turn's error is still there
	}{
		{"the limit alone", []string{limit}, false, true},
		{"the limit and its notice", []string{limit, armed, turnEndLine}, true, true},
		{"2.1.319's wording", []string{limit, informational("Usage limit reached · continuing automatically at 4:20pm · esc to cancel")}, true, true},
		{"a wake refused again", []string{limit, armed, turnEndLine, wake("w1"), limit, turnEndLine, wake("w2"), limit}, true, true},
		{"a wake still under way", []string{limit, armed, turnEndLine, wake("w1")}, true, false},
		{"a typed message", []string{limit, armed, turnEndLine, prompt("u2", "continue"), limit}, false, true},
		{"esc", []string{limit, armed, informational("Automatic continue cancelled · /rate-limit-options to re-arm")}, false, true},
		{"turned off", []string{limit, armed, informational("Automatic continue was turned off · this task will not resume on its own")}, false, true},
		{"too far out", []string{limit, armed, informational("Automatic continue stopped · the usage limit now resets more than 24 hours out, so this task will not resume on its own")}, false, true},
		{"repeated hits", []string{limit, armed, informational("Automatic continue stopped after repeated usage limit hits · This task will not resume on its own")}, false, true},
		{"slept past the reset", []string{limit, armed, informational("Usage limit has reset · Press enter to continue")}, false, true},
		{"re-armed", []string{limit, armed, informational("Automatic continue cancelled · /rate-limit-options to re-arm"), limit,
			informational("Usage limit reached again · continuing automatically at 9:00pm · esc to cancel")}, true, true},
		{"the reset", []string{limit, armed, turnEndLine, informational("Usage limit reset · Continuing automatically")}, true, true},
		{"available again", []string{limit, armed, informational("Usage limit available again · continuing now")}, true, true},
		{"its continuation answers", []string{limit, armed, turnEndLine, informational("Usage limit reset · continuing automatically"), thinkingLine}, false, false},
		{"another failure", []string{limit, armed, turnEndLine, apiErrorLine(t, "e2", "API Error: 529 Overloaded", map[string]any{"error": "server_error"})}, false, true},
		{"a local command", []string{limit, armed, turnEndLine, prompt("u3", "<command-name>/usage</command-name>"),
			`{"type":"user","uuid":"o3","timestamp":"2026-01-01T12:00:01Z","message":{"role":"user","content":"<local-command-stdout>Usage</local-command-stdout>"}}`}, true, true},
		{"a subagent's notice", []string{limit, `{"type":"system","subtype":"informational","isSidechain":true,"uuid":"n","content":"Usage limit reached · continuing automatically at 4:20pm · esc to cancel"}`}, false, true},
	} {
		var m Meta
		ParseLine(0, []byte(prompt("u1", "go")), &m)
		for i, l := range c.lines {
			ParseLine(i+1, []byte(l), &m)
		}
		if m.TurnErrorResumes != c.resumes || (m.TurnError != "") != c.err {
			t.Errorf("%s: resumes %v, error %q; want resumes %v, an error %v", c.name, m.TurnErrorResumes, m.TurnError, c.resumes, c.err)
		}
	}
}

// AfterWork: the failed turn did something before the error (a continuation the limit refused again did not); a turn
// that ended between does not lend its work to Claude Code's own continuation.
func TestClaudeTurnErrorAfterWork(t *testing.T) {
	limit := apiErrorLine(t, "e", fixtureLimit, map[string]any{"error": "rate_limit", "quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}})
	autoPrompt := `{"type":"user","uuid":"c","timestamp":"2026-01-01T12:00:00Z","isMeta":true,"origin":{"kind":"auto-continuation"},"message":{"role":"user","content":"Continue."}}`
	for _, c := range []struct {
		name  string
		lines []string
		after bool
	}{
		{"refused at once", []string{prompt("u", "go"), limit}, false},
		{"after thinking", []string{prompt("u", "go"), thinkingLine, limit}, true},
		{"after a tool call", []string{prompt("u", "go"), `{"type":"assistant","uuid":"a","message":{"id":"ma","role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{"command":"ls"}}]}}`, limit}, true},
		{"after a reply, in the next turn", []string{prompt("u", "go"), replyLine, turnEndLine, wake("w"), limit}, false},
		{"its own continuation, refused again", []string{prompt("u", "go"), thinkingLine, limit, turnEndLine, autoPrompt, limit}, false},
		{"after a subagent's work", []string{prompt("u", "go"), `{"type":"assistant","isSidechain":true,"uuid":"s","message":{"id":"ms","role":"assistant","content":[{"type":"text","text":"x"}]}}`, limit}, false},
	} {
		var m Meta
		for i, l := range c.lines {
			ParseLine(i, []byte(l), &m)
		}
		if m.TurnError == "" || m.TurnErrorAfterWork != c.after {
			t.Errorf("%s: error %q, after work %v; want %v", c.name, m.TurnError, m.TurnErrorAfterWork, c.after)
		}
	}
}

// A subagent's transcript (every line a sidechain) is its own conversation: its limit is its error, as its words are
// its last text. In the session's transcript the same line is not the session's.
func TestClaudeTurnErrorSidechain(t *testing.T) {
	line := strings.Replace(apiErrorLine(t, "e", fixtureLimit, map[string]any{"error": "rate_limit",
		"quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}}), `"isSidechain":false`, `"isSidechain":true`, 1)
	session := Meta{}
	ParseLine(1, []byte(line), &session)
	if session.TurnError != "" {
		t.Fatalf("a subagent's limit became the session's: %q", session.TurnError)
	}
	sub := Meta{Sidechain: true}
	ParseLine(1, []byte(line), &sub)
	if sub.TurnError != fixtureLimit || sub.TurnErrorKind != proto.TurnErrorUsageLimit || !sub.TurnErrorResetsAt.Equal(fixtureReset) {
		t.Fatalf("the subagent's own transcript: %q %q %v", sub.TurnError, sub.TurnErrorKind, sub.TurnErrorResetsAt)
	}
}

// The quick read's tail: a turn gone back over in the terminal ended on the limit; the conversation left did not.
func TestTailSettlesARewoundTurnError(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u1","parentUuid":null,"timestamp":"2026-01-01T12:00:00Z","message":{"role":"user","content":"one"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-01-01T12:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"answer one"}]}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"t1","parentUuid":"a1","timestamp":"2026-01-01T12:00:06Z","durationMs":6000}`,
		`{"type":"user","uuid":"u2","parentUuid":"t1","timestamp":"2026-01-01T12:10:00Z","message":{"role":"user","content":"two"}}`,
		`{"type":"assistant","uuid":"k2","parentUuid":"u2","timestamp":"2026-01-01T12:10:05Z","message":{"id":"m2","role":"assistant","content":[{"type":"thinking","thinking":"hm"}]}}`,
		strings.Replace(apiErrorLine(t, "e2", fixtureLimit, map[string]any{"parentUuid": "k2", "error": "rate_limit",
			"quotaLimits": map[string]any{"status": "rejected", "resetsAt": 1767284400}}), `"timestamp":"2026-01-01T12:00:00Z"`, `"timestamp":"2026-01-01T12:10:09Z"`, 1),
		informational("Usage limit reached · continuing automatically at 4:20pm · esc to cancel"),
		`{"type":"system","subtype":"turn_duration","uuid":"t2","parentUuid":"e2","timestamp":"2026-01-01T12:10:10Z","durationMs":10000}`,
	}
	read := func(lines []string) Meta {
		var tail Tail
		var m Meta
		for i, l := range lines {
			tail.Line(&m, ParseLine(i, []byte(l), &m))
		}
		tail.Settle(&m)
		return m
	}
	if m := read(lines); m.TurnError != fixtureLimit || !m.TurnErrorResumes {
		t.Fatalf("before the rewind: %q, resumes %v", m.TurnError, m.TurnErrorResumes)
	}
	recap := `{"type":"system","subtype":"away_summary","uuid":"s1","parentUuid":"t1","timestamp":"2026-01-01T12:13:00Z","content":"recap"}`
	if m := read(append(lines, recap)); m.TurnError != "" || m.TurnErrorKind != "" || !m.TurnErrorResetsAt.IsZero() || m.TurnErrorResumes ||
		!m.TurnSucceeded() || m.LastText != "answer one" {
		t.Fatalf("after the rewind: %q %q %v resumes %v, succeeded %v, last text %q", m.TurnError, m.TurnErrorKind, m.TurnErrorResetsAt, m.TurnErrorResumes, m.TurnSucceeded(), m.LastText)
	}
}

// A Codex usage limit resets when the windows the rollout last saw full do: the latest of them, never the message's
// "try again at …" (no time zone). A model's own limit, a window not full and a sample that reset already say nothing.
func TestCodexUsageLimitReset(t *testing.T) {
	const limited = `{"timestamp":"2026-01-01T12:00:09.800Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1",` +
		`"error":{"message":"You've hit your usage limit. Try again at 5:00 PM.","codex_error_info":"usage_limit_exceeded"}}}`
	limits := func(id string, primary, secondary float64) string {
		return `{"timestamp":"2026-01-01T12:00:09.500Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{` +
			`"limit_id":` + id + `,"primary":{"used_percent":` + jsonNum(primary) + `,"window_minutes":300,"resets_at":1767286800},` +
			`"secondary":{"used_percent":` + jsonNum(secondary) + `,"window_minutes":10080,"resets_at":1767690000}}}}`
	}
	started := `{"timestamp":"2026-01-01T12:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`
	agent := `{"timestamp":"2026-01-01T12:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"a1","content":[{"type":"text","text":"working"}]}}}`
	fiveHour, weekly := time.Date(2026, 1, 1, 17, 0, 0, 0, time.UTC), time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		lines []string
		reset time.Time
		after bool
	}{
		{"the 5-hour window full", []string{started, agent, limits(`"codex"`, 100, 41), limited}, fiveHour, true},
		{"both full: the later", []string{started, limits(`null`, 100, 100), limited}, weekly, false},
		{"none full", []string{started, limits(`"codex"`, 80, 41), limited}, time.Time{}, false},
		{"no rate limits", []string{started, agent, limited}, time.Time{}, true},
		{"a model's own limit", []string{started, limits(`"premium"`, 100, 100), limited}, time.Time{}, false},
		{"the account's, then a model's", []string{limits(`"codex"`, 100, 41), started, limits(`"premium"`, 10, 10), limited}, fiveHour, false},
		{"reset before the turn ended", []string{started, limits(`"codex"`, 100, 41),
			strings.Replace(limited, "2026-01-01T12:00:09.800Z", "2026-01-01T18:00:00Z", 1)}, time.Time{}, false},
	} {
		var m Meta
		for i, l := range c.lines {
			ParseLine(i, []byte(l), &m)
		}
		if m.TurnErrorKind != proto.TurnErrorUsageLimit || !m.TurnErrorResetsAt.Equal(c.reset) || m.TurnErrorAfterWork != c.after {
			t.Errorf("%s: %q reset %v after work %v; want reset %v after work %v", c.name, m.TurnErrorKind, m.TurnErrorResetsAt,
				m.TurnErrorAfterWork, c.reset, c.after)
		}
		ParseLine(len(c.lines), []byte(strings.Replace(started, "t1", "t2", 1)), &m)
		if m.TurnError != "" || !m.TurnErrorResetsAt.IsZero() || m.TurnErrorAfterWork {
			t.Errorf("%s: the next turn keeps %q %v %v", c.name, m.TurnError, m.TurnErrorResetsAt, m.TurnErrorAfterWork)
		}
	}
	// Another failure has no reset, whatever the windows say.
	var m Meta
	for i, l := range []string{started, limits(`"codex"`, 100, 41), strings.Replace(limited, `"usage_limit_exceeded"`, `"server_overloaded"`, 1)} {
		ParseLine(i, []byte(l), &m)
	}
	if m.TurnErrorKind != proto.TurnErrorTransient || !m.TurnErrorResetsAt.IsZero() {
		t.Fatalf("overloaded: %q %v", m.TurnErrorKind, m.TurnErrorResetsAt)
	}
}

func jsonNum(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestUpstreamErrorStreakAndCode(t *testing.T) {
	const (
		err503  = "API Error: 503 Service Unavailable"
		errPool = "API Error: 503 no eligible upstream account is currently available"
		loopHit = `{"type":"system","subtype":"scheduled_task_fire","uuid":"lf","timestamp":"2026-01-01T12:05:00Z","prompt":"check build"}`
	)
	fail503 := func(id string) string {
		return apiErrorLine(t, id, err503, map[string]any{"error": "server_error", "apiErrorStatus": 503})
	}
	failPool := func(id string) string {
		return apiErrorLine(t, id, errPool, map[string]any{"error": "server_error", "apiErrorStatus": 503})
	}

	// 1. Single transient 503 stays at streak 1 with empty TurnErrorCode; duplicate error record in same turn does not double-count.
	var m Meta
	for i, l := range []string{prompt("u1", "go"), fail503("e1"), fail503("e1b"), turnEndLine} {
		ParseLine(i, []byte(l), &m)
	}
	if m.UpstreamErrorStreak != 1 || m.TurnErrorCode != "" {
		t.Fatalf("single 503: streak=%d code=%q, want 1 / empty", m.UpstreamErrorStreak, m.TurnErrorCode)
	}

	// 2. Second consecutive 503 after a /loop wake crosses threshold >= 2 and marks TurnErrorUpstreamUnavailable.
	for i, l := range []string{loopHit, fail503("e2"), turnEndLine} {
		ParseLine(10+i, []byte(l), &m)
	}
	if m.UpstreamErrorStreak != 2 || m.TurnErrorCode != proto.TurnErrorUpstreamUnavailable {
		t.Fatalf("second 503 after /loop wake: streak=%d code=%q", m.UpstreamErrorStreak, m.TurnErrorCode)
	}

	// 3. Substantive reply resets the streak so a single 503 afterwards is unmarked again.
	for i, l := range []string{prompt("u3", "retry"), replyLine, turnEndLine, prompt("u4", "next"), fail503("e4"), turnEndLine} {
		ParseLine(20+i, []byte(l), &m)
	}
	if m.UpstreamErrorStreak != 1 || m.TurnErrorCode != "" {
		t.Fatalf("after recovery + single 503: streak=%d code=%q, want 1 / empty", m.UpstreamErrorStreak, m.TurnErrorCode)
	}

	// 4. A turn that did work before failing resets the streak instead of triggering >= 2.
	for i, l := range []string{prompt("u5", "work"), thinkingLine, fail503("e5"), turnEndLine} {
		ParseLine(30+i, []byte(l), &m)
	}
	if m.UpstreamErrorStreak != 0 || m.TurnErrorCode != "" || !m.TurnErrorAfterWork {
		t.Fatalf("503 after work: streak=%d code=%q afterWork=%v", m.UpstreamErrorStreak, m.TurnErrorCode, m.TurnErrorAfterWork)
	}

	// 5. Explicit account-pool exhaustion marks TurnErrorUpstreamUnavailable on the first turn.
	var pool Meta
	for i, l := range []string{prompt("up", "go"), failPool("ep"), turnEndLine} {
		ParseLine(i, []byte(l), &pool)
	}
	if pool.TurnErrorCode != proto.TurnErrorUpstreamUnavailable {
		t.Fatalf("pool exhaustion on turn 1: code=%q", pool.TurnErrorCode)
	}

	// 6. Tail.Settle rewind drops a rewound second 503 turn back to streak 1 and empty code.
	var rm Meta
	var tail Tail
	end2 := strings.Replace(strings.Replace(turnEndLine, `"uuid":"d"`, `"uuid":"d2"`, 1), `"parentUuid":"x"`, `"parentUuid":"e2"`, 1)
	for i, l := range []string{prompt("u1", "A"), fail503("e1"), turnEndLine} {
		tail.Line(&rm, ParseLine(i, []byte(l), &rm))
	}
	tail.Rewound([]string{"u2", "e2", "d2"})
	for i, l := range []string{prompt("u2", "B"), fail503("e2"), end2} {
		tail.Line(&rm, ParseLine(10+i, []byte(l), &rm))
	}
	if !tail.Settle(&rm) {
		t.Fatal("rewind of second 503 did not settle")
	}
	if rm.UpstreamErrorStreak != 1 || rm.TurnErrorCode != "" {
		t.Fatalf("after rewinding second 503: streak=%d code=%q, want 1 / empty", rm.UpstreamErrorStreak, rm.TurnErrorCode)
	}

	// 7. Codex rollout: single 503 stays unmarked, second consecutive 503 marks upstream_unavailable, AgentMessage resets.
	var cx Meta
	cxStart := func(id string) string {
		return `{"timestamp":"2026-01-01T12:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"` + id + `"}}`
	}
	cxFail := func(id string) string {
		return `{"timestamp":"2026-01-01T12:00:02Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"` + id + `","error":{"message":"unexpected status 503 Service Unavailable","codex_error_info":"other"}}}`
	}
	cxMsg := `{"timestamp":"2026-01-01T12:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"am1","content":[{"type":"text","text":"fixed"}]}}}`
	cxOK := `{"timestamp":"2026-01-01T12:00:06Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t3"}}`
	ParseLine(0, []byte(cxStart("t1")), &cx)
	ParseLine(1, []byte(cxFail("t1")), &cx)
	if cx.UpstreamErrorStreak != 1 || cx.TurnErrorCode != "" {
		t.Fatalf("codex turn 1: streak=%d code=%q", cx.UpstreamErrorStreak, cx.TurnErrorCode)
	}
	ParseLine(2, []byte(cxStart("t2")), &cx)
	ParseLine(3, []byte(cxFail("t2")), &cx)
	if cx.UpstreamErrorStreak != 2 || cx.TurnErrorCode != proto.TurnErrorUpstreamUnavailable {
		t.Fatalf("codex turn 2: streak=%d code=%q", cx.UpstreamErrorStreak, cx.TurnErrorCode)
	}
	ParseLine(4, []byte(cxStart("t3")), &cx)
	ParseLine(5, []byte(cxMsg), &cx)
	ParseLine(6, []byte(cxOK), &cx)
	if cx.UpstreamErrorStreak != 0 || cx.TurnErrorCode != "" {
		t.Fatalf("codex recovery: streak=%d code=%q", cx.UpstreamErrorStreak, cx.TurnErrorCode)
	}
}
