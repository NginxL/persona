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
)

func (c StopCause) String() string {
	switch c {
	case StopUser, StopIdle, StopTaskCompleted, StopRestart, StopExit:
		return string(c)
	default:
		return "unknown"
	}
}

func (c StopCause) AllowsSupervisorResume() bool {
	return c == StopIdle || c == StopTaskCompleted || c == StopRestart
}
