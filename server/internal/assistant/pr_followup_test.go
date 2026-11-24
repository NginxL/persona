package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	fupPR   = "https://github.com/o/r/pull/77"
	fupTodo = "todo_followup_case"
	fupSup  = "conv_supervisor"
)

// followupEnv builds a service on a real Postgres store with a forge that returns a scripted PR status.
func followupEnv(t *testing.T, status TodoPRStatus) (*Service, *statusForge) {
	t.Helper()
	st := repositorystore.New(todoTestDB(t))
	s, _ := newQuietServiceOn(t, st)
	forge := &statusForge{status: status}
	s.deploy.forge = forge
	return s, forge
}

func init() { followupRecheck = 0 } // let every test sweep poll immediately rather than wait the real cadence

func seedFollowup(t *testing.T, s *Service, closedAt time.Time) {
	t.Helper()
	st := s.store().(prFollowupStore)
	// The follow-up table references todos; insert a minimal closed row the foreign key accepts.
	rs, ok := s.store().(*repositorystore.Store)
	if !ok {
		t.Fatalf("test store is not *repositorystore.Store: %T", s.store())
	}
	if err := rs.CreateTodo(context.Background(), db.TodoRow{
		ID: fupTodo, Title: "closed work", CreatedAt: closedAt, UpdatedAt: closedAt, DoneAt: &closedAt, DoneBy: db.DoneByReview,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPRFollowups(context.Background(), fupTodo, fupSup, "conv_origin",
		[]string{fupPR}, closedAt); err != nil {
		t.Fatal(err)
	}
}

func followupRows(t *testing.T, s *Service) []repository.PRFollowup {
	t.Helper()
	rows, err := s.store().(prFollowupStore).ListPRFollowups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func queuedFollowupTools(s *Service) []string {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	var tools []string
	for _, msg := range b.st.Queue[fupSup] {
		if msg.Tool == prFollowupTool {
			tools = append(tools, msg.Text)
		}
	}
	return tools
}

func TestGoldenPRFollowupLanguages(t *testing.T) {
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		for _, event := range []string{"merged", "closed", "conflict", "changes_requested"} {
			text := prompts.RenderPRFollowupNotice(prompts.PRFollowupNotice{
				ID: fupTodo, Title: "Task 原文", PRURL: fupPR, Event: event, Realtime: true,
				Language: answerLanguage(locale),
			})
			if !strings.Contains(text, "Task 原文") || !strings.Contains(text, fupPR) {
				t.Fatalf("%s/%s lost the source title or PR URL: %s", locale, event, text)
			}
			golden(t, "pr_followup."+event+"."+locale, text)
		}
	}
}

func TestPRFollowupTerminalNoticeRequiresPostClosureTransition(t *testing.T) {
	for _, tc := range []struct {
		name, terminalField string
		eventOffset         time.Duration
		merged, sawOpen     bool
		want                string
	}{
		{name: "merged before closure", merged: true, terminalField: "merged_at", eventOffset: -time.Minute},
		{name: "first observation is undated and merged", merged: true},
		{name: "first observation is undated and closed"},
		{name: "merged after closure before first poll", merged: true, terminalField: "merged_at", eventOffset: time.Minute, want: "已合并"},
		{name: "closed after closure before first poll", terminalField: "closed_at", eventOffset: time.Minute, want: "未合并"},
		{name: "observed open then merged without a timestamp", sawOpen: true, merged: true, want: "已合并"},
		{name: "observed open then merged in the closure second", sawOpen: true, merged: true, terminalField: "merged_at", want: "已合并"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, f := newQuietServiceOn(t, repositorystore.New(todoTestDB(t)))
			f.mu.Lock()
			f.known[fupSup] = true
			f.mu.Unlock()
			f.setSessions("server", proto.Session{SessionID: fupSup, MachineID: "server", Assistant: true, Status: proto.StatusIdle})
			closedAt := time.Now().UTC().Truncate(time.Second)
			seedFollowup(t, s, closedAt)
			if tc.sawOpen {
				s.deploy.forge = &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h1"}}
				s.sweepPRFollowups(ctx, closedAt.Add(100*time.Millisecond))
			}
			pr := map[string]any{"state": "closed", "merged": tc.merged}
			if tc.terminalField != "" {
				pr[tc.terminalField] = closedAt.Add(tc.eventOffset)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/o/r/pulls/77" {
					t.Errorf("unexpected forge call: %s", r.URL.Path)
				}
				if err := json.NewEncoder(w).Encode(pr); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(srv.Close)
			s.deploy.forge = githubForge{base: srv.URL, client: srv.Client()}
			s.sweepPRFollowups(ctx, closedAt.Add(2*time.Minute))
			s.sweepPRFollowups(ctx, closedAt.Add(3*time.Minute))
			if rows := followupRows(t, s); len(rows) != 0 {
				t.Fatalf("terminal PR left %d follow-up subscriptions", len(rows))
			}
			if queued := queuedFollowupTools(s); len(queued) != 0 {
				t.Fatalf("idle supervisor has queued follow-ups: %v", queued)
			}
			got := f.sentTexts(fupSup)
			if tc.want == "" && len(got) != 0 {
				t.Fatalf("preexisting terminal state woke the supervisor: %v", got)
			}
			if tc.want != "" {
				if len(got) != 1 || !strings.Contains(got[0], tc.want) {
					t.Fatalf("expected one %q notice, got %v", tc.want, got)
				}
				assertSentSource(t, f, &proto.MessageSource{Kind: proto.SourceNotice, Title: "Persona"})
			}
		})
	}
}

func TestPRFollowupConflictNotifiesOncePerHeadAndKeepsTodoClosed(t *testing.T) {
	s, forge := followupEnv(t, TodoPRStatus{State: "open", HeadOID: "h1"})
	now := time.Now()
	seedFollowup(t, s, now.Add(-time.Minute))
	s.sweepPRFollowups(t.Context(), now.Add(-time.Second))
	forge.status.Conflicts = true
	s.sweepPRFollowups(context.Background(), now)
	rows := followupRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("conflict should keep the subscription, got %d rows", len(rows))
	}
	if rows[0].NotifiedConflictHead != "h1" {
		t.Fatalf("conflict head not recorded: %+v", rows[0])
	}
	if got := queuedFollowupTools(s); len(got) != 1 || !strings.Contains(got[0], "冲突") {
		t.Fatalf("expected one conflict notice, got %v", got)
	}
	// A second sweep on the same conflicting head must not re-wake the supervisor.
	s.sweepPRFollowups(context.Background(), now.Add(time.Minute))
	if got := queuedFollowupTools(s); len(got) != 1 {
		t.Fatalf("same-head conflict re-notified: %d notices", len(got))
	}
	// A new head with a conflict wakes once again.
	s.deploy.forge = &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h2", Conflicts: true, CheckedAt: time.Now()}}
	s.sweepPRFollowups(context.Background(), now.Add(2*time.Minute))
	if got := queuedFollowupTools(s); len(got) != 2 {
		t.Fatalf("new-head conflict did not re-notify: %d notices", len(got))
	}
}

func TestPRFollowupCurrentHeadChangesRequestedNotifies(t *testing.T) {
	now := time.Now()
	s, forge := followupEnv(t, TodoPRStatus{State: "open", HeadOID: "h1"})
	seedFollowup(t, s, now.Add(-time.Minute))
	s.sweepPRFollowups(t.Context(), now.Add(-time.Second))
	forge.status.Review, forge.status.ChangesRequestedAt = "changes_requested", &now
	s.sweepPRFollowups(t.Context(), now)
	if got := queuedFollowupTools(s); len(got) != 1 || !strings.Contains(got[0], "changes-requested") {
		t.Fatalf("expected one changes-requested notice, got %v", got)
	}
	rows := followupRows(t, s)
	if len(rows) != 1 || rows[0].NotifiedReviewHead != "h1" {
		t.Fatalf("review head not recorded: %+v", rows)
	}
}

func TestPRFollowupForgeUnavailableDoesNotPretendToWatch(t *testing.T) {
	s, _ := followupEnv(t, TodoPRStatus{})
	s.deploy.forge = &statusForge{statusErr: errGitHubUnconfigured}
	seedFollowup(t, s, time.Now())
	s.sweepPRFollowups(context.Background(), time.Now())
	rows := followupRows(t, s)
	if len(rows) != 1 || rows[0].LastObservedAt != nil {
		t.Fatalf("unavailable forge must leave the subscription unobserved, got %+v", rows)
	}
	if got := queuedFollowupTools(s); len(got) != 0 {
		t.Fatalf("unavailable forge sent a notice: %v", got)
	}
}

func TestCreatePRFollowupsOnAcceptedTodo(t *testing.T) {
	s, _ := followupEnv(t, TodoPRStatus{State: "open"})
	rs := s.store().(*repositorystore.Store)
	doneAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if err := rs.CreateTodo(context.Background(), db.TodoRow{
		ID: fupTodo, Title: "closed work", CreatedAt: doneAt, UpdatedAt: doneAt, DoneAt: &doneAt, DoneBy: db.DoneByReview,
	}); err != nil {
		t.Fatal(err)
	}
	const mergedPR = "https://github.com/o/r/pull/76"
	todo := Todo{
		ID: fupTodo, Title: "closed work", Origin: "conv_origin", DoneAt: &doneAt,
		PRURL: fupPR, PRs: []string{fupPR, mergedPR}, OtherPRs: []TodoPRStatus{{URL: mergedPR, State: "merged"}},
		Supervision: &Supervision{Supervisor: fupSup},
	}
	s.createPRFollowups(context.Background(), todo)
	rows := followupRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("expected one follow-up for the open delivery PR, got %d", len(rows))
	}
	if rows[0].Supervisor != fupSup || rows[0].PRURL != fupPR || !rows[0].CreatedAt.Equal(doneAt) {
		t.Fatalf("follow-up recorded wrong fields: %+v", rows[0])
	}
	// Re-accepting is idempotent.
	s.createPRFollowups(context.Background(), todo)
	if rows := followupRows(t, s); len(rows) != 1 {
		t.Fatalf("re-acceptance duplicated the follow-up: %d", len(rows))
	}
}

// End to end: accepting a to-do whose delivery PR is still open creates the follow-up subscription through the real
// review/close path (the hook in reviewDoneTodo), without reopening the to-do. Without that hook this fails.
func TestAcceptedTodoCreatesFollowupThroughReviewPath(t *testing.T) {
	st := repositorystore.New(todoTestDB(t))
	s, _, todo := newSupervisedServiceOn(t, st)
	setDeliveryGate(t, s, deliveryGateOff) // a to-do may close while its PR is still open; the follow-up hook is gate-independent
	s.deploy.forge = &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h1"}}
	s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
	s.deploy.listPaths = []string{}
	s.setTaskStage(context.Background(), todo.ID, "review", fupPR)
	if _, err := acceptReview(s, todo.ID, map[string]any{"link": fupPR}); err != nil {
		t.Fatalf("acceptance: %v", err)
	}
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("the to-do did not close")
	}
	rows, err := st.ListPRFollowups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].PRURL != fupPR {
		t.Fatalf("accepted to-do created %+v, want one follow-up for %s", rows, fupPR)
	}
}
