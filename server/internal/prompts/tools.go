package prompts

// RenderJSONOnly is a system prompt with the line asking for a bare JSON object after it (tools/json_only).
func RenderJSONOnly(system string) string { return render("tools/json_only", system) }

// RenderTabName names a new agent's herdr tab after its first message (tools/tab_name).
func RenderTabName() string { return render("tools/tab_name", nil) }

// Translation gives the model the language of its instructions and the independently requested target.
type Translation struct{ Language, Target string }

func RenderTranslateTexts(in Translation) string { return render("tools/translate_texts", in) }
func RenderTranslateMail(in Translation) string  { return render("tools/translate_mail", in) }

// RenderTranslateMailInput is the mail as the translation model reads it.
func RenderTranslateMailInput(language, subject, text string) string {
	return render("tools/translate_mail_input", struct{ Language, Subject, Text string }{language, subject, text})
}

// LauncherRunTurn is one turn of a conversation an API purpose sends to a launcher's CLI (tools/launcher_run_input).
type LauncherRunTurn struct {
	Assistant bool // the model's own earlier answer; else the user's
	Text      string
}

// RenderLauncherRunInput is a conversation of several turns as one prompt, for a CLI run that takes a single message
// (assistant/models_run.go).
func RenderLauncherRunInput(turns []LauncherRunTurn) string {
	return render("tools/launcher_run_input", turns)
}

// RenderLauncherCheck is the one-word prompt Settings › Models' "Test" runs a launcher with (tools/launcher_check).
func RenderLauncherCheck() string { return render("tools/launcher_check", nil) }

// SessionTitle is the system prompt naming an assistant conversation after its first turn (tools/session_title):
// "kind | topic" (in Chinese). The kind words stay Chinese whatever the language (the assistant parses them, titleKinds).
type SessionTitle struct {
	// Language is the topic's language by its own name ("English", "Japanese" written in Japanese); "" is Chinese, as before i18n.
	Language string
	TopicMax int // the topic's characters with a Language (Chinese's 14 stays in the template)
}

func RenderSessionTitle(t SessionTitle) string { return render("tools/session_title", t) }

// SessionTitleInput is what the title model reads: the first exchange, and its last answer when that broke the format.
// Source is the title the extension that opened the conversation gave it ("" for none, #3758): a hint, not the title.
type SessionTitleInput struct {
	SessionTitle
	First, Reply, Retry, Source string
}

func RenderSessionTitleInput(in SessionTitleInput) string {
	return render("tools/session_title_input", in)
}

// JSONSchema is an extension's system prompt (may be empty) and the JSON Schema its reply must match, indented
// (tools/json_schema, model.complete's json_schema; assistant/model_rpc.go).
type JSONSchema struct{ System, Schema string }

func RenderJSONSchema(in JSONSchema) string { return render("tools/json_schema", in) }

// RenderJSONSchemaRetry is the turn asking once more after a reply that did not match the schema: problem says
// what was wrong (tools/json_schema_retry).
func RenderJSONSchemaRetry(problem string) string { return render("tools/json_schema_retry", problem) }

// NextMessage is the system prompt suggesting the user's next message after a turn (tools/next_message, assistant/suggest_next.go):
// Language is the user's language by name when it is not Chinese ("" otherwise).
type NextMessage struct{ Language string }

func RenderNextMessage(n NextMessage) string { return render("tools/next_message", n) }

// NextMessageTurn is one visible message the suggestion model reads: Role "user" or "assistant".
type NextMessageTurn struct{ Role, Text string }

// RenderNextMessageInput is the chat's last visible messages as the suggestion model reads them (tools/next_message_input).
func RenderNextMessageInput(turns []NextMessageTurn) string {
	return render("tools/next_message_input", turns)
}
