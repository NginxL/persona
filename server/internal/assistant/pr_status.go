package assistant

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TodoPRStatus keeps forge observations beside the existing stage/delivery. A ready,
// open PR with no working Agent is a view of that state, not another task lifecycle.
type TodoPRStatus struct {
	URL                string     `json:"url"`
	HeadOID            string     `json:"head_oid,omitempty"`
	State              string     `json:"state"`
	Draft              bool       `json:"draft"`
	Own                bool       `json:"own,omitempty"`
	Review             string     `json:"review"`
	ChangesRequestedAt *time.Time `json:"changes_requested_at,omitempty"`
	Checks             string     `json:"checks"`
	Conflicts          bool       `json:"conflicts"`
	CheckedAt          time.Time  `json:"checked_at"`

	submittedReview prSubmittedReview // consumed by the park in the same pass, separate from the stage's review summary
}

type prSubmittedReview struct {
	ID          int64     `json:"id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type prStatusForge interface {
	PRStatus(context.Context, string, int) (TodoPRStatus, error)
}

func (g githubForge) PRStatus(ctx context.Context, repo string, number int) (TodoPRStatus, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	var pr struct {
		State          string `json:"state"`
		Draft          bool   `json:"draft"`
		Merged         bool   `json:"merged"`
		Mergeable      *bool  `json:"mergeable"`
		MergeableState string `json:"mergeable_state"`
		Head           struct {
			SHA string `json:"sha"`
		} `json:"head"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Reviewers []any `json:"requested_reviewers"`
		Teams     []any `json:"requested_teams"`
	}
	if err := g.get(ctx, path, &pr); err != nil {
		return TodoPRStatus{}, err
	}
	out := TodoPRStatus{HeadOID: pr.Head.SHA, State: pr.State, Draft: pr.Draft, Review: "pending", Checks: "none",
		Conflicts: pr.MergeableState == "dirty" || pr.Mergeable != nil && !*pr.Mergeable}
	if pr.Merged {
		out.State = "merged"
	}
	if out.State != "open" {
		return out, nil
	}
	if out.Draft && pr.User.Login != "" {
		var viewer struct {
			Login string `json:"login"`
		}
		// Unknown ownership must not turn someone else's draft into work for our supervisor.
		if err := g.get(ctx, "/user", &viewer); err == nil {
			out.Own = viewer.Login != "" && strings.EqualFold(viewer.Login, pr.User.Login)
		}
	}
	if pr.Head.SHA == "" {
		return TodoPRStatus{}, fmt.Errorf("PR has no head commit")
	}
	type review struct {
		ID          int64     `json:"id"`
		State       string    `json:"state"`
		CommitID    string    `json:"commit_id"`
		SubmittedAt time.Time `json:"submitted_at"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	var reviews []review
	// An incomplete review page cannot prove approval; retain a conservative label.
	if err := g.get(ctx, path+"/reviews?per_page=100", &reviews); err != nil {
		return TodoPRStatus{}, err
	}
	latest := map[string]review{}
	for _, r := range reviews {
		// A submitted review is an event even if it did not change the review verdict. Use provider time and head.
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "COMMENTED":
			if r.ID > 0 && r.User.Login != "" && r.CommitID == pr.Head.SHA && !r.SubmittedAt.IsZero() &&
				(r.SubmittedAt.After(out.submittedReview.SubmittedAt) ||
					r.SubmittedAt.Equal(out.submittedReview.SubmittedAt) && r.ID > out.submittedReview.ID) {
				out.submittedReview = prSubmittedReview{ID: r.ID, SubmittedAt: r.SubmittedAt.UTC()}
			}
		}
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			latest[r.User.Login] = r
		}
	}
	for _, r := range latest {
		if r.State == "APPROVED" {
			out.Review = "approved"
		}
	}
	for _, r := range latest {
		if r.State == "CHANGES_REQUESTED" {
			out.Review = "changes_requested"
			// A local poll time cannot date a review. A full page may omit its later dismissal or approval.
			if len(reviews) < 100 && strings.TrimSpace(r.User.Login) != "" && r.CommitID == pr.Head.SHA && !r.SubmittedAt.IsZero() &&
				(out.ChangesRequestedAt == nil || r.SubmittedAt.After(*out.ChangesRequestedAt)) {
				at := r.SubmittedAt.UTC()
				out.ChangesRequestedAt = &at
			}
		}
	}
	if out.Review != "changes_requested" && len(pr.Reviewers)+len(pr.Teams) > 0 {
		out.Review = "required"
	}
	if len(reviews) == 100 && out.Review == "approved" {
		out.Review = "pending"
	}
	var status struct {
		State string `json:"state"`
		Total int    `json:"total_count"`
	}
	commit := fmt.Sprintf("/repos/%s/commits/%s", repo, pr.Head.SHA)
	if err := g.get(ctx, commit+"/status", &status); err != nil {
		return TodoPRStatus{}, err
	}
	if status.Total > 0 {
		out.Checks = checkState(status.State)
	}
	var checks struct {
		Total int `json:"total_count"`
		Runs  []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := g.get(ctx, commit+"/check-runs?per_page=100&filter=latest", &checks); err != nil {
		return TodoPRStatus{}, err
	}
	if checks.Total > len(checks.Runs) {
		out.Checks = joinCheckState(out.Checks, "pending")
	}
	for _, run := range checks.Runs {
		state := "pending"
		if run.Status == "completed" {
			state = checkState(run.Conclusion)
		}
		out.Checks = joinCheckState(out.Checks, state)
	}
	return out, nil
}

func checkState(state string) string {
	switch strings.ToLower(state) {
	case "success", "neutral", "skipped":
		return "success"
	case "failure", "error", "cancelled", "timed_out", "action_required", "startup_failure", "stale":
		return "failure"
	default:
		return "pending"
	}
}

func joinCheckState(a, b string) string {
	for _, state := range []string{"failure", "pending", "success"} {
		if a == state || b == state {
			return state
		}
	}
	return "none"
}

// prsPolled bounds how many of a task's earlier PRs the poll reads besides its current one, newest first.
const prsPolled = 4

// polledPRs retains the current PR and bounded earlier sample (#7935), then includes a typed wait's source if needed.
// A bound source outside that sample adds at most one read, never displacing another open or unknown PR.
func polledPRs(t Todo) []string {
	out := []string{t.PRURL}
	known := map[string]TodoPRStatus{}
	for _, f := range t.OtherPRs {
		known[f.URL] = f
	}
	for i := len(t.PRs) - 1; i >= 0 && len(out) <= prsPolled; i-- {
		u := t.PRs[i]
		if u == t.PRURL || !ciPRURL.MatchString(u) && !otherReviewLink(u) {
			continue
		}
		if f, ok := known[u]; ok && (f.State == "merged" || f.State == "closed") {
			continue
		}
		out = append(out, u)
	}
	if r := t.Supervision.parking(); r != nil && r.Evidence != nil {
		for i := len(t.PRs) - 1; i >= 0; i-- {
			u := t.PRs[i]
			if slices.Contains(out, u) || !parkWaitsOnPR(r, u) {
				continue
			}
			if f, ok := known[u]; !ok || f.State != "merged" && f.State != "closed" || f.CheckedAt.Before(r.At) {
				out = append(out, u)
			}
			break
		}
		if u := externalParkPRURL(r); u != "" && !slices.ContainsFunc(out, func(known string) bool {
			return ciPRURL.MatchString(known) && triageNormalizePRURL(known) == u
		}) {
			out = append(out, u)
		}
	}
	return out
}

// prFacts are t's last observed facts for PR url: its current PR's, or an earlier one's.
func prFacts(t Todo, url string) *TodoPRStatus {
	if url == t.PRURL {
		if t.PRStatus != nil && t.PRStatus.URL == url {
			return t.PRStatus
		}
		return nil
	}
	for i := range t.OtherPRs {
		if t.OtherPRs[i].URL == url {
			return &t.OtherPRs[i]
		}
	}
	return nil
}

// The existing minute deploy pass owns polling. Reads never block a task list or
// snapshot, and failures leave the last observation to expire rather than invent success.
// A review request on another host is polled the same way through its code host (code_host.go), which says its state
// only: no review, checks or head.
func (s *Service) sweepPRStatuses(ctx context.Context, now time.Time) {
	f, github := s.forge().(prStatusForge)
	if s.ensureTodosLoaded(ctx) != nil {
		return
	}
	s.todos.mu.RLock()
	var tasks []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && (t.PRURL != "" || externalParkPRURL(t.Supervision.parking()) != "") && !isAskTodo(t.ID) {
			tasks = append(tasks, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	for _, t := range tasks {
		if t.DoneAt != nil || t.Cancelled() {
			continue
		}
		held := t.Supervision.parking()
		source := externalParkPRURL(held)
		for _, url := range polledPRs(t) {
			m := ciPRURL.FindStringSubmatch(url)
			var other codeHost
			switch {
			case m != nil && !github:
				continue
			case m == nil && !otherReviewLink(url):
				continue
			case m == nil:
				if other, _ = s.codeHostFor(url); other == nil {
					continue
				}
			}
			var checkedAt time.Time
			bound := source != "" && source == triageNormalizePRURL(url)
			if bound {
				if held.ExternalPR != nil {
					checkedAt = held.ExternalPR.CheckedAt
				}
			} else if old := prFacts(t, url); old != nil {
				checkedAt = old.CheckedAt
			}
			if !checkedAt.IsZero() && now.Sub(checkedAt) < deployRecheck {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			key := t.ID
			if url != t.PRURL {
				key = t.ID + "|" + url
			}
			p := &s.deploy
			p.mu.Lock()
			last := p.seen[key]
			if last.statusPR == url && now.Sub(last.statusAt) < deployRecheck {
				p.mu.Unlock()
				continue
			}
			if p.seen == nil || len(p.seen) > 2000 {
				p.seen = map[string]prSeen{}
			}
			last.statusPR, last.statusAt = url, now
			p.seen[key] = last // failures are attempts too, not fresh evidence
			p.mu.Unlock()
			var status TodoPRStatus
			var err error
			if other != nil {
				status, err = otherPRStatus(ctx, other, url)
			} else {
				number, _ := strconv.Atoi(m[2])
				status, err = f.PRStatus(ctx, m[1], number)
			}
			if err != nil {
				s.logf("task %s: PR status: %v", t.ID, err)
				continue
			}
			observedAt := now.UTC()
			// The pass may have started before this captured wait. Its source request cannot predate that wait.
			if held != nil && held.Evidence != nil && (parkWaitsOnPR(held, url) || bound) && observedAt.Before(held.At) {
				observedAt = held.At
			}
			status.URL, status.CheckedAt = url, observedAt
			if err := s.applyPRStatus(ctx, t, status); err != nil {
				s.logf("task %s: keep PR status: %v", t.ID, err)
			}
		}
	}
}

func (s *Service) applyPRStatus(ctx context.Context, t Todo, status TodoPRStatus) error {
	// The wait belongs to the snapshot taken before the forge request, not a park created while it was in flight.
	held := t.Supervision.parking()
	if source := externalParkPRURL(held); source != "" && source == triageNormalizePRURL(status.URL) {
		if err := s.applyExternalParkPRStatus(ctx, t, status); err != nil {
			return err
		}
	}
	current, ok := s.todoNow(t.ID)
	if !ok || current.DoneAt != nil || current.PRURL != status.URL && !slices.Contains(current.PRs, status.URL) {
		return nil
	}
	t = current
	// Returning from a known open PR (or a legacy PR-stage hold) wakes the same supervisor once no other PR of the task
	// is still open (#7935). A merged PR alone is never acceptance, and unrelated later parks stay put.
	old := prFacts(t, status.URL)
	wasOpen := old != nil && old.State == "open"
	// A PR-stage hold first read terminal; "merge" too, which the task's own current PR merging records before an
	// earlier PR of it is read.
	legacyHold := old == nil && (t.Delivery != nil && t.Delivery.State == deliveryPR || t.Stage == "review" || t.Stage == "merge" && len(t.PRs) > 0)
	terminal := status.State == "merged" || status.State == "closed"
	// The stage's PRs are checked under the stage lock first (below): an observation of a PR an Agent reply replaced
	// while the request was in flight changes nothing, neither the facts nor the supervision.
	b := &s.reports
	b.stageWriting.Lock()
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		b.stageWriting.Unlock()
		return fmt.Errorf("stages could not be read")
	}
	cur := b.stages[t.ID]
	var previous *TodoPRStatus
	if cur.PR == status.URL {
		previous = cur.PRStatus
	} else if f, ok := cur.EarlierPRStatus[status.URL]; ok {
		previous = &f
	}
	if previous != nil && status.CheckedAt.Before(previous.CheckedAt) {
		b.mu.Unlock()
		b.stageWriting.Unlock()
		return nil // an older response must not replace newer facts or resolve a wait
	}
	switch {
	case cur.PR == status.URL:
		cur.PRStatus = &status
	case slices.Contains(cur.PRs, status.URL):
		facts := make(map[string]TodoPRStatus, len(cur.EarlierPRStatus)+1)
		for u, f := range cur.EarlierPRStatus {
			facts[u] = f
		}
		facts[status.URL] = status
		cur.EarlierPRStatus = facts
	default:
		b.mu.Unlock()
		b.stageWriting.Unlock()
		return nil // a new Agent reply replaced the PR while the request was in flight
	}
	if store := s.store(); store != nil {
		stages := make(map[string]todoStage, len(b.stages))
		for id, stage := range b.stages {
			stages[id] = stage
		}
		stages[t.ID] = cur
		if err := store.PutSetting(ctx, reportStagesKey, stages); err != nil {
			b.mu.Unlock()
			b.stageWriting.Unlock()
			return err
		}
	}
	b.stages[t.ID] = cur
	b.mu.Unlock()
	s.todos.mu.Lock()
	if todo := s.todos.todos[t.ID]; todo != nil && todo.PRURL == cur.PR {
		todo.PRStatus, todo.OtherPRs = cur.PRStatus, cur.otherPRFacts()
	}
	s.todos.mu.Unlock()
	b.stageWriting.Unlock()
	s.todosChanged(ctx)
	if status.State == "open" && status.Draft && status.Own && status.HeadOID != "" && t.Supervision != nil {
		s.signal(superviseSignal{Todo: t.ID, Kind: sigDraftPR, PRURL: status.URL, Key: status.URL + "@" + status.HeadOID})
	}
	if status.URL == cur.PR && status.State == "open" && status.Conflicts && status.HeadOID != "" && t.Supervision != nil {
		// Only the current PR raises a conflict signal: the digest revalidates against the task/stage PR, so an
		// earlier PR's conflict would be dropped there anyway.
		s.signal(superviseSignal{Todo: t.ID, Kind: sigPRConflict, Key: status.URL + "@" + status.HeadOID,
			Detail: fmt.Sprintf("PR %s at head %s is open and has merge conflicts.", status.URL, status.HeadOID)})
	}
	reviewChange := parkReviewChangeRequested(held, status)
	if held != nil && (terminal && (wasOpen || legacyHold || held.Evidence != nil) || reviewChange) {
		// Typed waits reconcile terminal facts even after a failed ledger write or a restart. Legacy parks retain
		// their transition/stage gate. A new changes-request review is actionable before other PRs resolve.
		v, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
			p := v.parking()
			if p == nil || !p.At.Equal(held.At) {
				return false
			}
			now, ok := s.todoNow(t.ID)
			if !ok || now.DoneAt != nil || now.PRURL != status.URL && !slices.Contains(now.PRs, status.URL) || !s.stageFollows(t.ID, status.URL) {
				return false
			}
			f := prFacts(now, status.URL)
			if f == nil || f.State != status.State || !f.CheckedAt.Equal(status.CheckedAt) {
				return false
			}
			if terminal {
				if !parkPRsResolved(now, p, status.URL) {
					return false
				}
			} else if f.HeadOID != status.HeadOID || !parkReviewChangeRequested(p, *f) ||
				!f.ChangesRequestedAt.Equal(*status.ChangesRequestedAt) {
				return false
			}
			v.State = reviewPending
			return true
		})
		if err != nil {
			return err
		}
		if changed {
			detail := "PR " + status.State + ": " + status.URL
			if reviewChange {
				detail = "PR changes requested: " + status.URL
			}
			s.signal(superviseSignal{Todo: t.ID, Kind: sigReview, Key: reviewKey(&v), Detail: detail})
		}
	}
	if status.URL == t.PRURL && status.State == "merged" && stageIndex(t.Stage) < stageIndex("merge") {
		s.setTaskStageOn(ctx, t.ID, "merge", "", "", false, status.URL)
	}
	return nil
}

// parkReviewChangeRequested dates an active current-head review, not the poll, against this typed wait. Ambiguous
// same-second provider times stay parked rather than attributing a possibly pre-park review to a newer wait.
func parkReviewChangeRequested(r *TaskReview, status TodoPRStatus) bool {
	return r != nil && !r.At.IsZero() && r.Evidence != nil && parkWaitsOnPR(r, status.URL) &&
		status.State == "open" && status.HeadOID != "" && status.Review == "changes_requested" &&
		status.ChangesRequestedAt != nil && status.ChangesRequestedAt.After(r.At) &&
		!status.CheckedAt.IsZero() && !status.ChangesRequestedAt.After(status.CheckedAt)
}

// stagePR is the PR task id's stage holds now ("" when none).
func (s *Service) stagePR(id string) string {
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stages[id].PR
}

// stageFollows says whether task id's stage still follows the PR at url: its current PR or an earlier one.
func (s *Service) stageFollows(id, url string) bool {
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.stages[id]
	return st.PR == url || slices.Contains(st.PRs, url)
}

// parkPRsResolved retains the multi-PR gate, using current facts under the supervision write lock. A bound earlier
// PR may already be terminal and no longer polled: keep its source available when the last other PR resolves.
func parkPRsResolved(t Todo, r *TaskReview, observed string) bool {
	for _, u := range polledPRs(t) {
		if f := prFacts(t, u); f == nil || f.State != "merged" && f.State != "closed" {
			return false
		}
	}
	var terminal []string
	for _, u := range append([]string{t.PRURL}, t.PRs...) {
		// An old terminal receipt is not evidence that this wait's source has stayed resolved.
		if f := prFacts(t, u); f != nil && (f.State == "merged" || f.State == "closed") &&
			(r.Evidence == nil || u == observed || !f.CheckedAt.Before(r.At)) {
			terminal = append(terminal, u)
		}
	}
	return parkWaitsOnPR(r, terminal...)
}

// parkWaitsOnPR uses the recorded merge source for typed waits. Only legacy parks without evidence use prose or
// bare "#N" references; a different typed source or kind must never fall back to the reason.
func parkWaitsOnPR(r *TaskReview, urls ...string) bool {
	if r == nil || r.WaitingOn != parkExternalEvent && r.WaitingOn != parkSomeoneElse {
		return false
	}
	if e := r.Evidence; e != nil {
		if e.Kind != "merge_review" || !e.Ready || r.WaitingOn != parkSomeoneElse {
			return false
		}
		source := triageNormalizePRURL(e.URL)
		if source == "" || ciPRURL.FindString(source) != source && !otherReviewLink(source) { // other hosts: code_host.go
			return false
		}
		for _, u := range urls {
			if triageNormalizePRURL(u) == source {
				return true
			}
		}
		return false
	}
	reason := strings.ToLower(r.Reason)
	links, refs := ciPRURL.FindAllStringSubmatch(reason, -1), prBareRef.FindAllStringSubmatch(reason, -1)
	for _, url := range urls {
		m := ciPRURL.FindStringSubmatch(url)
		if m == nil {
			continue
		}
		for _, link := range links {
			if strings.EqualFold(link[1], m[1]) && link[2] == m[2] {
				return true
			}
		}
		for _, ref := range refs {
			if ref[1] == m[2] {
				return true
			}
		}
	}
	return false
}

// otherPRStatus is url's state as code host h reads it, with the labels GitHub's read keeps when it knows no more.
func otherPRStatus(ctx context.Context, h codeHost, url string) (TodoPRStatus, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	st, err := h.ReviewStatus(cctx, url)
	if err == nil && st.State == "" {
		err = errReviewUnsupported
	}
	if err != nil {
		return TodoPRStatus{}, fmt.Errorf("%s: %w", h.Name(), err)
	}
	return TodoPRStatus{State: st.State, Review: "pending", Checks: "none"}, nil
}
