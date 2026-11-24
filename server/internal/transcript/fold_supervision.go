package transcript

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// HasUserVisibleAction reports a successful Persona tool action that already answered
// or reached the owner directly, as it would be recognized in a folded work row.
func HasUserVisibleAction(items []proto.Item) bool {
	uses := pairedToolUses(items)
	return supervisionUserAction(items, uses)
}

func pairedToolUses(items []proto.Item) map[string]proto.Item {
	uses := make(map[string]proto.Item)
	for _, it := range items {
		if it.Kind == proto.KindToolUse {
			uses[it.ToolUseID] = it
		}
	}
	return uses
}

// CompletedSilentSupervisionRound reports the narrow disposition that may retire
// a main-conversation round into Background activity. The input must be a
// supervision message, a paired finish result must have succeeded with
// visibility=silent, and no explicit promotion may appear anywhere in the round.
// Tool names, arguments, timestamps and an unpaired result prove nothing by themselves.
func CompletedSilentSupervisionRound(items []proto.Item) bool {
	if len(items) == 0 {
		return false
	}
	source := prompts.ItemSource(items[0])
	if source == nil || source.Kind != proto.SourceSupervision {
		return false
	}
	uses := pairedToolUses(items)
	if supervisionUserAction(items, uses) {
		return false
	}
	silent := false
	for _, it := range items {
		if it.Kind == proto.KindNotice && it.Text == InterruptedNotice {
			return false
		}
		if it.SupervisionUser || it.Fold != nil && it.Fold.SupervisionUser {
			return false
		}
		in := it.Injected
		if in == nil {
			in = prompts.ParseInjected(it.Text)
		}
		if in != nil && in.State == "supervise" && in.Level == "user" {
			return false
		}
		if it.Kind != proto.KindToolResult || it.IsError || it.ToolUseID == "" {
			continue
		}
		use, ok := uses[it.ToolUseID]
		if ok && silentSupervisionFinish(use.ToolName, it) {
			silent = true
		}
	}
	return silent
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
				Verdict   string `json:"verdict"`
				UserCheck bool   `json:"user_check"`
			}
			if json.Unmarshal(use.ToolInput, &input) != nil {
				continue
			}
			internalAccepted := false
			if input.Verdict == "accepted" && !input.UserCheck {
				var disposition struct {
					Background bool `json:"background"`
				}
				internalAccepted = json.Unmarshal([]byte(result.Text), &disposition) == nil && disposition.Background
			}
			if (input.Verdict == "accepted" || input.Verdict == "needs_user") && !internalAccepted {
				return true
			}
		}
	}
	return false
}
