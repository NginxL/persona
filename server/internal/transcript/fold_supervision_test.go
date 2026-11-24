package transcript

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
		{"internal-accepted-review", "mcp__persona__task_review", `{"verdict":"accepted"}`, `{"background":true,"message":"Accepted."}`, false, false},
		{"user-check-accepted-review", "mcp__persona__task_review", `{"verdict":"accepted","user_check":true}`, `{"background":true,"message":"Accepted."}`, false, true},
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

func TestCompletedSilentSupervisionRound(t *testing.T) {
	input := proto.Item{Role: "user", Kind: proto.KindText, Text: prompts.MessageSourceText("Check the work", &proto.MessageSource{Kind: proto.SourceSupervision})}
	finish := proto.Item{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "finish", ToolName: "mcp__persona__supervision_finish"}
	receipt := proto.Item{Role: "user", Kind: proto.KindToolResult, ToolUseID: "finish", Text: `{"visibility":"silent","summary":"Handled."}`}
	close := proto.Item{Kind: "turn_duration", TS: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}
	for _, tc := range []struct {
		name  string
		items []proto.Item
		want  bool
	}{
		{name: "successful silent", items: []proto.Item{input, finish, receipt, close}, want: true},
		{name: "ordinary reply remains inside the quiet round", items: []proto.Item{input, finish, receipt, {Role: "assistant", Kind: proto.KindText, Text: "Notes retained in Background activity."}, close}, want: true},
		{name: "direct user input", items: []proto.Item{{Role: "user", Kind: proto.KindText, Text: "Check the work"}, finish, receipt, close}},
		{name: "failed receipt", items: []proto.Item{input, finish, {Role: "user", Kind: proto.KindToolResult, ToolUseID: "finish", Text: `{"visibility":"silent"}`, IsError: true}, close}},
		{name: "malformed receipt", items: []proto.Item{input, finish, {Role: "user", Kind: proto.KindToolResult, ToolUseID: "finish", Text: `not JSON`}, close}},
		{name: "unpaired receipt", items: []proto.Item{input, finish, {Role: "user", Kind: proto.KindToolResult, ToolUseID: "another-call", Text: `{"visibility":"silent"}`}, close}},
		{name: "empty tool use id is not a pair", items: []proto.Item{input, {Role: "assistant", Kind: proto.KindToolUse, ToolName: "mcp__persona__supervision_finish"}, {Role: "user", Kind: proto.KindToolResult, Text: `{"visibility":"silent"}`}, close}},
		{name: "other server receipt", items: []proto.Item{input, {Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "finish", ToolName: "mcp__example__supervision_finish"}, receipt, close}},
		{name: "notification promotion", items: []proto.Item{input,
			{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "notify", ToolName: "notify_user"},
			{Role: "user", Kind: proto.KindToolResult, ToolUseID: "notify", Text: "sent"}, finish, receipt, close}},
		{name: "question promotion", items: []proto.Item{input,
			{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "ask", ToolName: "ask_user"},
			{Role: "user", Kind: proto.KindToolResult, ToolUseID: "ask", Text: "answered"}, finish, receipt, close}},
		{name: "accepted review promotion", items: []proto.Item{input,
			{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "task_review", ToolInput: json.RawMessage(`{"verdict":"accepted"}`)},
			{Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: "accepted"}, finish, receipt, close}},
		{name: "internal accepted review stays silent", items: []proto.Item{input,
			{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "task_review", ToolInput: json.RawMessage(`{"verdict":"accepted"}`)},
			{Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: `{"background":true,"message":"accepted"}`}, finish, receipt, close}, want: true},
		{name: "user-check accepted review promotion", items: []proto.Item{input,
			{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "task_review", ToolInput: json.RawMessage(`{"verdict":"accepted","user_check":true}`)},
			{Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: `{"background":true,"message":"accepted"}`}, finish, receipt, close}},
		{name: "needs-user review promotion", items: []proto.Item{input,
			{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "task_review", ToolInput: json.RawMessage(`{"verdict":"needs_user"}`)},
			{Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: "card sent"}, finish, receipt, close}},
		{name: "needs-user review promotion even with a malformed background marker", items: []proto.Item{input,
			{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "task_review", ToolInput: json.RawMessage(`{"verdict":"needs_user"}`)},
			{Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: `{"background":true,"message":"card sent"}`}, finish, receipt, close}},
		{name: "rework is not a user promotion", items: []proto.Item{input,
			{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "review", ToolName: "task_review", ToolInput: json.RawMessage(`{"verdict":"rework"}`)},
			{Role: "user", Kind: proto.KindToolResult, ToolUseID: "review", Text: "returned"}, finish, receipt, close}, want: true},
		{name: "explicit user finish", items: []proto.Item{input, finish, {Role: "user", Kind: proto.KindToolResult, ToolUseID: "finish", Text: `{"visibility":"user"}`}, close}},
		{name: "folded promotion", items: []proto.Item{input, {Role: "assistant", Kind: proto.KindFold, Fold: &proto.Fold{SupervisionUser: true}}, finish, receipt, close}},
		{name: "item promotion", items: []proto.Item{input, {Role: "assistant", Kind: proto.KindThinking, SupervisionUser: true}, finish, receipt, close}},
		{name: "injected user promotion", items: []proto.Item{{Role: "user", Kind: proto.KindText, Injected: &proto.Injected{Kind: proto.InjectedSystem, State: "supervise", Level: "user"}, Source: &proto.MessageSource{Kind: proto.SourceSupervision}}, finish, receipt, close}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompletedSilentSupervisionRound(tc.items); got != tc.want {
				t.Fatalf("CompletedSilentSupervisionRound = %v, want %v", got, tc.want)
			}
		})
	}
}
