package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type skipFixtureStore struct {
	memStore
	failBatch atomic.Bool
}

func (st *skipFixtureStore) PutSettings(ctx context.Context, values map[string]any) error {
	if st.failBatch.Load() {
		return errors.New("injected failure persisting owner skip and schedule")
	}
	return st.memStore.PutSettings(ctx, values)
}

func skipFixture(t *testing.T, task Task) (*Service, *fakeHub, *skipFixtureStore) {
	t.Helper()
	st := &skipFixtureStore{}
	if err := st.PutSetting(t.Context(), tasksKey, []Task{task}); err != nil {
		t.Fatal(err)
	}
	f := readyHub()
	f.settings = &Settings{Timezone: "UTC"}
	return newServiceOn(t, f, st), f, st
}

func skipRequest(t *testing.T, s *Service, method, path, role, body string, status int) Task {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/assistant/tasks"+path, strings.NewReader(body))
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s got %d, want %d: %s", method, path, w.Code, status, w.Body)
	}
	var out struct {
		Task  Task   `json:"task"`
		Tasks []Task `json:"tasks"`
	}
	if status == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Tasks) == 1 {
			return out.Tasks[0]
		}
	}
	return out.Task
}

func markSkip(t *testing.T, s *Service, id string, at time.Time) Task {
	t.Helper()
	return skipRequest(t, s, "POST", "/"+id+"/skip-next", "", fmt.Sprintf(`{"at":%q}`, at.Format(time.RFC3339Nano)), 200)
}

func undoSkipPath(task Task) string {
	query := url.Values{"at": {task.SkipNextAt.Format(time.RFC3339Nano)}, "token": {task.SkipNextToken}}
	return "/" + task.ID + "/skip-next?" + query.Encode()
}

func TestScheduleSkipNextOwnerAndOccurrenceFence(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second).Add(time.Hour + 123456789)
	task := Task{ID: "interval", Title: "Status report", Prompt: "Review progress", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, At: at}
	s, f, st := skipFixture(t, task)
	path := "/interval/skip-next"
	valid := fmt.Sprintf(`{"at":%q}`, at.Format(time.RFC3339Nano))
	if row := skipRequest(t, s, "GET", "", "", "", 200); !row.CanSkipNext || !row.NextRun.Equal(at) {
		t.Fatalf("an active interval's view lacks its available skip: %+v", row)
	}
	for _, body := range []string{`{}`, `{"at":null}`, `{"at":"next week"}`, valid + `{}`, `{"at":"2040-01-01T00:00:00Z","enabled":false}`} {
		skipRequest(t, s, "POST", path, "", body, 400)
	}
	skipRequest(t, s, "POST", "/gone/skip-next", "", valid, 404)
	skipRequest(t, s, "POST", path, "answer", valid, 403)
	skipRequest(t, s, "POST", path, "", fmt.Sprintf(`{"at":%q}`, at.Add(time.Second).Format(time.RFC3339Nano)), 409)
	first := markSkip(t, s, task.ID, at)
	if !first.CanSkipNext || !first.SkipNextAt.Equal(at) || first.SkipNextToken == "" || first.SkipNextZone != "" {
		t.Fatalf("owner did not receive its pending marker: %+v", first)
	}
	if row := skipRequest(t, s, "GET", "", "answer", "", 200); row.CanSkipNext || row.SkipNextToken != "" || !row.SkipNextAt.Equal(at) {
		t.Fatalf("read-only task view has the wrong authority: %+v", row)
	}
	if retry := markSkip(t, s, task.ID, at); retry.SkipNextToken != first.SkipNextToken {
		t.Fatal("marking the same occurrence again replaced the pending operation")
	}
	restarted := newServiceOn(t, f, st)
	if row := skipRequest(t, restarted, "GET", "", "", "", 200); !row.SkipNextAt.Equal(at) || row.SkipNextToken != first.SkipNextToken {
		t.Fatalf("pending skip changed across restart: %+v", row)
	}
	skipRequest(t, restarted, "DELETE", undoSkipPath(first), "answer", "", 403)
	skipRequest(t, restarted, "DELETE", undoSkipPath(first), "", "", 200)
	second := markSkip(t, restarted, task.ID, at)
	if second.SkipNextToken == first.SkipNextToken {
		t.Fatal("another owner skip of the same occurrence reused its old token")
	}
	skipRequest(t, restarted, "DELETE", undoSkipPath(first), "", "", 409)
	last := skipRequest(t, restarted, "DELETE", undoSkipPath(second), "", "", 200)
	if !last.SkipNextAt.IsZero() || last.SkipNextToken != "" || !last.Enabled || !last.NextRun.Equal(at) {
		t.Fatalf("cancel changed recurrence or retained a marker: %+v", last)
	}
	if _, runs := scheduleState(restarted); len(runs) != 0 {
		t.Fatalf("a mark/cancel recorded an execution: %+v", runs)
	}
}

func TestScheduleSkipNextPreservedAcrossOrdinaryAndConcurrentEdits(t *testing.T) {
	at := time.Now().UTC().Add(time.Hour)
	old := Task{ID: "interval", Title: "Status report", Prompt: "Review progress", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, At: at}
	s, _, _ := skipFixture(t, old)
	saved := markSkip(t, s, old.ID, at)
	body := fmt.Sprintf(`{"title":"Rename","prompt":"Review progress","repeat":"interval","every_minutes":30,"at":%q,"enabled":true,"skip_next_at":"2040-01-01T00:00:00Z","skip_next_token":"forged"}`, at.Format(time.RFC3339Nano))
	row := skipRequest(t, s, "PUT", "/"+old.ID, "", body, 200)
	if row.Title != "Rename" || row.SkipNextToken != saved.SkipNextToken || !row.SkipNextAt.Equal(at) {
		t.Fatalf("an ordinary PUT did not preserve the real marker: %+v", row)
	}
	// A title edit was read before the marker was set. Validation can be slow and must not erase a newer marker.
	cur := old
	cur.SkipNextAt, cur.SkipNextToken = saved.SkipNextAt, saved.SkipNextToken
	edited := old
	edited.Title = "Concurrent rename"
	if err := mergeTaskEditRuntime(&edited, old, cur); err != nil || edited.SkipNextToken != saved.SkipNextToken {
		t.Fatalf("ordinary concurrent edit lost marker: %+v %v", edited, err)
	}
	for _, change := range []func(*Task){
		func(task *Task) { task.Enabled = false },
		func(task *Task) { task.EveryMinutes = 45 },
		func(task *Task) { task.At = at.Add(time.Minute) },
		func(task *Task) { task.Repeat = repeatOnce },
		func(task *Task) { task.Until = at },
	} {
		edited := cur
		change(&edited)
		rearm(cur, &edited, time.Now())
		if !edited.SkipNextAt.IsZero() || edited.SkipNextToken != "" {
			t.Fatalf("rule or availability changed but the marker survived: %+v", edited)
		}
	}
	// A pre-run edit cannot resurrect a marker consumed while the edit was being checked.
	consumed := cur
	consumed.consumeOwnerSkip("en")
	edited = cur
	edited.Title = "Rename after consumption"
	if err := mergeTaskEditRuntime(&edited, cur, consumed); err != nil || !edited.SkipNextAt.IsZero() || !edited.At.Equal(consumed.At) {
		t.Fatalf("edit attempted to restore consumed skip or former interval cursor: %+v %v", edited, err)
	}
}

func TestScheduleSkipNextConsumesOnlyOneOccurrence(t *testing.T) {
	for _, repeat := range []string{repeatDaily, repeatInterval, repeatCron} {
		t.Run(repeat, func(t *testing.T) {
			now := time.Now().UTC()
			minute := (now.Minute() + 15) % 30
			task := Task{ID: "plan", Title: "Follow progress", Prompt: "Check the work.", Repeat: repeat, Enabled: true,
				At: now.Add(time.Hour), Time: now.Add(2 * time.Hour).Format("15:04"), EveryMinutes: 30,
				Cron: fmt.Sprintf("%d,%d * * * *", minute, minute+30)}
			s, f, st := skipFixture(t, task)
			ctx := t.Context()
			at := task.next(now, time.UTC)
			markSkip(t, s, task.ID, at)
			if f.sent() != 0 {
				t.Fatal("marking a skip dispatched a task")
			}
			s.runDue(ctx, at)
			s.sched.inflight.Wait()
			got, history := scheduleState(s)
			if len(history) != 1 || len(got) != 1 || !got[0].SkipNextAt.IsZero() || !got[0].Enabled ||
				!history[0].OwnerSkipped || !history[0].Due.Equal(at) || history[0].Status != runSkipped ||
				counted(history[0]) || f.sent() != 0 || len(f.pushed()) != 0 {
				t.Fatalf("unexpected skip outcome: tasks=%+v history=%+v sends=%d", got, history, f.sent())
			}
			restarted := newServiceOn(t, f, st)
			restarted.runDue(ctx, at)
			restarted.sched.inflight.Wait()
			restored, runs := scheduleState(restarted)
			if len(restored) != 1 || len(runs) != 1 || runs[0].ID != history[0].ID || f.sent() != 0 {
				t.Fatalf("restart duplicated the run or receipt: tasks=%+v runs=%+v", restored, runs)
			}
			next := restored[0].next(at, time.UTC)
			want := at.Add(30 * time.Minute)
			if repeat == repeatDaily {
				want = at.Add(24 * time.Hour)
			}
			if !next.Equal(want) {
				t.Fatalf("the later cadence changed: next=%s want=%s", next, want)
			}
			restarted.runDue(ctx, next)
			restarted.sched.inflight.Wait()
			if _, runs = scheduleState(restarted); len(runs) != 2 || runs[1].OwnerSkipped || !runs[1].Due.Equal(next) {
				t.Fatalf("the later occurrence was not picked normally: %+v", runs)
			}
		})
	}
}

func TestScheduleSkipNextOlderMarkDoesNotSuppressOverdueRun(t *testing.T) {
	for _, repeat := range []string{repeatDaily, repeatInterval, repeatCron} {
		t.Run(repeat, func(t *testing.T) {
			now := time.Now().UTC()
			minute := (now.Minute() + 15) % 30
			task := Task{ID: "plan", Title: "Follow progress", Prompt: "Check the work.", Repeat: repeat, Enabled: true,
				At: now.Add(time.Hour), Time: now.Add(2 * time.Hour).Format("15:04"), EveryMinutes: 30,
				Cron: fmt.Sprintf("%d,%d * * * *", minute, minute+30)}
			s, _, _ := skipFixture(t, task)
			at := task.next(now, time.UTC)
			markSkip(t, s, task.ID, at)
			n := 30 * time.Minute
			if repeat == repeatDaily {
				n = 24 * time.Hour
			}
			s.runDue(t.Context(), at.Add(n+time.Minute))
			s.sched.inflight.Wait()
			_, runs := scheduleState(s)
			if len(runs) != 2 || !runs[0].OwnerSkipped || !runs[0].Due.Equal(at) ||
				runs[1].OwnerSkipped || !runs[1].Due.Equal(at.Add(n)) {
				t.Fatalf("the older mark suppressed a newer occurrence: %+v", runs)
			}
		})
	}
}

func TestScheduleSkipNextRunNowAndAtomicWriteFailure(t *testing.T) {
	at := time.Now().UTC().Add(time.Hour)
	task := Task{ID: "plan", Title: "Follow progress", Prompt: "Check the work.", Repeat: repeatInterval, Enabled: true, At: at, EveryMinutes: 30}
	s, f, st := skipFixture(t, task)
	ctx := t.Context()
	saved := markSkip(t, s, task.ID, at)
	if w := scheduleRequest(s, "POST", "tasks/plan/run", "{}"); w.Code != 200 {
		t.Fatalf("manual run while pending: %d %s", w.Code, w.Body)
	}
	tasks, runs := scheduleState(s)
	if len(runs) != 1 || !runs[0].Manual || len(tasks) != 1 || tasks[0].SkipNextToken != saved.SkipNextToken || !tasks[0].At.Equal(at) {
		t.Fatalf("Run now consumed the marked occurrence: tasks=%+v runs=%+v", tasks, runs)
	}
	st.failBatch.Store(true)
	s.runDue(ctx, at.Add(time.Minute))
	s.sched.inflight.Wait()
	if tasks, runs = scheduleState(s); len(runs) != 1 || tasks[0].SkipNextToken != saved.SkipNextToken ||
		!s.nextDue(ctx, at.Add(time.Minute)).Equal(at) {
		t.Fatalf("failure did not retain the pending operation for retry: tasks=%+v runs=%+v", tasks, runs)
	}
	st.failBatch.Store(false)
	restarted := newServiceOn(t, f, st)
	restarted.runDue(ctx, at.Add(time.Minute))
	restarted.sched.inflight.Wait()
	if tasks, runs = scheduleState(restarted); len(runs) != 2 || !runs[1].OwnerSkipped || !tasks[0].SkipNextAt.IsZero() ||
		!tasks[0].At.Equal(at.Add(30*time.Minute)) || f.sent() != 1 {
		t.Fatalf("retry after restart duplicated or lost work: tasks=%+v runs=%+v sends=%d", tasks, runs, f.sent())
	}
}

func TestScheduleSkipNextZoneChangesOnlyInvalidateCalendarMarkers(t *testing.T) {
	for _, repeat := range []string{repeatDaily, repeatInterval} {
		t.Run(repeat, func(t *testing.T) {
			now := time.Now().UTC()
			task := Task{ID: "plan", Title: "Follow progress", Prompt: "Check the work.", Repeat: repeat, Enabled: true,
				At: now.Add(time.Hour), Time: now.Add(2 * time.Hour).Format("15:04"), EveryMinutes: 30}
			s, _, _ := skipFixture(t, task)
			at := task.next(now, time.UTC)
			markSkip(t, s, task.ID, at)
			setSettings(t, s, func(settings *Settings) { settings.Timezone = "Asia/Shanghai" })
			s.runDue(t.Context(), now)
			got, runs := scheduleState(s)
			if got[0].SkipNextAt.IsZero() != (repeat == repeatDaily) || len(runs) != 0 {
				t.Fatalf("time zone change kept/canceled the wrong marker: %+v %+v", got, runs)
			}
		})
	}
}

func TestScheduleSkipNextConcurrentMarksArmOnceAndConsumeBeforeOwnerHold(t *testing.T) {
	at := time.Now().UTC().Add(time.Hour)
	task := Task{ID: "plan", Title: "Follow progress", Prompt: "Check the work.", Repeat: repeatInterval,
		Enabled: true, At: at, EveryMinutes: 30, SessionID: "owner-chat"}
	s, f, _ := skipFixture(t, task)
	ctx := t.Context()
	type result struct {
		changed bool
		err     error
	}
	begin := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-begin
			changed, err := s.changeNextSkip(ctx, task.ID, at, "", false)
			results <- result{changed, err}
		}()
	}
	close(begin)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.changed == second.changed {
		t.Fatalf("concurrent identical marks must reserve exactly once: %+v %+v", first, second)
	}
	s.ownersPresent(at) // initialize the relay book before simulating owner activity
	s.relays.mu.Lock()
	s.relays.ownerSpokeLocked(task.SessionID, at.Add(-time.Second))
	s.relays.mu.Unlock()
	if h := runHold(s.ownersPresent(at), task, at, at); h <= 0 {
		t.Fatal("fixture does not exercise the owner-presence hold")
	}
	s.runDue(ctx, at)
	s.sched.inflight.Wait()
	tasks, runs := scheduleState(s)
	if len(tasks) != 1 || !tasks[0].SkipNextAt.IsZero() || len(runs) != 1 || !runs[0].OwnerSkipped || f.sent() != 0 {
		t.Fatalf("owner hold consumed the wrong operation: tasks=%+v runs=%+v", tasks, runs)
	}
}
