package node

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// restartTyped is the start command typed into w1:p1 to resume sidA in Allow All, "" when none was.
func restartTyped(f *fakeHerdr) string {
	for _, s := range typedStarts(f, "w1:p1") {
		if strings.Contains(s, "--resume "+sidA) && strings.Contains(s, "--permission-mode "+proto.ModeBypass) {
			return s
		}
	}
	return ""
}

// Allow All for an idle claude with no bypass flag (#6476): not refused, but restarted at once in the same terminal,
// resuming the same conversation on its own launcher with --permission-mode bypassPermissions. No tab is opened or
// closed, and the row is listed as starting meanwhile.
func TestBypassRestartsAnIdleAgentInIt(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	knownProcess(t, n, 4101)
	quitsToShell(f, cwd)
	comesUpWhen(f, "w1:p1", func(typed string) bool { return strings.Contains(typed, "--permission-mode "+proto.ModeBypass) })
	if err := pick(t, n, map[string]any{"permission_mode": proto.ModeBypass}); err != nil {
		t.Fatalf("Allow All refused: %v", err)
	}
	waitFor(t, "the restart in Allow All", func() bool { return restartTyped(f) != "" && n.switches().restartMode(sidA) == "" })
	typed := restartTyped(f)
	if strings.Contains(typed, " launch ") || strings.HasPrefix(typed, "cd ") {
		t.Errorf("not resumed on its own launcher in its folder: %q", typed)
	}
	trace := strings.Join(f.trace("pane.send_keys", "pane.send_input"), "\n")
	if quit, start := strings.Index(trace, sendKeys("w1:p1", "ctrl+c")), strings.Index(trace, "pane.send_input"); quit < 0 || start < quit {
		t.Fatalf("not quit, then started:\n%s", trace)
	}
	if got := f.trace("tab.create", "workspace.create", "tab.close", "pane.close"); len(got) != 0 {
		t.Errorf("a tab was opened or closed: %q", got)
	}
	if got := f.trace("pane.send_keys"); strings.Contains(strings.Join(got, ""), "shift+tab") {
		t.Errorf("pressed shift+tab, which never reaches Allow All here: %q", got)
	}
	waitFor(t, "the row out of starting", func() bool { return !n.switches().inFlight(sidA) })
	if mode, _ := n.modesOf(sidA); mode != proto.ModeBypass {
		t.Errorf("mode after the restart = %q", mode)
	}
}

// While the agent works the restart waits: nothing is pressed or typed (the running tool call goes on), the row says
// the restart waits, and once the turn is over the session list starts it.
func TestBypassRestartWaitsForTheTurnToEnd(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusWorking, nil)
	knownProcess(t, n, 4101)
	f.reset()
	if err := pick(t, n, map[string]any{"permission_mode": proto.ModeBypass}); err != nil {
		t.Fatalf("Allow All refused: %v", err)
	}
	row := rowOf(t, n, sidA)
	if !row.BypassPending {
		t.Fatal("the row does not say a restart waits")
	}
	// the try queueModeRestart started at once finds the agent busy and puts the restart off
	waitFor(t, "the first try put off", func() bool {
		sh := n.switches()
		sh.mu.Lock()
		defer sh.mu.Unlock()
		r := sh.restarts[sidA]
		return r != nil && !r.applying && !r.retryAt.IsZero()
	})
	if got := drove(f); len(got) != 0 {
		t.Fatalf("drove a working agent: %q", got)
	}
	// The turn ends.
	quitsToShell(f, cwd)
	comesUpWhen(f, "w1:p1", func(string) bool { return true })
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	sh := n.switches()
	sh.mu.Lock()
	if r := sh.restarts[sidA]; r != nil {
		r.retryAt = time.Time{} // no backoff wait in a test
	}
	sh.mu.Unlock()
	waitFor(t, "the restart once idle", func() bool {
		listSessions(t, n)
		return restartTyped(f) != ""
	})
}

// Picking another mode before the restart ran drops it: the agent keeps running, switched with shift+tab as always.
func TestAnotherModeDropsAWaitingRestart(t *testing.T) {
	n, f, _, _, _ := switchNode(t, proto.StatusWorking, nil)
	knownProcess(t, n, 4101)
	if err := pick(t, n, map[string]any{"permission_mode": proto.ModeBypass}); err != nil {
		t.Fatal(err)
	}
	if n.switches().restartMode(sidA) != proto.ModeBypass {
		t.Fatal("no restart waits")
	}
	cycleModes(t, f, "w1:p1", "default")
	if err := pick(t, n, map[string]any{"permission_mode": "auto"}); err != nil {
		t.Fatal(err)
	}
	if n.switches().restartMode(sidA) != "" || rowOf(t, n, sidA).BypassPending {
		t.Error("the restart still waits")
	}
	if restartTyped(f) != "" {
		t.Error("restarted anyway")
	}
}

// A claude started with a bypass flag reaches Allow All with shift+tab: no restart.
func TestBypassWithTheFlagNeedsNoRestart(t *testing.T) {
	n, _, _, _, _ := switchNode(t, proto.StatusIdle, nil)
	if !n.needsModeRestart(sidA, proto.ModeBypass) {
		t.Error("a claude without the flag needs no restart")
	}
	n.hooks = &hookTurns{}
	n.hooks.mu.Lock()
	n.hooks.entry(sidA, time.Now()).bypass = true
	n.hooks.mu.Unlock()
	for _, mode := range []string{proto.ModeBypass, "auto", "plan", "acceptEdits", "default"} {
		if n.needsModeRestart(sidA, mode) {
			t.Errorf("%s needs a restart", mode)
		}
	}
}

// A restart that cannot go now is put off, longer each time; done or failed drops it; one picked too long ago goes.
func TestModeRestartBackoff(t *testing.T) {
	var s switchShare
	now := time.Now()
	s.setRestart("x", proto.ModeBypass, now, 4101, "linux:4101")
	if s.takeRestart("x", now) != proto.ModeBypass {
		t.Fatal("not due")
	}
	if s.takeRestart("x", now) != "" {
		t.Fatal("taken twice")
	}
	s.settleRestart("x", proto.ModeBypass, errRestartLater, now)
	if s.takeRestart("x", now.Add(modeRestartRetry-time.Millisecond)) != "" {
		t.Fatal("tried again before its retry")
	}
	if s.takeRestart("x", now.Add(modeRestartRetry)) == "" {
		t.Fatal("not tried again after its retry")
	}
	s.settleRestart("x", proto.ModeBypass, errRestartLater, now)
	if s.takeRestart("x", now.Add(modeRestartRetry)) != "" {
		t.Fatal("the second wait is not longer")
	}
	for range 10 {
		s.restarts["x"].retryAt = time.Time{}
		s.takeRestart("x", now)
		s.settleRestart("x", proto.ModeBypass, errRestartLater, now)
	}
	if d := s.restarts["x"].delay; d != modeRestartMaxRetry {
		t.Errorf("delay grew to %v", d)
	}
	s.restarts["x"].retryAt = time.Time{}
	s.takeRestart("x", now)
	s.settleRestart("x", proto.ModeBypass, errors.New("did not come up"), now)
	if s.restartMode("x") != "" {
		t.Error("a failed restart still waits")
	}
	s.setRestart("y", proto.ModeBypass, now.Add(-modeRestartMaxAge-time.Minute), 4101, "linux:4101")
	if s.takeRestart("y", now) != "" || s.restartMode("y") != "" {
		t.Errorf("a restart picked over %v ago still runs", modeRestartMaxAge)
	}
	// Picked again for another process (#10296): bound to that one, waiting from now.
	s.setRestart("z", proto.ModeBypass, now.Add(-time.Hour), 4101, "linux:4101")
	s.setRestart("z", proto.ModeBypass, now, 4202, "linux:4202")
	if pid, start := s.restartProcess("z"); pid != 4202 || start != "linux:4202" || !s.restarts["z"].at.Equal(now) {
		t.Errorf("a pick for another process kept the old one: %+v", s.restarts["z"])
	}
}

// withProcesses has processAlive answer from the returned switch: run(pid, true) starts a process, run(pid, false) ends it.
func withProcesses(t *testing.T) (run func(pid int, on bool)) {
	t.Helper()
	var mu sync.Mutex
	alive := map[int]bool{}
	was := processAlive
	processAlive = func(pid int, _ string) bool {
		mu.Lock()
		defer mu.Unlock()
		return alive[pid]
	}
	t.Cleanup(func() { processAlive = was })
	return func(pid int, on bool) {
		mu.Lock()
		alive[pid] = on
		mu.Unlock()
	}
}

// knownProcess has sidA's claude be process pid, alive, as its hooks report it.
func knownProcess(t *testing.T, n *Node, pid int) (run func(pid int, on bool)) {
	t.Helper()
	run = withProcesses(t)
	if n.hooks == nil {
		n.hooks = &hookTurns{}
	}
	run(pid, true)
	hookedProcess(n, sidA, pid, fmt.Sprintf("linux:%d", pid))
	return run
}

// hookedProcess is what the hooks of session id's claude report about it (hookTurns.agent): a new process, without
// reading its arguments (no bypass flag).
func hookedProcess(n *Node, id string, pid int, start string) {
	n.hooks.mu.Lock()
	defer n.hooks.mu.Unlock()
	*n.hooks.entry(id, time.Now()) = hookTurn{pid: pid, start: start, touched: time.Now()}
}

// A restart into Allow All belongs to the claude it was picked for (#10296). Picked while that claude works, it waits for
// the turn to end. When that claude exits instead, and the conversation is resumed in the same pane by a new claude
// before the next try (here at the computer, in its own mode), the pick goes with the old process: the new one is
// never quit and restarted into Allow All.
func TestAWaitingBypassRestartGoesWithItsProcess(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusWorking, nil)
	run := knownProcess(t, n, 4101)
	f.reset()
	if err := pick(t, n, map[string]any{"permission_mode": proto.ModeBypass}); err != nil {
		t.Fatalf("Allow All refused: %v", err)
	}
	sh := n.switches()
	waitFor(t, "the first try put off", func() bool {
		sh.mu.Lock()
		defer sh.mu.Unlock()
		r := sh.restarts[sidA]
		return r != nil && !r.applying && !r.retryAt.IsZero()
	})
	// That claude exits; a new one resumes the conversation in the same pane, idle, without a bypass flag.
	run(4101, false)
	run(4202, true)
	hookedProcess(n, sidA, 4202, "linux:4202")
	quitsToShell(f, cwd)
	comesUpWhen(f, "w1:p1", func(string) bool { return true })
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	sh.mu.Lock()
	if r := sh.restarts[sidA]; r != nil {
		r.retryAt = time.Time{} // no backoff wait in a test
	}
	sh.mu.Unlock()
	listSessions(t, n)
	n.tryModeRestart(sidA) // the try the listing starts, here to its end
	waitFor(t, "no try under way", func() bool {
		sh.mu.Lock()
		defer sh.mu.Unlock()
		r := sh.restarts[sidA]
		return r == nil || !r.applying
	})
	if typed := restartTyped(f); typed != "" {
		t.Fatalf("the claude that resumed the conversation after the one Allow All was picked for was quit and restarted into it: %q", typed)
	}
	if n.switches().restartMode(sidA) != "" {
		t.Error("the pick still waits, for a process that is gone")
	}
	if rowOf(t, n, sidA).BypassPending {
		t.Error("the row still says a restart waits")
	}
}

// The claude a restart waits for exits and nothing takes its place: the pick goes at the next look at the session, and
// the row stops saying a restart waits (#10296).
func TestAWaitingBypassRestartGoesWhenItsProcessExits(t *testing.T) {
	n, f, _, _, _ := switchNode(t, proto.StatusWorking, nil)
	run := knownProcess(t, n, 4101)
	if err := pick(t, n, map[string]any{"permission_mode": proto.ModeBypass}); err != nil {
		t.Fatal(err)
	}
	if !rowOf(t, n, sidA).BypassPending {
		t.Fatal("no restart waits")
	}
	// A try a listing started may still be under way; one being carried out is left alone (dropGoneRestart).
	sh := n.switches()
	waitFor(t, "no try under way", func() bool {
		sh.mu.Lock()
		defer sh.mu.Unlock()
		r := sh.restarts[sidA]
		return r == nil || !r.applying
	})
	run(4101, false)
	if rowOf(t, n, sidA).BypassPending || n.switches().restartMode(sidA) != "" {
		t.Error("the pick outlived the claude it was picked for")
	}
	if restartTyped(f) != "" {
		t.Error("restarted anyway")
	}
}

// Where Persona cannot tell which claude process runs the session (no hook has said, no sessions/<pid>.json names it),
// Allow All by a restart is refused rather than kept for whichever claude comes next (#10296).
func TestBypassRestartNeedsAKnownProcess(t *testing.T) {
	n, f, _, _, _ := switchNode(t, proto.StatusIdle, nil)
	n.hooks = &hookTurns{}
	err := pick(t, n, map[string]any{"permission_mode": proto.ModeBypass})
	if err == nil || !strings.Contains(err.Error(), "cannot tell which Claude Code process") {
		t.Fatalf("pick = %v, want the refusal", err)
	}
	if n.switches().restartMode(sidA) != "" || rowOf(t, n, sidA).BypassPending {
		t.Error("a restart was queued anyway")
	}
	if got := drove(f); len(got) != 0 {
		t.Errorf("drove the agent: %q", got)
	}
}

// The process comes from the hooks while what they name runs, else from the sessions/<pid>.json of a running claude
// with the session open (a claude whose hooks have not fired since the node started).
func TestAgentProcessFromHooksElseSessionFiles(t *testing.T) {
	n, _, home := herdrNode(t)
	if pid, _ := n.agentProcess(sidA); pid != 0 {
		t.Fatalf("a process for a session nothing runs: %d", pid)
	}
	writeClaudeSession(t, home, os.Getpid(), sidA, "cli") // this test's own pid: alive
	if pid, _ := n.agentProcess(sidA); pid != os.Getpid() {
		t.Errorf("from the session file = %d, want %d", pid, os.Getpid())
	}
	run := withProcesses(t)
	n.hooks = &hookTurns{}
	hookedProcess(n, sidA, 4101, "linux:4101")
	if pid, _ := n.agentProcess(sidA); pid != os.Getpid() {
		t.Errorf("a hooked process that has exited won over the running one: %d", pid)
	}
	run(4101, true)
	if pid, start := n.agentProcess(sidA); pid != 4101 || start != "linux:4101" {
		t.Errorf("from the hooks = %d %q", pid, start)
	}
}
