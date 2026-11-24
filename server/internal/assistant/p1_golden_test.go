package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The prompts of P1 (#3609): the main conversation's directions and the leader's own part of its context, a leader's
// stuck task in the main conversation's supervision message, and one line per direction in the briefings' facts.
// All names are placeholders.

func TestGoldenP1Context(t *testing.T) {
	golden(t, "context.main_directions", prompts.RenderAssistantContext(prompts.AssistantContext{Main: true, Owner: "Example user",
		Snapshot: "（快照）", Directions: []prompts.ContextDirection{{ID: "lead_a", Name: "Persona 产品"}, {ID: "lead_b", Name: "基建"}}}))
	golden(t, "context.leader", prompts.RenderAssistantContext(prompts.AssistantContext{Leader: true, Owner: "Example user",
		Snapshot: "（快照）", LeaderName: "基建"}))
	golden(t, "context.main", prompts.RenderAssistantContext(prompts.AssistantContext{Main: true, Owner: "Example user", Snapshot: "（快照）"}))
}

func TestGoldenP1SuperviseLeaderStuck(t *testing.T) {
	golden(t, "supervise.leader_stuck", prompts.RenderSupervise(prompts.Supervise{LeaderStuck: true, Entries: []prompts.SuperviseEntry{{
		ID: "todo_1", Title: "平板布局", Kind: sigLeaderStuck, Detail: "基建 · 卡住了，停着没做完：平板布局"}}}))
}

func TestGoldenP1BriefLeaders(t *testing.T) {
	golden(t, "routine_facts.leaders", prompts.RenderBriefFacts(prompts.BriefFacts{Hours: 12, Leaders: []prompts.BriefLeader{
		{Name: "Persona 产品", Open: 3, Waiting: 1, Stuck: 1, Reached: "侧栏分组上线并实测", Doing: "平板布局，卡在模拟器排队", NeedsYou: "确认平板要不要双栏"},
		{Name: "基建", Open: 1}}}))
}

// A leader's task past its inactivity threshold: the extensions get leader.report stuck, and the main conversation one supervision
// entry naming the direction (no push).
func TestLeaderStuckTellsTheMainConversation(t *testing.T) {
	st := &memStore{}
	e := directionEnv(t, st)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv_main" })
	e.title("conv_lead_a", "1110 | 探索 | x")
	e.leaders("conv_lead_a")
	e.syncLeaders()
	if err := e.s.setDirectionName(context.Background(), "conv_lead_a", "基建", directionByUser); err != nil {
		t.Fatal(err)
	}
	id := e.leaderTask("平板布局", "conv_lead_a")
	e.worked(id)
	later := time.Now().Add(3 * 24 * time.Hour)
	var got []assistantwire.LeaderReportEvent
	for _, r := range e.reports(later, true) {
		if r.Kind == assistantwire.LeaderStuck {
			got = append(got, r)
		}
	}
	if len(got) != 1 || got[0].Todo != id {
		t.Fatalf("stuck reports: %+v", got)
	}
	b := &e.s.supervise
	waitFor(t, "the main conversation's stuck entry", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, g := range b.pending {
			if g.Kind == sigLeaderStuck && g.Todo == id && strings.HasPrefix(g.Detail, "基建 · ") {
				return true
			}
		}
		return false
	})
	if to := e.s.digestTo(context.Background(), e.s.settings(context.Background()), superviseSignal{Todo: id, Kind: sigLeaderStuck}); to != "conv_main" {
		t.Fatalf("goes to %q", to)
	}
}

// The main conversation's context lists the current directions by name (by id when one has none at all); a leader's
// names its own direction; another conversation gets neither.
func TestContextNamesTheDirections(t *testing.T) {
	e := directionEnv(t, &memStore{})
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv_main" })
	e.title("conv_lead_a", "1110 | 探索 | x")
	e.leaders("conv_lead_a", "conv_lead_b")
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv_main" })
	e.syncLeaders()
	if err := e.s.setDirectionName(context.Background(), "conv_lead_a", "基建", directionByUser); err != nil {
		t.Fatal(err)
	}
	main := e.s.Context(context.Background(), ContextParams{SessionID: "conv_main"}).Text
	for _, want := range []string{"- conv_lead_a：基建", "- conv_lead_b：conv_lead_b", "主管派的任务由该主管督办", "只接 needs_user 升级"} {
		if !strings.Contains(main, want) {
			t.Fatalf("the main context lacks %q:\n%s", want, main)
		}
	}
	if lead := e.s.Context(context.Background(), ContextParams{SessionID: "conv_lead_a"}).Text; !strings.Contains(lead, "方向「基建」") || strings.Contains(lead, "现在的方向") {
		t.Fatalf("the leader's context:\n%s", lead)
	}
	if other := e.s.Context(context.Background(), ContextParams{SessionID: "conv_other"}).Text; strings.Contains(other, "基建") {
		t.Fatalf("another conversation's context names directions:\n%s", other)
	}
}

// The briefing's line per direction: open tasks of each current leader, those waiting for the owner, those reported
// stuck in the last day (a task waiting on the owner counted once), the summary of the day; the main conversation's
// own tasks are not a direction.
func TestBriefLeaders(t *testing.T) {
	e := directionEnv(t, &memStore{})
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv_main" })
	e.leaders("conv_lead_a")
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv_main" })
	e.syncLeaders()
	ctx := context.Background()
	_ = e.leaderTask("A", "conv_lead_a")
	b := e.leaderTask("B", "conv_lead_a")
	c := e.leaderTask("C", "conv_lead_a")
	_ = e.leaderTask("Main's own", "conv_main")
	e.review(b, reviewNeedsUser, "?")
	now := time.Now()
	lb := &e.s.lreports
	lb.mu.Lock()
	if !e.s.leaderReportsLocked(ctx) {
		t.Fatal("reports book")
	}
	lb.told.Stuck[b], lb.told.Stuck[c] = now.Add(-time.Hour), now.Add(-time.Hour)
	lb.mu.Unlock()
	if _, err := e.s.leaderProgressReport(ctx, "conv_lead_a", leaderProgress{Reached: "侧栏分组上线", At: now}); err != nil {
		t.Fatal(err)
	}
	got := e.s.briefLeaders(ctx, now)
	if len(got) != 1 || got[0].Open != 3 || got[0].Waiting != 1 || got[0].Stuck != 1 || got[0].Reached != "侧栏分组上线" {
		t.Fatalf("lines: %+v", got)
	}
	if later := e.s.briefLeaders(ctx, now.Add(25*time.Hour)); later[0].Stuck != 0 || later[0].Reached != "" {
		t.Fatalf("a day later: %+v", later)
	}
}

// A stuck task is told to the main conversation once a day, not again within the day nor after a restart.
func TestLeaderStuckOncePerDay(t *testing.T) {
	st := &memStore{}
	e := directionEnv(t, st)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv_main" })
	e.leaders("conv_lead_a")
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv_main" })
	id := e.leaderTask("平板布局", "conv_lead_a")
	e.worked(id)
	later := time.Now().Add(3 * 24 * time.Hour)
	count := func(rs []assistantwire.LeaderReportEvent) int {
		n := 0
		for _, r := range rs {
			if r.Kind == assistantwire.LeaderStuck && r.Todo == id {
				n++
			}
		}
		return n
	}
	if n := count(e.reports(later, true)); n != 1 {
		t.Fatalf("first: %d", n)
	}
	if n := count(e.reports(later.Add(time.Hour), true)); n != 0 {
		t.Fatalf("within the day: %d", n)
	}
	e2 := directionEnv(t, st)
	setSettings(t, e2.s, func(v *Settings) { v.Main, v.Leaders = "conv_main", []string{"conv_lead_a"} })
	if n := count(e2.reports(later.Add(2*time.Hour), true)); n != 0 {
		t.Fatalf("after a restart: %d", n)
	}
}
