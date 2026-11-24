package devices

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func restartingError(err error) bool {
	var coded *rpc.Error
	return errors.As(err, &coded) && coded.Code == nodeproto.CodeRestarting
}

func handshakeInterrupted(err error) bool {
	return restartingError(err) || errors.Is(err, rpc.ErrClosed) || errors.Is(err, io.EOF)
}

// residentRestartBarrier is an optional state.load flag, so older managers still answer a harmless read.
// New managers stop outgoing calls and acknowledge only after all pre-suspension replies were received.
func (m *Machine) residentRestartBarrier(ctx context.Context, peer *rpc.Peer, prepare bool) (any, error) {
	m.mu.Lock()
	if m.peer != peer {
		m.mu.Unlock()
		return nil, rpc.ErrClosed
	}
	if !prepare {
		peer.ResumeCalls()
		m.reloadUntil = time.Time{}
		m.mu.Unlock()
		return map[string]bool{"restart_ready": false}, nil
	}
	m.reloadUntil = time.Now().Add(m.reconnectGrace)
	peer.SuspendCalls()
	m.mu.Unlock()
	if err := peer.WaitPending(ctx); err != nil {
		m.mu.Lock()
		if m.peer == peer {
			m.reloadUntil = time.Time{}
			peer.ResumeCalls()
		}
		m.mu.Unlock()
		return nil, err
	}
	return map[string]bool{"restart_ready": true}, nil
}

func (m *Machine) expectedReload() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.reloadUntil) || m.warmLocked(time.Now())
}

// reloadRetryBudget bounds how long call retries not-started refusals, reloadRetryEvery how often it tries. Vars so
// tests change them.
var reloadRetryBudget, reloadRetryEvery = 20 * time.Second, 100 * time.Millisecond

// call retries only an explicit not-started refusal or a close before writing. An ambiguous dropped write is never
// replayed. The retry budget bounds waiting, not the operation's original execution deadline after the node is ready.
// When the budget or the caller's wait runs out between tries, the last try had not started: that is said
// (rpc.NotSent), so a caller can tell it from a request that may have reached the node. Each call leaves a line in the
// log when it fails (calllog.go).
func (m *Machine) call(ctx context.Context, method string, params any, out any) error {
	return m.callWithSnapshot(ctx, method, params, out, nil)
}

func taskExecutionCall(method string, params any) bool {
	if method == "assistant.directory" {
		return true
	}
	if method != "assistant.send" {
		return false
	}
	raw, _ := json.Marshal(params)
	var fields map[string]json.RawMessage
	return json.Unmarshal(raw, &fields) == nil && string(fields["task_execution"]) == "true"
}

// callWithSnapshot leaves a successful call's actual RPC attempt in snapshot, including after a wait or retry.
func (m *Machine) callWithSnapshot(ctx context.Context, method string, params any, out any, snapshot *sessionSnapshot) (err error) {
	start, tries := time.Now(), 0
	defer func() { m.logCall(method, err, time.Since(start), tries) }()
	var deadline time.Time
	for {
		tries++
		err := m.callOnce(ctx, method, params, out, snapshot)
		m.mu.Lock()
		updateHandoff := m.updateHandoff
		m.mu.Unlock()
		if updateHandoff && restartingError(err) {
			return err // an update handoff refuses new headless work until its deployment settles, not a short reconnect
		}
		if devicewire.TaskExecutionWaitEnabled(ctx) && taskExecutionCall(method, params) && restartingError(err) {
			return err // the scheduler's longer compatibility bound owns this pre-delivery wait
		}
		notSent := errors.Is(err, errOffline) || errors.Is(err, rpc.ErrSuspended) || errors.Is(err, rpc.ErrClosed) && !errors.Is(err, rpc.ErrDropped)
		if !restartingError(err) && !(notSent && m.expectedReload()) {
			return err
		}
		if deadline.IsZero() {
			deadline = time.Now().Add(reloadRetryBudget)
		}
		if time.Until(deadline) <= 0 {
			return rpc.NotSent(context.DeadlineExceeded)
		}
		select {
		case <-ctx.Done():
			return rpc.NotSent(ctx.Err())
		case <-time.After(min(reloadRetryEvery, time.Until(deadline))):
		}
	}
}
