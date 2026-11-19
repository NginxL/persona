package prompts

// AssistantAnswerAgain asks for a single written reply to the owner's last input.
type AssistantAnswerAgain struct {
	Language string
}

func RenderAssistantAnswerAgain(m AssistantAnswerAgain) string {
	return render("assistant/answer_again", m)
}
