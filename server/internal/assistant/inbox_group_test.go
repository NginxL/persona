package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/rpc"
)

// groupLark stands in for a plugin's Feishu connector that relates new messages to events: it hands one batch of a
// chat until every message of it is in an event, writes events the way the plugin does (id from the first message,
// meta = the messages, the Feishu block, its rule tier fyi) and groups by "structure" when asked (fallback).
type groupLark struct {
	mu        sync.Mutex
	batch     *connector.InboxThing // nil once written
	placed    map[string]bool
	appends   []connector.GroupAppend
	tools     []connector.GroupToolCall
	fallbacks []connector.GroupFallback
	fallback  connector.GroupFallbackResult
	failNext  map[string]int // event (or "" for a new one) → appends to fail first
	hold      chan struct{}  // when set, appends wait for it
	count     int            // when set, the count its answers say (the hub does not read it for an event)
	ruleTier  string         // when set, the rule tier its answers carry (else the chat's fyi)
	tier      string         // when set, the tier its answers carry (a rule tier the item takes, as a migrated thing does)
}

func (*groupLark) ID() string   { return "lark" }
func (*groupLark) Name() string { return "飞书" }
func (*groupLark) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox, connector.CapGroup, connector.CapMeta}, Kinds: []string{KindGroup},
		MetaDescription: "示例 meta 说明：messages 是这件事的消息，旧的在前"}
}
func (g *groupLark) Collect(context.Context) ([]connector.InboxThing, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.batch == nil {
		return nil, nil
	}
	return []connector.InboxThing{*g.batch}, nil
}
func (*groupLark) Reply(context.Context, connector.InboxReply) error { return nil }
func (g *groupLark) GroupTool(_ context.Context, c connector.GroupToolCall) (json.RawMessage, error) {
	g.mu.Lock()
	g.tools = append(g.tools, c)
	g.mu.Unlock()
	return json.RawMessage(`{"messages":[{"id":"m1050","sender":"甲","text":"示例：周四评审 A 方案","avatar":"avatar:1"}]}`), nil
}
func (g *groupLark) GroupAppend(_ context.Context, a connector.GroupAppend) (connector.InboxThing, error) {
	if g.hold != nil {
		<-g.hold
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.appends = append(g.appends, a)
	if g.failNext[a.Event] > 0 {
		g.failNext[a.Event]--
		return connector.InboxThing{}, fmt.Errorf("示例：写 %q 失败", a.Event)
	}
	var meta struct {
		Messages  []string `json:"messages"`
		Continues string   `json:"continues,omitempty"`
	}
	_ = json.Unmarshal(a.Meta, &meta)
	meta.Messages = append(meta.Messages, a.Messages...)
	if a.Continues != "" {
		meta.Continues = a.Continues
	}
	raw, _ := json.Marshal(meta)
	first := strings.TrimPrefix(meta.Messages[0], "m")
	last := strings.TrimPrefix(meta.Messages[len(meta.Messages)-1], "m")
	id := a.Event
	if id == "" {
		id = "lark:F1:" + first
	}
	for _, m := range a.Messages {
		g.placed[m] = true
	}
	if g.batch != nil && !slices.ContainsFunc(g.batch.Group.New, func(m string) bool { return !g.placed[m] }) {
		g.batch = nil
	}
	return connector.InboxThing{ID: id, Thread: a.Thread, Kind: KindGroup, Title: "示例兜底标题 " + id, Sender: "丙", Preview: "示例预览",
		Context: "示例群：" + strings.Join(meta.Messages, "、"), At: time.Now().Add(-time.Minute), Last: last, Meta: raw, Tier: connector.Tier(g.tier),
		Extra: &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "飞书", Count: cmp.Or(g.count, len(meta.Messages)), RuleTier: cmp.Or(g.ruleTier, TierFYI),
			Lark: rawJSON(InboxLark{AccountID: 1, FeedID: "F1", IsGroup: true, StartPosition: first})}}, nil
}
func (g *groupLark) GroupFallback(_ context.Context, f connector.GroupFallback) (connector.GroupFallbackResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fallbacks = append(g.fallbacks, f)
	return g.fallback, nil
}

func (g *groupLark) counts() (appends, tools, fallbacks int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.appends), len(g.tools), len(g.fallbacks)
}

// groupBatch is a batch of chat F1's two new messages.
func groupBatch() *connector.InboxThing {
	return &connector.InboxThing{Thread: "chat:F1", Group: &connector.ThingGroup{Batch: "chat:F1@1061", New: []string{"m1060", "m1061"}},
		Context: "10:05 丙：示例 A 那个周四下午可以 (m1060)\n10:06 丁：示例 B 的接口改了 (m1061)", Rules: "示例来源规则：接口变更都需要我", Last: "1061"}
}

// groupModel is a model that answers the relating prompts with replies in turn (the last one again after that), or
// fails (down); the judge is told nothing is handed over. It keeps the relating requests.
type groupModel struct {
	mu      sync.Mutex
	replies []string
	stops   []string // stop_reason per reply ("" none): "max_tokens" cuts it off, "refusal" is a decline
	sleep   time.Duration
	down    bool
	calls   []capturedCall
	budgets []int
}

func newGroupModel(t *testing.T, replies ...string) (*groupModel, *ai.Client) {
	t.Helper()
	m := &groupModel{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			System    string `json:"system"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		reply := `{"handle":false,"reason":"示例"}`
		stop := ""
		if strings.Contains(in.System, "归到「事件」里") {
			m.mu.Lock()
			c := capturedCall{System: in.System}
			if len(in.Messages) > 0 {
				c.User = in.Messages[0].Content
			}
			m.calls = append(m.calls, c)
			m.budgets = append(m.budgets, in.MaxTokens)
			down := m.down
			sleep := m.sleep
			i := min(len(m.calls), len(m.replies)) - 1
			reply = m.replies[i]
			if i < len(m.stops) {
				stop = m.stops[i]
			}
			m.mu.Unlock()
			if sleep > 0 {
				time.Sleep(sleep)
			}
			if down {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"example failure"}}`))
				return
			}
		}
		body := map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}}
		if stop != "" {
			body["stop_reason"] = stop
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	return m, ai.New(ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "m"})
}

func (m *groupModel) asked() []capturedCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]capturedCall(nil), m.calls...)
}

func (m *groupModel) tokenBudgets() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int(nil), m.budgets...)
}

// groupHub is a hub with the connector and model, an open event of chat F1 (lark:F1:1051) and one done yesterday
// (lark:F1:900).
func groupHub(t *testing.T, g *groupLark, model *ai.Client) (*inboxHub, *inboxMemStore) {
	t.Helper()
	h, st := admitHub(t, model, g)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Rules = "示例全局规则" })
	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	for _, it := range []*InboxItem{
		{ID: "lark:F1:1051", Source: "lark", Kind: KindGroup, Title: "示例 A 方案评审", Summary: "等你定时间", Tier: TierAction, Reason: "示例",
			Last: "1051", TriagedAt: "1051", TriagedBy: "model", Count: 1, UpdatedAt: now.Add(-time.Hour), Context: "示例",
			Meta: json.RawMessage(`{"messages":["m1051"]}`), Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"},
			Lark: &InboxLark{AccountID: 1, FeedID: "F1", IsGroup: true, StartPosition: "1051"}},
		{ID: "lark:F1:900", Source: "lark", Kind: KindGroup, Title: "示例上周的发布", Tier: TierFYI, Last: "900", TriagedAt: "900",
			UpdatedAt: yesterday, DoneAt: &yesterday, DoneBy: "user", Meta: json.RawMessage(`{"messages":["m900"]}`),
			Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"}, Lark: &InboxLark{AccountID: 1, FeedID: "F1", IsGroup: true}},
	} {
		h.inbox.items[it.ID] = it
	}
	return h, st
}

func (h *inboxHub) waitGrouping() { h.inbox.grouping.wg.Wait() }

const groupAnswerOK = `{"assign":[{"message":"m1060","event":"lark:F1:1051"},{"message":"m1061","event":"new:1"}],
	"events":[{"event":"lark:F1:1051","title":"示例 A 方案周四下午评审","summary":"丙说周四下午可以，等你确认","tier":"action","reason":"等你确认"},
	{"event":"new:1","title":"示例 B 接口变更","summary":"丁说 B 的接口改了","tier":"fyi","reason":"同步","continues":"lark:F1:900"}]}`

// The model reads the conversation (one tool call), puts each new message in an event (one existing, one new that
// continues a done one); the connector writes each event once and the items take the model's words, triaged_by model.
// The batch itself never becomes an item, and once written the connector stops handing it.
func TestInboxGroupByTheModel(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	model, client := newGroupModel(t, `{"tool":"messages_before","args":{"message":"m1060","n":5}}`, groupAnswerOK)
	h, st := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	calls := model.asked()
	if len(calls) != 2 {
		t.Fatalf("%d model calls", len(calls))
	}
	sys := calls[0].System
	for _, want := range []string{"示例 meta 说明：messages 是这件事的消息", "示例来源规则：接口变更都需要我", "noise（噪音）", "最多 4 次"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "示例全局规则") {
		t.Error("the batch's own rules should replace the global ones")
	}
	if u := calls[0].User; !strings.Contains(u, "m1060、m1061") || !strings.Contains(u, `"id":"lark:F1:1051"`) || !strings.Contains(u, `"meta":{"messages":["m1051"]}`) ||
		!strings.Contains(u, `"done":true`) || !strings.Contains(u, "还能查 4 次") {
		t.Errorf("first input:\n%s", u)
	}
	if u := calls[1].User; !strings.Contains(u, `你调了 messages_before {"message":"m1060","n":5}`) || !strings.Contains(u, "示例：周四评审 A 方案") ||
		strings.Contains(u, "avatar:1") || !strings.Contains(u, "还能查 3 次") {
		t.Errorf("second input:\n%s", u)
	}
	if len(g.tools) != 1 || g.tools[0].Thread != "chat:F1" || g.tools[0].Name != connector.GroupMessagesBefore {
		t.Fatalf("tools %+v", g.tools)
	}
	if len(g.appends) != 2 || g.appends[0].Event != "lark:F1:1051" || string(g.appends[0].Meta) != `{"messages":["m1051"]}` ||
		!slices.Equal(g.appends[0].Messages, []string{"m1060"}) || g.appends[1].Event != "" || g.appends[1].Meta != nil ||
		!slices.Equal(g.appends[1].Messages, []string{"m1061"}) || g.appends[1].Continues != "lark:F1:900" {
		t.Fatalf("appends %+v", g.appends)
	}
	old, fresh := h.inbox.items["lark:F1:1051"], h.inbox.items["lark:F1:1061"]
	if old.Title != "示例 A 方案周四下午评审" || old.Summary != "丙说周四下午可以，等你确认" || old.Tier != TierAction || old.TriagedBy != "model" ||
		old.Last != "1060" || old.TriagedAt != old.Last || string(old.Meta) != `{"messages":["m1051","m1060"]}` || old.Count != 2 {
		t.Fatalf("existing event %+v", old)
	}
	if fresh == nil || fresh.Tier != TierFYI || fresh.Title != "示例 B 接口变更" || fresh.TriagedBy != "model" || fresh.TriagedAt != fresh.Last ||
		fresh.Group == nil || fresh.Group.Thread != "chat:F1" || fresh.Lark == nil || fresh.Lark.FeedID != "F1" || fresh.Source != "lark" ||
		!strings.Contains(string(fresh.Meta), `"continues":"lark:F1:900"`) {
		t.Fatalf("new event %+v", fresh)
	}
	if len(h.inbox.items) != 3 || !st.saved("lark:F1:1051") || !st.saved("lark:F1:1061") {
		t.Fatalf("%d items, saved %v", len(h.inbox.items), st.ids)
	}
	items, _ := inboxTriageOf(t, h)
	for _, x := range items {
		m := x.(map[string]any)
		if m["id"] == "lark:F1:1061" && (m["triaged_by"] != "model" || m["meta"] != nil || m["group"] != nil) {
			t.Fatalf("listed %v", m)
		}
	}

	// The connector stops handing the batch once every message is in an event: nothing more happens.
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if n, _, _ := g.counts(); n != 2 || len(model.asked()) != 2 {
		t.Fatalf("after: %d appends, %d model calls", n, len(model.asked()))
	}
}

// A malformed answer or an answer that fails validation is handed back once with the original error. The correction
// spends one call allowance, is not recorded as a tool call, and a valid correction is written by the model.
func TestInboxGroupReasksAnInvalidAnswer(t *testing.T) {
	badAnswer := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`
	for _, tc := range []struct {
		name, reply, problem string
	}{
		{name: "validation", reply: badAnswer, problem: "消息 m1061 没有归到事件"},
		{name: "not JSON", reply: "not JSON", problem: ai.ErrNoObject.Error()},
		{name: "decode", reply: `{"assign":[}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
			model, client := newGroupModel(t, tc.reply, groupAnswerOK)
			h, _ := groupHub(t, g, client)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			calls := model.asked()
			if len(calls) != 2 {
				t.Fatalf("%d model calls, want the initial call and one correction", len(calls))
			}
			if !strings.Contains(calls[1].User, "你上一次给的结果不合格：") || !strings.Contains(calls[1].User, "改好后重新给出完整结果") {
				t.Fatalf("correction input lacks the problem:\n%s", calls[1].User)
			}
			if tc.problem != "" && !strings.Contains(calls[1].User, tc.problem) {
				t.Errorf("correction input lacks %q:\n%s", tc.problem, calls[1].User)
			}
			if !strings.Contains(calls[1].User, "还能查 3 次") || strings.Contains(calls[1].User, "你调了") {
				t.Errorf("correction did not spend only one allowance:\n%s", calls[1].User)
			}
			if len(g.fallbacks) != 0 {
				t.Fatalf("a valid correction used the fallback: %+v", g.fallbacks)
			}
			fresh := h.inbox.items["lark:F1:1061"]
			if fresh == nil || fresh.Title != "示例 B 接口变更" || fresh.TriagedBy != "model" {
				t.Fatalf("corrected event %+v", fresh)
			}
		})
	}
}

// The other malformed replies A-4 names are corrected the same way: an object that never closes (ErrUnterminated) and
// one cut off at max_tokens even after the client's own doubling (ErrTruncated).
func TestInboxGroupReasksUnterminatedAndTruncated(t *testing.T) {
	cut := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","ti`
	for _, tc := range []struct {
		name    string
		replies []string
		stops   []string
		problem string
		calls   int
	}{
		{name: "unterminated", replies: []string{`{"assign":[{"message":"m1060"`, groupAnswerOK}, problem: "unterminated", calls: 2},
		// A closed object that does not decode into the answer (a json.UnmarshalTypeError, not a syntax error).
		{name: "type mismatch", replies: []string{`{"assign":[{"message":1060,"event":"lark:F1:1051"}]}`, groupAnswerOK}, problem: "cannot unmarshal", calls: 2},
		// JSONMax asks once more with twice the room itself; both cut, ErrTruncated is the grouping's to correct.
		{name: "truncated", replies: []string{cut, cut, groupAnswerOK}, stops: []string{"max_tokens", "max_tokens"}, problem: "max_tokens", calls: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
			model, client := newGroupModel(t, tc.replies...)
			model.stops = tc.stops
			h, _ := groupHub(t, g, client)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			calls := model.asked()
			if len(calls) != tc.calls {
				t.Fatalf("%d model calls, want %d", len(calls), tc.calls)
			}
			last := calls[len(calls)-1].User
			if !strings.Contains(last, "你上一次给的结果不合格：") || !strings.Contains(last, tc.problem) {
				t.Fatalf("correction input lacks %q:\n%s", tc.problem, last)
			}
			if len(g.fallbacks) != 0 {
				t.Fatalf("a valid correction used the fallback: %+v", g.fallbacks)
			}
			if fresh := h.inbox.items["lark:F1:1061"]; fresh == nil || fresh.TriagedBy != "model" {
				t.Fatalf("corrected event %+v", fresh)
			}
		})
	}
}

// The correction is one of the four allowances, so after it only three tool calls remain: a fourth request for a tool
// is refused (the batch goes to the fallback), and a bad answer arriving when one allowance is left is corrected with
// the prompt saying no more lookups are allowed.
func TestInboxGroupCorrectionSpendsAnAllowance(t *testing.T) {
	bad := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`
	tool := `{"tool":"search","args":{"query":"示例","n":5}}`
	t.Run("three tools after a correction", func(t *testing.T) {
		model, client := newGroupModel(t, bad, tool, tool, tool, tool)
		g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
			New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierFYI}}}}
		h, _ := groupHub(t, g, client)
		plan, why := h.askGroup(context.Background(), sourceOf(g), *groupBatch(), h.groupEvents("lark", "chat:F1", ""))
		if plan != nil || !strings.Contains(why, "还要查") {
			t.Fatalf("plan %+v, why %q", plan, why)
		}
		if len(model.asked()) != groupCalls+1 || len(g.tools) != groupCalls-1 {
			t.Fatalf("%d model calls and %d tool calls: one correction plus three tools", len(model.asked()), len(g.tools))
		}
	})
	t.Run("bad answer with one allowance left", func(t *testing.T) {
		model, client := newGroupModel(t, tool, tool, tool, bad, groupAnswerOK)
		g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
		h, _ := groupHub(t, g, client)
		h.collectInbox(context.Background(), roundPoll)
		h.waitGrouping()

		calls := model.asked()
		if len(calls) != groupCalls+1 {
			t.Fatalf("%d model calls, want three tools, the bad answer and its correction", len(calls))
		}
		if last := calls[groupCalls].User; !strings.Contains(last, "你上一次给的结果不合格：") || !strings.Contains(last, "不能再查了") {
			t.Fatalf("the correction with no allowance left:\n%s", last)
		}
		if h.inbox.items["lark:F1:1061"] == nil {
			t.Fatal("not related after the correction")
		}
	})
}

// A model that takes too long is a timeout, not a malformed answer: it is not asked again, and the fallback groups the
// batch (A-4: timeouts, holds, unconfigured models and transport errors are not corrected).
func TestInboxGroupTimeoutIsNotReasked(t *testing.T) {
	step, total := groupStepTimeout, groupTimeout
	groupStepTimeout, groupTimeout = 20*time.Millisecond, time.Second
	t.Cleanup(func() { groupStepTimeout, groupTimeout = step, total })
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierFYI}}}}
	model, client := newGroupModel(t, groupAnswerOK)
	model.sleep = 200 * time.Millisecond
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if calls := model.asked(); len(calls) != 1 {
		t.Fatalf("a timeout was retried: %d model calls", len(calls))
	}
	if len(g.fallbacks) != 1 {
		t.Fatalf("%d fallback call(s)", len(g.fallbacks))
	}
}

// A model that declines the request is not asked again: the same input is declined again, so a second call only spends
// one (ai.ErrRefused). The fallback groups the batch.
func TestInboxGroupRefusalIsNotReasked(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierFYI}}}}
	model, client := newGroupModel(t, groupAnswerOK)
	model.stops = []string{"refusal"}
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if calls := model.asked(); len(calls) != 1 {
		t.Fatalf("a refusal was retried: %d model calls", len(calls))
	}
	if len(g.fallbacks) != 1 {
		t.Fatalf("%d fallback call(s)", len(g.fallbacks))
	}
}

// The correction is shown on the request right after the bad answer only: a tool call after a good correction carries
// no problem, and the allowance the correction spent still shows in "还能查 N 次".
func TestInboxGroupCorrectionShownOnce(t *testing.T) {
	bad := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`
	tool := `{"tool":"search","args":{"query":"示例","n":5}}`
	model, client := newGroupModel(t, bad, tool, groupAnswerOK)
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	calls := model.asked()
	if len(calls) != 3 {
		t.Fatalf("%d model calls, want bad, corrected tool call, answer", len(calls))
	}
	if !strings.Contains(calls[1].User, "你上一次给的结果不合格：") || !strings.Contains(calls[1].User, "还能查 3 次") {
		t.Fatalf("correction input:\n%s", calls[1].User)
	}
	if strings.Contains(calls[2].User, "你上一次给的结果不合格") {
		t.Errorf("the problem outlived the request it was for:\n%s", calls[2].User)
	}
	// One tool call and one correction: two allowances of the four, so the second tool call is still allowed.
	if !strings.Contains(calls[2].User, "还能查 2 次") {
		t.Errorf("allowance after a correction and a tool call:\n%s", calls[2].User)
	}
	if h.inbox.items["lark:F1:1061"] == nil {
		t.Fatal("not related after the correction")
	}
	// Every step, the correction included, carries the batch's computed budget (never the old 0 = default).
	budgets := model.tokenBudgets()
	want := groupOutputBudget(len(groupBatch().Group.New), 2)
	if len(budgets) != 3 {
		t.Fatalf("%d budgets for 3 calls: %v", len(budgets), budgets)
	}
	for i, b := range budgets {
		if b != want {
			t.Errorf("call %d budget %d, want %d", i, b, want)
		}
	}
}

// A valid answer opening four events is accepted and written: the 3-event limit is the prompt's guidance only. The
// extra events survive foldNoise (only noise is folded) and each is written once.
func TestInboxGroupAcceptsMoreThanThreeNewEvents(t *testing.T) {
	batch := groupBatch()
	batch.Group.New = []string{"m1060", "m1061", "m1062", "m1063"}
	var assign, events []string
	for i, m := range batch.Group.New {
		ev := fmt.Sprintf("new:%d", i+1)
		assign = append(assign, fmt.Sprintf(`{"message":%q,"event":%q}`, m, ev))
		events = append(events, fmt.Sprintf(`{"event":%q,"title":"示例事件 %d","summary":"示例","tier":"fyi","reason":"同步"}`, ev, i+1))
	}
	answer := `{"assign":[` + strings.Join(assign, ",") + `],"events":[` + strings.Join(events, ",") + `]}`
	g := &groupLark{batch: batch, placed: map[string]bool{}}
	model, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if calls := model.asked(); len(calls) != 1 {
		t.Fatalf("%d model calls: a valid four-event answer should not be asked again", len(calls))
	}
	if len(g.appends) != 4 || len(g.fallbacks) != 0 {
		t.Fatalf("%d appends, %d fallback(s)", len(g.appends), len(g.fallbacks))
	}
	for _, m := range batch.Group.New {
		if it := h.inbox.items["lark:F1:"+strings.TrimPrefix(m, "m")]; it == nil || it.TriagedBy != "model" {
			t.Fatalf("event of %s: %+v", m, it)
		}
	}
}

// An answer that is still invalid after its correction is the model being unavailable: the connector's fallback groups
// the batch, with the events the model would have seen. A new event keeps its rule tier ("By rule", triaged_by rules) for good. An existing
// event keeps its words and triage, with triaged_at advanced so the model coming back sorts nothing again.
func TestInboxGroupFallsBackOnABadAnswer(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}},
		New:    []connector.GroupNew{{Key: "k1", Messages: []string{"m1061"}, RuleTier: connector.TierNoise, Title: "示例结构分组"}}}}
	model, client := newGroupModel(t, `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`)
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	calls := model.asked()
	if len(calls) != 2 || !strings.Contains(calls[1].User, "你上一次给的结果不合格：") {
		t.Fatalf("bad answer was not corrected once: %d calls", len(calls))
	}
	h.inbox.grouping.mu.Lock()
	why := h.inbox.grouping.why["lark"]
	h.inbox.grouping.mu.Unlock()
	if !strings.Contains(why, "重问后仍不合格") {
		t.Errorf("fallback reason %q does not name the failed correction", why)
	}

	if len(g.fallbacks) != 1 || g.fallbacks[0].Batch != "chat:F1@1061" || len(g.fallbacks[0].Events) != 2 ||
		g.fallbacks[0].Events[0].ID != "lark:F1:1051" || string(g.fallbacks[0].Events[0].Meta) != `{"messages":["m1051"]}` || !g.fallbacks[0].Events[1].Done {
		t.Fatalf("fallback asked %+v", g.fallbacks)
	}
	old, fresh := h.inbox.items["lark:F1:1051"], h.inbox.items["lark:F1:1061"]
	if old.Tier != TierAction || old.Reason != "示例" || old.TriagedBy != "model" || old.Summary != "等你定时间" ||
		old.Title != "示例 A 方案评审" || old.Count != 2 || old.Last != "1060" || old.TriagedAt != old.Last {
		t.Fatalf("existing event %+v", old)
	}
	if fresh == nil || fresh.Tier != TierNoise || fresh.Title != "示例结构分组" || fresh.TriagedBy != "rules" || fresh.Reason != "按规则" {
		t.Fatalf("new event %+v", fresh)
	}
	before := len(model.asked())
	h.triageInbox(context.Background())
	h.inbox.triaging.Lock()
	h.inbox.triaging.Unlock()
	if len(model.asked()) != before || old.TriagedBy != "model" {
		t.Fatal("an event triaged at this version was sorted again")
	}
}

// Fallback appends preserve an existing event's triage whether the model or rules originally sorted it. In particular,
// noise stays folded and an urgent event stays urgent; only the message count and version advance.
func TestInboxGroupFallbackKeepsExistingTriage(t *testing.T) {
	tests := []struct {
		name, title, summary, tier, reason, triagedBy string
		urgent                                        bool
	}{
		{name: "noise", title: "示例折叠标题", summary: "示例折叠总结", tier: TierNoise, reason: "示例折叠理由", triagedBy: "model"},
		{name: "rules", title: "示例规则标题", summary: "示例规则总结", tier: TierFYI, reason: "按规则", triagedBy: "rules"},
		{name: "urgent", title: "示例紧急标题", summary: "示例紧急总结", tier: TierAction, reason: "示例紧急理由", triagedBy: "model", urgent: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
				Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}, {Message: "m1061", Event: "lark:F1:1051"}}}}
			h, _ := groupHub(t, g, nil)
			it := h.inbox.items["lark:F1:1051"]
			it.Title, it.Summary, it.Tier, it.Reason, it.TriagedBy = tc.title, tc.summary, tc.tier, tc.reason, tc.triagedBy
			it.Urgent = tc.urgent
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if it.Title != tc.title || it.Summary != tc.summary || it.Tier != tc.tier || it.Reason != tc.reason ||
				it.TriagedBy != tc.triagedBy || it.Urgent != tc.urgent || it.Count != 3 || it.Last != "1061" || it.TriagedAt != it.Last {
				t.Fatalf("existing event %+v", it)
			}
		})
	}
}

// A connector that keeps no meta (the hub composes its events): fallback messages joining an existing event keep its
// words and triage as well, and the rule tier the connector's thing carries is not recorded as a retriage.
func TestInboxGroupFallbackKeepsExistingTriageWithoutMeta(t *testing.T) {
	batch := chatBatch()
	batch.Tier, batch.Why = connector.TierNoise, "示例来源规则：这是噪音"
	g := &groupChat{batch: batch, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m2", Event: "examplechat:chat:C1#m1"}, {Message: "m3", Event: "examplechat:chat:C1#m1"}}}}
	model, client := newGroupModel(t, chatAnswer)
	model.down = true
	h := chatHub(t, g, client)
	it := h.inbox.items["examplechat:chat:C1#m1"]
	it.Summary, it.Reason = "示例总结：等你定时间", "示例理由：等你定时间"
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if g.fallbacks != 1 {
		t.Fatalf("%d fallback call(s)", g.fallbacks)
	}
	if it.Title != "示例 A 方案评审" || it.Summary != "示例总结：等你定时间" || it.Tier != TierAction || it.Reason != "示例理由：等你定时间" ||
		it.TriagedBy != "model" || it.Count != 3 || it.Last != "3" || it.TriagedAt != it.Last {
		t.Fatalf("existing event %+v", it)
	}
	if k := chainKinds(it); slices.Contains(k, ChainTriaged) {
		t.Fatalf("the event's rule tier was recorded as a retriage: %v", k)
	}
}

// A policy the user set still gives an existing event its tier when fallback messages join it: the words and the
// triager stay, the tier and reason are the policy's.
func TestInboxGroupFallbackKeepsPolicyTier(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}, {Message: "m1061", Event: "lark:F1:1051"}}}}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:1051"]
	it.Tier, it.Reason = TierFYI, "示例理由：同步"
	h.inbox.mu.Lock()
	h.inbox.policies = []InboxPolicy{{ID: "p1", Source: "lark", Tag: connector.TagChat, Value: "F1", Name: "示例项目群", Tier: TierAction}}
	h.inbox.mu.Unlock()
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if it.Tier != TierAction || it.Reason == "示例理由：同步" || it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" ||
		it.TriagedBy != "model" || it.Count != 3 || it.TriagedAt != it.Last {
		t.Fatalf("existing event %+v", it)
	}
	if k := chainKinds(it); k[len(k)-1] != ChainTriaged {
		t.Fatalf("the policy's tier was not recorded: %v", k)
	}
}

// An event a processing conversation is working on keeps its summary, tier, reason and urgency when new messages
// join it, whatever tier the rule carried by this batch says: the conversation decides, and a provisional rule tier
// is not recorded as a retriage. Both ways an event's messages are written (the connector keeps the meta, or the hub
// composes it).
func TestInboxGroupHandledEventKeepsItsTriage(t *testing.T) {
	t.Run("meta", func(t *testing.T) {
		g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, ruleTier: TierNoise, tier: TierNoise, fallback: connector.GroupFallbackResult{
			Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}, {Message: "m1061", Event: "lark:F1:1051"}}}}
		h, _ := groupHub(t, g, nil)
		it := h.inbox.items["lark:F1:1051"]
		it.Urgent = true
		it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
		h.collectInbox(context.Background(), roundPoll)
		h.waitGrouping()

		if it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" || it.Tier != TierAction || it.Reason != "示例" ||
			it.TriagedBy != "model" || !it.Urgent || it.Count != 3 || it.Last != "1061" || it.TriagedAt != it.Last {
			t.Fatalf("handled event %+v", it)
		}
		if k := chainKinds(it); slices.Contains(k, ChainTriaged) {
			t.Fatalf("the batch's rule tier was recorded as a retriage: %v", k)
		}
	})
	t.Run("no meta", func(t *testing.T) {
		batch := chatBatch()
		batch.Tier, batch.Why = connector.TierNoise, "示例来源规则：这是噪音"
		g := &groupChat{batch: batch, fallback: connector.GroupFallbackResult{
			Assign: []connector.GroupAssign{{Message: "m2", Event: "examplechat:chat:C1#m1"}, {Message: "m3", Event: "examplechat:chat:C1#m1"}}}}
		model, client := newGroupModel(t, chatAnswer)
		model.down = true
		h := chatHub(t, g, client)
		it := h.inbox.items["examplechat:chat:C1#m1"]
		it.Summary, it.Reason = "示例总结：等你定时间", "示例理由：等你定时间"
		it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
		h.collectInbox(context.Background(), roundPoll)
		h.waitGrouping()

		if it.Title != "示例 A 方案评审" || it.Summary != "示例总结：等你定时间" || it.Tier != TierAction ||
			it.Reason != "示例理由：等你定时间" || it.TriagedBy != "model" || it.Count != 3 || it.Last != "3" || it.TriagedAt != it.Last {
			t.Fatalf("handled event %+v", it)
		}
		if k := chainKinds(it); slices.Contains(k, ChainTriaged) {
			t.Fatalf("the batch's rule tier was recorded as a retriage: %v", k)
		}
	})
}

// The model down (or none set up): the fallback groups it. A model that only ever wants tools stops after four.
func TestInboxGroupFallsBackWithoutTheModel(t *testing.T) {
	fallback := connector.GroupFallbackResult{New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}}}
	for _, c := range []string{"down", "none", "tools"} {
		t.Run(c, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: fallback}
			model, client := newGroupModel(t, `{"tool":"search","args":{"query":"示例","n":5}}`)
			model.down = c == "down"
			if c == "none" {
				client = nil
			}
			h, _ := groupHub(t, g, client)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()
			fresh := h.inbox.items["lark:F1:1060"]
			if len(g.fallbacks) != 1 || fresh == nil || fresh.Tier != TierAction || fresh.TriagedBy != "rules" || fresh.Count != 2 ||
				fresh.Title != "示例兜底标题 lark:F1:1060" {
				t.Fatalf("%d fallback(s), event %+v", len(g.fallbacks), fresh)
			}
			if c == "tools" && (len(model.asked()) != groupCalls+1 || len(g.tools) != groupCalls) {
				t.Fatalf("%d model calls, %d tool calls", len(model.asked()), len(g.tools))
			}
			if c == "down" && len(model.asked()) != 1 {
				t.Fatalf("transport failure was retried: %d model calls", len(model.asked()))
			}
		})
	}
}

// An invalid answer after the last allowed call goes straight to the fallback: there is no allowance for a correction.
func TestInboxGroupDoesNotReaskWhenNoCallsRemain(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	tool := `{"tool":"search","args":{"query":"示例","n":5}}`
	bad := `{"assign":[{"message":"m1060","event":"lark:F1:1051"}],"events":[{"event":"lark:F1:1051","title":"示例","tier":"fyi"}]}`
	model, client := newGroupModel(t, tool, tool, tool, tool, bad, groupAnswerOK)
	h, _ := groupHub(t, g, client)
	plan, why := h.askGroup(context.Background(), sourceOf(g), *groupBatch(), h.groupEvents("lark", "chat:F1", ""))
	if plan != nil || !strings.Contains(why, "模型的结果不合格") {
		t.Fatalf("plan %+v, why %q", plan, why)
	}
	if len(model.asked()) != groupCalls+1 || len(g.tools) != groupCalls {
		t.Fatalf("%d model calls and %d tool calls, want %d and %d", len(model.asked()), len(g.tools), groupCalls+1, groupCalls)
	}
}

// A batch handed again while it is being related (the next round, or twice in one collection) is related once.
func TestInboxGroupBatchOnce(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, hold: make(chan struct{})}
	model, client := newGroupModel(t, groupAnswerOK)
	h, _ := groupHub(t, g, client)
	twice := []connector.InboxThing{*groupBatch(), *groupBatch()}
	if rest := h.relateBatches(context.Background(), sourceOf(g), twice); len(rest) != 0 {
		t.Fatalf("batches left as things: %+v", rest)
	}
	h.collectInbox(context.Background(), roundPoll) // still writing the first: not again
	waitFor(t, "the model asked", func() bool { return len(model.asked()) == 1 })
	h.collectInbox(context.Background(), roundPoll)
	close(g.hold)
	h.waitGrouping()
	if n, _, _ := g.counts(); n != 2 || len(model.asked()) != 1 {
		t.Fatalf("%d appends, %d model calls", n, len(model.asked()))
	}
}

// An event whose append fails is tried again alone; the others are written once.
func TestInboxGroupRetriesOneEvent(t *testing.T) {
	wait := groupRetryWait
	groupRetryWait = time.Millisecond
	t.Cleanup(func() { groupRetryWait = wait })
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, failNext: map[string]int{"": 1}}
	_, client := newGroupModel(t, groupAnswerOK)
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	var existing, fresh int
	for _, a := range g.appends {
		if a.Event == "" {
			fresh++
		} else {
			existing++
		}
	}
	if existing != 1 || fresh != 2 || h.inbox.items["lark:F1:1061"] == nil {
		t.Fatalf("appends: %d to the existing event, %d to the new one", existing, fresh)
	}
}

// A connector that does not declare group: its batches are not items, nor related.
func TestInboxGroupNeedsTheCapability(t *testing.T) {
	c := &inboxConnector{things: []connector.InboxThing{*groupBatch()}}
	h, st := admitHub(t, nil, c)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if len(h.inbox.items) != 0 || len(st.ids) != 0 {
		t.Fatalf("items %v", h.inbox.items)
	}
}

// The response budget grows with the batch but stays within the model's lower and upper bounds.
func TestInboxGroupOutputBudget(t *testing.T) {
	for _, tc := range []struct {
		fresh, candidates, want int
	}{
		{fresh: 1, candidates: 0, want: 1024},
		{fresh: 8, candidates: 2, want: 1440},
		{fresh: 100, candidates: 100, want: 4096},
		// The floor: a small batch, and a batch whose candidates are many still counts only the messages.
		{fresh: 0, candidates: 0, want: 1024},
		{fresh: 3, candidates: 0, want: 1024},
		{fresh: 2, candidates: 10, want: 1024},
		// Above the floor: 4 messages and 4 candidates → 256 + 192 + 640.
		{fresh: 4, candidates: 4, want: 1088},
		// min(fresh, candidates+3) tops out at the messages themselves.
		{fresh: 8, candidates: 100, want: 1920},
		// The ceiling.
		{fresh: 1000, candidates: 1000, want: 4096},
	} {
		if got := groupOutputBudget(tc.fresh, tc.candidates); got != tc.want {
			t.Errorf("groupOutputBudget(%d, %d) = %d, want %d", tc.fresh, tc.candidates, got, tc.want)
		}
	}
}

// askGroup passes the computed budget to the model, rather than relying on JSON's default budget.
func TestInboxGroupPassesOutputBudget(t *testing.T) {
	batch := groupBatch()
	batch.Group.New = []string{"m1060", "m1061", "m1062", "m1063", "m1064", "m1065", "m1066", "m1067"}
	assign := make([]string, 0, len(batch.Group.New))
	for _, id := range batch.Group.New {
		assign = append(assign, fmt.Sprintf(`{"message":%q,"event":"new:1"}`, id))
	}
	answer := `{"assign":[` + strings.Join(assign, ",") + `],"events":[{"event":"new:1","title":"示例","tier":"fyi"}]}`
	g := &groupLark{batch: batch, placed: map[string]bool{}}
	model, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	events := h.groupEvents("lark", batch.Thread, "")
	plan, why := h.askGroup(context.Background(), sourceOf(g), *batch, events)
	if plan == nil || why != "" {
		t.Fatalf("plan %+v, why %q", plan, why)
	}
	budgets := model.tokenBudgets()
	if len(budgets) != 1 || budgets[0] != 1440 {
		t.Fatalf("model budgets %v, want [1440]", budgets)
	}
}

// The rules an answer is held to.
func TestInboxGroupAnswerChecks(t *testing.T) {
	seen := map[string]groupEvent{"e1": {ID: "e1"}, "e0": {ID: "e0", Done: true}}
	fresh := []string{"m1", "m2"}
	ev := func(id, tier string) string {
		return fmt.Sprintf(`{"event":%q,"title":"示例","tier":%q}`, id, tier)
	}
	for name, c := range map[string]struct {
		answer string
		ok     bool
	}{
		"ok":             {`{"assign":[{"message":"m1","event":"e1"},{"message":"m2","event":"new:1"}],"events":[` + ev("e1", "fyi") + `,` + ev("new:1", "action") + `]}`, true},
		"continues done": {`{"assign":[{"message":"m1","event":"new:1"},{"message":"m2","event":"new:1"}],"events":[{"event":"new:1","title":"示例","tier":"fyi","continues":"e0"}]}`, true},
		"unassigned":     {`{"assign":[{"message":"m1","event":"e1"}],"events":[` + ev("e1", "fyi") + `]}`, false},
		"twice":          {`{"assign":[{"message":"m1","event":"e1"},{"message":"m1","event":"e1"},{"message":"m2","event":"e1"}],"events":[` + ev("e1", "fyi") + `]}`, false},
		"not new":        {`{"assign":[{"message":"m1","event":"e1"},{"message":"m9","event":"e1"}],"events":[` + ev("e1", "fyi") + `]}`, false},
		"unseen":         {`{"assign":[{"message":"m1","event":"e7"},{"message":"m2","event":"e7"}],"events":[` + ev("e7", "fyi") + `]}`, false},
		"done":           {`{"assign":[{"message":"m1","event":"e0"},{"message":"m2","event":"e0"}],"events":[` + ev("e0", "fyi") + `]}`, false},
		"bad tier":       {`{"assign":[{"message":"m1","event":"e1"},{"message":"m2","event":"e1"}],"events":[` + ev("e1", "urgent") + `]}`, false},
		"no title":       {`{"assign":[{"message":"m1","event":"e1"},{"message":"m2","event":"e1"}],"events":[{"event":"e1","tier":"fyi"}]}`, false},
		"undescribed":    {`{"assign":[{"message":"m1","event":"e1"},{"message":"m2","event":"new:1"}],"events":[` + ev("e1", "fyi") + `]}`, false},
		"continues open": {`{"assign":[{"message":"m1","event":"new:1"},{"message":"m2","event":"new:1"}],"events":[{"event":"new:1","title":"示例","tier":"fyi","continues":"e1"}]}`, false},
		"bad new name":   {`{"assign":[{"message":"m1","event":"new:x"},{"message":"m2","event":"new:x"}],"events":[` + ev("new:x", "fyi") + `]}`, false},
	} {
		var a groupAnswer
		if err := json.Unmarshal([]byte(c.answer), &a); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := checkGroupAnswer(a, fresh, seen); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The prompt suggests at most groupNewMax new events, but a larger valid answer is accepted.
	var a groupAnswer
	_ = json.Unmarshal([]byte(`{"assign":[{"message":"m1","event":"new:1"},{"message":"m2","event":"new:2"},{"message":"m3","event":"new:3"},
		{"message":"m4","event":"new:4"}],"events":[`+ev("new:1", "fyi")+`,`+ev("new:2", "fyi")+`,`+ev("new:3", "fyi")+`,`+ev("new:4", "fyi")+`]}`), &a)
	plan, err := checkGroupAnswer(a, []string{"m1", "m2", "m3", "m4"}, seen)
	if err != nil || len(plan.events) != 4 {
		t.Errorf("four new events: plan %+v, err %v", plan, err)
	}
}

// The events tool: the conversation's events only (not another chat whose id starts the same), open ones first,
// done ones within three days, at most ten; a query narrows them.
func TestInboxGroupEvents(t *testing.T) {
	h, _ := admitHub(t, nil)
	now := time.Now()
	long := now.Add(-4 * 24 * time.Hour)
	add := func(id, thread, title string, done *time.Time, at time.Time) {
		h.inbox.items[id] = &InboxItem{ID: id, Title: title, UpdatedAt: at, DoneAt: done, Group: &InboxGroup{Connector: "lark", Thread: thread}}
	}
	add("a", "chat:F1", "示例甲", nil, now.Add(-time.Hour))
	add("b", "chat:F1", "示例乙", &now, now)
	add("c", "chat:F1", "示例丙", &long, long)
	add("d", "chat:F10", "示例丁", nil, now)
	add("e", "chat:F1#t1", "示例戊", nil, now.Add(-2*time.Hour))
	for i := range 12 {
		add(fmt.Sprintf("x%d", i), "chat:F2", "示例", nil, now)
	}
	ids := func(evs []groupEvent) (out []string) {
		for _, e := range evs {
			out = append(out, e.ID)
		}
		return out
	}
	if got := ids(h.groupEvents("lark", "chat:F1", "")); !slices.Equal(got, []string{"a", "e", "b"}) {
		t.Fatalf("events %v", got)
	}
	if got := ids(h.groupEvents("lark", "chat:F1", "乙")); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("query %v", got)
	}
	if got := h.groupEvents("lark", "chat:F2", ""); len(got) != groupEventsMax {
		t.Fatalf("%d listed", len(got))
	}
	if got := h.groupEvents("github", "chat:F1", ""); len(got) != 0 {
		t.Fatalf("another connector's: %v", ids(got))
	}
}

// groupChat is a connector that groups but keeps no meta (no group.append): the hub writes its events. Its tool and
// fallback may be missing (they answer an unknown method).
type groupChat struct {
	mu                 sync.Mutex
	batch              *connector.InboxThing
	noTool, noFallback bool
	tools, fallbacks   int
	fallback           connector.GroupFallbackResult
}

func (*groupChat) ID() string   { return "examplechat" }
func (*groupChat) Name() string { return "Example Chat" }
func (*groupChat) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox, connector.CapGroup}}
}
func (g *groupChat) Collect(context.Context) ([]connector.InboxThing, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.batch == nil {
		return nil, nil
	}
	return []connector.InboxThing{*g.batch}, nil
}
func (*groupChat) Reply(context.Context, connector.InboxReply) error { return nil }
func (g *groupChat) GroupTool(context.Context, connector.GroupToolCall) (json.RawMessage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tools++
	if g.noTool {
		return nil, &rpc.Error{Code: "unknown_method", Message: "unknown method group.tool"}
	}
	return json.RawMessage(`{"messages":[]}`), nil
}
func (g *groupChat) GroupFallback(context.Context, connector.GroupFallback) (connector.GroupFallbackResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fallbacks++
	if g.noFallback {
		return connector.GroupFallbackResult{}, &rpc.Error{Code: "unknown_method", Message: "unknown method group.fallback"}
	}
	return g.fallback, nil
}

// chatBatch is a batch of chat C1: a heading, m2 over two lines (naming m1, which it answers), m3.
func chatBatch() *connector.InboxThing {
	return &connector.InboxThing{Thread: "chat:C1", Kind: KindGroup, Sender: "丁", Preview: "示例 B 的接口改了",
		Group:   &connector.ThingGroup{Batch: "chat:C1@3", New: []string{"m2", "m3"}},
		Context: "示例群\n[10:05] 丙：示例 A 那个周四下午可以 (id=m2, 回复 m1)\n  第二行\n[10:06] 丁：示例 B 的接口改了 (id=m3)",
		At:      time.Now().Add(-time.Minute), Last: "3"}
}

// chatHub is a hub with a connector that keeps no meta and one open event the hub wrote for it before.
func chatHub(t *testing.T, g *groupChat, model *ai.Client) *inboxHub {
	t.Helper()
	h, _ := admitHub(t, model, g)
	it := &InboxItem{ID: "examplechat:chat:C1#m1", Source: "examplechat", Kind: KindGroup, Title: "示例 A 方案评审", Tier: TierAction,
		Last: "1", TriagedAt: "1", TriagedBy: "model", Count: 1, UpdatedAt: time.Now().Add(-time.Hour),
		Context: "示例群\n[10:01] 甲：示例 A 方案什么时候评审 (id=m1)", Group: &InboxGroup{Connector: "examplechat", Thread: "chat:C1"},
		Connector: &InboxConnector{Thread: "chat:C1"}}
	h.inbox.items[it.ID] = it
	return h
}

const chatAnswer = `{"assign":[{"message":"m2","event":"examplechat:chat:C1#m1"},{"message":"m3","event":"new:1"}],
	"events":[{"event":"examplechat:chat:C1#m1","title":"示例 A 方案周四评审","summary":"丙说周四下午可以","tier":"fyi","reason":"同步"},
	{"event":"new:1","title":"示例 B 接口变更","summary":"丁说接口改了","tier":"action","reason":"要跟进"}]}`

// A connector that groups but keeps no meta: the hub writes its events from the batch, with no call to it: a new event
// gets an id of its own and the text of its messages (with the heading), an existing one its new messages after what it
// had, the batch's last and preview; title, summary and tier are the model's; no meta.
func TestInboxGroupWithoutMeta(t *testing.T) {
	g := &groupChat{batch: chatBatch()}
	_, client := newGroupModel(t, chatAnswer)
	h := chatHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	old, fresh := h.inbox.items["examplechat:chat:C1#m1"], h.inbox.items["examplechat:chat:C1#m3"]
	if old.Context != "示例群\n[10:01] 甲：示例 A 方案什么时候评审 (id=m1)\n[10:05] 丙：示例 A 那个周四下午可以 (id=m2, 回复 m1)\n  第二行" ||
		old.Last != "3" || old.Preview != "示例 B 的接口改了" || old.Count != 2 || old.Title != "示例 A 方案周四评审" ||
		old.Tier != TierFYI || old.TriagedBy != "model" || old.Meta != nil {
		t.Fatalf("existing event %+v", old)
	}
	if fresh == nil || fresh.Context != "示例群\n[10:06] 丁：示例 B 的接口改了 (id=m3)" || fresh.Tier != TierAction ||
		fresh.Title != "示例 B 接口变更" || fresh.Connector == nil || fresh.Connector.Thread != "chat:C1" || fresh.Count != 1 ||
		fresh.Group == nil || fresh.Source != "examplechat" || fresh.Meta != nil || fresh.TriagedAt != fresh.Last {
		t.Fatalf("new event %+v", fresh)
	}
	if len(h.inbox.items) != 2 {
		t.Fatalf("%d items", len(h.inbox.items))
	}
}

// A connector without a tool: the model is told it is not there and goes on with what it has.
func TestInboxGroupToolMissing(t *testing.T) {
	g := &groupChat{batch: chatBatch(), noTool: true}
	model, client := newGroupModel(t, `{"tool":"messages_before","args":{"message":"m2","n":5}}`, chatAnswer)
	h := chatHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	calls := model.asked()
	if g.tools != 1 || len(calls) != 2 || !strings.Contains(calls[1].User, "这个工具用不了") {
		t.Fatalf("%d tool call(s), %d model call(s)", g.tools, len(calls))
	}
	if h.inbox.items["examplechat:chat:C1#m3"] == nil {
		t.Fatal("not related after the missing tool")
	}
}

// A connector without a fallback: with the model down the batch waits (nothing written) and comes in once the model
// is back, as an inbox item without a fallback does.
func TestInboxGroupWithoutFallbackWaits(t *testing.T) {
	g := &groupChat{batch: chatBatch(), noFallback: true}
	model, client := newGroupModel(t, chatAnswer)
	model.down = true
	h := chatHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if g.fallbacks != 1 || len(h.inbox.items) != 1 || h.inbox.items["examplechat:chat:C1#m1"].Last != "1" {
		t.Fatalf("written without the model or a fallback: %d fallback call(s), %d items", g.fallbacks, len(h.inbox.items))
	}
	model.mu.Lock()
	model.down = false
	model.mu.Unlock()
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if h.inbox.items["examplechat:chat:C1#m3"] == nil {
		t.Fatal("not written once the model is back")
	}
}

// What a batch's text gives each event: the lines of its messages (an id as a word of its own: m1 is not m10; id=<id>,
// else the last id a line names: an answer naming what it answers is still its own), continuation lines with them, the
// heading with new events; a text naming none is the whole text.
func TestInboxEventText(t *testing.T) {
	text := "群：示例\n[1] 甲：一 (m1)\n  接着说\n[2] 乙：二，回 m1 (m10)\n[3] 丙：三 (id=m2, 回复 m10)"
	all := []string{"m1", "m10", "m2"}
	for _, c := range []struct {
		mine []string
		head bool
		want string
	}{
		{[]string{"m1"}, true, "群：示例\n[1] 甲：一 (m1)\n  接着说"},
		{[]string{"m10"}, false, "[2] 乙：二，回 m1 (m10)"},
		{[]string{"m1", "m2"}, false, "[1] 甲：一 (m1)\n  接着说\n[3] 丙：三 (id=m2, 回复 m10)"},
	} {
		if got := eventText(text, all, c.mine, c.head); got != c.want {
			t.Errorf("%v: %q", c.mine, got)
		}
	}
	if got := eventText("没有 id 的文字", all, []string{"m1"}, true); got != "没有 id 的文字" {
		t.Errorf("no ids: %q", got)
	}
}

// The hub keeps an event's count: a new one starts at the messages put in it and every later batch adds its own; what
// the connector says (its meta may keep only the newest messages) is not read. The event's start stays.
func TestInboxGroupEventCount(t *testing.T) {
	batch := func(last string, msgs ...string) *connector.InboxThing {
		return &connector.InboxThing{Thread: "chat:F1", Group: &connector.ThingGroup{Batch: "chat:F1@" + last, New: msgs},
			Context: "示例：" + strings.Join(msgs, "、"), Last: last}
	}
	assign := func(event string, msgs ...string) string {
		var a []string
		for _, m := range msgs {
			a = append(a, fmt.Sprintf(`{"message":%q,"event":%q}`, m, event))
		}
		return `{"assign":[` + strings.Join(a, ",") + `],"events":[{"event":"` + event + `","title":"示例","summary":"示例","tier":"fyi"}]}`
	}
	g := &groupLark{batch: batch("1061", "m1060", "m1061"), placed: map[string]bool{}, count: 99}
	_, client := newGroupModel(t, assign("new:1", "m1060", "m1061"), assign("lark:F1:1060", "m1062"), assign("lark:F1:1060", "m1063", "m1064"))
	h, _ := groupHub(t, g, client)
	round := func(next *connector.InboxThing) *InboxItem {
		t.Helper()
		h.collectInbox(context.Background(), roundPoll)
		h.waitGrouping()
		g.mu.Lock()
		g.batch = next
		g.mu.Unlock()
		return h.inbox.items["lark:F1:1060"]
	}
	it := round(batch("1062", "m1062"))
	if it == nil || it.Count != 2 {
		t.Fatalf("new event: %+v", it)
	}
	started := it.StartedAt
	if it = round(batch("1064", "m1063", "m1064")); it.Count != 3 || !it.StartedAt.Equal(started) {
		t.Fatalf("after one more: count %d, started %v (was %v)", it.Count, it.StartedAt, started)
	}
	if it = round(nil); it.Count != 5 || !it.StartedAt.Equal(started) || it.Last != "1064" {
		t.Fatalf("after two more: count %d, last %s", it.Count, it.Last)
	}
}
