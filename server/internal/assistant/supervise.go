package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Supervised tasks (#3124, docs/assistant/unified-entry.md"Supervision"). A task the main conversation hands out (task_plan, or an
// agent_start that became a quiet task) has the main conversation as its supervisor: the task keeps a ledger (what
// done means, how long it should take, where its review stands) and the hub watches it without a model call. What
// needs the supervisor — the work looks finished and waits for review, an agent stopped short of it, waits for an
// answer too long, makes no progress, was cut off or did not start — becomes a signal; the signals of all tasks go to
// the main conversation as one supervision message (the quiet tasks' notices are among them), at most every few minutes, and
// the turn it starts is not pushed unless the assistant pushes (notify_user). The assistant answers with task_review:
// accepted closes the todo (its agents are closed later, tasks_close.go), rework sends the gaps back to the agent (at
// most maxReworkRounds times), needs_user waits for the user, parked waits for something else until a time
// (parked.go), cancelled ends it.
//
// A supervised todo is never closed by the hub's judge (done.go): a finished stretch of work waits for review instead.

const (
	reviewPending   = "pending_review"
	reviewAccepted  = "accepted"
	reviewRework    = "rework"
	reviewNeedsUser = "needs_user"
	reviewCancelled = "cancelled"
)

// maxReworkRounds is how many times a task is sent back; one more rework goes to the user instead.
const maxReworkRounds = 2

// Supervision is a supervised task's ledger, as the apps get it on the todo (supervision).
type Supervision struct {
	Supervisor    string             `json:"supervisor"` // the conversation that handed it out
	DoneWhen      []string           `json:"done_when"`
	Criteria      []db.TodoCriterion `json:"criteria"`
	ExpectMinutes int                `json:"expect_minutes,omitempty"`
	State         string             `json:"state"` // "" going on, or one of review*
	// Parked: what a parked task waits for (parked.go); nil unless State is parked. Read off its latest review each time
	// the ledger is copied, not kept on its own.
	Parked  *Parked      `json:"parked,omitempty"`
	Round   int          `json:"round"` // reworks so far
	Reviews []TaskReview `json:"reviews"`
	// AutoReview is the latest independent review of its change (supervise_autoreview.go, #2173); nil when none ran.
	AutoReview *AutoReview `json:"auto_review,omitempty"`
	// Waits: the done judge found the latest stretch of work stopped on a decision only the owner can make (verdict
	// needs_user, #1277). While it holds, task_review accepted is refused: closing the task would lose the decision.
	// Its agent working again, or any review, clears it. nil when the last judgment said otherwise.
	Waits *OwnerWait `json:"waits,omitempty"`
	// PendingSince: when the task entered pending_review (updateSupervision keeps it: set on the way in, kept while the
	// review waits, gone on the way out). The apps list a review younger than an hour with the tasks in progress, as
	// its supervisor's to do, and an older one under Needs you (docs/assistant/unified-entry.md "Needs you and In
	// progress"). nil in every other state, and for a task that waited for review before the hub kept the time.
	PendingSince *time.Time `json:"pending_since,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// Progress: what its agent last reported to the supervisor (report_to_supervisor, agent_control.go). A live
	// reading kept in memory only (the next report brings it back after a restart), and apart from UpdatedAt, which
	// names the review a task waits for.
	Progress *TaskProgress `json:"progress,omitempty"`
}

// TaskProgress is a supervised task's agent's latest report: its kind, the step it is on of how many (0: not said),
// what it said in a line, and when. Line and Detail are for the apps only (#3181): no prompt, supervision message,
// briefing, card or push reads them.
type TaskProgress struct {
	Kind  string `json:"kind"`
	Step  int    `json:"step,omitempty"`
	Steps int    `json:"steps,omitempty"`
	Text  string `json:"text"`
	// Line: the report paraphrased in one sentence in the user's language (report_line.go), for the Progress row; ""
	// until the model wrote it, and for good when there is no model or it failed (the apps show Text).
	Line string `json:"line,omitempty"`
	// Detail: the report as the agent wrote it (Markdown, at most 2000 characters), for the task's details; "" when it
	// is Text (one line of at most 200 characters).
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// TaskReview is one review: the assistant's verdict, with each criterion's evidence or gap.
type TaskReview struct {
	At      time.Time     `json:"at"`
	Verdict string        `json:"verdict"`
	By      string        `json:"by,omitempty"`    // "reviewer": the independent reviewer's rework (#2173); "judge": the done judge's own acceptance (#3128); "" the supervisor's
	Round   int           `json:"round,omitempty"` // the rework round it started (rework)
	Checks  []ReviewCheck `json:"checks,omitempty"`
	Note    string        `json:"note,omitempty"`
	// UserCheck: an accepted review that still wants the owner to look themselves (task_review user_check): its
	// "Please verify" waits for them instead of being passed by the supervisor (acceptance_supervisor.go, #3240).
	UserCheck bool `json:"user_check,omitempty"`
	// A needs_user review's decision (#3804, decision.go): why only the owner can decide, what is to be decided, the
	// option recommended, the options (the recommended first) and what waiting costs.
	WhyOwner      string           `json:"why_owner,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	Recommend     string           `json:"recommend,omitempty"`
	Options       []DecisionOption `json:"options,omitempty"`
	CostOfWaiting string           `json:"cost_of_waiting,omitempty"`
	// Deadline and Reversible: the needs_user decision's deadline and whether its recommendation can be undone, as the
	// supervisor's task_review said them (decision_deadline.go); absent when it gave none, and on reviews from before
	// they were kept.
	Deadline   *time.Time `json:"deadline,omitempty"`
	Reversible *bool      `json:"reversible,omitempty"`
	// A parked review's (parked.go): until when it waits, what kind of thing it waits on (Reason says what), how many
	// parks in a row this one is with no agent turn in between, and when a supervision message carried its parked_due.
	Until     time.Time `json:"until,omitzero"`
	WaitingOn string    `json:"waiting_on,omitempty"`
	Parks     int       `json:"parks,omitempty"`
	DueAt     time.Time `json:"due_at,omitzero"`
	// Evidence is absent on legacy parks. A new park records its external wait, inspected source receipt and
	// recording conversation here, in the existing Reviews JSON; it needs no new state store.
	Evidence *ParkEvidence `json:"evidence,omitempty"`
	// Reply is the source receipt that ended this exact park, committed with the pending-review transition.
	Reply *connectorwire.LarkReplyObservation `json:"reply,omitempty"`
	// ExternalPR is this external-event park's observed object, independent of the task's PR associations.
	ExternalPR *externalPRObservation `json:"external_pr,omitempty"`
	// Decided: the owner's decision (its id) that ended this park before its time (parkDecided, parked.go), and
	// DecidedTold: when a supervision message carried that to the supervisor.
	Decided     string    `json:"decided,omitempty"`
	DecidedTold time.Time `json:"decided_told,omitzero"`
	// RestateAt: when the hub handed this needs_user back to its supervisor because the owner could not answer it
	// (undecidable, or owner_cannot_answer): told once.
	RestateAt time.Time `json:"restate_at,omitzero"`
	// RecheckAt: when this decision went back to its supervisor to confirm stale facts. While it is the latest review,
	// the old round is neither shown nor answerable; a fresh needs_user review starts a new round (decision.go).
	RecheckAt time.Time `json:"recheck_at,omitzero"`
	// OwnerResponse: the fixed answer the owner gave its decision (decide_yourself, not_what_i_asked, explain_first)
	// and when; HoldReleased: after not_what_i_asked, the supervisor sent the task's agent on again.
	OwnerResponse   string    `json:"owner_response,omitempty"`
	OwnerResponseAt time.Time `json:"owner_response_at,omitzero"`
	HoldReleased    bool      `json:"hold_released,omitempty"`
	// Handoff is an internal wait recorded after session.send was acknowledged.
	Handoff *taskHandoff `json:"handoff,omitempty"`
}

// OwnerWait is a judgment that a supervised task's stretch of work (begun at Work) stopped on the owner's decision:
// when it was judged and the judge's reason.
type OwnerWait struct {
	Work   time.Time `json:"work"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

type ReviewCheck struct {
	Criterion string `json:"criterion"`
	Met       bool   `json:"met"`
	Evidence  string `json:"evidence,omitempty"`
}

func (v *Supervision) clone() *Supervision {
	if v == nil {
		return nil
	}
	out := *v
	out.DoneWhen = slices.Clone(v.DoneWhen)
	out.Criteria = db.AlignTodoCriteria(v.DoneWhen, v.Criteria)
	out.Reviews = make([]TaskReview, len(v.Reviews))
	for i, r := range v.Reviews {
		out.Reviews[i] = r
		if r.Handoff != nil {
			handoff := *r.Handoff
			out.Reviews[i].Handoff = &handoff
		}
		out.Reviews[i].Checks = slices.Clone(r.Checks)
		out.Reviews[i].Options = slices.Clone(r.Options)
		out.Reviews[i].Evidence = r.Evidence.clone()
		if r.Reply != nil {
			receipt := *r.Reply
			out.Reviews[i].Reply = &receipt
		}
		if r.ExternalPR != nil {
			observation := *r.ExternalPR
			out.Reviews[i].ExternalPR = &observation
		}
	}
	if out.DoneWhen == nil {
		out.DoneWhen = []string{}
	}
	if v.Progress != nil {
		p := *v.Progress
		out.Progress = &p
	}
	if v.AutoReview != nil {
		a := *v.AutoReview
		out.AutoReview = &a
	}
	if v.Waits != nil {
		w := *v.Waits
		out.Waits = &w
	}
	if v.PendingSince != nil {
		at := *v.PendingSince
		out.PendingSince = &at
	}
	out.Parked = out.parked()
	return &out
}

// state is the review state that holds now: an accepted or cancelled task the user reopened goes on again, and a
// parked one that was closed is no longer parked.
func (v *Supervision) state(t Todo) string {
	if v == nil {
		return ""
	}
	if v.State == reviewParked && (t.DoneAt != nil || v.parking() == nil) {
		return ""
	}
	if t.DoneAt == nil && (v.State == reviewAccepted || v.State == reviewCancelled) {
		return ""
	}
	return v.State
}

// supervisionStore is what the ledgers keep in the database; a store without it keeps them in memory only.
type supervisionStore interface {
	ListTodoSupervision(ctx context.Context) ([]db.TodoSupervisionRow, error)
	PutTodoSupervision(ctx context.Context, r db.TodoSupervisionRow) error
	ReviewDoneTodo(ctx context.Context, id, reason string, accepted bool, at time.Time) (*db.TodoRow, bool, error)
	LinkTodoReviewer(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, via string, at time.Time) error
}

var _ supervisionStore = (*db.DB)(nil)

func (s *Service) supervisionDB() supervisionStore {
	st, _ := s.store().(supervisionStore)
	return st
}

func supervisionFromRow(r db.TodoSupervisionRow) *Supervision {
	v := &Supervision{Supervisor: r.Supervisor, DoneWhen: r.DoneWhen, Criteria: r.Criteria, ExpectMinutes: r.ExpectMinutes, State: r.State, Round: r.Round,
		PendingSince: r.PendingSince, UpdatedAt: r.UpdatedAt}
	_ = json.Unmarshal(r.Reviews, &v.Reviews)
	if len(r.AutoReview) > 0 {
		var a AutoReview
		if json.Unmarshal(r.AutoReview, &a) == nil {
			v.AutoReview = &a
		}
	}
	if len(r.Waits) > 0 {
		var w OwnerWait
		if json.Unmarshal(r.Waits, &w) == nil {
			v.Waits = &w
		}
	}
	return v.clone()
}

func (v *Supervision) row(todoID string) db.TodoSupervisionRow {
	raw, _ := json.Marshal(v.Reviews)
	var auto, waits json.RawMessage
	if v.AutoReview != nil {
		auto, _ = json.Marshal(v.AutoReview)
	}
	if v.Waits != nil {
		waits, _ = json.Marshal(v.Waits)
	}
	return db.TodoSupervisionRow{TodoID: todoID, Supervisor: v.Supervisor, DoneWhen: v.DoneWhen, Criteria: v.Criteria, ExpectMinutes: v.ExpectMinutes,
		State: v.State, Round: v.Round, Reviews: raw, AutoReview: auto, Waits: waits, PendingSince: v.PendingSince, UpdatedAt: v.UpdatedAt}
}

// cleanDoneWhen is a task's criteria as kept: trimmed, no empty or repeated ones, at most 6 of at most 200 characters.
func cleanDoneWhen(list []string) []string {
	out := []string{}
	for _, c := range list {
		c = clip(oneLine(c), 200)
		if c != "" && !slices.Contains(out, c) && len(out) < 6 {
			out = append(out, c)
		}
	}
	return out
}

// superviseTodo makes todo id a supervised task of conversation supervisor.
func (s *Service) superviseTodo(ctx context.Context, id, supervisor string, doneWhen []string, expect int) {
	_, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		v.Supervisor, v.DoneWhen, v.ExpectMinutes = supervisor, cleanDoneWhen(doneWhen), max(0, min(expect, 24*60))
		return true
	})
	if err != nil {
		s.logf("task %s: supervision: %v", id, err)
	}
}

// startSupervisionRound: a plan's task took todo id over to start a new Agent on it (task_plan continues, or its
// todo_id; takeTodoForTask), so the work from here is a new round of its supervision (#11553). A terminal review of the
// work before it (accepted or cancelled) stays in the ledger's reviews, as history, and no longer decides the to-do's
// state: the ledger goes back to going on, with no reworks and no owner wait yet. Otherwise every client (web
// acceptanceStands, Apple, Android and the hub's triage alike) went on showing the earlier round's acceptance on the
// new work, filed the to-do under Just finished or Done whenever its new Agent stopped, and superviseJudged dropped the
// new round's stops as more work sent after an acceptance. That rule stays for what it was made for (#3156, #7539):
// more work sent to an accepted task's Agent, a message on its card, a Reopen by hand. A to-do with no ledger gets
// none here, and a ledger in any other state is the current round's already.
func (s *Service) startSupervisionRound(ctx context.Context, id string) {
	if t, ok := s.todoNow(id); !ok || t.Supervision == nil {
		return
	}
	if _, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		if v.State != reviewAccepted && v.State != reviewCancelled {
			return false
		}
		v.State, v.Round, v.Waits = "", 0, nil
		return true
	}); err != nil {
		s.logf("task %s: new supervision round: %v", id, err)
	}
}

// pendingSince is a ledger's PendingSince after a write at now that took its stored state from before to after, given
// the one it held (since): now on the way into pending_review, unchanged while the review waits (whatever else the write
// changed: a criterion's report, a new supervisor), nil in every other state. A review that already waited with no
// time recorded gets none: the apps then keep it under Needs you, where it was.
func pendingSince(before, after string, since *time.Time, now time.Time) *time.Time {
	switch {
	case after != reviewPending:
		return nil
	case before != reviewPending:
		return &now
	}
	return since
}

// updateSupervision changes todo id's ledger (a new one when it has none) with change, which says whether it changed
// anything; the ledger as it is then. Writes are one at a time.
func (s *Service) updateSupervision(ctx context.Context, id string, change func(*Supervision) bool) (Supervision, bool, error) {
	b := &s.supervise
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	t, ok := s.todoNow(id)
	if !ok {
		return Supervision{}, false, db.ErrTodoNotFound
	}
	v := t.Supervision.clone()
	if v == nil {
		v = (&Supervision{}).clone()
	}
	before := v.State
	park := v.parking()
	if !change(v) {
		return *v, false, nil
	}
	v.Criteria = db.AlignTodoCriteria(v.DoneWhen, v.Criteria)
	v.UpdatedAt = time.Now().UTC()
	v.PendingSince = pendingSince(before, v.State, v.PendingSince, v.UpdatedAt)
	if park != nil && v.State == reviewPending && park.ExternalPR != nil && park.ExternalPR.wakes(park) {
		// Match the review period after a reload. Postgres stores PendingSince to microsecond precision.
		park.ExternalPR.PendingSince = v.UpdatedAt.Truncate(time.Microsecond)
	}
	if st := s.supervisionDB(); st != nil && s.todoDB() != nil {
		if err := st.PutTodoSupervision(ctx, v.row(id)); err != nil {
			return Supervision{}, false, err
		}
	}
	s.todos.mu.Lock()
	if cur := s.todos.todos[id]; cur != nil {
		next := v.clone()
		if cur.Supervision != nil { // the progress is noteProgress's: a report that came meanwhile stays
			next.Progress = cur.Supervision.clone().Progress
		}
		cur.Supervision = next
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	return *v, true, nil
}

// noteProgress keeps p as supervised task id's progress (in memory, no ledger write: see Supervision.Progress) and
// tells the apps; false when id is no supervised task.
func (s *Service) noteProgress(ctx context.Context, id string, p TaskProgress) bool {
	s.todos.mu.Lock()
	cur := s.todos.todos[id]
	ok := cur != nil && cur.Supervision != nil
	if ok {
		v := cur.Supervision.clone()
		v.Progress = &p
		cur.Supervision = v
	}
	s.todos.mu.Unlock()
	if ok {
		s.todosChanged(ctx)
	}
	return ok
}

// ---- the snapshot ----

// taskHealth is how supervised task t stands, for the snapshot: a label and a rank (lower: the more it needs the
// assistant). "" for a task that is not supervised.
func taskHealth(t Todo, agents []proto.Session, now time.Time) (string, int) {
	v := t.Supervision
	if v == nil {
		return "", 5
	}
	switch v.state(t) {
	case reviewNeedsUser:
		return "等你决定", 0
	case reviewPending:
		return "待验收", 1
	case reviewAccepted:
		return "已验收", 8
	case reviewCancelled:
		return "已取消", 8
	case reviewParked: // waits on purpose (parked.go): below everything that needs the assistant
		return parkedHealth(v.parked(), now.Location()), 6
	}
	if t.failedStart() {
		// Its agent never started: stored done (#1077) but nothing was done. Only the supervisor can retry it, so it
		// heads the list rather than sitting among the finished ones.
		return "启动失败，待你处理（重试只有你能做）", 0 // i18n-ignore: the snapshot is the assistant's prompt
	}
	if t.DoneAt != nil {
		return "", 8
	}
	if v.Waits != nil { // the judge found it stopped on the owner's decision (#1277): not to be accepted as done
		return "停下等用户拍板，别直接验收", 0 // i18n-ignore: the snapshot is the assistant's prompt
	}
	for _, c := range agents {
		if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			if wait := now.Sub(cmp.Or(c.StateSince, c.UpdatedAt)); wait >= time.Minute {
				return "等回答 " + humanDuration(wait), 0
			}
			return "等回答", 0
		}
	}
	for _, c := range agents {
		if c.State != proto.StateWorking {
			continue
		}
		moved := c.UpdatedAt
		if c.ActivityAt != nil && c.ActivityAt.After(moved) {
			moved = *c.ActivityAt
		}
		if still := now.Sub(moved); !moved.IsZero() && still >= stallAfter(v.ExpectMinutes) {
			return "卡住 " + humanDuration(still), 2
		}
	}
	if v.state(t) == reviewRework {
		return fmt.Sprintf("返工第 %d 轮", v.Round), 3
	}
	return "正常", 5
}
