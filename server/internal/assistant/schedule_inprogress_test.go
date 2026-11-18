package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// A run in progress, and one a hub restart cut short, never shows its own start time with an earlier run's outcome
// (#10182). A daily 08:45 task whose run the day before failed (the disk was full) read 「上次 11-16 08:45 失败：write …」
// while its 11-16 08:45 run was still starting: the scheduler moves last_run when a run starts and last_status /
// last_error only when it is recorded. The run's own live snapshot said so, and the run told the owner it was "recorded
// as failed". Now every view shows the outcome with the time of the run it is of (Task.lastShown).

const inProgressPrevErr = "write /home/me/.persona/launcher-settings.json: no space left on device"

// inProgressTask is the daily task as stored before 11-16 08:45, by a hub from before StatusRun: its 11-15 run failed.
func inProgressTask(prev time.Time) Task {
	return Task{ID: "brief", Title: "早报", Prompt: "整理今天的日程和待办", Repeat: repeatDaily, Time: "08:45", Enabled: true,
		LastRun: prev.UTC(), LastStatus: runFailed, LastError: inProgressPrevErr}
}

// lastRunShown is what one view of the task says of its last run.
type lastRunShown struct {
	view           string
	at             time.Time
	status, reason string
}

// checkLastRunShown fails when a view pairs this run's start (at or after begin) with the earlier run's failure, or
// shows the earlier run's time with anything but that run's own outcome.
func checkLastRunShown(t *testing.T, s lastRunShown, prev, begin time.Time) {
	t.Helper()
	switch {
	case s.at.IsZero():
		t.Errorf("%s: no last run shown, the 11-15 run failed: %+v", s.view, s)
	case !s.at.Before(begin):
		if s.status == runFailed || strings.Contains(s.reason, "no space") {
			t.Errorf("%s: this run's start %s is shown with the 11-15 run's failure (status %q, %q)", s.view,
				s.at.Format(time.RFC3339), s.status, s.reason)
		}
	case s.at.Equal(prev):
		if s.status != runFailed || !strings.Contains(s.reason, "no space") {
			t.Errorf("%s: the 11-15 run's time with another outcome: %+v", s.view, s)
		}
	default:
		t.Errorf("%s: a last run that is neither this one nor the 11-15 one: %+v", s.view, s)
	}
}

// checkRecorded fails unless a view shows this run (started at or after begin) with its own outcome.
func checkRecorded(t *testing.T, s lastRunShown, begin time.Time) {
	t.Helper()
	if s.at.Before(begin) || s.status == "" || strings.Contains(s.reason, "no space") {
		t.Errorf("%s: once recorded, this run is shown with its own outcome: %+v", s.view, s)
	}
}

// snapshotShown is the live snapshot's line for the task, as the assistant's conversations read it.
func snapshotShown(t *testing.T, h *Service, loc *time.Location, now time.Time) (lastRunShown, string) {
	t.Helper()
	_, _, routines, total, _, _ := h.snapshotSchedule(context.Background(), loc, now)
	text := prompts.RenderSnapshot(prompts.Snapshot{Now: now.In(loc), Zone: loc.String(), Routines: routines, RoutinesTotal: total})
	for _, r := range routines {
		if r.Label == "早报" {
			return lastRunShown{view: "live snapshot", at: r.LastAt, status: r.LastStatus, reason: r.LastWhy}, text
		}
	}
	t.Fatalf("the snapshot has no 早报: %+v", routines)
	return lastRunShown{}, ""
}

// appShown is the task's last_run / last_status / last_error as the apps get them (GET /api/v1/assistant/tasks), and
// all its fields.
func appShown(t *testing.T, h *Service, view string) (lastRunShown, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/assistant/tasks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: GET tasks: %d %s", view, rec.Code, rec.Body)
	}
	var list struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("%s: %v (%s)", view, err, rec.Body)
	}
	for _, raw := range list.Tasks {
		var v struct {
			ID         string    `json:"id"`
			LastRun    time.Time `json:"last_run"`
			LastStatus string    `json:"last_status"`
			LastError  string    `json:"last_error"`
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &v); err != nil || json.Unmarshal(raw, &fields) != nil {
			t.Fatalf("%s: %v (%s)", view, err, raw)
		}
		if v.ID == "brief" {
			return lastRunShown{view: view, at: v.LastRun, status: v.LastStatus, reason: v.LastError}, fields
		}
	}
	t.Fatalf("%s: no task brief in %s", view, rec.Body)
	return lastRunShown{}, nil
}

// The views while the run is between being started and being recorded: inside assistant.send, where the node asks for
// the live snapshot the run's first message starts with (node/assistant.go) while it starts the process.
func TestARunInProgressDoesNotShowTheLastRunsFailure(t *testing.T) {
	loc := mustZone(t, "Asia/Shanghai")
	prev := time.Date(2026, 11, 15, 8, 45, 4, 0, loc)
	for _, tc := range []struct {
		name string
		run  func(h *Service, start time.Time)
	}{
		{"scheduled", func(h *Service, start time.Time) {
			h.runDue(context.Background(), start)
			h.sched.inflight.Wait()
		}},
		{"run now", func(h *Service, _ time.Time) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/assistant/tasks/brief/run", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("Run now: %d %s", rec.Code, rec.Body)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := readyHub()
			f.settings = &Settings{Timezone: "Asia/Shanghai"}
			h := newStorelessService(t, f)
			setTasks(h, inProgressTask(prev))
			start := time.Date(2026, 11, 16, 8, 45, 5, 0, loc)
			begin := start
			if tc.name == "run now" {
				begin = time.Now().Truncate(time.Second)
			}
			var (
				once       sync.Once
				seen       bool
				snap, app  lastRunShown
				text, desc string
			)
			f.onAssistantSend = func(f *fakeHub, p map[string]any) {
				if s, _ := p["text"].(string); !strings.Contains(s, "整理今天的日程和待办") {
					return
				}
				once.Do(func() {
					// The views ask the hub (its hello), as the apps' requests do: not under the fake's lock (as authHold).
					f.mu.Unlock()
					defer f.mu.Lock()
					seen = true
					snap, text = snapshotShown(t, h, loc, begin)
					app, _ = appShown(t, h, "GET /api/v1/assistant/tasks")
					desc = h.describeSchedule(context.Background(), "")
				})
			}
			tc.run(h, start)
			if !seen {
				t.Fatal("the run never reached assistant.send")
			}
			checkLastRunShown(t, snap, prev, begin)
			if !snap.at.Before(begin) && snap.status == runFailed {
				t.Errorf("the run's own snapshot says it failed: %s", text)
			}
			checkLastRunShown(t, app, prev, begin)
			if when := begin.In(loc).Format("01-02 15:04"); strings.Contains(desc, "last run "+when+" failed") {
				t.Errorf("list_followups pairs this run with the earlier failure: %s", desc)
			}

			// Recorded (sent): the views show this run, with its own outcome.
			after, _ := snapshotShown(t, h, loc, begin)
			checkRecorded(t, after, begin)
			recorded, _ := appShown(t, h, "GET /api/v1/assistant/tasks once recorded")
			checkRecorded(t, recorded, begin)
		})
	}
}

// A hub restart between a run's start and its record loses the run (it was saved as started so as not to run twice).
// The restarted hub must not show that lost run's start with the earlier run's failure, nor show it running: nothing
// runs it any more, and the task's next run is a day away.
func TestARunCutShortByARestartDoesNotShowTheLastRunsFailure(t *testing.T) {
	loc := mustZone(t, "Asia/Shanghai")
	prev := time.Date(2026, 11, 15, 8, 45, 4, 0, loc)
	start := time.Date(2026, 11, 16, 8, 45, 5, 0, loc)
	store := &flakyScheduleStore{data: map[string][]byte{}}
	f1 := readyHub()
	f1.settings = &Settings{Timezone: "Asia/Shanghai"}
	h1 := newServiceOn(t, f1, store)
	sc := h1.loadSchedule(context.Background())
	sc.mu.Lock()
	if err := h1.saveLocked(sc, []Task{inProgressTask(prev)}); err != nil {
		sc.mu.Unlock()
		t.Fatal(err)
	}
	sc.mu.Unlock()

	// The first hub holds the run inside assistant.send; the second one comes up on the same database meanwhile.
	sending, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f1.onAssistantSend = func(_ *fakeHub, p map[string]any) {
		if s, _ := p["text"].(string); strings.Contains(s, "整理今天的日程和待办") {
			once.Do(func() { close(sending) })
			<-release
		}
	}
	go h1.runDue(context.Background(), start)
	t.Cleanup(func() {
		close(release)
		h1.sched.inflight.Wait()
	})
	select {
	case <-sending:
	case <-time.After(10 * time.Second):
		t.Fatal("the run never reached assistant.send")
	}

	f2 := newFakeHub()
	f2.settings = &Settings{Timezone: "Asia/Shanghai"}
	h2 := newServiceOn(t, f2, store)
	later := start.Add(time.Hour)
	snap, text := snapshotShown(t, h2, loc, later)
	checkLastRunShown(t, snap, prev, start)
	if snap.status != runOK && snap.status != runFailed && snap.status != runSkipped {
		t.Errorf("after the restart the snapshot shows a status that is no outcome (the run is gone): %+v", snap)
	}
	if strings.Contains(text, "上次11-16 08:45 失败") {
		t.Errorf("after the restart the snapshot pairs the lost run with the earlier failure: %s", text)
	}

	app, fields := appShown(t, h2, "GET /api/v1/assistant/tasks after the restart")
	checkLastRunShown(t, app, prev, start)
	if app.status != runOK && app.status != runFailed && app.status != runSkipped {
		t.Errorf("after the restart last_status is no outcome (the run is gone): %+v", app)
	}
	for k, v := range fields {
		if strings.Contains(k, "running") && v != nil && v != false && v != "" {
			t.Errorf("after the restart the task still says it is running (%s: %v), nothing runs it any more", k, v)
		}
	}
	if _, sent := fields["status_run"]; sent {
		t.Errorf("status_run is the hub's own, never sent: %v", fields)
	}

	if d := h2.describeSchedule(context.Background(), ""); strings.Contains(d, "last run 11-16 08:45 failed") {
		t.Errorf("list_followups pairs the lost run with the earlier failure: %s", d)
	}
}

// An outcome keeps the time of the run it is of, also when LastRun has moved on to a run that recorded none of its own
// (review of #10182): a daily run fails, a Run now never starts its conversation, then the daily run's automatic retry
// succeeds. Its outcome is shown with the retry's time, never with the Run now's, before and after the next day's
// start; and an outcome recorded at the LastRun == of boundary stays with that run when the next one starts.
func TestAnOutcomeKeepsItsOwnRunsTime(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	at := func(day, hh, mm int) time.Time { return time.Date(2026, 11, day, hh, mm, 0, 0, loc).UTC() }
	setTasks(h, Task{ID: "r", Title: "日报", Prompt: "x", Repeat: repeatDaily, Time: "08:45", Enabled: true})
	start := func(now time.Time) {
		sc := h.loadSchedule(context.Background())
		sc.mu.Lock()
		sc.tasks[sc.indexLocked("r")].started(now)
		sc.mu.Unlock()
	}
	shown := func(when string, wantAt time.Time, wantStatus string) {
		t.Helper()
		r, _ := taskByID(h, "r")
		if got := r.lastShown(); !got.Equal(wantAt) || r.LastStatus != wantStatus {
			t.Errorf("%s: shown %s %q, want %s %q", when, got.In(loc).Format("01-02 15:04"), r.LastStatus,
				wantAt.In(loc).Format("01-02 15:04"), wantStatus)
		}
		views := h.tasksView(context.Background(), nil)
		if len(views) != 1 || !views[0].LastRun.Equal(wantAt) || views[0].LastStatus != wantStatus {
			t.Errorf("%s: the tasks API shows %+v, want last_run %s with %q", when, views, wantAt, wantStatus)
		}
	}

	// The LastRun == of boundary: the run of the current LastRun records its outcome, and the next start keeps it.
	start(at(14, 8, 45))
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "r", Status: runOK, SessionID: "s1", of: at(14, 8, 45)})
	start(at(15, 8, 45))
	shown("the next run starting", at(14, 8, 45), runOK)

	h.recordRun(ScheduleRun{Kind: "routine", RefID: "r", Status: runFailed, Error: "launcher failed", of: at(15, 8, 45)})
	shown("the daily run failed", at(15, 8, 45), runFailed)
	// A Run now that never started its conversation: no session, so it does not cancel the daily run's retry.
	start(at(15, 10, 0))
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "r", Status: runFailed, Error: "no machine", Manual: true, of: at(15, 10, 0)})
	shown("the Run now failed to start", at(15, 10, 0), runFailed)
	// The daily run's automatic retry succeeds: its outcome goes with its own time, not the Run now's.
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "r", Status: runOK, SessionID: "s3", Retried: true, of: at(15, 10, 5)})
	shown("the retry succeeded", at(15, 10, 5), runOK)
	start(at(16, 8, 45))
	shown("the next day's run starting", at(15, 10, 5), runOK)
}
