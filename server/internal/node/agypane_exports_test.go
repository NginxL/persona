package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgyPaneReadsFullExportWithoutCompact(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))
	if r := n.hookReport(agyTestPane, "PreInvocation", agyTestID); r["ok"] != true {
		t.Fatal("hook report was refused")
	}
	if items, _ := agyTestItems(t, n, agyTestRow, nil); len(items) != 0 {
		t.Fatalf("no export yet: %+v", items)
	}

	const history = `{"step_index":0,"type":"USER_INPUT","content":"Earlier question"}
{"step_index":1,"type":"PLANNER_RESPONSE","content":"Earlier reply"}
{"step_index":2,"type":"USER_INPUT","content":"Read the sample"}
{"step_index":3,"type":"PLANNER_RESPONSE","tool_calls":[{"name":"read_file","args":{"path":"sample.txt"}}]}
{"step_index":4,"type":"GENERIC","content":"sample contents"}
`
	const reply = `{"step_index":5,"type":"PLANNER_RESPONSE","content":"Final reply"}`
	compact := filepath.Join(n.agyDataRoot(), "brain", agyTestID, agyTranscriptPath)
	full := filepath.Join(n.agyDataRoot(), "brain", agyTestID, agyFullTranscriptPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, full, history+reply)
	items, page := agyTestItems(t, n, agyTestRow, map[string]any{"turns": 1})
	if len(items) != 3 || items[0].Text != "Read the sample" || items[0].Role != "user" ||
		items[1].Kind != proto.KindToolUse || items[1].ToolName != "Read" || items[2].Kind != proto.KindToolResult ||
		items[2].ToolUseID != items[1].ToolUseID || items[2].Text != "sample contents" ||
		page["to_seq"] != 5 || page["has_more"] != true {
		t.Fatalf("full-only page before trailing newline: %+v", page)
	}

	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })
	agyTestAppend(t, full, "\n")
	waitFor(t, "the full export reply", func() bool { return len(hub.spans()) >= 1 })
	item := hub.got()[0]
	if batch := hub.spans()[0]; item.Text != "Final reply" || item.Role != "assistant" ||
		batch.history != page["history"] || batch.fromSeq != 5 || batch.toSeq != 6 || batch.n != 1 {
		t.Fatalf("full export append: %+v, %+v", batch, item)
	}
	got := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": item.ID}).(proto.Item)
	if got.ID != item.ID || got.Text != item.Text {
		t.Fatalf("full export item: %+v", got)
	}

	agyTestWrite(t, full, history)
	waitFor(t, "the full export truncation", func() bool { return len(hub.spans()) >= 2 })
	items, truncated := agyTestItems(t, n, agyTestRow, map[string]any{"turns": 1})
	if batch := hub.spans()[1]; batch.fromSeq != 0 || batch.toSeq != 5 || len(items) != 3 || truncated["to_seq"] != 5 {
		t.Fatalf("full export truncation: %+v, %+v", batch, truncated)
	}
	if _, err := request(t, n, "session.item", map[string]any{"session_id": agyTestRow, "item_id": item.ID}); err == nil {
		t.Fatal("the removed reply is still available by item ID")
	}

	agyTestWrite(t, compact, history+strings.Replace(reply, "Final reply", "Current reply", 1)+"\n")
	waitFor(t, "the compact export to take over", func() bool { return len(hub.spans()) >= 3 })
	items, current := agyTestItems(t, n, agyTestRow, map[string]any{"turns": 1})
	if batch := hub.spans()[2]; current["history"] == truncated["history"] || batch.history != current["history"] ||
		batch.fromSeq != 0 || batch.toSeq != 6 || len(items) != 4 || items[3].Text != "Current reply" {
		t.Fatalf("compact export did not replace full source: %+v, %+v", batch, current)
	}
}

func TestAgyPaneRejectsUnreadableFullExport(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	file := filepath.Join(n.agyDataRoot(), "brain", agyTestID, agyFullTranscriptPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"malformed":     "not JSON\n",
		"unknown shape": "{\"event\":\"result\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			agyTestWrite(t, file, contents)
			if _, err := request(t, n, "session.items", map[string]any{"session_id": agyTestRow, "turns": 1}); err == nil ||
				!strings.Contains(err.Error(), "unrecognized Antigravity transcript format") {
				t.Fatalf("read of %s export returned %v", name, err)
			}
		})
	}
}
