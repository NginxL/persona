package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// An agent on a reporting conversation's open todo reports back to that conversation while agent tasks are on. The
// report keeps the conversation's existing reporter destination instead of becoming a main-conversation supervision
// message. With agent tasks off, the old no-receiver answer and absence of report side effects are unchanged.
func TestReportFromAReportingTodoReachesItsReportingConversation(t *testing.T) {
	ctx := context.Background()
	report := json.RawMessage(`{"kind":"done","text":"MR 提好了","evidence":["https://example.com/mr/7"]}`)

	t.Run("agent tasks off", func(t *testing.T) {
		s, f := reportingWithTask(t)
		todo := onlyTodo(s)
		if todo.Origin != "lark1" || todo.DoneAt != nil || todo.Supervision != nil {
			t.Fatalf("reporting conversation's todo = %+v, supervision %+v", todo, todo.Supervision)
		}
		res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", report)
		if res.IsError || !strings.Contains(res.Content[0].Text, "Nobody receives this") {
			t.Fatalf("agent_tasks off report: %+v", res)
		}
		if got := todoNow(s, todo.ID); got.Supervision != nil {
			t.Fatalf("agent_tasks off made a supervision ledger: %+v", got.Supervision)
		}
		if kinds := feedKinds(s, "lark1"); strings.Contains(kinds, "report") {
			t.Fatalf("agent_tasks off added a report to the reporting conversation's feed: %q", kinds)
		}
		if kinds := feedKinds(s, "conv1"); strings.Contains(kinds, "report") {
			t.Fatalf("agent_tasks off added a report to the main conversation's feed: %q", kinds)
		}
		if got := f.noticesTo("lark1"); len(got) != 0 {
			t.Fatalf("agent_tasks off sent a supervision notice to the reporting conversation: %q", got)
		}
		if got := f.notices(); len(got) != 0 {
			t.Fatalf("agent_tasks off sent a supervision notice to the main conversation: %q", got)
		}
	})

	t.Run("agent tasks on", func(t *testing.T) {
		s, f := reportingWithTask(t)
		agentTasksOn(t, s)
		// An idle, active reporting conversation can receive its notice immediately.
		f.setSessions("server",
			proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "lark1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		todo := onlyTodo(s)
		if todo.Origin != "lark1" || todo.DoneAt != nil || todo.Supervision != nil {
			t.Fatalf("reporting conversation's todo = %+v, supervision %+v", todo, todo.Supervision)
		}
		res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", report)
		if res.IsError || !strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
			t.Fatalf("the reporting conversation's open todo has no report receiver: %+v", res)
		}
		if got := todoNow(s, todo.ID); got.Supervision != nil {
			t.Fatalf("the report changed the reporting task's stored supervision: %+v", got.Supervision)
		}
		waitFor(t, "the reporting conversation's supervision notice", func() bool {
			return strings.Contains(strings.Join(f.noticesTo("lark1"), "\n"), "MR 提好了")
		})
		if n := strings.Join(f.noticesTo("lark1"), "\n"); !strings.Contains(n, "📋") ||
			!strings.Contains(n, "MR 提好了") || !strings.Contains(n, "https://example.com/mr/7") {
			t.Fatalf("reporting conversation's supervision notice:\n%s", n)
		}
		if kinds := feedKinds(s, "lark1"); !strings.Contains(kinds, "report") {
			t.Fatalf("the reporting conversation's feed has no report: %q", kinds)
		}
		if got := f.notices(); len(got) != 0 {
			t.Fatalf("the report was delivered to the main conversation too: %q", got)
		}
		if kinds := feedKinds(s, "conv1"); strings.Contains(kinds, "report") {
			t.Fatalf("the main conversation's feed received the report: %q", kinds)
		}
	})
}

func TestReportingReportSourceUsesOwnerLanguage(t *testing.T) {
	for _, tt := range []struct{ lang, title string }{
		{"en", "Supervisor"}, {"zh-Hans", "督办"}, {"ja", "監督"},
	} {
		t.Run(tt.lang, func(t *testing.T) {
			s, f := reportingWithTask(t)
			f.mu.Lock()
			f.hello.Locale = tt.lang
			f.mu.Unlock()
			agentTasksOn(t, s)
			report := json.RawMessage(`{"kind":"done","text":"synthetic completed report"}`)
			if res := s.agentTool(context.Background(), "box", "s1", "report_to_supervisor", report); res.IsError {
				t.Fatalf("report: %+v", res)
			}
			queued := func() []relayMsg {
				b := &s.relays
				b.mu.Lock()
				defer b.mu.Unlock()
				return append([]relayMsg(nil), b.st.Queue["lark1"]...)
			}
			waitFor(t, "the localized queued report", func() bool { return len(queued()) == 1 })
			if got := queued()[0].FromTitle; got != tt.title {
				t.Fatalf("report source = %q, want %q", got, tt.title)
			}
		})
	}
}

func TestReportFromAReportingTodoUsesItsReporterMachine(t *testing.T) {
	s, f := reportingWithTask(t)
	ctx := context.Background()
	agentTasksOn(t, s)
	s.markReporter(ctx, "other", "lark1")
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	f.setSessions("other", proto.Session{SessionID: "lark1", MachineID: "other", Assistant: true, Status: proto.StatusIdle})
	report := json.RawMessage(`{"kind":"done","text":"remote reporter"}`)
	if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", report); res.IsError ||
		!strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
		t.Fatalf("report: %+v", res)
	}
	waitFor(t, "the reporter on its own machine", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("lark1"), "\n"), "remote reporter")
	})
	if got := f.notices(); len(got) != 0 {
		t.Fatalf("the report fell back to main: %q", got)
	}
}

func TestReportFromAReportingTodoKeepsRuntimeReporterDelivery(t *testing.T) {
	s, f := reportingWithTask(t)
	ctx := context.Background()
	agentTasksOn(t, s)
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lark1", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Status: proto.StatusIdle})
	report := json.RawMessage(`{"kind":"done","text":"runtime reporter"}`)
	if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", report); res.IsError ||
		!strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
		t.Fatalf("report: %+v", res)
	}
	waitFor(t, "the runtime reporter's existing notice path", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("lark1"), "\n"), "runtime reporter")
	})
	if got := f.notices(); len(got) != 0 {
		t.Fatalf("the runtime report fell back to main: %q", got)
	}
}

func TestReportFromAReportingTodoHonoursReporterStop(t *testing.T) {
	s, f := reportingWithTask(t) // the reporting conversation is working, so its report queues
	ctx := context.Background()
	agentTasksOn(t, s)
	report := json.RawMessage(`{"kind":"done","text":"queued report"}`)
	if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", report); res.IsError ||
		!strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
		t.Fatalf("report: %+v", res)
	}
	waitFor(t, "the queued reporting report", func() bool {
		q, _ := s.queuedRelays()
		return len(q["lark1"]) > 0
	})
	if _, err := s.stopReporting(ctx, "lark1", "app"); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.queuedRelays(); len(q["lark1"]) != 0 {
		t.Fatalf("the stopped reporter still has a queued report: %q", q["lark1"])
	}
	if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", report); res.IsError ||
		!strings.Contains(res.Content[0].Text, "Nobody receives this") {
		t.Fatalf("post-stop report: %+v", res)
	}
	if got := f.notices(); len(got) != 0 {
		t.Fatalf("the stopped report went to main: %q", got)
	}
}

func TestReportFromAReportingTodoSurvivesLaterQuietNotice(t *testing.T) {
	s, f := reportingWithTask(t) // the reporting conversation is working, so both messages queue
	ctx := context.Background()
	agentTasksOn(t, s)
	report := json.RawMessage(`{"kind":"done","text":"queued report"}`)
	if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", report); res.IsError ||
		!strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
		t.Fatalf("report: %+v", res)
	}
	queued := func() []relayMsg {
		b := &s.relays
		b.mu.Lock()
		defer b.mu.Unlock()
		return append([]relayMsg(nil), b.st.Queue["lark1"]...)
	}
	waitFor(t, "the queued reporting report", func() bool { return len(queued()) == 1 })
	agentTurn(s, f, proto.StatusDone, "MR 提好了", 0)
	waitFor(t, "the queued quiet completion notice", func() bool {
		for _, msg := range queued() {
			if strings.Contains(msg.Text, "🔔") {
				return true
			}
		}
		return false
	})
	messages := queued()
	var reportKept, quietKept bool
	for _, msg := range messages {
		reportKept = reportKept || strings.Contains(msg.Text, "queued report")
		quietKept = quietKept || strings.Contains(msg.Text, "🔔") && strings.Contains(msg.Text, "MR 提好了")
	}
	if len(messages) != 2 || !reportKept || !quietKept {
		t.Fatalf("queued report and quiet notice = %+v", messages)
	}
}

func TestReportFromAReportingTodoLeavesQuietNoticesAlone(t *testing.T) {
	s, f := reportingWithTask(t)
	ctx := context.Background()
	agentTasksOn(t, s)
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lark1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	report := json.RawMessage(`{"kind":"progress","text":"still working"}`)
	if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", report); res.IsError {
		t.Fatalf("report: %+v", res)
	}
	before := len(f.noticesTo("lark1"))
	agentTurn(s, f, proto.StatusDone, "MR 提好了", 0)
	waitFor(t, "the existing quiet completion notice", func() bool {
		got := f.noticesTo("lark1")
		return len(got) > before && strings.Contains(strings.Join(got[before:], "\n"), "🔔")
	})
	if got := strings.Join(f.notices(), "\n"); strings.Contains(got, "MR 提好了") {
		t.Fatalf("the quiet completion notice moved to main: %s", got)
	}
}

func TestReportFromAReportingTodoDefersQuestionInItsConversation(t *testing.T) {
	s, f := reportingWithTask(t)
	ctx := context.Background()
	agentTasksOn(t, s)
	if !s.laterBriefed(ctx) {
		t.Fatal("the fixture needs a facts-enabled daily briefing to reproduce the main-queue diversion")
	}
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lark1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	report := json.RawMessage(`{"kind":"question","text":"哪个分支？","evidence":["https://example.com/mr/7"],"urgent":false}`)
	res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", report)
	if res.IsError || !strings.Contains(res.Content[0].Text, "Queued without waking your reporting conversation") {
		t.Fatalf("report: %+v", res)
	}
	if got := s.laterOpen(ctx, time.Now()); len(got) != 0 {
		t.Fatalf("the reporting question entered the main briefing queue: %+v", got)
	}
	items, _, err := s.feed(ctx, "lark1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var reports []reportBody
	for _, item := range items {
		if item.Kind != feedReport {
			continue
		}
		var body reportBody
		if err := json.Unmarshal(item.Body, &body); err != nil {
			t.Fatal(err)
		}
		reports = append(reports, body)
	}
	if len(reports) != 1 || !reports[0].Later || reports[0].Kind != upReportQuestion || reports[0].Text != "哪个分支？" ||
		len(reports[0].Evidence) != 1 {
		t.Fatalf("reporting conversation's deferred report: %+v", reports)
	}
	if got := reportItems(t, s); len(got) != 0 {
		t.Fatalf("the main conversation received the deferred question: %+v", got)
	}
	if pendingSignals(s) != 0 || len(f.noticesTo("lark1")) != 0 || len(f.notices()) != 0 {
		t.Fatalf("the non-urgent question woke someone: pending %d, reporter %q, main %q",
			pendingSignals(s), f.noticesTo("lark1"), f.notices())
	}
}

func TestReportFromAReportingTodoRechecksReporterRegistration(t *testing.T) {
	s, f := reportingWithTask(t)
	ctx := context.Background()
	agentTasksOn(t, s)
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lark1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	first := json.RawMessage(`{"kind":"done","text":"before expiry"}`)
	if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", first); res.IsError {
		t.Fatalf("first report: %+v", res)
	}
	waitFor(t, "the active reporter's report", func() bool { return len(f.noticesTo("lark1")) > 0 })

	s.reporters.mu.Lock()
	r := s.reporters.m["lark1"]
	r.At = time.Now().Add(-reporterKeep - time.Hour)
	s.reporters.m["lark1"] = r
	s.reporters.mu.Unlock()
	second := json.RawMessage(`{"kind":"done","text":"after expiry"}`)
	if res := s.agentTool(ctx, "box", "s1", "report_to_supervisor", second); res.IsError ||
		!strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
		t.Fatalf("expired report: %+v", res)
	}
	waitFor(t, "the expired reporter's main-conversation fallback", func() bool {
		return strings.Contains(strings.Join(f.notices(), "\n"), "after expiry")
	})
	if got := strings.Join(f.noticesTo("lark1"), "\n"); strings.Contains(got, "after expiry") {
		t.Fatalf("the expired reporter still received the report: %s", got)
	}
}
