package node

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
)

func TestAgyPaneTranscriptMapping(t *testing.T) {
	// The JSONL example from issue #10236.
	const issue = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":"Show the example file"}
{"step_index":1,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:01Z","content":"I will read the example.","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/example/notes.txt"}}]}
{"step_index":2,"type":"GENERIC","status":"DONE","created_at":"2026-01-01T00:00:02Z","content":"sample content"}
{"step_index":3,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:03Z","content":"The file contains sample content."}
`
	// The ordered-steps fixture from integrations/antigravity/test/map.test.js, serialized as JSONL.
	const companion = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-05T10:00:00Z","content":"<USER_REQUEST>\nCheck files\n</USER_REQUEST>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-05T10:00:01Z","thinking":"Listing files first","content":"I will list the directory.","tool_calls":[{"name":"run_command","args":{"CommandLine":"ls -la","toolSummary":"List files"}}]}
{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-05T10:00:02Z","content":"Created At: 2026-11-05T10:00:02Z\nOutput:\ntotal 0\n"}
`
	const edges = `{"step_index":0,"type":"PLANNER_RESPONSE","created_at":"2026-01-01","tool_calls":[{"name":"replace_file_content","args":{"TargetFile":"/tmp/a.txt","TargetContent":"old","ReplacementContent":"new"}},{"name":"multi_edit","args":{"edits":[]}},{"name":"ls","args":[1,2]},{"name":"invoke_subagent","args":{}}]}
{"step_index":1,"type":"GENERIC","created_at":"not-a-time","content":"edited","media":[{"mime_type":"image/png","uri":"steps/1/image.png"}]}
{"step_index":2,"type":"GENERIC","content":"multi-edited"}
{"step_index":3,"type":"GENERIC","content":"listed"}
{"step_index":4,"type":"GENERIC","content":"delegated"}
{"step_index":5,"type":"ERROR_MESSAGE","error":"quota"}
`
	type mapped struct {
		seq              int
		role, kind, text string
		tool, input      string
		pair             int // 0 for non-tool items
	}
	for _, tc := range []struct {
		name, fixture string
		toSeq         int
		want          []mapped
	}{
		{"issue_fixture", issue, 4, []mapped{
			{seq: 1, role: "user", kind: "text", text: "Show the example file"},
			{seq: 2, role: "assistant", kind: "text", text: "I will read the example."},
			{seq: 2, role: "assistant", kind: "tool_use", tool: "Read", input: `{"file_path":"/example/notes.txt"}`, pair: 1},
			{seq: 3, role: "user", kind: "tool_result", text: "sample content", pair: 1},
			{seq: 4, role: "assistant", kind: "text", text: "The file contains sample content."},
		}},
		{"companion_fixture", companion, 3, []mapped{
			{seq: 1, role: "user", kind: "text", text: "Check files"},
			{seq: 2, role: "assistant", kind: "thinking", text: "Listing files first"},
			{seq: 2, role: "assistant", kind: "text", text: "I will list the directory."},
			{seq: 2, role: "assistant", kind: "tool_use", tool: "Bash", input: `{"command":"ls -la","description":"List files"}`, pair: 1},
			{seq: 3, role: "user", kind: "tool_result", text: "total 0\n", pair: 1},
		}},
		{"edge_cases", edges, 6, []mapped{
			{seq: 1, role: "assistant", kind: "tool_use", tool: "Edit", input: `{"file_path":"/tmp/a.txt","new_string":"new","old_string":"old"}`, pair: 1},
			{seq: 1, role: "assistant", kind: "tool_use", tool: "MultiEdit", input: `{"edits":[]}`, pair: 2},
			{seq: 1, role: "assistant", kind: "tool_use", tool: "LS", input: `[1,2]`, pair: 3},
			{seq: 1, role: "assistant", kind: "tool_use", tool: "Agent", input: `{"description":"subagent","prompt":""}`, pair: 4},
			{seq: 2, role: "user", kind: "tool_result", text: "edited", pair: 1},
			{seq: 2, role: "assistant", kind: "image", pair: 1},
			{seq: 3, role: "user", kind: "tool_result", text: "multi-edited", pair: 2},
			{seq: 4, role: "user", kind: "tool_result", text: "listed", pair: 3},
			{seq: 5, role: "user", kind: "tool_result", text: "delegated", pair: 4},
			{seq: 6, role: "assistant", kind: "text", text: "API Error: quota"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			file := agyTestFile(t, root, agyTestID, tc.fixture)
			mtime := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			if err := os.Chtimes(file, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			brain, err := agyBrain(root, agyTestID)
			if err != nil {
				t.Fatal(err)
			}
			defer brain.Close()
			var n Node
			history, _, err := n.agyRead(brain, file, agyTestID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if history.seq != tc.toSeq || len(history.items) != len(tc.want) {
				t.Fatalf("to_seq %d, items %+v", history.seq, history.items)
			}
			calls := map[string]int{}
			for i, it := range history.items {
				if it.Kind == "tool_use" {
					if it.ToolUseID == "" || calls[it.ToolUseID] != 0 {
						t.Fatalf("missing or duplicate tool-use ID: %+v", it)
					}
					calls[it.ToolUseID] = tc.want[i].pair
				}
				got := mapped{it.Seq, it.Role, it.Kind, it.Text, it.ToolName, string(it.ToolInput), calls[it.ToolUseID]}
				if got != tc.want[i] {
					t.Errorf("item %d = %+v, want %+v", i, got, tc.want[i])
				}
				if tc.name == "edge_cases" && !it.TS.Equal(mtime) {
					t.Errorf("item %d timestamp = %v, want file mtime %v", i, it.TS, mtime)
				}
			}
		})
	}
}

func TestAgyUserText(t *testing.T) {
	older := compat.ContextNames()[0] // the older context tag name, kept in compat
	for _, tc := range []struct{ input, want string }{
		{"<USER_REQUEST>\n<persona-context>\nsnapshot\n</persona-context>\n\nHello world\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\ntime\n</ADDITIONAL_METADATA>", "Hello world"},
		{"A<persona-context>x</persona-context>\u00a0<" + older + ">y</" + older + ">B", "AB"},
		{"\ufeff</PERSONA-CONTEXT>\v</" + older + "><persona-view-errors id=\"1\">hidden</persona-view-errors>\u2028Keep this\ufeff", "Keep this"},
		{"see <system-reminder>keep me</system-reminder> and <pasted_content id=\"1\">p</pasted_content>", "see <system-reminder>keep me</system-reminder> and <pasted_content id=\"1\">p</pasted_content>"},
		{"before <persona-context unclosed and after", "before <persona-context unclosed and after"},
		{"A<persona-context>x</" + older + ">B", "A<persona-context>x</" + older + ">B"},
		{"</perſona-context>Keep this", "</perſona-context>Keep this"},
	} {
		if got := agyUserText(tc.input); got != tc.want {
			t.Errorf("agyUserText(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// Array tool arguments are bounded like any other tool input (review on #10364).
func TestAgyToolArrayArgsAreClipped(t *testing.T) {
	big := `["` + strings.Repeat("x", 1<<20) + `"]`
	_, input := agyTool("view_file", json.RawMessage(big))
	if len(input) >= len(big) {
		t.Fatalf("array input not clipped: %d bytes", len(input))
	}
	if _, small := agyTool("view_file", json.RawMessage(`[1,2]`)); string(small) != `[1,2]` {
		t.Fatalf("small array changed: %s", small)
	}
}

// The pane reader follows the companion's rules (integrations/antigravity/lib/map.js): a parallel call that fails at
// once is exported before its sibling's result, and a call that never got a result must not take a later one.
func TestAgyParallelResultsOutOfOrderPairWithTheirOwnCalls(t *testing.T) {
	const early = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-01-01T00:00:00Z","content":"go"}
{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-01-01T00:00:01Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"tail log"}},{"name":"manage_task","args":{"Action":"status"}}]}
{"step_index":3,"type":"GENERIC","status":"INVALID","created_at":"2026-01-01T00:00:02Z","content":"Error invalid tool call","error":"invalid arguments"}
`
	const late = `{"step_index":2,"type":"GENERIC","status":"DONE","created_at":"2026-01-01T00:00:02Z","content":"tail output"}
{"step_index":4,"type":"PLANNER_RESPONSE","created_at":"2026-01-01T00:00:03Z","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/tmp/a"}}]}
{"step_index":5,"type":"GENERIC","status":"DONE","created_at":"2026-01-01T00:00:04Z","content":"file body"}
`
	steps, before := parseAgySteps([]byte(early), agyTestID)
	if len(steps) != 3 {
		t.Fatalf("early steps = %d", len(steps))
	}
	steps, after := parseAgySteps([]byte(early+late), agyTestID)
	var order []int
	for _, s := range steps {
		order = append(order, s.seq)
	}
	if want := []int{1, 2, 3, 4, 5, 6}; !slices.Equal(order, want) {
		t.Fatalf("step order = %v, want %v: a late step keeps its place and nothing is dropped", order, want)
	}
	if after.branch != before.branch {
		t.Errorf("a late arrival is not a rewind, but the branch went from %q to %q", before.branch, after.branch)
	}
	if after.native == before.native {
		t.Errorf("watchers must learn that earlier items changed: native stayed %q", after.native)
	}
	history := mapAgySteps(steps, time.Time{}, nil)
	results := map[string]string{}
	failed := map[string]bool{}
	for _, it := range history.items {
		if it.Kind == "tool_result" {
			results[it.ToolUseID], failed[it.ToolUseID] = it.Text, it.IsError
		}
	}
	var got []string
	var uses []string
	for _, it := range history.items {
		if it.Kind == "tool_use" {
			uses = append(uses, it.ToolUseID)
			got = append(got, results[it.ToolUseID])
		}
	}
	if want := []string{"tail output", "invalid arguments", "file body"}; !slices.Equal(got, want) {
		t.Fatalf("results by call = %q, want %q", got, want)
	}
	if !failed[uses[1]] {
		t.Error("the invalid call's result is an error")
	}
}

func TestAgyCallWithoutResultDoesNotShiftLaterResults(t *testing.T) {
	step := func(i int, typ, extra string) string {
		return `{"step_index":` + strconv.Itoa(i) + `,"type":"` + typ + `","status":"DONE","created_at":"2026-01-01T00:00:00Z"` + extra + "}\n"
	}
	calls := func(names ...string) string {
		var parts []string
		for _, n := range names {
			parts = append(parts, `{"name":"`+n+`","args":{}}`)
		}
		return `,"tool_calls":[` + strings.Join(parts, ",") + `]`
	}
	fixture := step(0, "USER_INPUT", `,"source":"USER_EXPLICIT","content":"go"`) +
		step(1, "PLANNER_RESPONSE", calls("view_file", "run_command")) +
		step(2, "GENERIC", `,"content":"first"`) + // the second call was interrupted: no result
		step(3, "USER_INPUT", `,"source":"USER_EXPLICIT","content":"again"`) +
		step(4, "PLANNER_RESPONSE", calls("view_file")) +
		step(5, "GENERIC", `,"content":"second"`) +
		step(6, "PLANNER_RESPONSE", calls("view_file", "grep_search")) +
		step(7, "SYSTEM_MESSAGE", `,"content":"a background task finished"`) +
		step(8, "GENERIC", `,"content":"third"`) +
		step(9, "GENERIC", `,"content":"fourth"`)
	steps, _ := parseAgySteps([]byte(fixture), agyTestID)
	history := mapAgySteps(steps, time.Time{}, nil)
	results := map[string]string{}
	for _, it := range history.items {
		if it.Kind == "tool_result" {
			results[it.ToolUseID] = it.Text
		}
	}
	var got []string
	for _, it := range history.items {
		if it.Kind == "tool_use" {
			got = append(got, results[it.ToolUseID])
		}
	}
	if want := []string{"first", "", "second", "third", "fourth"}; !slices.Equal(got, want) {
		t.Fatalf("results by call = %q, want %q", got, want)
	}
}

func TestAgyInterruptedErrorNoteIsDropped(t *testing.T) {
	const afterSystem = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":"run task"}
{"step_index":1,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:01Z","content":"Working on it."}
{"step_index":2,"type":"SYSTEM_MESSAGE","status":"DONE","created_at":"2026-01-01T00:00:02Z","content":"background notice"}
{"step_index":3,"type":"ERROR_MESSAGE","source":"SYSTEM","status":"DONE","created_at":"2026-01-01T00:00:03Z","error":"The stream was interrupted. Please continue the task you were working on."}
`
	steps, _ := parseAgySteps([]byte(afterSystem), agyTestID)
	stopped := mapAgySteps(steps, time.Time{}, nil)
	if stopped.seq != 4 {
		t.Fatalf("stopped seq = %d, want 4", stopped.seq)
	}
	var stoppedTexts []string
	for _, it := range stopped.items {
		if it.Wake {
			t.Fatalf("interrupted stop note must not trigger a wake notice: %+v", it)
		}
		stoppedTexts = append(stoppedTexts, it.Text)
	}
	if want := []string{"run task", "Working on it."}; !slices.Equal(stoppedTexts, want) {
		t.Fatalf("stopped items = %q, want %q", stoppedTexts, want)
	}

	const fixture = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":"run task"}
{"step_index":1,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:01Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"sleep 10"}}]}
{"step_index":2,"type":"ERROR_MESSAGE","source":"SYSTEM","status":"DONE","created_at":"2026-01-01T00:00:02Z","content":"The stream was interrupted."}
{"step_index":3,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-01-01T00:00:03Z","content":"try again"}
{"step_index":4,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-01-01T00:00:04Z","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/tmp/a"}}]}
{"step_index":5,"type":"GENERIC","status":"DONE","created_at":"2026-01-01T00:00:05Z","content":"file body"}
{"step_index":6,"type":"ERROR_MESSAGE","source":"SYSTEM","status":"DONE","created_at":"2026-01-01T00:00:06Z","error":"The stream was interrupted: 503 unavailable"}
`
	steps, _ = parseAgySteps([]byte(fixture), agyTestID)
	history := mapAgySteps(steps, time.Time{}, nil)
	if history.seq != 7 {
		t.Fatalf("seq = %d, want 7", history.seq)
	}
	var texts []string
	var uses []string
	results := map[string]string{}
	for _, it := range history.items {
		if it.Kind == "tool_use" {
			uses = append(uses, it.ToolUseID)
		}
		if it.Kind == "tool_result" {
			results[it.ToolUseID] = it.Text
		}
		if it.Kind == "text" {
			texts = append(texts, it.Text)
		}
	}
	wantTexts := []string{"run task", "try again", "API Error: The stream was interrupted: 503 unavailable"}
	if !slices.Equal(texts, wantTexts) {
		t.Fatalf("text items = %q, want %q", texts, wantTexts)
	}
	if len(uses) != 2 || results[uses[0]] != "" || results[uses[1]] != "file body" {
		t.Fatalf("tool call pairing after interrupt = uses:%v results:%v", uses, results)
	}
}
