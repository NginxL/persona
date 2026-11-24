package assistant

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

const (
	briefEstimateLearned = "learned"
	estimateHistoryAge   = 90 * 24 * time.Hour
	estimateHistoryLimit = 256
	estimateRepairLimit  = 32
	estimateSampleLimit  = 32
	estimateSampleMin    = 5
	estimateReadTimeout  = 500 * time.Millisecond
)

const (
	estimateRepoMachine = "repo_machine"
	estimateMachine     = "machine"
	estimateComparable  = "comparable"
	estimateDefault     = "default"
	estimateUnavailable = "history_unavailable"
)

// TaskEstimateEvidence says what the plan knew when it chose this estimate.
// It exposes counts and groups, without private machine or repository names.
type TaskEstimateEvidence struct {
	Minutes     int    `json:"minutes"`
	SampleCount int    `json:"sample_count"`
	Cohort      string `json:"cohort"`
}

type taskEstimateStore interface {
	CreateTaskEstimateRound(context.Context, db.TaskEstimateRound) error
	AdmitTaskEstimateRound(context.Context, string, string, string, string, db.TaskEstimateCohort) error
	AcceptTaskEstimateRound(context.Context, string, string, time.Time, int) error
	ReconcileTaskEstimateRounds(context.Context, db.TaskEstimateCohort, time.Time, time.Time, int) error
	RecentTaskEstimateSamples(context.Context, db.TaskEstimateCohort, time.Time, time.Time, int) ([]db.TaskEstimateSample, error)
}

var _ taskEstimateStore = (*db.DB)(nil)

func (s *Service) taskEstimateDB() taskEstimateStore {
	if s.todoDB() == nil {
		return nil
	}
	store, _ := s.store().(taskEstimateStore)
	return store
}

func (s *Service) taskEstimateCohort(t PlanTask) db.TaskEstimateCohort {
	return db.TaskEstimateCohort{
		Kind: taskStageKind(t.Readonly, firstNonEmpty(t.Goal, t.Title), t.DoneWhen, false), Readonly: t.Readonly,
		Role: t.Role, Tier: t.Tier, Machine: s.canonical(t.Machine), Repository: t.Repo,
	}
}

func (s *Service) estimateNewPlanTask(ctx context.Context, conversation string, t *PlanTask) {
	t.ExpectMinutes, t.EstimateSource = automaticEstimate(t.ExpectMinutes, t.EstimateSource)
	if t.EstimateSource != briefEstimateSystemDefault {
		return
	}
	evidence := TaskEstimateEvidence{Minutes: defaultTaskEstimateMinutes, Cohort: estimateUnavailable}
	t.EstimateEvidence = &evidence
	store := s.taskEstimateDB()
	if store == nil || s.selfPolicy(ctx, conversation).NoKBWrite {
		return
	}
	cohort := s.taskEstimateCohort(*t)
	now := time.Now().UTC()
	query, cancel := context.WithTimeout(ctx, estimateReadTimeout)
	defer cancel()
	since := now.Add(-estimateHistoryAge)
	// Recovery runs first so this plan can use what it restores, but it may
	// spend only half of the remaining deadline: the history read that follows
	// always starts with a live context.
	deadline, _ := query.Deadline()
	repair, cancelRepair := context.WithTimeout(query, time.Until(deadline)/2)
	err := store.ReconcileTaskEstimateRounds(repair, cohort, since, now, estimateRepairLimit)
	cancelRepair()
	if err != nil {
		s.logf("task estimate reconcile: %v", err)
	}
	samples, err := store.RecentTaskEstimateSamples(query, cohort, since, now, estimateHistoryLimit)
	if err != nil {
		s.logf("task estimate history: %v", err)
		return
	}
	selected, learned := chooseTaskEstimate(cohort, samples)
	t.ExpectMinutes, t.EstimateEvidence = selected.Minutes, &selected
	if learned {
		t.EstimateSource = briefEstimateLearned
	}
}

func chooseTaskEstimate(cohort db.TaskEstimateCohort, samples []db.TaskEstimateSample) (TaskEstimateEvidence, bool) {
	var repo, machine, comparable []int
	for _, sample := range samples {
		if sample.ActualMinutes <= 0 {
			continue
		}
		if len(comparable) < estimateSampleLimit {
			comparable = append(comparable, sample.ActualMinutes)
		}
		if cohort.Machine == "" || cohort.Machine != sample.Machine {
			continue
		}
		if len(machine) < estimateSampleLimit {
			machine = append(machine, sample.ActualMinutes)
		}
		if cohort.Repository != "" && cohort.Repository == sample.Repository && len(repo) < estimateSampleLimit {
			repo = append(repo, sample.ActualMinutes)
		}
	}
	for _, group := range []struct {
		cohort    string
		durations []int
	}{
		{estimateRepoMachine, repo},
		{estimateMachine, machine},
		{estimateComparable, comparable},
	} {
		if len(group.durations) < estimateSampleMin {
			continue
		}
		slices.Sort(group.durations)
		n := len(group.durations)
		// Round up an even-sized median, then stay within the plan's limit.
		middle := (int64(group.durations[(n-1)/2]) + int64(group.durations[n/2]) + 1) / 2
		return TaskEstimateEvidence{Minutes: int(min(middle, 24*60)), Cohort: group.cohort, SampleCount: n}, true
	}
	return TaskEstimateEvidence{Minutes: defaultTaskEstimateMinutes, Cohort: estimateDefault, SampleCount: len(comparable)}, false
}

// estimateDispatch freezes a new eligible cohort after its original dispatch
// event reached the brief store. There is no reconstruction for legacy rounds.
func (s *Service) estimateDispatch(ctx context.Context, p *TaskPlan, t *PlanTask, todo, brief, round string, at time.Time) {
	store := s.taskEstimateDB()
	if store == nil || s.selfPolicy(ctx, p.Conversation).NoKBWrite {
		return
	}
	if err := store.CreateTaskEstimateRound(ctx, db.TaskEstimateRound{
		ID: round, Todo: todo, Brief: brief, Plan: p.ID, Dispatched: at, Cohort: s.taskEstimateCohort(*t),
	}); err != nil {
		s.logf("task %s: record estimate cohort: %v", todo, err)
	}
}

// estimateAdmitted confirms the eligible round really started on its recorded
// target. A retry or placement change cannot rewrite the original cohort.
func (s *Service) estimateAdmitted(ctx context.Context, t PlanTask, machine, session string) {
	store := s.taskEstimateDB()
	if store == nil || !t.EstimateCalibration || t.planID == "" {
		return
	}
	brief, ok := s.briefOfTodo(t.TodoID)
	if !ok {
		return
	}
	for _, event := range slices.Backward(brief.Log) {
		if event.Kind != briefEventDispatch || event.Todo != t.TodoID {
			continue
		}
		if event.PlanID != t.planID || event.RoundID == "" {
			return
		}
		cohort := s.taskEstimateCohort(t)
		cohort.Machine = s.canonical(machine)
		if err := store.AdmitTaskEstimateRound(ctx, event.RoundID, t.planID, t.TodoID, session, cohort); err != nil {
			s.logf("task %s: estimate admission: %v", t.TodoID, err)
			return
		}
		// A completion and acceptance can race the link's bookkeeping.
		s.learnAcceptedEstimate(ctx, t.TodoID)
		return
	}
}

// learnAcceptedEstimate projects the persisted first completion into the
// sample table. The store rechecks the current dispatch, admission, matching
// completion event and the committed acceptance before it accepts the sample.
func (s *Service) learnAcceptedEstimate(ctx context.Context, todoID string) {
	store := s.taskEstimateDB()
	if store == nil {
		return
	}
	todo, ok := s.todoNow(todoID)
	if !ok || todo.DoneAt == nil || todo.DoneBy != db.DoneByReview || todo.Cancelled() || todo.StartError != "" || todo.Supervision == nil {
		return
	}
	view, ok := s.briefOfTodo(todoID)
	if !ok {
		return
	}
	brief := briefFromView(view)
	round, _, ok := brief.roundFor(todo)
	if !ok {
		return
	}
	completion := brief.completionFor(round, todoID)
	if completion == nil || completion.ActualMinutes <= 0 {
		return
	}
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), estimateReadTimeout)
	defer cancel()
	if err := store.AcceptTaskEstimateRound(write, round, todoID, completion.At, completion.ActualMinutes); err != nil {
		s.logf("task %s: learn accepted estimate: %v", todoID, err)
	}
}

func taskPlanEstimateNotes(tasks []PlanTask) string {
	var notes strings.Builder
	for i, task := range tasks {
		if task.EstimateSource == "" {
			continue
		}
		fmt.Fprintf(&notes, "\nTask %d estimate: %d minutes (%s", i+1, task.ExpectMinutes, task.EstimateSource)
		if evidence := task.EstimateEvidence; evidence != nil {
			if evidence.Cohort == estimateUnavailable {
				notes.WriteString(", comparable history unavailable")
			} else {
				fmt.Fprintf(&notes, ", %d samples, %s", evidence.SampleCount, evidence.Cohort)
			}
		}
		notes.WriteString(").")
	}
	return notes.String()
}
