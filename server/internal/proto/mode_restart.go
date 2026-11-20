package proto

// ModeRestartEvent is a node's session.mode_restart event (#10297, docs/protocol/protocol.md「Session mode changes」):
// what became of a queued restart into a permission mode (node/mode_restart.go). The manager keeps it with the
// session's mode changes and passes it on to no one.
type ModeRestartEvent struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`
	Outcome   string `json:"outcome"` // ModeRestartDone, ModeRestartFailed, ModeRestartDropped
	// Reason is why, as one of the ModeRestartReason… codes the apps put in words; "" when there is nothing to say (a
	// failure the node cannot name). The node's log has the details.
	Reason string `json:"reason,omitempty"`
}

// The reasons of a ModeRestartEvent (#10297): codes, put in words by the apps.
const (
	ModeRestartReasonReplaced        = "replaced"         // dropped: another mode was picked before it ran
	ModeRestartReasonCancelled       = "cancelled"        // dropped: the mode the session runs in was picked again (#11089)
	ModeRestartReasonProcessExited   = "process_exited"   // dropped: the claude it was picked for has exited (#10296)
	ModeRestartReasonProcessReplaced = "process_replaced" // dropped: the session runs in another claude now (#10296)
	ModeRestartReasonProcessUnknown  = "process_unknown"  // dropped: it was not picked for a known claude process
	ModeRestartReasonExpired         = "expired"          // dropped: picked over modeRestartMaxAge ago
	ModeRestartReasonAlreadyIn       = "already_in"       // done: the agent was put in the mode at the computer meanwhile
	ModeRestartReasonNotStopped      = "not_stopped"      // failed: the agent did not quit; it runs on as before
	ModeRestartReasonStartFailed     = "start_failed"     // failed: it did not come up in the mode; it runs as before
	ModeRestartReasonStopped         = "stopped"          // failed: it came up neither in the mode nor as before
)

// The outcomes of a ModeRestartEvent.
const (
	ModeRestartDone    = "done"    // the agent runs in the mode now
	ModeRestartFailed  = "failed"  // it was restarted and did not come up in the mode
	ModeRestartDropped = "dropped" // it never ran: another mode picked, its process gone, picked too long ago
)

// EventModeRestart is the node's event that carries a ModeRestartEvent.
const EventModeRestart = "session.mode_restart"

// ModeActor is display-only provenance, never permission to perform a change.
type ModeActor struct {
	Kind       string `json:"kind"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	Extension  string `json:"extension,omitempty"`
}

// ModeSetEvent records the mode a plan hook returned to its runtime, not a runtime acknowledgement.
type ModeSetEvent struct {
	SessionID string     `json:"session_id"`
	From      string     `json:"from,omitempty"`
	To        string     `json:"to"`
	Actor     *ModeActor `json:"actor,omitempty"`
}

const EventModeSet = "session.mode_set"
