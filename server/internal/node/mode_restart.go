package node

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Allow All for a running Claude Code Agent that cannot reach it with shift+tab (#6476).
//
// Claude Code puts bypassPermissions in its shift+tab cycle only when it was started with a bypass flag (argsCanBypass,
// #1379): for any other process no key reaches it. Picking Allow All for such an agent (session.modes with
// permission_mode bypassPermissions while bypass_available is false) is not refused any more. The node queues a
// restart instead: once the agent's turn is over (it neither works nor waits for the user, nothing runs in the
// background a quit would end: switchBusy, the launcher switch's rule) the agent is quit with Ctrl+C and the same
// conversation resumed in the same terminal, on the launcher and model it runs on, with
// `--resume <id> --permission-mode bypassPermissions`, as a launcher switch restarts it (launcher_switch.go). Nothing
// running is interrupted: a busy agent keeps its turn, and the restart waits.
//
//   - The row says it waits (Session.bypass_pending) and keeps the mode it still runs in; the apps say the session will
//     restart to apply it.
//   - The session list starts it once the row is idle (modeRestartRow), as it runs a queued ultracode switch: it takes
//     its turn at the input as a message does (inputTurn), so a message sent meanwhile goes to the restarted agent, and
//     while it restarts the row is listed as starting (switchShare.begin): messages and the user's Resume wait for it.
//   - A draft in the box or a dialog in front puts it off (retried after modeRestartRetry, doubling); nothing is typed
//     over what someone types at the computer.
//   - Picking any other mode before it ran drops it (picking the mode it still runs in calls it off: cancelled, not
//     replaced, #11089); so does a restart that failed (the agent is resumed in the mode it had when the new start did
//     not come up), and a pick older than modeRestartMaxAge.
//   - It belongs to the claude process it was picked for (#10296): the pick keeps that process' pid and start (the
//     hooks', else its sessions/<pid>.json), and goes once that process has exited or the session runs in another one.
//     A claude that resumes the conversation later, at the computer or by Persona's Resume, was never chosen for Allow
//     All and is never restarted into it by an old pick. Where Persona cannot tell the process, the pick is refused.
//   - It lives in the node's memory, shared by a resident node's Nodes (switch_share.go): a node restart forgets it, and
//     nothing restarts the agent behind the user's back after that.
//   - What became of it (done, failed, dropped and why) goes to the manager as a session.mode_restart event, kept
//     with the session's mode changes (#10297, devices/mode_changes.go); session.modes answers applied "restart"
//     when a pick waits for one, "now" when it took effect at once.

const (
	modeRestartRetry    = 3 * time.Second
	modeRestartMaxRetry = time.Minute
	// modeRestartMaxAge: how long a pick waits for the turn to end. Long enough for a long turn, short enough that a pick
	// the owner has forgotten does not run hours later (#10296; a launcher switch's switchMaxAge is a day).
	modeRestartMaxAge = 2 * time.Hour
)

// errRestartProcessUnknown: Allow All by a restart is refused where Persona cannot tell which claude process the pick
// is for, so it could not tell when that process is gone (#10296).
var errRestartProcessUnknown = errors.New("Persona cannot tell which Claude Code process runs this session yet, so it cannot restart it in Allow All; try again once the agent has answered")

// modeRestart is a restart of a session in mode that waits for its turn to end, for the claude process pid (started
// at start) it was picked for.
type modeRestart struct {
	mode     string
	at       time.Time
	pid      int
	start    string
	applying bool
	retryAt  time.Time
	delay    time.Duration
}

// errRestartLater: the restart cannot go now (busy, a draft, a dialog) and waits for a later look.
var errRestartLater = errors.New("the agent is busy: the restart waits for its turn to end")

// setRestart queues a restart of session id in mode for the claude process pid / start ("" drops one).
func (s *switchShare) setRestart(id, mode string, now time.Time, pid int, start string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode == "" {
		delete(s.restarts, id)
		return
	}
	if s.restarts == nil {
		s.restarts = map[string]*modeRestart{}
	}
	if r := s.restarts[id]; r != nil && r.mode == mode && r.pid == pid && r.start == start {
		return // already waiting: keep its place and its backoff
	}
	s.restarts[id] = &modeRestart{mode: mode, at: now, pid: pid, start: start}
}

// restartProcess is the claude process session id's waiting restart was picked for, 0 when none waits.
func (s *switchShare) restartProcess(id string) (pid int, start string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.restarts[id]; r != nil {
		return r.pid, r.start
	}
	return 0, ""
}

// expireRestart drops session id's restart when it was picked over modeRestartMaxAge ago and is not being carried
// out, and returns its mode ("" when nothing went).
func (s *switchShare) expireRestart(id string, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.restarts[id]
	if r == nil || r.applying || now.Sub(r.at) <= modeRestartMaxAge {
		return ""
	}
	delete(s.restarts, id)
	return r.mode
}

// restartApplying: session id's restart is being carried out now (taken by takeRestart, not settled yet).
func (s *switchShare) restartApplying(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.restarts[id]
	return r != nil && r.applying
}

// restartMode is the mode session id waits to be restarted in, "" when none.
func (s *switchShare) restartMode(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.restarts[id]; r != nil {
		return r.mode
	}
	return ""
}

// takeRestart marks session id's restart as being tried and returns its mode, "" when none is due now.
func (s *switchShare) takeRestart(id string, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.restarts[id]
	if r == nil || r.applying || now.Before(r.retryAt) {
		return ""
	}
	if now.Sub(r.at) > modeRestartMaxAge {
		delete(s.restarts, id)
		return ""
	}
	r.applying = true
	return r.mode
}

// settleRestart ends a try of session id's restart in mode: done or failed drops it, errRestartLater puts it off.
func (s *switchShare) settleRestart(id, mode string, err error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.restarts[id]
	if r == nil || r.mode != mode {
		return // dropped or replaced meanwhile
	}
	r.applying = false
	if !errors.Is(err, errRestartLater) {
		delete(s.restarts, id)
		return
	}
	r.delay = min(max(r.delay*2, modeRestartRetry), modeRestartMaxRetry)
	r.retryAt = now.Add(r.delay)
}

// needsModeRestart: mode can reach session id's running agent only by starting it again (Allow All for a claude whose
// process has no bypass flag).
func (n *Node) needsModeRestart(id, mode string) bool {
	return mode == proto.ModeBypass && !n.hooks.canBypass(id)
}

// restartable says why session id cannot be restarted in its terminal by the node, nil when it can: a Claude Code
// Agent in a pane of a terminal host that can start one there, on a launcher Persona can tell, in a folder Persona can
// tell (what a launcher switch needs to come back).
func (n *Node) restartable(ctx context.Context, id string) error {
	a, _, _, err := n.switchSession(ctx, id, false)
	if err != nil {
		return err
	}
	if _, known := n.ownLauncher(id); !known {
		return fmt.Errorf("Persona cannot tell which launcher session %s runs on, so it could not resume it", id)
	}
	if n.switchCwd(id, a) == "" {
		return fmt.Errorf("Persona cannot tell which folder session %s's conversation belongs to", id)
	}
	return nil
}

// queueModeRestart queues session id's restart in mode for the claude process it runs in now, and has it tried at once
// (it waits when the agent is busy). Where that process cannot be told, nothing is queued (errRestartProcessUnknown).
func (n *Node) queueModeRestart(id, mode string) error {
	pid, start := n.agentProcess(id)
	if pid <= 0 {
		n.logf("session %s: restart in permission mode %s refused: its claude process is not known", id, mode)
		return errRestartProcessUnknown
	}
	n.switches().setRestart(id, mode, time.Now(), pid, start)
	n.logf("session %s: restart in permission mode %s waits for the agent's turn to end (claude %d)", id, mode, pid)
	n.notifySessions()
	go n.tryModeRestart(id)
	return nil
}

// dropModeRestart drops a restart session id waits for, as a pick of another mode, picked, does. A pick of the mode the
// session runs in now calls it off and changes nothing else (cancelled, #11089); any other replaces it.
func (n *Node) dropModeRestart(id, picked string) {
	mode := n.switches().restartMode(id)
	if mode == "" {
		return
	}
	reason := proto.ModeRestartReasonReplaced
	if now, _ := n.modesOf(id); now == picked {
		reason = proto.ModeRestartReasonCancelled
	}
	n.switches().setRestart(id, "", time.Time{}, 0, "")
	n.logf("session %s: waiting restart in permission mode %s dropped: %s picked (%s)", id, mode, picked, reason)
	n.reportModeRestart(id, mode, proto.ModeRestartDropped, reason)
	n.notifySessions()
}

// reportModeRestart tells the manager what became of session id's restart in mode and why (a ModeRestartReason… code,
// #10297): kept with the session's mode changes.
func (n *Node) reportModeRestart(id, mode, outcome, reason string) {
	if n.peer == nil {
		return
	}
	_ = n.peer.Notify(proto.EventModeRestart, proto.ModeRestartEvent{SessionID: id, Mode: mode, Outcome: outcome, Reason: reason})
}

// restartGoneError: the claude a restart was picked for is gone (restartGone), so it is dropped, not failed: why in
// words for the log, and as a ModeRestartReason… code.
type restartGoneError struct{ why, code string }

func (e restartGoneError) Error() string { return e.why }

// agentProcess is the claude process session id runs in: as its hooks last reported it, else as the sessions/<pid>.json
// of a running claude with that session open says; 0 when neither can tell.
func (n *Node) agentProcess(id string) (pid int, start string) {
	if pid, start := n.hooks.process(id); pid > 0 && processAlive(pid, start) {
		return pid, start
	}
	for _, c := range n.liveClaudes() {
		if c.SessionID == id {
			return c.pid, claudeFileStart(c.ProcStart)
		}
	}
	return 0, ""
}

// restartGone says why session id's waiting restart no longer belongs to a running agent, "" while it does: the claude
// process it was picked for has exited, or the hooks report the session in another process (a resume) since. code is
// the same as a ModeRestartReason….
func (n *Node) restartGone(id string) (why, code string) {
	pid, start := n.switches().restartProcess(id)
	if pid <= 0 {
		return "it was not picked for a known claude process", proto.ModeRestartReasonProcessUnknown
	}
	if !processAlive(pid, start) {
		return fmt.Sprintf("claude %d, the process it was picked for, has exited", pid), proto.ModeRestartReasonProcessExited
	}
	if hp, hs := n.hooks.process(id); hp > 0 && (hp != pid || hs != "" && start != "" && hs != start) {
		return fmt.Sprintf("the session runs in claude %d now, not in claude %d it was picked for", hp, pid), proto.ModeRestartReasonProcessReplaced
	}
	return "", ""
}

// dropGoneRestart drops session id's waiting restart when its process is gone (restartGone), and says whether it did.
// One being carried out is left alone: quitting that process is the restart's own first step (runModeRestart checks
// before it).
func (n *Node) dropGoneRestart(id string) bool {
	if n.switches().restartApplying(id) {
		return false
	}
	why, code := n.restartGone(id)
	if why == "" {
		return false
	}
	mode := n.switches().restartMode(id)
	n.switches().setRestart(id, "", time.Time{}, 0, "")
	n.reportModeRestart(id, mode, proto.ModeRestartDropped, code)
	n.logf("session %s: waiting restart in another permission mode dropped: %s", id, why)
	return true
}

// modeRestartRow marks a row whose agent waits for a restart in Allow All, and starts it once the row is idle.
func (n *Node) modeRestartRow(s *proto.Session) {
	mode := n.switches().restartMode(s.SessionID)
	if mode == "" {
		return
	}
	if s.PermissionMode == mode && s.BypassAvailable {
		n.switches().setRestart(s.SessionID, "", time.Time{}, 0, "") // in it already (set at the computer)
		n.reportModeRestart(s.SessionID, mode, proto.ModeRestartDone, proto.ModeRestartReasonAlreadyIn)
		return
	}
	if n.dropGoneRestart(s.SessionID) { // the claude it was picked for is gone (#10296)
		go n.notifySessions()
		return
	}
	s.BypassPending = mode == proto.ModeBypass
	if s.PaneID != "" && !s.Starting && (s.Status == proto.StatusIdle || s.Status == proto.StatusDone) {
		go n.tryModeRestart(s.SessionID)
	}
}

// tryModeRestart tries session id's queued restart, when one is due (takeRestart), and settles it.
func (n *Node) tryModeRestart(id string) {
	if mode := n.switches().expireRestart(id, time.Now()); mode != "" {
		why := fmt.Sprintf("picked over %v ago", modeRestartMaxAge)
		n.logf("session %s: restart in permission mode %s dropped: %s", id, mode, why)
		n.reportModeRestart(id, mode, proto.ModeRestartDropped, proto.ModeRestartReasonExpired)
		n.notifySessions()
		return
	}
	mode := n.switches().takeRestart(id, time.Now())
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), switchProbeWait)
	defer cancel()
	err := n.runModeRestart(ctx, id, mode)
	n.switches().settleRestart(id, mode, err, time.Now())
	var gone restartGoneError
	switch {
	case err == nil:
		n.logf("session %s: restarted in permission mode %s", id, mode)
		n.reportModeRestart(id, mode, proto.ModeRestartDone, "")
	case errors.Is(err, errRestartLater):
	case errors.As(err, &gone):
		n.logf("session %s: restart in permission mode %s dropped: %v", id, mode, err)
		n.reportModeRestart(id, mode, proto.ModeRestartDropped, gone.code)
	default:
		n.logf("session %s: restart in permission mode %s dropped: %v", id, mode, err)
		n.reportModeRestart(id, mode, proto.ModeRestartFailed, restartFailure(err))
	}
	n.notifySessions()
}

// runModeRestart restarts session id in mode when its turn is over, holding the input as a message does.
func (n *Node) runModeRestart(ctx context.Context, id, mode string) error {
	unlock, err := n.inputTurn(ctx, id)
	if err != nil {
		return errRestartLater
	}
	defer unlock()
	if n.switches().restartMode(id) != mode {
		return nil // dropped while it waited for the input
	}
	if why, code := n.restartGone(id); why != "" {
		return restartGoneError{why, code} // the claude it was picked for is gone: a later one is never restarted by it (#10296)
	}
	if why := n.switchBusy(ctx, id); why != "" {
		return errRestartLater
	}
	pane, err := n.paneFor(ctx, id)
	if err != nil {
		return err
	}
	if err := n.checkPaneFor(ctx, pane, "claude", sendOpts{}); err != nil {
		return errRestartLater // a draft or a dialog: nothing typed over it
	}
	return n.restartInMode(ctx, id, mode)
}

// restartInMode quits session id's agent and resumes it in the same terminal, on its own launcher and model, in mode.
// A start that does not come up is followed by one in the mode it had, as a failed launcher switch comes back.
func (n *Node) restartInMode(ctx context.Context, id, mode string) error {
	defer n.markSwitching(id)()
	a, h, ref, err := n.switchSession(ctx, id, false)
	if err != nil {
		return err
	}
	own, known := n.ownLauncher(id)
	cwd := n.switchCwd(id, a)
	if !known || cwd == "" {
		return switchUnsupported("Persona cannot tell the launcher or the folder to resume it with")
	}
	prev, had := n.state.get(id)
	r := n.resumeWith(id, sendOpts{})
	execution := n.ranExecution(id, prev)
	// The last look before the first Ctrl+C.
	if why := n.switchBusy(ctx, id); why != "" {
		return errRestartLater
	}
	row := proto.Session{SessionID: id, Cwd: cwd, Launcher: own, Agent: "claude", Status: proto.StatusIdle, Starting: true,
		StartedByPersona: had, UpdatedAt: time.Now().UTC(), WorkspaceID: a.WorkspaceID, PermissionMode: r.mode, BypassPending: true}
	if !n.switches().begin(id, row) {
		return switchUnderWay()
	}
	defer func() {
		if serving := n.switches().end(id); serving != nil && serving != n {
			serving.notifySessions()
		}
		n.notifySessions()
	}()
	// From the first Ctrl+C on, on its own clock: the agent is never left half restarted.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), switchBudget)
	defer cancel()
	n.logf("session %s: restarting in %s on launcher %s in %s", id, mode, own, a.PaneID)
	if quit, err := n.quitAgent(wctx, id, a.PaneID); err != nil || !quit {
		return refusal(proto.CodeSwitchNotStopped, fmt.Sprintf("the agent did not quit (%v); it runs on as before", err), nil)
	}
	shells, dir, err := n.shellAfterQuit(wctx, id, "claude", a, h, ref)
	if err != nil {
		return refusal(proto.CodeSwitchFailedStopped, fmt.Sprintf("the agent quit, but its terminal did not come back to a shell prompt: %v", err), nil)
	}
	start := func(mode string, shells []string, dir string) error {
		sctx, cancel := switchStartCtx(wctx, a, h, ref, shells, dir, cwd)
		defer cancel()
		_, err := n.start(sctx, cwd, own, "", id, a.WorkspaceID, mode, r.effort, "", "", "claude", execution)
		return err
	}
	startErr := start(mode, shells, dir)
	if startErr == nil {
		n.noteModes(id, mode, "")
		return nil
	}
	n.logf("session %s: the agent did not come up in %s: %v", id, mode, startErr)
	n.state.restoreLaunch(id, prev, had, own)
	if shells, dir, err = n.clearPane(wctx, id, a, h, ref); err == nil {
		err = start(r.mode, shells, dir)
		n.state.restoreLaunch(id, prev, had, own)
	}
	if err != nil {
		return refusal(proto.CodeSwitchFailedStopped, fmt.Sprintf("the agent did not come up in %s (%v), nor again as before (%v); it has stopped", mode, startErr, err), nil)
	}
	return refusal(proto.CodeSwitchFailed, fmt.Sprintf("the agent did not come up in %s (%v); it runs as before", mode, startErr), nil)
}

// modesAnswer is session.modes' answer once it took a pick of mode (#10297): applied "restart" when a restart into it
// waits for the turn to end, "now" otherwise (shift+tab, or an assistant conversation's next message).
func (n *Node) modesAnswer(id, mode string) map[string]any {
	out := map[string]any{"ok": true}
	if mode == "" {
		return out
	}
	out["applied"] = "now"
	if n.switches().restartMode(id) == mode {
		out["applied"] = "restart"
	}
	return out
}

// restartFailure is the ModeRestartReason… of a restart that did not work, "" for one the node cannot name.
func restartFailure(err error) string {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return ""
	}
	switch re.Code {
	case proto.CodeSwitchNotStopped:
		return proto.ModeRestartReasonNotStopped
	case proto.CodeSwitchFailed:
		return proto.ModeRestartReasonStartFailed
	case proto.CodeSwitchFailedStopped:
		return proto.ModeRestartReasonStopped
	}
	return ""
}
