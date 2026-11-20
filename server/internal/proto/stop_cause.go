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
)

func (c StopCause) String() string {
	switch c {
	case StopUser, StopIdle, StopTaskCompleted, StopRestart, StopExit, StopResumeFailed, StopCompactFailed:
		return string(c)
	default:
		return "unknown"
	}
}

func (c StopCause) AllowsSupervisorResume() bool {
	return c == StopIdle || c == StopTaskCompleted || c == StopRestart || c == StopResumeFailed || c == StopCompactFailed
}
