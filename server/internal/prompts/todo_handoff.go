package prompts

// TodoHandoff carries the exact receipt fields together with the supervisor's message.
type TodoHandoff struct {
	Text, ReceiptJSON, Until string
}

func RenderTodoHandoff(v TodoHandoff) string { return render("assistant/task_handoff", v) }

type TodoHandoffResult struct {
	Todo, Title, Head, Check, Result, Detail string
}

func RenderTodoHandoffResult(v TodoHandoffResult) string {
	return render("assistant/task_handoff_result", v)
}
