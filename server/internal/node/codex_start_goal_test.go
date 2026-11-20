package node

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func withCodexGoalWait(t *testing.T, d time.Duration) {
	t.Helper()
	old, oldConfirm := codexGoalWait, codexConfirm
	codexGoalWait, codexConfirm = d, 5*time.Second // submission confirmation must outlast codexStep
	t.Cleanup(func() { codexGoalWait, codexConfirm = old, oldConfirm })
}

func withStartRolloutVersion(t *testing.T, v string) {
	t.Helper()
	old := startRolloutVersion
	startRolloutVersion = v
	t.Cleanup(func() { startRolloutVersion = old })
}

// appendCodexRollout appends one line to the new thread's rollout, as Codex itself would. The path lookup falls back
// to a glob when the cached lookup has not caught up with the file the fake just made.
func appendCodexRollout(t *testing.T, n *Node, line []byte) {
	t.Helper()
	path := n.codexPath(startThread)
	if path == "" {
		matches, _ := rolloutGlob(filepath.Join(n.codexHome(), "sessions", "*", "*", "*", "rollout-*-"+startThread+"*.jsonl"))
		if len(matches) > 0 {
			path = matches[0]
		}
	}
	if path == "" {
		t.Logf("the new thread's rollout is not found yet")
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Error(err)
		return
	}
	_, _ = file.Write(append(line, '\n'))
	_ = file.Close()
}

// codexGoalEvent is a thread_goal_updated line with an active goal, as Codex 0.169 writes it.
func codexGoalEvent(objective string) []byte {
	line, _ := json.Marshal(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": "event_msg",
		"payload": map[string]any{"type": "thread_goal_updated", "threadId": startThread, "goal": map[string]any{
			"threadId": startThread, "objective": objective, "status": "active",
			"tokensUsed": 0, "timeUsedSeconds": 0, "createdAt": time.Now().Unix(), "updatedAt": time.Now().Unix()}}})
	return line
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
		appendCodexRollout(t, n, codexGoalEvent(objective))
	})
}

// codexTextGoalAnswers makes the Codex in w1:p2 record an ordinary message with the goal's text in its rollout, the
// user message (item_completed/UserMessage) a delivered text goal is read back from.
func codexTextGoalAnswers(t *testing.T, n *Node, f *fakeHerdr, goal string) {
	t.Helper()
	f.after(func(c herdrCall) {
		if c.Method != "agent.prompt" || str(c.Params["text"]) != goal {
			return
		}
		line, _ := json.Marshal(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": "event_msg",
			"payload": map[string]any{"type": "item_completed",
				"item": map[string]any{"type": "UserMessage", "id": "u2", "content": []any{map[string]any{"type": "text", "text": goal}}}}})
		appendCodexRollout(t, n, line)
	})
}

// startCodexWithGoal starts a new Codex with goal and waits for its goal steps; the answered session is the thread
// whose first message the start already found in the rollout, so anything typed after is after the task.
func startCodexWithGoal(t *testing.T, n *Node, goal string) {
	t.Helper()
	params := map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "fix the build"}
	if goal != "" {
		params["goal"] = goal
	}
	res, err := request(t, n, "session.start", params)
	if err != nil {
		t.Fatal(err)
	}
	if s := decodeSessions(t, []any{res["session"]})[0]; s.SessionID != startThread {
		t.Fatalf("answered %+v", s)
	}
	n.firstWait.Wait()
}

// A new Codex start with a goal types Codex's /goal once, after the start found the thread its first message began, and
// the rollout then has the goal, active. A goal the native command set sends no text fallback.
func TestCodexStartSetsTheGoal(t *testing.T) {
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	f.setScreen("w1:p2", cxEmpty)
	codexGoalAnswers(t, n, f, true, nil)
	codexTextGoalAnswers(t, n, f, "go test ./... passes; the fix is merged")
	goal := "go test ./... passes; the fix is merged"

	startCodexWithGoal(t, n, goal)
	got := f.trace("agent.prompt")
	if len(got) != 1 || !strings.Contains(got[0], `"/goal `+goal+`"`) {
		t.Fatalf("typed %v", got)
	}
	if _, _, g, _ := n.backgroundTasks(n.codexPath(startThread)); g == nil || g.Objective != goal || g.Status != proto.GoalActive {
		t.Fatalf("goal %+v", g)
	}
}

// A Codex too old for /goal gets the same goal as an ordinary message instead: no command, the goal's text once,
// after the task's first message, confirmed by reading the rollout's user message back.
func TestCodexStartGoalTextFallbackOnOldCodex(t *testing.T) {
	withStartRolloutVersion(t, "0.168.2")
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	logs := &syncBuffer{}
	n.logger = log.New(logs, "", 0)
	f.setScreen("w1:p2", cxEmpty)
	goal := "CI is green"
	codexGoalAnswers(t, n, f, true, nil) // would answer a command: none may be typed
	codexTextGoalAnswers(t, n, f, goal)

	startCodexWithGoal(t, n, goal)
	got := f.trace("agent.prompt")
	if len(got) != 1 || !strings.Contains(got[0], goal) || strings.Contains(got[0], "/goal") {
		t.Fatalf("typed %v", got)
	}
	if !strings.Contains(logs.String(), "goal delivered as text, confirmed in the rollout") {
		t.Fatalf("log %q", logs.String())
	}
}

// /goal typed and submitted but never confirmed in the rollout: after the bounded wait the goal goes in once more as
// text, and the start is as it was.
func TestCodexStartGoalTextFallbackWhenUnconfirmed(t *testing.T) {
	withCodexGoalWait(t, 300*time.Millisecond)
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	logs := &syncBuffer{}
	n.logger = log.New(logs, "", 0)
	f.setScreen("w1:p2", cxEmpty)
	goal := "CI is green"
	codexGoalAnswers(t, n, f, false, nil) // the native goal never shows
	codexTextGoalAnswers(t, n, f, goal)

	startCodexWithGoal(t, n, goal)
	got := f.trace("agent.prompt")
	if len(got) != 2 || !strings.Contains(got[0], `"/goal `+goal+`"`) || !strings.Contains(got[1], goal) || strings.Contains(got[1], "/goal") {
		t.Fatalf("typed %v", got)
	}
	if !strings.Contains(logs.String(), "goal delivered as text, confirmed in the rollout") {
		t.Fatalf("log %q", logs.String())
	}
}

// A native goal the rollout confirms only after its wait ran out is not restated: the text fallback rechecks the
// rollout right before it would type, sees the late confirmation and sends nothing.
func TestCodexStartGoalTextFallbackLateNativeConfirmation(t *testing.T) {
	withCodexGoalWait(t, 300*time.Millisecond)
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	logs := &syncBuffer{}
	n.logger = log.New(logs, "", 0)
	f.setScreen("w1:p2", cxEmpty)
	goal := "CI is green"
	// After /goal went in, the first screen read is the submission check's; the next one is the text fallback's
	// readiness look. Confirm the native goal then, after its wait and before any text could be typed.
	goalTyped := false
	reads := 0
	var mu sync.Mutex
	f.after(func(c herdrCall) {
		mu.Lock()
		defer mu.Unlock()
		if c.Method == "agent.prompt" && strings.HasPrefix(str(c.Params["text"]), "/goal ") {
			goalTyped = true
			reads = 0
			return
		}
		if !goalTyped || c.Method != "pane.read" {
			return
		}
		reads++
		if reads == 2 {
			appendCodexRollout(t, n, codexGoalEvent(goal))
		}
	})

	startCodexWithGoal(t, n, goal)
	got := f.trace("agent.prompt")
	if len(got) != 1 || !strings.Contains(got[0], `"/goal `+goal+`"`) {
		t.Fatalf("typed %v", got)
	}
	if !strings.Contains(logs.String(), "goal set, confirmed after its wait; no text goal sent") {
		t.Fatalf("log %q", logs.String())
	}
}

// The text fallback never fails a start: a goal the rollout never shows, even as a user message, is only logged.
func TestCodexStartGoalTextFallbackNeverFailsTheStart(t *testing.T) {
	withStartRolloutVersion(t, "0.168.2")
	withCodexGoalWait(t, 300*time.Millisecond)
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	logs := &syncBuffer{}
	n.logger = log.New(logs, "", 0)
	f.setScreen("w1:p2", cxEmpty)
	goal := "CI is green" // no text answers: the rollout never shows the message

	startCodexWithGoal(t, n, goal)
	got := f.trace("agent.prompt")
	if len(got) != 1 || !strings.Contains(got[0], goal) {
		t.Fatalf("typed %v", got)
	}
	if !strings.Contains(logs.String(), "the rollout did not show the text goal") {
		t.Fatalf("log %q", logs.String())
	}
}

// Before typing /goal the node looks at the Codex screen as a send does: a draft in the box keeps the command out,
// and the text fallback takes the same look, so it types nothing over the draft either.
func TestCodexStartGoalNotTypedOverADraft(t *testing.T) {
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	f.setScreen("w1:p2", cxBox("half a message"))
	startCodexWithGoal(t, n, "CI is green")
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Fatalf("the goal was typed over a draft: %v", got)
	}
}

// A /goal whose submission could not be confirmed may still sit in the input box: no text follows it, where the two
// would go as one message.
func TestCodexStartGoalNoTextFallbackWhenSubmissionUncertain(t *testing.T) {
	withCodexGoalWait(t, time.Second)
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	logs := &syncBuffer{}
	n.logger = log.New(logs, "", 0)
	f.setScreen("w1:p2", cxEmpty)
	goal := "CI is green"
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" && strings.HasPrefix(str(c.Params["text"]), "/goal ") {
			f.setScreen("w1:p2", cxBox("/goal "+goal)) // the command never leaves the box
		}
	})

	startCodexWithGoal(t, n, goal)
	got := f.trace("agent.prompt")
	if len(got) != 1 || !strings.Contains(got[0], `"/goal `+goal+`"`) {
		t.Fatalf("typed %v", got)
	}
	if !strings.Contains(logs.String(), "the /goal command may still be in the input box") {
		t.Fatalf("log %q", logs.String())
	}
}

// A goal that starts with a slash would run as a command, not go in as text: the fallback leaves it alone.
func TestCodexStartGoalTextFallbackLeadingSlash(t *testing.T) {
	withStartRolloutVersion(t, "0.168.2")
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	logs := &syncBuffer{}
	n.logger = log.New(logs, "", 0)
	f.setScreen("w1:p2", cxEmpty)

	startCodexWithGoal(t, n, "/compact the thread")
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Fatalf("typed %v", got)
	}
	if !strings.Contains(logs.String(), "text goal not sent: it starts with /") {
		t.Fatalf("log %q", logs.String())
	}
}

// No goal (a task without criteria): nothing is typed after the first message, native or text.
func TestCodexStartWithoutGoalTypesNothing(t *testing.T) {
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	f.setScreen("w1:p2", cxEmpty)
	startCodexWithGoal(t, n, "")
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Fatalf("typed %v", got)
	}
}

// The late-confirmation recheck reads the rollout itself: a goal event already written, whether or not a background
// scan has caught up, suppresses the text fallback.
func TestCodexRolloutGoalSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	since := time.Now()
	goalLine := func(updatedAt int64, null bool) string {
		var goal any
		if !null {
			goal = map[string]any{"objective": "CI is green", "status": "active", "updatedAt": updatedAt}
		}
		line, _ := json.Marshal(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": "event_msg",
			"payload": map[string]any{"type": "thread_goal_updated", "goal": goal}})
		return string(line)
	}
	old := goalLine(since.Add(-time.Minute).Unix(), false) + "\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if codexRolloutGoalSet(path, since) {
		t.Fatal("a goal from before the send counted")
	}
	recent := goalLine(since.Unix(), false) + "\n"
	if err := os.WriteFile(path, []byte(old+recent), 0o600); err != nil {
		t.Fatal(err)
	}
	if !codexRolloutGoalSet(path, since) {
		t.Fatal("the goal the rollout already shows was not seen")
	}
	cleared := goalLine(since.Unix(), true) + "\n"
	if err := os.WriteFile(path, []byte(old+cleared), 0o600); err != nil {
		t.Fatal(err)
	}
	if codexRolloutGoalSet(path, since) {
		t.Fatal("a cleared goal counted")
	}
}

// The text fallback revalidates the pane before typing: if an engine switch replaced the session in it, nothing goes
// into the replacement agent.
func TestCodexStartGoalTextFallbackNotTypedIntoAReplacedPane(t *testing.T) {
	withCodexGoalWait(t, 300*time.Millisecond)
	n, f, _, _ := codexStartHerdr(t, 100*time.Millisecond)
	logs := &syncBuffer{}
	n.logger = log.New(logs, "", 0)
	f.setScreen("w1:p2", cxEmpty)
	goal := "CI is green"
	codexGoalAnswers(t, n, f, false, nil) // native goal never confirms, so the fallback is next
	typed := make(chan struct{})
	var once sync.Once
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" && strings.HasPrefix(str(c.Params["text"]), "/goal ") {
			once.Do(func() { close(typed) })
		}
	})
	res, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "fix the build", "goal": goal})
	if err != nil {
		t.Fatal(err)
	}
	s := decodeSessions(t, []any{res["session"]})[0]
	<-typed // the native command is in; its rollout wait is running
	// Before the fallback would type, the pane hosts another session (an engine switch, a replacement agent).
	n.mu.Lock()
	delete(n.agents, s.SessionID)
	n.agents["another-session"] = host.Agent{PaneID: "w1:p2"}
	n.mu.Unlock()
	n.firstWait.Wait()
	got := f.trace("agent.prompt")
	if len(got) != 1 || !strings.Contains(got[0], `"/goal `+goal+`"`) {
		t.Fatalf("typed into the replacement pane: %v", got)
	}
	if !strings.Contains(logs.String(), "the pane is no longer this session") {
		t.Fatalf("log %q", logs.String())
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
	startCodexWithGoal(t, n, goal)
	if got := logs.String(); !strings.Contains(got, "goal set (the rollout keeps its objective as") {
		t.Fatalf("log %q", got)
	}
}

// The text goal's readback: a user message at or after the send counts, also inside Codex's own wrapper and with the
// terminal's whitespace; the task's own first message never counts, even when it has the same words as the goal and is
// less than a second old.
func TestCodexRolloutUserMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	since := time.Now()
	userLine := func(at time.Time, text string) string {
		line, _ := json.Marshal(map[string]any{"timestamp": at.UTC().Format(time.RFC3339Nano), "type": "event_msg",
			"payload": map[string]any{"type": "item_completed",
				"item": map[string]any{"type": "UserMessage", "id": "u", "content": []any{map[string]any{"type": "text", "text": text}}}}})
		return string(line)
	}
	goal := "CI is green"
	// The task's first message, just before the goal was typed and with the same words: never the goal's readback.
	before := userLine(since.Add(-100*time.Millisecond), goal) + "\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if codexRolloutUserMessage(path, goal, since) {
		t.Fatal("the task's own first message counted as the goal's delivery")
	}
	wrapped := "<user_instructions>\nCI  is\n green\n</user_instructions>"
	if err := os.WriteFile(path, []byte(before+userLine(since.Add(time.Second), wrapped)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !codexRolloutUserMessage(path, goal, since) {
		t.Fatal("the wrapped, re-wrapped message was not read back")
	}
	if codexRolloutUserMessage(path, "another goal", since) {
		t.Fatal("a different text counted")
	}
	if codexRolloutUserMessage(filepath.Join(t.TempDir(), "missing.jsonl"), goal, since) {
		t.Fatal("a missing rollout counted")
	}
}
