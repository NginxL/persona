package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// run is what runDue does for one due task: read it as it is now, then run it.
func runNow(t *testing.T, h *Service, id string) (proto.Session, error) {
	t.Helper()
	task, ok := taskByID(h, id)
	if !ok {
		t.Fatalf("no task %s", id)
	}
	now := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	return h.runTask(task, now, now, false, false)
}

// The user's ask (2026-11-01): "run the same scheduled task in the same session". The first run starts the task's own
// conversation; later runs go into it, told the earlier runs are above instead of being given the background again.
func TestATaskRunsInItsOwnConversation(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	setTasks(h, Task{ID: "r", Title: "早报", Prompt: "给我今天的早报", Repeat: repeatDaily, Time: "09:00", Enabled: true})

	first, err := runNow(t, h, "r")
	if err != nil {
		t.Fatal(err)
	}
	if p := n.lastSend(t); p["session_id"] != nil && p["session_id"] != "" || strings.Contains(p["text"].(string), "还是这个任务自己的对话") {
		t.Fatalf("the first run starts a conversation with the whole message: %v", p)
	}
	task, _ := taskByID(h, "r")
	if task.RunSessionID != first.SessionID || task.RunMachineID != "server" || task.RunLauncher != "claude" {
		t.Fatalf("the new conversation is the task's own: %+v", task)
	}

	second, err := runNow(t, h, "r")
	if err != nil {
		t.Fatal(err)
	}
	p := n.lastSend(t)
	if second.MachineID != "server" || second.SessionID != first.SessionID || p["session_id"] != first.SessionID || p["resume_only"] != true {
		t.Fatalf("the second run goes into the same conversation: %+v %v", second, p)
	}
	if text := p["text"].(string); !strings.Contains(text, "还是这个任务自己的对话") || !strings.Contains(text, "给我今天的早报") {
		t.Fatalf("a continued run says so and still has the task's prompt: %q", text)
	}
	runs := waitRuns(t, h, 2)
	if runs[0].SessionID != first.SessionID || runs[1].SessionID != first.SessionID || runs[1].Status != runOK {
		t.Fatalf("both runs are recorded in that conversation: %+v", runs)
	}
	// The hub has its node keep listing the task's conversation however quiet (the profile's keep).
	deadline := time.Now().Add(5 * time.Second)
	for p := n.lastSnapshot().Profile; p == nil || !slices.Contains(p.Keep, SessionRef{MachineID: "server", SessionID: first.SessionID}); p = n.lastSnapshot().Profile {
		if time.Now().After(deadline) {
			t.Fatalf("the hub is told to keep the task's conversation listed: %+v", p)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if v := n.lastSnapshot().Tasks; len(v) != 1 || v[0].RunSessionID != first.SessionID {
		t.Fatalf("and the hub is told the task: %+v", v)
	}
}

func TestAFreshRecurringTaskStartsNewChatsWithARetainedOrigin(t *testing.T) {
	node := newFakeHub().withMachine()
	h := newStorelessService(t, node)
	setSettings(t, h, func(s *Settings) { s.Main, s.MachineID = "main", "server" })
	const origin = "old-followup-chat"
	node.known[origin] = true
	setTasks(h, Task{ID: "followup", Title: "Patrol", Prompt: "Check the changes.",
		Repeat: repeatOnce, At: time.Now().Add(time.Hour), Enabled: true, MachineID: "server", SessionID: origin})

	// The task editor converts an existing follow-up to a recurring task with fresh chats.
	update := func(fresh bool) {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"title": "Patrol", "prompt": "Check the changes.", "repeat": repeatInterval, "every_minutes": 30,
			"enabled": true, "session_id": origin, "fresh_each_run": fresh,
		})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant/tasks/followup", strings.NewReader(string(body))))
		if rec.Code != http.StatusOK {
			t.Fatalf("update task: %d %s", rec.Code, rec.Body)
		}
		saved, _ := taskByID(h, "followup")
		if saved.Repeat != repeatInterval || saved.FreshEachRun != fresh || saved.SessionID != origin {
			t.Fatalf("converted follow-up lost its origin or run preference: %+v", saved)
		}
	}
	update(true)

	first, err := runNow(t, h, "followup")
	if err != nil || first.SessionID == "" || first.SessionID == origin {
		t.Fatalf("first fresh run reused the origin: %+v, %v", first, err)
	}
	second, err := runNow(t, h, "followup")
	if err != nil || second.SessionID == "" || second.SessionID == origin || second.SessionID == first.SessionID {
		t.Fatalf("second fresh run did not start a separate conversation: %+v, %v", second, err)
	}
	if saved, _ := taskByID(h, "followup"); saved.SessionID != origin || saved.RunSessionID != second.SessionID {
		t.Fatalf("fresh dispatch changed the stored origin or lost the last run: %+v", saved)
	}

	update(false)
	continued, err := runNow(t, h, "followup")
	if err != nil || continued.SessionID != origin {
		t.Fatalf("turning off fresh runs did not restore the origin: %+v, %v", continued, err)
	}
}

func TestFreshEachRunOnRescheduleUsesFinalRepeat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		at    string
		every string
		want  string
	}{
		{name: "one-off", at: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), want: repeatOnce},
		{name: "promoted to interval", every: "30m", want: repeatInterval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := readyHub()
			node.settings = &Settings{Main: "main", Timezone: "UTC"}
			s := newStorelessService(t, node)
			const origin = "origin-chat"
			node.known[origin] = true
			const prompt = "Check the renewal"
			setTasks(s, Task{ID: "followup", Title: "Renewal", Prompt: prompt, Repeat: repeatOnce,
				At: time.Now().Add(-time.Minute), MachineID: "server", SessionID: origin})
			s.recordRun(ScheduleRun{RefID: "followup", Status: runOK, MachineID: "server", SessionID: origin})

			args, err := json.Marshal(map[string]any{
				"title": "Renewal", "prompt": prompt, "at": tc.at, "every": tc.every, "fresh_each_run": true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.toolScheduleFollowup(t.Context(), "server", origin, args); err != nil {
				t.Fatal(err)
			}
			task, _ := taskByID(s, "followup")
			if task.Repeat != tc.want || task.SessionID != origin || task.FreshEachRun != (tc.want != repeatOnce) {
				t.Fatalf("rescheduled task: %+v", task)
			}
			if tc.want == repeatOnce {
				sess, err := runNow(t, s, "followup")
				if err != nil || sess.SessionID != origin {
					t.Fatalf("one-off did not go back to its originating chat: %+v, %v", sess, err)
				}
			}
		})
	}
}

// A conversation that is gone (Claude Code cleans transcripts up after 30 days), one on another launcher, or one the
// user reset ("New chat next time") can't be continued: the run starts a new one and that becomes the task's.
func TestATaskStartsANewConversationWhenItsOwnCannotGoOn(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newTestService(t, n)
	setTasks(h, Task{ID: "p", Title: "巡检", Prompt: "巡检", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	first, _ := runNow(t, h, "p")

	n.forget(first.SessionID)
	gone, err := runNow(t, h, "p")
	if err != nil {
		t.Fatal(err)
	}
	if gone.SessionID == first.SessionID || gone.SessionID == "" {
		t.Fatalf("a gone conversation is replaced: %+v", gone)
	}
	if task, _ := taskByID(h, "p"); task.RunSessionID != gone.SessionID {
		t.Fatalf("and the new one is kept: %+v", task)
	}
	if text := n.lastSend(t)["text"].(string); !strings.Contains(text, "不用再用 schedule_followup") {
		t.Fatalf("a new conversation gets the whole message again: %q", text)
	}

	// Model configuration › Main chat now runs on another launcher (a task's conversations follow it, #1570).
	n.zshrc = map[string][]proto.Launcher{"server": {{Name: "codex", Agent: "codex"}}}
	setModel(t, h, "assistant", ModelBinding{Provider: "launcher.codex"})
	switched, _ := runNow(t, h, "p")
	if switched.SessionID == gone.SessionID {
		t.Fatal("a conversation started with claude isn't continued with codex")
	}
	if p := n.lastSend(t); p["session_id"] != nil && p["session_id"] != "" || p["launcher"] != "codex" {
		t.Fatalf("a new conversation with the new launcher: %v", p)
	}
	if task, _ := taskByID(h, "p"); task.RunSessionID != switched.SessionID || task.RunLauncher != "codex" {
		t.Fatalf("%+v", task)
	}

	// "New chat next time" in the app.
	srv := httptest.NewServer(scheduleMux(h))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/api/v1/assistant/tasks/p/fresh", "application/json", nil)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("fresh: %v %v", res, err)
	}
	var body struct{ Task Task }
	_ = json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if body.Task.RunSessionID != "" {
		t.Fatalf("the task forgets its conversation: %+v", body.Task)
	}
	fresh, _ := runNow(t, h, "p")
	if fresh.SessionID == switched.SessionID {
		t.Fatal("the next run starts a new conversation")
	}
	if res, _ := http.Post(srv.URL+"/api/v1/assistant/tasks/nope/fresh", "application/json", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown task: %d", res.StatusCode)
	}
}

// A repeating task due while its last run is still going (a patrol stuck on an approval) is skipped and recorded, not
// stacked into the same conversation; a one-off still goes in, and "Run now" says why it didn't.
func TestATaskDueWhileItsLastRunIsGoingIsSkipped(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	setTasks(h, Task{ID: "p", Title: "巡检", Prompt: "巡检", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true},
		Task{ID: "f", Prompt: "看看部署", Repeat: repeatOnce, Enabled: true})
	first, _ := runNow(t, h, "p")
	n.setSessions("server", proto.Session{SessionID: first.SessionID, Status: proto.StatusWorking, State: proto.StateNeedsYou})
	sends := n.sent()

	if _, err := runNow(t, h, "p"); !errors.Is(err, errTaskBusy) {
		t.Fatalf("err = %v", err)
	}
	if n.sent() != sends {
		t.Fatal("nothing is sent into a busy conversation")
	}
	runs := waitRuns(t, h, 2)
	if r := runs[1]; r.Status != runSkipped || r.Error != errTaskBusy.Error() || r.SessionID != first.SessionID {
		t.Fatalf("the skip is in the history: %+v", r)
	}
	if task, _ := taskByID(h, "p"); task.RunSessionID != first.SessionID || task.LastStatus != runSkipped {
		t.Fatalf("the task keeps its conversation: %+v", task)
	}

	srv := httptest.NewServer(scheduleMux(h))
	defer srv.Close()
	res, _ := http.Post(srv.URL+"/api/v1/assistant/tasks/p/run", "application/json", nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("立即运行 on a busy task: %d", res.StatusCode)
	}

	// A one-off that shares the conversation (rescheduled by its own run) still goes in, after the turn.
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	f := &sc.tasks[sc.indexLocked("f")]
	f.RunMachineID, f.RunSessionID, f.RunLauncher = "server", first.SessionID, "claude"
	sc.mu.Unlock()
	if sess, err := runNow(t, h, "f"); err != nil || sess.SessionID != first.SessionID {
		t.Fatalf("a one-off is not skipped: %+v %v", sess, err)
	}

	// Two runs of one task at the same moment: the second one waits for nothing and is skipped.
	if !h.sched.begin("p") {
		t.Fatal("begin")
	}
	n.setSessions("server")
	if _, err := runNow(t, h, "p"); !errors.Is(err, errTaskBusy) {
		t.Fatalf("a run while the same task is being sent: %v", err)
	}
	h.sched.end("p")
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
}

// A run skipped because its last run waits on the user (an approval, a question) is pushed once per wait: only they
// can let it go on, and the morning brief not coming said nothing (#4499). The same wait skipped again: no second push. The
// old card answered and the turn stopped at a new one (no run went in between): a new wait, pushed again (review of
// #4506). A last run only still working, a muted conversation and Run now are not pushed.
func TestASkipForALastRunWaitingOnTheUserIsPushedOnce(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	setTasks(h, Task{ID: "p", Title: "早报", Prompt: "早报", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	first, _ := runNow(t, h, "p")
	sid := first.SessionID
	waiting := func(since time.Time) {
		n.setSessions("server", proto.Session{SessionID: sid, Status: proto.StatusBlocked, State: proto.StateNeedsYou,
			PendingInteractions: 1, StateSince: since})
	}
	skip := func() {
		t.Helper()
		if _, err := runNow(t, h, "p"); !errors.Is(err, errTaskBusy) {
			t.Fatalf("err = %v", err)
		}
	}
	// waits: the waiting-skip pushes only. Three skips in a row also make a failure streak, told by its own notice
	// (schedule_streak.go), which this test does not count.
	waits := func() []Notification {
		var w []Notification
		for _, p := range n.pushed() {
			if strings.Contains(p.Title, "等你") {
				w = append(w, p)
			}
		}
		return w
	}

	// Still working: skipped, nothing pushed.
	n.setSessions("server", proto.Session{SessionID: sid, Status: proto.StatusWorking, State: proto.StateWorking})
	skip()
	if p := waits(); len(p) != 0 {
		t.Fatalf("a last run still working is not pushed: %+v", p)
	}

	// Waiting on card A: pushed once across two skips.
	cardA := time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)
	waiting(cardA)
	skip()
	skip()
	p := waits()
	if len(p) != 1 || !strings.Contains(p[0].Title, "早报") || !strings.Contains(p[0].Title, "等你") || p[0].SessionID != sid {
		t.Fatalf("pushed once, naming the task and opening its conversation: %+v", p)
	}

	// A answered, the old turn stopped at card B: another wait, pushed again though no run went in.
	waiting(cardA.Add(time.Hour))
	skip()
	if p := waits(); len(p) != 2 {
		t.Fatalf("a new wait is pushed again: %+v", p)
	}

	// A skip that finds it no longer waiting clears the mark: the next wait on the same card id is news again.
	n.setSessions("server", proto.Session{SessionID: sid, Status: proto.StatusWorking, State: proto.StateWorking})
	skip()
	waiting(cardA.Add(time.Hour))
	skip()
	if p := waits(); len(p) != 3 {
		t.Fatalf("after the wait ended, waiting again is news: %+v", p)
	}

	// Run now on it: answered busy_wait in the app, no push.
	waiting(cardA.Add(2 * time.Hour))
	task, _ := taskByID(h, "p")
	if _, err := h.runTask(task, time.Now(), time.Now(), true, false); !errors.Is(err, errTaskBusy) {
		t.Fatalf("立即运行: %v", err)
	}
	if p := waits(); len(p) != 3 {
		t.Fatalf("立即运行 is not pushed: %+v", p)
	}

	// Muted: skipped and recorded, no push.
	h.fleet.mutesMu.Lock()
	h.fleet.mutes = &Pins{Spaces: []string{}, Sessions: []string{"server/" + sid}}
	h.fleet.mutesMu.Unlock()
	skip()
	if p := waits(); len(p) != 3 {
		t.Fatalf("a muted conversation is not pushed: %+v", p)
	}

	// Deleting the task forgets its mark.
	if ok, err := h.deleteTask(context.Background(), "p"); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	h.sched.mu.Lock()
	_, kept := h.sched.waitTold["p"]
	h.sched.mu.Unlock()
	if kept {
		t.Fatal("a deleted task's mark is dropped")
	}
}

// A task from a conversation (SessionID) whose conversation fails keeps the new one as its own: the next failure
// continues there instead of starting yet another one.
func TestATaskFromAConversationFallsBackToItsOwn(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	setTasks(h, Task{ID: "f", Prompt: "接着看", Repeat: repeatInterval, EveryMinutes: 60, Enabled: true, MachineID: "gone", SessionID: "origin"})
	first, _ := runNow(t, h, "f")
	if first.SessionID == "" || first.SessionID == "origin" {
		t.Fatalf("%+v", first)
	}
	again, _ := runNow(t, h, "f")
	if again.SessionID != first.SessionID || n.lastSend(t)["resume_only"] != true {
		t.Fatalf("the fallback conversation is continued: %+v", again)
	}
	if task, _ := taskByID(h, "f"); task.SessionID != "origin" {
		t.Fatalf("the conversation it came from is still tried first: %+v", task)
	}
}

// scheduleMux is the routes as the hub relays them.
func scheduleMux(h *Service) http.Handler { return h }

// A run the node accepts and whose turn then ends in an API error (503 / no such model) is failed, pushed and retried
// once — the node reports the send as accepted, so it used to stay "ok" and the user was never told.
func TestTaskTurnEndingInAPIErrorFailsAndRetriesOnce(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = 50 * time.Millisecond
	h.turnPoll = 200 * time.Millisecond
	n.items = []proto.Item{{Role: "assistant", Kind: "text", Text: "API Error: 503 upstream attempt budget is exhausted"}}
	setTasks(h, Task{ID: "p", Title: "值守", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant"})

	// The turn runs, then ends: the watcher sees working and then idle.
	n.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
		Cwd: "/home/me/.persona/assistant"})
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
	// The turn ends after the first poll saw it working (started after the run went, so the watcher's polls line up).
	go func() {
		time.Sleep(3 * h.turnPoll / 2)
		n.mu.Lock()
		for i := range n.sessions["server"] {
			n.sessions["server"][i].Status, n.sessions["server"][i].State = proto.StatusOffline, proto.StateIdle
		}
		n.mu.Unlock()
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sc := h.loadSchedule(context.Background())
		sc.mu.Lock()
		failed := len(sc.runs) > 0 && slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool {
			return r.RefID == "p" && r.Status == runFailed && strings.Contains(r.Error, "503")
		})
		sc.mu.Unlock()
		if failed {
			if p := n.pushedBy(deadline); len(p) == 0 || p[0].Title != "定时任务失败：值守" || p[0].SessionID != "s1" || p[0].DeviceID != "server" {
				t.Fatalf("the failure is pushed: %+v", p)
			}
			// and the retry went, marked as such
			for time.Now().Before(deadline) {
				if n.sent() >= 2 {
					sc.mu.Lock()
					retried := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RefID == "p" && r.Retried })
					sc.mu.Unlock()
					if retried {
						return
					}
				}
				time.Sleep(50 * time.Millisecond)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the failed run was not recorded")
}

// A run whose turn ends with no output at all (the model service answered 200 with nothing: no reply, no tool call,
// no error) is failed, pushed and retried once, not left "ok" behind a push that says "done" (#3752).
func TestTaskSilentTurnFailsAndRetriesOnce(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = 50 * time.Millisecond
	h.turnPoll = 200 * time.Millisecond
	n.items = []proto.Item{{ID: "u1", Role: "user", Kind: proto.KindText, Text: "⏰ 早报（定时任务）\n\n给我今天的早报"},
		{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: ""}}
	setTasks(h, Task{ID: "p", Title: "早报", Prompt: "给我今天的早报", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant"})

	n.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
		Cwd: "/home/me/.persona/assistant"})
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
	// The turn ends after the first poll saw it working (started after the run went, so the watcher's polls line up).
	go func() {
		time.Sleep(3 * h.turnPoll / 2)
		n.mu.Lock()
		for i := range n.sessions["server"] {
			n.sessions["server"][i].Status, n.sessions["server"][i].State = proto.StatusOffline, proto.StateIdle
		}
		n.mu.Unlock()
	}()
	want := silentTurnErrorIn(h.lang(context.Background()))
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sc := h.loadSchedule(context.Background())
		sc.mu.Lock()
		failed := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RefID == "p" && r.Status == runFailed && r.Error == want })
		sc.mu.Unlock()
		if failed {
			if p := n.pushedBy(deadline); len(p) == 0 || p[0].Title != "定时任务失败：早报" || p[0].SessionID != "s1" {
				t.Fatalf("the failure is pushed: %+v", p)
			}
			h.fleet.mu.Lock()
			_, held := h.fleet.notified["s1"]
			h.fleet.mu.Unlock()
			if !held {
				t.Fatal("the turn's own 「早报 · 完成了」 push is held back (the failure was pushed instead)")
			}
			for time.Now().Before(deadline) {
				if n.sent() >= 2 {
					sc.mu.Lock()
					retried := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RefID == "p" && r.Retried })
					sc.mu.Unlock()
					if retried {
						return
					}
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatal("the silent run was not retried")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the silent run was not recorded as failed")
}

// The session's state lags: it is never seen working, and the last item changed only because the prompt landed. The
// turn read holds the prompt alone: that is no turn known to have ended, so the run is not failed, not pushed as a
// failure and not run again (review of #3777: a wrong verdict ran the whole morning briefing twice).
func TestTaskPromptOnlyTurnIsNotJudgedSilent(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = 50 * time.Millisecond
	h.turnPoll = 200 * time.Millisecond
	setTasks(h, Task{ID: "p", Title: "早报", Prompt: "给我今天的早报", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant"})
	n.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusIdle, State: proto.StateIdle, Assistant: true,
		Cwd: "/home/me/.persona/assistant"})
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(h.turnPoll / 2) // before the first poll
		n.setItems(proto.Item{ID: "u1", Role: "user", Kind: proto.KindText, Text: "⏰ 早报（定时任务）\n\n给我今天的早报"})
	}()
	time.Sleep(6 * h.turnPoll) // the watcher has seen the changed item and read the turn
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	failed := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RefID == "p" && r.Status == runFailed })
	sc.mu.Unlock()
	if failed {
		t.Fatal("a turn that holds only its prompt was failed as silent")
	}
	if p := n.pushed(); len(p) != 0 {
		t.Fatalf("no failure is pushed: %+v", p)
	}
	if got := n.sent(); got != 1 {
		t.Fatalf("the task is not run again: %d sends", got)
	}
}

// The turn is seen working, one poll reads it idle (a status flap, a missed spinner), then it works again: with only
// [prompt, thinking] read so far it is still going, so the run is not failed, not pushed as a failure and not run
// again (third review of #3777).
func TestTaskFlapMidTurnIsNotJudgedSilent(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = 50 * time.Millisecond
	h.turnPoll = 200 * time.Millisecond
	// No items yet when the run is sent: the watcher's "before" is the empty conversation, so the prompt landing
	// changes the last item, which is what made the old order end the wait on the first idle poll.
	setTasks(h, Task{ID: "p", Title: "早报", Prompt: "给我今天的早报", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant"})
	working := proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true, Cwd: "/home/me/.persona/assistant"}
	idle := working
	idle.Status, idle.State = proto.StatusIdle, proto.StateIdle
	n.setSessions("server", working)
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(3 * h.turnPoll / 2) // after the first poll saw it working: the prompt and thinking land, and it flaps idle
		n.setItems(proto.Item{ID: "u1", Role: "user", Kind: proto.KindText, Text: "⏰ 早报（定时任务）\n\n给我今天的早报"},
			proto.Item{ID: "t1", Role: "assistant", Kind: proto.KindThinking, Text: "先看日历"})
		n.setSessions("server", idle)
		time.Sleep(h.turnPoll) // one poll reads it idle
		n.setSessions("server", working)
	}()
	time.Sleep(10 * h.turnPoll)
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	failed := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RefID == "p" && r.Status == runFailed })
	sc.mu.Unlock()
	if failed {
		t.Fatal("a turn that flapped idle once mid-turn was failed as silent")
	}
	if p := n.pushed(); len(p) != 0 {
		t.Fatalf("no failure is pushed: %+v", p)
	}
	if got := n.sent(); got != 1 {
		t.Fatalf("the task is not run again: %d sends", got)
	}
}

// silentRun only fails a turn known to be the run's and to have ended.
func TestSilentRun(t *testing.T) {
	task := Task{Prompt: "给我今天的早报"}
	prompt := proto.Item{ID: "u1", Role: "user", Kind: proto.KindText, Text: "⏰ 早报（定时任务）\n\n给我今天的早报"}
	empty := proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: ""}
	end := proto.Item{ID: "e1", Role: "system", Kind: "turn_duration", TurnDuration: &proto.TurnDuration{DurationMS: 900}}
	other := proto.Item{ID: "u2", Role: "user", Kind: proto.KindText, Text: "顺便看下明天的会"}
	reply := proto.Item{ID: "a2", Role: "assistant", Kind: proto.KindText, Text: "今天三个会"}
	for _, c := range []struct {
		name  string
		items []proto.Item
		ran   bool
		want  bool
	}{
		{"seen working, then no output", []proto.Item{prompt}, true, true},
		{"an empty assistant item, not seen working", []proto.Item{prompt, empty}, false, false},
		{"thinking, the model still going", []proto.Item{prompt, {ID: "t1", Role: "assistant", Kind: proto.KindThinking, Text: "想一想"}}, false, false},
		{"an empty assistant item, seen working", []proto.Item{prompt, empty}, true, true},
		{"the user mentions the task", []proto.Item{{ID: "u3", Role: "user", Kind: proto.KindText, Text: "给我今天的早报呢？"}}, true, false},
		{"the end marker", []proto.Item{prompt, end}, false, true},
		{"the prompt alone, state lagging", []proto.Item{prompt}, false, false},
		{"the user's own next turn", []proto.Item{other, empty}, true, false},
		{"it answered", []proto.Item{prompt, reply}, true, false},
	} {
		if got := silentRun(c.items, task, c.ran); got != c.want {
			t.Errorf("%s: silentRun = %v, want %v", c.name, got, c.want)
		}
	}
}

// In a chat the task reuses, the node's one-turn page reaches back past this run's prompt (an injected one, not counted
// as a turn) into the previous run, with its reply: the silent run is still found. Paged by the node's own reader
// (transcript.PageTurns), not the fake hub, which counts every prompt as a turn (#8901).
func TestSilentRunInAReusedChat(t *testing.T) {
	task := Task{Title: "早报", Prompt: "给我今天的早报"}
	at := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	run := taskPrompt(task, at, at, time.UTC, false, false, true)
	if prompts.ParseInjected(run) == nil {
		t.Fatal("the run's prompt is not an injected one: the page would stop at it")
	}
	n := 0
	line := func(b *strings.Builder, format string, args ...any) {
		parent := "null"
		if n > 0 {
			parent = fmt.Sprintf(`"l%d"`, n)
		}
		n++
		fmt.Fprintf(b, `{"uuid":"l%d","parentUuid":%s,"timestamp":"2026-11-16T08:%02d:00Z",`+format+"}\n", append([]any{n, parent, n}, args...)...)
	}
	user := func(b *strings.Builder, text string) {
		line(b, `"type":"user","message":{"role":"user","content":%q}`, text)
	}
	reply := func(b *strings.Builder, text string) {
		line(b, `"type":"assistant","message":{"role":"assistant","id":"m%d","content":[{"type":"text","text":%q}]}`, n+1, text)
	}
	tool := func(b *strings.Builder) {
		line(b, `"type":"assistant","message":{"role":"assistant","id":"m%d","content":[{"type":"tool_use","id":"tu%d","name":"Read","input":{"file_path":"a.md"}}]}`, n+1, n+1)
	}
	end := func(b *strings.Builder) { line(b, `"type":"system","subtype":"turn_duration","durationMs":900`) }
	page := func(build func(b *strings.Builder)) []proto.Item {
		var b strings.Builder
		build(&b)
		p := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		f := transcript.NewFile(p)
		if _, err := f.Refresh(); err != nil {
			t.Fatal(err)
		}
		items, _, _ := f.PageTurns(0, 1, 2000)
		return items
	}
	previous := func(b *strings.Builder) { user(b, run); reply(b, "今天三个会"); end(b) }
	for _, c := range []struct {
		name  string
		build func(b *strings.Builder)
		want  bool
	}{
		{"a fresh chat", func(b *strings.Builder) { user(b, run); end(b) }, true},
		{"after a run that replied", func(b *strings.Builder) { previous(b); user(b, run); end(b) }, true},
		{"after a run that replied, seen working only", func(b *strings.Builder) { previous(b); user(b, run) }, true},
		{"this run replied", func(b *strings.Builder) { previous(b); user(b, run); reply(b, "今天两个会"); end(b) }, false},
		{"this run called a tool", func(b *strings.Builder) { previous(b); user(b, run); tool(b); end(b) }, false},
		{"the user wrote since", func(b *strings.Builder) {
			previous(b)
			user(b, run)
			end(b)
			user(b, "顺便看下明天的会")
			end(b)
		}, false},
	} {
		items := page(c.build)
		if got := silentRun(items, task, true); got != c.want {
			t.Errorf("%s: silentRun = %v, want %v (page %+v)", c.name, got, c.want, items)
		}
	}
	// The page did reach back: without the split, the previous run's reply hid the silence.
	if items := page(func(b *strings.Builder) { previous(b); user(b, run); end(b) }); !slices.ContainsFunc(items, func(it proto.Item) bool { return it.Text == "今天三个会" }) {
		t.Fatalf("the page did not reach the previous run: %+v", items)
	}
}

// A run where the turn ends and the session carries a TurnError (e.g. a Codex turn failure on the model service)
// is failed, pushed and retried once (#2571).
func TestTaskTurnEndingInSessionTurnErrorFailsAndRetriesOnce(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.work.switches = &TriageSwitches{TransientRetry: new(false)}
	h.runRetry = 50 * time.Millisecond
	h.turnPoll = 200 * time.Millisecond
	setTasks(h, Task{ID: "p", Title: "值守", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant"})

	// The turn runs, then ends: the session carries TurnError and no assistant text item.
	n.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
		Cwd: "/home/me/.persona/assistant"})
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
	// The turn ends after the first poll saw it working (started after the run went, so the watcher's polls line up).
	go func() {
		time.Sleep(3 * h.turnPoll / 2)
		n.mu.Lock()
		for i := range n.sessions["server"] {
			n.sessions["server"][i].Status, n.sessions["server"][i].State = proto.StatusOffline, proto.StateIdle
			n.sessions["server"][i].TurnError = "unexpected status 503 upstream attempt budget is exhausted"
			n.sessions["server"][i].TurnErrorKind = proto.TurnErrorTransient
		}
		n.mu.Unlock()
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sc := h.loadSchedule(context.Background())
		sc.mu.Lock()
		failed := len(sc.runs) > 0 && slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool {
			return r.RefID == "p" && r.Status == runFailed && strings.Contains(r.Error, "503")
		})
		sc.mu.Unlock()
		if failed {
			if p := n.pushedBy(deadline); len(p) == 0 || p[0].Title != "定时任务失败：值守" || p[0].SessionID != "s1" || p[0].DeviceID != "server" {
				t.Fatalf("the failure is pushed: %+v", p)
			}
			// and the retry went, marked as such
			for time.Now().Before(deadline) {
				if n.sent() >= 2 {
					sc.mu.Lock()
					retried := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RefID == "p" && r.Retried })
					sc.mu.Unlock()
					if retried {
						return
					}
				}
				time.Sleep(50 * time.Millisecond)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the failed run was not recorded")
}

// The failure push sends the user to the task's page. A task switched off or deleted while the retry waited must not
// run again: the retry used the copy the run started with, so it went anyway, with the old prompt, and silently (a
// retry that fails is not pushed).
func TestTaskSwitchedOffOrDeletedAfterFailureIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name string
		undo func(t *testing.T, h *Service)
	}{
		{"switched off", func(t *testing.T, h *Service) {
			sc := h.loadSchedule(context.Background())
			sc.mu.Lock()
			defer sc.mu.Unlock()
			i := sc.indexLocked("p")
			if i < 0 {
				t.Fatal("no task p")
			}
			sc.tasks[i].Enabled = false
		}},
		{"deleted", func(t *testing.T, h *Service) {
			if ok, err := h.deleteTask(context.Background(), "p"); err != nil || !ok {
				t.Fatalf("delete: %v %v", ok, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newFakeHub().withMachine()
			h := newStorelessService(t, n)
			h.runRetry = 50 * time.Millisecond
			h.turnPoll = 200 * time.Millisecond
			n.items = []proto.Item{{Role: "assistant", Kind: "text", Text: "API Error: 503 upstream attempt budget is exhausted"}}
			setTasks(h, Task{ID: "p", Title: "值守", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant"})
			n.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
				Cwd: "/home/me/.persona/assistant"})
			if _, err := runNow(t, h, "p"); err != nil {
				t.Fatal(err)
			}
			tc.undo(t, h) // while the turn still runs: before the watcher can retry
			// The turn ends: the watcher sees working, then idle.
			time.Sleep(3 * h.turnPoll / 2)
			n.mu.Lock()
			for i := range n.sessions["server"] {
				n.sessions["server"][i].Status, n.sessions["server"][i].State = proto.StatusOffline, proto.StateIdle
			}
			n.mu.Unlock()

			// The failure is still recorded and pushed ...
			deadline := time.Now().Add(15 * time.Second)
			for len(n.pushed()) == 0 {
				if !time.Now().Before(deadline) {
					t.Fatal("the failed run was not pushed")
				}
				time.Sleep(50 * time.Millisecond)
			}
			if p := n.pushed(); p[0].Title != "定时任务失败：值守" {
				t.Fatalf("the failure is pushed: %+v", p)
			}
			// ... and the retry does not go.
			time.Sleep(20 * h.runRetry)
			if got := n.sent(); got != 1 {
				t.Fatalf("sent %d times, want the first run only", got)
			}
			sc := h.loadSchedule(context.Background())
			sc.mu.Lock()
			retried := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RefID == "p" && r.Retried })
			sc.mu.Unlock()
			if retried {
				t.Fatal("a retry was recorded")
			}
		})
	}
}

// A model error can end the turn before the first poll sees it working (a vanished model fails immediately): the
// changed last item alone must still end the watch, or the run stays "ok" for the whole timeout.
func TestTaskFailingBeforeTheWatcherSeesItWorking(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = 50 * time.Millisecond
	h.turnPoll = 200 * time.Millisecond
	setTasks(h, Task{ID: "p", Title: "值守", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant"})
	n.items = []proto.Item{{ID: "before", Role: "assistant", Kind: "text", Text: "old"}}
	n.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusOffline, State: proto.StateIdle, Assistant: true,
		Cwd: "/home/me/.persona/assistant"})
	from := n.callCount()
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
	go func() {
		// After the watcher read the last item, before its first poll (a turnPoll later): a fixed sleep raced with
		// a watcher goroutine that started late.
		for stop := time.Now().Add(15 * time.Second); !n.itemsReadAfter(from) && time.Now().Before(stop); {
			time.Sleep(5 * time.Millisecond)
		}
		n.mu.Lock()
		n.items = append(n.items, proto.Item{ID: "err", Role: "assistant", Kind: "text", Text: "There's an issue with the selected model (gone)."})
		n.mu.Unlock()
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sc := h.loadSchedule(context.Background())
		sc.mu.Lock()
		failed := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RefID == "p" && r.Status == runFailed })
		sc.mu.Unlock()
		if failed {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the failure before the first working poll was not recorded")
}

// A daily run's knowledge-base hint names the hub's knowledge directory, wherever it is (PERSONA_HOME, kb_dir), not the
// default ~/.persona/data: the model reads that directory with kb_list and the hint is where it is told.
func TestDailyRunHintNamesTheKnowledgeDir(t *testing.T) {
	n := newFakeHub().withMachine()
	n.hello.KB = &KBSettings{}
	h := newKBService(t, n)
	dir := h.kb.dir
	if dir == "" || strings.Contains(dir, ".persona") {
		t.Fatalf("the test needs a custom knowledge directory: %q", dir)
	}
	setTasks(h, Task{ID: "r", Title: "早报", Prompt: "给我今天的早报", Repeat: repeatDaily, Time: "09:00", Enabled: true})
	if _, err := runNow(t, h, "r"); err != nil {
		t.Fatal(err)
	}
	text := n.lastSend(t)["text"].(string)
	if !strings.Contains(text, "（知识库") {
		t.Fatalf("the first run has the hint: %q", text)
	}
	if strings.Contains(text, "~/.persona/data") || !strings.Contains(text, "（知识库 "+dir+"：") {
		t.Fatalf("the hint should name %s: %q", dir, text)
	}
}
