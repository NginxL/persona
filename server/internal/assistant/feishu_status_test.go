package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

func statusHub(t *testing.T, publicURL string) (*Service, *fakeHub, *[]map[string]any) {
	t.Helper()
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
			PublicURL: publicURL, Features: FeishuFeatures{Main: true, Agents: true, Cards: true}}, nil
	})
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main0" })
	return s, f, posts
}

var statusAgent = botTarget{Kind: targetAgent, MachineID: "server", SessionID: "agent0001"}

func putStatusPending(s *Service, id, kind, text string, at int64) {
	ix := proto.Interaction{ID: id, MachineID: "server", SessionID: "agent0001", Kind: kind, Summary: text, CreatedAt: time.Unix(at, 0)}
	if kind == proto.IxQuestion {
		ix.Questions = []proto.Question{{Question: text}}
	}
	s.fleet.mu.Lock()
	s.ixMapLocked()[id] = &pending{ix: ix, done: make(chan proto.Resolution, 1)}
	s.fleet.mu.Unlock()
}

func TestStatusCardStates(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ state, want string }{
		{proto.StateWorking, "工作中"}, {proto.StateNeedsYou, "等你拍板"}, {proto.StateIdle, "空闲"},
		{proto.StateUnread, "空闲"}, {proto.StateOffline, "离线 / 状态未知"},
		{proto.StateUnknown, "离线 / 状态未知"}, {"", "离线 / 状态未知"},
	} {
		s, f, _ := statusHub(t, "")
		f.setSessions("server", proto.Session{SessionID: "agent0001", MachineID: "server", Agent: "claude",
			State: c.state, LastText: strings.Repeat("忙", 120)})
		card, fb := s.statusCard(ctx, statusAgent)
		got := card.JSON()
		if !strings.Contains(got, c.want) || !strings.Contains(got, "没有待答问题") || strings.Count(got, "忙") != 90 ||
			!strings.Contains(fb, c.want) || strings.ContainsAny(fb, "*_[`") {
			t.Fatalf("state %q wrong: card=%s fallback=%q", c.state, got, fb)
		}
	}
}

func TestStatusCardGoal(t *testing.T) {
	ctx := context.Background()
	for i, status := range []string{proto.GoalActive, "", proto.GoalComplete} {
		s, f, _ := statusHub(t, "")
		f.setSessions("server", proto.Session{SessionID: "agent0001", MachineID: "server", Agent: "claude",
			State: proto.StateIdle, Goal: &proto.GoalInfo{Objective: "修完 5439", Status: status}})
		if card, _ := s.statusCard(ctx, statusAgent); strings.Contains(card.JSON(), "修完 5439") != (i < 2) {
			t.Fatalf("goal %q shown=%v", status, i < 2)
		}
	}
}

// Pending rows belong to the target session only, with a web link (none without a public URL) and the fallback.
func TestStatusCardPending(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, url string) *Service {
		t.Helper()
		s, f, _ := statusHub(t, url)
		f.setSessions("server", proto.Session{SessionID: "agent0001", MachineID: "server", Agent: "claude", State: proto.StateNeedsYou})
		putStatusPending(s, "ix1", proto.IxQuestion, "要现在发版吗？", 1000)
		putStatusPending(s, "ix3", proto.IxElicitation, "要授权外部工具", 2000)
		putStatusPending(s, "ix4", proto.IxQuestion, "", 0)
		s.fleet.mu.Lock()
		s.ixMapLocked()["ix1"].ix.SessionTitle = "修 5439 的会话" // the row must not repeat it
		s.ixMapLocked()["ix2"] = &pending{ix: proto.Interaction{ID: "ix2", MachineID: "server", SessionID: "agent0002",
			Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "另一个问题"}}}}
		s.fleet.mu.Unlock()
		return s
	}
	card, fb := setup(t, "https://hub.example.com").statusCard(ctx, statusAgent)
	got := card.JSON()
	for _, want := range []string{"**待答问题**", "要现在发版吗", "打开会话", "/s/server/agent0001", "<text_tag color='orange'>授权</text_tag>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	for _, bad := range []string{"修 5439 的会话", "另一个问题", "agent0002", "******"} {
		if strings.Contains(got, bad) {
			t.Fatalf("unexpected %q: %s", bad, got)
		}
	}
	if !strings.Contains(fb, "要现在发版吗") {
		t.Fatalf("fallback missing the question: %q", fb)
	}
	noLink, _ := setup(t, "").statusCard(ctx, statusAgent)
	if strings.Contains(noLink.JSON(), "打开会话") || strings.Contains(noLink.JSON(), "/s/") {
		t.Fatalf("links rendered without a public URL: %s", noLink.JSON())
	}
}

// More pending interactions than the cap are truncated with a "more" note, so a long queue can't overflow the card.
func TestStatusCardPendingCap(t *testing.T) {
	ctx := context.Background()
	s, f, _ := statusHub(t, "https://hub.example.com")
	f.setSessions("server", proto.Session{SessionID: "agent0001", MachineID: "server", Agent: "claude", State: proto.StateNeedsYou})
	for i := 0; i < statusPendingMax+3; i++ {
		putStatusPending(s, fmt.Sprintf("ix%02d", i), proto.IxQuestion, fmt.Sprintf("问题 %02d", i), int64(i))
	}
	card, fb := s.statusCard(ctx, statusAgent)
	got := card.JSON()
	if n := strings.Count(got, "问题 "); n != statusPendingMax || !strings.Contains(got, "还有 3 个") ||
		len(got) >= listCardMaxBytes || !strings.Contains(fb, "还有 3 个") {
		t.Fatalf("cap wrong: card=%s fallback=%q", got, fb)
	}
}

// In a thread /status follows the thread's target, and the post carries the content as a non-empty fallback.
func TestStatusCommandFollowsThreadTarget(t *testing.T) {
	ctx := context.Background()
	s, f, posts := statusHub(t, "")
	f.setSessions("server",
		proto.Session{SessionID: "main0", MachineID: "server", Assistant: true, State: proto.StateIdle, LastText: "主对话最近一句"},
		proto.Session{SessionID: "agent0001", MachineID: "server", Agent: "claude", State: proto.StateWorking, LastText: "正在跑测试"})
	s.rememberBotThread("om_root", statusAgent, "agent work")
	feats := FeishuFeatures{Main: true, Agents: true, Cards: true}
	if h, _ := s.botCommand(ctx, feats, "om", "/status", "om_root", ""); !h {
		t.Fatal("/status not handled")
	}
	post := (*posts)[len(*posts)-1]
	card, fallback := post["card"].(string), post["fallback"].(string)
	if !strings.Contains(card, "正在跑测试") || strings.Contains(card, "主对话最近一句") || strings.TrimSpace(fallback) == "" {
		t.Fatalf("card=%s fallback=%q", card, fallback)
	}
}

func TestStatusCardRunCount(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	if err := st.PutSetting(ctx, channelsStateKey, channelsState{Runs: []*channel.Run{{ID: "fb_1", Channel: channel.Feishu, MachineID: "server", SessionID: "main0"}}}); err != nil {
		t.Fatal(err)
	}
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main0" })
	f.setSessions("server", proto.Session{SessionID: "main0", MachineID: "server", Assistant: true, State: proto.StateIdle})
	card, fb := s.statusCard(ctx, botTarget{Kind: targetMain})
	if !strings.Contains(card.JSON(), "在等 1 个回答") || !strings.Contains(fb, "在等 1 个回答") {
		t.Fatalf("card=%s fallback=%q", card.JSON(), fb)
	}
}

// Without a public URL there is no web app to send the reader to: the "more" note just counts (#5677).
func TestStatusCardPendingCapWithoutWeb(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		url     string
		web     bool
		message string
	}{{"https://hub.test", true, "还有 3 个，去网页版看"}, {"", false, "还有 3 个待答"}, {"hub.test", false, "还有 3 个待答"}} {
		s, f, _ := statusHub(t, c.url)
		f.setSessions("server", proto.Session{SessionID: "agent0001", MachineID: "server", Agent: "claude", State: proto.StateNeedsYou})
		for i := 0; i < statusPendingMax+3; i++ {
			putStatusPending(s, fmt.Sprintf("ix%02d", i), proto.IxQuestion, fmt.Sprintf("问题 %02d", i), int64(i))
		}
		card, fb := s.statusCard(ctx, statusAgent)
		got := card.JSON()
		if !strings.Contains(got, c.message) || !strings.Contains(fb, c.message) ||
			strings.Contains(got, "网页版") != c.web || strings.Contains(fb, "网页版") != c.web {
			t.Fatalf("url %q: card=%s fallback=%q", c.url, got, fb)
		}
	}
}

// The fallback is plain text: the last activity loses its Markdown like every other line, and no line is doubled
// into a blank one (#5677).
func TestStatusCardFallbackPlain(t *testing.T) {
	ctx := context.Background()
	s, f, _ := statusHub(t, "")
	f.setSessions("server", proto.Session{SessionID: "agent0001", MachineID: "server", Agent: "claude",
		State: proto.StateIdle, LastText: "改好了 **foo_bar** 和 `x[1]`"})
	_, fb := s.statusCard(ctx, statusAgent)
	if strings.ContainsAny(fb, "*_[]`") || !strings.Contains(fb, "改好了 foobar 和 x1") || strings.Contains(fb, "\n\n") {
		t.Fatalf("fallback=%q", fb)
	}
}
