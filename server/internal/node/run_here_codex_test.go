package node

import (
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

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Run Agent Here with a Codex launcher (#5504): Codex starts in the shell pane the user picked, with no first message
// (codex_waiting.go). The pane is the user's own: a start that fails leaves it open and queues nothing to close later,
// and one that comes up answers with the waiting row of that pane, whose first message becomes its thread, with the
// user's shell history above Codex on the screen.

// codexHereHistory is what the user's shell showed before Codex: commands and their output, the prompt last.
const codexHereHistory = "$ ls\nnotes.txt  src\n$ git status\nOn branch main\nnothing to commit, working tree clean\n$ "

// codexHereNode is actionNode with a Codex launcher, the shell pane w1:p2 at its prompt under the user's history.
func codexHereNode(t *testing.T) (*Node, *fakeHerdr, string) {
	t.Helper()
	withCodexWaits(t)
	withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
	n, f, cwd := actionNode(t)
	f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": []any{
		map[string]any{"pid": 1, "name": "zsh", "argv0": "-zsh"}}}})
	f.setScreen("w1:p2", codexHereHistory)
	return n, f, cwd
}

// codexHereLayout is actionNode's layout once Codex runs in w1:p2, under the title the user's shell gave the pane
// (with " | " in it, as a Codex title has: it names no thread).
func codexHereLayout(cwd string) map[string]any {
	shell := func(pane, tab string) map[string]any {
		return map[string]any{"pane_id": pane, "tab_id": tab, "workspace_id": "w1", "cwd": cwd, "terminal_title": "zsh", "terminal_title_stripped": "zsh"}
	}
	agent := claudeAgent(actionSession, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "fix it")
	codex := agentOf("codex", "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", cwd, "zsh | ~/proj")
	return snapshot(
		[]any{map[string]any{"workspace_id": "w1", "label": "proj", "number": 1, "cwd": cwd, "branch": "main"}},
		[]any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "label": "agent-1", "number": 1},
			map[string]any{"tab_id": "w1:t2", "workspace_id": "w1", "label": "shell", "number": 2},
			map[string]any{"tab_id": "w1:t3", "workspace_id": "w1", "label": "split", "number": 3}},
		[]any{agent, codex},
		[]any{agent, codex, shell("w1:p3", "w1:t3"), shell("w1:p4", "w1:t3")},
	)
}

// startFileOf is the start file a typed codex-start line names, read before the start removes it.
func startFileOf(t *testing.T, line string) (codexStartFile, bool) {
	m := startFileRE.FindStringSubmatch(line)
	if m == nil {
		return codexStartFile{}, false
	}
	var file codexStartFile
	b, err := os.ReadFile(m[1])
	if err != nil || json.Unmarshal(b, &file) != nil {
		return codexStartFile{}, false
	}
	return file, true
}

func TestRunCodexInAShellPane(t *testing.T) {
	n, f, cwd := codexHereNode(t)
	home := filepath.Dir(cwd)
	var mu sync.Mutex
	var started *codexStartFile
	f.after(func(c herdrCall) {
		if str(c.Params["pane_id"]) != "w1:p2" {
			return
		}
		switch {
		case c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "codex-start"):
			if file, ok := startFileOf(t, str(c.Params["text"])); ok {
				mu.Lock()
				started = &file
				mu.Unlock()
			}
			// Codex draws inline, under what the shell showed.
			f.setScreen("w1:p2", codexHereHistory+"codex\n"+cxReady)
			f.reply("session.snapshot", codexHereLayout(cwd))
		case c.Method == "pane.send_text":
			f.setScreen("w1:p2", codexHereHistory+fmt.Sprintf("codex\n› [Pasted Content %d chars]\n\n  gpt-6 xhigh · /tmp/proj\n", len([]rune(specialPrompt))))
		case c.Method == "pane.send_keys" && slices.Equal(keysOf(c), []string{"enter"}):
			f.setScreen("w1:p2", codexHereHistory+"codex\n› fix\n\n• Working (0s • esc to interrupt)\n\n› Ask Codex to do anything\n")
			writeWaitRollout(t, home, cwd, startThread, time.Now(), specialPrompt)
		}
	})

	res, err := request(t, n, "panes.agent", map[string]any{"pane": "w1:p2", "launcher": "codex"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res["session_id"].(string) // what the app opens: the waiting row
	if !isCodexWaitID(id) {
		t.Fatalf("answered %v", res)
	}
	mu.Lock()
	if started == nil || started.Prompt != "" || strings.Join(started.Argv, " ") != "codex" || started.Dir != cwd {
		t.Errorf("start file %+v: the launcher alone, in the pane's directory, no message", started)
	}
	mu.Unlock()
	if got := f.trace("tab.create", "workspace.create", "pane.close", "tab.close"); len(got) != 0 {
		t.Errorf("a tab was opened or closed: %v", got)
	}

	// The pane lists as that waiting row, not as a thread its title names.
	var rows []proto.Session
	for _, x := range listSessions(t, n) {
		if x.PaneID == "w1:p2" {
			rows = append(rows, x)
		}
	}
	if len(rows) != 1 || rows[0].SessionID != id || !rows[0].AwaitingFirstMessage || rows[0].Agent != "codex" || rows[0].Cwd != cwd ||
		rows[0].Capabilities == nil || !rows[0].Capabilities.Send {
		t.Fatalf("rows for the pane %+v", rows)
	}

	// The first message from the app goes into this Codex and the row becomes the thread it began.
	got, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": specialPrompt, "client_msg_id": "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if th := decodeSessions(t, []any{got["session"]})[0]; th.SessionID != startThread || th.PaneID != "w1:p2" {
		t.Fatalf("the first message answered %+v", th)
	}
	if st, ok := n.state.get(startThread); !ok || st.Pane != "w1:p2" || st.Cwd != cwd {
		t.Errorf("thread's record %+v", st)
	}
	if got := f.trace("pane.close", "tab.close"); len(got) != 0 {
		t.Errorf("the pane was closed: %v", got)
	}
}

// A Codex start in the user's pane that fails leaves the pane as it is: not closed, nothing queued to close before the
// next start in that directory, and no key pressed in it after the start line.
func TestRunCodexInAShellPaneKeepsItWhenTheStartFails(t *testing.T) {
	for _, tc := range []struct{ name, screen, want string }{
		{"the launcher fails", codexHereHistory + "codex\n" + codexStartError + " codex: command not found\n$ ", "command not found"},
		{"no input box", codexHereHistory + "codex\n", "codex_no_box_here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, _ := codexHereNode(t)
			old := codexStartWait
			codexStartWait = 300 * time.Millisecond
			t.Cleanup(func() { codexStartWait = old })
			f.after(func(c herdrCall) {
				if c.Method == "pane.send_input" && str(c.Params["pane_id"]) == "w1:p2" && strings.Contains(str(c.Params["text"]), "codex-start") {
					f.setScreen("w1:p2", tc.screen)
				}
			})
			_, err := request(t, n, "panes.agent", map[string]any{"pane": "w1:p2", "launcher": "codex"})
			var coded *rpc.Error
			if err == nil || (!(errors.As(err, &coded) && coded.Code == tc.want) && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("a failed start answered %v", err)
			}
			if got := f.trace("pane.close", "tab.close", "tab.create", "workspace.create"); len(got) != 0 {
				t.Errorf("the user's pane was closed or a tab opened: %v", got)
			}
			if keys := f.trace("pane.send_keys"); len(keys) != 0 {
				t.Errorf("keys pressed in the user's shell: %v", keys)
			}
			n.mu.Lock()
			pending := len(n.codexAbandoned)
			n.mu.Unlock()
			if pending != 0 {
				t.Errorf("the user's pane is queued to be closed: %v", n.codexAbandoned)
			}
			if inputs := f.trace("pane.send_input"); len(inputs) != 1 {
				t.Errorf("typed %d lines, not the start line alone: %v", len(inputs), inputs)
			}
		})
	}
}

// Codex launchers say the node starts them in a pane the user picked; Claude Code's need no word for it.
func TestCodexLaunchersRunHere(t *testing.T) {
	list, _, _, _ := mergeLaunchers(launcherBegin + "\x1d\x1d/usr/local/bin/codex\n" + launcherEnd) // plain codex on the PATH
	aliases, _ := parseCodexAliases("cx\x1fcodex\x1f-m\x1fgpt-6\x1f")                               // a ~/.zshrc alias running codex
	list = append(list, aliases...)
	var codex int
	for _, l := range list {
		if l.Agent == "codex" {
			codex++
			if !l.RunHere || !l.PromptOptional {
				t.Errorf("%s: run_here %v, prompt_optional %v", l.Name, l.RunHere, l.PromptOptional)
			}
		} else if l.RunHere {
			t.Errorf("%s (%s) says run_here", l.Name, l.Agent)
		}
	}
	if codex != 2 {
		t.Fatalf("Codex launchers listed: %+v", list)
	}
}
