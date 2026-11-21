package assistant

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/prompts"
)

// prFollowupStore is the Postgres persistence for post-closure PR subscriptions. A store that does not implement it
// (in-memory test adapter) simply has no follow-up lifecycle, matching the rest of the repository store's fallback.
type prFollowupStore interface {
	UpsertPRFollowups(ctx context.Context, todoID, supervisor, origin string, prURLs []string, now time.Time) error
	ListPRFollowups(ctx context.Context) ([]repository.PRFollowup, error)
	SavePRFollowupObservation(ctx context.Context, f repository.PRFollowup) (bool, error)
	DeletePRFollowup(ctx context.Context, id string) error
	StalePRFollowups(ctx context.Context, before time.Time) ([]repository.PRFollowup, error)
}

func (s *Service) prFollowupDB() prFollowupStore {
	st, ok := s.store().(prFollowupStore)
	if !ok {
		return nil
	}
	return st
}

// sweepPRFollowupsBounded is sweepPRFollowups with the same per-pass deadline as the open-to-do PR sweep.
func (s *Service) sweepPRFollowupsBounded(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), prStatusPassMost)
	defer cancel()
	s.sweepPRFollowups(ctx, now)
}

// followupRecheck bounds how often one subscription's PR is polled, reusing the deploy sweep's cadence. A var so tests
// can shorten it without waiting out the real ten-minute floor.
var followupRecheck = deployRecheck

// followupStaleAfter is how long a non-terminal subscription stays silent before it surfaces in the daily briefing.
const followupStaleAfter = 24 * time.Hour

// deliveryPRsForFollowup is the set of a closed to-do's own delivery PRs: the stage-tracked current and earlier PRs
// (#7935's bounded set), not every repository link (which may include mere references).
func deliveryPRsForFollowup(t Todo) []string {
	out := make([]string, 0, 1+len(t.PRs))
	seen := map[string]bool{}
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] || !ciPRURL.MatchString(u) && !otherReviewLink(u) {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	add(t.PRURL)
	for _, u := range t.PRs {
		add(u)
	}
	return out
}

// createPRFollowups runs when a to-do is accepted and closed: it records one subscription per delivery PR, leaving
// whether each is still open to the next sweep. It never reopens the to-do and is idempotent on repeated acceptance.
func (s *Service) createPRFollowups(ctx context.Context, t Todo) {
	st := s.prFollowupDB()
	if st == nil {
		return
	}
	prs := deliveryPRsForFollowup(t)
	if len(prs) == 0 {
		return
	}
	supervisor := ""
	origin := t.Origin
	if t.Supervision != nil {
		supervisor = t.Supervision.Supervisor
	}
	if supervisor == "" {
		supervisor = origin // no recorded supervisor: fall back to the to-do's own conversation rather than dropping it
	}
	if err := st.UpsertPRFollowups(ctx, t.ID, supervisor, origin, prs, time.Now().UTC()); err != nil {
		s.logf("task %s: PR follow-up subscriptions not saved: %v", t.ID, err)
	}
}

// sweepPRFollowups polls every active post-closure subscription independently of the to-do's DoneAt: terminal PRs
// notify once and clear the subscription; a new conflict or a current-head changes-requested review notifies the
// original supervisor once per head. A forge that cannot read the PR degrades silently rather than pretending to
// watch it. The to-do is never reopened.
func (s *Service) sweepPRFollowups(ctx context.Context, now time.Time) {
	st := s.prFollowupDB()
	if st == nil {
		return
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return
	}
	f, github := s.forge().(prStatusForge)
	subs, err := st.ListPRFollowups(ctx)
	if err != nil {
		s.logf("PR follow-ups: list failed: %v", err)
		return
	}
	now = now.UTC()
	for i := range subs {
		if ctx.Err() != nil {
			return
		}
		sub := subs[i]
		if sub.LastObservedAt != nil && now.Sub(*sub.LastObservedAt) < followupRecheck {
			continue
		}
		status, ok := s.observeFollowupPR(ctx, sub.PRURL, f, github)
		if !ok {
			continue // host unconnected / unsupported / transient: do not pretend to watch, keep the subscription for later
		}
		observedAt := now
		status.URL = sub.PRURL
		status.CheckedAt = observedAt
		s.applyFollowupObservation(ctx, st, sub, status, observedAt)
	}
}

// observeFollowupPR reads one PR through the GitHub forge or the matching code host. ok=false means the PR cannot be
// read now; callers skip it without recording a fake observation.
func (s *Service) observeFollowupPR(ctx context.Context, url string, f prStatusForge, github bool) (TodoPRStatus, bool) {
	m := ciPRURL.FindStringSubmatch(url)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if m != nil {
		if !github {
			return TodoPRStatus{}, false
		}
		number, _ := strconv.Atoi(m[2])
		status, err := f.PRStatus(cctx, m[1], number)
		if err != nil {
			s.logf("PR follow-up %s: %v", url, err)
			return TodoPRStatus{}, false
		}
		return status, true
	}
	if !otherReviewLink(url) {
		return TodoPRStatus{}, false
	}
	h, _ := s.codeHostFor(url)
	if h == nil {
		s.logf("PR follow-up %s: no code host reads it", url)
		return TodoPRStatus{}, false
	}
	status, err := otherPRStatus(cctx, h, url)
	if err != nil {
		s.logf("PR follow-up %s: %v", url, err)
		return TodoPRStatus{}, false
	}
	return status, true
}

// applyFollowupObservation reconciles one observation with the subscription: terminal clears it after a closing
// notice; conflict and current-head review notify once per head; everything else just refreshes stored facts.
func (s *Service) applyFollowupObservation(ctx context.Context, st prFollowupStore, sub repository.PRFollowup, status TodoPRStatus, observedAt time.Time) {
	terminal := status.State == "merged" || status.State == "closed"
	// Conflict and review only act on the current head, and each head wakes the supervisor once.
	newConflict := status.State == "open" && status.Conflicts && status.HeadOID != "" &&
		sub.NotifiedConflictHead != status.HeadOID
	newReview := status.State == "open" && status.Review == "changes_requested" && status.HeadOID != "" &&
		status.ChangesRequestedAt != nil && sub.NotifiedReviewHead != status.HeadOID &&
		!status.ChangesRequestedAt.After(observedAt)

	sub.HeadOID = status.HeadOID
	sub.State = status.State
	sub.Review = status.Review
	sub.ChangesRequestedAt = status.ChangesRequestedAt
	sub.LastObservedAt = &observedAt

	t, hasTodo := s.todoNow(sub.TodoID)
	title := sub.TodoID
	supervisor := sub.Supervisor
	if hasTodo {
		title = t.Title
	}

	switch {
	case terminal:
		s.notifyFollowup(ctx, sub, title, supervisor, status.State, false)
		if err := st.DeletePRFollowup(ctx, sub.ID); err != nil {
			s.logf("PR follow-up %s: delete failed: %v", sub.PRURL, err)
		}
		return
	case newConflict:
		s.notifyFollowup(ctx, sub, title, supervisor, "conflict", true)
		sub.NotifiedConflictHead = status.HeadOID
	case newReview:
		s.notifyFollowup(ctx, sub, title, supervisor, "changes_requested", true)
		sub.NotifiedReviewHead = status.HeadOID
	}

	if _, err := st.SavePRFollowupObservation(ctx, sub); err != nil {
		s.logf("PR follow-up %s: observation not saved: %v", sub.PRURL, err)
	}
}

// notifyFollowup sends one record to the closed to-do's original supervisor conversation. It is a relay record like
// task_end, not a reopen and not an acceptance signal; only real-time conflict/review events ask for a push.
func (s *Service) notifyFollowup(ctx context.Context, sub repository.PRFollowup, title, supervisor, event string, realtime bool) {
	if supervisor == "" {
		return
	}
	text := prompts.RenderPRFollowupNotice(prompts.PRFollowupNotice{
		ID: sub.TodoID, Title: title, PRURL: sub.PRURL, Event: event, Realtime: realtime,
		Language: answerLanguage(s.hello(ctx).Locale),
	})
	s.relayOrQueue(ctx, supervisor, relayMsg{
		From: sub.TodoID, FromTitle: "任务「" + title + "」", Tool: prFollowupTool, Text: text, At: time.Now().UTC(), // i18n-ignore: queue label, as the task notices'
	})
}

// stalePRFollowups is the non-terminal subscriptions that have been silent for at least followupStaleAfter, for the
// daily briefing. It never includes one the forge cannot currently read.
func (s *Service) stalePRFollowups(ctx context.Context, now time.Time) []repository.PRFollowup {
	st := s.prFollowupDB()
	if st == nil {
		return nil
	}
	subs, err := st.StalePRFollowups(ctx, now.Add(-followupStaleAfter))
	if err != nil {
		s.logf("PR follow-ups: stale list failed: %v", err)
		return nil
	}
	return slices.DeleteFunc(subs, func(f repository.PRFollowup) bool { return f.State == "merged" || f.State == "closed" })
}
