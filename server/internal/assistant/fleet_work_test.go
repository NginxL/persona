package assistant

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The work line of an agent waiting on the user reads in the language asked for, Chinese when none was said.
func TestInteractionLineLanguages(t *testing.T) {
	plan := proto.Interaction{Kind: proto.IxPlan}
	perm := proto.Interaction{Kind: proto.IxPermission, Summary: "Bash: go test"}
	for _, c := range []struct {
		l    i18n.Lang
		ix   proto.Interaction
		want string
	}{
		{"", plan, "计划等你审批"},
		{i18n.ZhHans, perm, "需要你授权：Bash: go test"},
		{i18n.En, plan, "Plan awaiting approval"},
		{i18n.En, perm, "Needs your permission: Bash: go test"},
		{i18n.Ja, proto.Interaction{}, "ターミナルで対応が必要です"},
	} {
		if got := interactionLine(c.l, c.ix); got != c.want {
			t.Errorf("%q %s: %q, want %q", c.l, c.ix.Kind, got, c.want)
		}
	}
}

func TestWorkState(t *testing.T) {
	done := &proto.WorkState{Phase: proto.WorkDone}
	wake := time.Now()
	ext := &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Wait: "部署", WakeAt: &wake}
	unwoken := &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Wait: "部署"}
	background := &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Line: "后台 1 个任务在跑"}
	user := &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser}
	for _, c := range []struct {
		state string
		w     *proto.WorkState
		want  string
	}{
		{proto.StateUnread, done, proto.StateIdle},
		{proto.StateUnread, ext, proto.StateIdle},
		{proto.StateUnread, user, proto.StateUnread},
		{proto.StateUnread, unwoken, proto.StateUnread},
		{proto.StateUnread, background, proto.StateIdle},
		{proto.StateUnread, nil, proto.StateUnread},
		{proto.StateNeedsYou, done, proto.StateNeedsYou},
		{proto.StateWorking, done, proto.StateWorking},
	} {
		if got := workState(c.state, c.w); got != c.want {
			t.Errorf("workState(%s, %+v) = %s, want %s", c.state, c.w, got, c.want)
		}
	}
}

func listed(t *testing.T, s *Service, machine, sid string) proto.Session {
	t.Helper()
	sess, ok := s.decoratedSession(machine, sid)
	if !ok {
		t.Fatalf("no session %s", sid)
	}
	return sess
}

func workReport(on bool, ws map[string]proto.WorkState) *WorkReport {
	r := &WorkReport{On: on, Sessions: []assistantwire.SessionWork{}}
	for sid, w := range ws {
		r.Sessions = append(r.Sessions, assistantwire.SessionWork{MachineID: "mac", SessionID: sid, Work: w})
	}
	return r
}

// The lists show what the Agents business reported for a turn while the session is still stopped on it; what it
// shows while it works or asks on its own card; the triage off, nothing; a report it could not read changes nothing.
// A report that changes a machine's sessions has the hub told their list.
func TestWorkOnTheListsIsTheReport(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	working := proto.Session{SessionID: "s3", Status: proto.StatusWorking, UpdatedAt: turnEnd}
	ft.d.setSessions("mac", []proto.Session{stopped("s1", "合好了"), stopped("s2", "选哪个？"), working})
	done := proto.WorkState{Phase: proto.WorkDone, Line: "已合并 PR #12", TurnAt: turnEnd,
		Artifacts: []proto.WorkArtifact{{Kind: "pr", Ref: "https://github.com/example/app/pull/12"}}}
	ask := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, Line: "等你选方案", TurnAt: turnEnd, InteractionID: "work_1"}
	if sess := listed(t, s, "mac", "s1"); sess.Work != nil || sess.State != proto.StateUnread {
		t.Fatalf("before any report: %s %+v", sess.State, sess.Work)
	}
	before := len(ft.toldOf(assistantwire.EventFleetSessions))
	s.fleetTakeSnapshot(Snapshot{Work: workReport(true, map[string]proto.WorkState{"s1": done, "s2": ask, "s3": done})})
	if len(ft.toldOf(assistantwire.EventFleetSessions)) != before+1 {
		t.Fatal("the hub was not told the list the report changed")
	}
	sess := listed(t, s, "mac", "s1")
	if sess.State != proto.StateIdle || sess.Work == nil || sess.Work.Phase != proto.WorkDone {
		t.Fatalf("done: %s %+v", sess.State, sess.Work)
	}
	if got := workLogLine(sess); !strings.HasPrefix(got, "完成：已合并 PR #12") || !strings.Contains(got, "/pull/12") {
		t.Fatalf("log line %q", got)
	}
	if sess := listed(t, s, "mac", "s3"); sess.Work == nil || sess.Work.Phase != proto.WorkWorking {
		t.Fatalf("working: %+v", sess.Work)
	}
	if sess := listed(t, s, "mac", "s2"); sess.Work == nil || sess.Work.InteractionID != "" || sess.Work.Line != "等你选方案" {
		t.Fatalf("no card: %+v", sess.Work)
	}
	putPending(s, &pending{ix: proto.Interaction{ID: "work_1", MachineID: "mac", SessionID: "s2", Kind: proto.IxQuestion,
		ToolName: assistantwire.WorkCardTool}})
	putPending(s, &pending{ix: proto.Interaction{ID: "own", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "删掉这个分支吗？"}}}})
	if sess := listed(t, s, "mac", "s2"); sess.State != proto.StateNeedsYou || sess.Work.InteractionID != "work_1" {
		t.Fatalf("the card: %s %+v", sess.State, sess.Work)
	}
	if sess := listed(t, s, "mac", "s1"); sess.Work.Phase != proto.WorkBlocked || sess.Work.Line != "删掉这个分支吗？" {
		t.Fatalf("the agent's own question first: %+v", sess.Work)
	}
	s.finishInteraction("own")

	s1 := stopped("s1", "又改了一点")
	s1.UpdatedAt = turnEnd.Add(time.Hour)
	ft.d.setSessions("mac", []proto.Session{s1, stopped("s2", "选哪个？"), working})
	if sess := listed(t, s, "mac", "s1"); sess.Work != nil || sess.State != proto.StateUnread {
		t.Fatalf("new turn: %s %+v", sess.State, sess.Work)
	}
	s.fleetTakeSnapshot(Snapshot{}) // a report it could not read keeps the last
	if sess := listed(t, s, "mac", "s2"); sess.Work == nil {
		t.Fatal("the last report went with one it could not read")
	}
	soon, late := time.Now().Add(time.Minute).UTC(), time.Now().Add(-10*time.Minute).UTC()
	wait := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient, Line: "模型服务暂时出错，自动重试", Wait: "503", TurnAt: turnEnd}
	soonW, lateW := wait, wait
	soonW.WakeAt, lateW.WakeAt = &soon, &late
	ft.d.setSessions("mac", []proto.Session{stopped("s4", "API Error: 503"), stopped("s5", "API Error: 503")})
	s.fleetTakeSnapshot(Snapshot{Work: workReport(true, map[string]proto.WorkState{"s4": soonW, "s5": lateW})})
	if sess := listed(t, s, "mac", "s4"); sess.State != proto.StateIdle || sess.Work.WakeAt == nil {
		t.Fatalf("a wake due soon: %s %+v", sess.State, sess.Work)
	}
	if sess := listed(t, s, "mac", "s5"); sess.State != proto.StateUnread || sess.Work == nil || sess.Work.WakeAt != nil {
		t.Fatalf("a wake long overdue: %s %+v", sess.State, sess.Work)
	}
	s.fleetTakeSnapshot(Snapshot{Work: &WorkReport{On: false}})
	if sess := listed(t, s, "mac", "s4"); sess.Work != nil {
		t.Fatalf("off: %+v", sess.Work)
	}
}

// A done turn nobody has seen is Just finished on the lists while it ended less than the window ago (#9751); not one
// already seen, nor one past the window, nor a turn the triage did not call done, nor any with the triage off. Seeing
// it takes it out, and so does a new turn.
func TestJustFinished(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	now := time.Now().UTC().Truncate(time.Second)
	recent, old := now.Add(-time.Hour), now.Add(-4*time.Hour)
	ended := func(id string, at time.Time) proto.Session {
		c := stopped(id, "合好了")
		c.UpdatedAt = at
		return c
	}
	done := func(at time.Time) proto.WorkState {
		return proto.WorkState{Phase: proto.WorkDone, Line: "已合并", TurnAt: at}
	}
	ft.d.setSessions("mac", []proto.Session{ended("fresh", recent), ended("old", old), ended("seen", recent), ended("asks", recent)})
	s.markSeen(context.Background(), "mac", "seen")
	s.fleetTakeSnapshot(Snapshot{Work: workReport(true, map[string]proto.WorkState{"fresh": done(recent), "old": done(old),
		"seen": done(recent), "asks": {Phase: proto.WorkBlocked, On: proto.WaitUser, Line: "选哪个", TurnAt: recent}})})
	for _, c := range []struct {
		sid   string
		state string
		just  bool
	}{
		{"fresh", proto.StateIdle, true},
		{"old", proto.StateIdle, false},
		{"seen", proto.StateIdle, false},
		{"asks", proto.StateUnread, false},
	} {
		sess := listed(t, s, "mac", c.sid)
		if sess.State != c.state || sess.Work == nil || sess.Work.JustFinished != c.just {
			t.Errorf("%s: %s %+v, want %s just finished %v", c.sid, sess.State, sess.Work, c.state, c.just)
		}
	}
	s.fleet.rep.mu.RLock()
	report := s.fleet.rep.work[[2]string{"mac", "fresh"}]
	s.fleet.rep.mu.RUnlock()
	if report.JustFinished {
		t.Error("the flag reached the report the lists are made from")
	}
	var told bool
	for _, sess := range ft.lastList("mac") {
		told = told || sess.SessionID == "fresh" && sess.Work != nil && sess.Work.JustFinished
	}
	if !told {
		t.Error("the hub was not told the Just finished row")
	}

	s.markSeen(context.Background(), "mac", "fresh")
	if sess := listed(t, s, "mac", "fresh"); sess.Work == nil || sess.Work.JustFinished {
		t.Fatalf("seen: %+v", sess.Work)
	}

	ft.d.setSessions("mac", []proto.Session{ended("next", recent)})
	s.fleetTakeSnapshot(Snapshot{Work: workReport(true, map[string]proto.WorkState{"next": done(recent)})})
	if sess := listed(t, s, "mac", "next"); sess.Work == nil || !sess.Work.JustFinished {
		t.Fatalf("before the next turn: %+v", sess.Work)
	}
	working := ended("next", now)
	working.Status = proto.StatusWorking
	ft.d.setSessions("mac", []proto.Session{working})
	if sess := listed(t, s, "mac", "next"); sess.Work == nil || sess.Work.JustFinished {
		t.Fatalf("a new turn: %+v", sess.Work)
	}

	ft.d.setSessions("mac", []proto.Session{ended("next", recent)})
	s.fleetTakeSnapshot(Snapshot{Work: &WorkReport{On: false}})
	if sess := listed(t, s, "mac", "next"); sess.Work != nil || sess.State != proto.StateUnread {
		t.Fatalf("the triage off: %s %+v", sess.State, sess.Work)
	}
}

// Nothing else tells a list again when only time passes: once the first Just finished row's window ends, the manager
// tells the hub the machine's list without it, and notes the next one (#9751).
func TestJustFinishedRunsOut(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	now := time.Now().UTC().Truncate(time.Second)
	first, second := now.Add(-2*time.Hour), now.Add(-30*time.Minute)
	ended := func(id string, at time.Time) proto.Session {
		c := stopped(id, "合好了")
		c.UpdatedAt = at
		return c
	}
	ft.d.setSessions("mac", []proto.Session{ended("a", first), ended("b", second)})
	s.fleetTakeSnapshot(Snapshot{Work: workReport(true, map[string]proto.WorkState{
		"a": {Phase: proto.WorkDone, TurnAt: first}, "b": {Phase: proto.WorkDone, TurnAt: second}})})
	justFinished := func() map[string]bool {
		out := map[string]bool{}
		for _, sess := range ft.lastList("mac") {
			out[sess.SessionID] = sess.Work != nil && sess.Work.JustFinished
		}
		return out
	}
	if got := justFinished(); !got["a"] || !got["b"] {
		t.Fatalf("told before: %v", got)
	}
	s.fleet.shownMu.Lock()
	due := s.fleet.finishedDue["mac"]
	s.fleet.shownMu.Unlock()
	if !due.Equal(first.Add(3 * time.Hour)) {
		t.Fatalf("due %v, want the first row's end and three hours", due)
	}

	before := len(ft.toldOf(assistantwire.EventFleetSessions))
	s.republishFinished(due.Add(-time.Second))
	if len(ft.toldOf(assistantwire.EventFleetSessions)) != before {
		t.Fatal("told again before the window ended")
	}
	// The window ends for "a" (an hour-long window stands in for three hours passing).
	ft.f.mu.Lock()
	ft.f.hello.Agents.Triage.JustFinishedHours = 1
	ft.f.mu.Unlock()
	s.republishFinished(due)
	if got := justFinished(); got["a"] || !got["b"] {
		t.Fatalf("told after a's window: %v", got)
	}
	s.fleet.shownMu.Lock()
	next := s.fleet.finishedDue["mac"]
	s.fleet.shownMu.Unlock()
	if !next.Equal(second.Add(time.Hour)) {
		t.Fatalf("next due %v, want b's end and the window", next)
	}
}

func TestFinishedDue(t *testing.T) {
	at := func(id string, end time.Time, just bool) proto.Session {
		return proto.Session{SessionID: id, UpdatedAt: end.Add(time.Second), Work: &proto.WorkState{Phase: proto.WorkDone, TurnAt: end, JustFinished: just}}
	}
	early, late := turnEnd, turnEnd.Add(time.Hour)
	if due := finishedDue([]proto.Session{at("a", late, true), at("b", early, false), {SessionID: "c"}}, time.Hour); !due.Equal(late.Add(time.Hour)) {
		t.Fatalf("due %v: the Just finished row's end and the window", due)
	}
	if due := finishedDue([]proto.Session{at("a", late, true), at("b", early, true)}, time.Hour); !due.Equal(early.Add(time.Hour)) {
		t.Fatalf("due %v: the first to run out", due)
	}
	if due := finishedDue([]proto.Session{at("b", early, false)}, time.Hour); !due.IsZero() {
		t.Fatalf("due %v with none", due)
	}
}

// The assistant's own cards (its snapshot's interactions) are shown like any card; one that left the snapshot is
// withdrawn and nothing is said to its Action.
func TestWithdrawnCardSaysNothing(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	card := proto.Interaction{ID: "work_z", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion, ToolName: assistantwire.WorkCardTool,
		Questions: []proto.Question{{Question: "A？", Options: []proto.QuestionOption{{Label: "A"}, {Label: "B"}}}},
		ExpiresAt: time.Now().Add(time.Hour)}
	s.askOnPhone([]proto.Interaction{card})
	ft.waitPending(1)
	s.askOnPhone(nil)
	ft.waitPending(0)
	time.Sleep(100 * time.Millisecond)
	if s.answerKept(card.ID) {
		t.Fatal("a dismiss was kept for the Action")
	}
}

// The snapshot tells the assistant what stopped agents wait for, and what the finished ones concluded: the session
// lists as the manager decorates them with the Agents business's report (moved from the hub's golden test).
func TestGoldenAssistantSnapshotWork(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ft.f.devices = []Device{{ID: "server", Name: "服务器", Online: true, Reachable: true}, {ID: "laptop", Name: "笔记本"}, {ID: "desk", Name: "台式机"}}
	ft.d.add("server", "服务器")
	setSettings(t, s, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
	at := time.Now().Add(-10 * time.Minute).UTC()
	ft.d.setSessions("server", []proto.Session{
		{SessionID: "aaaaaaaa-1", Cwd: "/code/demo", Title: "修登录", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "bbbbbbbb-2", Cwd: "/code/demo", Title: "等部署", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "cccccccc-3", Cwd: "/code/demo", Title: "选方案", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "dddddddd-4", Cwd: "/code/demo", Title: "没分诊", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "eeeeeeee-5", Cwd: "/code/demo", Title: "在问", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "ffffffff-6", Cwd: "/code/demo", Title: "屏蔽了", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "99999999-7", Cwd: "/code/demo", Title: "看过没回", Status: proto.StatusIdle, UpdatedAt: at, Asks: "要推到 beta 吗？"},
	})
	setMutes(s, Pins{Sessions: []string{"server/ffffffff-6"}})
	for _, sid := range []string{"eeeeeeee-5", "ffffffff-6"} {
		putPending(s, &pending{ix: proto.Interaction{ID: "own-" + sid, MachineID: "server", SessionID: sid, Kind: proto.IxQuestion,
			Questions: []proto.Question{{Question: "删掉这个分支吗？"}}}})
	}
	wake := time.Now().Add(5 * time.Minute).UTC()
	var report []assistantwire.SessionWork
	for sid, w := range map[string]proto.WorkState{
		"aaaaaaaa-1": {Phase: proto.WorkDone, Line: "已合并 PR #12"},
		"bbbbbbbb-2": {Phase: proto.WorkBlocked, On: proto.WaitExternal, Line: "等部署跑完再跑线上测试", Wait: "部署跑完", WakeAt: &wake},
		"cccccccc-3": {Phase: proto.WorkBlocked, On: proto.WaitUser, Line: "等你选 D2 / D4"},
	} {
		w.TurnAt = at
		report = append(report, assistantwire.SessionWork{MachineID: "server", SessionID: sid, Work: w})
	}
	s.fleetTakeSnapshot(Snapshot{Work: &WorkReport{On: true, Sessions: report}})
	ft.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{PublicURL: "http://hub.example.com:18660"}, nil
	})
	res := s.Context(context.Background(), ContextParams{SessionID: "side-session"})
	head := regexp.MustCompile(`^- 现在：\d{4}-\d\d-\d\d 周[日一二三四五六] \d\d:\d\d（([^）]+)）\n`)
	m := head.FindStringSubmatch(res.Text)
	if m == nil {
		t.Fatalf("snapshot header: %q", res.Text)
	}
	golden(t, "assistant_snapshot.work", "- 现在：YYYY-MM-DD 周X HH:MM（"+m[1]+"）\n"+res.Text[len(m[0]):])
}

// A plan's work line says what the plan is, and a card of several questions says more follow (#4331): the user tells
// one agent's plan from another's and knows a question is not the only one before opening the card.
func TestInteractionLineSaysWhatIsAsked(t *testing.T) {
	plan := proto.Interaction{Kind: proto.IxPlan, Plan: "# Move the cache to Postgres\n\n1. add a table"}
	long := strings.Repeat("问", 80)
	two := proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "部署到哪？"}, {Question: "要回滚吗？"}}}
	three := proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: long}, {Question: "b"}, {Question: "c"}}}
	for _, c := range []struct {
		l    i18n.Lang
		ix   proto.Interaction
		want string
	}{
		{i18n.ZhHans, plan, "计划等你审批：Move the cache to Postgres"},
		{i18n.En, plan, "Plan awaiting approval: Move the cache to Postgres"},
		{i18n.ZhHans, two, "部署到哪？（还有 1 个问题）"},
		{i18n.En, two, "部署到哪？ (1 more question)"},
		{i18n.Ja, two, "部署到哪？（ほかに1問）"},
		{i18n.En, proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: " "}, {Question: "b"}, {Question: "c"}}}, "Has a question (2 more questions)"},
		{i18n.ZhHans, proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "部署到哪？"}}}, "部署到哪？"},
		{i18n.ZhHans, proto.Interaction{Kind: proto.IxPlan, Plan: " \n#\n"}, "计划等你审批"},
		// The plan's first line without its Markdown marks (#4387).
		{i18n.ZhHans, proto.Interaction{Kind: proto.IxPlan, Plan: "- [ ] **第一步**：改 `cache.go`\n- [ ] 第二步"}, "计划等你审批：第一步：改 cache.go"},
	} {
		if got := interactionLine(c.l, c.ix); got != c.want {
			t.Errorf("%q %s: %q, want %q", c.l, c.ix.Kind, got, c.want)
		}
	}
	// A long first question is what gets cut, never the count after it; the line is 60 runes and an ellipsis, as
	// every other work line.
	got := interactionLine(i18n.ZhHans, three)
	if !strings.HasSuffix(got, "…（还有 2 个问题）") || utf8.RuneCountInString(got) != 61 {
		t.Errorf("long question: %q (%d runes)", got, utf8.RuneCountInString(got))
	}
}

// The snapshot's "Needs you" is the agents in that state, as the Live Activity, the lists and the badges count them: a
// decision the triage put no card up for (cards off, muted, "Not now") stays "Unread" with what it waits for, and is not
// listed once seen (#4449).
func TestSnapshotNeedsYouIsTheState(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ft.f.devices = []Device{{ID: "mac", Name: "Mac", Online: true, Reachable: true}}
	ft.d.add("mac", "Mac")
	at := time.Now().Add(-10 * time.Minute).UTC()
	seen := proto.Session{SessionID: "bbbbbbbb-2", Cwd: "/code/demo", Title: "看过了", Status: proto.StatusIdle, UpdatedAt: at}
	ft.d.setSessions("mac", []proto.Session{
		{SessionID: "aaaaaaaa-1", Cwd: "/code/demo", Title: "选方案", Status: proto.StatusDone, UpdatedAt: at},
		seen,
		{SessionID: "cccccccc-3", Cwd: "/code/demo", Title: "在问", Status: proto.StatusDone, UpdatedAt: at},
	})
	user := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, Line: "你说先不管", TurnAt: at}
	s.fleetTakeSnapshot(Snapshot{Work: workReport(true, map[string]proto.WorkState{"aaaaaaaa-1": user, "bbbbbbbb-2": user})})
	putPending(s, &pending{ix: proto.Interaction{ID: "own", MachineID: "mac", SessionID: "cccccccc-3", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "删掉这个分支吗？"}}}})

	needs, unread, awaiting, waiting, _, _, _ := s.snapshotAgents(context.Background(), ft.f.devices, "")
	if live := s.fleetSnapshot().NeedsYou; len(needs) != live || live != 1 || needs[0].Session != "cccccccc" {
		t.Fatalf("需要你: snapshot %+v, Live Activity %d; want the one asking", needs, live)
	}
	if len(unread) != 1 || unread[0].Session != "aaaaaaaa" || unread[0].Line != "你说先不管" {
		t.Fatalf("完成未看 %+v: want the cardless decision, with what it waits for", unread)
	}
	if len(waiting) != 0 {
		t.Fatalf("在等的 %+v: the hub waits out none of them", waiting)
	}
	// Seen with its decision still open (no card: cards off): asked and unanswered (#5096).
	if len(awaiting) != 1 || awaiting[0].Session != "bbbbbbbb" || awaiting[0].Line != "你说先不管" {
		t.Fatalf("看过还没回 %+v: want the seen one waiting on a decision", awaiting)
	}
}

// A muted agent (or one in a muted space) is not "Needs you", "Working" or "Unread" in the snapshot, as the Live Activity, the
// Agents badge and the inbox leave it out: the assistant would tell the user an agent waits for them that they muted.
// It is listed apart, for when they ask about it (#4562).
func TestSnapshotLeavesMutedAgentsOut(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ft.f.devices = []Device{{ID: "mac", Name: "Mac", Online: true, Reachable: true}}
	at := time.Now().Add(-10 * time.Minute).UTC()
	ft.d.setSessions("mac", []proto.Session{
		{SessionID: "aaaaaaaa-1", Cwd: "/code/demo", Title: "在问", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "bbbbbbbb-2", Cwd: "/code/demo", Title: "屏蔽了在问", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "cccccccc-3", Cwd: "/code/side", SpaceName: "side", Title: "屏蔽的空间在跑", Status: proto.StatusWorking, UpdatedAt: at},
		{SessionID: "dddddddd-4", Cwd: "/code/demo", Title: "屏蔽了做完", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "eeeeeeee-5", Cwd: "/code/side", SpaceName: "side", Title: "空间里开着通知", Status: proto.StatusWorking, UpdatedAt: at},
	})
	setMutes(s, Pins{Spaces: []string{"mac/side"}, Sessions: []string{"mac/bbbbbbbb-2", "mac/dddddddd-4"},
		Allowed: []string{"mac/eeeeeeee-5"}})
	for _, sid := range []string{"aaaaaaaa-1", "bbbbbbbb-2"} {
		putPending(s, &pending{ix: proto.Interaction{ID: "ix-" + sid, MachineID: "mac", SessionID: sid, Kind: proto.IxQuestion,
			Questions: []proto.Question{{Question: "删掉这个分支吗？"}}}})
	}

	needs, unread, _, _, _, muted, working := s.snapshotAgents(context.Background(), ft.f.devices, "")
	live := s.fleetSnapshot()
	if len(needs) != live.NeedsYou || live.NeedsYou != 1 || needs[0].Session != "aaaaaaaa" {
		t.Fatalf("需要你: snapshot %+v, Live Activity %d; want only the agent not muted", needs, live.NeedsYou)
	}
	if len(working) != live.Working || live.Working != 1 || !strings.HasSuffix(working[0].Label, "空间里开着通知") {
		t.Fatalf("工作中: snapshot %v, Live Activity %d; want the one turned back on in the muted space", working, live.Working)
	}
	if len(unread) != live.Unread || live.Unread != 0 {
		t.Fatalf("完成未看: snapshot %+v, Live Activity %d; want none", unread, live.Unread)
	}
	var got []string
	for _, a := range muted {
		got = append(got, a.Session)
	}
	if strings.Join(got, ",") != "bbbbbbbb,cccccccc,dddddddd" {
		t.Fatalf("已屏蔽 %v: want the muted ones apart", got)
	}
}

// setMutes is the user's mutes as the hub told them (GET /api/v1/mutes).
func setMutes(s *Service, p Pins) {
	s.fleet.mutesMu.Lock()
	s.fleet.mutes = &p
	s.fleet.mutesMu.Unlock()
}

func TestWorkProjectionRejectsStoredUnsupportedModelWait(t *testing.T) {
	at := time.Now().UTC()
	row := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: at,
		AsksRead: true, ReadOnlyReason: "Read-only history"}
	for _, tc := range []struct {
		name      string
		edit      func(*proto.WorkState)
		wantReply bool
	}{
		{name: "only a model summary"},
		{name: "verified question", edit: func(w *proto.WorkState) {
			w.Questions = []proto.Question{{Question: "Which file should I use?"}}
		}, wantReply: true},
		{name: "authorization link", edit: func(w *proto.WorkState) {
			w.Link = &proto.WorkLink{URL: "https://example.com/authorize"}
		}, wantReply: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, By: "model", TurnAt: at, Line: "Waiting for approval"}
			if tc.edit != nil {
				tc.edit(&w)
			}
			c := row
			c.Work = workOf(i18n.Default, c, &w, nil)
			if got := awaitsReply(c); got != tc.wantReply || (c.Work != nil) != tc.wantReply {
				t.Fatalf("awaits reply = %v, work = %+v, want reply %v", got, c.Work, tc.wantReply)
			}
			if w.Phase != proto.WorkBlocked {
				t.Fatal("projection rewrote the stored work")
			}
		})
	}
}
