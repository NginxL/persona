package assistant

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// applyChecks records only the criteria actually checked, without deriving any
// task verdict from their count. Unmatched checks remain in the review history.
func (v *Supervision) applyChecks(checks []ReviewCheck, at time.Time, by string) {
	v.Criteria = db.AlignTodoCriteria(v.DoneWhen, v.Criteria)
	for _, check := range checks {
		for i, text := range v.DoneWhen {
			if !sameCriterion(check.Criterion, text) {
				continue
			}
			state := "not_met"
			if check.Met {
				state = "met"
			}
			v.Criteria[i] = db.TodoCriterion{Criterion: text, State: state, Evidence: check.Evidence, At: &at, By: by}
			break
		}
	}
}

// completeReviewCriteriaError refuses an ordinary acceptance whose checks do not prove every current criterion.
// Callers use it only for accepted/done reviews: cannot, cancelled, criteria-free tasks and the judge keep their rules.
func completeReviewCriteriaError(doneWhen []string, checks []ReviewCheck) error {
	if len(doneWhen) == 0 {
		return nil
	}
	matched := make([]int, len(doneWhen))
	for i, check := range checks {
		criterion := clip(oneLine(check.Criterion), 200)
		at, matches := -1, 0
		for j, current := range doneWhen {
			if sameCriterion(criterion, current) {
				at, matches = j, matches+1
			}
		}
		switch {
		case matches == 0:
			return fmt.Errorf("accepted: check %d names unknown current done_when %q; use the current criteria and exactly one met check with non-empty evidence for each; nothing was written", i+1, criterion)
		case matches > 1:
			return fmt.Errorf("accepted: check %d criterion %q matches more than one current done_when entry; nothing was written", i+1, criterion)
		case matched[at] != 0:
			return fmt.Errorf("accepted: current done_when %q has duplicate checks; supply exactly one met check with non-empty evidence for every criterion; nothing was written", doneWhen[at])
		case !check.Met:
			return fmt.Errorf("accepted: current done_when %q is not met; use rework for what is missing; nothing was written", doneWhen[at])
		case strings.TrimSpace(check.Evidence) == "":
			return fmt.Errorf("accepted: current done_when %q needs non-empty evidence; nothing was written", doneWhen[at])
		}
		matched[at] = i + 1
	}
	for i, current := range doneWhen {
		if matched[i] == 0 {
			return fmt.Errorf("accepted: current done_when %q has no check; supply exactly one met check with non-empty evidence for every criterion; nothing was written", current)
		}
	}
	return nil
}

// criterionReport uses the one-based position the agent saw in done_when. Text
// matching remains the persisted identity, so later reordering cannot move evidence.
type criterionReport struct {
	Number   int    `json:"number"`
	State    string `json:"state"`
	Evidence string `json:"evidence"`
}

func cleanCriterionReports(reports []criterionReport, doneWhen []string) ([]criterionReport, error) {
	out := make([]criterionReport, 0, len(reports))
	seen := map[int]bool{}
	for _, r := range reports {
		if r.Number < 1 || r.Number > len(doneWhen) {
			return nil, fmt.Errorf("criteria: number must be between 1 and %d", len(doneWhen))
		}
		if seen[r.Number] {
			return nil, fmt.Errorf("criteria: number %d appears twice", r.Number)
		}
		seen[r.Number] = true
		switch r.State {
		case "pending", "met", "not_met":
		default:
			return nil, fmt.Errorf("criteria: state must be pending, met or not_met")
		}
		r.Evidence = clip(strings.TrimSpace(r.Evidence), 600)
		if r.State != "pending" && r.Evidence == "" {
			return nil, fmt.Errorf("criteria: number %d needs evidence", r.Number)
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) recordCriterionReports(ctx context.Context, t Todo, machine, session string, reports []criterionReport, at time.Time) error {
	if len(reports) == 0 {
		return nil
	}
	moved := false // a reading changed: progress (supervise_progress.go)
	_, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		moved = false
		// A concurrent criteria edit must not attach this report to a different entry.
		cur, ok := s.taskOf(ctx, machine, session)
		if !ok || cur.ID != t.ID || cur.DoneAt != nil || !slices.Equal(v.DoneWhen, t.Supervision.DoneWhen) {
			return false
		}
		v.Criteria = db.AlignTodoCriteria(v.DoneWhen, v.Criteria)
		for _, r := range reports {
			text := v.DoneWhen[r.Number-1]
			if old := v.Criteria[r.Number-1]; old.State != r.State || old.Evidence != r.Evidence {
				moved = true
			}
			v.Criteria[r.Number-1] = db.TodoCriterion{Criterion: text, State: r.State, Evidence: r.Evidence,
				At: &at, By: "agent:" + machine + "/" + session}
		}
		return true
	})
	if err == nil && !changed {
		return fmt.Errorf("task or done_when changed: read the current task and report again")
	}
	if err == nil && moved {
		s.progressMade(machine, session, at)
	}
	return err
}
