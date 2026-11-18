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
// another turn toward it each time one ends. As for Claude Code, a goal never fails a start.

// codexGoalWait bounds how long the rollout has to show the goal after /goal went in; tests shorten it.
var codexGoalWait = 15 * time.Second

// codexGoalMinVersion is the first Codex whose TUI has /goal on by default (goals stable): an older one shows the
// command as unknown, or would send it as a message.
var codexGoalMinVersion = [3]int{0, 169, 0}

// codexGoalAfterStart sets the goal of the new Codex thread id in pane, in the background; nothing when goal is empty.
// The session's typing lock is taken before it returns, so a message sent right after the start waits until the
// command is typed; the answer is read without it.
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
		ctx, cancel := context.WithTimeout(parent, codexGoalWait+codexConfirm+15*time.Second)
		defer cancel()
		n.codexStartGoal(ctx, id, pane, goal, unlock)
	}()
}

// codexStartGoal types /goal <goal> into the Codex in pane, calls unlock once the command is typed (or nothing was),
// then waits for the rollout to show the goal. It is the best it can do; what it saw is logged.
func (n *Node) codexStartGoal(ctx context.Context, id, pane, goal string, unlock func()) {
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	if host.ParsePane(pane).Kind == host.Botmux { // checkStartGoal refuses it; botmux may hand /goal over as a message
		n.logf("start %s: goal not set: the terminal cannot be read or typed into", id)
		return
	}
	path := n.codexPath(id)
	if path == "" {
		n.logf("start %s: goal not set: the thread's rollout is not found", id)
		return
	}
	if v := codexRolloutVersion(path); !versionAtLeast(v, codexGoalMinVersion) {
		n.logf("start %s: goal not set: Codex %q has no /goal (0.169 or later has)", id, v)
		return
	}
	// The look a send takes first: never type onto a draft (the two would go as one message) or into an approval.
	if err := n.checkPane(ctx, pane, "codex", ""); err != nil {
		n.logf("start %s: goal not set: the terminal is not ready for it: %v", id, err)
		return
	}
	cmd := goalCommand(goal)
	typedAt := time.Now()
	if err := n.prompt(ctx, pane, cmd); err != nil {
		n.logf("start %s: goal not typed: %v", id, err)
		return
	}
	if err := n.codexSubmitted(ctx, pane); err != nil {
		n.logf("start %s: the /goal command may still be in the input box: %v", id, err)
		return
	}
	n.typing.typed(pane, cmd)
	release() // typed: another message may follow now
	for deadline := time.Now().Add(codexGoalWait); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			n.logf("start %s: the rollout did not show the goal: %v", id, ctx.Err())
			return
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
		return
	}
	n.logf("start %s: the rollout did not show the goal", id)
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
