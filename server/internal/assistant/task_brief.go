package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Task briefs (#3822, docs/assistant/unified-entry.md "Briefs"). A brief is one record per piece of work, made when the
// work is handed out and never rewritten: the owner's message that led to it, verbatim (or who started it when it was
// not the owner, and why), the goal in a sentence, what done means, how long it should take and what is out of bounds.
// A task's notes are the prompt its agent got, and a take-over's notes are the hand-over prompt; the brief is what all
// of them are held to. To-dos that carry the same work on (a take-over, a retry on another computer, a rework round, a
// task split off another) link to the same brief, and their links in order are its lineage.
//
// What happens to a brief afterwards is its log: a scope change (task_scope, task_brief_scope.go), the overdue mark
// (the clock, task_brief_scope.go) and the per-criterion result when the work is accepted (task_brief_result.go). The
// criteria in effect are the brief's as its scope changes left them; a supervised task's ledger carries the same list
// (Supervision.DoneWhen), which the done judge and task_review read.

// Who started a piece of work (TaskBrief.StartedBy).
const (
	briefByOwner     = "owner"     // the owner asked for it: Request is their message
	briefByAssistant = "assistant" // the assistant on its own (after a supervision message, a notice, a follow-up)
	briefByLeader    = "leader"    // a leader conversation, not on the owner's message
	briefByRoutine   = "routine"   // a scheduled routine's run
	briefByHub       = "hub"       // the hub itself (an agent that became a task, an inbox hand-off)
	briefByUnknown   = "unknown"   // the conversation could not be read, or nothing says (a reconstructed brief)
)

const (
	// briefRequestMax is how much of the owner's message a brief keeps; the rest is behind Request's pointer.
	briefRequestMax = 2000
	// briefHandoverMax is how much of the hand-over prompt a reconstructed brief keeps.
	briefHandoverMax = 600
	// briefOriginTurns is how many turns back the conversation is read for the message that led to the hand-out.
	briefOriginTurns = 3
	// briefLogMax is the most scope changes a brief takes: past it the work is another piece of work (task_scope says so).
	briefLogMax = 40
)

// Automatic estimates are attached only to a task_plan dispatch, never to manual Agent work (#11685).
const (
	defaultTaskEstimateMinutes = 60
	briefEstimateSystemDefault = "system_default"
	briefEstimateOverride      = "override"
)

// automaticEstimate resolves one task_plan task's estimate while preserving its source across plan serialization.
// Clearing an override restores the default; changing the default's value makes it an explicit override.
func automaticEstimate(minutes int, source string) (int, string) {
	if minutes <= 0 {
		return defaultTaskEstimateMinutes, briefEstimateSystemDefault
	}
	if source == "" || source == briefEstimateSystemDefault && minutes != defaultTaskEstimateMinutes {
		source = briefEstimateOverride
	}
	return minutes, source
}

// Kinds of brief log entries.
const (
	briefEventScope       = "scope"
	briefEventOverdue     = "overdue"
	briefEventDispatch    = "dispatch"
	briefEventCompletion  = "completion"
	briefEventEstimateDue = "estimate_due"
)

// BriefRequest is the owner's message that led to a task: its words as they wrote them (cut at briefRequestMax, then
// Truncated), when, and where the whole of it is (the conversation and the message's id in its transcript).
type BriefRequest struct {
	Text         string    `json:"text"`
	Truncated    bool      `json:"truncated,omitempty"`
	Part         bool      `json:"part,omitempty"` // a quoted passage from the source message
	At           time.Time `json:"at"`
	Conversation string    `json:"conversation,omitempty"`
	MessageID    string    `json:"message_id,omitempty"`
	// Via: the conversations that passed it on with conversation_send / conversation_new before the work was handed
	// out (the first is where the owner wrote it), oldest first; empty when the owner spoke in the conversation that
	// handed it out (relay_quote.go, #3822). Relayed words are the owner's request, never their approval of a step.
	Via []string `json:"via,omitempty"`
}

// BriefEvent is one entry of a brief's log. A scope change says what changed and the criteria/estimate it left;
// dispatch starts one automatic task_plan round; completion records that round's first active completion and actual
// minutes; estimate_due and overdue record accepted supervisor delivery. Approval belongs only to a risky scope change.
type BriefEvent struct {
	At               time.Time             `json:"at"`
	Kind             string                `json:"kind"`
	Todo             string                `json:"todo_id,omitempty"`
	By               string                `json:"by,omitempty"` // the conversation that changed it
	Change           string                `json:"change,omitempty"`
	Why              string                `json:"why,omitempty"`
	DoneWhen         []string              `json:"done_when,omitempty"`
	ExpectMinutes    int                   `json:"expect_minutes,omitempty"`
	EstimateSource   string                `json:"estimate_source,omitempty"`
	EstimateEvidence *TaskEstimateEvidence `json:"estimate_evidence,omitempty"`
	// Dispatch records one task_plan round (source is task_plan; round_id names it); completion records its first
	// active completion report (source is report_to_supervisor, task_review or judge) and wall-clock actual_minutes.
	Source        string        `json:"source,omitempty"`
	PlanID        string        `json:"plan_id,omitempty"`
	RoundID       string        `json:"round_id,omitempty"`
	ActualMinutes int           `json:"actual_minutes,omitempty"`
	Added         []string      `json:"added,omitempty"`
	Dropped       []string      `json:"dropped,omitempty"`
	Approval      *BriefRequest `json:"approval,omitempty"`
}

// BriefCriterion is one criterion's result when the work ended: met, not_met, unchecked (nobody checked it), or dropped
// by a scope change (Change says which, ChangedAt when).
type BriefCriterion struct {
	Criterion string     `json:"criterion"`
	State     string     `json:"state"`
	Evidence  string     `json:"evidence,omitempty"`
	Change    string     `json:"change,omitempty"`
	ChangedAt *time.Time `json:"changed_at,omitempty"`
}

const (
	criterionMet       = "met"
	criterionNotMet    = "not_met"
	criterionUnchecked = "unchecked"
	criterionDropped   = "dropped"
)

// BriefResult is how the work ended against its brief: each criterion, then what was done beyond it.
type BriefResult struct {
	At       time.Time        `json:"at"`
	Todo     string           `json:"todo_id,omitempty"`
	By       string           `json:"by,omitempty"` // "judge": the done judge's own acceptance; "" the supervisor's review
	Verdict  string           `json:"verdict,omitempty"`
	Criteria []BriefCriterion `json:"criteria"`
	AlsoDone []string         `json:"also_done,omitempty"`
}

// briefRecord is the part of a brief written once (the brief column): nothing changes it afterwards.
type briefRecord struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	// Request: the owner's message, nil when the work was not started on one (StartedBy says by whom) or it could not
	// be read. Trigger is what the turn that handed the work out followed instead (supervision, followup, routine,
	// conversation, notice, meeting, wake), TriggerTitle its one line; Reason is why, as whoever started it said.
	Request          *BriefRequest         `json:"request,omitempty"`
	StartedBy        string                `json:"started_by"`
	Trigger          string                `json:"trigger,omitempty"`
	TriggerTitle     string                `json:"trigger_title,omitempty"`
	Reason           string                `json:"reason,omitempty"`
	Conversation     string                `json:"conversation,omitempty"` // where it was handed out
	Goal             string                `json:"goal"`
	DoneWhen         []string              `json:"done_when"`
	ExpectMinutes    int                   `json:"expect_minutes,omitempty"`
	EstimateSource   string                `json:"estimate_source,omitempty"`
	EstimateEvidence *TaskEstimateEvidence `json:"estimate_evidence,omitempty"`
	OutOfBounds      []string              `json:"out_of_bounds,omitempty"`
	// Reconstructed: made later from what the to-do still had (a task from before briefs): Handover is the prompt its
	// agent was handed, which is not the owner's words, and nothing else was invented.
	Reconstructed bool   `json:"reconstructed,omitempty"`
	Handover      string `json:"handover,omitempty"`
}

// briefState is what happened to a brief since (the state column).
type briefState struct {
	Log    []BriefEvent `json:"log,omitempty"`
	Result *BriefResult `json:"result,omitempty"`
}

// TaskBrief is a brief as the apps get it on a to-do (brief) and on a plan's task before it starts: the record, its
// log, and what is in effect now.
type TaskBrief struct {
	briefRecord
	Log    []BriefEvent `json:"log"`
	Result *BriefResult `json:"result,omitempty"`
	// Criteria and ExpectNow are done_when and expect_minutes as the scope changes left them; ClockAt is when its
	// clock last started (the hand-out, or the latest scope change) and DueAt when it runs out; Overdue: it ran out
	// and the supervisor was told.
	Criteria  []string `json:"criteria"`
	ExpectNow int      `json:"expect_minutes_now,omitempty"`
	// EstimateSourceNow, EstimateClockAt and EstimateDueAt describe the automatic review clock, not the older
	// overdue clock below. Empty for manual or pre-feature work until a task_plan dispatch makes it eligible.
	EstimateSourceNow   string                `json:"estimate_source_now,omitempty"`
	EstimateEvidenceNow *TaskEstimateEvidence `json:"estimate_evidence_now,omitempty"`
	EstimateClockAt     time.Time             `json:"estimate_clock_at,omitzero"`
	EstimateDueAt       time.Time             `json:"estimate_due_at,omitzero"`
	EstimateDueToldAt   *time.Time            `json:"estimate_due_told_at,omitempty"`
	ActualMinutes       int                   `json:"actual_minutes,omitempty"`
	CompletionReported  *time.Time            `json:"completion_reported_at,omitempty"`
	ClockAt             time.Time             `json:"clock_at"`
	DueAt               time.Time             `json:"due_at"`
	Overdue             *time.Time            `json:"overdue_at,omitempty"`
}

// LineageStep is one to-do of a brief's lineage: who worked on it, from when to when, and why it stopped ("" while it
// goes on). Continues is the to-do it took the work over from.
type LineageStep struct {
	TodoID    string     `json:"todo_id"`
	Title     string     `json:"title,omitempty"`
	Agent     string     `json:"agent,omitempty"`
	Machine   string     `json:"machine,omitempty"`
	Session   string     `json:"session_id,omitempty"`
	Started   time.Time  `json:"started"`
	Ended     *time.Time `json:"ended,omitempty"`
	Why       string     `json:"why,omitempty"`
	Continues string     `json:"continues,omitempty"`
	// SupersededBy: the to-do that took its work over (Why is then the reason that one gave); it may still be open,
	// when the other was split off it.
	SupersededBy string `json:"superseded_by,omitempty"`
	Current      bool   `json:"current,omitempty"` // the to-do this lineage was asked for
}

type taskBrief struct {
	rec briefRecord
	st  briefState
}

type taskBriefLink struct {
	Brief, Continues, Reason string
	At                       time.Time
}

// taskBriefBook holds the briefs and each to-do's link to one. Its lock is never held across a call into the to-dos.
type taskBriefBook struct {
	mu     sync.Mutex
	briefs map[string]*taskBrief
	links  map[string]taskBriefLink // by to-do id
	// digestAt: when a briefing last told the briefs, without a store (task_brief_digest.go).
	digestAt time.Time
}

func (b *taskBriefBook) init() {
	if b.briefs == nil {
		b.briefs, b.links = map[string]*taskBrief{}, map[string]taskBriefLink{}
	}
}

// taskBriefStore is what the briefs keep in the database; a store without it keeps them in memory only.
type taskBriefStore interface {
	ListTaskBriefs(ctx context.Context) ([]db.TaskBriefRow, []db.TaskBriefTodoRow, error)
	CreateTaskBrief(ctx context.Context, r db.TaskBriefRow) (bool, error)
	PutTaskBriefState(ctx context.Context, id string, state json.RawMessage, at time.Time) (bool, error)
	LinkTaskBriefTodo(ctx context.Context, l db.TaskBriefTodoRow) (bool, error)
}

var _ taskBriefStore = (*db.DB)(nil)

func (s *Service) taskBriefDB() taskBriefStore {
	if s.todoDB() == nil {
		return nil
	}
	st, _ := s.store().(taskBriefStore)
	return st
}

// loadTaskBriefs reads the briefs with the to-dos (loadTodosLocked); a store that keeps none has none.
func (s *Service) loadTaskBriefs(ctx context.Context) error {
	st := s.taskBriefDB()
	if st == nil {
		return nil
	}
	rows, links, err := st.ListTaskBriefs(ctx)
	if err != nil {
		return err
	}
	b := &s.tbriefs
	b.mu.Lock()
	defer b.mu.Unlock()
	b.briefs, b.links = map[string]*taskBrief{}, map[string]taskBriefLink{}
	for _, r := range rows {
		tb := &taskBrief{}
		if json.Unmarshal(r.Brief, &tb.rec) != nil {
			continue
		}
		_ = json.Unmarshal(r.State, &tb.st)
		tb.rec.ID = r.ID
		b.briefs[r.ID] = tb
	}
	for _, l := range links {
		if b.briefs[l.BriefID] != nil {
			b.links[l.TodoID] = taskBriefLink{Brief: l.BriefID, Continues: l.Continues, Reason: l.Reason, At: l.LinkedAt}
		}
	}
	return nil
}

// ---- what is in effect ----

// criteria are the brief's done_when as its scope changes left them.
func (tb *taskBrief) criteria() []string {
	out := tb.rec.DoneWhen
	for _, e := range tb.st.Log {
		if e.Kind == briefEventScope && e.DoneWhen != nil {
			out = e.DoneWhen
		}
	}
	return slices.Clone(out)
}

// expect is the brief's expect_minutes as its dispatches and scope changes left it.
func (tb *taskBrief) expect() int {
	out := tb.rec.ExpectMinutes
	for _, e := range tb.st.Log {
		if (e.Kind == briefEventScope || e.Kind == briefEventDispatch) && e.ExpectMinutes > 0 {
			out = e.ExpectMinutes
		}
	}
	return out
}

// estimateState is the automatic review clock as task_plan dispatches and actual estimate revisions left it. A
// criteria-only scope change deliberately does not restart this clock; it still restarts the older overdue clock.
func (tb *taskBrief) estimateState() (expect int, source string, clock time.Time) {
	source, clock = tb.rec.EstimateSource, tb.rec.CreatedAt
	for _, e := range tb.st.Log {
		if e.Kind != briefEventDispatch && (e.Kind != briefEventScope || e.ExpectMinutes <= 0) {
			continue
		}
		clock = e.At
		if e.EstimateSource != "" {
			source = e.EstimateSource
		}
	}
	return tb.expect(), source, clock
}

// estimateEvidence is the evidence of the active dispatch estimate. A scope
// change can replace the estimate without inheriting its old samples.
func (tb *taskBrief) estimateEvidence() *TaskEstimateEvidence {
	evidence := tb.rec.EstimateEvidence
	for _, event := range tb.st.Log {
		if event.Kind == briefEventDispatch || event.Kind == briefEventScope && event.ExpectMinutes > 0 {
			evidence = event.EstimateEvidence
		}
	}
	if evidence != nil {
		copy := *evidence
		return &copy
	}
	return nil
}

// estimateDueToldAt is when a supervision message carried the current estimate_due, nil before that or after a new
// dispatch / estimate revision. The event is written only after delivery or durable queue admission.
func (tb *taskBrief) estimateDueToldAt() *time.Time {
	var told *time.Time
	for _, e := range tb.st.Log {
		if e.Kind == briefEventDispatch || e.Kind == briefEventScope && e.ExpectMinutes > 0 {
			told = nil
			continue
		}
		if e.Kind == briefEventEstimateDue {
			at := e.At
			told = &at
		}
	}
	return told
}

// roundFor names the current task_plan round on t. A manual reopen starts a new measurement from ReopenedAt;
// a task_plan continuation has a later dispatch event and therefore already has a new round.
func (tb *taskBrief) roundFor(t Todo) (id string, start time.Time, ok bool) {
	for i := len(tb.st.Log) - 1; i >= 0; i-- {
		e := tb.st.Log[i]
		if e.Kind != briefEventDispatch || e.Todo != t.ID || e.RoundID == "" {
			continue
		}
		id, start = e.RoundID, e.At
		break
	}
	if id == "" {
		return "", time.Time{}, false
	}
	if t.ReopenedAt != nil && t.ReopenedAt.After(start) {
		start = t.ReopenedAt.UTC()
		id += "@reopened:" + start.Format(time.RFC3339Nano)
	}
	return id, start, true
}

// completionFor is this round's first active completion report, nil when it has none.
func (tb *taskBrief) completionFor(roundID, todoID string) *BriefEvent {
	for i := len(tb.st.Log) - 1; i >= 0; i-- {
		e := &tb.st.Log[i]
		if e.Kind == briefEventCompletion && e.RoundID == roundID && e.Todo == todoID {
			return e
		}
	}
	return nil
}

// latestCompletion is the brief's newest active completion report, used only as a summary; every round remains in Log.
func (tb *taskBrief) latestCompletion() *BriefEvent {
	for i := len(tb.st.Log) - 1; i >= 0; i-- {
		if tb.st.Log[i].Kind == briefEventCompletion {
			return &tb.st.Log[i]
		}
	}
	return nil
}

// clockAt is when the brief's clock last started: when the work was handed out, or its latest scope change.
func (tb *taskBrief) clockAt() time.Time {
	at := tb.rec.CreatedAt
	for _, e := range tb.st.Log {
		if e.Kind == briefEventScope {
			at = e.At
		}
	}
	return at
}

// briefClockMin is the least time a brief's clock runs, whatever its task should take.
const briefClockMin = 24 * time.Hour

// dueAt is when the clock runs out: three times what the work should take after it started, and at least a day.
func (tb *taskBrief) dueAt() time.Time {
	return tb.clockAt().Add(max(time.Duration(tb.expect())*3*time.Minute, briefClockMin))
}

// overdueAt is when the supervisor was told the clock ran out, nil when it was not for the clock running now: a
// scope change logged after the mark started another clock.
func (tb *taskBrief) overdueAt() *time.Time {
	for i := len(tb.st.Log) - 1; i >= 0; i-- {
		switch e := tb.st.Log[i]; e.Kind {
		case briefEventScope:
			return nil
		case briefEventOverdue:
			at := e.At
			return &at
		}
	}
	return nil
}

// dropped are the criteria scope changes took out and never put back, each with the change that dropped it.
func (tb *taskBrief) dropped() []BriefCriterion {
	now := tb.criteria()
	var out []BriefCriterion
	for _, e := range tb.st.Log {
		if e.Kind != briefEventScope {
			continue
		}
		for _, c := range e.Dropped {
			if slices.Contains(now, c) || slices.ContainsFunc(out, func(x BriefCriterion) bool { return x.Criterion == c }) {
				continue
			}
			at := e.At
			out = append(out, BriefCriterion{Criterion: c, State: criterionDropped, Change: e.Change, ChangedAt: &at})
		}
	}
	return out
}

func (tb *taskBrief) view() TaskBrief {
	_, source, clock := tb.estimateState()
	v := TaskBrief{briefRecord: tb.rec, Log: slices.Clone(tb.st.Log), Criteria: tb.criteria(), ExpectNow: tb.expect(),
		EstimateSourceNow: source, EstimateEvidenceNow: tb.estimateEvidence(), ClockAt: tb.clockAt(), DueAt: tb.dueAt(), Overdue: tb.overdueAt()}
	if source != "" && v.ExpectNow > 0 {
		v.EstimateClockAt, v.EstimateDueAt = clock, clock.Add(time.Duration(v.ExpectNow)*time.Minute)
		v.EstimateDueToldAt = tb.estimateDueToldAt()
	}
	if c := tb.latestCompletion(); c != nil {
		at := c.At
		v.ActualMinutes, v.CompletionReported = c.ActualMinutes, &at
	}
	v.DoneWhen = slices.Clone(tb.rec.DoneWhen)
	if v.DoneWhen == nil {
		v.DoneWhen = []string{}
	}
	if v.Criteria == nil {
		v.Criteria = []string{}
	}
	if v.Log == nil {
		v.Log = []BriefEvent{}
	}
	if tb.st.Result != nil {
		r := *tb.st.Result
		r.Criteria = slices.Clone(r.Criteria)
		v.Result = &r
	}
	return v
}

// ---- reading ----

// continuationTodos is the to-do and its actual continues ancestors, oldest first. Only the parent pointer is
// followed: another to-do on the same brief is not thereby an ancestor. A missing, cross-brief or already visited
// parent stops the walk, so a damaged link cannot import unrelated work or loop.
func (s *Service) continuationTodos(id string) []string {
	b := &s.tbriefs
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	out := []string{id}
	link, ok := b.links[id]
	if !ok || link.Brief == "" {
		return out
	}
	brief := link.Brief
	seen := map[string]bool{id: true}
	for link.Continues != "" {
		parent := link.Continues
		link, ok = b.links[parent]
		if !ok || link.Brief != brief || seen[parent] {
			break
		}
		out = append(out, parent)
		seen[parent] = true
	}
	slices.Reverse(out)
	return out
}

// briefOfTodo is the brief to-do id works for, false when it has none.
func (s *Service) briefOfTodo(id string) (TaskBrief, bool) {
	b := &s.tbriefs
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	tb := b.briefs[b.links[id].Brief]
	if tb == nil {
		return TaskBrief{}, false
	}
	return tb.view(), true
}

// briefTodos are the to-dos on brief id, in the order they joined it.
func (s *Service) briefTodos(id string) []string {
	b := &s.tbriefs
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	var out []string
	for todo, l := range b.links {
		if l.Brief == id {
			out = append(out, todo)
		}
	}
	slices.SortFunc(out, func(x, y string) int {
		if c := b.links[x].At.Compare(b.links[y].At); c != 0 {
			return c
		}
		return strings.Compare(x, y)
	})
	return out
}

// briefTip is the to-do that carries brief id's work now: the one that joined it last (a take-over supersedes the
// to-dos before it, whether or not they were closed); "" when none is left.
func (s *Service) briefTip(id string) string {
	tip := ""
	for _, todo := range s.briefTodos(id) {
		if _, ok := s.todoNow(todo); ok {
			tip = todo
		}
	}
	return tip
}

// taskLineage is the chain of to-dos that carried to-do id's work (its brief's to-dos, oldest first): who worked on
// each, from when to when, and why it stopped. A to-do with no brief, or alone on its brief with nothing to tell, has
// its one step all the same.
func (s *Service) taskLineage(id string) []LineageStep {
	b := &s.tbriefs
	b.mu.Lock()
	b.init()
	brief := b.links[id].Brief
	links := map[string]taskBriefLink{}
	for todo, l := range b.links {
		if brief != "" && l.Brief == brief {
			links[todo] = l
		}
	}
	b.mu.Unlock()
	return s.lineageOf(id, links)
}

// briefLinks are the brief links read once for a whole list (GET /api/v1/todos): each to-do's brief, and the to-dos on
// each brief with their links. Reading them for every to-do would read every link n times (#5995).
type briefLinks struct {
	of map[string]string                   // brief id by to-do id
	on map[string]map[string]taskBriefLink // the links on a brief, by brief id and to-do id
}

func (s *Service) briefLinksNow() briefLinks {
	b := &s.tbriefs
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	out := briefLinks{of: make(map[string]string, len(b.links)), on: map[string]map[string]taskBriefLink{}}
	for todo, l := range b.links {
		out.of[todo] = l.Brief
		if l.Brief == "" {
			continue
		}
		on := out.on[l.Brief]
		if on == nil {
			on = map[string]taskBriefLink{}
			out.on[l.Brief] = on
		}
		on[todo] = l
	}
	return out
}

// lineageOf is taskLineage(id) with links the to-dos on id's brief (nil or empty: it has none). It only reads links.
func (s *Service) lineageOf(id string, links map[string]taskBriefLink) []LineageStep {
	if len(links) == 0 {
		return nil
	}
	ids := make([]string, 0, len(links))
	for todo := range links {
		ids = append(ids, todo)
	}
	slices.SortFunc(ids, func(x, y string) int {
		if c := links[x].At.Compare(links[y].At); c != 0 {
			return c
		}
		return strings.Compare(x, y)
	})
	// Why a to-do stopped: the reason the one that took over from it gave, else its own end.
	superseded, by := map[string]taskBriefLink{}, map[string]string{}
	for _, todo := range ids {
		if l := links[todo]; l.Continues != "" {
			if _, ok := superseded[l.Continues]; !ok {
				superseded[l.Continues], by[l.Continues] = l, todo
			}
		}
	}
	out := make([]LineageStep, 0, len(ids))
	for _, todo := range ids {
		step, ok := s.lineageStep(todo)
		if !ok {
			continue
		}
		step.Continues, step.Current = links[todo].Continues, todo == id
		if l, ok := superseded[todo]; ok {
			step.SupersededBy = by[todo]
			if l.Reason != "" {
				step.Why = l.Reason
			}
		}
		out = append(out, step)
	}
	return out
}

// lineageStep is to-do id's own part of its step in a lineage: read where it is kept, without copying the whole to-do.
func (s *Service) lineageStep(id string) (LineageStep, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t, ok := s.todos.todos[id]
	if !ok {
		return LineageStep{}, false
	}
	step := LineageStep{TodoID: id, Title: t.Title, Started: t.CreatedAt}
	var latest *TodoAgent
	for i := range t.Agents {
		if a := &t.Agents[i]; a.Source != "review" && (latest == nil || a.LinkedAt.After(latest.LinkedAt)) {
			latest = a
		}
	}
	if latest != nil {
		step.Agent, step.Machine, step.Session = latest.Agent, latest.MachineID, latest.SessionID
	}
	if t.DoneAt != nil {
		at := *t.DoneAt
		step.Ended = &at
		step.Why = firstNonEmpty(t.StartError, t.CancelledReason, t.DoneReason)
	}
	return step, true
}

// withBrief is to-do t as the apps get it with its brief and lineage (GET /api/v1/todos/{id}).
func (s *Service) withBrief(t Todo) Todo {
	if v, ok := s.briefOfTodo(t.ID); ok {
		t.Brief = &v
		t.Lineage = s.taskLineage(t.ID)
	}
	return t
}

// withBriefFrom is withBrief for one to-do of a list, its lineage read from links (briefLinksNow).
func (s *Service) withBriefFrom(t Todo, links briefLinks) Todo {
	if v, ok := s.briefOfTodo(t.ID); ok {
		t.Brief = &v
		t.Lineage = s.lineageOf(t.ID, links.on[links.of[t.ID]])
	}
	return t
}

// ---- making and linking ----

// putTaskBrief stores a new brief (its id and time filled in when empty) and puts to-do todoID on it.
func (s *Service) putTaskBrief(ctx context.Context, rec briefRecord, todoID string) (TaskBrief, error) {
	if rec.ID == "" {
		rec.ID = "brief_" + randomHex(8)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	rec.Goal = clip(oneLine(rec.Goal), 300)
	rec.Reason = clip(oneLine(rec.Reason), 300)
	rec.DoneWhen = cleanDoneWhen(rec.DoneWhen)
	rec.OutOfBounds = cleanDoneWhen(rec.OutOfBounds)
	if len(rec.OutOfBounds) == 0 {
		rec.OutOfBounds = nil
	}
	if rec.StartedBy == "" {
		rec.StartedBy = briefByUnknown
	}
	if st := s.taskBriefDB(); st != nil {
		raw, err := json.Marshal(rec)
		if err != nil {
			return TaskBrief{}, err
		}
		if _, err := st.CreateTaskBrief(ctx, db.TaskBriefRow{ID: rec.ID, Brief: raw, CreatedAt: rec.CreatedAt, UpdatedAt: rec.CreatedAt}); err != nil {
			return TaskBrief{}, err
		}
	}
	tb := &taskBrief{rec: rec}
	b := &s.tbriefs
	b.mu.Lock()
	b.init()
	if cur := b.briefs[rec.ID]; cur != nil {
		tb = cur // written once: one already there stays as it is
	} else {
		b.briefs[rec.ID] = tb
	}
	v := tb.view()
	b.mu.Unlock()
	if todoID != "" {
		if err := s.linkTaskBrief(ctx, rec.ID, todoID, "", ""); err != nil {
			return v, err
		}
	}
	return v, nil
}

// linkTaskBrief puts to-do todoID on brief id, continuing to-do continues for reason (a take-over); a to-do already on
// a brief stays on it.
func (s *Service) linkTaskBrief(ctx context.Context, id, todoID, continues, reason string) error {
	l := taskBriefLink{Brief: id, Continues: continues, Reason: clip(oneLine(reason), 300), At: time.Now().UTC()}
	b := &s.tbriefs
	b.mu.Lock()
	b.init()
	_, linked := b.links[todoID]
	known := b.briefs[id] != nil
	b.mu.Unlock()
	if linked || !known {
		return nil
	}
	if st := s.taskBriefDB(); st != nil {
		if _, err := st.LinkTaskBriefTodo(ctx, db.TaskBriefTodoRow{TodoID: todoID, BriefID: id, Continues: continues, Reason: l.Reason, LinkedAt: l.At}); err != nil {
			return err
		}
	}
	b.mu.Lock()
	if _, linked := b.links[todoID]; !linked {
		b.links[todoID] = l
	}
	b.mu.Unlock()
	return nil
}

// changeBrief changes brief id's state with change (which says whether it changed anything) and writes it; the brief
// as it is then. The record itself is never touched.
func (s *Service) changeBrief(ctx context.Context, id string, change func(tb *taskBrief) bool) (TaskBrief, bool, error) {
	b := &s.tbriefs
	b.mu.Lock()
	b.init()
	tb := b.briefs[id]
	if tb == nil {
		b.mu.Unlock()
		return TaskBrief{}, false, fmt.Errorf("no brief %q", id)
	}
	before := tb.st
	before.Log = slices.Clone(tb.st.Log)
	if !change(tb) {
		v := tb.view()
		b.mu.Unlock()
		return v, false, nil
	}
	raw, err := json.Marshal(tb.st)
	v := tb.view()
	b.mu.Unlock()
	if err == nil {
		if st := s.taskBriefDB(); st != nil {
			_, err = st.PutTaskBriefState(ctx, id, raw, time.Now().UTC())
		}
	}
	if err != nil {
		b.mu.Lock()
		tb.st = before
		b.mu.Unlock()
		return TaskBrief{}, false, err
	}
	return v, true, nil
}

// ensureTaskBrief is to-do t's brief, reconstructed now when it has none (a task from before briefs): its title as the
// goal, its notes as the prompt at hand-over (not the owner's words), when it was made, its conversation and its
// ledger's criteria. Nothing else is invented, and it is marked reconstructed.
func (s *Service) ensureTaskBrief(ctx context.Context, t Todo) (TaskBrief, error) {
	if v, ok := s.briefOfTodo(t.ID); ok {
		return v, nil
	}
	rec := briefRecord{CreatedAt: t.CreatedAt, StartedBy: briefByUnknown, Conversation: t.Origin, Goal: t.Title,
		Reconstructed: true, Handover: clipMiddle(strings.TrimSpace(t.Notes), briefHandoverMax/2)}
	if t.Supervision != nil {
		rec.DoneWhen, rec.ExpectMinutes = t.Supervision.DoneWhen, t.Supervision.ExpectMinutes
	}
	if _, err := s.putTaskBrief(ctx, rec, t.ID); err != nil {
		return TaskBrief{}, err
	}
	v, _ := s.briefOfTodo(t.ID) // the one it is on now: another call may have linked it first
	return v, nil
}

// backfillBriefs reconstructs the brief of every open task that has an agent and no brief (ensureTaskBrief), a few at
// a time: a task whose brief is being made with it has no agent linked yet, so it is never taken for one from before.
func (s *Service) backfillBriefs(ctx context.Context, tasks []Todo) {
	n := 0
	for _, t := range tasks {
		if t.DoneAt != nil || len(t.Agents) == 0 || n >= 20 {
			continue
		}
		if _, ok := s.briefOfTodo(t.ID); ok {
			continue
		}
		if _, err := s.ensureTaskBrief(ctx, t); err != nil {
			s.logf("task %s: reconstruct its brief: %v", t.ID, err)
		}
		n++
	}
}

// ---- who asked ----

// toolCallerKey carries the conversation a model's tool call came from (Tool), for what is made deep inside a tool to
// say who asked for it. The hub's own calls of a tool (runTool) carry none.
type toolCallerKey struct{}

type toolCaller struct{ machine, conversation string }

func withToolCaller(ctx context.Context, machine, conversation string) context.Context {
	return context.WithValue(ctx, toolCallerKey{}, toolCaller{machine, conversation})
}

// briefOrigin is who started the work conversation is handing out now, read off that conversation on machine: the
// latest message in it decides. One the owner wrote is the request, verbatim. One Persona or another conversation put
// there (a supervision message, a notice, a follow-up, a routine's run, a relayed message) means the owner did not
// ask for this just now: the work is marked as started by the assistant, a leader or a routine, with what it followed,
// and no older message of the owner's is attributed. A conversation that cannot be read says unknown.
func (s *Service) briefOrigin(ctx context.Context, machine, conversation string) briefRecord {
	rec := briefRecord{StartedBy: briefByUnknown, Conversation: conversation}
	if machine == "" || conversation == "" {
		return rec
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems, map[string]any{"session_id": conversation, "turns": briefOriginTurns, "lazy_images": true}, &res); err != nil {
		s.logf("brief: read %s on %s: %v", short(conversation), machine, err)
		return rec
	}
	for i := len(res.Items) - 1; i >= 0; i-- {
		it := res.Items[i]
		if it.Role != "user" || it.Kind != proto.KindText || it.Task != nil && !it.Wake {
			continue // a tool result, a picture, a background task's notice inside the turn
		}
		src := prompts.ItemSource(it)
		if src == nil && it.From == "" && !it.Wake {
			text := stripContext(it.Text)
			if text == "" {
				continue // only context: not what led to anything
			}
			rec.Request, rec.StartedBy = ownerRequest(text, it.TS, conversation, it.ID), briefByOwner
			return rec
		}
		rec.StartedBy = briefByAssistant
		if s.isLeader(ctx, conversation) {
			rec.StartedBy = briefByLeader
		}
		switch {
		case it.Wake:
			rec.Trigger = "wake"
		case src == nil:
			rec.Trigger, rec.TriggerTitle = "conversation", it.From
		case src.Kind == proto.SourcePatrol:
			rec.StartedBy, rec.Trigger, rec.TriggerTitle = briefByRoutine, "routine", src.Title
		case src.Kind == proto.SourceFollowup:
			rec.Trigger, rec.TriggerTitle = "followup", src.Title
		case src.Kind == proto.SourceSupervision:
			rec.Trigger = "supervision"
		case src.Kind == proto.SourceConversation:
			rec.Trigger, rec.TriggerTitle = "conversation", src.Title
			// The owner's words it carried, as the hub verified them when it was sent (relay_quote.go).
			if q := s.receivedQuote(ctx, it, src); q != nil {
				rec.Request = &q.BriefRequest
			}
		case src.Kind == proto.SourceMeeting:
			rec.Trigger, rec.TriggerTitle = "meeting", src.Title
		default:
			rec.Trigger, rec.TriggerTitle = "notice", src.Title
		}
		if it.Injected != nil && rec.TriggerTitle == "" {
			rec.TriggerTitle = it.Injected.Line
		}
		rec.TriggerTitle = clip(oneLine(rec.TriggerTitle), 120)
		return rec
	}
	return rec
}

// callerOrigin is briefOrigin for the conversation whose tool call this is (withToolCaller); the hub's own call, or one
// from another conversation than the task's, is the hub's.
func (s *Service) callerOrigin(ctx context.Context, conversation string) briefRecord {
	c, ok := ctx.Value(toolCallerKey{}).(toolCaller)
	if !ok || c.conversation != conversation {
		return briefRecord{StartedBy: briefByHub, Conversation: conversation}
	}
	return s.briefOrigin(ctx, c.machine, conversation)
}

// briefFor makes the brief of a task handed out outside a plan (agent_start's task, an adopted agent, an agent that
// became a task): who asked is origin, the goal its title, done as given. Logged, never fatal: the task goes on.
func (s *Service) briefFor(ctx context.Context, origin briefRecord, todo Todo, doneWhen []string, expect int) {
	origin.Goal, origin.DoneWhen, origin.ExpectMinutes = todo.Title, doneWhen, expect
	if _, err := s.putTaskBrief(ctx, origin, todo.ID); err != nil {
		s.logf("task %s: brief: %v", todo.ID, err)
	}
}
