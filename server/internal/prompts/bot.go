package prompts

// BotMessage is a message the user sent the Feishu bot, as it goes into a conversation (bot/message): for a Persona
// conversation a note that the answer goes back to Feishu; the message it quotes, a forwarded chat, and the paths of
// the files it carried, saved on the machine the conversation runs on.
type BotMessage struct {
	Persona  bool   // a Persona conversation (not a coding agent): add the note about answering in Feishu
	Topic    bool   // said in a topic of a group chat (the thread of a todo), not in the bot's private chat
	Owner    string // optional owner name
	Language string // owner language; empty preserves the original Chinese instructions
	Text     string
	Todo     *BotTodo // the reminder being answered; stored to-do content, not another instruction from the owner
	Quote    *BotQuote
	Forward  string // the forwarded messages, one line each
	Files    []string
}

// BotTodo is the to-do behind a reminder card. Notes is an excerpt; the web link on the card opens the whole to-do.
type BotTodo struct {
	ID, Title, Notes string
}

type BotQuote struct {
	Who, Text string
}

func RenderBotMessage(m BotMessage) string { return render("bot/message", m) }
