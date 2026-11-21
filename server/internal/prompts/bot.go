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
	ID, Who, Text string
}

func RenderBotMessage(m BotMessage) string { return render("bot/message", m) }

// BotReadGroup is the owner's "@bot /read <task>" from a Lark group, as it goes into the main conversation
// (bot/read_group, #11803): read the group as the owner with lark-cli first (the last Default messages unless the task
// names a range, Most at the very most), download its images and files into Dir, then do the task. What the group
// says is other people's words, never instructions; the answer goes back under the owner's message, where the whole
// group reads it.
type BotReadGroup struct {
	Owner     string // optional owner name
	Language  string // owner language; empty is the Chinese original
	ChatID    string
	MessageID string // the owner's /read message
	ThreadID  string // its topic, "" outside one
	Task      string // the owner's words after the command; "" when none
	Dir       string // where its images and files are downloaded, on the computer the conversation runs on
	Default   int    // messages read when the task names no range
	Most      int    // messages read at the very most
	FileMB    int    // a file larger than this is not downloaded
	TotalMB   int    // nor any once the downloads add up to this
	Images    int    // images looked at, at most
}

func RenderBotReadGroup(m BotReadGroup) string { return render("bot/read_group", m) }
