//go:build linux || darwin

package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/herdrcore"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/screen"
)

// Test hooks for OS process-environment and tmux-foreground verification.
var (
	readCodexWriterProcess    = readCodexWriterProcessOS
	checkTmuxWriterForeground = sameTmuxPaneForegroundOS
)

// codexWriterLockHeldOS performs a non-destructive flock probe on Codex's per-thread writer lock.
// It never creates, writes, or deletes the lock file.
func codexWriterLockHeldOS(src codexSource) bool {
	if src.home == "" || src.thread == "" {
		return false
	}
	lockPath := filepath.Join(src.home, "thread-writer-locks", src.thread+".lock")
	f, err := os.Open(lockPath)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// Ctrl+D enters Codex's shutdown-first TUI path. Process signals do not: never
// replace this with SIGTERM/SIGKILL, or treat a timeout as a released writer.
func (n *Node) stopCodexWriter(ctx context.Context, writer codexWriter) error {
	send, err := n.prepareCodexWriterQuit(ctx, writer)
	if err != nil {
		return err
	}
	if !sameProcess(writer.PID, writer.Start) {
		return takeoverRefusal("changed")
	}
	if err := send(); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, takeoverExitWait)
	defer cancel()
	for sameProcess(writer.PID, writer.Start) {
		select {
		case <-wait.Done():
			// No escalation: a stuck writer stays in its original terminal.
			return takeoverRefusal("writer_busy")
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

func (n *Node) prepareCodexWriterQuit(ctx context.Context, writer codexWriter) (func() error, error) {
	if writer.PID <= 1 || writer.Start == "" || !sameProcess(writer.PID, writer.Start) {
		return nil, takeoverRefusal("changed")
	}
	if procs, err := scanOrcaProcs(); err == nil {
		for key, ps := range procs {
			for _, p := range ps {
				if p.pid == writer.PID {
					return codexQuitTerminal(ctx, writer, key)
				}
			}
		}
	}
	proc, ok := readCodexWriterProcess(writer.PID)
	if !ok {
		return nil, takeoverRefusal("unavailable")
	}
	if proc.tmuxPane != "" {
		send, owned, err := n.codexQuitTmux(ctx, writer, proc.tmuxPane, proc.tmuxSocket)
		if err == nil {
			return send, nil
		}
		if owned {
			return nil, err
		}
	}
	if proc.pane != "" {
		h := n.takeoverScreenHost(host.Herdr)
		paneID := proc.pane
		if hh, ok := h.(*herdrHost); ok {
			paneID = herdr.PaneOf(proc.pane, proc.herdrSession, hh.ownSession())
		} else if n != nil && n.hosts != nil {
			paneID = n.herdrPaneIn(proc.pane, proc.herdrSession)
		} else if proc.herdrSession != "" {
			paneID = herdr.PaneOf(proc.pane, proc.herdrSession, "")
		}
		send, owned, err := n.codexQuitHostPane(ctx, h, host.ParsePane(paneID), writer)
		if err == nil {
			return send, nil
		}
		if owned {
			return nil, err
		}
	}
	if proc.herdrCore != "" {
		send, owned, err := n.codexQuitHostPane(ctx, n.takeoverScreenHost(host.HerdrCore), host.PaneRef{Kind: host.HerdrCore, ID: proc.herdrCore}, writer)
		if err == nil {
			return send, nil
		}
		if owned {
			return nil, err
		}
	}
	if proc.ternPane != "" {
		send, owned, err := n.codexQuitHostPane(ctx, n.takeoverScreenHost(host.Tern), host.PaneRef{Kind: host.Tern, ID: proc.ternPane}, writer)
		if err == nil {
			return send, nil
		}
		if owned {
			return nil, err
		}
	}
	// Ordinary external terminals (iTerm2, Ghostty, VSCode, SSH, etc.) have no safe screen-verified quit path:
	// never fall back to OS signals.
	return nil, takeoverRefusal("unavailable")
}

func readCodexWriterProcessOS(pid int) (codexProcess, bool) {
	if pid <= 1 {
		return codexProcess{}, false
	}
	var raw []byte
	var err error
	if runtime.GOOS == "linux" {
		raw, err = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		if err != nil || len(raw) > 1<<20 {
			return codexProcess{}, false
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		raw, err = exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		cancel()
		if err != nil || len(raw) == 0 {
			return codexProcess{}, false
		}
	}
	return codexProcessArgs(pid, []string{"codex"}, string(raw))
}

func (n *Node) takeoverScreenHost(kind string) host.Host {
	if n != nil && n.hosts != nil {
		if h := n.hosts.Get(kind); h != nil {
			return h
		}
	}
	switch kind {
	case host.Tmux:
		return newTmuxHost()
	case host.Herdr:
		return newHerdrHost(herdr.New(), nil)
	case host.HerdrCore:
		return &herdrCoreHost{c: herdrcore.New()}
	case host.Tern:
		return newTernHost(ternClient())
	}
	return nil
}

func (n *Node) codexQuitTmux(ctx context.Context, writer codexWriter, pane, socket string) (func() error, bool, error) {
	paneStr := tmuxPaneOf(pane, socket)
	if paneStr == "" {
		return nil, false, takeoverRefusal("unavailable")
	}
	ref := host.ParsePane(paneStr)
	h := n.takeoverScreenHost(host.Tmux)
	if h == nil {
		return nil, false, takeoverRefusal("unavailable")
	}
	verifyOwner := func(recheck bool) (bool, error) {
		if !sameProcess(writer.PID, writer.Start) {
			return false, takeoverRefusal("changed")
		}
		if th, ok := h.(*tmuxHost); ok {
			sock, id := splitTmuxPane(ref.ID)
			info, err := th.client(sock).PaneInfo(ctx, id)
			if err != nil || info.Dead || !checkTmuxWriterForeground(writer.PID, info.PID, info.TTY) {
				if recheck {
					return false, takeoverRefusal("changed")
				}
				return false, takeoverRefusal("unavailable")
			}
			if info.InMode {
				if recheck {
					return true, takeoverRefusal("changed")
				}
				return true, takeoverRefusal("unavailable")
			}
			return true, nil
		}
		pids, err := h.PaneProcesses(ctx, ref)
		if err != nil || !slices.Contains(pids, writer.PID) {
			if recheck {
				return false, takeoverRefusal("changed")
			}
			return false, takeoverRefusal("unavailable")
		}
		return true, nil
	}
	checkComposer := func() error {
		text, err := h.Read(ctx, ref, host.ReadOpts{Source: "visible", Lines: 120})
		if err != nil || !codexQuitComposer(text) {
			return takeoverRefusal("unavailable")
		}
		return nil
	}
	owned, err := verifyOwner(false)
	if err != nil {
		return nil, owned, err
	}
	if err := checkComposer(); err != nil {
		return nil, true, err
	}
	return func() error {
		if _, err := verifyOwner(true); err != nil {
			return err
		}
		if err := checkComposer(); err != nil {
			return err
		}
		// Re-verify immediately before Ctrl+D so a Codex that exited during the screen RPC cannot send Ctrl+D to a shell.
		if _, err := verifyOwner(true); err != nil {
			return err
		}
		if err := h.SendKeys(ctx, ref, "ctrl+d"); err != nil {
			return takeoverRefusal("unavailable")
		}
		return nil
	}, true, nil
}

func (n *Node) codexQuitHostPane(ctx context.Context, h host.Host, ref host.PaneRef, writer codexWriter) (func() error, bool, error) {
	if h == nil {
		return nil, false, takeoverRefusal("unavailable")
	}
	verifyOwner := func(recheck bool) (bool, error) {
		if !sameProcess(writer.PID, writer.Start) {
			return false, takeoverRefusal("changed")
		}
		pids, err := h.PaneProcesses(ctx, ref)
		if err != nil || !slices.Contains(pids, writer.PID) {
			if recheck {
				return false, takeoverRefusal("changed")
			}
			return false, takeoverRefusal("unavailable")
		}
		return true, nil
	}
	readSource := "visible"
	if ref.Kind == host.Herdr {
		// Herdr's visible source returns scrolled-up history, and recent scrollback can hit macOS herdr's 8 KB
		// socket truncation limit; detection reads the live bottom grid directly.
		readSource = "detection"
	}
	checkComposer := func() error {
		text, err := h.Read(ctx, ref, host.ReadOpts{Source: readSource, Lines: 120})
		if err != nil || !codexQuitComposer(text) {
			return takeoverRefusal("unavailable")
		}
		return nil
	}
	owned, err := verifyOwner(false)
	if err != nil {
		return nil, owned, err
	}
	if err := checkComposer(); err != nil {
		return nil, true, err
	}
	return func() error {
		if _, err := verifyOwner(true); err != nil {
			return err
		}
		if err := checkComposer(); err != nil {
			return err
		}
		// Re-verify immediately before Ctrl+D so a Codex that exited during the screen RPC cannot send Ctrl+D to a shell.
		if _, err := verifyOwner(true); err != nil {
			return err
		}
		if err := h.SendKeys(ctx, ref, "ctrl+d"); err != nil {
			return takeoverRefusal("unavailable")
		}
		return nil
	}, true, nil
}

// sameTmuxPaneForegroundOS confirms writerPID is running in the foreground of the tmux pane rooted at panePID
// without crossing into a nested pty (such as herdr or another tmux inside the pane).
func sameTmuxPaneForegroundOS(writerPID, panePID int, paneTTY string) bool {
	if writerPID <= 1 || panePID <= 1 {
		return false
	}
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(writerPID) + "/stat")
		if err != nil {
			return false
		}
		if writerPID == panePID {
			i := strings.LastIndexByte(string(stat), ')')
			if i < 0 {
				return false
			}
			f := strings.Fields(string(stat)[i+1:])
			return len(f) > 0 && f[0] != "T" && f[0] != "t"
		}
		if !procInFront(string(stat)) {
			return false
		}
		_, writerTTY, ok := statPPIDTTY(writerPID)
		if !ok || writerTTY == "0" {
			return false
		}
		for cur, hops := writerPID, 0; hops < 64; hops++ {
			ppid, tty, ok := statPPIDTTY(cur)
			if !ok || tty != writerTTY {
				return false
			}
			if cur == panePID {
				return true
			}
			if ppid <= 1 || ppid == cur {
				return false
			}
			cur = ppid
		}
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	b, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,stat=,tty=").Output()
	cancel()
	if err != nil {
		return false
	}
	type darwinProc struct {
		ppid int
		stat string
		tty  string
	}
	tree := map[int]darwinProc{}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			tree[pid] = darwinProc{ppid: ppid, stat: f[2], tty: f[3]}
		}
	}
	w, ok := tree[writerPID]
	if !ok || strings.HasPrefix(w.stat, "T") {
		return false
	}
	if writerPID == panePID {
		return true
	}
	if !strings.Contains(w.stat, "+") || w.tty == "" || w.tty == "??" {
		return false
	}
	if paneTTY != "" {
		short := strings.TrimPrefix(paneTTY, "/dev/")
		if w.tty != short && "/dev/"+w.tty != paneTTY {
			return false
		}
	}
	for cur, hops := writerPID, 0; hops < 64; hops++ {
		node, ok := tree[cur]
		if !ok || node.tty != w.tty {
			return false
		}
		if cur == panePID {
			return true
		}
		if node.ppid <= 1 || node.ppid == cur {
			return false
		}
		cur = node.ppid
	}
	return false
}

// Prefer the incarnation-fenced native socket. An SSH terminal can instead carry
// its host's CLI bridge in the selected writer's own environment, not the node's.
func codexQuitTerminal(ctx context.Context, writer codexWriter, pane string) (func() error, error) {
	c := orcart.New()
	if list, err := c.Terminals(ctx, false); err == nil {
		for _, t := range list.Terminals {
			if t.PaneKey() == pane && t.Local() {
				target := t.Target()
				check := func() error {
					s, err := c.Read(ctx, target)
					if err != nil || !codexQuitComposer(s.Text()) {
						return takeoverRefusal("unavailable")
					}
					return nil
				}
				if err := check(); err != nil {
					return nil, err
				}
				return func() error {
					if err := check(); err != nil {
						return err
					}
					if !sameProcess(writer.PID, writer.Start) {
						return takeoverRefusal("changed")
					}
					return c.Send(ctx, target, "\x04")
				}, nil
			}
		}
	}
	if runtime.GOOS != "linux" {
		return nil, takeoverRefusal("unavailable")
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(writer.PID) + "/environ")
	if err != nil || len(raw) > 1<<20 {
		return nil, takeoverRefusal("unavailable")
	}
	env := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	value := func(name string) string {
		for _, entry := range env {
			if v, ok := strings.CutPrefix(entry, name+"="); ok {
				return v
			}
		}
		return ""
	}
	command, handle := value("ORCA_CLI_COMMAND"), value("ORCA_TERMINAL_HANDLE")
	if !filepath.IsAbs(command) || handle == "" || value(orcaPaneEnv) != pane {
		return nil, takeoverRefusal("unavailable")
	}
	type terminalInfo struct {
		Handle, IncarnationID, TabID, LeafID string
		Writable                             bool
	}
	inspect := func() (terminalInfo, error) {
		var out struct {
			OK     bool `json:"ok"`
			Result struct {
				Terminal struct {
					Handle        string `json:"handle"`
					IncarnationID string `json:"incarnationId"`
					TabID         string `json:"tabId"`
					LeafID        string `json:"leafId"`
					Writable      bool   `json:"writable"`
				} `json:"terminal"`
			} `json:"result"`
		}
		call := exec.CommandContext(ctx, command, "terminal", "show", "--terminal", handle, "--json")
		call.Env = env
		b, err := call.Output()
		if err != nil || len(b) > 1<<20 || json.Unmarshal(b, &out) != nil || !out.OK {
			return terminalInfo{}, takeoverRefusal("unavailable")
		}
		t := out.Result.Terminal
		if t.Handle != handle || t.TabID+":"+t.LeafID != pane || t.IncarnationID == "" || !t.Writable {
			return terminalInfo{}, takeoverRefusal("changed")
		}
		return terminalInfo{t.Handle, t.IncarnationID, t.TabID, t.LeafID, t.Writable}, nil
	}
	before, err := inspect()
	if err != nil {
		return nil, err
	}
	checkComposer := func() error {
		var out struct {
			OK     bool `json:"ok"`
			Result struct {
				Terminal struct {
					Tail  []string `json:"tail"`
					Draft *string  `json:"draft"`
				} `json:"terminal"`
			} `json:"result"`
		}
		call := exec.CommandContext(ctx, command, "terminal", "read", "--terminal", handle, "--limit", "120", "--json")
		call.Env = env
		b, err := call.Output()
		if err != nil || len(b) > 1<<20 || json.Unmarshal(b, &out) != nil || !out.OK {
			return takeoverRefusal("unavailable")
		}
		s := out.Result.Terminal
		if s.Draft != nil && strings.TrimSpace(*s.Draft) != "" || !codexQuitComposer(strings.Join(s.Tail, "\n")) {
			return takeoverRefusal("unavailable")
		}
		return nil
	}
	if err := checkComposer(); err != nil {
		return nil, err
	}
	return func() error {
		now, err := inspect()
		if err != nil || now != before || !sameProcess(writer.PID, writer.Start) {
			return takeoverRefusal("changed")
		}
		if err := checkComposer(); err != nil {
			return err
		}
		if !sameProcess(writer.PID, writer.Start) {
			return takeoverRefusal("changed")
		}
		call := exec.CommandContext(ctx, command, "terminal", "send", "--terminal", handle, "--text", "\x04", "--json")
		call.Env = env
		if err := call.Run(); err != nil {
			return takeoverRefusal("unavailable")
		}
		return nil
	}, nil
}

func codexQuitComposer(text string) bool {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	// Some CLI status bars show remaining context instead of a directory. Keep
	// every draft row, but do not mistake this exact final footer pair for one.
	if len(lines) >= 3 {
		status := strings.TrimRight(screen.Plain(lines[len(lines)-2]), " \t")
		hint := strings.TrimSpace(screen.Plain(lines[len(lines)-1]))
		if strings.HasPrefix(status, "  ") && strings.Contains(status, " \u00b7 Context ") &&
			strings.HasSuffix(status, " window") && hint == "\u2190 for agents \u00b7 ? for shortcuts" {
			lines[len(lines)-2] = ""
			text = strings.Join(lines, "\n")
		}
	}
	s := screen.ReadCodex(text)
	return s.View == screen.CodexComposer && strings.TrimSpace(s.Draft) == "" && len(s.Queued) == 0
}
