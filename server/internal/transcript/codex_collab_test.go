package transcript

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A coordinator's sleeps, its subagents' activity and its waits are work, not notices: they fold with the calls around
// them, so a chat that delegates shows its prompt, one folded row and its reply (#10436).
func TestCodexCollabItemsAreFoldableWork(t *testing.T) {
	f := NewFile(filepath.Join("testdata", "codex-collab.jsonl"))
	items, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range items {
		if it.Kind == proto.KindNotice && !strings.Contains(it.Text, "image.generate") {
			t.Fatalf("collab item left as a notice: %q", it.Text)
		}
		if it.Kind == proto.KindToolUse {
			names = append(names, it.ToolName)
		}
	}
	want := "subagent subagent sleep sleep subagent wait_agent subagent"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("tool calls\n got %s\nwant %s", got, want)
	}

	var started, sleep map[string]any
	for _, it := range items {
		switch it.ID {
		case "call_start_b#0":
			_ = json.Unmarshal(it.ToolInput, &started)
		case "call_sleep_2#0":
			_ = json.Unmarshal(it.ToolInput, &sleep)
		}
	}
	if started["name"] != "review_docs" || started["event"] != "started" || started["model"] != "example-model-mini" ||
		started["reasoning_effort"] != "low" || started["agent_thread_id"] != "00000000-0000-7000-8000-00000000000b" {
		t.Fatalf("subagent start input: %v", started)
	}
	if sleep["duration"] != "1m30s" || sleep["duration_ms"] != float64(90000) {
		t.Fatalf("sleep input: %v", sleep)
	}

	folded := FoldWork(items)
	var kinds []string
	for _, it := range folded {
		kinds = append(kinds, it.Role+":"+it.Kind)
	}
	if got := strings.Join(kinds, " "); got != "user:text assistant:fold system:notice assistant:text system:turn_duration" {
		t.Fatalf("folded page: %s", got)
	}
	if fo := folded[1].Fold; fo.Tools != 7 || strings.Join(fo.ToolNames, " ") != "subagent sleep wait_agent" {
		t.Fatalf("fold: %+v", fo)
	}
}

func TestCodexCollabCallStatus(t *testing.T) {
	lines := []string{
		codexTestSession,
		codexTestItem(`{"type":"CollabAgentToolCall","id":"w1","tool":"wait","status":"failed","receiver_thread_ids":["t-1","t-2"],"receiver_agents":[],"agents_states":{"t-1":{"status":"running"}}}`),
		codexTestItem(`{"type":"Extension","kind":"clock.sleep","id":"s1"}`),
	}
	items, _ := codexParseAll(t, lines)
	if len(items) != 4 {
		t.Fatalf("items: %+v", items)
	}
	use, res := items[0], items[1]
	if use.ToolName != "wait_agent" || string(use.ToolInput) != `{"receiver_thread_ids":["t-1","t-2"]}` {
		t.Fatalf("wait call: %s %s", use.ToolName, use.ToolInput)
	}
	if !res.IsError || res.Outcome.Status != proto.OutcomeFailed || res.Text != `{"t-1":{"status":"running"}}` {
		t.Fatalf("wait result: %+v %+v", res, res.Outcome)
	}
	if items[2].ToolName != "sleep" || string(items[2].ToolInput) != `{}` {
		t.Fatalf("sleep without a duration: %s %s", items[2].ToolName, items[2].ToolInput)
	}
}

func TestSleepDuration(t *testing.T) {
	for ms, want := range map[int64]string{0: "0s", 1500: "1.5s", 25000: "25s", 120000: "2m", 90000: "1m30s", 3600000: "1h", 3660000: "1h1m"} {
		if got := sleepDuration(ms); got != want {
			t.Errorf("sleepDuration(%d) = %q, want %q", ms, got, want)
		}
	}
}
