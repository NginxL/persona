package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentDirectoryGuardResolvesAliases(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "custom-state")
	assistant := filepath.Join(state, "assistant")
	if err := os.MkdirAll(assistant, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "project-alias")
	if err := os.Symlink(assistant, alias); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(assistant, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	childAlias := filepath.Join(home, "child-alias")
	if err := os.Symlink(child, childAlias); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: home, dir: state}
	// Resolve symlinks before lexical cleaning: alias/.. names the target's parent.
	for _, cwd := range []string{assistant, alias, "~/.persona/assistant", "~/" + compat.StateDir + "/assistant", assistant + "/../assistant", childAlias + "/.."} {
		if err := n.guardAgentDirectory(context.Background(), cwd); rpcCode(err) != "agent_assistant_workspace" {
			t.Errorf("%s: expected workspace refusal, got %v", cwd, err)
		}
	}
	for _, cwd := range []string{home, filepath.Join(home, "project", "assistant"), assistant + "-project"} {
		if err := n.guardAgentDirectory(context.Background(), cwd); err != nil {
			t.Errorf("project %s: %v", cwd, err)
		}
	}
}

func TestSessionStartRefusesAssistantAliasBeforeLaunching(t *testing.T) {
	n, _, home := herdrNode(t)
	assistant := filepath.Join(n.dir, "assistant")
	if err := os.MkdirAll(assistant, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(assistant, alias); err != nil {
		t.Fatal(err)
	}
	_, err := request(t, n, "session.start", map[string]any{"cwd": alias, "prompt": "Inspect the source"})
	if rpcCode(err) != "agent_assistant_workspace" {
		t.Fatalf("got %v", err)
	}
}

func TestAgentStartRules(t *testing.T) {
	const sent = "Keep the task scope."
	n := &Node{dir: t.TempDir()}
	// Persona adds nothing of its own (#13642): no file, an empty one, a node without a data directory.
	if got := n.agentStartRules(sent); got != sent {
		t.Errorf("without agent-rules.md: %q", got)
	}
	if got := (&Node{}).agentStartRules(sent); got != sent {
		t.Errorf("without a data directory: %q", got)
	}
	path := filepath.Join(n.dir, agentRulesFile)
	if err := os.WriteFile(path, []byte(" \n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := n.agentStartRules(sent); got != sent {
		t.Errorf("empty agent-rules.md: %q", got)
	}
	if err := os.WriteFile(path, []byte("\nRun the tests before you report.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := n.agentStartRules(sent); got != sent+"\n\nRun the tests before you report." {
		t.Errorf("with agent-rules.md: %q", got)
	}
	if got := n.agentStartRules(""); got != "Run the tests before you report." {
		t.Errorf("nothing sent, with agent-rules.md: %q", got)
	}
}

// A new session.start carries what its caller sent and nothing else unless the computer has agent-rules.md, whose text
// it adds once; a resume adds none (#13393, #13642). On every platform: no rule is built in for any.
func TestAgentRulesOnStartNotResume(t *testing.T) {
	const own = "Run builds on the build machine."
	start := func(t *testing.T, rules bool) sessionState {
		shellHome := fakeCLIHome(t)
		if err := os.WriteFile(filepath.Join(shellHome, ".zshrc"), []byte("forward() { claude \"$@\"; }\n"), 0600); err != nil {
			t.Fatal(err)
		}
		n, f, home := herdrNode(t)
		if rules {
			if err := os.WriteFile(filepath.Join(n.dir, agentRulesFile), []byte(own+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cwd := mkdir(t, filepath.Join(home, "proj"))
		f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
		startHerdr(f, "w1:p2", "idle")
		f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
		f.screenSteps("w1:p2", isKeys(), screenFixture(t, "claude-2.1.312-empty-162.txt"))
		promptLands(t, f, home, cwd)
		_, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": cwd, "launcher": "forward", "prompt": "fix the build", "task_instructions": "Keep this assignment's scope."})
		if err != nil {
			t.Fatal(err)
		}
		n.firstWait.Wait()
		st, _ := n.state.get(startID)
		// The Claude channel's file, where this start took it, carries the same text (the channel's own tests cover it).
		if st.TaskInstructionFile != "" {
			if raw, err := os.ReadFile(st.TaskInstructionFile); err != nil || string(raw) != st.TaskInstructions {
				t.Fatalf("instruction file=%q %v", raw, err)
			}
		}
		return st
	}

	t.Run("start", func(t *testing.T) {
		if st := start(t, false); st.TaskInstructions != "Keep this assignment's scope." {
			t.Fatalf("task instructions=%q", st.TaskInstructions)
		}
	})

	t.Run("start with agent-rules.md", func(t *testing.T) {
		if st := start(t, true); st.TaskInstructions != "Keep this assignment's scope.\n\n"+own || strings.Count(st.TaskInstructions, own) != 1 {
			t.Fatalf("task instructions=%q", st.TaskInstructions)
		}
	})

	t.Run("resume", func(t *testing.T) {
		n, f, home := herdrNode(t)
		if err := os.WriteFile(filepath.Join(n.dir, agentRulesFile), []byte(own), 0600); err != nil {
			t.Fatal(err)
		}
		n.starts.add(startID, &startRec{done: make(chan struct{}), session: proto.Session{SessionID: startID, Starting: true}})
		n.starts.finish(startID, errors.New("claude did not become ready"))
		cwd := mkdir(t, filepath.Join(home, "fresh"))
		f.reply("workspace.list", map[string]any{"workspaces": []any{}})
		f.reply("workspace.create", map[string]any{"workspace": map[string]any{"workspace_id": "w7"},
			"tab": map[string]any{"tab_id": "w7:t1"}, "root_pane": map[string]any{"pane_id": "w7:p1"}})
		startHerdr(f, "w7:p1", proto.StatusDone)
		f.setScreen("w7:p1", screenFixture(t, "claude-2.1.312-empty-162.txt"))
		if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "claude", "resume_session_id": startID}); err != nil {
			t.Fatal(err)
		}
		if err := n.starts.wait(t.Context(), startID); err != nil {
			t.Fatal(err)
		}
		if st, _ := n.state.get(startID); st.TaskInstructions != "" || st.TaskInstructionFile != "" {
			t.Fatalf("resume state=%+v", st)
		}
		want := call("pane.send_input", map[string]any{"pane_id": "w7:p1", "keys": []string{"Enter"},
			"text": startCmd("claude", "--session-id "+startID, personaDir(home))})
		if trace := f.trace(); !slices.Contains(trace, want) {
			t.Fatalf("no plain --session-id start in %v", trace)
		}
	})
}

// A track's directory is the assistant's (#13472): an Agent is not dispatched into it under any spelling of its path,
// a link or (on a disk that ignores case) another case; a project beside it is not refused.
func TestAgentDirectoryGuardRefusesTrackDirs(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "custom-state")
	track := filepath.Join(state, "tracks", "qa-plain")
	if err := os.MkdirAll(track, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "track-alias")
	if err := os.Symlink(track, alias); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: home, dir: state}
	cwds := []string{track, alias}
	if _, err := os.Stat(filepath.Join(state, "TRACKS", "qa-plain")); err == nil {
		cwds = append(cwds, filepath.Join(state, "TRACKS", "qa-plain")) // a disk that ignores case
	}
	for _, cwd := range cwds {
		if err := n.guardAgentDirectory(context.Background(), cwd); rpcCode(err) != "agent_assistant_workspace" {
			t.Errorf("%s: expected workspace refusal, got %v", cwd, err)
		}
	}
	project := filepath.Join(home, "code", "qa-plain")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := n.guardAgentDirectory(context.Background(), project); err != nil {
		t.Errorf("a project: %v", err)
	}
}
