package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ScheduleTodoAssessment describes observable evidence, not acceptance of the work.
type ScheduleTodoAssessment struct {
	TodoID string `json:"todo_id"`
	// pending, baseline, progress, working, no_progress, unknown, error, inactive, not_run, superseded
	State      string `json:"state"`
	NoProgress int    `json:"no_progress,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type scheduleTodoSnapshot struct {
	Answers    map[string]string `json:"answers,omitempty"`
	Criteria   map[string]string `json:"criteria,omitempty"`
	Report     string            `json:"report,omitempty"`
	Unknown    bool              `json:"unknown,omitempty"`
	Unreadable map[string]bool   `json:"unreadable,omitempty"` // Agent identities whose evidence could not be read
	// Calls: each Agent's distinct tool calls in its latest turn, hashed (scheduleCallKeys); At: when it was read. The
	// next check tells new work from waits and repeats by them (scheduledTodoWork).
	Calls map[string][]string           `json:"calls,omitempty"`
	At    time.Time                     `json:"at,omitzero"`
	seen  map[string][]scheduleToolCall // the latest turns' calls as read, for comparing; not kept
}

func (v scheduleTodoSnapshot) validate() error {
	// Check decoded strings: literal backslash-u0000 text is valid JSONB.
	for _, entries := range []map[string]string{v.Answers, v.Criteria} {
		for key, value := range entries {
			if strings.ContainsRune(key, 0) || strings.ContainsRune(value, 0) {
				return errors.New("progress snapshot contains NUL")
			}
		}
	}
	if strings.ContainsRune(v.Report, 0) {
		return errors.New("progress snapshot contains NUL")
	}
	for key := range v.Unreadable {
		if strings.ContainsRune(key, 0) {
			return errors.New("progress snapshot contains NUL")
		}
	}
	return nil
}

// Kept with the schedule, not just the capped, shared run history.
type scheduleTodoProgress struct {
	Run        string                 `json:"run"`
	At         time.Time              `json:"at"`
	Snapshot   scheduleTodoSnapshot   `json:"snapshot"`
	Assessment ScheduleTodoAssessment `json:"assessment"`
	NoProgress int                    `json:"no_progress"`
	// Since: when the evidence the streak's first no-progress run compared with was read; Calls: the tool calls its
	// runs saw, all waits or repeats. Zero without a streak.
	Since     time.Time `json:"since,omitzero"`
	Calls     int       `json:"calls,omitempty"`
	Signaled  bool      `json:"signaled,omitempty"`
	Escalated bool      `json:"escalated,omitempty"`
	// Kept before creating the review, so a failed final marker write can recognize its own decision.
	EscalationRun string `json:"escalation_run,omitempty"`
}

func (s *Service) validateScheduleTodo(ctx context.Context, self string, id *string) error {
	if id == nil || strings.TrimSpace(*id) == "" {
		return nil
	}
	if self == "" {
		return errors.New("a supervising conversation is required for todo_id")
	}
	*id = strings.TrimSpace(*id)
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	t, ok := s.todoNow(*id)
	t = s.withBrief(t)
	if !ok || !scheduledTodoOpen(t) {
		return errors.New("todo_id must name an open, supervised to-do")
	}
	if t.Origin != self && t.Supervision.Supervisor != self && !s.isMain(ctx, self) &&
		!s.backgroundSupervises(ctx, self, t) && !s.mayReviewForLeader(ctx, self, t) {
		return errors.New("this conversation does not supervise todo_id")
	}
	return nil
}

func bindScheduleTodo(t *Task, id *string) error {
	if id == nil {
		return nil
	}
	value := strings.TrimSpace(*id)
	if value != "" && t.Repeat == repeatOnce {
		return errors.New("todo_id progress tracking requires a recurring schedule")
	}
	if t.TodoID != value {
		t.TodoID, t.TodoProgress = value, nil
	}
	return nil
}

func scheduledTodoOpen(t Todo) bool {
	if t.DoneAt != nil || t.CancelledAt != nil || t.Supervision == nil {
		return false
	}
	for _, step := range t.Lineage {
		if step.TodoID == t.ID && step.SupersededBy != "" {
			return false
		}
	}
	return true
}

func evidenceHash(v any) string {
	b, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func (s *Service) scheduledTodoSnapshot(ctx context.Context, task Task, run ScheduleRun) (scheduleTodoSnapshot, bool) {
	// Read before the turns are: a call made while they are read is in them, or comes after this time.
	out := scheduleTodoSnapshot{Answers: map[string]string{}, Criteria: map[string]string{}, At: time.Now().UTC()}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		out.Unknown = true
		return out, true
	}
	t, ok := s.todoNow(task.TodoID)
	t = s.withBrief(t)
	if !ok || !scheduledTodoOpen(t) {
		return out, false
	}
	for _, c := range t.Supervision.Criteria {
		out.Criteria[c.Criterion] = c.State
	}
	if p := t.Supervision.Progress; p != nil && p.Kind == upReportProgress {
		out.Report = evidenceHash([]any{p.Text, p.Detail, p.Step, p.Steps})
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var origins map[string]shownOrigin
	if len(t.Agents) > 0 {
		var read bool
		origins, read = s.conversationOrigins()
		if !read {
			out.Unknown = true
			out.Unreadable = map[string]bool{}
			for _, a := range t.Agents {
				out.Unreadable[evidenceHash([2]string{a.MachineID, a.SessionID})] = true
			}
			return out, true
		}
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil ||
			a.MachineID == run.MachineID && a.SessionID == run.SessionID ||
			a.MachineID == task.MachineID && a.SessionID == task.SessionID ||
			a.MachineID == task.RunMachineID && a.SessionID == task.RunSessionID {
			continue
		}
		if o := origins[a.SessionID]; o.machine == a.MachineID && o.origin == OriginTask && o.ref == task.ID {
			continue // Origin ownership outlives rotation, bounded run history and manager restarts.
		}
		items, read := s.lastTurnItems(ctx, a.MachineID, a.SessionID)
		key := evidenceHash([2]string{a.MachineID, a.SessionID})
		if !read {
			out.Unknown = true
			if out.Unreadable == nil {
				out.Unreadable = map[string]bool{}
			}
			out.Unreadable[key] = true
			continue
		}
		_, answer := turnExchange(items)
		if answer != "" && !apiErrorText(answer) {
			// Hash the pair, not a NUL-delimited string that JSONB cannot store.
			out.Answers[key] = evidenceHash(answer)
		}
		if calls := scheduleCalls(items); len(calls) > 0 {
			if out.seen == nil {
				out.seen, out.Calls = map[string][]scheduleToolCall{}, map[string][]string{}
			}
			out.seen[key], out.Calls[key] = calls, scheduleCallKeys(calls)
		}
	}
	return out, true
}

func scheduledTodoAdvanced(before, after scheduleTodoSnapshot) bool {
	for agent, answer := range after.Answers {
		old, known := before.Answers[agent]
		if answer != "" && old != answer && (known || !before.Unknown && !before.Unreadable[agent]) {
			return true
		}
	}
	for criterion, state := range after.Criteria {
		if old, ok := before.Criteria[criterion]; ok && old != state {
			return true
		}
	}
	return after.Report != "" && after.Report != before.Report && (before.Report != "" || !before.Unknown)
}

func retainTodoEvidence(before, after scheduleTodoSnapshot) scheduleTodoSnapshot {
	out := after
	// A failed read must not replace a previously readable baseline with an unknown one.
	out.Unknown = before.Unknown && after.Unknown
	if after.Unknown && len(after.Criteria) == 0 {
		out.Criteria = maps.Clone(before.Criteria)
	}
	out.Answers = maps.Clone(before.Answers)
	if out.Answers == nil {
		out.Answers = map[string]string{}
	}
	maps.Copy(out.Answers, after.Answers)
	if out.Report == "" {
		out.Report = before.Report
	}
	// An Agent whose turn could not be read keeps the calls it had, so its next repeat is still one.
	out.Calls = maps.Clone(before.Calls)
	if out.Calls == nil && len(after.Calls) > 0 {
		out.Calls = map[string][]string{}
	}
	maps.Copy(out.Calls, after.Calls)
	return out
}

// scheduledRunReply is a linked run's own reply, its assessment's explanation (and so the reason of the owner decision a
// second no-progress run raises, escalateScheduledTodo); "" when it cannot be told apart, and the assessment then says
// what the hub saw (todo_no_evidence). The reply is the one to the run's own prompt (runReply). A run in its source
// conversation shares that conversation with its other work: its supervision messages, the owner's words, the runs of
// other schedules sent there. Two linked runs due in the same minute are answered one after the other, and both
// watchers see the conversation settle after the second answer, so that conversation's latest turn put one run's
// reply on the other's to-do (#11553). Only a run in a conversation of its own, where nothing else goes and a run is
// skipped rather than stacked on the last one, still falls back to the latest turn when its prompt is not on the page.
func (s *Service) scheduledRunReply(ctx context.Context, task Task, run ScheduleRun) string {
	items, ok := s.taskStateItems(ctx, run.MachineID, run.SessionID)
	if !ok {
		return ""
	}
	if reply, found := runReply(items, task.stateRunKey); found || run.Conversation == "source" {
		return reply
	}
	_, last := splitLastTurn(items)
	_, reply := turnExchange(last)
	return reply
}

// runReply is the reply in items to the prompt the hub sent for the run whose key is key (runKeyIn): the last reply
// after that prompt, up to the next message that opened a turn of its own. found says whether that prompt is in items;
// with it, reply is "" when nothing answered it, or when its turn was not its own: the prompt was queued into another
// turn, or another message was queued into its turn, and a reply after that answers both.
func runReply(items []proto.Item, key string) (reply string, found bool) {
	if key == "" {
		return "", false
	}
	i := len(items) - 1
	for i >= 0 && runKeyIn(items[i]) != key {
		i--
	}
	if i < 0 {
		return "", false
	}
	if items[i].Queued {
		return "", true
	}
	for _, it := range items[i+1:] {
		if it.Kind != proto.KindText || strings.TrimSpace(it.Text) == "" {
			continue
		}
		switch it.Role {
		case "user":
			if it.Queued {
				return "", true
			}
			return reply, true
		case "assistant":
			reply = strings.TrimSpace(it.Text)
		}
	}
	return reply, true
}

func (s *Service) finishScheduledTodo(ctx context.Context, task Task, run ScheduleRun) {
	if task.TodoID == "" || task.Repeat == repeatOnce || run.ID == "" {
		return
	}
	var after scheduleTodoSnapshot
	var active bool
	if run.Status != runFailed {
		after, active = s.scheduledTodoSnapshot(ctx, task, run)
	}
	reason := run.Error
	if reason == "" {
		reason = s.scheduledRunReply(ctx, task, run)
	}
	reason = clip(oneLine(strings.ReplaceAll(reason, "\x00", "")), 500)
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return
	}
	i := sc.indexLocked(task.ID)
	j := slices.IndexFunc(sc.runs, func(r ScheduleRun) bool { return r.ID == run.ID })
	if i < 0 || sc.tasks[i].TodoID != task.TodoID || sc.tasks[i].Repeat == repeatOnce {
		sc.mu.Unlock()
		return
	}
	evicted := j < 0
	if evicted {
		// The watcher still owns the original identity even after unrelated runs evicted its row.
		sc.runs = append(sc.runs, run)
		if len(sc.runs) > maxRuns {
			sc.runs = slices.Clone(sc.runs[len(sc.runs)-maxRuns:])
		}
		j = len(sc.runs) - 1
	}
	cur := sc.tasks[i]
	failed := run.Status == runFailed || sc.runs[j].Status == runFailed
	if failed {
		sc.runs[j].Status = runFailed
		sc.runs[j].TodoProgress = &ScheduleTodoAssessment{TodoID: task.TodoID, State: "not_run", Reason: reason}
	} else if old := cur.TodoProgress; old != nil && old.Run == run.ID && old.Assessment.State != "" {
		// Recover a history write that failed after the schedule's assessment was durable.
		result := old.Assessment
		sc.runs[j].TodoProgress = &result
	}
	if sc.runs[j].TodoProgress == nil || sc.runs[j].TodoProgress.State == "pending" || sc.runs[j].TodoProgress.State == "error" {
		result := ScheduleTodoAssessment{TodoID: task.TodoID, State: "no_progress", Reason: reason}
		progress := scheduleTodoProgress{Run: run.ID, At: run.At, Snapshot: after}
		before := task.todoBefore
		// The first run since the link only sets the baseline: compared with what was there just before it was sent,
		// it would judge only its own few seconds, and an Agent at work rarely writes in those (#11587).
		first := cur.TodoProgress == nil
		var since time.Time // when the baseline's evidence was read: tool calls after it are new
		if old := cur.TodoProgress; old != nil {
			oldIndex := slices.IndexFunc(sc.runs, func(r ScheduleRun) bool { return r.ID == old.Run })
			if old.At.After(run.At) || old.Run != run.ID && old.At.Equal(run.At) &&
				(evicted || oldIndex < 0 || j < oldIndex) {
				result.State = "superseded"
			} else {
				progress.NoProgress, progress.Signaled, progress.Escalated = old.NoProgress, old.Signaled, old.Escalated
				progress.Since, progress.Calls = old.Since, old.Calls
				since = scheduleSince(old.Snapshot, old.At)
				progress.EscalationRun = old.EscalationRun
				if progress.EscalationRun == "" && old.NoProgress >= 2 {
					progress.EscalationRun = old.Run // recover a partial write made before this field existed
				}
				baseline := old.Snapshot
				legacyUnknown := old.Assessment.State == "unknown" && len(baseline.Unreadable) == 0
				if before != nil && (len(baseline.Unreadable) > 0 || legacyUnknown) {
					// Known evidence keeps its older baseline. A first readable answer is only a baseline
					// when it was already present before dispatch, not proof that this run advanced it.
					baseline.Answers = maps.Clone(old.Snapshot.Answers)
					if baseline.Answers == nil {
						baseline.Answers = map[string]string{}
					}
					for agent, answer := range before.Answers {
						if baseline.Answers[agent] == "" && (baseline.Unreadable[agent] || legacyUnknown) {
							baseline.Answers[agent] = answer
						}
					}
				}
				before = &baseline
			}
		}
		lang, loc := s.lang(ctx), s.location(ctx)
		var work scheduleWork
		if !first && before != nil {
			work = scheduledTodoWork(*before, after, since)
		}
		restart := func() { // progress, or nothing to judge: the streak starts over
			progress.NoProgress, progress.Signaled, progress.Escalated = 0, false, false
			progress.EscalationRun, progress.Since, progress.Calls = "", time.Time{}, 0
		}
		switch {
		case result.State == "superseded":
		case !active || !cur.Enabled:
			result.State = "inactive"
			restart()
		case first:
			result.State, result.Reason = "baseline", i18n.T(lang, "assistant-schedule.todo_baseline")
			restart()
		case before == nil:
			result.State = "unknown"
		case scheduledTodoAdvanced(*before, after):
			result.State = "progress"
			restart()
		case work.work > 0:
			// Tool calls that are neither waits nor repeats: the Agent works, though it has not written yet.
			result.State, result.Reason = "working", scheduleWorkingReason(lang, loc, since, run.At, work)
			restart()
		case before.Unknown || after.Unknown || task.todoBefore != nil && task.todoBefore.Unknown:
			result.State = "unknown"
		default:
			if progress.NoProgress == 0 || progress.Since.IsZero() {
				progress.Since = cmp.Or(since, run.At)
			}
			if progress.NoProgress == 0 {
				progress.Calls = 0
			}
			progress.NoProgress++
			progress.Calls += work.calls
			// What was seen, not the run's own summary, which often says the work goes on fine.
			result.Reason = scheduleEvidence(lang, loc, progress.Since, run.At, progress.Calls)
		}
		if result.State == "no_progress" && progress.NoProgress >= 2 && progress.EscalationRun == "" {
			progress.EscalationRun = run.ID
		}
		if before != nil {
			progress.Snapshot = retainTodoEvidence(*before, after)
		}
		result.NoProgress = progress.NoProgress
		if result.Reason == "" {
			result.Reason = i18n.T(s.lang(ctx), "assistant-schedule.todo_no_evidence")
		}
		if result.State == "unknown" {
			result.Reason = i18n.T(s.lang(ctx), "assistant-schedule.todo_unknown", i18n.Args{"reason": result.Reason})
		}
		progress.Assessment = result
		tasks := slices.Clone(sc.tasks)
		if result.State != "superseded" {
			tasks[i].TodoProgress = &progress
		}
		if err = progress.Snapshot.validate(); err == nil {
			err = s.saveLocked(sc, tasks)
		}
		if err != nil {
			sc.mu.Unlock()
			s.scheduledTodoSaveFailed(ctx, task, run, err, nil)
			return
		}
		sc.runs[j].TodoProgress = &result
	}
	result := *sc.runs[j].TodoProgress
	if st := s.store(); st != nil {
		err = st.PutSetting(ctx, runsKey, sc.runs)
	}
	cur = sc.tasks[i]
	sc.mu.Unlock()
	if err != nil {
		s.scheduledTodoSaveFailed(ctx, task, run, err, &result)
		// The task's assessment is durable even if its history write failed.
	}
	s.broadcastSchedule()
	if result.State != "no_progress" || !cur.Enabled || cur.TodoProgress == nil || cur.TodoProgress.Run != run.ID {
		return
	}
	s.escalateScheduledTodo(ctx, cur, run, result)
}

func (s *Service) scheduledTodoSaveFailed(ctx context.Context, task Task, run ScheduleRun, cause error, committed *ScheduleTodoAssessment) {
	s.logf("task %s run %s: save to-do progress: %v", task.ID, run.ID, cause)
	detail := i18n.T(s.lang(ctx), "assistant-schedule.todo_save_failed", i18n.Args{
		"name": task.labelIn(s.lang(ctx)), "error": strings.ReplaceAll(cause.Error(), "\x00", ""),
	})
	if sc, err := s.lockedSchedule(ctx); err == nil {
		if j := slices.IndexFunc(sc.runs, func(r ScheduleRun) bool { return r.ID == run.ID }); j >= 0 {
			result := ScheduleTodoAssessment{TodoID: task.TodoID, State: "error", Reason: detail}
			if committed != nil {
				result = *committed
			}
			sc.runs[j].TodoProgress = &result
			if st := s.store(); st != nil {
				if err := st.PutSetting(ctx, runsKey, sc.runs); err != nil {
					s.logf("task %s run %s: save progress error: %v", task.ID, run.ID, err)
				}
			}
		}
		sc.mu.Unlock()
	}
	s.broadcastSchedule()
	// A failed write must reach supervision; a committed assessment keeps its evidence and count. It is the hub's
	// check that failed, about the task's Agent, not the conversation the run went to (#11587).
	g := superviseSignal{Todo: task.TodoID, Kind: sigCheck, Report: checkErrorReport,
		Key: run.ID + ":progress_error", At: run.At, Detail: detail + "\n" + run.ID}
	if t, ok := s.todoNow(task.TodoID); ok {
		if ag := scheduleAgent(t); ag.SessionID != "" {
			g.Machine, g.Session = ag.MachineID, ag.SessionID
		}
	}
	s.signal(g)
}

func (s *Service) escalateScheduledTodo(ctx context.Context, task Task, run ScheduleRun, result ScheduleTodoAssessment) {
	t, ok := s.todoNow(task.TodoID)
	t = s.withBrief(t)
	if !ok || !scheduledTodoOpen(t) {
		return
	}
	progress := *task.TodoProgress
	if result.NoProgress >= 2 && progress.EscalationRun == "" {
		progress.EscalationRun = run.ID // replay a completed assessment from an older hub
	}
	lang := s.lang(ctx)
	detail := i18n.T(lang, "assistant-schedule.todo_stalled", i18n.Args{
		"name": task.labelIn(lang), "count": result.NoProgress, "reason": result.Reason,
	})
	// How long nothing new came: from the streak's baseline to this run's own read of the evidence. A run sent while
	// the one before it was still being read (each in a new chat) starts before that baseline was read.
	quiet := time.Duration(0)
	if seen := cmp.Or(progress.Snapshot.At, run.At); !progress.Since.IsZero() && seen.After(progress.Since) {
		quiet = seen.Sub(progress.Since)
	}
	if !progress.Signaled {
		// The hub's check, about the task's Agent: its computer and session, not those of the conversation the run
		// went to, and never worded as the Agent's own report (#11587).
		g := superviseSignal{Todo: t.ID, Kind: sigCheck, Report: checkNoProgressReport, Key: run.ID, At: run.At,
			Detail: detail + "\n" + run.ID, Minutes: int(quiet.Minutes())}
		if ag := scheduleAgent(t); ag.SessionID != "" {
			g.Machine, g.Session = ag.MachineID, ag.SessionID
		}
		s.signal(g)
		progress.Signaled = true
	}
	// A committed review is the outcome; its marker may still need saving after a partial write or restart.
	if result.NoProgress >= 2 && !progress.Escalated && t.Supervision.State == reviewNeedsUser {
		if n := len(t.Supervision.Reviews); n > 0 {
			review := t.Supervision.Reviews[n-1]
			progress.Escalated = review.Verdict == reviewNeedsUser && strings.HasSuffix(review.Note, "\n"+progress.EscalationRun)
		}
	}
	// Never overwrite another question already waiting for the owner, and ask only once the task went without progress
	// for as long as the no_progress signal waits (scheduleAskAfter): two short windows are no reason on a long task.
	due := quiet > 0 && quiet >= s.scheduleAskAfter(t)
	if result.NoProgress >= 2 && due && !progress.Escalated && t.Supervision.State != reviewNeedsUser {
		change := i18n.T(lang, "assistant-schedule.todo_change")
		wait := i18n.T(lang, "assistant-schedule.todo_wait")
		args, _ := json.Marshal(map[string]any{
			"todo_id": t.ID, "verdict": reviewNeedsUser, "why_owner": whyPriority,
			"note":      detail + "\n" + progress.EscalationRun,
			"reason":    i18n.T(lang, "assistant-schedule.todo_decide", i18n.Args{"reason": result.Reason}),
			"recommend": change, "cost_of_waiting": i18n.T(lang, "assistant-schedule.todo_wait_cost"),
			"options": []DecisionOption{
				{Label: change, LeadsTo: i18n.T(lang, "assistant-schedule.todo_change_result")},
				{Label: wait, LeadsTo: i18n.T(lang, "assistant-schedule.todo_wait_result")},
			},
			// Revisit the decision, never silently change the owner's approved strategy.
			"deadline": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339), "reversible": false,
		})
		supervisor := t.Supervision.Supervisor
		if s.agentSupervised(t) {
			// A worker's Agent reference is not a conversation; use the authority above its leader.
			supervisor, _ = s.supervisorAboveTask(ctx, t)
		} else if supervisor == "" {
			supervisor = t.Origin
		}
		// Marked as the check's: the owner's answer goes to the supervisor, not the Agent at work (checkDecided).
		if _, err := s.toolTaskReview(withScheduledCheck(ctx, task.ID), supervisor, args); err != nil {
			s.logf("task %s: no-progress decision: %v", task.ID, err)
		} else {
			progress.Escalated = true
		}
	}
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return
	}
	defer sc.mu.Unlock()
	if i := sc.indexLocked(task.ID); i >= 0 && sc.tasks[i].TodoProgress != nil && sc.tasks[i].TodoProgress.Run == run.ID {
		tasks := slices.Clone(sc.tasks)
		tasks[i].TodoProgress = &progress
		if err := s.saveLocked(sc, tasks); err != nil {
			s.logf("task %s: save no-progress escalation: %v", task.ID, err)
		}
	}
}
