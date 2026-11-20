package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	prEventTerminal         = "terminal"
	prEventTerminalOrReview = "terminal_or_review"
	externalPRReviewPrefix  = "external-pr@"
)

func externalPREvent(event string) bool {
	return event == prEventTerminal || event == prEventTerminalOrReview
}

func githubParkPRURL(raw string) string {
	source := triageNormalizePRURL(raw)
	m := ciPRURL.FindStringSubmatch(source)
	if m == nil || m[0] != source {
		return ""
	}
	if _, err := strconv.Atoi(m[2]); err != nil {
		return ""
	}
	return source
}

// A PR may be the reference for a different external dependency. Only an explicit PR event enrolls the link.
func externalParkPRURL(r *TaskReview) string {
	if r == nil || r.WaitingOn != parkExternalEvent || r.Evidence == nil ||
		r.Evidence.Kind != "external_event" || !externalPREvent(r.Evidence.PREvent) {
		return ""
	}
	return githubParkPRURL(r.Evidence.URL)
}

// externalPRObservation travels with the park even when the task has no stage PR.
type externalPRObservation struct {
	URL          string            `json:"url"`
	State        string            `json:"state"`
	CheckedAt    time.Time         `json:"checked_at"`
	HeadOID      string            `json:"head_oid,omitempty"`
	Review       prSubmittedReview `json:"review,omitzero"`
	PendingSince time.Time         `json:"pending_since,omitzero"`
}

func (o externalPRObservation) wakes(r *TaskReview) bool {
	if o.State == "merged" || o.State == "closed" {
		return true
	}
	return o.State == "open" && r.Evidence != nil && r.Evidence.PREvent == prEventTerminalOrReview && o.HeadOID != "" &&
		o.Review.ID > 0 && o.Review.SubmittedAt.After(r.At) && !o.Review.SubmittedAt.After(o.CheckedAt)
}

func (s *Service) applyExternalParkPRStatus(ctx context.Context, t Todo, status TodoPRStatus) error {
	held := t.Supervision.parking()
	source := externalParkPRURL(held)
	if source == "" || source != triageNormalizePRURL(status.URL) || held.At.IsZero() ||
		status.CheckedAt.IsZero() || status.CheckedAt.Before(held.At) ||
		status.State != "open" && status.State != "merged" && status.State != "closed" {
		return nil
	}
	observation := externalPRObservation{URL: source, State: status.State, CheckedAt: status.CheckedAt,
		HeadOID: status.HeadOID, Review: status.submittedReview}
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	v, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		p := v.parking()
		cur, ok := s.todoNow(t.ID)
		if !ok || !taskOpen(cur) || p == nil || !p.At.Equal(held.At) || externalParkPRURL(p) != source ||
			p.Evidence.PREvent != held.Evidence.PREvent ||
			p.ExternalPR != nil && !status.CheckedAt.After(p.ExternalPR.CheckedAt) {
			return false
		}
		if f := prFacts(cur, status.URL); f != nil && status.CheckedAt.Before(f.CheckedAt) {
			return false
		}
		p.ExternalPR = &observation
		if observation.wakes(p) {
			v.State = reviewPending
		}
		return true
	})
	if err != nil {
		return err
	}
	if changed && v.State == reviewPending {
		s.signal(superviseSignal{Todo: t.ID, Kind: sigReview, Key: reviewKey(&v)})
	}
	return nil
}

func (v *Supervision) externalPRWake() *TaskReview {
	if v.State != reviewPending || v.PendingSince == nil || len(v.Reviews) == 0 {
		return nil
	}
	r := &v.Reviews[len(v.Reviews)-1]
	if r.ExternalPR != nil && r.ExternalPR.PendingSince.Equal(v.PendingSince.Truncate(time.Microsecond)) {
		return r
	}
	return nil
}

func (o externalPRObservation) detail() string {
	if o.State == "open" {
		return fmt.Sprintf("A new review was submitted on the bound PR: %s#pullrequestreview-%d. Reassess the task against its criteria.", o.URL, o.Review.ID)
	}
	return fmt.Sprintf("The bound PR is %s: %s. Reassess the task against its criteria.", o.State, o.URL)
}

func (g superviseSignal) externalPRReview() bool {
	return g.Kind == sigReview && strings.HasPrefix(g.Key, externalPRReviewPrefix)
}
