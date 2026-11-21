package prompts

// ChannelMessage is a message the owner sent through a chat app's channel (docs/extensions/channels.md), as it goes into
// a conversation: a private IM uses channel/message, while a public mention uses channel/mention_message. The wrapper
// explains where the answer goes and how it shows; origin, gathered context, quotes, forwarded chats and attachments
// remain data, never owner-authored instructions.
type ChannelMessage struct {
	Persona bool   // a Persona conversation (not a coding agent): add the note about answering in the chat app
	Name    string // the channel's name: WeChat
	Owner   string // optional owner name
	Plain   bool   // the chat app shows plain text
	Asks    bool   // what is asked can be answered there
	// ImagesOut: the chat app gets an answer's pictures on lines of their own as pictures (#11986)
	ImagesOut bool
	Public    bool   // a mention in someone else's chat: use the localized public-audience prompt
	Language  string // English / 日本語; empty is zh-Hans
	Chat      string // where the owner invoked the mention
	Topic     string // its topic or thread
	Context   string // extension-gathered context, always data rather than instructions
	Text      string
	Quote     string
	Forward   string
	Files     []string
	// Unsupported are what came that cannot be passed on (a file "contract.pdf", "a video").
	Unsupported []string
}

func RenderChannelMessage(m ChannelMessage) string {
	if m.Public {
		return render("channel/mention_message", m)
	}
	return render("channel/message", m)
}

// ChannelAnswerAgain asks a conversation for the answer a turn from a chat app did not give (it ended with nothing,
// or a placeholder like "No response requested."): channel/answer_again.
type ChannelAnswerAgain struct {
	Name     string // the chat app: Lark, WeChat
	Language string // owner language; empty preserves the original Chinese instructions
}

func RenderChannelAnswerAgain(m ChannelAnswerAgain) string { return render("channel/answer_again", m) }
