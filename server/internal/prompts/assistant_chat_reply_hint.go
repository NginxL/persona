package prompts

// ChatReplyHint is the one-time note to a conversation that wrote into a chat as the user while assistant.chat_reply_relay
// is off (the default): others replied there, Persona does not pass it on, and the owner can turn that on.
type ChatReplyHint struct {
	Language, Title string
	Group           bool
}

func RenderChatReplyHint(d ChatReplyHint) string { return render("assistant/chat_reply_hint", d) }
