package node

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestFailedCodexStartArchivesOnlyItsFreshPaneThread(t *testing.T) {
	n, terminal := stalledCodexNode(t)
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess { return nil }
	n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	n.configureCodex(proto.CodexConfig{})
	home, cwd := n.codexHome(), filepath.Join(filepath.Dir(n.dir), "proj")
	id := n.registerCodex(home, startThread, "")
	other := "01a19a00-0000-7000-8000-00000000c0df"
	bus, remote := connectRemoteFixture(t, n, home, "app", startThread, other)
	waitFor(t, "failed launch thread owner", func() bool { _, _, err := n.codexOwner(id); return err == nil })
	remote.mu.Lock()
	thread := remote.threads[startThread]
	thread.turn = "orphan-turn"
	remote.threads[startThread] = thread
	remote.mu.Unlock()
	var closed atomic.Bool
	bus.mu.Lock()
	bus.response = func(method string, params map[string]any) (any, bool) {
		if method != "turn/interrupt" {
			return nil, false
		}
		if !closed.Load() || params["threadId"] != startThread || params["turnId"] != "orphan-turn" {
			t.Errorf("unsafe interrupt after close: %v", params)
		}
		bus.push(map[string]any{"method": "turn/completed", "params": map[string]any{
			"threadId": startThread, "turn": map[string]any{"id": "orphan-turn", "status": "interrupted"},
		}})
		return map[string]any{}, true
	}
	bus.archive = func(method, thread string) {
		if !closed.Load() || thread != startThread || method != "thread/archive" {
			t.Errorf("unsafe archive: closed=%v, %s %s", closed.Load(), method, thread)
		}
		moveCodexRollout(t, home, thread, true)
	}
	bus.mu.Unlock()
	terminal.reply("agent.get", map[string]any{"agent": agentOf("codex", startThread, proto.StatusBlocked, "w1:p2", "w1:t2", "w1", cwd, "")})
	terminal.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "codex-start") {
			at := time.Now()
			writeWaitRollout(t, filepath.Dir(n.dir), cwd, startThread, at, "方向 · 合成测试 · 说明")
			writeWaitRollout(t, filepath.Dir(n.dir), cwd, other, at, "方向 · 合成测试 · 说明")
		}
		if c.Method == "tab.close" || c.Method == "pane.close" {
			closed.Store(true)
		}
	})
	_, err := n.startCodex(context.Background(), n.defaultHost().(host.Launcher), cwd, "codex", []string{"codex"}, modelExecution{}, "方向 · 合成测试 · 说明\n\nThe rest of the task", "w1", "", "")
	if err == nil {
		t.Fatal("incomplete first message counted as a successful start")
	}
	if !closed.Load() || !archiveIn(t, home, startThread) || archiveIn(t, home, other) || bus.count("thread/archive") != 1 {
		t.Fatalf("orphan cleanup: closed=%v, archive calls=%d", closed.Load(), bus.count("thread/archive"))
	}
	if bus.count("turn/interrupt") != 1 {
		t.Fatal("the failed start's pending turn was not stopped before archive")
	}
	if n.stopCause(id).AllowsSupervisorResume() {
		t.Fatal("failed start granted supervisor resume")
	}
	for _, row := range n.codexSessions(map[string]bool{}) {
		if row.SessionID == id {
			t.Fatal("archived failed start is still advertised as an Agent")
		}
	}
}

func TestFailedCodexStartDoesNotArchiveAfterCloseFailure(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	path := writeArchiveRollout(t, home, startThread)
	id := n.registerCodex(home, startThread, path)
	bus, _ := connectRemoteFixture(t, n, home, "app", startThread)
	launch := &abandonedCodexLauncher{close: func(context.Context, host.PaneRef) error { return errors.New("close refused") }}
	start := abandonedCodexPane(launch, "w1:p2")
	start.session = id
	if err := n.closeFailedCodexStart(context.Background(), start); err == nil {
		t.Fatal("failed close reported success")
	}
	if bus.count("thread/archive") != 0 || archiveIn(t, home, startThread) {
		t.Fatal("archived a thread whose pane is still open")
	}
}

func TestFailedCodexIdentityRejectsAnOldOrOtherPanesThread(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: "another pane", true: "old thread"}[old], func(t *testing.T) {
			n, f := stalledCodexNode(t)
			began := time.Now()
			at := began
			if old {
				at = began.Add(-time.Hour)
			}
			cwd := filepath.Join(filepath.Dir(n.dir), "proj")
			writeWaitRollout(t, filepath.Dir(n.dir), cwd, startThread, at, "方向 · 合成测试 · 说明")
			f.reply("agent.get", map[string]any{"agent": agentOf("codex", startThread, proto.StatusBlocked, "w1:p2", "w1:t2", "w1", cwd, "")})
			if !old {
				n.bindPane("w1:p9", startThread)
			}
			if got := n.failedCodexSession(context.Background(), "w1:p2", began); got != "" {
				t.Fatalf("unsafe orphan identity: %s", got)
			}
		})
	}
}
