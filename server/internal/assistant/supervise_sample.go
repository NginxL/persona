package assistant

import (
	"context"
	"strings"
)

// Spot-check reviews (#3128, docs/assistant/todos.md "Spot-check reviews"): with
// assistant.review_sample_percent below 100, the done judge also checks each done_when criterion (taskAutoCheck).
// The checks are delivery evidence only: every finished task still waits for the main conversation's task_review,
// including work that passed and was not sampled (#5732). At 100 (the default) the judge checks nothing more.

// reviewerJudge marks a review the done judge made itself (TaskReview.By).
const reviewerJudge = "judge"

// reviewSamplePercent is assistant.review_sample_percent: the share of passing judge checks marked as spot checks.
// Every finished task goes to the main conversation regardless; 100 when unset.
func (s *Service) reviewSamplePercent(ctx context.Context) int {
	if p := s.settings(ctx).ReviewSamplePercent; p != nil && *p >= 0 && *p <= 100 {
		return *p
	}
	return 100
}

// taskAutoCheck is the judge's own check of a supervised task's done_when: each criterion (in the ledger's words and
// order) with whether it can be checked from evidence, whether it is met and the evidence, and how far the work went
// out (a delivery state).
type taskAutoCheck struct {
	Checks   []autoCheckItem `json:"checks"`
	Delivery string          `json:"delivery"`
}

type autoCheckItem struct {
	ReviewCheck
	Checkable bool `json:"checkable"`
}

// passed: every criterion is checkable and met with evidence, and the work is live. A task with no criteria never
// passes: there is nothing to check it against.
func (c *taskAutoCheck) passed() bool {
	if c == nil || len(c.Checks) == 0 || c.Delivery != deliveryLive {
		return false
	}
	for _, x := range c.Checks {
		if !x.Checkable || !x.Met || strings.TrimSpace(x.Evidence) == "" {
			return false
		}
	}
	return true
}

// autoCheckOf is the judge's checks matched to criteria: one per criterion in order, the criterion as the ledger has
// it. A check counts only when the judge gave exactly one per criterion and copied each criterion where it belongs;
// otherwise every criterion is left unchecked (not met): a list that is off by one must not pass.
func autoCheckOf(criteria []string, got []autoCheckItem, delivery string) *taskAutoCheck {
	c := &taskAutoCheck{Delivery: delivery}
	aligned := len(got) == len(criteria)
	for i := 0; aligned && i < len(got); i++ {
		aligned = sameCriterion(got[i].Criterion, criteria[i])
	}
	for i, crit := range criteria {
		x := autoCheckItem{ReviewCheck: ReviewCheck{Criterion: crit}}
		if aligned {
			x.Checkable, x.Met, x.Evidence = got[i].Checkable, got[i].Met, clip(oneLine(got[i].Evidence), 300)
		}
		c.Checks = append(c.Checks, x)
	}
	return c
}

// sameCriterion: the judge copied criterion want as got (spacing aside).
func sameCriterion(got, want string) bool {
	return strings.Join(strings.Fields(got), " ") == strings.Join(strings.Fields(want), " ")
}
