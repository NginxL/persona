package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise the Windows policy on every CI platform, without reaching a real pane.
func TestWindowsHerdrPromptPastesEveryRuntimeAsOneMessage(t *testing.T) {
	for _, kind := range []string{"claude", "codex", piAgent, cursorAgent, agyAgent} {
		t.Run(kind, func(t *testing.T) {
			n, f, home := herdrNode(t)
			f.reply("agent.get", map[string]any{"agent": agentOf(kind, startThread, proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, "")})
			prompt := "方向 · 合成测试 · 说明\n\nContext body\nLast line: 中文 and tabs\tremain"
			h := n.defaultHost().(*herdrHost)
			if err := h.prompt(context.Background(), host.ParsePane("w1:p2"), prompt, true); err != nil {
				t.Fatal(err)
			}
			var writes []string
			for _, call := range f.calls() {
				switch call.Method {
				case "agent.prompt":
					t.Fatal("Windows delegated a multiline prompt to the host")
				case "pane.send_text":
					writes = append(writes, str(call.Params["text"]))
				case "pane.send_keys":
					if !slices.Equal(keysOf(call), []string{"enter"}) {
						t.Fatalf("unexpected keys: %v", keysOf(call))
					}
					writes = append(writes, "submit")
				}
			}
			if !slices.Equal(writes, []string{pasteStart + prompt + pasteEnd, "submit"}) {
				t.Fatalf("paste/submit sequence: %q", writes)
			}
		})
	}
}

func TestWindowsHerdrPromptDoesNotSubmitIntoADialog(t *testing.T) {
	for _, afterPaste := range []bool{false, true} {
		t.Run(map[bool]string{false: "already blocked", true: "blocked after paste"}[afterPaste], func(t *testing.T) {
			n, f, home := herdrNode(t)
			status := proto.StatusBlocked
			if afterPaste {
				status = proto.StatusIdle
			}
			f.reply("agent.get", map[string]any{"agent": agentOf("claude", startThread, status, "w1:p2", "w1:t2", "w1", home, "")})
			f.after(func(c herdrCall) {
				if c.Method == "pane.send_text" {
					f.reply("agent.get", map[string]any{"agent": agentOf("claude", startThread, proto.StatusBlocked, "w1:p2", "w1:t2", "w1", home, "")})
				}
			})
			err := n.defaultHost().(*herdrHost).prompt(context.Background(), host.ParsePane("w1:p2"), "title\nbody", true)
			if err == nil || len(f.trace("pane.send_keys")) != 0 {
				t.Fatalf("dialog received a submit: %v", err)
			}
			if errors.Is(err, host.ErrAgentBlocked) == afterPaste {
				t.Fatalf("delivery classification lost: %v", err)
			}
		})
	}
}

func TestCodexStartKeepsTheCompleteFirstMessageOutOfTerminalInput(t *testing.T) {
	n, f := stalledCodexNode(t)
	withCodexWaits(t)
	codexStartWait = 3 * time.Second
	cwd := filepath.Join(filepath.Dir(n.dir), "proj")
	home := filepath.Dir(cwd)
	prompt := "方向 · 合成测试 · 说明\n\nContext body 🧪\nLast line: 中文 and \"quotes\" $HOME"
	var initial string
	var mu sync.Mutex
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" {
			return
		}
		m := startFileRE.FindStringSubmatch(str(c.Params["text"]))
		if m == nil {
			return
		}
		var start codexStartFile
		b, err := os.ReadFile(m[1])
		if err != nil || json.Unmarshal(b, &start) != nil {
			t.Error("could not inspect the launch file")
			return
		}
		mu.Lock()
		initial = start.Prompt
		mu.Unlock()
		writeWaitRollout(t, home, cwd, startThread, time.Now(), start.Prompt)
	})
	session, err := n.startCodex(context.Background(), n.defaultHost().(host.Launcher), cwd, "codex", []string{"codex"}, modelExecution{}, prompt, "w1", "", "")
	if err != nil || session.SessionID != startThread {
		t.Fatalf("complete first message not matched: %+v %v", session, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if initial != prompt || len(f.trace("pane.send_text", "agent.prompt")) != 0 {
		t.Fatal("first message was cut or typed into the terminal")
	}
	for _, call := range f.calls() {
		if call.Method == "pane.send_input" && strings.Contains(str(call.Params["text"]), "Context body") {
			t.Fatal("first message leaked into the shell command")
		}
	}
}
