package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSideConversationMarkerSurvivesTranscriptParsing(t *testing.T) {
	text := prompts.RenderSideConversation("Can you explain the unrelated change?")
	for _, peer := range []bool{false, true} {
		line := map[string]any{"type": "user", "uuid": "synthetic-user", "timestamp": "2026-11-01T12:00:00Z", "message": map[string]any{"role": "user", "content": text}}
		if peer {
			line["origin"] = map[string]any{"kind": "peer", "name": "Persona", "body": text}
		}
		raw, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		items := ParseLine(1, raw, &Meta{})
		found := false
		for _, item := range items {
			found = found || strings.Contains(item.Text, "<persona-side-conversation />")
		}
		if !found {
			t.Fatalf("side provenance stripped (peer=%v): %+v", peer, items)
		}
	}
}

func TestFoldPreservesSupervisionVisibility(t *testing.T) {
	cases := []struct {
		name, tool, input, result string
		failed, visible           bool
	}{
		{"notification", "mcp__persona__notify_user", `{}`, "Notification sent.", false, true},
		{"question", "mcp__persona__ask_user", `{}`, "The user answered.", false, true},
		{"explicit", "mcp__persona__supervision_finish", `{"visibility":"user"}`, `{"visibility":"user","summary":"Decision needed."}`, false, true},
		{"failed-silent", "mcp__persona__supervision_finish", `{"visibility":"silent"}`, `{"visibility":"silent"}`, true, false},
		{"invalid-disposition", "mcp__persona__supervision_finish", `{"visibility":"user"}`, `not JSON`, false, false},
		{"failed", "mcp__persona__notify_user", `{}`, "Not sent.", true, false},
		{"accepted-review", "mcp__persona__task_review", `{"verdict":"accepted"}`, "Accepted.", false, true},
		{"legacy-notification", "mcp__" + compat.MCPServer + "__notify_user", `{}`, "Notification sent.", false, true},
		{"legacy-question", "mcp__" + compat.MCPServer + "__ask_user", `{}`, "The user answered.", false, true},
		{"legacy-explicit", "mcp__" + compat.MCPServer + "__supervision_finish", `{}`, `{"visibility":"user"}`, false, true},
		{"legacy-accepted-review", "mcp__" + compat.MCPServer + "__task_review", `{"verdict":"accepted"}`, "Accepted.", false, true},
		{"legacy-failed", "mcp__" + compat.MCPServer + "__notify_user", `{}`, "Not sent.", true, false},
		{"agy-notification", "mcp_persona_notify_user", `{}`, "Notification sent.", false, true},
		{"agy-scoped-notification", "mcp_persona_0c671287_notify_user", `{}`, "Notification sent.", false, true},
		{"agy-scoped-finish", "mcp_persona_55a1c59d_supervision_finish", `{"visibility":"user"}`, `{"visibility":"user"}`, false, true},
		{"agy-legacy-scoped-review", "mcp_" + compat.MCPServer + "_0c671287_task_review", `{"verdict":"accepted"}`, "Accepted.", false, true},
		{"agy-other-server", "mcp_persona_other_notify_user", `{}`, "Not a Persona notification.", false, false},
		{"decision-review", "task_review", `{"verdict":"needs_user"}`, "Decision card sent.", false, true},
		{"rework", "task_review", `{"verdict":"rework"}`, "Sent back.", false, false},
		{"other-server-notification", "mcp__example__notify_user", `{}`, "Not a Persona notification.", false, false},
		{"other-server-disposition", "mcp__example__supervision_finish", `{"visibility":"silent"}`, `{"visibility":"silent"}`, false, false},
		{"other-tool", "Read", `{}`, "Synthetic file.", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := []proto.Item{
				{ID: "thought", Seq: 1, Kind: proto.KindThinking, Text: "Checking this supervision signal."},
				{ID: "call", Seq: 2, Kind: proto.KindToolUse, ToolUseID: "call-example", ToolName: tc.tool, ToolInput: json.RawMessage(tc.input)},
				{ID: "result", Seq: 3, Kind: proto.KindToolResult, ToolUseID: "call-example", Text: tc.result, IsError: tc.failed},
			}
			folded := FoldWork(items)
			if len(folded) != 1 || folded[0].Fold == nil || folded[0].Fold.SupervisionUser != tc.visible {
				t.Fatalf("folded visibility: %+v, want %v", folded, tc.visible)
			}
			if len(items) != 3 || items[2].Text != tc.result {
				t.Fatal("folding discarded the audit transcript")
			}
		})
	}
}

func TestFoldRetainsSilentFinishAlongsideAReply(t *testing.T) {
	for _, tool := range []string{"mcp__persona__supervision_finish", "mcp_persona_55a1c59d_supervision_finish"} {
		t.Run(tool, func(t *testing.T) {
			items := []proto.Item{
				{ID: "thought", Seq: 1, Kind: proto.KindThinking},
				{ID: "call", Seq: 2, Kind: proto.KindToolUse, ToolUseID: "finish", ToolName: tool,
					ToolInput: json.RawMessage(`{"visibility":"user"}`)},
				{ID: "result", Seq: 3, Kind: proto.KindToolResult, ToolUseID: "finish", Text: `{"visibility":"silent"}`},
				{ID: "reply", Seq: 4, Kind: proto.KindText, Role: "assistant", Text: "The example build passed."},
			}
			for _, fold := range []func([]proto.Item) []proto.Item{FoldWork, FoldWorkLatest} {
				page := fold(items)
				if len(page) != len(items) || page[1].ID != "call" || page[2].ID != "result" || page[3].ID != "reply" {
					t.Fatalf("silent disposition and reply must survive transit together: %+v", page)
				}
			}
		})
	}
}
