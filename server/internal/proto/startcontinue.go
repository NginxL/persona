package proto

// Answering a start's startup question from the app (#10365, docs/protocol/protocol.md "Answering a start's
// question"): a New Agent start whose new tab stopped on a question of the shell's startup files (CodeShellWaiting)
// lists the question and the answers it may be given in the session's start_error_args; a full device sends one, the
// node types it into the kept tab while the question is still there and starts the session again in that tab.

// MethodStartContinue is the node method (capability nodeproto.StartContinue): StartContinueParams → {"session": Session}.
const MethodStartContinue = "start.continue"

// StartContinueParams is start.continue's params and the body of POST /api/v1/sessions/{mid}/{sid}/start/continue
// (the route's sid is SessionID).
type StartContinueParams struct {
	SessionID string `json:"session_id,omitempty"`
	// Choice is one of start_error_args.choices: a key ("y", "n", "q", "0", "c"), "enter", "yes", "no" or "cancel"
	// (Ctrl-C). Nothing else is typed.
	Choice string `json:"choice"`
}

// MethodStartAnswer is the node method (capability nodeproto.StartAnswer): StartAnswerParams → StartAnswerResult. It
// answers the question a failed start's kept tab stopped on with the same guards as start.continue, for a start the
// hub runs again itself in that tab (a planned task's, POST /api/v1/todos/{id}/start/answer).
const MethodStartAnswer = "start.answer"

// StartAnswerParams is start.answer's params: the kept tab (Pane, the failed start's start_error_args pane), the
// question it listed (Line: typed into only while it is still the tab's last line) and one of its Choices.
type StartAnswerParams struct {
	Pane   string `json:"pane"`
	Line   string `json:"line"`
	Choice string `json:"choice"`
}

// StartAnswerResult is start.answer's answer. Typed says whether the answer was typed: false when Line was no longer
// the tab's last line (answered at the computer, gone by itself, or another question since), so nothing was. A reply
// without it (nil, a node of the first start.answer) is taken as typed.
type StartAnswerResult struct {
	OK    bool  `json:"ok"`
	Typed *bool `json:"typed,omitempty"`
}

// StartErrorArgs keys of a CodeShellWaiting start (besides line, screen, pane and kept_until): the question's kind
// (update, yes_no, press_key, compinit, ssh_host_key, zsh_newuser, p10k_wizard, rvm_trust, secret), its answers' ids
// joined by commas, "true" when it reads a whole line or a secret, and the answer Enter alone gives.
const (
	ShellAskKind     = "kind"
	ShellAskChoices  = "choices"
	ShellAskLineMode = "line_mode"
	ShellAskSecret   = "secret"
	ShellAskDefault  = "default"
)
