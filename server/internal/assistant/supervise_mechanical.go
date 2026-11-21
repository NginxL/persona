package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Mechanical waits are screened before routing, including before starting a background supervisor.
// Restoring a scheduled wake's signal (rather than marking it told) keeps an expired or cancelled wake retryable.
func (s *Service) screenMechanicalSignals(ctx context.Context, list []superviseSignal, now, prev time.Time) []superviseSignal {
	actionable := map[string]bool{}
	for _, g := range list {
		switch g.Kind {
		case sigIdle, sigStalled, sigOrphaned:
		default:
			actionable[g.Todo] = true
		}
	}
	var kept []superviseSignal
	for _, g := range list {
		if actionable[g.Todo] {
			// An actionable sibling keeps the to-do out of mechanical parking, but its idle still needs the read-only wake check.
			if g.Kind == sigIdle {
				if _, retry := s.mechanicalCheck(ctx, g, now, true); !retry.IsZero() {
					s.restoreScreened(g, now, prev)
					continue
				}
			}
			kept = append(kept, g)
			continue
		}
		handled, retry := s.mechanicalSignal(ctx, g, now)
		switch {
		case !retry.IsZero():
			s.restoreScreened(g, now, prev)
		case handled:
			s.turnsTold(ctx, []superviseSignal{g})
			s.logf("supervise: task %s %s handled without a model", g.Todo, g.Kind)
		default:
			kept = append(kept, g)
		}
	}
	if len(kept) == 0 {
		b := &s.supervise
		b.mu.Lock()
		if b.last.Equal(now) {
			b.last = prev
		}
		b.mu.Unlock()
	}
	return kept
}

// restoreScreened keeps a deferred signal out of the digest batch that screening just kept it out of.
func (s *Service) restoreScreened(g superviseSignal, now, prev time.Time) {
	g.batchTo, g.batchUntil = "", time.Time{}
	s.restoreDigest([]superviseSignal{g}, now, prev)
}

// Unknown, actionable and conflicting evidence deliberately falls through to ordinary supervision.
func (s *Service) mechanicalSignal(ctx context.Context, g superviseSignal, now time.Time) (bool, time.Time) {
	return s.mechanicalCheck(ctx, g, now, false)
}

// mechanicalCheck is mechanicalSignal; wakeOnly stops before delivery reads, writes or automatic parking.
func (s *Service) mechanicalCheck(ctx context.Context, g superviseSignal, now time.Time, wakeOnly bool) (bool, time.Time) {
	switch g.Kind {
	case sigIdle, sigStalled, sigOrphaned:
	default:
		return false, time.Time{}
	}
	t, ok := s.todoNow(g.Todo)
	if !ok || t.DoneAt != nil || t.Supervision == nil || g.Ask != "" {
		return false, time.Time{}
	}
	v := t.Supervision
	if v.state(t) != "" || v.Waits != nil || v.AutoReview != nil && v.AutoReview.State == autoRunning {
		return false, time.Time{}
	}
	agents, reached := s.taskAgentsReached(ctx, t)
	agents = currentTaskAgents(agents, supersededAgents(t, agents))
	if !reached || len(agents) == 0 {
		return false, time.Time{}
	}
	for _, c := range agents {
		if g.Kind == sigIdle && c.Loop != nil && c.Loop.Kind == "wakeup" &&
			(c.Status == proto.StatusWorking || c.State == proto.StateWorking) {
			return false, now // the wakeup's turn is running; its end will judge the stop afresh
		}
		if c.Status != proto.StatusDone || c.State == proto.StateWorking || c.State == proto.StateNeedsYou ||
			c.PendingInteractions > 0 || len(c.Queued) > 0 || c.TurnError != "" || c.ReadOnlyReason != "" {
			return false, time.Time{}
		}
	}
	if !s.loadWork(ctx) {
		return false, time.Time{}
	}
	for _, c := range agents {
		if e, exists := s.workEntry([2]string{c.MachineID, c.SessionID}); exists && sameTurn(c, e.TurnAt) &&
			(e.Work.On == proto.WaitUser || e.Work.Phase == proto.WorkWorking) {
			return false, time.Time{}
		}
	}

	// A wake is only a promise while the switch is on, its durable row is current, and its time is in the future.
	var wake time.Time
	wakes := s.triageSwitches(ctx)
	for _, c := range agents {
		var next time.Time
		if c.Loop != nil && c.Loop.Kind == "wakeup" && c.Loop.NextAt.After(now) {
			next = c.Loop.NextAt
		} else if wakes.Enabled && wakes.Wake {
			e, exists := s.workEntry([2]string{c.MachineID, c.SessionID})
			if !exists || !sameTurn(c, e.TurnAt) || e.Work.Phase != proto.WorkBlocked ||
				(e.Work.On != proto.WaitExternal && e.Work.On != proto.WaitAgent) ||
				(e.Work.On == proto.WaitAgent && e.Work.Agent == nil) ||
				e.WakeAt == nil || !e.WakeAt.After(now) || e.InteractionID != "" || e.Work.RetiredAt != nil {
				wake = time.Time{}
				break
			}
			next = *e.WakeAt
		}
		if next.IsZero() {
			wake = time.Time{}
			break
		}
		if wake.IsZero() || next.Before(wake) {
			wake = next
		}
	}
	if wakeOnly {
		return false, wake
	}
	d := t.Delivery
	if d == nil || d.Unblock != unblockOther || d.Gate == "" || d.At.IsZero() ||
		(d.State != deliveryPR && d.State != deliveryMerged) || t.PRURL == "" || len(agents) != 1 {
		return false, wake
	}
	c := agents[0]
	if c.Cwd == "" || d.At.Before(c.UpdatedAt) {
		return false, wake
	}
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var res struct {
		PR *struct {
			URL, State, Review, Mergeable string
			HeadOID                       string `json:"head_oid"`
			ChecksState                   string `json:"checks_state"`
			MergeState                    string `json:"merge_state"`
			Draft                         bool
			ChecksComplete                bool       `json:"checks_complete"`
			ReviewsComplete               bool       `json:"reviews_complete"`
			ThreadsComplete               bool       `json:"threads_complete"`
			LastReviewAt                  *time.Time `json:"last_review_at"`
			Threads                       []struct{}
		} `json:"pr"`
	}
	if err := s.call(qctx, c.MachineID, "git.pr", map[string]any{"cwd": c.Cwd}, &res); err != nil {
		return false, time.Time{}
	}
	p := res.PR
	if p == nil || p.URL != t.PRURL || p.HeadOID == "" || p.Draft || !p.ChecksComplete || !p.ReviewsComplete ||
		!p.ThreadsComplete || len(p.Threads) > 0 || p.Review == "CHANGES_REQUESTED" || p.Mergeable == "CONFLICTING" ||
		p.MergeState == "DIRTY" || p.State == "OPEN" && p.Mergeable != "MERGEABLE" ||
		p.ChecksState == "failing" || p.LastReviewAt != nil && p.LastReviewAt.After(d.At) {
		return false, time.Time{}
	}
	// Read again after the remote check: a new turn or another review must win over this old wait.
	cur, exists := s.todoNow(t.ID)
	latest, present := s.session(ctx, c.MachineID, c.SessionID)
	sameAgents := slices.EqualFunc(cur.Agents, t.Agents, func(a, b TodoAgent) bool {
		return a.MachineID == b.MachineID && a.SessionID == b.SessionID &&
			(a.ReleasedAt == nil) == (b.ReleasedAt == nil) && (a.DismissedAt == nil) == (b.DismissedAt == nil)
	})
	if !exists || cur.DoneAt != nil || cur.PRURL != t.PRURL || !sameAgents ||
		cur.Supervision == nil || !cur.Supervision.UpdatedAt.Equal(v.UpdatedAt) ||
		!present || latest.Status != proto.StatusDone || !sameTurn(latest, c.UpdatedAt) ||
		latest.PendingInteractions > 0 || len(latest.Queued) > 0 || latest.TurnError != "" {
		return false, time.Time{}
	}
	switch p.State {
	case "OPEN":
		if !wake.IsZero() {
			return false, wake // parking would disable the agent's already scheduled follow-up
		}
		if d.State != deliveryPR {
			return false, time.Time{} // conflicting delivery evidence needs judgment
		}
		// A ready/open PR does not prove anyone requested a named reviewer. Leave the signal with the supervisor
		// until it can supply the actual contact receipt through task_review; do not synthesize evidence.
		return false, time.Time{}
	case "MERGED":
		if p.ChecksState != "passing" {
			return false, time.Time{}
		}
		if d.State == deliveryMerged {
			if !wake.IsZero() {
				return false, wake
			}
			// A delivery gate alone does not identify an observable external event and its wake condition.
			return false, time.Time{}
		}
		// Merge/CI proves a delivery stage, not deployment, live verification or acceptance. If no wake owns the
		// next step the supervisor still hears of it; otherwise the recorded follow-up proceeds without a model wake.
		if !s.setTaskDelivery(ctx, t.ID, TodoDelivery{State: deliveryMerged, Gate: i18n.T(s.lang(ctx),
			"assistant-todos.supervision_needs_deployment"), Unblock: unblockAgent, At: now.UTC()}) {
			return false, time.Time{}
		}
		return false, wake
	}
	return false, time.Time{}
}
