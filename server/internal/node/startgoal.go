package node

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// A start's goal (session.start's goal, nodeproto.StartGoal; docs/integrations/agent-goal.md "A goal set at the
// start"): Claude Code's `/goal <condition>`, typed into a new Claude after its first message, so that the agent checks
// the condition each time it would stop. /goal is a local command that answers at once and then starts a turn toward
// its condition; typed before the task it would set the agent to work from the condition alone, and typed in the same
// message as the task it would take the task for its condition. So it goes on its own, after the first message is in
// the transcript (firstPrompt), while the agent works: Claude Code runs it at once even mid-turn. A goal never fails a
// start: Claude is up, and the work goes on either way. A new Codex takes Codex's own /goal (codex_start_goal.go).

// goalVerdictWait bounds how long the screen has to show what Claude Code said to /goal; tests shorten it.
var goalVerdictWait = 5 * time.Second

const goalVerdictPoll = 200 * time.Millisecond

// Why a start's goal cannot be had: refused before anything starts, in the words the node's other start refusals use
// (ultracode.go), which the caller may put on the task.
const (
	goalNotClaude  = "只有新起的 Claude Code 或 Codex Agent 才能设开始时目标"
	goalResumed    = "恢复的会话不设开始时目标（会覆盖它原来的目标）"
	goalTooLong    = "目标太长，没有设 /goal"
	goalNoPrompt   = "开始时目标要跟在第一条消息后面设，这次启动没有第一条消息"
	goalScreenless = "这个会话所在的终端不能读屏或按键，没法设 /goal"
)

// What Claude Code said to a start's /goal when it did not take, for the node's log.
const (
	goalUntrusted    = "the workspace is not trusted"
	goalHooksOff     = "hooks are restricted (disableAllHooks or allowManagedHooksOnly)"
	goalPolicyUnread = "the organization's settings were not read yet"
	goalOverLong     = "the condition is too long"
	goalEmpty        = "the condition is empty"
	goalUnknown      = "this Claude Code has no /goal"
)

func goalCommand(goal string) string { return "/goal " + goal }

// cleanStartGoal is a start's goal on one line, "" for none: a newline would submit the command with only its first
// line.
func cleanStartGoal(goal string) string { return strings.Join(strings.Fields(goal), " ") }

// checkStartGoal refuses, before anything starts, a start whose goal (cleaned) cannot be had: kind is the agent the
// start runs ("claude", or what the launcher, a runtime or an app makes it), resume the session it resumes, prompt
// the first message the goal is set after, hostKind the host the start runs in (its own, else the default).
func (n *Node) checkStartGoal(goal, kind, hostKind, resume, prompt string) error {
	switch {
	case goal == "":
		return nil
	case strings.TrimSpace(prompt) == "":
		return refusal(proto.CodeStartGoalUnsupported, goalNoPrompt, nil)
	case utf8.RuneCountInString(goal) > proto.StartGoalMax:
		return refusal(proto.CodeStartGoalUnsupported, goalTooLong, nil)
	case resume != "":
		return refusal(proto.CodeStartGoalUnsupported, goalResumed, nil)
	case kind != "claude" && kind != "codex": // Codex's goal: codex_start_goal.go
		return refusal(proto.CodeStartGoalUnsupported, goalNotClaude, nil)
	}
	if h := n.hosts.Get(hostKind); h != nil && !drivesScreen(h) { // botmux may hand /goal over as a plain message
		return refusal(proto.CodeStartGoalUnsupported, goalScreenless, nil)
	}
	return nil
}

// goalOutcome reads what Claude Code said to a /goal typed at a screen that showed before: set, or why not; both
// zero while it has said nothing yet. Only what is new since before counts, never an earlier goal's line above it.
// Lines are joined first: a long condition wraps, and "Goal set:" may be split across two lines.
func goalOutcome(before, shown string) (set bool, note string) {
	flat := strings.Join(strings.Fields(screen.Plain(shown)), " ")
	prev := strings.Join(strings.Fields(screen.Plain(before)), " ")
	newer := func(s string) bool { return strings.Count(flat, s) > strings.Count(prev, s) }
	switch {
	case newer("Goal set: "):
		return true, ""
	case newer("/goal is only available in trusted workspaces"):
		return false, goalUntrusted
	case newer("/goal can't run while hooks are restricted"):
		return false, goalHooksOff
	case newer("/goal can't run yet"):
		return false, goalPolicyUnread
	case newer("Goal condition is limited to"), newer("The goal condition exceeds"):
		return false, goalOverLong
	case newer("The goal condition is empty"):
		return false, goalEmpty
	case newer("Unknown command: /goal"):
		return false, goalUnknown
	}
	return false, ""
}

// goalAfterFirst sets the goal of a start whose first message the transcript has just shown, with a bound of its
// own (the first message's check may have spent most of its own); nothing when goal is empty. The caller holds the
// session's typing lock while the command is typed, so no other message goes in between; unlock gives the lock back
// as soon as the command is typed, and the answer is read without it, so the user's next message never waits on it.
func (n *Node) goalAfterFirst(parent context.Context, id, pane, goal string, unlock func()) {
	if goal == "" {
		unlock()
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, goalVerdictWait+15*time.Second)
	defer cancel()
	if n.startGoalUnlocking(ctx, id, pane, goal, unlock) {
		// Not seen to go in: it may still be in the box, where the next message's own check finds it as a draft and
		// refuses rather than sending the two together.
		n.logf("start %s: the /goal command may still be in the input box", id)
	}
}

// startGoal types /goal <goal> into the Claude in pane and reads the answer off the screen, holding no lock of its
// own (tests and callers that hold none). See startGoalUnlocking.
func (n *Node) startGoal(ctx context.Context, id, pane, goal string) (unsure bool) {
	return n.startGoalUnlocking(ctx, id, pane, goal, func() {})
}

// startGoalUnlocking types /goal <goal> into the Claude in pane (after its first message is in), calls unlock once
// the command is typed (or nothing was), and then reads the answer off the screen. It is the best it can do; what it
// saw is logged. unsure: the command went to the terminal and is not known to have gone in, so it may still sit in the
// input box.
func (n *Node) startGoalUnlocking(ctx context.Context, id, pane, goal string, unlock func()) (unsure bool) {
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	// The same look the ultracode switch takes first: never type into a dialog (Enter would answer it) or onto a
	// draft in the box (the two would go as one message).
	if err := n.checkPane(ctx, pane, "claude", ""); err != nil {
		n.logf("start %s: goal not set: the terminal is not ready for it: %v", id, err)
		return false
	}
	before, err := n.readPane(ctx, pane, "visible", 60)
	if err != nil { // its answer could not be read: better no goal than a command whose fate is unknown
		n.logf("start %s: goal not set: the screen cannot be read: %v", id, err)
		return false
	}
	cmd := goalCommand(goal)
	if err := n.prompt(ctx, pane, cmd); err != nil {
		n.logf("start %s: goal not typed: %v", id, err)
		return deliveryRefusal(err) == nil // a refusal typed nothing; any other error may have typed it
	}
	n.typing.typed(pane, cmd)
	release() // typed: another message may follow now, behind the command in Claude Code's own order
	for deadline := time.Now().Add(goalVerdictWait); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			n.logf("start %s: no answer to /goal on the screen: %v", id, ctx.Err())
			return true
		case <-time.After(goalVerdictPoll):
		}
		shown, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil || shown == before {
			continue
		}
		if set, note := goalOutcome(before, shown); set {
			n.logf("start %s: goal set", id)
			return false
		} else if note != "" {
			n.logf("start %s: goal not set: %s", id, note)
			return false
		}
	}
	n.logf("start %s: no answer to /goal on the screen", id)
	return true
}
