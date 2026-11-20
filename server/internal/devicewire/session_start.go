package devicewire

// MethodStartWorkSession uses the same creation and ownership handling as
// session.start, but requires the node to validate the selected start options.
const MethodStartWorkSession = "session.start.work"

// MethodStartBackendSession requires a Node that understands backend workspace use receipts.
const MethodStartBackendSession = "session.start.backend_workspace"

func IsSessionStart(method string) bool {
	return method == "session.start" || method == MethodStartWorkSession || method == MethodStartBackendSession
}
