package proto

// AssistantExecution is the read-only answer to assistant.execution on a node. An
// external runtime may provide state and location for its own session via
// session.execution; the node supplies source and the harness from its owner.
type AssistantExecution struct {
	State        string             `json:"state"`
	Source       string             `json:"source,omitempty"` // native | runtime
	Harness      string             `json:"harness,omitempty"`
	HarnessLabel string             `json:"harness_label,omitempty"`
	Location     *ExecutionLocation `json:"location,omitempty"`

	// Mode is how a native Claude process runs when the assistant's claude_interactive setting is on (#10058):
	// "interactive" (interactive Claude Code) or "print" (claude -p although interactive was asked for, ModeNote
	// saying why). Empty when the setting is off, and for anything but Claude.
	Mode     string `json:"mode,omitempty"`
	ModeNote string `json:"mode_note,omitempty"`
}

// ExecutionLocation describes where the harness runs, as reported by the
// executor. Host is the execution environment's hostname, not necessarily its
// physical host. Container is set only when the executor knows its container.
type ExecutionLocation struct {
	Host      string `json:"host,omitempty"`
	Container string `json:"container,omitempty"`
}

const (
	ExecutionUnknown  = "unknown"
	ExecutionStarting = "starting"
	ExecutionRunning  = "running"
	ExecutionStopped  = "stopped"
	ExecutionOffline  = "offline"
)
