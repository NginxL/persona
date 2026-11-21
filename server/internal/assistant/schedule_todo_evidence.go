package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// What a linked schedule's progress check counts besides an Agent's words, a criterion's state and an explicit
// progress report: the work an Agent does with tools (#11587). A tool call made since the last check is work unless it
// waits, polls or checks CI (activityCall), or repeats a call seen before (in its turn at the last check, or earlier
// since then). An Agent that keeps reading, running and editing is working before it writes a word; one that only
// sleeps, polls or runs the same command again is not. The calls come from the same read of each Agent's latest turn
// as its words. The snapshot keeps that turn's distinct calls (hashed, scheduleCallsKept of them) so the next check
// can tell a repeat, and when it was read, so the next check can tell a new call by its time.

// scheduleCallsKept bounds the distinct calls a snapshot keeps for one Agent. A call older than these, seen again, is
// only a repeat when its time says it came before the last check.
const scheduleCallsKept = 64

// scheduleToolCall is one tool call of an Agent's latest turn as a check reads it.
type scheduleToolCall struct {
	at   time.Time
	key  string // its name and input, hashed: the same command again has the same key
	wait bool   // it waits, polls or checks CI (activityCall)
}

// scheduleCalls are the tool calls in items (an Agent's latest turn), in order.
func scheduleCalls(items []proto.Item) []scheduleToolCall {
	var out []scheduleToolCall
	for _, it := range items {
		if it.Role != "assistant" || it.Kind != proto.KindToolUse || it.ToolName == "" {
			continue
		}
		out = append(out, scheduleToolCall{at: it.TS, key: scheduleCallKey(it.ToolName, it.ToolInput),
			wait: activityCall(transcript.Activity(it.ToolName, it.ToolInput)) == callWait})
	}
	return out
}

// scheduleCallKey names a call by what it does: its tool and its input.
func scheduleCallKey(name string, input []byte) string {
	return evidenceHash([]any{name, string(input)})[:16]
}

// scheduleCallKeys are the distinct keys of calls, the latest last, at most scheduleCallsKept; nil for none.
func scheduleCallKeys(calls []scheduleToolCall) []string {
	var out []string
	seen := map[string]bool{}
	for i := len(calls) - 1; i >= 0 && len(out) < scheduleCallsKept; i-- {
		if k := calls[i].key; !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	slices.Reverse(out)
	return out
}

// scheduleWork is the tool work a check saw since its baseline: the calls made since (each one), how many of them were
// work (not a wait, not a repeat), and when the latest one came.
type scheduleWork struct {
	calls, work int
	latest      time.Time
}

// scheduledTodoWork is the tool work in after (its Agents' latest turns as read) since the baseline before was read at
// since. A call without a time, or from before since, belongs to the baseline: it is no new call, and a call like it
// later is a repeat. Without since nothing can be told apart, and nothing counts.
func scheduledTodoWork(before, after scheduleTodoSnapshot, since time.Time) scheduleWork {
	var w scheduleWork
	if since.IsZero() {
		return w
	}
	for agent, calls := range after.seen {
		old := map[string]bool{}
		for _, k := range before.Calls[agent] {
			old[k] = true
		}
		for _, c := range calls {
			if c.at.IsZero() || !c.at.After(since) {
				old[c.key] = true
				continue
			}
			w.calls++
			if c.at.After(w.latest) {
				w.latest = c.at
			}
			if !c.wait && !old[c.key] {
				w.work++
			}
			old[c.key] = true
		}
	}
	return w
}

// scheduleSince is when the evidence of baseline was read: its own time, or (written before snapshots kept one) the
// time of the run that left it.
func scheduleSince(baseline scheduleTodoSnapshot, run time.Time) time.Time {
	if !baseline.At.IsZero() {
		return baseline.At
	}
	return run
}

// scheduleWorkingReason is the reason of an assessment that found tool work and no visible progress.
func scheduleWorkingReason(l i18n.Lang, loc *time.Location, since, now time.Time, w scheduleWork) string {
	return i18n.T(l, "assistant-schedule.todo_working", i18n.Args{"count": w.calls,
		"since": scheduleClock(loc, since, now), "at": scheduleClock(loc, w.latest, now)})
}

// scheduleEvidence is a no-progress assessment's reason: since when nothing new was seen, and whether its Agents made
// no tool calls at all or only calls that waited or repeated. The owner's decision and the supervisor's signal
// say the same, so neither quotes a run's summary (often "the work goes on fine") as the reason there is no progress.
func scheduleEvidence(l i18n.Lang, loc *time.Location, since, now time.Time, calls int) string {
	args := i18n.Args{"since": scheduleClock(loc, since, now)}
	if calls == 0 {
		return i18n.T(l, "assistant-schedule.todo_evidence_quiet", args)
	}
	args["count"] = calls
	return i18n.T(l, "assistant-schedule.todo_evidence_waiting", args)
}

// scheduleClock is t on the owner's clock: the time alone on now's day, with its date on another.
func scheduleClock(loc *time.Location, t, now time.Time) string {
	t, now = t.In(loc), now.In(loc)
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// scheduleAskAfter is how long a linked to-do may go without progress before its check asks the owner: the floor the
// no_progress signal uses (spinAfter: at least 30 minutes, or 1.5× what the task should take), so a long task is not
// raised to the owner after two short windows. Its supervisor hears of the first no-progress run either way.
func (s *Service) scheduleAskAfter(t Todo) time.Duration {
	if s.todoAskAfter > 0 {
		return s.todoAskAfter
	}
	expect := 0
	if t.Supervision != nil {
		expect = t.Supervision.ExpectMinutes
	}
	return spinAfter(expect)
}

// The check a decision came from. escalateScheduledTodo raises its decision through task_review, whose conclusion
// keeps the schedule's ID on the ask (acceptAsk.Check), so the owner's answer goes to the supervisor (checkDecided).
type scheduledCheckKey struct{}

func withScheduledCheck(ctx context.Context, schedule string) context.Context {
	return context.WithValue(ctx, scheduledCheckKey{}, schedule)
}

func scheduledCheckOf(ctx context.Context) string {
	id, _ := ctx.Value(scheduledCheckKey{}).(string)
	return id
}

// checkDecided hands the owner's option label on decision a, which task t's recurring progress check raised (a.Check),
// to t's supervising conversation, not its Agent. Change approach asks the supervisor for another approach and Keep
// waiting keeps the one it has: neither is an instruction for the Agent at work, which hears only what the supervisor
// then sends it. The supervisor gets the decision as asked, with each option's result. A worker's decision goes to
// the conversation above its task leader, which reviewed it. told: the answer is already in the Agent's chat (the
// owner named the option there, or a conversation passed it on: decisionTold), and the supervisor hears that instead of
// "not told". The caller records the decision and ends the ask.
func (s *Service) checkDecided(ctx context.Context, t Todo, a *acceptAsk, label string, told bool) {
	s.userDecided(ctx, t.ID) // no longer needs_user: the signal goes to the supervisor, not the main conversation
	g := superviseSignal{Todo: t.ID, Kind: sigCheck, Report: checkDecidedReport,
		Key: checkDecidedReport + "@" + a.AskedAt.UTC().Format(time.RFC3339Nano), Detail: label,
		Decision: s.signalDecision(ctx, a), Told: told}
	if ag := scheduleAgent(t); ag.SessionID != "" {
		g.Machine, g.Session = ag.MachineID, ag.SessionID
	}
	if s.agentSupervised(t) {
		g.To, _ = s.supervisorAboveTask(ctx, t)
	}
	s.signal(g)
}

// scheduleAgent is the task Agent a check's signal names: the latest one still working on t, else the latest one.
func scheduleAgent(t Todo) TodoAgent {
	for i := len(t.Agents) - 1; i >= 0; i-- {
		if a := t.Agents[i]; a.ReleasedAt == nil && a.DismissedAt == nil {
			return a
		}
	}
	return lastAgent(t)
}
