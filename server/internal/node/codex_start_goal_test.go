package node

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func withCodexGoalWait(t *testing.T, d time.Duration) {
	t.Helper()
	old, oldConfirm := codexGoalWait, codexConfirm
	codexGoalWait, codexConfirm = d, d
	t.Cleanup(func() { codexGoalWait, codexConfirm = old, oldConfirm })
}

func withStartRolloutVersion(t *testing.T, v string) {
	t.Helper()
	old := startRolloutVersion
	startRolloutVersion = v
	t.Cleanup(func() { startRolloutVersion = old })
}

// codexGoalAnswers makes the Codex in w1:p2 take a /goal typed at it as Codex 0.169 does: thread_goal_updated, active,
// in its rollout (when take), with the objective keep makes of the typed one (nil: the same).
func codexGoalAnswers(t *testing.T, n *Node, f *fakeHerdr, take bool, keep func(string) string) {
	t.Helper()
	f.after(func(c herdrCall) {
		text := str(c.Params["text"])
		if c.Method != "agent.prompt" || !strings.HasPrefix(text, "/goal ") || !take {
			return
		}
		objective := strings.TrimPrefix(text, "/goal ")
		if keep != nil {
			objective = keep(objective)
		}
		line, _ := json.Marshal(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": "event_msg",
			"payload": map[string]any{"type": "thread_goal_updated", "threadId": startThread, "goal": map[string]any{
				"threadId": startThread, "objective": objective, "status": "active",
				"tokensUsed": 0, "timeUsedSeconds": 0, "createdAt": time.Now().Unix(), "updatedAt": time.Now().Unix()}}})
		file, err := os.OpenFile(n.codexPath(startThread), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = file.Write(append(line, '\n'))
		_ = file.Close()
	})
}

// A new Codex start with a goal types Codex's /goal once, after the start found the thread its first message began, and
// the rollout then has the goal, active.
func TestCodexStartSetsTheGoal(t *testing.T) {
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	f.setScreen("w1:p2", cxEmpty)
	codexGoalAnswers(t, n, f, true, nil)
	goal := "go test ./... passes; the fix is merged"

	res, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "fix the build", "goal": goal})
	if err != nil {
		t.Fatal(err)
	}
	if s := decodeSessions(t, []any{res["session"]})[0]; s.SessionID != startThread {
		t.Fatalf("answered %+v", s)
	}
	n.firstWait.Wait()
	got := f.trace("agent.prompt")
	if len(got) != 1 || !strings.Contains(got[0], `"/goal `+goal+`"`) {
		t.Fatalf("typed %v", got)
	}
	if _, _, g, _ := n.backgroundTasks(n.codexPath(startThread)); g == nil || g.Objective != goal || g.Status != proto.GoalActive {
		t.Fatalf("goal %+v", g)
	}
}

// A goal that never shows in the rollout, or a Codex too old for /goal, leaves the start as it is.
func TestCodexStartGoalNeverFailsTheStart(t *testing.T) {
	withCodexGoalWait(t, 300*time.Millisecond)
	for name, tc := range map[string]struct {
		version string
		take    bool
		typed   int
	}{
		"not taken": {"0.169.0", false, 1},
		"old codex": {"0.168.2", true, 0},
	} {
		t.Run(name, func(t *testing.T) {
			withStartRolloutVersion(t, tc.version)
			n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
			f.setScreen("w1:p2", cxEmpty)
			codexGoalAnswers(t, n, f, tc.take, nil)
			res, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "fix the build", "goal": "CI is green"})
			if err != nil {
				t.Fatal(err)
			}
			if s := decodeSessions(t, []any{res["session"]})[0]; s.SessionID != startThread {
				t.Fatalf("answered %+v", s)
			}
			n.firstWait.Wait()
			if got := f.trace("agent.prompt"); len(got) != tc.typed {
				t.Fatalf("typed %v", got)
			}
		})
	}
}

// Before typing /goal the node looks at the Codex screen as a send does: a draft in the box keeps the command out.
func TestCodexStartGoalNotTypedOverADraft(t *testing.T) {
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	f.setScreen("w1:p2", cxBox("half a message"))
	if _, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "fix the build", "goal": "CI is green"}); err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Fatalf("the goal was typed over a draft: %v", got)
	}
}

// Codex may keep a long objective as something else (a paste placeholder, a file reference); the goal the new thread
// shows after /goal went in is still this one, and the log says how it was kept.
func TestCodexStartGoalLongObjective(t *testing.T) {
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	logs := &syncBuffer{}
	n.logger = log.New(logs, "", 0)
	f.setScreen("w1:p2", cxEmpty)
	codexGoalAnswers(t, n, f, true, func(typed string) string {
		return fmt.Sprintf("[Pasted Content %d chars]", len(typed))
	})
	goal := strings.TrimSpace(strings.Repeat("every check in the suite passes ", 60))
	if _, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "fix the build", "goal": goal}); err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	if got := logs.String(); !strings.Contains(got, "goal set (the rollout keeps its objective as") {
		t.Fatalf("log %q", got)
	}
}
