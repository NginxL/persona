package assistantwire

// ---- the outbound channel port's values (internal/larkhost.Outbound, #13404) ----

// ChannelTarget says where a channel's post or note goes beyond the default: the chat to send into (ChatID; "" is the
// owner's private chat), whether a reply goes into the parent's thread, whether a reply whose parent is gone fails
// instead of landing in the chat as a message of its own (NoDetour: for what belongs under that parent or nowhere),
// and whether it answers the owner's "/read" in a group (Read: its cards carry no workbench button, which would only
// be refused there). The zero value is the owner's private chat.
type ChannelTarget struct {
	ChatID   string
	InThread bool
	NoDetour bool
	Read     bool
}

// ThreadKind is what a line in a to-do's chat thread tells.
type ThreadKind int

const (
	ThreadDecision      ThreadKind = iota // a decision was noted (Text: its line)
	ThreadAgentFinished                   // an agent working on it finished a turn (Agent)
	ThreadAgentNeeds                      // an agent working on it needs the owner (Agent, Text: what it asks; DM)
	ThreadTodoAccepted                    // the owner accepted it
	ThreadTodoDone                        // it is done (Text: why it closed)
	ThreadTodoFailed                      // it did not start (Text: why)
)

// ThreadEvent is one thing to tell the chat thread of a to-do. DM: the agent's card is in the owner's private chat.
type ThreadEvent struct {
	Kind  ThreadKind
	Agent string
	Text  string
	DM    bool
}
