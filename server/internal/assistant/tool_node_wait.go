package assistant

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// A model's tool call comes with no deadline (a runtime waits for its MCP tool as long as it takes), and a node call
// waits for the answer, the caller or the connection closing. A computer whose connection stays open while its node
// no longer answers held agent_read for as long as the conversation lasted, and the whole turn with it (#13196). The
// node calls a tool makes are bounded here, per method, when the tool's own code set no deadline of its own: a send
// that resumes a closed agent or compacts it first keeps its minutes. Waits for the user (a confirmation on the phone)
// are no node call and are not bounded by this.

// toolCallKey marks a context as a model's tool call (Service.Tool).
type toolCallKey struct{}

// asToolCall marks ctx as a model's tool call: its node calls are bounded (toolNodeWait).
func asToolCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, toolCallKey{}, true)
}

func isToolCall(ctx context.Context) bool { v, _ := ctx.Value(toolCallKey{}).(bool); return v }

// toolNodeWait is how long a tool's call of each node method waits for the node's answer. Methods not listed are not
// bounded here (an agent's start, a worktree, a launcher check: they take as long as they take, and their tools bound
// them where they need to). A var so tests shorten it.
var toolNodeWait = map[string]time.Duration{
	DeviceSessionItems:  20 * time.Second, // agent_read: reading a transcript
	DeviceSessionSend:   60 * time.Second, // agent_send to a running agent: the node types it in or queues it
	"session.interrupt": 20 * time.Second, // agent_interrupt
	"session.close":     30 * time.Second, // agent_archive with close, task_close
	"session.archive":   20 * time.Second, // the engine's own archive (agent_archive_native.go)
	DeviceSessionRename: 20 * time.Second,
}

// errNodeSilent is the cause of a bounded tool node call's deadline.
var errNodeSilent = errors.New("node did not answer")

// toolNodeContext is ctx bounded for a tool's call of method, and whether it was.
func toolNodeContext(ctx context.Context, method string) (context.Context, context.CancelFunc, bool) {
	wait, ok := toolNodeWait[method]
	if !ok || !isToolCall(ctx) {
		return ctx, func() {}, false
	}
	if _, has := ctx.Deadline(); has {
		return ctx, func() {}, false // the tool's own code chose how long
	}
	cctx, cancel := context.WithTimeoutCause(ctx, wait, errNodeSilent)
	return cctx, cancel, true
}

// nodeSilentError is a tool's node call that ran out of its bound while the caller still waited. It unwraps to the
// call's own error (context.DeadlineExceeded, or rpc.ErrNotSent when not even the request went out), so what reads a
// send's outcome tells the two apart as before; sendUnknown counts it as a send that may have arrived.
type nodeSilentError struct {
	err             error
	machine, method string
	wait            time.Duration
	lastReport      time.Duration // since the node's last resource sample; < 0: none known
}

func (e *nodeSilentError) Error() string {
	last := "no report from it is known"
	if e.lastReport >= 0 {
		last = "its last report was " + roundAge(e.lastReport) + " ago"
	}
	// Every other method toolNodeWait bounds changes something on the computer (a send, a stop, a close, a rename).
	what := "it is not known whether it was done"
	if e.method == DeviceSessionItems {
		what = "the transcript could not be read"
	}
	return fmt.Sprintf("the node on %s did not answer within %s (%s): %s. The computer may be asleep or its node stuck; "+
		"go on without it or try again later", e.machine, roundAge(e.wait), last, what)
}

func (e *nodeSilentError) Unwrap() error { return e.err }

// roundAge is a duration as a person reads it: seconds under two minutes, else minutes, else hours.
func roundAge(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	case d < 2*time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
	}
	return fmt.Sprintf("%d h", int(d.Round(time.Hour)/time.Hour))
}

// nodeSilent is the error for a bounded node call to device that ran out of its time.
func (s *Service) nodeSilent(device, method string, err error) error {
	e := &nodeSilentError{err: err, machine: s.machineName(device), method: method, wait: toolNodeWait[method], lastReport: -1}
	if s.devs != nil {
		if _, sample, _ := s.devs.MachineResources(device); sample != nil && sample.T > 0 {
			e.lastReport = max(time.Since(time.Unix(sample.T, 0)), 0)
		}
	}
	return e
}
