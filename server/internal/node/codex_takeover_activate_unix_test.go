//go:build linux || darwin

package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tmux"
)

func TestCodexHeldSharedAppServerReportsSocketGone(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	cwd := t.TempDir()
	ctrlDir := filepath.Join(home, "app-server-control")
	daemonDir := filepath.Join(home, "app-server-daemon")
	if err := os.MkdirAll(ctrlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(daemonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(ctrlDir, "app-server-control.sock")
	realCtrl, err := filepath.EvalSymlinks(ctrlDir)
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(filepath.Join(realCtrl, "app-server-control.sock")))
	missingTarget := filepath.Join(tmp, "codex-daemon-"+strconv.Itoa(os.Geteuid()), hex.EncodeToString(sum[:]))
	if err := os.Symlink(missingTarget, alias); err != nil {
		t.Fatal(err)
	}
	pidJSON, _ := json.Marshal(map[string]int{"pid": os.Getpid()})
	if err := os.WriteFile(filepath.Join(daemonDir, "daemon.pid"), pidJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	at := time.Now().Add(-time.Minute)
	path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", at, codexEventAt("task_started", at), codexEventAt("task_complete", at))
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n.codices.scan = func() []codexProcess {
		return []codexProcess{{pid: os.Getpid(), home: home, endpoint: "unix://"}}
	}
	n.discoverCodex()
	id := n.registerCodex(home, remoteRaw, path)
	s := byID(n.codexSessions(map[string]bool{}))[id]
	want := i18n.T(i18n.Default, "codex-history.shared_socket_gone")
	if s.ReadOnlyReason != want {
		t.Fatalf("ReadOnlyReason = %q, want %q", s.ReadOnlyReason, want)
	}

	// A separate custom listener in the same home must NOT inherit the broken default daemon's socket_gone diagnosis.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()
	customEP := "ws://" + closedAddr
	n.codices.scan = func() []codexProcess {
		return []codexProcess{{pid: os.Getpid(), home: home, endpoint: customEP}}
	}
	n.discoverCodex()
	n.ensureCodexRemote(t.Context(), proto.CodexServer{ID: "srv-custom", Home: canonicalCodexHome(home, n.home), Endpoint: customEP})
	waitFor(t, "custom listener unreachable diagnostic", func() bool {
		n.codices.mu.Lock()
		defer n.codices.mu.Unlock()
		return n.codices.remoteDiags[customEP] == codexDiagUnreachable
	})
	s = byID(n.codexSessions(map[string]bool{}))[id]
	wantUnreachable := i18n.T(i18n.Default, "codex-history.shared_unreachable")
	if s.ReadOnlyReason != wantUnreachable {
		t.Fatalf("custom listener ReadOnlyReason = %q, want %q", s.ReadOnlyReason, wantUnreachable)
	}
}

func TestCodexTakeoverRespectsActiveThreadWriterLock(t *testing.T) {
	n, _, cwd := codexHistoryNode(t)
	home := n.codexHome()
	lockDir := filepath.Join(home, "thread-writer-locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(lockDir, startThread+".lock")
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock setup: %v", err)
	}

	n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
	s := byID(listSessions(t, n))[startThread]
	if s.Capabilities.Takeover == nil || !s.Capabilities.Takeover.Running || s.Capabilities.Takeover.CanResume || s.Capabilities.Takeover.Reason != "unavailable" {
		t.Fatalf("locked thread takeover cap = %+v", s.Capabilities.Takeover)
	}
	plan, err := n.prepareCodexTakeover(context.Background(), startThread)
	if err != nil || !plan.Running || plan.CanResume || plan.Reason != "unavailable" || plan.ContinuationPrompt == "" {
		t.Fatalf("locked thread prepare = (%+v, %v)", plan, err)
	}
	// Lock file must never be removed by our probe.
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file removed or missing: %v", err)
	}

	// Unlock -> prepare succeeds; re-lock before commit -> commit refuses writer_busy.
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	plan, err = n.prepareCodexTakeover(context.Background(), startThread)
	if err != nil || plan.Running || !plan.CanResume {
		t.Fatalf("unlocked thread prepare = (%+v, %v)", plan, err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	sharedCalled := false
	n.takeovers.shared = func(context.Context, codexSource) ([]string, error) {
		sharedCalled = true
		return []string{"codex", "--remote", "unix:///shared/socket"}, nil
	}
	if _, err := n.commitCodexTakeover(context.Background(), startThread, plan.Token, false); refusalCode(err) != "codex_takeover_writer_busy" {
		t.Fatalf("relocked commit err = %v, want codex_takeover_writer_busy", err)
	}
	_ = cwd
	_ = sharedCalled
}

func TestCodexTakeoverSafeQuitTmuxPane(t *testing.T) {
	writer := codexWriter{os.Getpid(), procStartMark(os.Getpid())}
	if writer.Start == "" {
		t.Skip("procStartMark unavailable")
	}

	for _, tc := range []struct {
		name         string
		inMode       string
		dead         string
		screenText   string
		fgOK         bool
		wantPrepare  bool
		wantSendErr  string
		wantSentCtrl bool
	}{
		{
			name:         "idle composer in tmux sends ctrl+d",
			inMode:       "0",
			dead:         "0",
			screenText:   "\u203a Ask Codex to do anything\n\n  gpt-6 low \u00b7 /work/example\n  ? for shortcuts",
			fgOK:         true,
			wantPrepare:  true,
			wantSentCtrl: true,
		},
		{
			name:        "tmux copy-mode refuses quit",
			inMode:      "1",
			dead:        "0",
			screenText:  "\u203a Ask Codex to do anything\n  ? for shortcuts",
			fgOK:        true,
			wantPrepare: false,
		},
		{
			name:        "unsent draft in tmux refuses quit",
			inMode:      "0",
			dead:        "0",
			screenText:  "\u203a Keep my draft\n  ? for shortcuts",
			fgOK:        true,
			wantPrepare: false,
		},
		{
			name:        "non-foreground process in tmux refuses quit",
			inMode:      "0",
			dead:        "0",
			screenText:  "\u203a Ask Codex to do anything\n  ? for shortcuts",
			fgOK:        false,
			wantPrepare: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			screenFile := filepath.Join(dir, "screen")
			if err := os.WriteFile(screenFile, []byte(tc.screenText+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "tmux")
			script := `#!/bin/sh
[ "$1" = "-S" ] && { echo "socket:$2" >> ` + calls + `; shift 2; }
case "$1" in
display-message) printf '%s\t/dev/ttys005\t%s\t%s\n' "` + strconv.Itoa(writer.PID) + `" "` + tc.dead + `" "` + tc.inMode + `" ;;
capture-pane) cat ` + screenFile + ` ;;
send-keys) echo "$@" >> ` + calls + ` ;;
esac
`
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			n := remoteTestNode(t)
			n.hosts = host.NewRegistry(&tmuxHost{c: tmux.Client{Bin: bin}})

			oldRead := readCodexWriterProcess
			oldFG := checkTmuxWriterForeground
			readCodexWriterProcess = func(pid int) (codexProcess, bool) {
				return codexProcess{pid: pid, tmuxPane: "%11", tmuxSocket: "/tmp/tmux-501/work"}, true
			}
			checkTmuxWriterForeground = func(int, int, string) bool { return tc.fgOK }
			t.Cleanup(func() {
				readCodexWriterProcess = oldRead
				checkTmuxWriterForeground = oldFG
			})

			send, err := n.prepareCodexWriterQuit(context.Background(), writer)
			if !tc.wantPrepare {
				if refusalCode(err) != "codex_takeover_unavailable" {
					t.Fatalf("prepare err = %v, want codex_takeover_unavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepare err = %v", err)
			}
			if err := send(); err != nil {
				t.Fatalf("send err = %v", err)
			}
			b, err := os.ReadFile(calls)
			if err != nil {
				t.Fatalf("calls file missing: %v", err)
			}
			got := string(b)
			if !strings.Contains(got, "socket:/tmp/tmux-501/work") || !strings.Contains(got, "send-keys -t %11 C-d") {
				t.Fatalf("tmux calls = %q, want socket and send-keys C-d", got)
			}
		})
	}

	t.Run("writer exiting during tmux capture-pane blocks ctrl+d", func(t *testing.T) {
		dir := t.TempDir()
		calls := filepath.Join(dir, "calls")
		screenFile := filepath.Join(dir, "screen")
		if err := os.WriteFile(screenFile, []byte("\u203a Ask Codex to do anything\n\n  gpt-6 low \u00b7 /work/example\n  ? for shortcuts\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "tmux")
		script := `#!/bin/sh
[ "$1" = "-S" ] && shift 2
case "$1" in
display-message) printf '%s\t/dev/ttys005\t0\t0\n' "` + strconv.Itoa(writer.PID) + `" ;;
capture-pane) cat ` + screenFile + ` ;;
send-keys) echo "$@" >> ` + calls + ` ;;
esac
`
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		n := remoteTestNode(t)
		n.hosts = host.NewRegistry(&tmuxHost{c: tmux.Client{Bin: bin}})
		oldRead := readCodexWriterProcess
		oldFG := checkTmuxWriterForeground
		readCodexWriterProcess = func(pid int) (codexProcess, bool) {
			return codexProcess{pid: pid, tmuxPane: "%11"}, true
		}
		// Prepare calls verifyOwner (#1 -> true), send calls pre-read verifyOwner (#2 -> true),
		// then post-read verifyOwner (#3 -> false, simulating exit during capture-pane).
		fgCalls := 0
		checkTmuxWriterForeground = func(int, int, string) bool {
			fgCalls++
			return fgCalls <= 2
		}
		t.Cleanup(func() {
			readCodexWriterProcess = oldRead
			checkTmuxWriterForeground = oldFG
		})
		send, err := n.prepareCodexWriterQuit(context.Background(), writer)
		if err != nil {
			t.Fatalf("prepare err = %v", err)
		}
		if err := send(); refusalCode(err) != "codex_takeover_changed" {
			t.Fatalf("send err = %v, want codex_takeover_changed (fgCalls=%d)", err, fgCalls)
		}
		if b, err := os.ReadFile(calls); err == nil && len(b) > 0 {
			t.Fatalf("send-keys ran despite exit during capture-pane: %q", b)
		}
	})
}

func TestCodexTakeoverSafeQuitHerdrHerdrCoreAndTernPanes(t *testing.T) {
	writer := codexWriter{os.Getpid(), procStartMark(os.Getpid())}
	if writer.Start == "" {
		t.Skip("procStartMark unavailable")
	}
	emptyComposer := "\u203a Ask Codex to do anything\n\n  gpt-6 low \u00b7 /work/example\n  ? for shortcuts"
	draftComposer := "\u203a Unsent user text\n  ? for shortcuts"

	for _, tc := range []struct {
		name string
		kind string
		proc codexProcess
	}{
		{"herdr", host.Herdr, codexProcess{pid: writer.PID, pane: "w1:p2"}},
		{"herdr-core", host.HerdrCore, codexProcess{pid: writer.PID, herdrCore: "hc-7"}},
		{"tern", host.Tern, codexProcess{pid: writer.PID, ternPane: "42"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fh := &fakeTakeoverLauncherHost{
				kind:     tc.kind,
				readText: emptyComposer,
				pids:     []int{writer.PID},
			}
			n := remoteTestNode(t)
			n.hosts = host.NewRegistry(fh)
			oldRead := readCodexWriterProcess
			readCodexWriterProcess = func(int) (codexProcess, bool) { return tc.proc, true }
			t.Cleanup(func() { readCodexWriterProcess = oldRead })

			send, err := n.prepareCodexWriterQuit(context.Background(), writer)
			if err != nil {
				t.Fatalf("prepare err = %v", err)
			}
			if err := send(); err != nil {
				t.Fatalf("send err = %v", err)
			}
			if len(fh.keysSent) != 1 || !slices.Equal(fh.keysSent[0], []string{"ctrl+d"}) {
				t.Fatalf("keysSent = %v, want [[ctrl+d]]", fh.keysSent)
			}
			wantSrc := "visible"
			if tc.kind == host.Herdr {
				wantSrc = "detection"
			}
			if len(fh.readSources) < 2 {
				t.Fatalf("readSources = %v, want >=2 reads", fh.readSources)
			}
			for _, src := range fh.readSources {
				if src != wantSrc {
					t.Fatalf("%s used read source %q, want %q", tc.kind, src, wantSrc)
				}
			}

			// Draft appearing before send refuses without pressing ctrl+d.
			fh.keysSent = nil
			send, err = n.prepareCodexWriterQuit(context.Background(), writer)
			if err != nil {
				t.Fatal(err)
			}
			fh.readText = draftComposer
			if err := send(); refusalCode(err) != "codex_takeover_unavailable" || len(fh.keysSent) != 0 {
				t.Fatalf("draft did not block ctrl+d: err=%v keys=%v", err, fh.keysSent)
			}

			// Process leaving pane before send refuses with changed without pressing ctrl+d.
			fh.readText = emptyComposer
			send, err = n.prepareCodexWriterQuit(context.Background(), writer)
			if err != nil {
				t.Fatal(err)
			}
			fh.pids = []int{writer.PID + 1}
			if err := send(); refusalCode(err) != "codex_takeover_changed" || len(fh.keysSent) != 0 {
				t.Fatalf("changed pane owner did not block ctrl+d: err=%v keys=%v", err, fh.keysSent)
			}

			// Process exiting DURING the screen-read RPC right before SendKeys must refuse without sending ctrl+d to a shell.
			fh.pids = []int{writer.PID}
			send, err = n.prepareCodexWriterQuit(context.Background(), writer)
			if err != nil {
				t.Fatal(err)
			}
			fh.onRead = func(host.ReadOpts) {
				fh.pids = []int{writer.PID + 99}
			}
			if err := send(); refusalCode(err) != "codex_takeover_changed" || len(fh.keysSent) != 0 {
				t.Fatalf("exit during screen read sent ctrl+d to shell: err=%v keys=%v", err, fh.keysSent)
			}
			fh.onRead = nil
		})
	}

	t.Run("herdr scrolled viewport cannot mask live draft in detection grid", func(t *testing.T) {
		fh := &fakeTakeoverLauncherHost{
			kind: host.Herdr,
			readBySrc: map[string]string{
				"visible":   emptyComposer,
				"detection": draftComposer,
			},
			pids: []int{writer.PID},
		}
		n := remoteTestNode(t)
		n.hosts = host.NewRegistry(fh)
		oldRead := readCodexWriterProcess
		readCodexWriterProcess = func(int) (codexProcess, bool) {
			return codexProcess{pid: writer.PID, pane: "w1:p2"}, true
		}
		t.Cleanup(func() { readCodexWriterProcess = oldRead })

		if _, err := n.prepareCodexWriterQuit(context.Background(), writer); refusalCode(err) != "codex_takeover_unavailable" {
			t.Fatalf("scrolled herdr viewport masked live draft: err=%v sources=%v", err, fh.readSources)
		}
	})
}

func TestCodexTakeoverExternalTerminalNeverSignalsAndOffersContinue(t *testing.T) {
	n, _, cwd := codexHistoryNode(t)
	path := rolloutPath(n.codexHome(), startThread)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Process holds rollout open from an ordinary external terminal (no tmux/herdr/herdr-core/tern/orca env).
	n.codices.scan = func() []codexProcess {
		return []codexProcess{{pid: os.Getpid(), home: n.codexHome()}}
	}
	n.codices.checked = func() ([]codexProcess, bool) { return n.codices.scan(), true }
	n.discoverCodex()

	oldRead := readCodexWriterProcess
	readCodexWriterProcess = func(pid int) (codexProcess, bool) {
		return codexProcess{pid: pid, home: n.codexHome()}, true
	}
	t.Cleanup(func() { readCodexWriterProcess = oldRead })

	s := byID(listSessions(t, n))[startThread]
	if s.Capabilities.Takeover == nil || !s.Capabilities.Takeover.Running || s.Capabilities.Takeover.CanResume || s.Capabilities.Takeover.Reason != "unavailable" {
		t.Fatalf("ordinary external terminal cap = %+v, want {Running:true CanResume:false Reason:unavailable}", s.Capabilities.Takeover)
	}
	plan, err := n.prepareCodexTakeover(context.Background(), startThread)
	if err != nil || !plan.Running || plan.CanResume || plan.Reason != "unavailable" || plan.ContinuationPrompt == "" {
		t.Fatalf("ordinary external terminal prepare = (%+v, %v)", plan, err)
	}
	if _, err := n.commitCodexTakeover(context.Background(), startThread, plan.Token, true); refusalCode(err) != "codex_takeover_continue_only" {
		t.Fatalf("commit on ordinary external terminal = %v, want codex_takeover_continue_only", err)
	}
	// Verify the current process is still alive (zero OS signals sent) and state stayed intact.
	if s.Status == proto.StatusOffline || cwd == "" {
		t.Fatalf("unexpected state: %+v", s)
	}
}
