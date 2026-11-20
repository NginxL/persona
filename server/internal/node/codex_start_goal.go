package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A new Codex start's goal (docs/integrations/agent-goal.md "A goal set at the start"): Codex's `/goal <objective>`,
// typed into its terminal once the start found its thread, whose first message is the task (startCodex). Codex takes
// the command at once, mid-turn too, and writes thread_goal_updated to the rollout; with the goal active it starts
// another turn toward it each time one ends. When the command cannot set the goal (an older Codex, a screen that is
// not ready, a rollout that never confirms it), the same condition goes in as an ordinary message instead
// (codexTextGoal), read back from the rollout's user messages. As for Claude Code, a goal never fails a start.

// codexGoalWait bounds how long the rollout has to show the goal after /goal went in; tests shorten it.
var codexGoalWait = 15 * time.Second

// codexGoalMinVersion is the first Codex whose TUI has /goal on by default (goals stable): an older one shows the
// command as unknown, or would send it as a message.
var codexGoalMinVersion = [3]int{0, 169, 0}

// codexGoalOutcome is what came of the native /goal step. set: the rollout confirmed the goal. fallback: a plain
// text goal may still be delivered (codexTextGoal) — false when the input box may still hold the command, the
// rollout is unknown or the terminal cannot be typed into, which keep the log-only behavior. path and typedAt are
// what the fallback needs: its readback, and the late-confirmation recheck when /goal went in unconfirmed.
type codexGoalOutcome struct {
	set      bool
	fallback bool
	path     string
	typedAt  time.Time // when /goal went in, zero when it never did
}

// codexGoalAfterStart sets the goal of the new Codex thread id in pane, in the background; nothing when goal is empty.
// The session's typing lock is taken before it returns, so a message sent right after the start waits until the
// command is typed; the answer is read without it. The text fallback takes the lock again for its own message.
func (n *Node) codexGoalAfterStart(id, pane, goal string) {
	if goal == "" || id == "" || pane == "" {
		return
	}
	unlock := n.typing.lock(id)
	parent := n.firstPromptParent
	if parent == nil {
		parent = context.Background()
	}
	n.firstWait.Add(1)
	go func() {
		defer n.firstWait.Done()
		// Room for both steps: each may spend a submission confirmation and a rollout wait.
		ctx, cancel := context.WithTimeout(parent, 2*(codexGoalWait+codexConfirm)+30*time.Second)
		defer cancel()
		out := n.codexStartGoal(ctx, id, pane, goal, unlock)
		if out.set || !out.fallback {
			return
		}
		n.codexTextGoal(ctx, id, pane, goal, out)
	}()
}

// codexStartGoal types /goal <goal> into the Codex in pane, calls unlock once the command is typed (or nothing was),
// then waits for the rollout to show the goal. It is the best it can do; what it saw is logged. Its outcome says
// whether a plain text goal may still be delivered; the lock is released before it returns either way.
func (n *Node) codexStartGoal(ctx context.Context, id, pane, goal string, unlock func()) codexGoalOutcome {
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	if host.ParsePane(pane).Kind == host.Botmux { // checkStartGoal refuses it; botmux may hand /goal over as a message
		n.logf("start %s: goal not set: the terminal cannot be read or typed into", id)
		return codexGoalOutcome{}
	}
	path := n.codexPath(id)
	if path == "" {
		n.logf("start %s: goal not set: the thread's rollout is not found", id)
		return codexGoalOutcome{}
	}
	if v := codexRolloutVersion(path); !versionAtLeast(v, codexGoalMinVersion) {
		n.logf("start %s: goal not set: Codex %q has no /goal (0.169 or later has)", id, v)
		return codexGoalOutcome{fallback: true, path: path}
	}
	// The look a send takes first: never type onto a draft (the two would go as one message) or into an approval.
	if err := n.checkPane(ctx, pane, "codex", ""); err != nil {
		n.logf("start %s: goal not set: the terminal is not ready for it: %v", id, err)
		return codexGoalOutcome{fallback: true, path: path}
	}
	cmd := goalCommand(goal)
	typedAt := time.Now()
	if err := n.prompt(ctx, pane, cmd); err != nil {
		n.logf("start %s: goal not typed: %v", id, err)
		// Only a refusal that typed nothing may be followed by the text: any other error may have left a draft.
		return codexGoalOutcome{fallback: deliveryRefusal(err) != nil, path: path}
	}
	if err := n.codexSubmitted(ctx, pane); err != nil {
		n.logf("start %s: the /goal command may still be in the input box: %v", id, err)
		return codexGoalOutcome{} // the box is uncertain: more text could join the command's draft
	}
	n.typing.typed(pane, cmd)
	release() // typed: another message may follow now
	for deadline := time.Now().Add(codexGoalWait); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			n.logf("start %s: the rollout did not show the goal: %v", id, ctx.Err())
			return codexGoalOutcome{fallback: true, path: path, typedAt: typedAt}
		case <-time.After(goalVerdictPoll):
		}
		// A new thread has no goal but the one just typed. Codex may keep a long objective another way (a paste
		// placeholder, a file reference), so a goal updated since is this one whatever its objective reads.
		_, _, g, _ := n.backgroundTasks(path)
		if g == nil || g.UpdatedAt.Before(typedAt.Add(-time.Second)) {
			continue
		}
		state := "set"
		if g.Status != proto.GoalActive {
			state = "set, now " + g.Status
		}
		if g.Objective != goal {
			state += fmt.Sprintf(" (the rollout keeps its objective as %d characters, typed %d)",
				utf8.RuneCountInString(g.Objective), utf8.RuneCountInString(goal))
		}
		n.logf("start %s: goal %s", id, state)
		return codexGoalOutcome{set: true}
	}
	n.logf("start %s: the rollout did not show the goal", id)
	return codexGoalOutcome{fallback: true, path: path, typedAt: typedAt}
}

// codexTextGoal delivers a start's goal as an ordinary message when Codex's own /goal could not be set: the same
// condition as text, after the task's first message, confirmed from the rollout's user messages. It is a delivery
// fallback, not a native goal: nothing about Session.goal is invented. It never fails a start — a screen still not
// ready, a typing failure or a rollout that never shows the message is logged, and the work goes on either way.
func (n *Node) codexTextGoal(ctx context.Context, id, pane, goal string, native codexGoalOutcome) {
	if strings.HasPrefix(goal, "/") { // Codex would take a leading slash for a command, not for text
		n.logf("start %s: text goal not sent: it starts with / and would run as a command", id)
		return
	}
	unlock := n.typing.lock(id) // the native step released the lock; this message takes it again
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	if err := ctx.Err(); err != nil {
		n.logf("start %s: text goal not sent: %v", id, err)
		return
	}
	if current := n.sessionOfPane(pane); current != id { // an engine switch may have replaced the agent in the pane
		n.logf("start %s: text goal not sent: the pane is no longer this session (%q)", id, current)
		return
	}
	// The same look a send takes first: a draft or an approval is still the user's, for this message too.
	if err := n.checkPane(ctx, pane, "codex", ""); err != nil {
		n.logf("start %s: text goal not sent: the terminal is not ready for it: %v", id, err)
		return
	}
	if !native.typedAt.IsZero() { // /goal may have landed after its wait ran out: never restate a goal that is set
		// Read the rollout itself, not a background scan that may not have caught up with a late confirmation. This
		// look and the send below are not atomic: a native goal recorded in between still gets the text too. That
		// leaves both the native goal and the same condition as a message, which is harmless, never a second command.
		if codexRolloutGoalSet(native.path, native.typedAt) {
			n.logf("start %s: goal set, confirmed after its wait; no text goal sent", id)
			return
		}
	}
	typedAt := time.Now()
	if err := n.prompt(ctx, pane, goal); err != nil {
		n.logf("start %s: text goal not typed: %v", id, err)
		return
	}
	if err := n.codexSubmitted(ctx, pane); err != nil {
		n.logf("start %s: the text goal may still be in the input box: %v", id, err)
		return
	}
	n.typing.typed(pane, goal)
	release() // typed: another message may follow now, behind this one
	for deadline := time.Now().Add(codexGoalWait); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			n.logf("start %s: the rollout did not show the text goal: %v", id, ctx.Err())
			return
		case <-time.After(goalVerdictPoll):
		}
		if codexRolloutUserMessage(native.path, goal, typedAt) {
			n.logf("start %s: goal delivered as text, confirmed in the rollout", id)
			return
		}
	}
	n.logf("start %s: the rollout did not show the text goal", id)
}

// codexRolloutUserMessage says whether the rollout has a user message carrying text, recorded at or after since (a
// second of clock tolerance, as the native goal check's). The task's own first message never counts: a goal may be
// the same words as the prompt, and the first message is often written within the tolerance. The text may sit inside
// Codex's own message wrapper, so a message that contains it counts. Only a new thread's rollout is scanned (a
// start's goal), so the file is small. item_completed/UserMessage lines carry the full text, also for a paste the
// composer showed as a chip (codexThreadStart reads the first message the same way).
func codexRolloutUserMessage(path, text string, since time.Time) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	want := strings.Join(strings.Fields(text), " ")
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	first := true // the first user message is the task's prompt, never the goal's delivery
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"UserMessage"`) {
			continue
		}
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Item struct {
					Type    string `json:"type"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil || l.Payload.Type != "item_completed" || l.Payload.Item.Type != "UserMessage" {
			continue
		}
		if first {
			first = false
			continue
		}
		if at := lineTime(line); at.Before(since.Add(-time.Second)) {
			continue
		}
		var b strings.Builder
		for _, c := range l.Payload.Item.Content {
			b.WriteString(c.Text)
		}
		if got := strings.Join(strings.Fields(b.String()), " "); got == want || strings.Contains(got, want) {
			return true
		}
	}
	return false
}

// codexRolloutGoalSet says whether the rollout has a thread_goal_updated with a non-null goal updated at or after
// since (a second of clock tolerance, as the native goal check's). It reads the file directly, so a confirmation the
// background scan has not reached yet still counts.
func codexRolloutGoalSet(path string, since time.Time) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"thread_goal_updated"`) {
			continue
		}
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Goal *struct {
					UpdatedAt int64 `json:"updatedAt"`
				} `json:"goal"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil || l.Type != "event_msg" || l.Payload.Type != "thread_goal_updated" || l.Payload.Goal == nil {
			continue
		}
		if l.Payload.Goal.UpdatedAt >= since.Add(-time.Second).Unix() {
			return true
		}
	}
	return false
}

// codexRolloutVersion is the Codex version a rollout's session_meta names, "" when it names none.
func codexRolloutVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20) // session_meta carries the base instructions
	for lines := 0; sc.Scan() && lines < 5; lines++ {
		if !strings.Contains(sc.Text(), `"session_meta"`) {
			continue
		}
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				CLIVersion string `json:"cli_version"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.Type == "session_meta" {
			return l.Payload.CLIVersion
		}
	}
	return ""
}
