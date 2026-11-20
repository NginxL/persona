package transcript

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

// HasUserVisibleAction reports a successful Persona tool action that already answered
// or reached the owner directly, as it would be recognized in a folded work row.
func HasUserVisibleAction(items []proto.Item) bool {
	uses := make(map[string]proto.Item)
	for _, it := range items {
		if it.Kind == proto.KindToolUse {
			uses[it.ToolUseID] = it
		}
	}
	return supervisionUserAction(items, uses)
}

var supervisionMCPTools = compat.MCPToolNames([]string{"notify_user", "ask_user", "supervision_finish", "task_review"})

func supervisionToolName(name string) string {
	name = compat.CanonicalMCPToolName(name)
	if !strings.HasPrefix(name, "mcp__") {
		return name
	}
	if !slices.Contains(supervisionMCPTools, name) {
		return "" // A similarly named tool on another MCP server is not Persona's user action.
	}
	return name[strings.LastIndex(name, "__")+2:]
}

func supervisionFinishVisibility(result proto.Item) string {
	var disposition struct {
		Visibility string `json:"visibility"`
	}
	if json.Unmarshal([]byte(result.Text), &disposition) != nil {
		return ""
	}
	return disposition.Visibility
}

func silentSupervisionFinish(toolName string, result proto.Item) bool {
	return !result.IsError && supervisionToolName(toolName) == "supervision_finish" &&
		supervisionFinishVisibility(result) == "silent"
}

// supervisionUserAction keeps successful explicit user actions visible even when
// their tool calls and results are replaced by a server-side work fold.
func supervisionUserAction(run []proto.Item, uses map[string]proto.Item) bool {
	for _, result := range run {
		if result.Kind != proto.KindToolResult || result.IsError {
			continue
		}
		use, ok := uses[result.ToolUseID]
		if !ok {
			continue
		}
		switch supervisionToolName(use.ToolName) {
		case "notify_user", "ask_user":
			return true
		case "supervision_finish":
			if supervisionFinishVisibility(result) == "user" {
				return true
			}
		case "task_review":
			var input struct {
				Verdict string `json:"verdict"`
			}
			if json.Unmarshal(use.ToolInput, &input) == nil && (input.Verdict == "accepted" || input.Verdict == "needs_user") {
				return true
			}
		}
	}
	return false
}
