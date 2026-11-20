package assistant

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestGoldenAgentCloseoutBrief(t *testing.T) {
	brief := func(b prompts.AgentCloseoutBrief) string {
		b.Form = "<closeout …>"
		return prompts.RenderAgentCloseoutBrief(b)
	}
	golden(t, "agent_closeout_brief", brief(prompts.AgentCloseoutBrief{Language: "English"}))
	golden(t, "agent_closeout_brief.after_delivery", brief(prompts.AgentCloseoutBrief{AfterDelivery: true}))
	golden(t, "agent_closeout_brief.session", brief(prompts.AgentCloseoutBrief{Session: true}))
	// The delivery standard when the form follows it; without, it is pinned by the hub package's task_delivery golden.
	golden(t, "task_delivery.closeout", prompts.RenderTaskDelivery(true))
}

// closeoutOn turns the triage and its closeout switch on.
func closeoutOn(t *testing.T, s *Service) {
	t.Helper()
	if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true, Closeout: true}); err != nil {
		t.Fatal(err)
	}
}

// A task's agent is told the closeout's form with its prompt only while the switches are on; with the delivery
// standard on too, the one block also carries delivery disposition and responsibility.
func TestTaskPromptCarriesTheCloseoutForm(t *testing.T) {
	s, f := newTaskService(t)
	ctx := context.Background()
	start := func(task PlanTask) string {
		t.Helper()
		f.starts = nil
		if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
			t.Fatal(err)
		}
		p, _ := f.starts[0]["prompt"].(string)
		return p
	}
	edit := PlanTask{Title: "修", Prompt: "修好登录", Machine: "box", Cwd: "/code/app"}
	read := PlanTask{Title: "查", Prompt: "查一下", Machine: "box", Cwd: "/code/app", Readonly: true}

	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if p := start(edit); !strings.HasPrefix(p, "修好登录") || !strings.Contains(p, "file: <这台电脑上的绝对路径>") || strings.Contains(p, "收尾记录") {
		t.Fatalf("the closeout switch is off, yet the prompt is %q", p)
	}
	if err := s.putTriageSwitches(ctx, TriageSwitches{Closeout: true}); err != nil {
		t.Fatal(err)
	}
	if p := start(edit); !strings.HasPrefix(p, "修好登录") || !strings.Contains(p, "file: <这台电脑上的绝对路径>") || strings.Contains(p, "收尾记录") {
		t.Fatalf("the triage is off, yet the prompt is %q", p)
	}

	closeoutOn(t, s)
	p := start(edit)
	if !strings.HasPrefix(p, "修好登录") || !strings.Contains(p, "file: <这台电脑上的绝对路径>") || !strings.Contains(p, "收尾记录") || !strings.Contains(p, `<closeout status="done|not_done|blocked"`) ||
		strings.Contains(p, "交付：") {
		t.Fatalf("closeout only:\n%s", p)
	}

	deliveryOn(t, s)
	p = start(edit)
	if strings.Contains(p, "交付：<已上线并实测") || strings.Count(p, "<closeout status=") != 1 || !strings.Contains(p, "delivery:") || !strings.Contains(p, "unblock:") || !strings.Contains(p, "不再另写") {
		t.Fatalf("both switches must request one structured record:\n%s", p)
	}
	if p := start(read); strings.Contains(p, "交付：") || !strings.Contains(p, "收尾记录") || strings.Contains(p, "那一行的后面") {
		t.Fatalf("a read-only task has no delivery line to put the block after:\n%s", p)
	}
}

// agent_start and dispatch_start hand the form over too, once, after the prompt as written.
func TestStartedAgentsCarryTheCloseoutForm(t *testing.T) {
	s, f := newTaskService(t)
	closeoutOn(t, s)
	if _, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"修登录"}`)); err != nil {
		t.Fatal(err)
	}
	if len(f.starts) != 1 {
		t.Fatalf("starts %v", f.starts)
	}
	if p, _ := f.starts[0]["prompt"].(string); !strings.Contains(p, "修登录\n\n收尾记录") || strings.Count(p, "<closeout status=") != 1 {
		t.Fatalf("agent_start:\n%s", p)
	}

	d, df, _ := newDispatchService(t, true)
	closeoutOn(t, d)
	if r := dispatchCall(d, "mac", "caller-0001", "dispatch_start", `{"machine":"box","cwd":"/code/app","prompt":"run the Linux tests"}`); r.IsError {
		t.Fatalf("dispatch: %q", resultText(r))
	}
	if p, _ := df.starts[0]["prompt"].(string); !strings.Contains(p, "run the Linux tests\n\n收尾记录") || strings.Count(p, "<closeout status=") != 1 {
		t.Fatalf("dispatch_start:\n%s", p)
	}
}

// The delivery line is read as before when a closeout block follows it, and wins over what the block says.
func TestDeliveryLineBeforeACloseout(t *testing.T) {
	reply := "做完了，PR 开了。\n\n交付：停在 PR；卡点：等用户批准合并；谁来解：用户\n" +
		"<closeout status=\"done\" merged=\"no\" deployed=\"no\">\ndid: 修了重试\nartifact: pr https://github.com/example/app/pull/12\n" +
		"missing: 合并要用户点头\n</closeout>"
	d, ok := deliveryMark(reply)
	if !ok || d.State != deliveryPR || d.Gate != "等用户批准合并" || d.Unblock != unblockUser {
		t.Fatalf("delivery %+v %v", d, ok)
	}
	// The block's own lines are not taken for a delivery line.
	if d, ok := deliveryMark("好了。\n交付：已上线并实测；卡点：无；谁来解：无\n<closeout status=\"done\" merged=\"yes\" deployed=\"no\">\ndid: x\n</closeout>"); !ok || d.State != deliveryLive {
		t.Fatalf("the line did not win: %+v %v", d, ok)
	}
}

// A starting session is told the form only under both switches, and never a review's reviewer: by its worktree
// while its start has not answered yet, by its session after.
func TestCloseoutStart(t *testing.T) {
	s, _ := workService(t)
	ctx := context.Background()
	ask := func(sid, cwd string) string {
		t.Helper()
		return s.closeoutStart(ctx, "box", assistantwire.CloseoutBriefRequest{SessionID: sid, Cwd: cwd}).Context
	}
	if c := ask("s1", "/code/app"); c != "" {
		t.Fatalf("told with the switch off: %q", c)
	}
	if err := s.putTriageSwitches(ctx, TriageSwitches{Closeout: true}); err != nil {
		t.Fatal(err)
	}
	if c := ask("s1", "/code/app"); c != "" {
		t.Fatalf("told with the triage off: %q", c)
	}
	closeoutOn(t, s)
	c := ask("s1", "/code/app")
	if !strings.Contains(c, `<closeout status="done|not_done|blocked"`) || !strings.HasPrefix(c, "（Persona 给这个会话的约定") {
		t.Fatalf("context: %q", c)
	}
	if c := ask("", "/code/app"); c != "" {
		t.Fatalf("told a session with no id: %q", c)
	}

	s.todos.mu.Lock()
	s.todos.todos["todo_r"] = &Todo{ID: "todo_r", Title: "x", Supervision: &Supervision{AutoReview: &AutoReview{ID: "rv_1",
		State: autoRunning, MachineID: "box", Worktree: "/tmp/review-1"}},
		Agents: []TodoAgent{{MachineID: "box", SessionID: "rev1", Source: sourceReview}}}
	s.todos.mu.Unlock()
	if c := ask("new", "/tmp/review-1"); c != "" {
		t.Fatalf("a reviewer was told, by its worktree: %q", c)
	}
	if c := ask("rev1", "/elsewhere"); c != "" {
		t.Fatalf("a reviewer was told, by its session: %q", c)
	}
	if c := s.closeoutStart(ctx, "other", assistantwire.CloseoutBriefRequest{SessionID: "s3", Cwd: "/tmp/review-1"}).Context; c == "" {
		t.Fatal("another machine's directory of the same name was taken for the review's")
	}
}

// A turn nobody can call finished is shown: no phase (the row stays Unread), a line that says so, the closeout kept.
func TestCloseoutNotDoneIsShown(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"done","line":"都做完了"}`
	f.setSessions("box", stopped("s1", "先到这。\n<closeout status=\"not_done\">\ndid: 改了重试\nmissing: 还差线上验证\n</closeout>"))
	triage(t, s, "s1")
	w, shown := reportOf(t, s, "s1")
	if !shown || w.Phase != "" || w.Closeout == nil || w.Closeout.Status != proto.CloseoutNotDone {
		t.Fatalf("report %+v %v", w, shown)
	}
	if want := i18n.T(s.lang(context.Background()), "assistant-agents_triage.closeout_not_finished_text", i18n.Args{"text": "还差线上验证"}); w.Line != want {
		t.Fatalf("line %q, want %q", w.Line, want)
	}
	// In the lists: the work with its line, and still Unread.
	sess := stopped("s1", "x")
	if got := workOf(i18n.En, sess, &w, nil); got == nil || got.Line != w.Line || workState(proto.StateUnread, got) != proto.StateUnread {
		t.Fatalf("listed as %+v", got)
	}

	// The model cannot say, the closeout names nothing missing: what it did, else the bare words.
	f.triage = `{"phase":"unsure"}`
	f.setSessions("box", stopped("s2", "停一下。\n<closeout status=\"not_done\">\ndid: 改了一半的重试\n</closeout>"))
	triage(t, s, "s2")
	if w, shown := reportOf(t, s, "s2"); !shown || !strings.HasSuffix(w.Line, "改了一半的重试") || w.Phase != "" {
		t.Fatalf("unsure: %+v %v", w, shown)
	}
	// A turn without a closeout that the model cannot read is still not shown.
	f.setSessions("box", stopped("s3", "嗯。"))
	triage(t, s, "s3")
	if _, shown := reportOf(t, s, "s3"); shown {
		t.Fatal("an unsure turn without a closeout was shown")
	}
}

func TestCloseoutLine(t *testing.T) {
	for name, tc := range map[string]struct {
		c    proto.Closeout
		want string
	}{
		"missing":      {proto.Closeout{Status: proto.CloseoutNotDone, Missing: "the staging key", Did: []string{"x"}}, "Not finished: the staging key"},
		"waiting":      {proto.Closeout{Status: proto.CloseoutBlocked, Waiting: "review"}, "Not finished: review"},
		"did":          {proto.Closeout{Status: proto.CloseoutNotDone, Did: []string{"moved the cache", "y"}}, "Not finished: moved the cache"},
		"nothing":      {proto.Closeout{Status: proto.CloseoutNotDone}, "Not finished"},
		"not merged":   {proto.Closeout{Status: proto.CloseoutDone, Merged: "yes", Deployed: "yes", Checked: &proto.CloseoutChecked{Merged: "no", Deployed: "no"}}, "Says merged, but the pull request is not merged"},
		"not deployed": {proto.Closeout{Status: proto.CloseoutDone, Merged: "yes", Deployed: "yes", Checked: &proto.CloseoutChecked{Merged: "yes", Deployed: "no"}}, "Says deployed, but the running version does not have it"},
		"confirmed":    {proto.Closeout{Status: proto.CloseoutNotDone, Merged: "yes", Missing: "a deploy", Checked: &proto.CloseoutChecked{Merged: "yes"}}, "Not finished: a deploy"},
	} {
		if got := closeoutLine(tc.c, i18n.En); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	long := closeoutLine(proto.Closeout{Status: proto.CloseoutNotDone, Missing: strings.Repeat("字", 200)}, i18n.ZhHans)
	if n := len([]rune(long)); n > 61 { // 60 and the ellipsis, as the other work lines are cut
		t.Errorf("a long line has %d characters", n)
	}
}

// A claim the hub disproved is shown too, with the claim that is not so as its line.
func TestCloseoutDisprovedIsShown(t *testing.T) {
	s, f, _, hub, forge := deployService(t)
	s.work.switches = &TriageSwitches{Enabled: true}
	f.sessions["box"] = nil
	l := s.lang(context.Background())

	hub.deploy(oldHub) // merged, the running hub does not contain it
	f.setSessions("box", stopped("s1", mergedCloseout))
	triage(t, s, "s1")
	if w, shown := reportOf(t, s, "s1"); !shown || w.Phase != "" || w.Line != i18n.T(l, "assistant-agents_triage.closeout_not_deployed") {
		t.Fatalf("deploy: %+v %v", w, shown)
	}
	forge.merged = false
	s.deploy.cache = nil
	f.setSessions("box", stopped("s2", mergedCloseout))
	triage(t, s, "s2")
	if w, shown := reportOf(t, s, "s2"); !shown || w.Phase != "" || w.Line != i18n.T(l, "assistant-agents_triage.closeout_not_merged") {
		t.Fatalf("merge: %+v %v", w, shown)
	}
}

// Codex has no stop to hold, but a closeout it ends its final reply with is read like any agent's: the triage reads
// the last reply of the turn, whatever runtime wrote it.
func TestCodexCloseoutIsRead(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"user","line":"the model was asked"}`
	c := stopped("cx1", "")
	c.Agent = "codex"
	f.setSessions("box", c)
	f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "fix the retry"},
		proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Fixed.\n\n<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\n" +
			"did: The retry backs off instead of spinning\nverified: go test ./retry, 14 passed\n</closeout>"})
	triage(t, s, "cx1")
	w, ok := reportOf(t, s, "cx1")
	if !ok || w.Phase != proto.WorkDone || w.By != "rule" || w.Line != "The retry backs off instead of spinning" || w.Closeout == nil {
		t.Fatalf("work %+v", w)
	}
	if len(f.triageAsked) != 0 {
		t.Fatalf("the model was asked %d times", len(f.triageAsked))
	}
}

func TestSnapshotCloseout(t *testing.T) {
	for name, tc := range map[string]struct {
		c    *proto.Closeout
		want *prompts.SnapshotAgentCloseout
	}{
		"none":         {nil, nil},
		"says neither": {&proto.Closeout{Status: "done"}, nil},
		"claims":       {&proto.Closeout{Merged: "yes", Deployed: "no"}, &prompts.SnapshotAgentCloseout{Merged: "yes", Deployed: "no"}},
		"confirmed":    {&proto.Closeout{Merged: "yes", Checked: &proto.CloseoutChecked{Merged: "yes"}}, &prompts.SnapshotAgentCloseout{Merged: "yes"}},
		"differs": {&proto.Closeout{Merged: "yes", Deployed: "yes", Checked: &proto.CloseoutChecked{Merged: "yes", Deployed: "no"}},
			&prompts.SnapshotAgentCloseout{Merged: "yes", Deployed: "yes", FoundDeployed: "no"}},
		"found, unsaid": {&proto.Closeout{Checked: &proto.CloseoutChecked{Merged: "yes"}}, &prompts.SnapshotAgentCloseout{FoundMerged: "yes"}},
	} {
		got := snapshotCloseout(&proto.WorkState{Closeout: tc.c})
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}

// In the live snapshot an agent's line ends with what its closeout claims of merging and deploying, and with the
// hub's finding where that differs; a turn that is not finished is under "unread" with its line.
func TestGoldenAssistantSnapshotCloseout(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ft.f.devices = []Device{{ID: "server", Name: "服务器", Online: true, Reachable: true}}
	ft.d.add("server", "服务器")
	setSettings(t, s, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
	at := time.Now().Add(-10 * time.Minute).UTC()
	ft.d.setSessions("server", []proto.Session{
		{SessionID: "aaaaaaaa-1", Cwd: "/code/demo", Title: "修登录", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "bbbbbbbb-2", Cwd: "/code/demo", Title: "改重试", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "cccccccc-3", Cwd: "/code/demo", Title: "发版", Status: proto.StatusDone, UpdatedAt: at},
		{SessionID: "dddddddd-4", Cwd: "/code/demo", Title: "写文档", Status: proto.StatusDone, UpdatedAt: at},
	})
	var report []assistantwire.SessionWork
	for sid, w := range map[string]proto.WorkState{
		"aaaaaaaa-1": {Phase: proto.WorkDone, Line: "登录不再丢会话", Closeout: &proto.Closeout{Status: proto.CloseoutDone,
			Merged: proto.CloseoutYes, Deployed: proto.CloseoutNo, Checked: &proto.CloseoutChecked{Merged: proto.CloseoutYes, Deployed: proto.CloseoutNo}}},
		"bbbbbbbb-2": {Line: "没做完：还差线上验证", Closeout: &proto.Closeout{Status: proto.CloseoutNotDone, Merged: proto.CloseoutNo,
			Missing: "还差线上验证"}},
		"cccccccc-3": {Line: "说已部署，但线上不是这次的改动", Closeout: &proto.Closeout{Status: proto.CloseoutDone, Merged: proto.CloseoutYes,
			Deployed: proto.CloseoutYes, Checked: &proto.CloseoutChecked{Merged: proto.CloseoutYes, Deployed: proto.CloseoutNo}}},
		"dddddddd-4": {Phase: proto.WorkDone, Line: "文档写好了", Closeout: &proto.Closeout{Status: proto.CloseoutDone}},
	} {
		w.TurnAt = at
		report = append(report, assistantwire.SessionWork{MachineID: "server", SessionID: sid, Work: w})
	}
	s.fleetTakeSnapshot(Snapshot{Work: &WorkReport{On: true, Sessions: report}})
	res := s.Context(context.Background(), ContextParams{SessionID: "side-session"})
	head := regexp.MustCompile(`^- 现在：\d{4}-\d\d-\d\d 周[日一二三四五六] \d\d:\d\d（([^）]+)）\n`)
	m := head.FindStringSubmatch(res.Text)
	if m == nil {
		t.Fatalf("snapshot header: %q", res.Text)
	}
	golden(t, "assistant_snapshot.closeout", "- 现在：YYYY-MM-DD 周X HH:MM（"+m[1]+"）\n"+res.Text[len(m[0]):])
}
