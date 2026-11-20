package assistant

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The app sets a task's "Push when done" (#2396): POST and PUT take push; a PUT without it — an app that does not know the
// field — keeps it, so does the old app's routines PUT; null, a number or an unknown word is refused, not taken for
// "every".
func TestTaskPushThroughTheApp(t *testing.T) {
	h := newStorelessService(t, newFakeHub())
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	created := func(rec *httptest.ResponseRecorder) Task {
		t.Helper()
		var out struct{ Task Task }
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out.Task
	}
	push := func(id string) string {
		t.Helper()
		task, ok := taskByID(h, id)
		if !ok {
			t.Fatalf("no task %s", id)
		}
		return task.Push
	}

	plain := created(do("POST", "/api/v1/assistant/tasks", `{"title":"早报","prompt":"给我早报","repeat":"daily","time":"09:00","enabled":true}`))
	if plain.Push != pushEvery {
		t.Fatalf("a task created without push pushes every run: %+v", plain)
	}
	mail := created(do("POST", "/api/v1/assistant/tasks", `{"title":"新邮件","prompt":"看看邮箱","repeat":"interval","every_minutes":5,"enabled":true,"push":"asked"}`))
	if mail.Push != pushAsked || !strings.Contains(do("GET", "/api/v1/assistant/tasks", "").Body.String(), `"push":"asked"`) {
		t.Fatalf("created with push asked: %+v", mail)
	}

	body := `{"title":"新邮件","prompt":"看看邮箱有没有新的","repeat":"interval","every_minutes":5,"enabled":false}`
	if rec := do("PUT", "/api/v1/assistant/tasks/"+mail.ID, body); rec.Code != 200 || push(mail.ID) != pushAsked {
		t.Fatalf("a PUT without push keeps it: %d %s, push %q", rec.Code, rec.Body, push(mail.ID))
	}
	for _, bad := range []string{`null`, `1`, `"sometimes"`} {
		with := strings.TrimSuffix(body, "}") + `,"push":` + bad + `}`
		if rec := do("PUT", "/api/v1/assistant/tasks/"+mail.ID, with); rec.Code != 400 || push(mail.ID) != pushAsked {
			t.Fatalf("push %s is refused and changes nothing: %d %s, push %q", bad, rec.Code, rec.Body, push(mail.ID))
		}
		if rec := do("POST", "/api/v1/assistant/tasks", with); rec.Code != 400 {
			t.Fatalf("a new task with push %s is refused: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if rec := do("PUT", "/api/v1/assistant/tasks/"+mail.ID, strings.TrimSuffix(body, "}")+`,"push":""}`); rec.Code != 200 || push(mail.ID) != pushEvery {
		t.Fatalf("push \"\" sets every run: %d %s, push %q", rec.Code, rec.Body, push(mail.ID))
	}

	// The app from before 2026-10-31 saves its routines (the daily tasks) as a list without the field.
	daily := created(do("POST", "/api/v1/assistant/tasks", `{"title":"日报","prompt":"有要紧的才说","repeat":"daily","time":"18:00","enabled":true,"push":"asked"}`))
	routines := `{"routines":[{"id":"` + plain.ID + `","title":"早报","prompt":"给我早报","time":"09:00","enabled":true},` +
		`{"id":"` + daily.ID + `","title":"日报","prompt":"有要紧的才说","time":"18:30","enabled":true}]}`
	if rec := do("PUT", "/api/v1/assistant/routines", routines); rec.Code != 200 {
		t.Fatalf("routines: %d %s", rec.Code, rec.Body)
	}
	if got, _ := taskByID(h, daily.ID); got.Push != pushAsked || got.Time != "18:30" {
		t.Fatalf("the old app's routines PUT keeps push: %+v", got)
	}
}

// The scheduling tools take push: "asked" for a task that watches something, "every" to go back; left out, a new task
// pushes every run and an existing one keeps what it had. Changing push alone does not turn a follow-up into a daily
// routine.
func TestScheduleToolsTakePush(t *testing.T) {
	h := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	setSettings(t, h, func(v *Settings) { v.Main, v.MachineID = "main", "server" })
	tool := func(session, name string, args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		return h.runTool(ctx, "server", session, name, raw)
	}
	only := func(prefix string) Task {
		t.Helper()
		sc := h.loadSchedule(ctx)
		sc.mu.Lock()
		defer sc.mu.Unlock()
		var found []Task
		for _, task := range sc.tasks {
			if strings.HasPrefix(task.ID, prefix) {
				found = append(found, task)
			}
		}
		if len(found) != 1 {
			t.Fatalf("tasks %s*: %+v", prefix, found)
		}
		return found[0]
	}

	out, err := tool("main", "schedule_routine", map[string]any{"title": "日报", "prompt": "有要紧的才告诉我", "time": "18:00", "push": "asked"})
	if err != nil || !strings.Contains(out, "notify_user") {
		t.Fatalf("schedule_routine push asked: %q %v", out, err)
	}
	rt := only("rt_")
	if rt.Push != pushAsked {
		t.Fatalf("routine: %+v", rt)
	}
	if _, err := tool("main", "schedule_routine", map[string]any{"id": rt.ID, "enabled": false}); err != nil || only("rt_").Push != pushAsked {
		t.Fatalf("a change without push keeps it: %v %+v", err, only("rt_"))
	}
	if _, err := tool("main", "schedule_routine", map[string]any{"id": rt.ID, "push": "every"}); err != nil || only("rt_").Push != pushEvery {
		t.Fatalf("push every: %v %+v", err, only("rt_"))
	}
	if _, err := tool("main", "schedule_routine", map[string]any{"id": rt.ID, "push": "sometimes"}); err == nil || only("rt_").Push != pushEvery {
		t.Fatalf("an unknown push is refused: %v %+v", err, only("rt_"))
	}

	out, err = tool("main", "schedule_followup", map[string]any{"prompt": "看看邮箱有没有新邮件", "title": "新邮件", "every": "5m", "push": "asked"})
	if err != nil || !strings.Contains(out, "notify_user") {
		t.Fatalf("schedule_followup push asked: %q %v", out, err)
	}
	fu := only("fu_")
	if fu.Push != pushAsked || fu.Repeat != repeatInterval || fu.SessionID != "" {
		t.Fatalf("a follow-up that pushes only what it sends runs in its own conversation, not the main one: %+v", fu)
	}
	// Asked from another conversation (a leader's patrol), it goes on there (#3673).
	out, err = tool("lead1", "schedule_followup", map[string]any{"prompt": "巡检一遍待办", "title": "巡检", "every": "30m", "push": "asked"})
	if err != nil || !strings.Contains(out, "notify_user") || !strings.Contains(out, "the conversation it came from") {
		t.Fatalf("schedule_followup push asked from a leader conversation: %q %v", out, err)
	}
	sc := h.loadSchedule(ctx)
	sc.mu.Lock()
	var patrol Task
	for _, task := range sc.tasks {
		if task.Title == "巡检" {
			patrol = task
		}
	}
	sc.mu.Unlock()
	if patrol.Push != pushAsked || patrol.SessionID != "lead1" || patrol.Repeat != repeatInterval {
		t.Fatalf("a quiet follow-up keeps the conversation it came from, other than the main one: %+v", patrol)
	}
	if _, err := h.deleteTask(ctx, patrol.ID); err != nil {
		t.Fatal(err)
	}
	// schedule_routine with an id changes any task's push, and only that.
	if _, err := tool("main", "schedule_routine", map[string]any{"id": fu.ID, "push": "every"}); err != nil {
		t.Fatal(err)
	}
	if got := only("fu_"); got.Push != pushEvery || got.Repeat != repeatInterval || got.EveryMinutes != 5 {
		t.Fatalf("push changed, nothing else: %+v", got)
	}
	if _, err := tool("main", "schedule_followup", map[string]any{"prompt": "x", "at": "30m", "push": "never"}); err == nil {
		t.Fatal("schedule_followup refuses an unknown push")
	}

	// list_followups says which tasks push only what they send.
	if _, err := tool("main", "schedule_routine", map[string]any{"id": fu.ID, "push": "asked"}); err != nil {
		t.Fatal(err)
	}
	list := h.describeSchedule(ctx, "")
	line := ""
	for _, l := range strings.Split(list, "\n") {
		if strings.Contains(l, fu.ID) {
			line = l
		}
	}
	if !strings.Contains(line, "notify_user") {
		t.Fatalf("list_followups says the follow-up pushes only what it sends: %s", list)
	}
	for _, l := range strings.Split(list, "\n") {
		if strings.Contains(l, rt.ID) && strings.Contains(l, "notify_user") {
			t.Fatalf("a task that pushes every run is listed plainly: %s", l)
		}
	}
}

// A follow-up a task's own run schedules for itself reschedules that task (scheduleOwn): it keeps the task's push
// unless the call gives one.
func TestScheduleOwnKeepsPush(t *testing.T) {
	h := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	setTasks(h, Task{ID: "fu_own", Title: "新邮件", Prompt: "看看邮箱", Repeat: repeatOnce, At: time.Now().Add(-time.Minute), Enabled: false,
		MachineID: "server", Push: pushAsked, LastRun: time.Now().Add(-time.Minute)})
	sc := h.loadSchedule(ctx)
	sc.mu.Lock()
	sc.runs = append(sc.runs, ScheduleRun{RefID: "fu_own", SessionID: "run-1", Status: runOK})
	sc.mu.Unlock()
	tool := func(args map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(args)
		if _, err := h.runTool(ctx, "server", "run-1", "schedule_followup", raw); err != nil {
			t.Fatal(err)
		}
	}
	tool(map[string]any{"prompt": "看看邮箱", "title": "新邮件", "at": "30m"})
	if got, _ := taskByID(h, "fu_own"); got.Push != pushAsked || !got.Enabled {
		t.Fatalf("rescheduled without push: %+v", got)
	}
	tool(map[string]any{"prompt": "看看邮箱", "title": "新邮件", "at": "30m", "push": "every"})
	if got, _ := taskByID(h, "fu_own"); got.Push != pushEvery {
		t.Fatalf("rescheduled with push every: %+v", got)
	}
}

// A run rescheduling a follow-up that continues a conversation cannot give it its own launcher: those runs keep the
// conversation's. Refused before the task is touched.
func TestScheduleOwnRefusesAModelOverrideIntoASourceConversation(t *testing.T) {
	h := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	setTasks(h, Task{ID: "fu_own", Title: "新邮件", Prompt: "看看邮箱", Repeat: repeatOnce, At: time.Now().Add(-time.Minute), Enabled: false,
		MachineID: "server", SessionID: "source-1", LastRun: time.Now().Add(-time.Minute)})
	sc := h.loadSchedule(ctx)
	sc.mu.Lock()
	sc.runs = append(sc.runs, ScheduleRun{RefID: "fu_own", SessionID: "run-1", Status: runOK})
	sc.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"prompt": "看看邮箱", "title": "新邮件", "at": "30m", "launcher": "cheap"})
	if _, err := h.runTool(ctx, "server", "run-1", "schedule_followup", raw); err == nil {
		t.Fatal("a launcher override for a run into a source conversation was accepted")
	}
	if got, _ := taskByID(h, "fu_own"); got.Enabled {
		t.Fatalf("the refused call still rescheduled the task: %+v", got)
	}
	// A task with its own conversation: an invalid launcher is refused before the reschedule is saved.
	setTasks(h, Task{ID: "fu_own2", Title: "自己的", Prompt: "看看", Repeat: repeatOnce, At: time.Now().Add(-time.Minute), Enabled: false,
		MachineID: "server"})
	sc = h.loadSchedule(ctx)
	sc.mu.Lock()
	sc.runs = append(sc.runs, ScheduleRun{RefID: "fu_own2", SessionID: "run-2", Status: runOK})
	sc.mu.Unlock()
	raw, _ = json.Marshal(map[string]any{"prompt": "看看", "title": "自己的", "at": "30m", "launcher": "no-such-launcher"})
	if _, err := h.runTool(ctx, "server", "run-2", "schedule_followup", raw); err == nil {
		t.Fatal("an invalid launcher was accepted")
	}
	if got, _ := taskByID(h, "fu_own2"); got.Enabled {
		t.Fatalf("the refused call still rescheduled the task: %+v", got)
	}
}

// A task that pushes only what its runs send never runs in the main conversation (#2396): set from the app, a
// follow-up that ran in the main one moves to its own; switched back, it stays there.
func TestQuietTaskRunsInItsOwnConversation(t *testing.T) {
	h := newStorelessService(t, newFakeHub().withMachine())
	setSettings(t, h, func(v *Settings) { v.Main, v.MachineID = "main-1", "server" })
	setTasks(h, Task{ID: "fu_1", Title: "新邮件", Prompt: "看看邮箱", Repeat: repeatInterval, EveryMinutes: 5, At: time.Now().Add(time.Minute),
		Enabled: true, MachineID: "server", SessionID: "main-1"})
	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/assistant/tasks/fu_1", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	put(`{"title":"新邮件","prompt":"看看邮箱","repeat":"interval","every_minutes":5,"enabled":true,"session_id":"main-1","push":"asked"}`)
	if got, _ := taskByID(h, "fu_1"); got.Push != pushAsked || got.SessionID != "" {
		t.Fatalf("push asked moves it to its own conversation: %+v", got)
	}
	put(`{"title":"新邮件","prompt":"看看邮箱","repeat":"interval","every_minutes":5,"enabled":true,"push":""}`)
	if got, _ := taskByID(h, "fu_1"); got.Push != pushEvery || got.SessionID != "" {
		t.Fatalf("switched back, it keeps its own conversation: %+v", got)
	}
	// The snapshot tells the hub (TaskView.Push), which leaves that conversation's finished turns unpushed.
	put(`{"title":"新邮件","prompt":"看看邮箱","repeat":"interval","every_minutes":5,"enabled":true,"push":"asked"}`)
	views := h.taskViews(context.Background())
	if len(views) != 1 || views[0].Push != pushAsked {
		t.Fatalf("task views: %+v", views)
	}
}

// The channels' mirror leaves out a quiet task's own conversation when its turn ended normally on Claude Code or a
// headless Codex, like the hub's finished push; an error (a Codex turn the model service cut off too), a cut-off, a
// runtime's, another conversation or a task that pushes every run is mirrored as ever.
func TestQuietTaskTurn(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	ctx := context.Background()
	setTasks(h,
		Task{ID: "fu_q", Title: "新邮件", Prompt: "x", Repeat: repeatInterval, EveryMinutes: 5, Enabled: true, Push: pushAsked, RunMachineID: "server", RunSessionID: "own-q"},
		Task{ID: "fu_e", Title: "早报", Prompt: "y", Repeat: repeatInterval, EveryMinutes: 5, Enabled: true, RunMachineID: "server", RunSessionID: "own-e"})
	for _, c := range []struct {
		name string
		s    proto.Session
		want bool
	}{
		{"normal finish", proto.Session{SessionID: "own-q", Status: proto.StatusDone, LastText: "没有新邮件。"}, true},
		{"model error", proto.Session{SessionID: "own-q", Status: proto.StatusDone, LastText: "没有新邮件。", TurnError: "overloaded"}, false},
		{"cut off", proto.Session{SessionID: "own-q", Status: proto.StatusDone, LastText: "API Error: 529 overloaded"}, false},
		{"a task that pushes every run", proto.Session{SessionID: "own-e", Status: proto.StatusDone, LastText: "早报好了。"}, false},
		{"another conversation", proto.Session{SessionID: "main-1", Status: proto.StatusDone, LastText: "好的。"}, false},
		{"headless codex", proto.Session{SessionID: "own-q", Status: proto.StatusDone, LastText: "没有新邮件。", Agent: "codex"}, true},
		{"headless codex, failed turn", proto.Session{SessionID: "own-q", Status: proto.StatusDone, LastText: "我先查一下收件箱。", Agent: "codex",
			TurnError: "unexpected status 503 Service Unavailable", TurnErrorKind: proto.TurnErrorTransient}, false},
		{"a runtime", proto.Session{SessionID: "own-q", Status: proto.StatusDone, LastText: "没有新邮件。", Agent: "dsh"}, false},
	} {
		n.setSessions("server", c.s)
		if got := h.quietTaskTurn(ctx, "server", c.s.SessionID); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Another task does not run into a quiet task's conversation, where its finished turns would go unpushed: a follow-up
// asked from there runs in its own, and one that already pointed there is sent elsewhere (#2396).
func TestOtherTasksStayOutOfAQuietConversation(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	ctx := context.Background()
	setTasks(h,
		Task{ID: "fu_q", Title: "新邮件", Prompt: "看看邮箱", Repeat: repeatInterval, EveryMinutes: 5, At: time.Now().Add(time.Hour), Enabled: true,
			MachineID: "server", Push: pushAsked, RunMachineID: "server", RunSessionID: "own-q", RunLauncher: "claude"},
		Task{ID: "fu_b", Title: "提醒喝水", Prompt: "提醒我喝水", Repeat: repeatOnce, At: time.Now().Add(time.Hour), Enabled: true,
			MachineID: "server", SessionID: "own-q"})
	raw, _ := json.Marshal(map[string]any{"prompt": "一小时后提醒我开会", "title": "开会", "at": "1h"})
	if _, err := h.runTool(ctx, "server", "own-q", "schedule_followup", raw); err != nil {
		t.Fatal(err)
	}
	sc := h.loadSchedule(ctx)
	sc.mu.Lock()
	var made Task
	for _, task := range sc.tasks {
		if task.Title == "开会" {
			made = task
		}
	}
	sc.mu.Unlock()
	if made.ID == "" || made.SessionID != "" || made.Push != pushEvery {
		t.Fatalf("a follow-up asked in a quiet task's conversation runs in its own: %+v", made)
	}
	n.setSessions("server", proto.Session{SessionID: "own-q", Status: proto.StatusDone, Assistant: true})
	if _, err := runNow(t, h, "fu_b"); err != nil {
		t.Fatal(err)
	}
	if got := n.lastSend(t)["session_id"]; got == "own-q" {
		t.Fatalf("a task that pushes every run was sent into the quiet conversation: %v", n.lastSend(t))
	}
}

// A quiet task whose conversation became the main one starts a new one: the main conversation is never quiet.
func TestQuietTaskLeavesTheMainConversation(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	ctx := context.Background()
	setTasks(h, Task{ID: "fu_q", Title: "新邮件", Prompt: "看看邮箱", Repeat: repeatInterval, EveryMinutes: 5, At: time.Now().Add(time.Hour),
		Enabled: true, MachineID: "server", Push: pushAsked})
	if _, err := runNow(t, h, "fu_q"); err != nil { // the first run starts the task's own conversation
		t.Fatal(err)
	}
	own, _ := taskByID(h, "fu_q")
	if own.RunSessionID == "" {
		t.Fatalf("no conversation of its own: %+v", own)
	}
	n.setSessions("server", proto.Session{SessionID: own.RunSessionID, Status: proto.StatusDone, Assistant: true, LastText: "没有新邮件。"})
	if _, err := runNow(t, h, "fu_q"); err != nil {
		t.Fatal(err)
	}
	if p := n.lastSend(t); p["session_id"] != own.RunSessionID {
		t.Fatalf("the next run goes on in its own conversation: %v", p)
	}
	if !h.quietTaskTurn(ctx, "server", own.RunSessionID) {
		t.Fatal("its own conversation's normal finish is not mirrored")
	}

	setSettings(t, h, func(v *Settings) { v.Main = own.RunSessionID }) // the user makes it the main conversation
	if h.quietTaskTurn(ctx, "server", own.RunSessionID) {
		t.Fatal("the main conversation's turns are mirrored as ever")
	}
	if _, err := runNow(t, h, "fu_q"); err != nil {
		t.Fatal(err)
	}
	if p := n.lastSend(t); p["session_id"] == own.RunSessionID {
		t.Fatalf("the run went into the main conversation: %v", p)
	}
}

// A quiet task's run, renewing itself with the message it was sent (what the prompt tells a patrol to do), is
// rescheduled — the note about pushing does not stop it being recognised — and keeps its push; the saved prompt has
// none of the run's notes.
func TestRenewingAQuietTaskKeepsIt(t *testing.T) {
	h := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	task := Task{ID: "fu_own", Title: "新邮件", Prompt: "看看邮箱有没有新邮件，有就告诉我", Repeat: repeatOnce, At: time.Now().Add(-time.Minute),
		Enabled: false, MachineID: "server", Push: pushAsked, LastRun: time.Now().Add(-time.Minute), RunMachineID: "server", RunSessionID: "run-1"}
	setTasks(h, task)
	sc := h.loadSchedule(ctx)
	sc.mu.Lock()
	sc.runs = append(sc.runs, ScheduleRun{RefID: "fu_own", SessionID: "run-1", Status: runOK})
	sc.mu.Unlock()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	for _, continued := range []bool{false, true} {
		sent := taskPrompt(task, time.Now(), time.Now(), loc, false, false, continued)
		if !strings.Contains(sent, "notify_user") {
			t.Fatalf("the run's message says how it pushes: %s", sent)
		}
		raw, _ := json.Marshal(map[string]any{"prompt": sent, "at": "30m", "new_conversation": true})
		if _, err := h.runTool(ctx, "server", "run-1", "schedule_followup", raw); err != nil {
			t.Fatal(err)
		}
		sc.mu.Lock()
		n := len(sc.tasks)
		sc.mu.Unlock()
		got, _ := taskByID(h, "fu_own")
		if n != 1 || got.Push != pushAsked || !got.Enabled || got.Prompt != task.Prompt {
			t.Fatalf("continued %v: renewed as itself, asked, with its own prompt: %d tasks, %+v", continued, n, got)
		}
	}
}

// Settings that could not be read are not "no main conversation": the quiet task's conversation, which the user may
// have made the main one, is mirrored as ever, and the task does not go on in it.
func TestQuietTaskWhenTheMainConversationIsUnknown(t *testing.T) {
	ctx := context.Background()
	store := &flakyScheduleStore{data: map[string][]byte{}}
	n := newFakeHub().withMachine()
	h := newServiceOn(t, n, store)
	setTasks(h, Task{ID: "fu_q", Title: "新邮件", Prompt: "看看邮箱", Repeat: repeatInterval, EveryMinutes: 5, At: time.Now().Add(time.Hour),
		Enabled: true, MachineID: "server", Push: pushAsked, RunMachineID: "server", RunSessionID: "own-q", RunLauncher: "claude"})
	n.setSessions("server", proto.Session{SessionID: "own-q", Status: proto.StatusDone, Assistant: true, LastText: "没有新邮件。"})
	if !h.quietTaskTurn(ctx, "server", "own-q") || h.mayBeMain(ctx, "own-q") {
		t.Fatal("with the settings read (no main conversation), the quiet task's normal finish is not mirrored")
	}
	store.mu.Lock()
	store.failRead = true
	store.mu.Unlock()
	h.prefs.mu.Lock()
	h.prefs.cached = nil // what was read has gone stale
	h.prefs.mu.Unlock()
	if h.quietTaskTurn(ctx, "server", "own-q") {
		t.Fatal("the main conversation is not known: mirrored as ever")
	}
	if !h.mayBeMain(ctx, "own-q") {
		t.Fatal("the task's conversation may be the main one: it does not go on in it")
	}
}

// A quiet follow-up keeps its source conversation without holding all its pushes. Its actual input identifies a
// quiet completion; a later user turn has no such mode. The main conversation remains excluded.
func TestQuietFollowupUsesItsInputSource(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	ctx := context.Background()
	setSettings(t, h, func(v *Settings) { v.Main, v.MachineID = "main-1", "server" })
	setTasks(h,
		Task{ID: "fu_p", Title: "巡检", Prompt: "巡检一遍待办", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(time.Hour),
			Enabled: true, MachineID: "server", SessionID: "lead1", Push: pushAsked},
		Task{ID: "fu_e", Title: "日报", Prompt: "写日报", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(time.Hour),
			Enabled: true, MachineID: "server", SessionID: "lead2"})
	idle := proto.Session{SessionID: "lead1", Status: proto.StatusDone, Assistant: true, LastText: "没有新情况。"}
	n.setSessions("server", idle, proto.Session{SessionID: "lead2", Status: proto.StatusDone, Assistant: true, LastText: "日报好了。"})
	n.known["lead1"], n.known["lead2"] = true, true // both listed sources have transcripts that resume_only may continue

	if _, err := runNow(t, h, "fu_p"); err != nil {
		t.Fatal(err)
	}
	if p := n.lastSend(t); p["session_id"] != "lead1" || p[holdPushesParam] == true {
		t.Fatalf("the run goes on in its conversation without a conversation-wide hold: %v", p)
	}
	n.mu.Lock()
	n.items = []proto.Item{
		{Role: "user", Kind: proto.KindText, Text: "巡检一遍待办", Source: taskMessageSource(Task{SessionID: "lead1", Push: pushAsked})},
		{Role: "assistant", Kind: proto.KindText, Text: "没有新情况。"},
	}
	n.mu.Unlock()
	if h.supervising("lead1") || !h.quietTaskTurn(ctx, "server", "lead1") {
		t.Fatal("the actual scheduled input, not the conversation, makes this answer quiet")
	}
	failed := idle
	failed.TurnError = "overloaded"
	n.setSessions("server", failed)
	if h.quietTaskTurn(ctx, "server", "lead1") {
		t.Fatal("a failed run is mirrored as ever")
	}
	n.setSessions("server", idle)
	h.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "lead1"})
	if !h.quietTaskTurn(ctx, "server", "lead1") {
		t.Fatal("receiving another input cannot change the patrol reply's origin")
	}
	n.mu.Lock()
	n.items = []proto.Item{
		{Role: "user", Kind: proto.KindText, Text: "Show me the details"},
		{Role: "assistant", Kind: proto.KindText, Text: "Here are the details"},
	}
	n.mu.Unlock()
	if h.quietTaskTurn(ctx, "server", "lead1") {
		t.Fatal("the user's own subsequent turn is mirrored as ever")
	}

	busy := idle
	busy.Status = proto.StatusWorking
	n.setSessions("server", busy)
	if _, err := runNow(t, h, "fu_p"); err != nil {
		t.Fatal(err)
	}
	if p := n.lastSend(t); p["session_id"] != "lead1" || p[holdPushesParam] == true || h.supervising("lead1") {
		t.Fatalf("a run that comes while another turn is on holds nothing: %v", p)
	}

	if _, err := runNow(t, h, "fu_e"); err != nil {
		t.Fatal(err)
	}
	if p := n.lastSend(t); p["session_id"] != "lead2" || p[holdPushesParam] == true || h.supervising("lead2") {
		t.Fatalf("a task that pushes every run holds nothing: %v", p)
	}

	setSettings(t, h, func(v *Settings) { v.Main = "lead1" })
	n.setSessions("server", idle)
	if _, err := runNow(t, h, "fu_p"); err != nil {
		t.Fatal(err)
	}
	if p := n.lastSend(t); p["session_id"] == "lead1" || h.supervising("lead1") {
		t.Fatalf("the main conversation is never quiet: %v", p)
	}
}

// The app sets push "asked" on a follow-up that goes on in a conversation other than the main one: it keeps that
// conversation, as the API reads back (#3673).
func TestQuietFollowupKeepsItsConversationThroughTheApp(t *testing.T) {
	h := newStorelessService(t, newFakeHub().withMachine())
	setSettings(t, h, func(v *Settings) { v.Main, v.MachineID = "main-1", "server" })
	setTasks(h, Task{ID: "fu_1", Title: "巡检", Prompt: "巡检一遍待办", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(time.Minute),
		Enabled: true, MachineID: "server", SessionID: "lead1"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/assistant/tasks/fu_1", strings.NewReader(
		`{"title":"巡检","prompt":"巡检一遍待办","repeat":"interval","every_minutes":30,"enabled":true,"machine_id":"server","session_id":"lead1","push":"asked"}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got, _ := taskByID(h, "fu_1")
	if got.Push != pushAsked || got.SessionID != "lead1" {
		t.Fatalf("push asked keeps the conversation it came from: %+v", got)
	}
	list := httptest.NewRecorder()
	h.ServeHTTP(list, httptest.NewRequest("GET", "/api/v1/assistant/tasks", nil))
	if body := list.Body.String(); !strings.Contains(body, `"session_id":"lead1"`) || !strings.Contains(body, `"push":"asked"`) {
		t.Fatalf("read back: %s", body)
	}
}
