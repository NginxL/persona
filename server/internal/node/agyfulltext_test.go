package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestAgyPaneRefreshesWhenFullExportArrives(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))

	middle := strings.Repeat("écho ", 1400)
	reply := "Start\n" + middle + "\nEnd"
	preview := fmt.Sprintf("Start\n<truncated %d bytes>\nEnd", len(middle)+2)
	thought := strings.Repeat("Reasoning. ", 6000)
	thinking := "Before\n" + thought + "\nAfter"
	thinkingPreview := fmt.Sprintf("Before\n<truncated %d bytes>\nAfter", len(thought)+2)
	const record = `{"step_index":0,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-01-01T00:00:00Z","content":%q,"thinking":%q%s}`
	compact := fmt.Sprintf(record, preview, thinkingPreview, `,"truncated_fields":["content","thinking"]`) + "\n"
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, compact)
	past := time.Now().Add(-time.Minute)
	if err := os.Chtimes(file, past, past); err != nil {
		t.Fatal(err)
	}

	initial, page := agyTestItems(t, n, agyTestRow, nil)
	if len(initial) != 2 || initial[1].Text != preview || initial[0].Text != thinkingPreview {
		t.Fatal("the compact export must remain readable before the full export arrives")
	}
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })

	fullPath := filepath.Join(n.agyDataRoot(), "brain", agyTestID, agyFullTranscriptPath)
	agyTestWrite(t, fullPath, fmt.Sprintf(record, reply, thinking, ""))
	if items, current := agyTestItems(t, n, agyTestRow, nil); len(items) != 2 || items[1].Text != preview || current["history"] != page["history"] {
		t.Fatal("an incomplete full record became visible")
	}
	agyTestAppend(t, fullPath, "\n")
	waitFor(t, "complete text in the watch", func() bool { return len(hub.spans()) > 0 })

	items, refreshed := agyTestItems(t, n, agyTestRow, nil)
	if len(items) != 2 || items[1].Text != reply || items[0].FullSize != len(thinking) ||
		items[0].ID != initial[0].ID || items[1].ID != initial[1].ID {
		t.Fatal("history must restore the full export before applying page limits and keep item IDs")
	}
	if batch := hub.spans()[0]; batch.fromSeq != 0 || batch.history == page["history"] || batch.history != refreshed["history"] ||
		len(hub.got()) != 2 || hub.got()[1].Text != reply {
		t.Fatalf("the watcher did not refresh the same settled reply: %+v", batch)
	}
	full := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": initial[0].ID}).(proto.Item)
	if full.Text != thinking {
		t.Fatal("the single-item read must return the complete field")
	}
}

func TestAgyPaneRejectsStaleFullText(t *testing.T) {
	const preview = "Same beginning\n<truncated 4 bytes>\nRight ending"
	for _, tc := range []struct {
		name, created, text string
	}{
		{"different step after rewind", "2026-01-01T00:00:01Z", "Same beginningtextRight ending"},
		{"different suffix", "2026-01-01T00:00:00Z", "Same beginningtextWrong ending"},
		{"different omitted length", "2026-01-01T00:00:00Z", "Same beginningextraRight ending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps, rev := parseAgySteps([]byte(fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","created_at":"2026-01-01T00:00:00Z","content":%q,"truncated_fields":["content"]}`, preview)), agyTestID)
			full, _ := parseAgySteps([]byte(fmt.Sprintf(`{"step_index":0,"type":"PLANNER_RESPONSE","created_at":%q,"content":%q}`, tc.created, tc.text)), agyTestID)
			if restoreAgyFullText(steps, full, rev.steps) || steps[0].Content != preview {
				t.Fatal("the full record replaced the compact preview without a match")
			}
		})
	}
}

func TestAgyPaneRestoresMatchingFullToolCalls(t *testing.T) {
	const record = `{"step_index":0,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-01-01T00:00:00Z","tool_calls":[{"name":"write_to_file","args":{"TargetFile":%q,"CodeContent":%q}}]%s}`
	const path = "/example/file.go"
	const content = "line1\nline2\nline3\n"
	const prefix = `"line1\nline2\nli`
	omitted := len(fmt.Sprintf("%q", content)) - len(prefix)
	preview := fmt.Sprintf("%s\n<truncated %d bytes>", prefix, omitted)
	compact := fmt.Sprintf(record, fmt.Sprintf("%q", path), preview, `,"truncated_fields":["tool_calls"]`) + "\n"
	for _, tc := range []struct {
		name, fullPath, fullContent string
		restored                    bool
	}{
		{"matching full call", path, content, true},
		{"mismatched argument", "/example/other.go", content, false},
		{"mismatched prefix", path, "wrong\nline2\nline3\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			key := agyTestFile(t, root, agyTestID, compact)
			full := fmt.Sprintf(record, tc.fullPath, tc.fullContent, "") + "\n"
			agyTestWrite(t, filepath.Join(root, "brain", agyTestID, agyFullTranscriptPath), full)
			brain, err := agyBrain(root, agyTestID)
			if err != nil {
				t.Fatal(err)
			}
			defer brain.Close()
			var n Node
			history, _, err := n.agyRead(brain, key, agyTestID, nil)
			if err != nil {
				t.Fatal(err)
			}
			wantContent := content
			if !tc.restored {
				const head = "line1\nline2\nli"
				wantContent = head + transcript.ApproximateClipNote(len(head)+omitted)
			}
			want := fmt.Sprintf(`{"content":%q,"file_path":%q}`, wantContent, path)
			if len(history.items) != 1 || history.items[0].Kind != proto.KindToolUse ||
				history.items[0].ToolName != "Write" || string(history.items[0].ToolInput) != want {
				t.Fatalf("history items = %+v, want one Write with input %s", history.items, want)
			}
		})
	}
}
