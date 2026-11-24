package node

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The quick read parses a transcript's head (to its first prompt) and its tail. Read into one Meta, the first
// prompt's time, its open turn and its mode were the session's latest whenever the tail held no exchange, and one
// line can fill a tail (#5896): the row's updated_at jumped back to the first message (days, for a session days
// old), it showed as working after every turn had ended, in the mode it was started in.

// twoTurns are two finished turns three days apart, the first in plan mode and the second accepting edits; the
// second answer asks the user a question.
var twoTurns = []string{
	`{"type":"user","uuid":"u1","parentUuid":null,"timestamp":"2026-11-01T08:00:00Z","cwd":"/src/app","permissionMode":"plan","message":{"role":"user","content":"first question"}}`,
	`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-11-01T08:00:05Z","cwd":"/src/app","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"first answer"}]}}`,
	`{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1","timestamp":"2026-11-01T08:00:06Z","cwd":"/src/app","durationMs":6000}`,
	`{"type":"user","uuid":"u2","parentUuid":"d1","timestamp":"2026-11-04T01:00:00Z","cwd":"/src/app/web","permissionMode":"acceptEdits","message":{"role":"user","content":"second question"}}`,
	`{"type":"assistant","uuid":"a2","parentUuid":"u2","timestamp":"2026-11-04T01:00:05Z","cwd":"/src/app/web","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"Both are done.\n\nWhich one should ship first?"}]}}`,
	`{"type":"system","subtype":"turn_duration","uuid":"d2","parentUuid":"a2","timestamp":"2026-11-04T01:00:06Z","cwd":"/src/app/web","durationMs":6000}`,
}

var (
	firstPrompt  = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	secondPrompt = time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	secondAnswer = time.Date(2026, 11, 4, 1, 0, 5, 0, time.UTC)
)

// requestBlob is a line of Claude Code's that is no exchange and fills a tail read by itself.
func requestBlob() string {
	return `{"type":"api-request-blob","sessionId":"s","blob":"` + strings.Repeat("x", quickTail+1) + `"}`
}

// A recap written while the session is idle: its request fills the tail, and the recap and the title after it are
// no exchange. The session is as its second turn left it, read for the first time or again.
func TestQuickMetaTailWithoutAnExchange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := append(twoTurns[:len(twoTurns):len(twoTurns)], requestBlob(),
		`{"type":"system","subtype":"away_summary","uuid":"r1","parentUuid":"d2","timestamp":"2026-11-04T03:00:00Z","cwd":"/src/app/web","content":"You asked two questions; both are answered."}`,
		`{"type":"ai-title","sessionId":"s","aiTitle":"Two questions"}`)
	before := readQuickMeta(path, writeLines(t, path, twoTurns)) // the node's read before the recap
	st := writeLines(t, path, lines)
	written := time.Date(2026, 11, 4, 3, 0, 1, 0, time.UTC)
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]quickMeta{"first read": readQuickMeta(path, st), "read again": readQuickMetaSince(path, st, before)} {
		if got := lastUsed(m, st); !got.Equal(secondAnswer) {
			t.Errorf("%s: last used %v, want the last exchange %v (the first prompt was at %v)", name, got, secondAnswer, firstPrompt)
		}
		if m.claudeTurn || !m.turnClosed {
			t.Errorf("%s: turn open %v, closed %v: both turns ended and the recap starts none", name, m.claudeTurn, m.turnClosed)
		}
		if m.mode != "acceptEdits" || !m.modeAt.Equal(secondPrompt) {
			t.Errorf("%s: mode %q at %v, want the second prompt's", name, m.mode, m.modeAt)
		}
		if !m.lastPromptAt.Equal(secondPrompt) || !m.lastTextAt.Equal(secondAnswer) {
			t.Errorf("%s: last prompt %v, last words %v", name, m.lastPromptAt, m.lastTextAt)
		}
		// The question outlasts the recap: no message came in after it (#5096).
		if m.lastText != "Both are done.\n\nWhich one should ship first?" || m.asks != "Which one should ship first?" {
			t.Errorf("%s: last text %q, asks %q", name, m.lastText, m.asks)
		}
		// What the head is read for stays the head's.
		if m.first != "first question" || m.start != "/src/app" || m.title != "Two questions" {
			t.Errorf("%s: first %q, started in %q, title %q", name, m.first, m.start, m.title)
		}
	}
}

// The transcript's last exchange is a line bigger than the tail (a screenshot's result), which the tail's start
// cuts: it is the last exchange, and its turn is still open. With a hook's summary after it, as Claude Code writes
// when the turn stops there, the same.
func TestQuickMetaLastExchangeBiggerThanTheTail(t *testing.T) {
	result := time.Date(2026, 11, 4, 1, 0, 7, 0, time.UTC)
	shot := []string{
		`{"type":"assistant","uuid":"a3","parentUuid":"u3","timestamp":"2026-11-04T01:00:05Z","message":{"id":"m3","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"mcp__browser__screenshot","input":{}}]}}`,
		`{"type":"user","uuid":"r3","parentUuid":"a3","timestamp":"2026-11-04T01:00:07Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` +
			strings.Repeat("A", 900<<10) + `"}}]}]}}`,
	}
	prompt := `{"type":"user","uuid":"u3","parentUuid":"d1","timestamp":"2026-11-04T01:00:00Z","message":{"role":"user","content":"take a screenshot of the page"}}`
	for name, after := range map[string][]string{
		"nothing after it": nil,
		"a hook's summary": {`{"type":"system","subtype":"stop_hook_summary","uuid":"h1","parentUuid":"r3","timestamp":"2026-11-04T01:00:09Z","hookCount":1}`,
			`{"type":"last-prompt","lastPrompt":"take a screenshot of the page","sessionId":"s"}`},
	} {
		path := filepath.Join(t.TempDir(), "s.jsonl")
		st := writeLines(t, path, append(append(append(twoTurns[:3:3], prompt), shot...), after...))
		m := readQuickMeta(path, st)
		if !m.activity.Equal(result) {
			t.Errorf("%s: last exchange %v, want the screenshot's result %v (the first prompt was at %v)", name, m.activity, result, firstPrompt)
		}
		if !m.claudeTurn || m.turnClosed {
			t.Errorf("%s: turn open %v, closed %v: the turn the screenshot is in has not ended", name, m.claudeTurn, m.turnClosed)
		}
		if want := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC); !m.lastPromptAt.Equal(want) || m.lastText != "first answer" {
			t.Errorf("%s: last prompt %v, last text %q", name, m.lastPromptAt, m.lastText)
		}
	}
}

// Through the list of sessions not running: a session used a minute ago whose last line is a screenshot's result
// is listed as used a minute ago, not when it was started.
func TestOfflineRowIsLastUsedAtItsLastExchange(t *testing.T) {
	n, _ := claudeHome(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	const id = "dddddddd-0000-0000-0000-00000000a001"
	now := time.Now().UTC().Truncate(time.Second)
	started, last := now.Add(-72*time.Hour), now.Add(-time.Minute)
	at := func(t time.Time) string { return t.Format(time.RFC3339) }
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"` + at(started) + `","cwd":"/src/app","message":{"role":"user","content":"first question"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"` + at(started.Add(5*time.Second)) + `","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"first answer"}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","timestamp":"` + at(last.Add(-10*time.Second)) + `","message":{"role":"user","content":"take a screenshot"}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"u2","timestamp":"` + at(last.Add(-5*time.Second)) + `","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"mcp__browser__screenshot","input":{}}]}}`,
		`{"type":"user","uuid":"r2","parentUuid":"a2","timestamp":"` + at(last) + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` +
			strings.Repeat("A", 500<<10) + `"}}]}]}}`,
	}
	writeLines(t, filepath.Join(n.home, ".claude", "projects", "-src-app", id+".jsonl"), lines)
	for _, s := range n.scanOffline() {
		if s.SessionID != id {
			continue
		}
		if !s.UpdatedAt.Equal(last) {
			t.Fatalf("updated_at %v, want its last exchange %v (it started at %v)", s.UpdatedAt, last, started)
		}
		return
	}
	t.Fatal("the session is not listed")
}

// A long turn: its prompt is soon out of the tail, and the tail then starts and ends no turn. Read as the node reads
// a growing transcript (each read from the last), the turn stays open, in the mode and at the time of its own prompt;
// a first read after it (a node just started) finds the same further back. Then the turn ends, and lines that are no
// exchange push its end out of the tail: it stays ended.
func TestQuickMetaTurnStandsWhenTheTailSaysNothingOfIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := append(twoTurns[:3:3],
		`{"type":"user","uuid":"u2","parentUuid":"d1","timestamp":"2026-11-04T01:00:00Z","permissionMode":"acceptEdits","message":{"role":"user","content":"read every file"}}`)
	last := time.Time{}
	step := func(i int) {
		at := secondPrompt.Add(time.Duration(i+1) * time.Minute)
		last = at.Add(time.Second)
		lines = append(lines,
			fmt.Sprintf(`{"type":"assistant","uuid":"a%d","timestamp":%q,"message":{"id":"t%d","role":"assistant","content":[{"type":"tool_use","id":"c%d","name":"Read","input":{"file_path":"/src/app/f%d.go"}}]}}`, 10+i, at.Format(time.RFC3339), i, i, i),
			fmt.Sprintf(`{"type":"user","uuid":"r%d","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"c%d","content":"%s"}]}}`, 10+i, last.Format(time.RFC3339), i, strings.Repeat("x", 60<<10)))
	}
	check := func(name string, m quickMeta, open bool) {
		t.Helper()
		if m.claudeTurn != open || m.turnClosed == open {
			t.Errorf("%s: turn open %v, closed %v", name, m.claudeTurn, m.turnClosed)
		}
		if !m.activity.Equal(last) || !m.lastPromptAt.Equal(secondPrompt) || m.mode != "acceptEdits" || !m.modeAt.Equal(secondPrompt) {
			t.Errorf("%s: last exchange %v (want %v), last prompt %v, mode %q at %v", name, m.activity, last, m.lastPromptAt, m.mode, m.modeAt)
		}
		if m.first != "first question" || m.start != "/src/app" {
			t.Errorf("%s: first %q, started in %q", name, m.first, m.start)
		}
	}
	m := readQuickMeta(path, writeLines(t, path, lines))
	for i := range 10 { // 600 KB of tool results, a read after each
		step(i)
		m = readQuickMetaSince(path, writeLines(t, path, lines), m)
	}
	st, _ := os.Stat(path)
	if st.Size() < quickTail+200<<10 {
		t.Fatalf("the prompt has to be out of the tail: %d bytes", st.Size())
	}
	check("read as it grew", m, true)
	check("first read", readQuickMeta(path, st), true)

	// The turn ends; hook receipts (no exchange, no turn) then fill the tail.
	lines = append(lines,
		fmt.Sprintf(`{"type":"assistant","uuid":"a9","timestamp":%q,"message":{"id":"m9","role":"assistant","content":[{"type":"text","text":"Read them all."}]}}`, last.Add(time.Second).Format(time.RFC3339)),
		fmt.Sprintf(`{"type":"system","subtype":"turn_duration","uuid":"d9","parentUuid":"a9","timestamp":%q,"durationMs":600000}`, last.Add(2*time.Second).Format(time.RFC3339)))
	last = last.Add(time.Second)
	m = readQuickMetaSince(path, writeLines(t, path, lines), m)
	check("ended", m, false)
	for i := range 8 {
		lines = append(lines, fmt.Sprintf(`{"type":"system","subtype":"async_hook_response","uuid":"h%d","timestamp":%q,"content":"%s"}`, i,
			last.Add(time.Duration(i+1)*time.Hour).Format(time.RFC3339), strings.Repeat("y", 60<<10)))
		m = readQuickMetaSince(path, writeLines(t, path, lines), m)
	}
	check("its end out of the tail, read as it grew", m, false)
	st, _ = os.Stat(path)
	check("its end out of the tail, first read", readQuickMeta(path, st), false)
	if m.lastText != "Read them all." {
		t.Errorf("last text %q", m.lastText)
	}
}

// A session whose one prompt is its last exchange: the first prompt's time is the right one then, found before the
// tail like any other.
func TestQuickMetaFirstPromptIsTheLastExchangeWhenItIsTheOnlyOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	m := readQuickMeta(path, writeLines(t, path, []string{twoTurns[0], requestBlob()}))
	if !m.activity.Equal(firstPrompt) || !m.lastPromptAt.Equal(firstPrompt) || !m.claudeTurn || m.mode != "plan" || m.first != "first question" {
		t.Fatalf("last exchange %v, last prompt %v, turn open %v, mode %q, first %q", m.activity, m.lastPromptAt, m.claudeTurn, m.mode, m.first)
	}
}

// A Codex rollout says its directory, version and client on its first line only: they stay when a turn has written
// more than the tail, and so does the turn (codex_more_test.go). Its last exchange is the turn's latest item, not the
// first prompt, when only tool output (no item) fills the tail.
func TestQuickMetaRolloutLongerThanTheTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-2026-11-01T08-00-00-"+testThread+".jsonl")
	lines := []string{
		`{"timestamp":"2026-11-01T08:00:00Z","type":"session_meta","payload":{"id":"` + testThread + `","cwd":"/work","cli_version":"0.170.0","originator":"codex_cli_rs"}}`,
		`{"timestamp":"2026-11-01T08:00:00Z","type":"turn_context","payload":{"cwd":"/work","model":"gpt-5.5","approval_policy":"on-request","sandbox_policy":{"type":"workspace-write"}}}`,
		`{"timestamp":"2026-11-01T08:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"2026-11-01T08:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"build it"}]}}}`,
		`{"timestamp":"2026-11-01T08:00:09Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`,
		`{"timestamp":"2026-11-04T01:00:00Z","type":"turn_context","payload":{"cwd":"/work","model":"gpt-5.5","approval_policy":"never","sandbox_policy":{"type":"danger-full-access"}}}`,
		`{"timestamp":"2026-11-04T01:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}`,
		`{"timestamp":"2026-11-04T01:00:00Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u2","content":[{"type":"text","text":"now test it"}]}}}`,
		`{"timestamp":"2026-11-04T01:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"m2","content":[{"type":"Text","text":"Running the tests."}]}}}`,
	}
	for range 8 { // 512 KB of a command's output: no item
		lines = append(lines, `{"timestamp":"2026-11-04T01:00:06Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"`+strings.Repeat("x", 64<<10)+`"}}`)
	}
	before := readQuickMeta(path, writeLines(t, path, lines[:9]))
	st := writeLines(t, path, lines)
	for name, m := range map[string]quickMeta{"first read": readQuickMeta(path, st), "read again": readQuickMetaSince(path, st, before)} {
		if !m.codex || m.cwd != "/work" || m.version != "0.170.0" || m.surface == "" || m.first != "build it" || m.model != "gpt-5.5" {
			t.Errorf("%s: codex %v, cwd %q, version %q, surface %q, first %q, model %q", name, m.codex, m.cwd, m.version, m.surface, m.first, m.model)
		}
		if !m.turnOpen || m.turnClosed {
			t.Errorf("%s: turn open %v, closed %v: the second turn runs", name, m.turnOpen, m.turnClosed)
		}
		if !m.activity.Equal(secondAnswer) || !m.lastPromptAt.Equal(secondPrompt) || m.lastText != "Running the tests." {
			t.Errorf("%s: last exchange %v, last prompt %v, last text %q", name, m.activity, m.lastPromptAt, m.lastText)
		}
		if m.mode != "never" || m.sandbox != "danger-full-access" {
			t.Errorf("%s: mode %q, sandbox %q, want the second turn's", name, m.mode, m.sandbox)
		}
	}
}

// placeBoundary writes lines, a pad and tail so that the first window read back from the tail's whole lines (lookBack's
// first, quickTail long) starts at at: what is read back is then cut there.
func placeBoundary(t *testing.T, path string, lines, tail []string, at int64) os.FileInfo {
	t.Helper()
	pad := func(n int64) string {
		return `{"type":"system","subtype":"async_hook_response","content":"` + strings.Repeat("p", int(n)) + `"}`
	}
	boundary := func(n int64) int64 {
		st := writeLines(t, path, append(append(lines[:len(lines):len(lines)], pad(n)), tail...))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		off := st.Size() - quickTail
		return off + int64(bytes.IndexByte(data[off:], '\n')) - quickTail
	}
	n := 2 * int64(quickTail)
	n += at - boundary(n)
	if n < 0 || boundary(n) != at {
		t.Fatalf("cannot place the boundary at %d", at)
	}
	st, _ := os.Stat(path)
	return st
}

// boundaries are the places lookBack's windows can cut lines: the start and the middle of each of lines from the
// first'th on.
func boundaries(lines []string, first int) []int64 {
	var at []int64
	pos := int64(0)
	for i, l := range lines {
		if i >= first {
			at = append(at, pos, pos+int64(len(l))/2)
		}
		pos += int64(len(l)) + 1
	}
	return at
}

// hooks are lines that are no exchange, say nothing of a turn and fill a tail: the tail reads nothing of where the
// session stands, and lookBack reads all of it.
func hooks(from time.Time) []string {
	var out []string
	for i := range 7 {
		out = append(out, fmt.Sprintf(`{"type":"system","subtype":"async_hook_response","uuid":"h%d","timestamp":%q,"content":"%s"}`, i,
			from.Add(time.Duration(i+1)*time.Minute).Format(time.RFC3339), strings.Repeat("y", 60<<10)))
	}
	return out
}

// errorTurn is a turn that ended on an API error.
var errorTurn = []string{
	`{"type":"user","uuid":"u1","parentUuid":null,"timestamp":"2026-11-01T08:00:00Z","cwd":"/src/app","permissionMode":"plan","message":{"role":"user","content":"first question"}}`,
	`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-11-01T08:00:05Z","isApiErrorMessage":true,"error":"server_error","message":{"id":"m1","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: 500 Internal server error"}]}}`,
	`{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1","timestamp":"2026-11-01T08:00:06Z","durationMs":6000}`,
}

// lookBack reads where the session stood before the tail as one stretch of lines, whatever window cuts them: it does not
// take each "last X" from a window of its own. A long turn after one that ended on an API error has said nothing yet:
// it is open, in its own prompt's mode and time, and the error's words are the last ones. A local command after the
// error turn ends where it started: the turn stays ended, wherever a window parts the command from its output.
func TestQuickMetaLookBackKeepsTheNearestTurnWholeAcrossWindows(t *testing.T) {
	work := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	long := append(errorTurn[:3:3],
		`{"type":"user","uuid":"u2","parentUuid":"d1","timestamp":"2026-11-04T01:00:00Z","permissionMode":"acceptEdits","message":{"role":"user","content":"read every file"}}`)
	last := work
	for i := range 6 {
		at := work.Add(time.Duration(i+1) * time.Minute)
		last = at.Add(time.Second)
		long = append(long,
			fmt.Sprintf(`{"type":"assistant","uuid":"a%d","timestamp":%q,"message":{"id":"t%d","role":"assistant","content":[{"type":"tool_use","id":"c%d","name":"Read","input":{"file_path":"/src/app/f%d.go"}}]}}`, 10+i, at.Format(time.RFC3339), i, i, i),
			fmt.Sprintf(`{"type":"user","uuid":"r%d","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"c%d","content":"%s"}]}}`, 10+i, last.Format(time.RFC3339), i, strings.Repeat("x", 4<<10)))
	}
	command := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	local := append(errorTurn[:3:3],
		`{"type":"user","uuid":"c1","parentUuid":"d1","timestamp":"2026-11-01T09:00:00Z","message":{"role":"user","content":"<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args>opus</command-args>"}}`,
		`{"type":"user","uuid":"c2","parentUuid":"c1","timestamp":"2026-11-01T09:00:01Z","message":{"role":"user","content":"<local-command-stdout>Set model to opus</local-command-stdout>"}}`)

	for _, c := range []struct {
		name     string
		lines    []string
		open     bool
		activity time.Time
		prompt   time.Time
		mode     string
		tail     time.Time
	}{
		{"a long turn after an error turn", long, true, last, work, "acceptEdits", last},
		{"a local command after an error turn", local, false, command, firstPrompt, "plan", command},
	} {
		for _, at := range boundaries(c.lines, 1) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			m := readQuickMeta(path, placeBoundary(t, path, c.lines, hooks(c.tail), at))
			if m.claudeTurn != c.open || m.turnClosed == c.open {
				t.Errorf("%s, cut at %d: turn open %v, closed %v", c.name, at, m.claudeTurn, m.turnClosed)
			}
			if m.lastText != "API Error: 500 Internal server error" || !m.lastTextAt.Equal(time.Date(2026, 11, 1, 8, 0, 5, 0, time.UTC)) {
				t.Errorf("%s, cut at %d: last text %q at %v, want the error turn's", c.name, at, m.lastText, m.lastTextAt)
			}
			if !m.activity.Equal(c.activity) || !m.lastPromptAt.Equal(c.prompt) || m.mode != c.mode {
				t.Errorf("%s, cut at %d: last exchange %v (want %v), last prompt %v (want %v), mode %q (want %q)", c.name, at,
					m.activity, c.activity, m.lastPromptAt, c.prompt, m.mode, c.mode)
			}
		}
	}
}

// A turn the user went back over while it was still in the tail (a rewind in the terminal, then a recap beside its
// prompt): what the last read knew came from that turn, and none of it stands. The session is as the turn before it
// left it, ended, with its words; not working, as the last read saw the rewound turn.
func TestQuickMetaRewoundTurnInTheTailThenARecap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := append(twoTurns[:3:3], `{"type":"system","subtype":"async_hook_response","content":"`+strings.Repeat("p", quickTail)+`"}`,
		`{"type":"user","uuid":"u2","parentUuid":"d1","timestamp":"2026-11-04T01:00:00Z","permissionMode":"acceptEdits","message":{"role":"user","content":"rewrite everything"}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"u2","timestamp":"2026-11-04T01:00:05Z","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"Rewriting it all now."}]}}`)
	prev := readQuickMeta(path, writeLines(t, path, lines)) // the rewound turn, working
	if !prev.claudeTurn || prev.lastText != "Rewriting it all now." {
		t.Fatalf("the last read: turn open %v, last text %q", prev.claudeTurn, prev.lastText)
	}
	lines = append(lines,
		`{"type":"system","subtype":"turn_duration","uuid":"d2","parentUuid":"a2","timestamp":"2026-11-04T01:00:06Z","durationMs":6000}`,
		`{"type":"system","subtype":"away_summary","uuid":"s1","parentUuid":"d1","timestamp":"2026-11-04T03:00:00Z","content":"You went back to before the rewrite."}`)
	st := writeLines(t, path, lines)
	for name, m := range map[string]quickMeta{"read again": readQuickMetaSince(path, st, prev), "first read": readQuickMeta(path, st)} {
		if m.claudeTurn || !m.turnClosed {
			t.Errorf("%s: turn open %v, closed %v: the turn gone back over is not the session's", name, m.claudeTurn, m.turnClosed)
		}
		if m.lastText != "first answer" || !m.lastTextAt.Equal(time.Date(2026, 11, 1, 8, 0, 5, 0, time.UTC)) {
			t.Errorf("%s: last text %q at %v, want the first answer", name, m.lastText, m.lastTextAt)
		}
	}
}

func TestQuickMetaLateCodexCommandDoesNotMoveTurnActivity(t *testing.T) {
	for _, gap := range []bool{false, true} {
		t.Run(fmt.Sprintf("closed turn outside tail=%t", gap), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			lines := []string{
				`{"timestamp":"2026-11-04T01:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
				`{"timestamp":"2026-11-04T01:00:01Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"Check the file"}]}}}`,
				`{"timestamp":"2026-11-04T01:00:05Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m1","content":[{"type":"Text","text":"The file is ready."}]}}}`,
				`{"timestamp":"2026-11-04T01:00:06Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`,
			}
			before := readQuickMeta(path, writeLines(t, path, lines))
			if gap {
				lines = append(lines, `{"timestamp":"2026-11-04T01:02:00Z","type":"response_item","payload":{"type":"function_call_output","output":"`+strings.Repeat("x", quickTail)+`"}}`)
			}
			lines = append(lines, `{"timestamp":"2026-11-04T01:04:00Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"CommandExecution","id":"c1","status":"completed","aggregated_output":"ok"}}}`)
			st := writeLines(t, path, lines)
			for name, m := range map[string]quickMeta{"cold": readQuickMeta(path, st), "warm": readQuickMetaSince(path, st, before)} {
				if at := lastUsed(m, st); !at.Equal(secondAnswer) || !m.turnClosed || m.turnOpen {
					t.Errorf("%s: updated at %v, open %v, closed %v; want the same closed turn at %v", name, at, m.turnOpen, m.turnClosed, secondAnswer)
				}
			}
			lines = append(lines,
				`{"timestamp":"2026-11-04T01:05:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}`,
				`{"timestamp":"2026-11-04T01:05:05Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t2","item":{"type":"CommandExecution","id":"c2","status":"completed","aggregated_output":"new output"}}}`,
				`{"timestamp":"2026-11-04T01:05:06Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2"}}`,
			)
			st = writeLines(t, path, lines)
			want := time.Date(2026, 11, 4, 1, 5, 5, 0, time.UTC)
			if got := lastUsed(readQuickMeta(path, st), st); !got.Equal(want) {
				t.Fatalf("updated at %v after the new turn, want %v", got, want)
			}
		})
	}
}
