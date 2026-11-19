package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestWindowsCodexStartPastesTheCompleteFirstMessage(t *testing.T) {
	n, f := stalledCodexNode(t)
	withCodexWaits(t)
	oldPaste := codexStartPaste
	codexStartPaste, codexStartWait = true, 3*time.Second
	t.Cleanup(func() { codexStartPaste = oldPaste })
	cwd := filepath.Join(filepath.Dir(n.dir), "proj")
	home := filepath.Dir(cwd)
	prompt := "方向 · 合成测试 · 说明\n\nContext body\nLast line: 中文"
	f.setScreen("w1:p2", cxReady)
	var initial, pasted string
	var mu sync.Mutex
	f.after(func(c herdrCall) {
		mu.Lock()
		defer mu.Unlock()
		switch c.Method {
		case "pane.send_input":
			m := startFileRE.FindStringSubmatch(str(c.Params["text"]))
			if m != nil {
				var start codexStartFile
				b, err := os.ReadFile(m[1])
				if err != nil || json.Unmarshal(b, &start) != nil {
					t.Error("could not inspect the launch file")
				}
				initial = start.Prompt
			}
		case "pane.send_text":
			pasted = str(c.Params["text"])
			f.setScreen("w1:p2", fmt.Sprintf("› [Pasted Content %d chars]\n\n  gpt-6 xhigh · /tmp/proj\n", len([]rune(prompt))))
		case "pane.send_keys":
			if slices.Equal(keysOf(c), []string{"enter"}) {
				if pasted != pasteStart+prompt+pasteEnd {
					t.Error("submitted before the complete message was pasted")
				}
				writeWaitRollout(t, home, cwd, startThread, time.Now(), strings.TrimSuffix(strings.TrimPrefix(pasted, pasteStart), pasteEnd))
				f.setScreen("w1:p2", cxReady)
			}
		}
	})
	s, err := n.startCodex(context.Background(), n.defaultHost().(host.Launcher), cwd, "codex", []string{"codex"}, modelExecution{}, prompt, "w1", "", "")
	if err != nil || s.SessionID != startThread {
		t.Fatalf("start did not match the complete prompt: %+v %v", s, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if initial != "" || pasted != pasteStart+prompt+pasteEnd || len(f.trace("pane.send_keys")) != 1 {
		t.Fatalf("initial=%q, pasted=%q, submits=%d", initial, pasted, len(f.trace("pane.send_keys")))
	}
}
