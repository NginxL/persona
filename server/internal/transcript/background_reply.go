package transcript

import (
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ReplyPlaceholder reports a known whole-reply stand-in emitted by a runtime.
func ReplyPlaceholder(text string) bool {
	switch strings.TrimRight(strings.ToLower(strings.TrimSpace(text)), ".。!！ ") {
	case "no response requested", "(no content)", "no content", "[no content]":
		return true
	}
	return false
}

// FoldBackgroundPlaceholders displays a background turn's placeholder as folded work. Keep the item ID so
// a previously loaded reply is replaced by the projection. The native transcript is still the source of truth.
func (f *File) FoldBackgroundPlaceholders(items []proto.Item) []proto.Item {
	var out []proto.Item
	for i, item := range items {
		if item.Seq <= 0 || item.Role != "assistant" || item.Kind != proto.KindText || item.Narration ||
			item.FullSize != 0 || !ReplyPlaceholder(item.Text) {
			continue
		}
		// An incremental watch or a page can start inside the round. Read the actual preceding input.
		before, _, _ := f.PageTurns(item.Seq, 1, 2000)
		if _, background := lastBackgroundInput(before); background {
			if out == nil {
				out = slices.Clone(items)
			}
			out[i].Kind = proto.KindThinking
		}
	}
	if out == nil {
		return items
	}
	return out
}

// BackgroundReplyMissing checks the latest verified background input and its final text. A projected placeholder
// can already be folded as thinking, which carries the same words. A normal answer overrides earlier work.
func BackgroundReplyMissing(items []proto.Item) bool {
	start, background := lastBackgroundInput(items)
	if !background {
		return false
	}
	if start == len(items)-1 {
		if previous, previousBackground := lastBackgroundInput(items[:start]); previous >= 0 && !previousBackground {
			// A trailing input with no output yet belongs to the next turn. It must not silence the completed
			// non-background exchange above it; an earlier background turn keeps the missing-reply behavior.
			return false
		}
	}
	for i := len(items) - 1; i > start; i-- {
		item := items[i]
		if item.Role != "assistant" || item.Narration {
			continue
		}
		if item.Kind == proto.KindText && strings.TrimSpace(item.Text) != "" {
			return item.FullSize == 0 && ReplyPlaceholder(item.Text)
		}
		if item.Kind == proto.KindThinking && item.FullSize == 0 && ReplyPlaceholder(item.Text) {
			return true
		}
	}
	return true // a known background run with no written answer
}

// RoutineConversationReply reports a cross-conversation background round with no successful explicit owner-visibility
// action. The main conversation keeps its ordinary finished push and visible reply folded; all other sources and every
// explicit promotion keep their existing behavior. Delivery and execution are not inspected or changed here.
func RoutineConversationReply(items []proto.Item) bool {
	start, background := lastBackgroundInput(items)
	if !background || start < 0 {
		return false
	}
	source := prompts.ItemSource(items[start])
	if source == nil || source.Kind != proto.SourceConversation {
		return false
	}
	for _, item := range items[start:] {
		if item.SupervisionUser || item.Fold != nil && item.Fold.SupervisionUser {
			return false
		}
	}
	return !HasUserVisibleAction(items[start+1:])
}

// lastBackgroundInput stops at the last input or wake. Tool results are part of the input's turn.
func lastBackgroundInput(items []proto.Item) (int, bool) {
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Wake {
			return i, false
		}
		if item.Role != "user" || item.ToolUseID != "" {
			continue
		}
		if item.Kind == proto.KindImage {
			if i == 0 || item.Seq != items[i-1].Seq {
				return i, false
			}
			continue // the accompanying text on the same line carries the source
		}
		if item.Kind != proto.KindText {
			continue
		}
		if item.ClientMsgID != "" || item.Queued {
			return i, false
		}
		source := prompts.ItemSource(item)
		if source == nil {
			return i, false
		}
		switch source.Kind {
		case proto.SourceSupervision, proto.SourcePatrol, proto.SourceScheduledTask, proto.SourceConversation, proto.SourceNotice:
			return i, true
		case proto.SourceFollowup:
			// A scheduled follow-up has its own title. A wake or usage-limit continuation can be the owner's turn.
			in := item.Injected
			if in == nil {
				in = prompts.ParseInjected(item.Text)
			}
			return i, strings.TrimSpace(source.Title) != "" && !prompts.ContinuesOwnWork(in)
		}
		return i, false
	}
	return -1, false
}
