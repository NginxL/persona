package node

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

var (
	// replayNotifyTimeout bounds one replayed notification.
	replayNotifyTimeout = 5 * time.Second
	// replayGateMost is the longest a new hook / ask / notify waits for the replay.
	replayGateMost = 20 * time.Second
	// replayRetryAfter are the waits before a connection tries again what the manager did not take: the session state
	// (reloadState, node.go) and the notifications its replay kept (retryNotifications). The last one repeats.
	replayRetryAfter = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
	// replayGoneGrace: after a notification timed out, how long the replay looks whether the connection went with it
	// (then no try is counted).
	replayGoneGrace = 250 * time.Millisecond
)

// replaySpool hands the manager what standby took, in order, then opens the gate for new hooks (n.replayed).
//
// It waits for the session state first (takesReport needs it: an empty state takes every session for one Persona does
// not report for, and a notification's pane for no session). When the first try of the state fails, the replay is held
// until a later try brings it (reloadState), and the gate opens at once: new hooks never wait for the hold. A replay
// that runs after new hooks went ahead of it leaves what they moved on alone (replayAhead).
//
// An entry goes from the file once it was sent (spoolRemovals: in one rewrite every spoolRemoveEvery entries and at the
// end); a hook event that could not be sent stops the replay (the connection is gone), and the rest stays for the next
// one. A notification the manager refused, did not answer, or could not take now stays too; retry says whether one is
// left for retryNotifications on this connection. One the manager got twice (the node stopped between sending it and
// taking it out) it drops by spool_id.
func (n *Node) replaySpool(ctx context.Context, sp *spool) (retry bool) {
	var once sync.Once
	openGate := func() { once.Do(func() { close(n.replayed) }) }
	defer openGate()
	defer n.ahead.finish() // before the gate opens: hooks after it are not ahead
	if len(sp.load()) == 0 {
		return false
	}
	select {
	case <-n.stateReady:
	case <-n.stateMissed:
		n.logger.Printf("spool: the session state did not come from the manager; %d kept until it does", len(sp.load()))
		openGate()
		select {
		case <-n.stateReady:
		case <-ctx.Done():
			return false
		}
	case <-ctx.Done():
		return false
	}
	sp.replayMu.Lock() // the last connection's replay may still be on its last entry
	defer sp.replayMu.Unlock()
	sent, stale, ahead := 0, 0, 0
	later := false // a notification timed out, or the hub is away: the others wait for the retries, not the gate
	defer func() {
		if sent+stale+ahead > 0 {
			n.logger.Printf("spool: replayed %d taken while the manager was away (%d too old, %d behind new hooks: dropped)", sent, stale, ahead)
		}
	}()
	done := spoolRemovals{sp: sp, logf: n.logger.Printf}
	defer done.flush() // on every return, before replayMu is let go: the next replay never sees what this one handed over
	for _, e := range sp.load() {
		if ctx.Err() != nil {
			return false
		}
		switch {
		case time.Since(e.At) > spoolMaxAge:
			stale++
		case e.Kind == "hook" && n.ahead.behind(e):
			n.replayQuestion(e) // stale news is dropped, but a resumed hook still needs its exact question state
			ahead++             // its session had a new hook since: older news than what the manager heard
		case e.Kind == "hook":
			if err := n.replayHook(e); err != nil {
				return false // a hook.event write fails only when the connection is gone: all kept for the next one
			}
			sent++
		case e.Kind == "notify":
			if later {
				retry = true // kept, untried, for the retries
				continue
			}
			switch r := n.replayNotify(ctx, sp, e); r {
			case notifyGone:
				return false // it and the rest stay for the next connection, no try counted
			case notifySent:
				sent++
			default:
				// Kept (the rest goes on, so the hook events after it keep their order), or dropped.
				retry = retry || r != notifyDropped
				later = later || r == notifyTimedOut || r == notifyHubAway
				continue
			}
		}
		done.add(e.ID)
	}
	return retry
}

// replayAhead is what live hooks did on a connection before its replay was done: hooks go first when the replay is
// held for the session state, or when one waited out replayGateMost. The replay then drops the kept events of a
// session that had a new hook (the manager heard newer news of it), and binds no pane a new hook bound to the session
// it had before.
type replayAhead struct {
	mu       sync.Mutex
	done     bool
	sessions map[string]bool
	panes    map[string]bool
}

// note records a live hook that passed the gate (serveSock), unless the replay is done. A resumed hook is not news: it
// asks again for an event reported already, or kept by standby (which told it to retry), whose kept copy the replay
// still has to hand over. A report-only hook binds no pane, so its pane is not taken.
func (a *replayAhead) note(req sockRequest) {
	var in hookInput
	if req.Resume || json.Unmarshal(req.Input, &in) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	if validID(in.SessionID) {
		if a.sessions == nil {
			a.sessions = map[string]bool{}
		}
		a.sessions[in.SessionID] = true
	}
	if req.Pane != "" && !req.ReportOnly {
		if a.panes == nil {
			a.panes = map[string]bool{}
		}
		a.panes[req.Pane] = true
	}
}

// behind: a kept hook event of a session that had a live hook since.
func (a *replayAhead) behind(e spoolEntry) bool {
	var in hookInput
	if json.Unmarshal(e.Request.Input, &in) != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[in.SessionID]
}

// paneTaken: a live hook bound pane since.
func (a *replayAhead) paneTaken(pane string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.panes[pane]
}

func (a *replayAhead) finish() {
	a.mu.Lock()
	a.done, a.sessions, a.panes = true, nil, nil
	a.mu.Unlock()
}

// What became of one replayed notification.
type notifyOutcome int

const (
	notifySent     notifyOutcome = iota // handed over: it leaves the spool
	notifyGone                          // the connection is gone: kept, no try counted
	notifyBusy                          // the manager still has an earlier try (nodeproto.ErrSpoolBusy): kept, no try counted
	notifyHubAway                       // the manager cannot reach the hub now (nodeproto.ErrHubAway): kept, no try counted
	notifyRefused                       // refused: kept, a try counted
	notifyTimedOut                      // not answered in replayNotifyTimeout: kept, a try counted
	notifyDropped                       // refused for good, or once too often: gone from the spool
)

// replayNotify hands the manager one kept notification. It counts a failed try (sp.failed) when it was refused or not
// answered, and drops one that can never be sent; it does not remove a sent one.
func (n *Node) replayNotify(ctx context.Context, sp *spool, e spoolEntry) notifyOutcome {
	cctx, cancel := context.WithTimeout(ctx, replayNotifyTimeout)
	req := e.Request
	req.Pane = n.ownPane(req.Pane) // kept as the client sent it (serveSock translates live ones)
	rep, err := n.notifyCall(cctx, req, e.ID)
	timedOut := cctx.Err() != nil && ctx.Err() == nil
	cancel()
	if err == nil && rep.Error != "" { // refused before the manager was asked (empty, not JSON): never sendable
		n.logger.Printf("spool: notification %s dropped: %s", e.ID, rep.Error)
		if rerr := sp.remove(e.ID); rerr != nil {
			n.logger.Printf("spool: %v", rerr)
		}
		return notifyDropped
	}
	var refused *rpc.Error
	var r notifyOutcome
	switch {
	case err == nil:
		return notifySent
	case errors.Is(err, rpc.ErrClosed):
		return notifyGone
	case errors.As(err, &refused) && refused.Code == nodeproto.ErrSpoolBusy:
		return notifyBusy
	case errors.As(err, &refused) && refused.Code == nodeproto.ErrHubAway:
		return notifyHubAway
	case errors.As(err, &refused):
		r = notifyRefused
	case timedOut && !n.peerGone(ctx):
		r = notifyTimedOut
	default:
		return notifyGone // the connection went (maybe right at the deadline)
	}
	drop, ferr := sp.failed(e.ID)
	n.logger.Printf("spool: notification %s not handed over (%v); dropped: %v %v", e.ID, err, drop, ferr)
	if drop {
		return notifyDropped
	}
	return r
}

// peerGone says whether the connection ended, waiting replayGoneGrace for a close that came with a timeout.
func (n *Node) peerGone(ctx context.Context) bool {
	t := time.NewTimer(replayGoneGrace)
	defer t.Stop()
	select {
	case <-n.peer.Done():
		return true
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

// retryWait is the wait before try i+2 of something this connection tries again (replayRetryAfter).
func retryWait(i int) time.Duration { return replayRetryAfter[min(i, len(replayRetryAfter)-1)] }

// sleepCtx waits d, false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// retryNotifications tries again, on this connection, the notifications its replay kept: after each wait of
// replayRetryAfter (the last one repeating), until none is left or the connection ends. Refusals count, so one goes
// after spoolTriesMost of them; "not now" answers (the manager still sending it, or away from the hub) do not, and
// only the age limit (spoolMaxAge, from when standby took it) bounds those. New hooks do not wait for this: the gate
// opened with the replay.
func (n *Node) retryNotifications(ctx context.Context, sp *spool) {
	for i := 0; ; i++ {
		if !sleepCtx(ctx, retryWait(i)) || !n.retryNotificationsOnce(ctx, sp) {
			return
		}
	}
}

// retryNotificationsOnce is one round of retryNotifications; it says whether one is left to try again.
func (n *Node) retryNotificationsOnce(ctx context.Context, sp *spool) (again bool) {
	sp.replayMu.Lock()
	defer sp.replayMu.Unlock()
	done := spoolRemovals{sp: sp, logf: n.logger.Printf}
	defer done.flush()
	for _, e := range sp.load() {
		if e.Kind != "notify" {
			continue // a hook entry left by a connection that dropped: the next connection's replay has it
		}
		if ctx.Err() != nil {
			return false
		}
		if time.Since(e.At) > spoolMaxAge {
			n.logger.Printf("spool: notification %s too old, dropped unsent", e.ID)
			done.add(e.ID)
			continue
		}
		switch n.replayNotify(ctx, sp, e) {
		case notifyGone:
			return false
		case notifySent:
			n.logger.Printf("spool: notification %s handed over on a retry", e.ID)
			done.add(e.ID)
		case notifyBusy, notifyRefused:
			again = true
		case notifyTimedOut, notifyHubAway:
			return true // the manager cannot take any now: the rest waits for the next round, untried
		}
	}
	return again
}

// replayQuestion restores the lifecycle of a managed AskUserQuestion without creating a card. It also runs for an
// event whose stale hook.event is dropped: that filtering is only about news, never about a live hook's pairing (#2382).
func (n *Node) replayQuestion(e spoolEntry) {
	var in hookInput
	if json.Unmarshal(e.Request.Input, &in) != nil || e.Request.ReportOnly || in.ToolName != "AskUserQuestion" {
		return
	}
	switch in.HookEventName {
	case "PreToolUse":
		n.questions.before(in, e.At)
	case "PermissionRequest":
		n.questions.pair(in, e.Request.Attempt, false, e.At)
	case "PostToolUse":
		in.ToolInput = questionInputWithoutAnswers(in.ToolInput)
		n.questions.complete(in, e.At)
	}
}

// replayHook is what handleHook does with an event before it asks the phone (bookkeeping and the report), for a hook
// that standby answered: no card, it is long over or asks again by itself.
func (n *Node) replayHook(e spoolEntry) error {
	req := e.Request
	req.Pane = n.ownPane(req.Pane)
	var in hookInput
	if json.Unmarshal(req.Input, &in) != nil {
		return nil
	}
	if n.sessionBlocked(in.SessionID, in.Cwd) {
		return nil // a blocked Agent folder (agentdirs.go): kept while the hub was away, never sent
	}
	if req.ReportOnly && !n.takesReport(in) {
		return nil
	}
	stopSig := n.recordHookTurn(req, in, e.At)
	if !req.ReportOnly {
		if paneKindOK(n, req.Pane) && !n.ahead.paneTaken(req.Pane) {
			n.bindPane(req.Pane, in.SessionID)
		}
	}
	n.rememberTranscript(in)
	n.replayQuestion(e)
	p := hookEventParams(in, stopSig, req.ReportOnly)
	p["spool_id"], p["spooled_at"] = e.ID, e.At
	return n.peer.Notify("hook.event", p)
}
