package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexRollout(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-10-26T05:05:58.393Z","type":"session_meta","payload":{"id":"01a1","cwd":"/r/dots","cli_version":"0.159.3"}}`,
		`{"timestamp":"2026-10-26T05:05:59.475Z","type":"turn_context","payload":{"model":"gpt-6.1-sol","approval_policy":"never"}}`,
		`{"timestamp":"2026-10-26T05:05:59.515Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"why is it stuck"}]}}}`,
		`{"timestamp":"2026-10-26T05:06:00.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"dup"}]}}`,
		`{"timestamp":"2026-10-26T05:06:01.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"m1","phase":"commentary","content":[{"type":"Text","text":"Looking at the tabs first."}]}}}`,
		`{"timestamp":"2026-10-26T05:06:02.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"e1","command":["/bin/zsh","-lc","herdr agent list"],"status":"completed","aggregated_output":"ok\n","exit_code":0}}}`,
		`{"timestamp":"2026-10-26T05:06:03.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"e2","command":["/bin/zsh","-lc","false"],"status":"failed","aggregated_output":"","exit_code":1}}}`,
		`{"timestamp":"2026-10-26T05:06:04.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"FileChange","id":"f1","changes":{"/r/dots/a.txt":{"type":"update","unified_diff":"@@ -1 +1 @@\n-a\n+b\n"}},"status":"completed"}}}`,
		`{"timestamp":"2026-10-26T05:06:05.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","id":"c1","server":"cua","tool":"js","arguments":{"code":"1"},"status":"completed","result":{"content":[{"type":"text","text":"2"}],"isError":false}}}}`,
		`{"timestamp":"2026-10-26T05:06:06.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"output_tokens":211},"last_token_usage":{"input_tokens":26279}}}}`,
		`{"timestamp":"2026-10-26T05:06:07.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"m2","phase":"final_answer","content":[{"type":"Text","text":"Tab 1 waits on a prompt."}]}}}`,
		`{"timestamp":"2026-10-26T05:06:08.000Z","type":"event_msg","payload":{"type":"turn_aborted","reason":"interrupted"}}`,
	}
	path := filepath.Join(t.TempDir(), "rollout-2026-10-26T13-05-38-01a1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFile(path)
	items, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Role+":"+it.Kind+":"+it.ToolName)
	}
	want := []string{
		"user:text:", "assistant:text:",
		"assistant:tool_use:Bash", "user:tool_result:", "assistant:tool_use:Bash", "user:tool_result:",
		"assistant:tool_use:Edit", "user:tool_result:", "assistant:tool_use:mcp__cua__js", "user:tool_result:",
		"assistant:text:", "system:notice:",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("items\n got %v\nwant %v", got, want)
	}
	if string(items[2].ToolInput) != `{"command":"herdr agent list"}` || items[3].IsError || !items[5].IsError {
		t.Fatalf("bash: %s %+v %+v", items[2].ToolInput, items[3], items[5])
	}
	if !strings.Contains(string(items[6].ToolInput), `"diff":"@@ -1 +1 @@\n-a\n+b\n"`) || items[6].ToolUseID != items[7].ToolUseID {
		t.Fatalf("edit: %s", items[6].ToolInput)
	}
	m := f.Meta()
	if !m.Codex || m.Cwd != "/r/dots" || m.Model != "gpt-6.1-sol" || m.FirstUser != "why is it stuck" ||
		m.LastText != "Tab 1 waits on a prompt." || m.ContextTokens != 26279 || m.Turns != 1 {
		t.Fatalf("meta: %+v", m)
	}
	if items[11].Text != "已中断" || items[0].Kind != proto.KindText {
		t.Fatalf("notice: %+v", items[11])
	}
}

func TestCodexUserMessageTags(t *testing.T) {
	line := `{"timestamp":"2026-10-26T05:05:59.515Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u2","content":[` +
		`{"type":"text","text":"<send_user_message_question_reply>\n[{\"questionItemId\":\"x\",\"question\":\"Accept the Xcode license?\",\"answer\":\"done, agreed\"}]\n</send_user_message_question_reply>"},` +
		`{"type":"text","text":"\n<in-app-browser-context source=\"ambient-ui-state\">tabs: a, b</in-app-browser-context>"},` +
		`{"type":"local_image","path":"/tmp/shot.png"}]}}}`
	meta := &Meta{Codex: true}
	it := only(t, ParseLine(1, []byte(line), meta))
	if it.Text != "done, agreed\n\n<attached_images>\n/tmp/shot.png\n</attached_images>" {
		t.Fatalf("got %q", it.Text)
	}
	if got := codexUserText(`<send_user_message_question_reply>[{"question":"A?","answer":"1"},{"question":"B?","answer":"2"}]</send_user_message_question_reply>`); got != "A?：1\nB?：2" {
		t.Fatalf("several answers: %q", got)
	}
}

// a compatible CLI (a Codex fork) writes the newer rollout: messages come as history_mutation display_completions, tool calls
// still as event_msg/item_completed, and an item may appear in both.
func TestHistoryMutationRollout(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-10-28T08:42:16.027Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a1","id":"01a1","cwd":"/r/fake","originator":"traex-tui","cli_version":"0.210.1"}}`,
		`{"timestamp":"2026-10-28T08:42:48.700Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"2026-10-28T08:42:48.752Z","ordinal":2,"type":"history_mutation","payload":{"version":1,"turn_id":"t1","operation":"append","items":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"<collaboration_mode>…</collaboration_mode>"}]}]}}`,
		`{"timestamp":"2026-10-28T08:42:48.954Z","ordinal":6,"type":"history_mutation","payload":{"display_completions":[{"turn_id":"t1","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"build the proxy","text_elements":[]}]}}],"version":1,"operation":"append","items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"build the proxy"}]}]}}`,
		`{"timestamp":"2026-10-28T08:43:40.492Z","ordinal":8,"type":"history_mutation","payload":{"display_completions":[{"turn_id":"t1","item":{"type":"AgentMessage","id":"m1","content":[{"type":"Text","text":"Checking the repo first."}],"phase":"commentary"}}],"version":1,"operation":"append","items":[]}}`,
		`{"timestamp":"2026-10-28T08:43:41.000Z","ordinal":9,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"e1","command":["/bin/zsh","-lc","ls"],"status":"completed","aggregated_output":"a\n","exit_code":0}}}`,
		`{"timestamp":"2026-10-28T08:43:41.100Z","ordinal":10,"type":"history_mutation","payload":{"display_completions":[{"turn_id":"t1","item":{"type":"CommandExecution","id":"e1","command":["/bin/zsh","-lc","ls"],"status":"completed","aggregated_output":"a\n","exit_code":0}}]}}`,
		`{"timestamp":"2026-10-28T08:43:42.000Z","ordinal":11,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"TerminalInteraction","id":"ti1","stdin":"","command":"go test ./..."}}}`,
		`{"timestamp":"2026-10-28T08:43:50.000Z","ordinal":12,"type":"history_mutation","payload":{"display_completions":[{"turn_id":"t1","item":{"type":"AgentMessage","id":"m2","content":[{"type":"Text","text":"Done: the proxy runs."}],"phase":"final_answer"}}]}}`,
	}
	path := filepath.Join(t.TempDir(), "rollout-2026-10-28T16-42-16-01a1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFile(path)
	items, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Role+":"+it.Kind+":"+it.ToolName+":"+it.Text)
	}
	want := []string{
		"user:text::build the proxy", "assistant:text::Checking the repo first.",
		"assistant:tool_use:Bash:", "user:tool_result::a\n", "assistant:text::Done: the proxy runs.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("items\n got %q\nwant %q", got, want)
	}
	m := f.Meta()
	if !m.Codex || m.Cwd != "/r/fake" || m.LastText != "Done: the proxy runs." || !m.TurnOpen {
		t.Fatalf("meta: %+v", m)
	}
	_ = os.WriteFile(path, []byte(strings.Join(append(lines, `{"timestamp":"2026-10-28T08:43:51.000Z","ordinal":13,"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`), "\n")+"\n"), 0o644)
	if _, err := f.Refresh(); err != nil || f.Meta().TurnOpen {
		t.Fatalf("turn still open after task_complete: %v", err)
	}
}

func TestCodexCommentaryAnswersUserMidTurn(t *testing.T) {
	const ts = `"timestamp":"2026-11-12T08:00:00.000Z"`
	ev := func(payload string) string { return `{` + ts + `,"type":"event_msg","payload":` + payload + `}` }
	item := func(it string) string { return ev(`{"type":"item_completed","item":` + it + `}`) }
	lines := []string{
		`{` + ts + `,"type":"session_meta","payload":{"id":"01a1","cwd":"/r/fake","cli_version":"0.159.2"}}`,
		ev(`{"type":"task_started","turn_id":"t1"}`),
		item(`{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"What is the current status?"}]}`),
		item(`{"type":"Reasoning","id":"r1","summary_text":["Checking the work already done."]}`),
		item(`{"type":"CommandExecution","id":"e1","command":["git","status"],"status":"completed","aggregated_output":"ok\n","exit_code":0}`),
		item(`{"type":"AgentMessage","id":"m1","phase":"commentary","content":[{"type":"Text","text":"The focused tests pass; the full run is next."}]}`),
		item(`{"type":"CommandExecution","id":"e2","command":["go","test","./..."],"status":"completed","aggregated_output":"ok\n","exit_code":0}`),
		ev(`{"type":"task_complete","turn_id":"t1"}`),
	}
	m := Meta{Codex: true}
	var got []proto.Item
	for i, line := range lines {
		got = append(got, ParseLine(i+1, []byte(line), &m)...)
	}
	if len(got) != 7 || got[1].Kind != proto.KindThinking || got[4].Kind != proto.KindText || got[4].Text != "The focused tests pass; the full run is next." {
		t.Fatalf("commentary must be text and Reasoning thinking: %+v", got)
	}
	if m.LastText != "The focused tests pass; the full run is next." {
		t.Fatalf("the commentary answer is the latest text: %+v", m)
	}
}

// codexTurns is a synthesized rollout of n turns: each a user message and four agent messages, the second of them
// recorded again in history_mutation (the newer format), and the last turn reusing an id of the first.
func codexTurns(n int) []string {
	const ts = `"timestamp":"2026-11-10T08:00:00.000Z"`
	agent := func(id, text string) string {
		return `{"type":"AgentMessage","id":"` + id + `","content":[{"type":"Text","text":"` + text + `"}],"phase":"commentary"}`
	}
	lines := []string{`{` + ts + `,"type":"session_meta","payload":{"id":"01a1","cwd":"/r/fake","cli_version":"0.170.0"}}`}
	for k := range n {
		lines = append(lines,
			fmt.Sprintf(`{%s,"type":"event_msg","payload":{"type":"task_started","turn_id":"t%d"}}`, ts, k),
			fmt.Sprintf(`{%s,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u%d","content":[{"type":"text","text":"ask %d"}]}}}`, ts, k, k))
		for j := range 4 {
			id := fmt.Sprintf("m%d-%d", k, j)
			if k == n-1 && j == 3 {
				id = "m0-3" // an id an earlier turn used
			}
			it := agent(id, fmt.Sprintf("step %d of %d", j, k))
			lines = append(lines, fmt.Sprintf(`{%s,"type":"event_msg","payload":{"type":"item_completed","item":%s}}`, ts, it))
			if j == 1 {
				lines = append(lines, fmt.Sprintf(`{%s,"type":"history_mutation","payload":{"display_completions":[{"turn_id":"t%d","item":%s}]}}`, ts, k, it))
			}
		}
		lines = append(lines, fmt.Sprintf(`{%s,"type":"event_msg","payload":{"type":"task_complete","turn_id":"t%d"}}`, ts, k))
	}
	return lines
}

// An item is recorded twice only within its turn (window.go), so the ids the parser keeps to drop the repeat are
// those of the turn under way: a long rollout does not grow them with every item it ever had.
func TestCodexSeenBoundedByTurn(t *testing.T) {
	const turns = 2000
	meta := &Meta{Codex: true}
	most, n := 0, 0
	for i, line := range codexTurns(turns) {
		n += len(ParseLine(i+1, []byte(line), meta))
		most = max(most, len(meta.codexSeen))
	}
	if most > 5 {
		t.Fatalf("the parser kept %d item ids; a turn has 5", most)
	}
	if n != turns*5 {
		t.Fatalf("%d items for %d turns of 5: the repeat in history_mutation is not dropped once", n, turns)
	}
}

// A full read and a read opened by its end (its window starting at a turn) give the same items: neither drops an
// item whose id an earlier turn used, and both drop the repeat within a turn.
func TestCodexSeenFullEqualsWindowed(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	data := []byte(strings.Join(codexTurns(60), "\n") + "\n")
	p := filepath.Join(t.TempDir(), "rollout-2026-11-10T08-00-00-01a1.jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	windowBytes = 1 << 40
	full := NewFile(p)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	if len(all) != 60*5 {
		t.Fatalf("a full read gives %d items for 60 turns of 5", len(all))
	}
	windowBytes = int64(len(data)) / 8
	f := NewFile(p)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if f.base == 0 {
		t.Fatal("the file was not opened by its end: the test checks nothing")
	}
	var got []proto.Item
	for before := 0; ; {
		items, more, _ := f.Page(before, 7)
		got = append(append([]proto.Item(nil), items...), got...)
		if !more || len(items) == 0 {
			break
		}
		before = items[0].Seq
	}
	if !bytes.Equal(marshal(t, got), marshal(t, all)) {
		t.Fatalf("a windowed read gives %d items, a full read %d", len(got), len(all))
	}
}

// A rollout's token_count carries the model's context window next to the latest request's input (#2430).
func TestCodexContextWindow(t *testing.T) {
	f := NewFile(filepath.Join("testdata", "codex-0.163.jsonl"))
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if m := f.Meta(); m.ContextWindow != 475000 || m.ContextTokens <= 0 || m.ContextTokens >= m.ContextWindow {
		t.Fatalf("context %d of window %d", m.ContextTokens, m.ContextWindow)
	}
}

// The backgrounded-command tests below use synthetic items only: a command starts in the background, then the rollout
// records the same id as completed or failed when the process exits (#8868).
const codexTestTS = "2026-11-16T08:00:00.000Z"

const codexTestSession = `{"timestamp":"` + codexTestTS + `","type":"session_meta","payload":{"id":"01a1","cwd":"/r/fake","cli_version":"0.170.0"}}`

func codexTestEvent(payload string) string {
	return `{"timestamp":"` + codexTestTS + `","type":"event_msg","payload":` + payload + `}`
}

func codexTestItem(item string) string {
	return codexTestEvent(`{"type":"item_completed","item":` + item + `}`)
}

func codexTestHistory(item string) string {
	return `{"timestamp":"` + codexTestTS + `","type":"history_mutation","payload":{"display_completions":[{"item":` + item + `}]}}`
}

func codexTestUser(id, text string) string {
	return `{"type":"UserMessage","id":"` + id + `","content":[{"type":"text","text":"` + text + `"}]}`
}

func codexCommandItem(t *testing.T, id, status, output string, exitCode int, secs int64) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type":              "CommandExecution",
		"id":                id,
		"command":           []string{"go", "test", "./..."},
		"status":            status,
		"aggregated_output": output,
		"exit_code":         exitCode,
		"duration":          map[string]any{"secs": secs, "nanos": 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func codexParseAll(t *testing.T, lines []string) ([]proto.Item, *Meta) {
	t.Helper()
	meta := &Meta{Codex: true}
	var got []proto.Item
	for i, line := range lines {
		got = append(got, ParseLine(i+1, []byte(line), meta)...)
	}
	return got, meta
}

// A backgrounded command is a pending call until its terminal record: no result at the start, then exactly one result
// under the same tool_use_id with the final output and the recorded outcome.
func TestCodexBackgroundCommandCompletes(t *testing.T) {
	lines := []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(codexTestUser("u1", "run the tests")),
		codexTestItem(codexCommandItem(t, "async-1", "backgrounded", "tests started\n", 0, 1)),
		codexTestItem(codexCommandItem(t, "async-1", "completed", "all tests passed\n", 0, 3)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
	}
	got, meta := codexParseAll(t, lines)
	if len(got) != 3 {
		t.Fatalf("items: %d, want the user message and one call with its result: %+v", len(got), got)
	}
	use, res := got[1], got[2]
	if use.Kind != proto.KindToolUse || use.ID != "async-1#0" || use.Seq != 4 || use.ToolUseID != "async-1" || use.ToolName != "Bash" {
		t.Fatalf("pending call: %+v", use)
	}
	if res.Kind != proto.KindToolResult || res.ID != "async-1#1" || res.Seq != 5 || res.ToolUseID != "async-1" {
		t.Fatalf("result: %+v", res)
	}
	if res.Text != "all tests passed\n" || res.IsError {
		t.Fatalf("the final output is the result: %q isError=%v", res.Text, res.IsError)
	}
	if res.Outcome == nil || res.Outcome.Status != proto.OutcomeOK || res.Outcome.ExitCode == nil || *res.Outcome.ExitCode != 0 ||
		res.Outcome.DurationMS == nil || *res.Outcome.DurationMS != 3000 {
		t.Fatalf("outcome: %+v", res.Outcome)
	}
	if len(meta.codexPending) != 0 {
		t.Fatalf("the terminal record must clear the pending id: %+v", meta.codexPending)
	}
}

func TestLateCodexCommandKeepsItemsWithoutAdvancingActivity(t *testing.T) {
	var meta Meta
	for i, line := range []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(codexTestUser("u1", "Run the checks")),
		codexTestItem(codexCommandItem(t, "async-1", "backgrounded", "starting", 0, 1)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
	} {
		ParseLine(i+1, []byte(line), &meta)
	}
	before := meta.LastActivity
	command := func(at time.Time, turnID, id string) []byte {
		return fmt.Appendf(nil, `{"timestamp":%q,"type":"event_msg","payload":{"type":"item_completed","turn_id":%q,"item":%s}}`,
			at.Format(time.RFC3339Nano), turnID, codexCommandItem(t, id, "completed", "all checks passed", 0, 2))
	}
	items := ParseLine(6, command(before.Add(time.Minute), "t1", "async-1"), &meta)
	if len(items) != 1 || items[0].Kind != proto.KindToolResult || items[0].Text != "all checks passed" || len(meta.codexPending) != 0 {
		t.Fatalf("late result was not applied: items = %+v, pending = %v", items, meta.codexPending)
	}
	if !items[0].TS.After(before) || !meta.LastActivity.Equal(before) || !meta.TurnClosed {
		t.Fatalf("result at %v moved closed-turn activity: got %v, want %v", items[0].TS, meta.LastActivity, before)
	}
	// Some older rollouts omit task_started even for a new turn. Its id still
	// distinguishes its commands from a late result on the closed turn.
	at := before.Add(2 * time.Minute)
	ParseLine(7, command(at, "t2", "new-command"), &meta)
	if !meta.LastActivity.Equal(at) {
		t.Fatalf("new turn's command did not advance activity: got %v, want %v", meta.LastActivity, at)
	}
}

// A failure after the backgrounded record is reported as failed, not as the initial zero exit code.
func TestCodexBackgroundCommandFails(t *testing.T) {
	lines := []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(codexTestUser("u1", "run the tests")),
		codexTestItem(codexCommandItem(t, "async-1", "backgrounded", "starting\n", 0, 1)),
		codexTestItem(codexCommandItem(t, "async-1", "failed", "boom", 1, 2)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
	}
	got, _ := codexParseAll(t, lines)
	if len(got) != 3 {
		t.Fatalf("items: %d, want the user message and one call with its result: %+v", len(got), got)
	}
	res := got[2]
	if !res.IsError || res.Text != "boom\n(exit code 1)" {
		t.Fatalf("a failed command: text %q isError=%v", res.Text, res.IsError)
	}
	if res.Outcome == nil || res.Outcome.Status != proto.OutcomeFailed || res.Outcome.ExitCode == nil || *res.Outcome.ExitCode != 1 ||
		res.Outcome.DurationMS == nil || *res.Outcome.DurationMS != 2000 {
		t.Fatalf("outcome: %+v", res.Outcome)
	}
}

// The same backgrounded or terminal record repeated in event_msg and history_mutation is still one call and one result.
func TestCodexBackgroundDuplicateRecords(t *testing.T) {
	bg := codexCommandItem(t, "async-2", "backgrounded", "started\n", 0, 1)
	done := codexCommandItem(t, "async-2", "completed", "done\n", 0, 2)
	lines := []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(bg),
		codexTestHistory(bg),
		codexTestHistory(done),
		codexTestItem(done),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
	}
	got, meta := codexParseAll(t, lines)
	if len(got) != 2 {
		t.Fatalf("items: %d, want one call and one result: %+v", len(got), got)
	}
	if got[0].Kind != proto.KindToolUse || got[0].ID != "async-2#0" || got[0].Seq != 3 {
		t.Fatalf("call: %+v", got[0])
	}
	if got[1].Kind != proto.KindToolResult || got[1].ID != "async-2#1" || got[1].Seq != 5 || got[1].Text != "done\n" {
		t.Fatalf("result: %+v", got[1])
	}
	if len(meta.codexPending) != 0 {
		t.Fatalf("the duplicate records must still clear the pending id: %+v", meta.codexPending)
	}
}

// A command with no backgrounded record before it is a synchronous call: its line still gives the call and its result.
func TestCodexSynchronousCommandsUnchanged(t *testing.T) {
	lines := []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(codexTestUser("u1", "check")),
		codexTestItem(codexCommandItem(t, "e1", "completed", "ok\n", 0, 1)),
		codexTestItem(codexCommandItem(t, "e2", "failed", "boom", 1, 2)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
	}
	got, _ := codexParseAll(t, lines)
	if len(got) != 5 {
		t.Fatalf("items: %d, want two calls with their results: %+v", len(got), got)
	}
	if got[1].Kind != proto.KindToolUse || got[2].Kind != proto.KindToolResult || got[2].Text != "ok\n" || got[2].IsError {
		t.Fatalf("completed call: %+v %+v", got[1], got[2])
	}
	if got[3].Kind != proto.KindToolUse || got[4].Kind != proto.KindToolResult || got[4].Text != "boom\n(exit code 1)" || !got[4].IsError {
		t.Fatalf("failed call: %+v %+v", got[3], got[4])
	}
}

// An incremental refresh that reads the terminal record later gives the same items as a full read; a cold read opened
// by its end and paged back agrees, and Full returns the result whole from its own line.
func TestCodexBackgroundIncrementalAndCold(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	big := strings.Repeat("line of output\n", 400)
	lines := []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t0"}`),
		codexTestItem(codexTestUser("u0", "first")),
		codexTestItem(codexCommandItem(t, "e0", "completed", "ok\n", 0, 1)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t0"}`),
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(codexTestUser("u1", "second")),
		codexTestItem(codexCommandItem(t, "e1", "completed", "ok\n", 0, 1)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
		codexTestEvent(`{"type":"task_started","turn_id":"t2"}`),
		codexTestItem(codexTestUser("u2", "last")),
		codexTestItem(codexCommandItem(t, "async-cold", "backgrounded", "started\n", 0, 1)),
		codexTestItem(codexCommandItem(t, "async-cold", "completed", big, 0, 4)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t2"}`),
	}
	path := filepath.Join(t.TempDir(), "rollout-2026-11-16T08-00-00-01a1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines[:12], "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inc := NewFile(path)
	before, err := inc.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 || before[len(before)-1].Kind != proto.KindToolUse || before[len(before)-1].ID != "async-cold#0" {
		t.Fatalf("the backgrounded record is not a pending call: %+v", before)
	}
	for _, it := range before {
		if it.Kind == proto.KindToolResult && it.ToolUseID == "async-cold" {
			t.Fatalf("the backgrounded record claimed a result: %+v", it)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh, err := inc.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].ID != "async-cold#1" || fresh[0].Kind != proto.KindToolResult || fresh[0].FullSize != len(big) {
		t.Fatalf("the terminal record must give exactly one result with the final output: %+v", fresh)
	}
	if whole, err := inc.Full("async-cold#1"); err != nil || whole.Text != big {
		t.Fatalf("Full after the incremental read: %d bytes, err %v", len(whole.Text), err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	windowBytes = 1 << 40
	full := NewFile(path)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	if len(all) != 9 {
		t.Fatalf("a full read gives %d items, want 9", len(all))
	}
	windowBytes = int64(len(data)) / 4
	cold := NewFile(path)
	if _, err := cold.Refresh(); err != nil {
		t.Fatal(err)
	}
	if cold.base == 0 {
		t.Fatal("the file was not opened by its end: the test checks nothing")
	}
	var got []proto.Item
	for before := 0; ; {
		items, more, _ := cold.Page(before, 2)
		got = append(append([]proto.Item(nil), items...), got...)
		if !more || len(items) == 0 {
			break
		}
		before = items[0].Seq
	}
	if !bytes.Equal(marshal(t, got), marshal(t, all)) {
		t.Fatalf("a cold read gives %d items, a full read %d", len(got), len(all))
	}
	res, err := cold.Full("async-cold#1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != big {
		t.Fatalf("Full gave %d bytes of the result, want %d", len(res.Text), len(big))
	}
	use, err := cold.Full("async-cold#0")
	if err != nil {
		t.Fatal(err)
	}
	if use.Kind != proto.KindToolUse || use.ToolName != "Bash" {
		t.Fatalf("Full of the pending call: %+v", use)
	}
}

// A command can finish in a later turn: the pending call stays visible across task_started and only the result arrives
// in the new turn. A backgrounded record repeated after task_started is the same pending command, not a second call.
func TestCodexBackgroundPendingAcrossTurns(t *testing.T) {
	lines := []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(codexTestUser("u1", "first")),
		codexTestItem(codexCommandItem(t, "async-x", "backgrounded", "started\n", 0, 1)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
		codexTestEvent(`{"type":"task_started","turn_id":"t2"}`),
		codexTestItem(codexTestUser("u2", "second")),
		codexTestItem(codexCommandItem(t, "async-x", "backgrounded", "started\n", 0, 1)),
		codexTestItem(codexCommandItem(t, "async-x", "completed", "done\n", 0, 2)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t2"}`),
	}
	got, meta := codexParseAll(t, lines)
	if len(got) != 4 {
		t.Fatalf("items: %d, want two messages and the call with its result: %+v", len(got), got)
	}
	if got[1].Kind != proto.KindToolUse || got[1].ID != "async-x#0" || got[1].Seq != 4 {
		t.Fatalf("pending call: %+v", got[1])
	}
	if got[2].Kind != proto.KindText || got[2].Text != "second" {
		t.Fatalf("the new turn's message comes before the result: %+v", got[2])
	}
	if got[3].Kind != proto.KindToolResult || got[3].ID != "async-x#1" || got[3].Seq != 9 || got[3].Text != "done\n" {
		t.Fatalf("result in the later turn: %+v", got[3])
	}
	if len(meta.codexPending) != 0 {
		t.Fatalf("the later terminal record must clear the pending id: %+v", meta.codexPending)
	}
}

// Once the terminal record is read, a later turn may use the id again: the pending state is gone, not a permanent
// suppression of the id.
func TestCodexBackgroundIDReusedAfterCompletion(t *testing.T) {
	lines := []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(codexTestUser("u1", "first")),
		codexTestItem(codexCommandItem(t, "e1", "backgrounded", "started\n", 0, 1)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
		codexTestEvent(`{"type":"task_started","turn_id":"t2"}`),
		codexTestItem(codexTestUser("u2", "second")),
		codexTestItem(codexCommandItem(t, "e1", "completed", "done\n", 0, 2)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t2"}`),
		codexTestEvent(`{"type":"task_started","turn_id":"t3"}`),
		codexTestItem(codexTestUser("u3", "third")),
		codexTestItem(codexCommandItem(t, "e1", "completed", "again\n", 0, 1)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t3"}`),
	}
	got, _ := codexParseAll(t, lines)
	if len(got) != 7 {
		t.Fatalf("items: %d, want three messages, the pending call, its result and a reused synchronous call: %+v", len(got), got)
	}
	if got[3].Kind != proto.KindToolResult || got[3].ID != "e1#1" || got[3].Seq != 8 || got[3].Text != "done\n" {
		t.Fatalf("result of the backgrounded call: %+v", got[3])
	}
	if got[5].Kind != proto.KindToolUse || got[5].ID != "e1#0" || got[5].Seq != 12 {
		t.Fatalf("reused call: %+v", got[5])
	}
	if got[6].Kind != proto.KindToolResult || got[6].ID != "e1#1" || got[6].Seq != 12 || got[6].Text != "again\n" {
		t.Fatalf("reused call's result: %+v", got[6])
	}
}

// A backgrounded command whose pending state the cap evicted still keeps its final output: with no tracked call left,
// its terminal record is a standalone call and result pair, as a synchronous command's line is.
func TestCodexPendingEvictionKeepsResult(t *testing.T) {
	meta := &Meta{Codex: true}
	seq := 0
	parse := func(line string) []proto.Item {
		seq++
		return ParseLine(seq, []byte(line), meta)
	}
	parse(codexTestEvent(`{"type":"task_started","turn_id":"t1"}`))
	if got := parse(codexTestItem(codexCommandItem(t, "evicted", "backgrounded", "started\n", 0, 1))); len(got) != 1 || got[0].ID != "evicted#0" {
		t.Fatalf("pending call: %+v", got)
	}
	for i := 0; i < codexPendingKept; i++ {
		parse(codexTestItem(codexCommandItem(t, fmt.Sprintf("filler-%d", i), "backgrounded", "started\n", 0, 1)))
	}
	if meta.codexPending["evicted"] {
		t.Fatalf("the cap must evict the oldest pending id")
	}
	got := parse(codexTestItem(codexCommandItem(t, "evicted", "completed", "final output\n", 0, 7)))
	if len(got) != 2 {
		t.Fatalf("an evicted command's terminal record must keep its call and result: %+v", got)
	}
	use, res := got[0], got[1]
	if use.Kind != proto.KindToolUse || use.ID != "evicted#0" || use.Seq != seq || use.ToolUseID != "evicted" {
		t.Fatalf("standalone call: %+v", use)
	}
	if res.Kind != proto.KindToolResult || res.ID != "evicted#1" || res.Seq != seq || res.Text != "final output\n" || res.IsError {
		t.Fatalf("the final output must survive eviction: %+v", res)
	}
	if res.Outcome == nil || res.Outcome.DurationMS == nil || *res.Outcome.DurationMS != 7000 {
		t.Fatalf("outcome: %+v", res.Outcome)
	}
}

// The pending queue holds only live ids. An id that finished and is then reused must not be evicted by its old queue
// entry: the oldest genuinely pending command is evicted instead, and the reused command's terminal record is still
// only its result.
func TestCodexPendingIDReusedWhileOtherPending(t *testing.T) {
	lines := []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(codexTestUser("u1", "first")),
		codexTestItem(codexCommandItem(t, "reused", "backgrounded", "started\n", 0, 1)),
		codexTestItem(codexCommandItem(t, "reused", "completed", "first done\n", 0, 2)),
		codexTestItem(codexCommandItem(t, "older", "backgrounded", "started\n", 0, 1)),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
		codexTestEvent(`{"type":"task_started","turn_id":"t2"}`),
		codexTestItem(codexTestUser("u2", "second")),
		codexTestItem(codexCommandItem(t, "reused", "backgrounded", "started again\n", 0, 1)),
	}
	got, meta := codexParseAll(t, lines)
	if got[2].Kind != proto.KindToolResult || got[2].ID != "reused#1" || got[2].Text != "first done\n" {
		t.Fatalf("the first completion: %+v", got[2])
	}
	if got[5].Kind != proto.KindToolUse || got[5].ID != "reused#0" || got[5].Seq != 10 {
		t.Fatalf("the reused id must be a new pending call: %+v", got[5])
	}
	for i := 0; i < codexPendingKept-1; i++ {
		bg := codexTestItem(codexCommandItem(t, fmt.Sprintf("filler-%d", i), "backgrounded", "started\n", 0, 1))
		got = append(got, ParseLine(len(lines)+i+1, []byte(bg), meta)...)
	}
	if meta.codexPending["older"] {
		t.Fatalf("the cap must evict the oldest genuinely pending command")
	}
	if !meta.codexPending["reused"] {
		t.Fatalf("the reused pending id must not be evicted by a stale queue entry")
	}
	done := codexTestItem(codexCommandItem(t, "reused", "completed", "second done\n", 0, 3))
	final := ParseLine(len(lines)+codexPendingKept, []byte(done), meta)
	if len(final) != 1 || final[0].ID != "reused#1" || final[0].Kind != proto.KindToolResult || final[0].Text != "second done\n" {
		t.Fatalf("the reused command's terminal record must be only its result: %+v", final)
	}
	calls := 0
	for _, it := range got {
		if it.Kind == proto.KindToolUse && it.ToolUseID == "reused" {
			calls++
		}
	}
	if calls != 2 {
		t.Fatalf("want the first call and the reused call, got %d", calls)
	}
}

// A call with no result yet stays visible when the page is folded; once its result is there the call folds with the
// work around it as any other finished call does.
func TestCodexBackgroundFold(t *testing.T) {
	lines := []string{
		codexTestSession,
		codexTestEvent(`{"type":"task_started","turn_id":"t1"}`),
		codexTestItem(codexTestUser("u1", "run it")),
		codexTestItem(`{"type":"Reasoning","id":"r1","summary_text":["checking"]}`),
		codexTestItem(codexCommandItem(t, "async-fold", "backgrounded", "started\n", 0, 1)),
		codexTestItem(codexCommandItem(t, "async-fold", "completed", "done\n", 0, 2)),
		codexTestItem(`{"type":"AgentMessage","id":"m1","phase":"final_answer","content":[{"type":"Text","text":"done"}]}`),
		codexTestEvent(`{"type":"task_complete","turn_id":"t1"}`),
	}
	got, _ := codexParseAll(t, lines)
	if len(got) != 5 {
		t.Fatalf("items: %d, want message, thinking, call, result, answer: %+v", len(got), got)
	}
	pending := foldWork([]proto.Item{got[2]}, false)
	if len(pending) != 1 || pending[0].Kind != proto.KindToolUse {
		t.Fatalf("a pending call stays visible: %+v", pending)
	}
	orphan := foldWork([]proto.Item{got[3]}, false)
	if len(orphan) != 1 || orphan[0].Kind != proto.KindToolResult {
		t.Fatalf("a result whose call is on an older page stays visible: %+v", orphan)
	}
	finished := foldWork([]proto.Item{got[1], got[2], got[3]}, false)
	if len(finished) != 1 || finished[0].Kind != proto.KindFold || finished[0].Fold == nil || finished[0].Fold.Tools != 1 {
		t.Fatalf("a finished call folds with the work around it: %+v", finished)
	}
}

// The pending ids are bounded: a rollout that starts backgrounded commands and never records their end must not grow
// the parser's state with every one, and the newest still gets its result once.
func TestCodexPendingBounded(t *testing.T) {
	meta := &Meta{Codex: true}
	n := 2 * codexPendingKept
	for i := 0; i < n; i++ {
		started := codexTestEvent(fmt.Sprintf(`{"type":"task_started","turn_id":"t%d"}`, i))
		bg := codexTestItem(codexCommandItem(t, fmt.Sprintf("async-%d", i), "backgrounded", "started\n", 0, 1))
		ParseLine(2*i+1, []byte(started), meta)
		ParseLine(2*i+2, []byte(bg), meta)
	}
	if len(meta.codexPending) > codexPendingKept {
		t.Fatalf("the parser kept %d pending ids, bound is %d", len(meta.codexPending), codexPendingKept)
	}
	if len(meta.codexPendingIDs) != len(meta.codexPending) {
		t.Fatalf("the queue holds %d entries for %d pending ids", len(meta.codexPendingIDs), len(meta.codexPending))
	}
	last := fmt.Sprintf("async-%d", n-1)
	done := codexTestItem(codexCommandItem(t, last, "completed", "done\n", 0, 2))
	got := ParseLine(2*n+1, []byte(done), meta)
	if len(got) != 1 || got[0].ID != last+"#1" || got[0].Kind != proto.KindToolResult || got[0].Text != "done\n" {
		t.Fatalf("the newest pending command's terminal record: %+v", got)
	}
	if meta.codexPending[last] {
		t.Fatalf("the terminal record did not clear the pending id")
	}
	if len(meta.codexPendingIDs) != len(meta.codexPending) {
		t.Fatalf("the queue holds %d entries for %d pending ids after completion", len(meta.codexPendingIDs), len(meta.codexPending))
	}
}
