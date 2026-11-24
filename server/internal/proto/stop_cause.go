package proto

// StopCause is node-observed session provenance, not a reason supplied by the assistant.
type StopCause string

const (
	StopUnknown       StopCause = ""
	StopUser          StopCause = "user"
	StopIdle          StopCause = "idle"
	StopTaskCompleted StopCause = "task_completed"
	StopRestart       StopCause = "restart"
	// StopExit: the Agent was told to quit (/exit, /quit sent through Persona): its session ended (#10355). Like a
	// user stop it never permits a supervisor resume.
	StopExit StopCause = "exit"
	// StopResumeFailed: the assistant's own resume of an Agent that stopped without the user (one of the causes above
	// that allow it) did not bring it up (#11441). The user did nothing, so the assistant may try again.
	StopResumeFailed StopCause = "resume_failed"
	// StopCompactFailed: the Agent quit while the node compacted it before a message (an update it was steered into,
	// a crash), as a step of Persona's own, not the user's (#11441).
	StopCompactFailed StopCause = "compact_failed"
	// Gateway failures are observed from the latest failed turn after its process exits. Explicit user stops win.
	StopUsageLimit     StopCause = "usage_limit"
	StopTransientError StopCause = "transient_error"
)

func (c StopCause) String() string {
	switch c {
	case StopUser, StopIdle, StopTaskCompleted, StopRestart, StopExit, StopResumeFailed, StopCompactFailed, StopUsageLimit, StopTransientError:
		return string(c)
	default:
		return "unknown"
	}
}

// AllowsSupervisorResume: c is a recorded stop that was not the user's (idle, a finished task, a restart, a failed
// resume or compaction, a gateway error). A resume consumes such a stop before it starts, and an automatic stop or close
// that records one maps any other cause to the user's. Whether the assistant may resume an Agent is
// SupervisorMayResume, which also takes an unknown cause.
func (c StopCause) AllowsSupervisorResume() bool {
	return c == StopIdle || c == StopTaskCompleted || c == StopRestart || c == StopResumeFailed || c == StopCompactFailed || c == StopUsageLimit || c == StopTransientError
}

// UserStopped: the user ended the Agent: their Stop, Close or "Archive and close" (user), or the quit command sent
// through Persona (exit). Only these need the user's own Resume on its terminal page.
func (c StopCause) UserStopped() bool {
	return c == StopUser || c == StopExit
}

// SupervisorMayResume: the assistant may resume an Agent stopped with c (agent_send with resume). Every stop but the
// user's: a recorded non-user stop, and an unknown one too (an Agent that quit by itself, a crash, a machine that was
// off for longer than the restart window): nothing says the user ended it, and leaving it to the user's Resume made the
// supervisor start a replacement without its context. A cause this build does not know stays refused: it may be a
// newer node's user stop. The node still refuses an Agent that runs, or one a resume of which is still settling.
func (c StopCause) SupervisorMayResume() bool {
	return c == StopUnknown || c.AllowsSupervisorResume()
}
