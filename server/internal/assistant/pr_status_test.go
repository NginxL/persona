package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type statusForge struct {
	fakeForge
	status      TodoPRStatus
	statusErr   error
	statusCalls int
}

func (f *statusForge) PRStatus(context.Context, string, int) (TodoPRStatus, error) {
	f.statusCalls++
	return f.status, f.statusErr
}

func TestPRStatusReadsForgeBlockers(t *testing.T) {
	for _, tc := range []struct {
		name, pr, reviews, statuses, runs string
		want                              TodoPRStatus
	}{
		{
			name:    "ready with review, checks and conflicts",
			pr:      `{"state":"open","draft":false,"mergeable_state":"dirty","head":{"sha":"head"},"requested_reviewers":[{}]}`,
			reviews: `[]`, statuses: `{"state":"success","total_count":1}`,
			runs: `{"total_count":2,"check_runs":[{"status":"completed","conclusion":"failure"},{"status":"in_progress"}]}`,
			want: TodoPRStatus{HeadOID: "head", State: "open", Review: "required", Checks: "failure", Conflicts: true},
		},
		{
			name: "explicitly unmergeable", pr: `{"state":"open","mergeable":false,"head":{"sha":"head"}}`,
			reviews: `[]`, statuses: `{"total_count":0}`, runs: `{"check_runs":[]}`,
			want: TodoPRStatus{HeadOID: "head", State: "open", Review: "pending", Checks: "none", Conflicts: true},
		},
		{
			name: "mergeability not computed", pr: `{"state":"open","mergeable":null,"mergeable_state":"unknown","head":{"sha":"head"}}`,
			reviews: `[]`, statuses: `{"total_count":0}`, runs: `{"check_runs":[]}`,
			want: TodoPRStatus{HeadOID: "head", State: "open", Review: "pending", Checks: "none"},
		},
		{
			name: "blocked without conflicts", pr: `{"state":"open","mergeable":true,"mergeable_state":"blocked","head":{"sha":"head"}}`,
			reviews: `[]`, statuses: `{"total_count":0}`, runs: `{"check_runs":[]}`,
			want: TodoPRStatus{HeadOID: "head", State: "open", Review: "pending", Checks: "none"},
		},
		{
			name:     "latest review per reviewer",
			pr:       `{"state":"open","head":{"sha":"head"}}`,
			reviews:  `[{"user":{"login":"reviewer"},"state":"CHANGES_REQUESTED"},{"user":{"login":"reviewer"},"state":"APPROVED"}]`,
			statuses: `{"state":"pending","total_count":0}`,
			runs:     `{"total_count":1,"check_runs":[{"status":"completed","conclusion":"neutral"}]}`,
			want:     TodoPRStatus{HeadOID: "head", State: "open", Review: "approved", Checks: "success"},
		},
		{
			name:     "draft with requested changes",
			pr:       `{"state":"open","draft":true,"head":{"sha":"head"}}`,
			reviews:  `[{"user":{"login":"reviewer"},"state":"CHANGES_REQUESTED"}]`,
			statuses: `{"state":"pending","total_count":1}`, runs: `{"check_runs":[]}`,
			want: TodoPRStatus{HeadOID: "head", State: "open", Draft: true, Review: "changes_requested", Checks: "pending"},
		},
		{
			name: "merged needs no commit endpoints", pr: `{"state":"closed","merged":true}`,
			want: TodoPRStatus{State: "merged", Review: "pending", Checks: "none"},
		},
		{
			name: "closed without merge", pr: `{"state":"closed","merged":false}`,
			want: TodoPRStatus{State: "closed", Review: "pending", Checks: "none"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body string
				switch r.URL.RequestURI() {
				case "/repos/example/app/pulls/12":
					body = tc.pr
				case "/repos/example/app/pulls/12/reviews?per_page=100":
					body = tc.reviews
				case "/repos/example/app/commits/head/status":
					body = tc.statuses
				case "/repos/example/app/commits/head/check-runs?per_page=100&filter=latest":
					body = tc.runs
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.Error(w, "unexpected", http.StatusNotFound)
					return
				}
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			got, err := (githubForge{base: srv.URL, client: srv.Client()}).PRStatus(context.Background(), "example/app", 12)
			if err != nil || got != tc.want {
				t.Fatalf("PR status = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestPRStatusPollReturnsTerminalPRToSupervision(t *testing.T) {
	for _, terminal := range []string{"merged", "closed"} {
		t.Run(terminal, func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			ctx := context.Background()
			url := "https://github.com/example/app/pull/12"
			s.setTaskStage(ctx, task.ID, "review", url)
			if _, err := reviewErr(s, task.ID, mergePark(url)); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			f := &statusForge{status: TodoPRStatus{State: "open", Checks: "success", Review: "required"}}
			s.deploy.forge = f
			s.sweepPRStatuses(ctx, now)
			got := todoNow(s, task.ID)
			if got.DoneAt != nil || got.Supervision.State != reviewParked || got.PRStatus == nil || got.PRStatus.State != "open" {
				t.Fatalf("ready PR lost its open parked task: %+v", got)
			}
			s.sweepPRStatuses(ctx, now.Add(time.Minute))
			if f.statusCalls != 1 {
				t.Fatalf("polled a fresh observation %d times", f.statusCalls)
			}
			f.status.State = terminal
			s.sweepPRStatuses(ctx, now.Add(deployRecheck))
			got = todoNow(s, task.ID)
			if got.DoneAt != nil || got.PRStatus.State != terminal || got.Supervision.State != reviewPending || got.Supervision.Parked != nil {
				t.Fatalf("terminal PR did not return to review, or auto-closed: %+v, %+v", got, got.Supervision)
			}
			if terminal == "merged" && got.Stage != "merge" {
				t.Fatalf("merged PR stage = %q", got.Stage)
			}
			if terminal == "closed" && got.Stage != "review" {
				t.Fatalf("closed PR advanced as if merged: %q", got.Stage)
			}
			b := &s.supervise
			b.mu.Lock()
			found := false
			for _, signal := range b.pending {
				found = found || signal.Todo == task.ID && signal.Kind == sigReview
			}
			b.mu.Unlock()
			if !found {
				t.Fatal("no supervisor wake was queued")
			}
			raw, _ := json.Marshal(got)
			if !strings.Contains(string(raw), `"pr_status"`) {
				t.Fatal("PR facts not published on Todo")
			}
		})
	}
}

func TestPRStatusFailurePreservesLastObservation(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	s.setTaskStage(ctx, task.ID, "review", "https://github.com/example/app/pull/12")
	f := &statusForge{status: TodoPRStatus{State: "open", Checks: "failure"}}
	s.deploy.forge = f
	s.sweepPRStatuses(ctx, now)
	before := *todoNow(s, task.ID).PRStatus
	f.statusErr = errors.New("forge unavailable")
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	got := todoNow(s, task.ID)
	if got.PRStatus == nil || *got.PRStatus != before || got.Supervision.State == reviewPending {
		t.Fatalf("failure replaced facts or triggered acceptance: %+v", got)
	}
	s.sweepPRStatuses(ctx, now.Add(deployRecheck+time.Minute))
	if f.statusCalls != 2 {
		t.Fatalf("an outage bypassed the polling interval: %d calls", f.statusCalls)
	}
}

func TestPRStatusDoesNotWakeAnUnrelatedLaterPark(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	s.setTaskStage(ctx, task.ID, "deploy", "https://github.com/example/app/pull/12")
	if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	f := &statusForge{status: TodoPRStatus{State: "merged"}}
	s.deploy.forge = f
	s.sweepPRStatuses(ctx, now)
	if got := todoNow(s, task.ID); got.Supervision.State != reviewParked || got.DoneAt != nil {
		t.Fatalf("an already merged task's unrelated park was disturbed: %+v", got)
	}
}

func TestPRStatusPassDoesNotNeedRunningHubCommit(t *testing.T) {
	s, _, task := newSupervisedService(t)
	s.setTaskStage(context.Background(), task.ID, "review", "https://github.com/example/app/pull/12")
	f := &statusForge{status: TodoPRStatus{State: "open", Checks: "success"}}
	s.deploy.forge = f
	s.deploy.hubBuild = func(context.Context) (string, string, error) {
		return "", "", errors.New("hub build unavailable")
	}
	s.sweepDeploys(time.Now())
	if got := todoNow(s, task.ID); got.PRStatus == nil || got.PRStatus.State != "open" || f.statusCalls != 1 {
		t.Fatalf("PR observations depend on the hub's repository/build: calls=%d, facts=%+v", f.statusCalls, got.PRStatus)
	}
}

func TestPRStatusIgnoresAReplacedPR(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx := context.Background()
	oldURL, newURL := "https://github.com/example/app/pull/12", "https://github.com/example/app/pull/13"
	s.setTaskStage(ctx, task.ID, "review", oldURL)
	old := todoNow(s, task.ID)
	if _, err := reviewErr(s, task.ID, mergePark(oldURL)); err != nil {
		t.Fatal(err)
	}
	s.setTaskStage(ctx, task.ID, "review", newURL)
	if err := s.applyPRStatus(ctx, old, TodoPRStatus{URL: oldURL, State: "merged", CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, task.ID); got.PRStatus != nil || got.Supervision.State != reviewParked || got.PRURL != newURL {
		t.Fatalf("a stale response changed the replacement PR's task: %+v", got)
	}
}

// mergePark parks a task on the maintainers merging the PR at url, naming it as a supervisor does.
func mergePark(url string) map[string]any {
	return map[string]any{"verdict": "parked", "reason": "Waiting for the maintainers to review and merge " + url,
		"waiting_on": "someone_else", "until": "3d", "evidence": testParkEvidence("merge_review", "named maintainer", url,
			"The named maintainer submits a review or merges the PR")}
}

func TestParkWaitsOnPR(t *testing.T) {
	url := "https://github.com/example/app/pull/12"
	for reason, want := range map[string]bool{
		"Waiting for the maintainers to merge " + url:                   true,
		"Waiting for review of https://github.com/Example/App/pull/12":  true,
		"PR #12 waits for the maintainer":                               true,
		"The design team has to pick the icon set first":                false,
		"Waiting for https://github.com/example/app/pull/120 to merge":  false,
		"Waiting for https://github.com/example/other/pull/12 to merge": false,
		"Release 2.1 has to ship first (#120)":                          false,
	} {
		if got := parkWaitsOnPR(&TaskReview{Verdict: reviewParked, WaitingOn: parkSomeoneElse, Reason: reason}, url); got != want {
			t.Errorf("%q: %v, want %v", reason, got, want)
		}
	}
	if parkWaitsOnPR(&TaskReview{Verdict: reviewParked, WaitingOn: parkDate, Reason: "PR #12"}, url) {
		t.Error("a date park waits on the PR")
	}
}

// A park for something else, made before the PR was seen open or after, is not the PR's merge wait: the PR merging
// or closing leaves it parked and queues no review (#7614 review).
func TestPRStatusTerminalPRLeavesAnUnrelatedParkAlone(t *testing.T) {
	for _, when := range []string{"before", "after"} {
		t.Run(when, func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			ctx, now := context.Background(), time.Now()
			s.setTaskStage(ctx, task.ID, "review", "https://github.com/example/app/pull/12")
			park := func() {
				if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
					t.Fatal(err)
				}
			}
			if when == "before" {
				park()
			}
			f := &statusForge{status: TodoPRStatus{State: "open", Checks: "success"}}
			s.deploy.forge = f
			s.sweepPRStatuses(ctx, now)
			if when == "after" {
				park()
			}
			f.status.State = "merged"
			s.sweepPRStatuses(ctx, now.Add(deployRecheck))
			got := todoNow(s, task.ID)
			if got.PRStatus == nil || got.PRStatus.State != "merged" {
				t.Fatalf("merged PR facts not kept: %+v", got.PRStatus)
			}
			if got.Supervision.State != reviewParked || pendingReviewSignal(s, task.ID) {
				t.Fatalf("a merged PR woke a park for something else: %+v", got.Supervision)
			}
		})
	}
}

// A merged observation moves the stage only while the open to-do and its stage still name that PR: one replaced, or a
// to-do closed, between the facts being kept and the stage update stays where it is (#7614 review).
func TestPRStatusMergedStageNeedsTheSamePR(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx := context.Background()
	oldURL, newURL := "https://github.com/example/app/pull/12", "https://github.com/example/app/pull/13"
	s.setTaskStage(ctx, task.ID, "review", oldURL)
	s.setTaskStage(ctx, task.ID, "review", newURL) // the replacement lands after the old PR's facts were applied
	s.setTaskStageOn(ctx, task.ID, "merge", "", "", false, oldURL)
	if got := todoNow(s, task.ID); got.Stage != "review" || got.PRURL != newURL {
		t.Fatalf("a replaced PR's merge moved the stage: %q (%s)", got.Stage, got.PRURL)
	}
	s.setTaskStageOn(ctx, task.ID, "merge", "", "", false, newURL)
	if got := todoNow(s, task.ID); got.Stage != "merge" {
		t.Fatalf("the current PR's merge did not move the stage: %q", got.Stage)
	}
}

// pendingReviewSignal says whether a supervisor wake for task id's review is queued.
func pendingReviewSignal(s *Service, id string) bool {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, signal := range b.pending {
		if signal.Todo == id && signal.Kind == sigReview {
			return true
		}
	}
	return false
}

// A terminal PR wakes only a park waiting on an outside event or someone else: a task at work (no review yet) keeps
// its state, and no review is queued (#7614).
func TestPRStatusTerminalPRLeavesAnUnparkedTaskAlone(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	s.setTaskStage(ctx, task.ID, "review", "https://github.com/example/app/pull/12")
	f := &statusForge{status: TodoPRStatus{State: "open", Checks: "success"}}
	s.deploy.forge = f
	s.sweepPRStatuses(ctx, now)
	before := todoNow(s, task.ID).Supervision.State
	f.status.State = "merged"
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	got := todoNow(s, task.ID)
	if got.PRStatus == nil || got.PRStatus.State != "merged" {
		t.Fatalf("merged PR facts not kept: %+v", got.PRStatus)
	}
	if got.Supervision.State != before || got.Supervision.State == reviewPending || pendingReviewSignal(s, task.ID) {
		t.Fatalf("a merged PR changed an unparked task's supervision %q -> %q", before, got.Supervision.State)
	}
}

// The stage's PR is checked before the supervision: an observation of a PR replaced while it was read (the to-do
// still names the old one, the stage the new) neither wakes the park nor keeps its facts (#7614).
func TestPRStatusReplacedInFlightDoesNotWakeTheSupervisor(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx := context.Background()
	oldURL, newURL := "https://github.com/example/app/pull/12", "https://github.com/example/app/pull/13"
	s.setTaskStage(ctx, task.ID, "review", oldURL)
	if _, err := reviewErr(s, task.ID, mergePark(oldURL)); err != nil {
		t.Fatal(err)
	}
	old := todoNow(s, task.ID)
	b := &s.reports
	b.mu.Lock()
	st := b.stages[task.ID]
	st.PR = newURL // the replacement landed in the stages; the to-do is published next
	b.stages[task.ID] = st
	b.mu.Unlock()
	if err := s.applyPRStatus(ctx, old, TodoPRStatus{URL: oldURL, State: "merged", CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, task.ID); got.Supervision.State != reviewParked || pendingReviewSignal(s, task.ID) {
		t.Fatalf("a replaced PR's observation woke the supervisor: %+v", got.Supervision)
	}
}

type hangingStatusForge struct {
	fakeForge
	called func()
}

func (f *hangingStatusForge) PRStatus(ctx context.Context, _ string, _ int) (TodoPRStatus, error) {
	f.called()
	<-ctx.Done()
	return TodoPRStatus{}, ctx.Err()
}

// PR observations come after the deploy checks, on a deadline of their own: a hanging forge read neither runs
// before the hub's commit is read nor holds the pass for the deploy checks' two minutes (#7614).
func TestPRStatusPassRunsAfterTheDeployChecksOnItsOwnDeadline(t *testing.T) {
	s, _, task := newSupervisedService(t)
	s.setTaskStage(context.Background(), task.ID, "review", "https://github.com/example/app/pull/12")
	var mu sync.Mutex
	var order []string
	note := func(what string) { mu.Lock(); order = append(order, what); mu.Unlock() }
	s.deploy.forge = &hangingStatusForge{called: func() { note("pr") }}
	s.deploy.hubBuild = func(context.Context) (string, string, error) {
		note("hub")
		return "", "", errors.New("hub build unavailable")
	}
	old := prStatusPassMost
	prStatusPassMost = 50 * time.Millisecond
	t.Cleanup(func() { prStatusPassMost = old })
	start := time.Now()
	s.sweepDeploys(time.Now())
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("a hanging PR read held the pass for %s", took)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "hub,pr" {
		t.Fatalf("order %v, want the hub's commit before the PR reads", order)
	}
}

func newPRConflictTask(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	task, err := s.createTodo(ctx, "Resolve merge conflicts", "Keep the current task PR mergeable.", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task.ID, "conv1", []string{"The task PR is merged."}, 0)
	s.setTaskStage(ctx, task.ID, "review", "https://github.com/example/app/pull/12")
	return s, f, todoNow(s, task.ID)
}

func pendingPRConflict(s *Service, id string) (superviseSignal, bool) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.pending[(superviseSignal{Todo: id, Kind: sigPRConflict}).id()]
	return g, ok
}

// Advance both the shared and recipient clocks without sleeps or a timer racing the state changes under test.
func sendPendingPRConflicts(s *Service) {
	b := &s.supervise
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer, b.timerAt = nil, time.Time{}
	clear(b.told)
	clear(b.batch)
	for id, g := range b.pending {
		g.batchTo, g.batchUntil = "", time.Time{}
		g.At = time.Now().Add(-2 * time.Hour)
		b.pending[id] = g
	}
	b.last = time.Time{}
	b.mu.Unlock()
	s.sendDigest(context.Background())
}

func TestPRStatusConflictSignalsWithoutRunningAgents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status TodoPRStatus
		want   bool
	}{
		{"conflicting", TodoPRStatus{State: "open", HeadOID: "head-a", Conflicts: true}, true},
		{"clean", TodoPRStatus{State: "open", HeadOID: "head-a"}, false},
		{"failed checks only", TodoPRStatus{State: "open", HeadOID: "head-a", Checks: "failure"}, false},
		{"closed", TodoPRStatus{State: "closed", HeadOID: "head-a", Conflicts: true}, false},
		{"merged", TodoPRStatus{State: "merged", HeadOID: "head-a", Conflicts: true}, false},
		{"missing head", TodoPRStatus{State: "open", Conflicts: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, task := newPRConflictTask(t, nil)
			if agents := s.taskAgents(context.Background(), task); len(agents) != 0 {
				t.Fatalf("fixture has task agents: %+v", agents)
			}
			s.deploy.forge = &statusForge{status: tc.status}
			s.sweepPRStatuses(context.Background(), time.Now())
			g, ok := pendingPRConflict(s, task.ID)
			if ok != tc.want {
				t.Fatalf("conflict signal = %+v, %v; want %v", g, ok, tc.want)
			}
			if tc.want && (!g.urgent() || g.level() != supervisionTriage) {
				t.Fatalf("conflict is not actionable and urgent: %+v", g)
			}
			sendPendingPRConflicts(s)
			notices := f.notices()
			if !tc.want {
				if len(notices) != 0 {
					t.Fatalf("non-conflict notified the supervisor: %q", notices)
				}
				return
			}
			if len(notices) != 1 || !strings.Contains(notices[0], task.PRURL) ||
				!strings.Contains(notices[0], "head-a") || !strings.Contains(notices[0], "PR 有合并冲突") {
				t.Fatalf("supervisor did not receive the conflict facts: %q", notices)
			}
		})
	}
}

func TestPRStatusConflictQueuedSignalRechecksCurrentFacts(t *testing.T) {
	for _, changed := range []string{"resolved", "closed", "new head", "new task PR", "new stage PR", "completed", "no status"} {
		t.Run(changed, func(t *testing.T) {
			s, f, task := newPRConflictTask(t, nil)
			ctx := context.Background()
			status := TodoPRStatus{URL: task.PRURL, State: "open", HeadOID: "head-a", Conflicts: true, CheckedAt: time.Now()}
			if err := s.applyPRStatus(ctx, task, status); err != nil {
				t.Fatal(err)
			}
			g, ok := pendingPRConflict(s, task.ID)
			if !ok {
				t.Fatal("no conflict queued before the facts changed")
			}
			switch changed {
			case "resolved", "closed", "new head":
				switch changed {
				case "resolved":
					status.Conflicts = false
				case "closed":
					status.State = "closed"
				case "new head":
					status.HeadOID = "head-b"
				}
				if err := s.applyPRStatus(ctx, task, status); err != nil {
					t.Fatal(err)
				}
			case "new task PR":
				s.setTaskStage(ctx, task.ID, "review", "https://github.com/example/app/pull/13")
			case "new stage PR":
				b := &s.reports
				b.mu.Lock()
				stage := b.stages[task.ID]
				stage.PR = "https://github.com/example/app/pull/13"
				b.stages[task.ID] = stage
				b.mu.Unlock()
			case "completed", "no status":
				s.todos.mu.Lock()
				if changed == "completed" {
					now := time.Now()
					s.todos.todos[task.ID].DoneAt = &now
				} else {
					s.todos.todos[task.ID].PRStatus = nil
				}
				s.todos.mu.Unlock()
			}
			if _, holds := s.digestEntry(ctx, g, ""); holds {
				t.Fatal("the old queued conflict still holds")
			}
			if s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, "", time.Now(), time.Time{}) ||
				len(f.notices()) != 0 {
				t.Fatalf("a stale queued conflict was delivered: %q", f.notices())
			}
			if changed == "resolved" {
				sendPendingPRConflicts(s)
				if len(f.notices()) != 0 {
					t.Fatal("resolved conflict escaped the pending queue recheck")
				}
			}
		})
	}
}

func TestPRStatusConflictIgnoresStalePRAndFailedPersistence(t *testing.T) {
	for _, changed := range []string{"task PR", "stage PR", "persistence"} {
		t.Run(changed, func(t *testing.T) {
			st := &memStore{}
			s, _, task := newPRConflictTask(t, st)
			switch changed {
			case "task PR":
				s.setTaskStage(context.Background(), task.ID, "review", "https://github.com/example/app/pull/13")
			case "stage PR":
				b := &s.reports
				b.mu.Lock()
				stage := b.stages[task.ID]
				stage.PR = "https://github.com/example/app/pull/13"
				b.stages[task.ID] = stage
				b.mu.Unlock()
			case "persistence":
				st.mu.Lock()
				st.failPut = errors.New("synthetic stage write failure")
				st.mu.Unlock()
			}
			err := s.applyPRStatus(context.Background(), task, TodoPRStatus{
				URL: task.PRURL, State: "open", HeadOID: "head-a", Conflicts: true, CheckedAt: time.Now(),
			})
			if (err != nil) != (changed == "persistence") {
				t.Fatalf("apply error = %v", err)
			}
			if g, ok := pendingPRConflict(s, task.ID); ok {
				t.Fatalf("uncommitted or stale facts signalled: %+v", g)
			}
			if got := todoNow(s, task.ID); got.PRStatus != nil {
				t.Fatalf("uncommitted or stale facts published: %+v", got.PRStatus)
			}
		})
	}
}

func TestPRStatusConflictTellsEachHeadOnlyOnce(t *testing.T) {
	st := &memStore{}
	s, f, task := newPRConflictTask(t, st)
	for i, head := range []string{"head-a", "head-a", "head-b", "head-a"} {
		s.deploy.forge = &statusForge{status: TodoPRStatus{State: "open", HeadOID: head, Conflicts: true}}
		s.sweepPRStatuses(context.Background(), time.Now().Add(time.Duration(i)*deployRecheck))
		sendPendingPRConflicts(s)
		want := 1
		if i >= 2 {
			want = 2
		}
		if notices := f.notices(); len(notices) != want {
			t.Fatalf("head %s at pass %d: %d notices; want %d: %q", head, i, len(notices), want, notices)
		}
	}
	if got := todoNow(s, task.ID); got.PRStatus.HeadOID != "head-a" {
		t.Fatalf("the current observation did not return to head-a: %+v", got.PRStatus)
	}
}

func TestPRStatusConflictResolvedWhileSupervisorBusyIsNotDelivered(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing relay %v", existing), func(t *testing.T) {
			s, f, task := newPRConflictTask(t, nil)
			ctx := context.Background()
			supervisor, ok := s.session(ctx, "server", "conv1")
			if !ok {
				t.Fatal("missing supervisor")
			}
			supervisor.Status = "working"
			f.setSessions("server", supervisor)
			want := 0
			if existing {
				s.signal(superviseSignal{Todo: task.ID, Kind: sigStalled, Key: "earlier", Detail: "Earlier status event."})
				sendPendingPRConflicts(s)
				want = 1
			}
			status := TodoPRStatus{URL: task.PRURL, State: "open", HeadOID: "head-a", Conflicts: true, CheckedAt: time.Now()}
			if err := s.applyPRStatus(ctx, task, status); err != nil {
				t.Fatal(err)
			}
			sendPendingPRConflicts(s)
			if _, pending := pendingPRConflict(s, task.ID); !pending || s.queuedFor("conv1") != want || len(f.notices()) != 0 {
				t.Fatal("the conflict was not left pending outside the rendered relay queue")
			}
			for _, msg := range queuedRelays(s, "conv1") {
				if strings.Contains(msg.Text, "head-a") {
					t.Fatal("the conflict was merged into an existing rendered relay")
				}
			}
			status.Conflicts = false
			if err := s.applyPRStatus(ctx, task, status); err != nil {
				t.Fatal(err)
			}
			supervisor.Status = "idle"
			f.setSessions("server", supervisor)
			sessions, err := s.sessions(ctx, "server")
			if err != nil {
				t.Fatal(err)
			}
			s.drainRelays("server", sessions)
			if existing {
				waitFor(t, "the unrelated relay to drain", func() bool { return len(f.notices()) == want })
			}
			sendPendingPRConflicts(s)
			if len(f.notices()) != want {
				t.Fatalf("a resolved conflict escaped while its supervisor was busy: %q", f.notices())
			}
		})
	}
}

func TestPRStatusConflictRetriesAfterBusySupervisor(t *testing.T) {
	st := &memStore{}
	s, f, task := newPRConflictTask(t, st)
	ctx := context.Background()
	supervisor, ok := s.session(ctx, "server", "conv1")
	if !ok {
		t.Fatal("missing supervisor")
	}
	supervisor.Status = "working"
	f.setSessions("server", supervisor)
	if err := s.applyPRStatus(ctx, task, TodoPRStatus{
		URL: task.PRURL, State: "open", HeadOID: "head-a", Conflicts: true, CheckedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sendPendingPRConflicts(s)
	var history map[string]superviseFingerprint
	if _, err := st.Setting(ctx, superviseDedupKey, &history); err != nil || len(history) != 0 {
		t.Fatalf("a busy supervisor permanently consumed the conflict: %+v, %v", history, err)
	}
	supervisor.Status = "idle"
	f.setSessions("server", supervisor)
	sendPendingPRConflicts(s)
	if len(f.notices()) != 1 || s.queuedFor("conv1") != 0 {
		t.Fatalf("the still-current conflict did not retry once: %q", f.notices())
	}
	if _, err := st.Setting(ctx, superviseDedupKey, &history); err != nil || len(history) != 1 {
		t.Fatalf("the delivered conflict was not kept: %+v, %v", history, err)
	}
}

// prsForge answers each PR from its own facts, by number.
type prsForge struct {
	fakeForge
	byNumber map[int]TodoPRStatus
	calls    map[int]int
}

func (f *prsForge) PRStatus(_ context.Context, _ string, n int) (TodoPRStatus, error) {
	if f.calls == nil {
		f.calls = map[int]int{}
	}
	f.calls[n]++
	return f.byNumber[n], nil
}

// Work spanning several PRs (#7935): the poll reads the earlier ones too, the task waits for merge while any of them
// is open, and its supervisor is woken only once the last open one is merged or closed.
func TestPRStatusFollowsEveryPROfTheTask(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	first, second, last := "https://github.com/example/app/pull/11", "https://github.com/example/app/pull/12", "https://github.com/example/app/pull/13"
	for _, u := range []string{first, second, last} {
		s.setTaskStage(ctx, task.ID, "review", u)
	}
	if _, err := reviewErr(s, task.ID, mergePark(last)); err != nil {
		t.Fatal(err)
	}
	f := &prsForge{byNumber: map[int]TodoPRStatus{
		11: {State: "merged"},
		12: {State: "open", Review: "required", Checks: "success"},
		13: {State: "merged"},
	}}
	s.deploy.forge = f
	s.sweepPRStatuses(ctx, now)
	got := todoNow(s, task.ID)
	if f.calls[11] != 1 || f.calls[12] != 1 || f.calls[13] != 1 {
		t.Fatalf("not every PR was read: %v", f.calls)
	}
	if open := triageOpenPRs(got, now); len(open) != 1 || open[0].URL != second {
		t.Fatalf("open PRs = %+v (current %+v, others %+v)", open, got.PRStatus, got.OtherPRs)
	}
	if tr := triageTodo(listedTodo{Todo: got}, noSessions, now, time.Time{}, time.UTC, false); tr.Section != triagePR {
		t.Fatalf("a task whose earlier PR is open is not waiting for merge: %+v (review %q, status %q, open %d)", tr, triageReviewState(got), triageStatus(got, noSessions, now), len(triageOpenPRs(got, now)))
	}
	if got.Supervision.State != reviewParked {
		t.Fatalf("woken while a PR is still open: %+v", got.Supervision)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"other_prs"`) {
		t.Fatal("earlier PR facts not published on Todo")
	}
	// The last open one merges: woken now; a merged PR is not polled again.
	f.byNumber[12] = TodoPRStatus{State: "merged"}
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	got = todoNow(s, task.ID)
	if got.Supervision.State != reviewPending {
		t.Fatalf("last open PR merged without a wake: %+v", got.Supervision)
	}
	if f.calls[11] != 1 {
		t.Fatalf("a merged earlier PR was polled again: %v", f.calls)
	}
}

// A task held at its PRs whose PRs are all found merged the first time they are read is woken once the last one is
// read, not by the first one while the others are still unknown (#7935).
func TestPRStatusWakesAPRHoldOnlyAfterEveryPRIsRead(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	for _, u := range []string{"https://github.com/example/app/pull/11", "https://github.com/example/app/pull/12"} {
		s.setTaskStage(ctx, task.ID, "review", u)
	}
	if _, err := reviewErr(s, task.ID, mergePark("https://github.com/example/app/pull/12")); err != nil {
		t.Fatal(err)
	}
	f := &prsForge{byNumber: map[int]TodoPRStatus{11: {State: "merged"}, 12: {State: "merged"}}}
	s.deploy.forge = f
	s.sweepPRStatuses(ctx, now)
	if got := todoNow(s, task.ID); got.Supervision.State != reviewPending {
		t.Fatalf("all PRs merged, not woken: %+v", got.Supervision)
	}
}

// Only the current PR raises a conflict signal: an earlier PR of the task that conflicts is shown with its blockers,
// but the digest revalidates against the task/stage PR, so a signal for it would only be dropped there (#7935).
func TestPRStatusConflictSignalsOnlyForTheCurrentPR(t *testing.T) {
	s, _, task := newPRConflictTask(t, nil)
	ctx := context.Background()
	current := "https://github.com/example/app/pull/13"
	s.setTaskStage(ctx, task.ID, "review", current)
	s.deploy.forge = &prsForge{byNumber: map[int]TodoPRStatus{
		12: {State: "open", HeadOID: "head-a", Conflicts: true},
		13: {State: "open", HeadOID: "head-b"},
	}}
	s.sweepPRStatuses(ctx, time.Now())
	if g, ok := pendingPRConflict(s, task.ID); ok {
		t.Fatalf("an earlier PR's conflict raised a signal: %+v", g)
	}
	got := todoNow(s, task.ID)
	if len(got.OtherPRs) != 1 || !got.OtherPRs[0].Conflicts {
		t.Fatalf("the earlier PR's conflict was not kept as its fact: %+v", got.OtherPRs)
	}
	s.deploy.forge = &prsForge{byNumber: map[int]TodoPRStatus{
		12: {State: "open", HeadOID: "head-a", Conflicts: true},
		13: {State: "open", HeadOID: "head-b", Conflicts: true},
	}}
	s.sweepPRStatuses(ctx, time.Now().Add(deployRecheck))
	if g, ok := pendingPRConflict(s, task.ID); !ok || g.Key != current+"@head-b" {
		t.Fatalf("the current PR's conflict = %+v, %v", g, ok)
	}
}
