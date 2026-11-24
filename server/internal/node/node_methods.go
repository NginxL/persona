package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

// The node's method table (docs/server/architecture.md "The node's method table", #12920). A method that is not
// bound to a session is answered from its domain's own file, registered by an init() there,
//
//	func init() { nodeRPC(proto.MethodMachinePorts, (*Node).machinePorts) }
//
// never by a new case in Node.handleMethod. handleMethod consults the table after the delivery aliases and before its
// shared parameter struct, so a method in the table decodes only its own params and gets none of the session
// middleware that runs after that struct (alias resolution, the Codex thread mapping, the identity-conflict refusal).
// That is why the table refuses session-bound names. Every registration is checked where it is made (admit), and an
// invalid one panics when the node starts, at the init() that made it.

// A methodName is the name the manager calls a node method by ("machine.ports"). Every string the manager sends is
// one; whether the table may answer it is tableRefusal's to say. The zero value names no method.
type methodName string

// A methodHandler answers one call of a method in the table, from the call's params as they came.
type methodHandler func(n *Node, ctx context.Context, params json.RawMessage) (any, error)

// A methodTable maps method names to their handlers. The zero value is an empty table. Entries come in only through
// register, which admits each one, so a table holds only names it may answer.
type methodTable struct{ handlers map[methodName]methodHandler }

// nodeMethods is the table handleMethod consults. The domains fill it from their init() through nodeRPC; nothing
// changes it once the node runs, so calls only read it.
var nodeMethods methodTable

// nodeRPC registers fn as the answer to the method name, from an init() in fn's domain file. Each call decodes its
// params into P once (methodParams) and hands fn the value; fn keeps its own result type R. It panics when the table may
// not hold name (methodTable.admit): a wiring error, reported at the init() that made it.
func nodeRPC[P, R any](name methodName, fn func(*Node, context.Context, P) (R, error)) {
	nodeMethods.register(name, rpcHandler(name, fn))
}

// rpcHandler adapts fn to the table: the params are decoded into P (methodParams) before fn sees them. A nil fn stays
// a nil handler, which admit refuses.
func rpcHandler[P, R any](name methodName, fn func(*Node, context.Context, P) (R, error)) methodHandler {
	if fn == nil {
		return nil
	}
	return func(n *Node, ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := methodParams[P](name, raw)
		if err != nil {
			return nil, err
		}
		return fn(n, ctx, p)
	}
}

// methodParams decodes a call of method name's params into P. Fields P does not have are ignored, as the shared
// decode ignores them, so a newer manager's extra field never fails an older node; empty params leave P its zero
// value. A P of json.RawMessage is handed the params themselves, the same bytes, for a handler that decodes them its
// own way or clears them after reading (secret.run, model.run): a copy would leave the original uncleared.
func methodParams[P any](name methodName, raw json.RawMessage) (P, error) {
	var p P
	if same, ok := any(&p).(*json.RawMessage); ok {
		*same = raw
		return p, nil
	}
	if len(raw) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		var zero P
		return zero, paramsError{name, err}
	}
	return p, nil
}

// A paramsError is a call whose params do not decode into its method's type. Its text is "bad <method> params", as the
// node's own handlers answer (machine.ports, node.doctor), without the decoder's words, which may quote the params; the
// decoder's error is its cause.
type paramsError struct {
	method methodName
	cause  error
}

func (e paramsError) Error() string { return "bad " + string(e.method) + " params" }
func (e paramsError) Unwrap() error { return e.cause }

// register adds h as the answer to m, or panics with why the table may not hold it (admit).
func (t *methodTable) register(m methodName, h methodHandler) {
	if err := t.admit(m, h); err != nil {
		panic("node: " + err.Error() + `; see docs/server/architecture.md "The node's method table"`)
	}
	if t.handlers == nil {
		t.handlers = map[methodName]methodHandler{}
	}
	t.handlers[m] = h
}

// admit says why the table may not hold h as the answer to m, or nil when it may: m must be a name the table can
// answer (tableRefusal), h a handler, and m not in the table yet. It changes nothing.
func (t methodTable) admit(m methodName, h methodHandler) error {
	if err := m.tableRefusal(); err != nil {
		return err
	}
	if h == nil {
		return fmt.Errorf("node method %q: %w", m, errNoHandler)
	}
	if _, ok := t.handlers[m]; ok {
		return fmt.Errorf("node method %q: %w", m, errRegisteredTwice)
	}
	return nil
}

// answer answers a call of m when the table holds m; answered is false, and the call stays handleMethod's, when it
// does not.
func (t methodTable) answer(n *Node, ctx context.Context, m methodName, params json.RawMessage) (result any, err error, answered bool) {
	h, ok := t.handlers[m]
	if !ok {
		return nil, nil, false
	}
	result, err = h(n, ctx, params)
	return result, err, true
}

// Why the table refuses a registration (methodTable.admit, methodName.tableRefusal). Tests tell them apart with
// errors.Is.
var (
	errUnnamedMethod   = errors.New("a method in the table needs a name")
	errNoHandler       = errors.New("a method in the table needs a handler")
	errRegisteredTwice = errors.New("registered twice: the table holds the method already")
	errReservedPrefix  = errors.New("under a prefix handleMethod hands to a dispatcher of its own, which answers it")
	errDeliveryAlias   = errors.New("a delivery alias: handleMethod rewrites it, before the table, to the session method it delivers")
	errSessionBound    = errors.New("bound to a session: it stays in handleMethod's switch, after the shared decode and the " +
		"session middleware a table entry would skip")
)

// tableRefusal says why the table may not answer m, or nil when it may. It reads only m and the lists below.
func (m methodName) tableRefusal() error {
	refused := func(why error, detail string) error {
		if detail != "" {
			return fmt.Errorf("node method %q: %w (%s)", m, why, detail)
		}
		return fmt.Errorf("node method %q: %w", m, why)
	}
	switch {
	case m == "":
		return refused(errUnnamedMethod, "")
	case slices.Contains(deliveryAliases, m):
		return refused(errDeliveryAlias, "")
	case slices.Contains(sessionMethods, m):
		return refused(errSessionBound, "")
	}
	for _, p := range sessionMethodPrefixes {
		if strings.HasPrefix(string(m), p) {
			return refused(errSessionBound, p+"*")
		}
	}
	for _, p := range reservedMethodPrefixes {
		if strings.HasPrefix(string(m), p) {
			return refused(errReservedPrefix, p+"*")
		}
	}
	return nil
}

// The names the table refuses, besides the empty one. server/internal/archguard/central_lists_test.go reads these
// literals, so its freeze of handleMethod's labels leaves out the same names: keep them literals or constants.
var (
	// sessionMethodPrefixes and sessionMethods are bound to a session: handleMethod runs the shared decode and the
	// session middleware for them after the table, and a table entry would skip both.
	sessionMethodPrefixes = []string{"session.", "terminal."}
	sessionMethods        = []methodName{"assistant.send", assistantwire.DeviceAssistantAnswerAgain, "assistant.permissions.reset", "codex.answer"}

	// deliveryAliases are rewritten by handleMethod, before the table, to the session method each delivers (session.send,
	// session.interrupt, codex.answer), with the context marked by who delivered it.
	deliveryAliases = []methodName{nodeproto.SourceSend, nodeproto.OwnerInterrupt, nodeproto.OwnerCardAnswer}

	// reservedMethodPrefixes are the namespaces handleMethod hands to a dispatcher of their own (assistantSegment,
	// backendGit, handleUpdate, taskWorkspace, handlePlugins), before the table or after it: a table entry under one
	// would never be reached, or would take the method from its dispatcher.
	reservedMethodPrefixes = []string{"assistant.segment.", methodGitBackendPrefix, "node.update.", "task.workspace.", "plugin.node."}
)
