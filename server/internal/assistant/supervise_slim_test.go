package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A long request (a routine's inspection prompt, a hand-over) is quoted in each supervision message only by its start,
// with where the whole of it is: before, every message about the task repeated up to 300 characters of it.
func TestSuperviseQuotesTheStartOfALongRequest(t *testing.T) {
	long := strings.Repeat("巡检每台机器的磁盘、内存和日志，", 40) // 640 runes
	s, _, _ := briefService(t, long)
	if got, isErr := tool(t, s, "task_plan", briefTask(nil)); isErr {
		t.Fatal(got)
	}
	waitFor(t, "the agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	e := prompts.SuperviseEntry{ID: todo.ID, Title: todo.Title, Kind: sigIdle, Link: "https://hub.example/todos/" + todo.ID}
	s.briefEntry(context.Background(), todo.ID, &e)
	if !e.AskedCut || utf8.RuneCountInString(e.Asked) != superviseAskedRunes+1 || !strings.HasSuffix(e.Asked, "…") {
		t.Fatalf("asked = %q (%d runes), cut %v", e.Asked, utf8.RuneCountInString(e.Asked), e.AskedCut)
	}
	text := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}})
	// The pointer is short: the task's link is in the entry's first line already, not repeated here.
	if strings.Contains(text, long) || !strings.Contains(text, "…（只引了开头，全文见任务链接或 todo_get）") ||
		strings.Count(text, "https://hub.example/todos/"+todo.ID) != 1 {
		t.Fatalf("message:\n%s", text)
	}

	// A short one is quoted whole, as before, with no pointer.
	s2, _, _ := briefService(t, "please fix the login crash before Friday")
	if got, isErr := tool(t, s2, "task_plan", briefTask(nil)); isErr {
		t.Fatal(got)
	}
	waitFor(t, "the agent", func() bool { return len(onlyTodo(s2).Agents) == 1 })
	short := prompts.SuperviseEntry{ID: onlyTodo(s2).ID, Kind: sigIdle}
	s2.briefEntry(context.Background(), short.ID, &short)
	text = prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{short}})
	if short.AskedCut || !strings.Contains(text, "用户原话）：please fix the login crash before Friday\n") || strings.Contains(text, "todo_get "+short.ID) {
		t.Fatalf("short request: cut %v\n%s", short.AskedCut, text)
	}
}

// A report its Agent makes and the end of the same turn are one entry of one message: the stop entry says what the
// Agent reported, with its words and evidence, and the report has no entry of its own.
func TestJoinTurnReportsMakesOneEntry(t *testing.T) {
	list := []prompts.SuperviseEntry{
		{ID: "todo_a", Title: "修登录", Kind: sigReport, Report: upReportDone, Said: "PR 已合并", Evidence: []string{"https://example.com/pr/1"}, Session: "s1"},
		{ID: "todo_b", Title: "别的任务", Kind: sigStalled, Minutes: 30},
		{ID: "todo_a", Title: "修登录", Kind: sigUnjudged, Detail: "判定模型出错", Last: "做完了", Session: "s1", Evidence: []string{"criterion 1"}},
	}
	got := joinTurnReports(list)
	if len(got) != 2 || got[0].ID != "todo_b" || got[1].Kind != sigUnjudged || got[1].Reported != upReportDone ||
		got[1].Said != "PR 已合并" || len(got[1].Evidence) != 2 || got[1].Last != "做完了" {
		t.Fatalf("joined = %+v", got)
	}
	if list[0].Kind != sigReport || len(list[2].Evidence) != 1 {
		t.Fatalf("the caller's entries changed: %+v", list)
	}
	text := prompts.RenderSupervise(prompts.Supervise{Entries: got})
	if strings.Count(text, "todo_a") != 1 || !strings.Contains(text, "停下前它主动向你汇报说做完了（同一轮，合成这一条）") ||
		!strings.Contains(text, "它说：\n   PR 已合并") {
		t.Fatalf("message:\n%s", text)
	}

	// Another session's report, or a report with no stop, stays an entry of its own.
	alone := []prompts.SuperviseEntry{
		{ID: "todo_a", Kind: sigReport, Report: upReportQuestion, Session: "s2"},
		{ID: "todo_a", Kind: sigIdle, Session: "s1"},
		{ID: "todo_c", Kind: sigReport, Report: upReportBlocked},
	}
	if got := joinTurnReports(alone); len(got) != 3 || got[1].Reported != "" {
		t.Fatalf("joined = %+v", got)
	}
}

// End to end: an Agent mid-turn reports done, then stops; the supervisor gets one message with one entry for the task,
// not a message for the report and another for the stop. An Agent that is not working has its report told at once.
func TestReportAndStopOfOneTurnAreOneMessage(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	shortSupervise(t, s)
	s.noReportHold = false
	ctx := context.Background()
	sid := todo.Agents[0].SessionID
	f.setSessions("box", proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	if res := s.agentTool(ctx, "box", sid, "report_to_supervisor", json.RawMessage(`{"kind":"done","text":"PR 已合并","evidence":["https://example.com/pr/7"]}`)); res.IsError {
		t.Fatalf("report: %+v", res)
	}
	reportID := todo.ID + "|" + sigReport + ":" + upReportDone
	held := func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		g, ok := s.supervise.pending[reportID]
		return ok && g.NotBefore.After(time.Now().Add(time.Minute))
	}
	waitFor(t, "the report held for the stop", held)
	if n := len(assistantSendsTo(f, "conv1")); n != 0 {
		t.Fatalf("the report went out before its Agent stopped: %d message(s)", n)
	}
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: time.Now().UTC().Format(time.RFC3339Nano), Session: sid, Machine: "box", Detail: "没做完"})
	waitFor(t, "the supervision message", func() bool { return len(assistantSendsTo(f, "conv1")) == 1 })
	text, _ := lastSendTo(f, "conv1")["text"].(string)
	if strings.Count(text, todo.ID+"，signal=") != 1 || !strings.Contains(text, "signal=idle+report") ||
		!strings.Contains(text, "停下前它主动向你汇报说做完了") || !strings.Contains(text, "PR 已合并") || !strings.Contains(text, "https://example.com/pr/7") {
		t.Fatalf("message:\n%s", text)
	}
	waitFor(t, "nothing left to tell", func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		return len(s.supervise.pending) == 0
	})
	if n := len(assistantSendsTo(f, "conv1")); n != 1 {
		t.Fatalf("%d messages for one turn", n)
	}

	// Not working: told at once, as before.
	s2, f2, todo2 := newSupervisedService(t)
	shortSupervise(t, s2)
	s2.noReportHold = false
	sid2 := todo2.Agents[0].SessionID
	f2.setSessions("box", proto.Session{SessionID: sid2, MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusDone, State: proto.StateUnread, UpdatedAt: time.Now()})
	if res := s2.agentTool(ctx, "box", sid2, "report_to_supervisor", json.RawMessage(`{"kind":"blocked","text":"缺权限"}`)); res.IsError {
		t.Fatalf("report: %+v", res)
	}
	waitFor(t, "the report", func() bool { return len(assistantSendsTo(f2, "conv1")) == 1 })
}

// A done report waits for its Agent's stop (reportStopHold) and goes out with it, in one message; the stop releases
// it in memory and in the durable outbox.
func TestReportWaitsForItsTurnEnd(t *testing.T) {
	s, _ := newQuietService(t)
	s.noReportHold = false
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour} // nothing goes out meanwhile
	ctx := context.Background()
	todo, err := s.newTodo(ctx, "修登录", "fix it", "conv1", "", true)
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"PR merged"}, 30)
	now := time.Now()
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigReport, Key: now.UTC().Format(time.RFC3339Nano), At: now, Report: upReportDone,
		Detail: "PR 已合并", NotBefore: now.Add(reportStopHold)})
	pending := func(id string) (superviseSignal, bool) {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		g, ok := s.supervise.pending[id]
		return g, ok
	}
	reportID := todo.ID + "|" + sigReport + ":" + upReportDone
	held, ok := pending(reportID)
	if !ok || held.NotBefore.IsZero() {
		t.Fatalf("the report is not held: %+v %v", held, ok)
	}
	if wait, ok := digestWait(now.Add(time.Second), time.Time{}, []superviseSignal{held}, superviseTimings{gather: time.Second, every: time.Hour, urgent: time.Hour}); !ok || wait < time.Minute {
		t.Fatalf("the held report is due in %s", wait)
	}
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: now.Add(time.Second).UTC().Format(time.RFC3339Nano), Detail: "没做完"})
	if g, ok := pending(reportID); !ok || !g.NotBefore.IsZero() {
		t.Fatalf("the stop did not release the report: %+v", g)
	}
	s.relays.mu.Lock()
	durable, inOutbox := s.relays.st.SupervisePending[reportID]
	s.relays.mu.Unlock()
	if !inOutbox || !durable.NotBefore.IsZero() {
		t.Fatalf("the outbox still holds the report: %+v", durable)
	}
}
