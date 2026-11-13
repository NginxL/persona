package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

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
	Waits     *OwnerWait `json:"waits,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
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
	// RestateAt: when the hub handed this needs_user back to its supervisor because the owner could not answer it
	// (undecidable, or owner_cannot_answer): told once.
	RestateAt time.Time `json:"restate_at,omitzero"`
	// OwnerResponse: the fixed answer the owner gave its decision (decide_yourself, not_what_i_asked, explain_first)
	// and when; HoldReleased: after not_what_i_asked, the supervisor sent the task's agent on again.
	OwnerResponse   string    `json:"owner_response,omitempty"`
	OwnerResponseAt time.Time `json:"owner_response_at,omitzero"`
	HoldReleased    bool      `json:"hold_released,omitempty"`
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
		out.Reviews[i].Checks = slices.Clone(r.Checks)
		out.Reviews[i].Options = slices.Clone(r.Options)
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
	LinkTodoReviewer(ctx context.Context, todoID, machineID, sessionID, title, cwd, via string, at time.Time) error
}

var _ supervisionStore = (*db.DB)(nil)

func (s *Service) supervisionDB() supervisionStore {
	st, _ := s.store().(supervisionStore)
	return st
}

func supervisionFromRow(r db.TodoSupervisionRow) *Supervision {
	v := &Supervision{Supervisor: r.Supervisor, DoneWhen: r.DoneWhen, Criteria: r.Criteria, ExpectMinutes: r.ExpectMinutes, State: r.State, Round: r.Round,
		UpdatedAt: r.UpdatedAt}
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
		State: v.State, Round: v.Round, Reviews: raw, AutoReview: auto, Waits: waits, UpdatedAt: v.UpdatedAt}
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
	if !change(v) {
		return *v, false, nil
	}
	v.Criteria = db.AlignTodoCriteria(v.DoneWhen, v.Criteria)
	v.UpdatedAt = time.Now().UTC()
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
	if t.DoneAt != nil && t.StartError != "" && !t.Cancelled() {
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
