package prompts

import "time"

// SpaceContext is the <persona-context> text of a conversation in a track other than the main space
// (assistant/space_context, #13499, docs/assistant/health.md "Tracks and kinds"): the time, the track and the owner's
// language, and nothing of the main chat's snapshot (no Agents, computers, unread chats, tasks or knowledge base).
// Its lines are sections the node tells apart by the text before a full-width colon (node/assistant_context.go
// sectionKey), so every language writes "：" after a line's name: with an ASCII colon each new minute would be a new
// section and the last one listed as gone.
type SpaceContext struct {
	Now   time.Time
	Zone  string
	Owner string // the owner's name; "" leaves it out
	Title string // the track's title, one line
	// Health: the built-in health space, which has no title of its own (Title is then not shown).
	Health bool
	// Language is the owner's language (answerLanguage: "" for zh-Hans, else its name in itself).
	Language string
}

func RenderSpaceContext(c SpaceContext) string { return render("assistant/space_context", c) }
