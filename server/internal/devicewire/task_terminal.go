package devicewire

import "time"

const (
	MethodCloseTaskTerminal     = "session.close_task_terminal"
	MethodAuthorizeTaskTerminal = "agents.task_terminal.authorize"
	TaskTerminalClosed          = "closed"
	TaskTerminalSkipped         = "skipped"
	TaskTerminalPending         = "pending"
)

// TaskTerminal identifies the pane and process from one successful task start.
type TaskTerminal struct {
	SessionID   string `json:"session_id"`
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Token       string `json:"token"`
	PID         int    `json:"pid"`
	Process     string `json:"process"`
}

// TaskTerminalCloseRequest ties a close attempt to the accepted to-do.
type TaskTerminalCloseRequest struct {
	TodoID     string       `json:"todo_id"`
	AcceptedAt time.Time    `json:"accepted_at"`
	Terminal   TaskTerminal `json:"terminal"`
}
