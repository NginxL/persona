package node

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// withGoalVerdictWait shortens how long a start waits for /goal's answer on the screen.
func withGoalVerdictWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := goalVerdictWait
	goalVerdictWait = d
	t.Cleanup(func() { goalVerdictWait = old })
}

// goalAnswers makes the pane show Claude Code's answer to a /goal typed at it (verdict, e.g. "Goal set: …"), and
// writes every other prompt to the transcript as delivered.
func goalAnswers(t *testing.T, f *fakeHerdr, home, cwd, plain, ansi, verdict string) {
	t.Helper()
	f.after(func(c herdrCall) {
		if c.Method != "agent.prompt" {
			return
		}
		text := str(c.Params["text"])
		switch {
		case strings.HasPrefix(text, "/goal "):
			if verdict != "" {
				f.setScreens("w1:p2", plain+"\n❯ "+text+"\n  ⎿  "+verdict+"\n", ansi)
			}
		case text == ultracodeCommand(true):
			f.setScreens("w1:p2", plain+"\n❯ "+text+"\n  ⎿ Ultracode on (this session only): dynamic workflows on every task. Effort stays max.\n", ansi)
		default:
			writeUserLine(t, home, cwd, startedID(f), text)
		}
	})
}

// A start with a goal types the first message, and once the transcript has it, /goal on its own: Claude Code's /goal
// starts a turn toward its condition at once, so typed first it would set the agent to work from the criteria alone,
// and typed in the same message it would take the task for its condition.
func TestStartGoalAfterFirstPrompt(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	startPane(t, f, plain, ansi)
	goal := "go test ./... passes; the fix is merged"
	goalAnswers(t, f, home, cwd, plain, ansi, "Goal set: "+goal)

	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "fix the build", "goal": goal}); err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	got := f.trace("agent.prompt")
	if len(got) != 2 || !strings.Contains(got[0], "fix the build") || !strings.Contains(got[1], `"/goal `+goal+`"`) {
		t.Fatalf("wrong start order: %v", got)
	}
	if _, lost := n.lostFirst.get(startedID(f)); lost {
		t.Error("the first message was marked as lost")
	}
}

// With ultracode too: ultracode, then the first message, then the goal, one command at a time.
func TestStartGoalAfterUltracodeAndFirstPrompt(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	startPane(t, f, plain, ansi)
	goal := "the report is in docs/report.md"
	goalAnswers(t, f, home, cwd, plain, ansi, "Goal set: "+goal)

	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "write the report",
		"goal": goal, "ultracode": true, "effort": "max"}); err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	got := f.trace("agent.prompt")
	if len(got) != 3 || !strings.Contains(got[0], "/effort ultracode on") || !strings.Contains(got[1], "write the report") ||
		!strings.Contains(got[2], "/goal "+goal) {
		t.Fatalf("wrong start order: %v", got)
	}
}

// A goal Claude Code refuses (here: hooks are off) changes nothing else: the first message went in before it, and is
// not lost or typed again.
func TestStartGoalRefusedByClaudeLeavesTheStartAlone(t *testing.T) {
	withGoalVerdictWait(t, 2*time.Second)
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	startPane(t, f, plain, ansi)
	goalAnswers(t, f, home, cwd, plain, ansi,
		"/goal can't run while hooks are restricted (disableAllHooks or allowManagedHooksOnly is set in settings or by policy).")

	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "fix the build", "goal": "CI is green"}); err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	if got := f.trace("agent.prompt"); len(got) != 2 || !strings.Contains(got[0], "fix the build") || !strings.Contains(got[1], "/goal ") {
		t.Fatalf("typed %v", got)
	}
	if _, lost := n.lostFirst.get(startedID(f)); lost {
		t.Error("the first message was marked as lost")
	}
}

// A first message the transcript never shows gets no goal: the goal is about that task, and typing it at an agent
// that never got the task would set it to work from the criteria alone.
func TestStartGoalNotTypedWhenTheFirstMessageIsLost(t *testing.T) {
	withFirstPromptWaits(t, 50*time.Millisecond, 50*time.Millisecond)
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	startPane(t, f, screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	// Nothing reaches the transcript.
	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "fix the build", "goal": "CI is green"}); err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	for _, c := range f.trace("agent.prompt") {
		if strings.Contains(c, "/goal") {
			t.Fatalf("the goal was typed without its task: %v", f.trace("agent.prompt"))
		}
	}
	if _, lost := n.lostFirst.get(startedID(f)); !lost {
		t.Fatal("the lost first message is not reported")
	}
}

// What Claude Code answers to /goal, read off the screen: only what is new since the command was typed counts.
func TestGoalOutcome(t *testing.T) {
	before := "╭────╮\n│ > │\n╰────╯\n"
	for _, tc := range []struct {
		name, shown string
		set         bool
		note        string
	}{
		{"set", before + "❯ /goal tests pass\n  ⎿  Goal set: tests pass\n", true, ""},
		{"wrapped", before + "❯ /goal the whole\n  suite passes\n  ⎿  Goal\n  set: the whole suite passes\n", true, ""},
		{"untrusted", before + "  ⎿  /goal is only available in trusted workspaces. Restart, accept the trust dialog, and try again.\n", false, "trusted"},
		{"hooks", before + "  ⎿  /goal can't run while hooks are restricted (disableAllHooks or allowManagedHooksOnly is set in settings or by policy).\n", false, "hooks"},
		{"policy", before + "  ⎿  /goal can't run yet: your organization's settings haven't been read, so it isn't known whether it allows hooks.\n", false, "organization"},
		{"long", before + "  ⎿  Goal condition is limited to 4000 characters (got 4100)\n", false, "too long"},
		{"empty", before + "  ⎿  The goal condition is empty once whitespace and invisible characters are removed. Provide a visible condition.\n", false, "empty"},
		{"old", before + "  ⎿  Unknown command: /goal\n", false, "no /goal"},
		{"nothing yet", before + "❯ /goal tests pass\n", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, note := goalOutcome(before, tc.shown)
			if set != tc.set || tc.note == "" && note != "" || tc.note != "" && !strings.Contains(note, tc.note) {
				t.Fatalf("set=%v note=%q", set, note)
			}
		})
	}
	// An earlier goal's line on the screen is not this one's answer.
	old := before + "  ⎿  Goal set: an earlier goal\n"
	if set, _ := goalOutcome(old, old+"❯ /goal tests pass\n"); set {
		t.Fatal("an earlier goal's line counted")
	}
}

// A start whose goal cannot be had is refused before anything starts, with the code the caller starts again on
// without the goal.
func TestStartGoalRefusedBeforeAnythingStarts(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	for name, params := range map[string]map[string]any{
		"traex":  {"cwd": cwd, "workspace_id": "w1", "prompt": "x", "launcher": "traex", "goal": "done"},
		"resume": {"cwd": cwd, "workspace_id": "w1", "prompt": "x", "resume_session_id": sidA, "goal": "done"},
		"long":   {"cwd": cwd, "workspace_id": "w1", "prompt": "x", "goal": strings.Repeat("y", proto.StartGoalMax+1)},
		// The goal is set after the first message; an App's async start that sends its first message later has none.
		"no prompt": {"cwd": cwd, "workspace_id": "w1", "async": true, "goal": "done"},
	} {
		_, err := request(t, n, "session.start", params)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != proto.CodeStartGoalUnsupported {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := f.trace("tab.create", "agent.prompt"); len(got) != 0 {
		t.Fatalf("started anyway: %v", got)
	}
}

// A start in a host that cannot read the screen or press keys (botmux) takes no goal, even when the node's default
// host can: botmux may hand /goal to the agent as a plain message.
func TestStartGoalRefusedInAScreenlessHost(t *testing.T) {
	n, f, home := herdrNode(t)
	n.hosts.Set(append(n.hosts.All(), &botmuxHost{})...)
	cwd := mkdir(t, filepath.Join(home, "project"))
	_, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "x", "launcher": "codex",
		"host": "botmux", "goal": "done"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeStartGoalUnsupported {
		t.Fatalf("botmux: %v", err)
	}
	if got := f.trace("tab.create", "agent.prompt"); len(got) != 0 {
		t.Fatalf("started anyway: %v", got)
	}
}

func TestCleanStartGoal(t *testing.T) {
	if got := cleanStartGoal("  tests pass\n and\tthe PR is merged  "); got != "tests pass and the PR is merged" {
		t.Fatalf("%q", got)
	}
	if got := cleanStartGoal(" \n "); got != "" {
		t.Fatalf("%q", got)
	}
}

// Before typing /goal, the node looks at the terminal the way the ultracode switch does: a dialog keeps the command
// out, and an unsent draft in the box is never answered or sent.
func TestStartGoalRefusesWhenTheTerminalIsNotReady(t *testing.T) {
	n, f, _ := herdrNode(t)
	plain, ansi := screenFixture(t, "claude-2.1.312-typed-162.txt"), screenFixture(t, "claude-2.1.312-typed-162.ansi")
	startPane(t, f, plain, ansi)
	// A typed draft in the box: checkPane refuses it, so no command is typed.
	n.startGoal(context.Background(), "id", "w1:p2", "CI is green")
	if c := f.trace("agent.prompt"); len(c) != 0 {
		t.Fatalf("the goal was typed over a draft: %v", c)
	}
}

// An async start (the shape every App start uses) sets the goal too: still after the first message is in, not before.
func TestStartGoalOnAnAsyncStart(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	startPane(t, f, plain, ansi)
	goal := "async start: the report is in docs/report.md"
	goalAnswers(t, f, home, cwd, plain, ansi, "Goal set: "+goal)

	res, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "async": true,
		"prompt": "write the report", "goal": goal})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := res["session"].(map[string]any)
	if session == nil || session["starting"] != true {
		t.Fatalf("an async start should answer starting in its session: %v", res)
	}
	waitFor(t, "the async message and goal", func() bool {
		return len(f.trace("agent.prompt")) >= 2
	})
	n.firstWait.Wait()
	got := f.trace("agent.prompt")
	if len(got) != 2 || !strings.Contains(got[0], "write the report") || !strings.Contains(got[1], "/goal "+goal) {
		t.Fatalf("async start order: %v", got)
	}
}

// A start whose Claude comes up blocked on its own dialog: the first message is held, then typed once the dialog is
// answered, and only then is the goal set.
func TestStartGoalAfterADialogHeldFirstMessage(t *testing.T) {
	withFirstAfterDialog(t, 400*time.Millisecond, 20*time.Millisecond)
	n, f, home := herdrNode(t)
	n.claudeStart, n.claudeReady = 500*time.Millisecond, 500*time.Millisecond
	cwd := mkdir(t, filepath.Join(home, "project"))
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	blockedPlain, blockedAnsi := screenFixture(t, "claude-2.1.312-model-picker-162.txt"), screenFixture(t, "claude-2.1.312-model-picker-162.ansi")
	startPane(t, f, blockedPlain, blockedAnsi)
	// waitReady returns at once on a dialog: the pane's agent is blocked, and the first message is held for it.
	f.reply("agent.get", map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": proto.StatusBlocked,
		"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1"}})
	goal := "dialog start: CI is green"
	goalAnswers(t, f, home, cwd, plain, ansi, "Goal set: "+goal)

	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1",
		"prompt": "fix the build", "goal": goal}); err != nil {
		t.Fatal(err)
	}
	// Held while the dialog is up: nothing has been typed and the session asks for the user.
	waitFor(t, "the held first message", func() bool {
		if l, ok := n.lostFirst.get(startedID(f)); ok && l.why == lostDialogHeld {
			return true
		}
		return false
	})
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Fatalf("text was typed into the dialog: %v", got)
	}
	// The dialog is answered: the pane's agent is idle again and the box is free; the held first message goes in and
	// then the goal follows it.
	f.reply("agent.get", map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": proto.StatusIdle,
		"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1"}})
	f.setScreens("w1:p2", plain, ansi)
	waitFor(t, "the message and the goal", func() bool {
		got := f.trace("agent.prompt")
		return len(got) == 2 && strings.Contains(got[0], "fix the build") && strings.Contains(got[1], "/goal "+goal)
	})
	// The held mark is cleared once the first message's own check confirms it; the goal waits behind that check.
	waitFor(t, "the held mark to clear", func() bool {
		lost, ok := n.lostFirst.get(startedID(f))
		return !ok || lost.why == ""
	})
	if lost, ok := n.lostFirst.get(startedID(f)); ok && lost.why != "" {
		t.Fatalf("the held mark was not cleared: %+v", lost)
	}
}
