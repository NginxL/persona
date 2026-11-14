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

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// An open "Needs you" item the model sorted under older criteria is sorted again once, and only that one: not one
// sorted under the current criteria, not FYI, not one its conversation handles, not one a policy sets, and not twice.
func TestInboxRetriagesStaleNeedsYouOnce(t *testing.T) {
	m, client := newTriageModel(t, "ok", `{"tier":"fyi","summary":"示例：群里在问别人","reason":"不是找你"}`)
	h, st := admitHub(t, client)
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	add := func(id string, change func(*InboxItem)) *InboxItem {
		it := &InboxItem{ID: id, Source: "lark", Kind: KindGroup, Title: "示例 " + id, Summary: "示例旧总结", Tier: TierAction, Reason: "示例",
			Context: "示例群：丙：@丁 帮忙看下", Last: "10", TriagedAt: "10", UpdatedAt: now, StartedAt: now,
			Lark: &InboxLark{FeedID: "F" + id, IsGroup: true}}
		if change != nil {
			change(it)
		}
		h.inbox.items[id] = it
		return it
	}
	stale := add("a", nil)
	current := add("b", func(it *InboxItem) { it.TriageRev = inboxTriageRev })
	fyi := add("c", func(it *InboxItem) { it.Tier = TierFYI })
	handled := add("d", func(it *InboxItem) { it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking} })
	policied := add("e", func(it *InboxItem) { it.Lark.SenderID = "u9" })
	setPolicyFor(t, h, InboxPolicy{Source: "lark", Tag: "sender", Value: "u9", Name: "示例同事", Tier: TierAction})

	h.triageInbox(context.Background())
	h.inbox.triaging.Lock()
	h.inbox.triaging.Unlock() //nolint:staticcheck // waiting for the pass

	if n := len(m.asked()); n != 1 {
		t.Fatalf("%d triage calls, want 1 (the stale one)", n)
	}
	if stale.Tier != TierFYI || stale.Reason != "不是找你" || stale.TriageRev != inboxTriageRev || stale.Summary != "示例：群里在问别人" || !st.saved("a") {
		t.Fatalf("stale item: %+v", stale)
	}
	if last := stale.Chain[len(stale.Chain)-1]; last.Kind != ChainTriaged {
		t.Fatalf("chain %+v, want a triaged entry last", stale.Chain)
	}
	for _, it := range []*InboxItem{current, handled, policied} {
		if it.Tier != TierAction {
			t.Errorf("%s: tier %q", it.ID, it.Tier)
		}
	}
	if fyi.Tier != TierFYI {
		t.Errorf("fyi: tier %q", fyi.Tier)
	}

	// Sorted again, the stale item is not asked about again; a failed try is not retried in the same run.
	stale.Tier, stale.TriageRev = TierAction, 0
	h.triageInbox(context.Background())
	h.inbox.triaging.Lock()
	h.inbox.triaging.Unlock() //nolint:staticcheck // waiting for the pass
	if n := len(m.asked()); n != 1 {
		t.Fatalf("%d triage calls after a second pass, want 1", n)
	}
}

// A model that fails on a stale item leaves it as it was: no rule tier replaces the model's earlier one.
func TestInboxRetriageFailureKeepsTheTier(t *testing.T) {
	_, client := newTriageModel(t, "fail", "")
	h, _ := admitHub(t, client)
	it := &InboxItem{ID: "lark:F1:1", Source: "lark", Kind: KindDM, Title: "示例", Summary: "示例总结", Tier: TierAction, Reason: "示例理由",
		Context: "示例：在吗", Last: "1", TriagedAt: "1", UpdatedAt: time.Now(), Lark: &InboxLark{FeedID: "F1"}}
	h.inbox.items[it.ID] = it
	h.triageInbox(context.Background())
	h.inbox.triaging.Lock()
	h.inbox.triaging.Unlock() //nolint:staticcheck // waiting for the pass
	if it.Tier != TierAction || it.Reason != "示例理由" || it.Summary != "示例总结" || it.TriageRev != 0 {
		t.Fatalf("item %+v", it)
	}
}

// The grouping model may say a batch closed an open event: "over" files it done_by manager with a done entry saying
// so, "replied" files it as replied; never one it keeps in "Needs you", and never one its conversation handles.
func TestInboxGroupSettledFilesTheEvent(t *testing.T) {
	for _, tc := range []struct {
		name, settled, tier, doneBy, kind string
	}{
		{"over", SettledOver, TierFYI, doneByManager, ChainDone},
		{"replied", SettledReplied, TierNoise, "replied", ChainSelfReplied},
		{"still needs you", SettledOver, TierAction, "", ""},
		{"unknown value", "maybe", TierFYI, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := `{"assign":[{"message":"m1060","event":"lark:F1:1051"},{"message":"m1061","event":"lark:F1:1051"}],
				"events":[{"event":"lark:F1:1051","title":"示例 A 方案评审","summary":"丙说好了","tier":"` + tc.tier + `","reason":"对方说好了","settled":"` + tc.settled + `"}]}`
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
			_, client := newGroupModel(t, answer)
			h, _ := groupHub(t, g, client)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()
			it := h.inbox.items["lark:F1:1051"]
			if tc.doneBy == "" {
				if !it.open() {
					t.Fatalf("filed: %+v", it)
				}
				return
			}
			if it.open() || it.DoneBy != tc.doneBy || it.SettledOf != it.Last {
				t.Fatalf("item %+v", it)
			}
			last := it.Chain[len(it.Chain)-1]
			if last.Kind != tc.kind || last.Detail != "对方说好了" {
				t.Fatalf("last chain entry %+v", last)
			}
			if tc.kind == ChainDone && (last.By != doneByManager || last.Text != i18n.T(h.inbox.userLang(), "assistant-inbox_chain.done_over")) {
				t.Fatalf("done entry %+v", last)
			}
		})
	}
}

// settleLark is the Lark plugin's connector as the handled-elsewhere pass reads it: messages_around answers what the
// test set, and the calls are kept.
type settleLark struct {
	groupLark
	mu     sync.Mutex
	answer string
	asks   []connector.GroupToolCall
}

func (c *settleLark) GroupTool(_ context.Context, call connector.GroupToolCall) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asks = append(c.asks, call)
	return json.RawMessage(c.answer), nil
}

func (c *settleLark) set(answer string) {
	c.mu.Lock()
	c.answer = answer
	c.mu.Unlock()
}

// settleModel answers the settle prompt with reply and counts those calls; any other prompt gets the judge's "no".
func settleModel(t *testing.T, reply string) (*int, *sync.Mutex, *ai.Client) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			System string `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		text := `{"handle":false,"reason":"示例"}`
		if strings.Contains(in.System, "还要不要用户管") {
			mu.Lock()
			n++
			mu.Unlock()
			text = reply
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
	}))
	t.Cleanup(srv.Close)
	return &n, &mu, modelAt(t, srv.URL)
}

// settleHub is a hub with the connector, the model and one open "Needs you" event of chat F1 (group or direct),
// whose messages are m1051 (position 1051, its newest from someone else) and m1050.
func settleHub(t *testing.T, c *settleLark, model *ai.Client, group bool) (*inboxHub, *InboxItem) {
	t.Helper()
	h, _ := admitHub(t, model, c)
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	kind := KindDM
	if group {
		kind = KindGroup
	}
	it := &InboxItem{ID: "lark:F1:1050", Source: "lark", Kind: kind, Title: "示例：问部署时间", Summary: "丙问什么时候部署", Tier: TierAction,
		Reason: "示例", Context: "示例：丙：什么时候部署？", Last: "1051", TriagedAt: "1051", TriagedBy: "model", UpdatedAt: time.Now(),
		Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"},
		Lark:  &InboxLark{AccountID: 1, FeedID: "F1", IsGroup: group, StartPosition: "1050", ReplyTo: "m1051"}}
	h.inbox.items[it.ID] = it
	return h, it
}

func (h *inboxHub) settleAndWait() {
	h.inbox.mu.Lock()
	h.inbox.settlePass = time.Time{} // passes are spaced a minute apart: each call here is a pass
	h.inbox.mu.Unlock()
	h.settleInbox(context.Background())
	h.inbox.settling.Lock()
	h.inbox.settling.Unlock() //nolint:staticcheck // waiting for the pass
}

// The owner's own message after the event in a direct chat files it as replied, without the model; reopened by
// hand, the same message never files it again, and a newer one does.
func TestInboxSettleDirectReply(t *testing.T) {
	c := &settleLark{groupLark: groupLark{placed: map[string]bool{}}}
	c.set(`{"messages":[{"id":"m1050","pos":"1050","sender":"丙","text":"示例：在吗"},{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},
		{"id":"m1052","pos":"1052","sender":"我","text":"示例：下午三点","mine":true}]}`)
	n, mu, model := settleModel(t, `{"state":"over","reason":"示例"}`)
	h, it := settleHub(t, c, model, false)
	h.settleAndWait()

	if it.open() || it.DoneBy != "replied" || it.SettledOf != "1052" {
		t.Fatalf("item %+v", it)
	}
	last := it.Chain[len(it.Chain)-1]
	if last.Kind != ChainSelfReplied || last.Link == nil || last.Link.Position != "1052" || last.Via != "lark" {
		t.Fatalf("chain entry %+v", last)
	}
	if !h.st.saved(it.ID) {
		t.Fatal("not saved")
	}
	if len(c.asks) != 1 || c.asks[0].Name != connector.GroupMessagesAround || c.asks[0].Thread != "chat:F1" || !strings.Contains(string(c.asks[0].Args), `"message":"m1051"`) {
		t.Fatalf("asks %+v", c.asks)
	}
	mu.Lock()
	if *n != 0 {
		t.Fatalf("%d model calls for a direct reply", *n)
	}
	mu.Unlock()

	// Reopened by hand: the same reply doesn't file it again.
	h.inbox.mu.Lock()
	it.DoneAt, it.DoneBy = nil, ""
	h.inbox.settleAsked = map[string]time.Time{}
	h.inbox.mu.Unlock()
	h.settleAndWait()
	if !it.open() {
		t.Fatalf("filed again on the same reply: %+v", it)
	}
	// A newer message of the owner's does.
	c.set(`{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},{"id":"m1052","pos":"1052","sender":"我","text":"示例：下午三点","mine":true},
		{"id":"m1053","pos":"1053","sender":"我","text":"示例：已经部署了","mine":true}]}`)
	h.inbox.mu.Lock()
	h.inbox.settleAsked = map[string]time.Time{}
	h.inbox.mu.Unlock()
	h.settleAndWait()
	if it.open() || it.DoneBy != "replied" || it.SettledOf != "1053" {
		t.Fatalf("after a newer reply: %+v", it)
	}
}

// In a group, the owner's message quoting the event's is a reply (no model); one that quotes nothing goes to the
// model, which files it as over or leaves it open; nothing after the event asks nothing.
func TestInboxSettleGroup(t *testing.T) {
	for _, tc := range []struct {
		name, answer, reply string
		calls               int
		doneBy              string
		change              func(*InboxItem)
	}{
		{"quoted", `{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},
			{"id":"m1060","pos":"1060","sender":"我","text":"示例：下午","reply_to":"m1051","mine":true}]}`, `{"state":"open"}`, 0, "replied", nil},
		{"model over", `{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},
			{"id":"m1060","pos":"1060","sender":"丁","text":"示例：已经部署好了，不用等了"}]}`, `{"state":"over","reason":"已部署"}`, 1, doneByManager, nil},
		{"model open", `{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},
			{"id":"m1060","pos":"1060","sender":"我","text":"示例：午饭吃啥","mine":true}]}`, `{"state":"open","reason":"无关"}`, 1, "", nil},
		{"nothing after", `{"messages":[{"id":"m1050","pos":"1050","sender":"丙","text":"示例：在吗"},
			{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"}]}`, `{"state":"over"}`, 0, "", nil},
		{"cut answer", `{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},{"id":"m1060","pos":"1060","sender":"我","text":"示例`,
			`{"state":"over"}`, 0, "", nil},
		// The event's context names its messages: a quote of another event's message between them is not a reply to it.
		{"quotes another event", `{"messages":[{"id":"m1045","pos":"1045","sender":"丁","text":"示例：另一件事"},{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},
			{"id":"m1060","pos":"1060","sender":"我","text":"示例：好","reply_to":"m1045","mine":true}]}`, `{"state":"open"}`, 1, "",
			func(it *InboxItem) {
				it.Lark.StartPosition = "1040"
				it.Context = "飞书群「示例」里的一件事：\n[11-10 09:00] 丙：示例：在吗 (id=m1040)\n[11-10 09:05] 丙：示例：什么时候部署？ (id=m1051)"
			}},
		{"quotes the event", `{"messages":[{"id":"m1040","pos":"1040","sender":"丙","text":"示例：在吗"},{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},
			{"id":"m1060","pos":"1060","sender":"我","text":"示例：好","reply_to":"m1040","mine":true}]}`, `{"state":"open"}`, 0, "replied",
			func(it *InboxItem) {
				it.Lark.StartPosition = "1040"
				it.Context = "飞书群「示例」里的一件事：\n[11-10 09:00] 丙：示例：在吗 (id=m1040)\n[11-10 09:05] 丙：示例：什么时候部署？ (id=m1051)"
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &settleLark{groupLark: groupLark{placed: map[string]bool{}}}
			c.set(tc.answer)
			n, mu, model := settleModel(t, tc.reply)
			h, it := settleHub(t, c, model, true)
			if tc.change != nil {
				tc.change(it)
			}
			h.settleAndWait()
			mu.Lock()
			calls := *n
			mu.Unlock()
			if calls != tc.calls {
				t.Fatalf("%d model calls, want %d", calls, tc.calls)
			}
			if tc.doneBy == "" {
				if !it.open() {
					t.Fatalf("filed: %+v", it)
				}
				return
			}
			if it.open() || it.DoneBy != tc.doneBy {
				t.Fatalf("item %+v", it)
			}
		})
	}
}

// What the pass leaves alone: FYI, an item its conversation handles, one with no anchor, one asked about recently.
func TestInboxSettleSkips(t *testing.T) {
	c := &settleLark{groupLark: groupLark{placed: map[string]bool{}}}
	c.set(`{"messages":[{"id":"m1052","pos":"1052","sender":"我","text":"示例：好","mine":true}]}`)
	_, _, model := settleModel(t, `{"state":"over"}`)
	h, it := settleHub(t, c, model, false)
	for _, change := range []func(*InboxItem){
		func(it *InboxItem) { it.Tier = TierFYI },
		func(it *InboxItem) { it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking} },
		func(it *InboxItem) { it.Lark.ReplyTo = "" },
		func(it *InboxItem) { it.Group = nil },
	} {
		h.inbox.mu.Lock()
		saved := *it
		lark := *it.Lark
		change(it)
		h.inbox.settleAsked = map[string]time.Time{}
		h.inbox.mu.Unlock()
		h.settleAndWait()
		h.inbox.mu.Lock()
		if !it.open() {
			t.Fatalf("filed: %+v", it)
		}
		*it = saved
		it.Lark = &lark
		h.inbox.mu.Unlock()
	}
	if len(c.asks) != 0 {
		t.Fatalf("asked %d times", len(c.asks))
	}
	// Asked once, the item isn't asked again within settleEvery.
	h.settleAndWait()
	c.set(`{"messages":[]}`)
	h.inbox.mu.Lock()
	it.DoneAt, it.DoneBy = nil, ""
	h.inbox.mu.Unlock()
	h.settleAndWait()
	if len(c.asks) != 1 {
		t.Fatalf("asked %d times, want 1", len(c.asks))
	}
}

func TestSettleMessagesReadsWhatIsWhole(t *testing.T) {
	got := settleMessages(`{"more":true,"messages":[{"id":"a","pos":"1","mine":true},{"id":"b","pos":"2","text":"示例`)
	if len(got) != 1 || got[0].ID != "a" || !got[0].Mine {
		t.Fatalf("%+v", got)
	}
	if got := settleMessages(`not json`); got != nil {
		t.Fatalf("%+v", got)
	}
	if !positionAfter("1000", "999") || positionAfter("999", "1000") || positionAfter("x", "1") || positionAfter("1", "") {
		t.Fatal("positionAfter")
	}
}

// The model said "open" to what came after an item: the next look asks again only once something newer came.
func TestInboxSettleAsksTheModelOncePerNews(t *testing.T) {
	c := &settleLark{groupLark: groupLark{placed: map[string]bool{}}}
	c.set(`{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},{"id":"m1060","pos":"1060","sender":"丁","text":"示例：我也想知道"}]}`)
	n, mu, model := settleModel(t, `{"state":"open"}`)
	h, it := settleHub(t, c, model, true)
	look := func() int {
		h.inbox.mu.Lock()
		h.inbox.settleAsked = map[string]time.Time{}
		h.inbox.mu.Unlock()
		h.settleAndWait()
		mu.Lock()
		defer mu.Unlock()
		return *n
	}
	if got := look(); got != 1 {
		t.Fatalf("%d model calls, want 1", got)
	}
	if got := look(); got != 1 {
		t.Fatalf("%d model calls on nothing new, want 1", got)
	}
	c.set(`{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},{"id":"m1060","pos":"1060","sender":"丁","text":"示例：我也想知道"},
		{"id":"m1061","pos":"1061","sender":"丙","text":"示例：好了，已经部署了"}]}`)
	if got := look(); got != 2 || !it.open() {
		t.Fatalf("%d model calls after news, open %v", got, it.open())
	}
	// A second pass within a minute of the last does nothing.
	h.inbox.mu.Lock()
	h.inbox.settleAsked = map[string]time.Time{}
	h.inbox.settlePass = time.Now()
	h.inbox.mu.Unlock()
	asks := len(c.asks)
	h.settleInbox(context.Background())
	h.inbox.settling.Lock()
	h.inbox.settling.Unlock() //nolint:staticcheck // waiting for a pass, if one started
	if len(c.asks) != asks {
		t.Fatalf("a pass ran within a minute of the last")
	}
}

// The settle prompt as the model reads it (testdata/prompts/inbox_settled.*.golden; UPDATE_GOLDEN=1 writes them).
func TestGoldenInboxSettled(t *testing.T) {
	golden(t, "inbox_settled.system", prompts.RenderInboxSettled(prompts.InboxSettled{Owner: "示例用户"}))
	golden(t, "inbox_settled.user", prompts.RenderInboxSettledInput(prompts.InboxSettledInput{Title: "示例：问部署时间", Summary: "丙问什么时候部署",
		Context: "飞书群「示例」里的一件事：\n[11-10 09:05] 丙：什么时候部署？ (id=m1051)",
		Later:   []prompts.InboxSettledLine{{At: "11-10 09:20", Sender: "丁", Text: "已经部署好了"}, {Sender: "我", Text: "好的", Mine: true}}}))
}

// Grouping is told who the other side of a direct chat is (the connector's words for the kind): a bot's "在问你" is not
// a person asking. A group batch gets no such line; its messages carry their mention markers.
func TestInboxGroupInputNamesTheDirectChat(t *testing.T) {
	for _, kind := range []string{KindBot, KindGroup} {
		t.Run(kind, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
			model, client := newGroupModel(t, groupAnswerOK)
			h, _ := groupHub(t, g, client)
			setGroupConversationKind(g, h, kind)
			h.inbox.srcMu.Lock()
			h.inbox.kinds["lark/"+KindBot] = "示例：机器人或应用发来的私聊"
			h.inbox.kinds["lark/"+KindGroup] = "示例：群消息"
			h.inbox.srcMu.Unlock()
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()
			calls := model.asked()
			if len(calls) == 0 {
				t.Fatal("no grouping call")
			}
			named := strings.Contains(calls[0].User, "会话：chat:F1（示例：机器人或应用发来的私聊）")
			if named != (kind == KindBot) || strings.Contains(calls[0].User, "示例：群消息") {
				t.Fatalf("input:\n%s", calls[0].User)
			}
		})
	}
}

// Review fixes (#7986): what the re-sort leaves alone, and a model that isn't there gives the try back.
func TestInboxRetriageSkipsAndRetries(t *testing.T) {
	m, client := newTriageModel(t, "busy", `{"tier":"fyi","summary":"示例","reason":"示例"}`)
	h, _ := admitHub(t, client)
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	add := func(id string, change func(*InboxItem)) *InboxItem {
		it := &InboxItem{ID: id, Source: "lark", Kind: KindGroup, Title: "示例 " + id, Tier: TierAction, Reason: "示例", Context: "示例群：丙：在吗",
			Last: "10", TriagedAt: "10", UpdatedAt: now, Lark: &InboxLark{FeedID: "F" + id, IsGroup: true}}
		if change != nil {
			change(it)
		}
		h.inbox.items[id] = it
		return it
	}
	stale := add("a", nil)
	add("failed", func(it *InboxItem) { it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingFailed} })
	add("due", func(it *InboxItem) { it.Follow = &InboxFollow{Due: true} })
	add("agent", func(it *InboxItem) { it.Agent = &InboxAgent{State: AgentSkipped} })
	add("github", func(it *InboxItem) {
		it.Source, it.Lark = "github", nil
		it.Connector = &InboxConnector{Thread: "example-org/example-repo#1", RuleTier: TierAction}
	})
	pass := func() {
		h.triageInbox(context.Background())
		h.inbox.triaging.Lock()
		h.inbox.triaging.Unlock() //nolint:staticcheck // waiting for the pass
	}
	pass()
	if n := len(m.asked()); n != 1 || !strings.Contains(m.asked()[0].User, "在吗") {
		t.Fatalf("%d calls, want only the stale one", n)
	}
	if stale.Tier != TierAction || h.inbox.retriaged[stale.ID] {
		t.Fatalf("a busy model: item %+v, tried %v", stale, h.inbox.retriaged[stale.ID])
	}
	back, client2 := newTriageModel(t, "ok", `{"tier":"fyi","summary":"示例","reason":"示例"}`)
	h.in.mu.Lock()
	h.in.model = client2 // the model is back (a fresh connection: the busy one stays held a while)
	h.in.mu.Unlock()
	pass()
	if stale.Tier != TierFYI || len(back.asked()) != 1 {
		t.Fatalf("model back: tier %q after %d calls", stale.Tier, len(back.asked()))
	}
}

// A "settled" the model writes as something other than a string is ignored, not a reason to throw the answer away;
// and a new event is never filed on arrival.
func TestInboxGroupSettledOddValues(t *testing.T) {
	answer := `{"assign":[{"message":"m1060","event":"lark:F1:1051"},{"message":"m1061","event":"new:1"}],
		"events":[{"event":"lark:F1:1051","title":"示例 A","summary":"示例","tier":"fyi","reason":"示例","settled":false},
		{"event":"new:1","title":"示例 B","summary":"示例","tier":"fyi","reason":"示例","settled":"over"}]}`
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	model, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if n := len(model.asked()); n != 1 {
		t.Fatalf("%d grouping calls: the answer was re-asked", n)
	}
	if _, _, fallbacks := g.counts(); fallbacks != 0 {
		t.Fatal("went to the fallback")
	}
	for id, it := range h.inbox.items {
		if id != "lark:F1:900" && !it.open() {
			t.Fatalf("%s filed: %+v", id, it)
		}
	}
}

// After a hand reopen, a newer message that isn't the owner's doesn't file the item again on the old reply; in a
// direct chat with another open event in between, the owner's message goes to the model; a recalled one is nothing;
// "replied" with no message of the owner's is "over"; a failed model call is asked again.
func TestInboxSettleReviewCases(t *testing.T) {
	t.Run("reopened, newer message from the other side", func(t *testing.T) {
		c := &settleLark{groupLark: groupLark{placed: map[string]bool{}}}
		c.set(`{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},{"id":"m1052","pos":"1052","sender":"我","text":"示例：下午","mine":true},
			{"id":"m1053","pos":"1053","sender":"丙","text":"示例：好的，那我等你"}]}`)
		n, mu, model := settleModel(t, `{"state":"open"}`)
		h, it := settleHub(t, c, model, false)
		h.inbox.mu.Lock()
		it.SettledOf = "1052" // filed on the reply, then reopened by hand
		h.inbox.mu.Unlock()
		h.settleAndWait()
		mu.Lock()
		calls := *n
		mu.Unlock()
		if !it.open() || calls != 1 {
			t.Fatalf("open %v, %d model calls: want left open after the model saw only 1053", it.open(), calls)
		}
	})
	t.Run("direct chat, another open event in between", func(t *testing.T) {
		c := &settleLark{groupLark: groupLark{placed: map[string]bool{}}}
		c.set(`{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},{"id":"m1055","pos":"1055","sender":"丙","text":"示例：另外，周报呢"},
			{"id":"m1056","pos":"1056","sender":"我","text":"示例：周报晚点发","mine":true}]}`)
		n, mu, model := settleModel(t, `{"state":"open"}`)
		h, it := settleHub(t, c, model, false)
		h.inbox.mu.Lock()
		h.inbox.items["lark:F1:1055"] = &InboxItem{ID: "lark:F1:1055", Source: "lark", Kind: KindDM, Tier: TierFYI, Last: "1055",
			Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"}, Lark: &InboxLark{FeedID: "F1", ReplyTo: "m1055"}}
		h.inbox.mu.Unlock()
		h.settleAndWait()
		mu.Lock()
		calls := *n
		mu.Unlock()
		if !it.open() || calls != 1 {
			t.Fatalf("open %v after %d model calls: the reply may answer the other event, so the model decides", it.open(), calls)
		}
	})
	t.Run("recalled", func(t *testing.T) {
		c := &settleLark{groupLark: groupLark{placed: map[string]bool{}}}
		c.set(`{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},{"id":"m1052","pos":"1052","sender":"我","text":"(撤回了一条消息)","mine":true}]}`)
		n, mu, model := settleModel(t, `{"state":"over"}`)
		h, it := settleHub(t, c, model, false)
		h.settleAndWait()
		mu.Lock()
		calls := *n
		mu.Unlock()
		if !it.open() || calls != 0 {
			t.Fatalf("open %v, %d model calls", it.open(), calls)
		}
	})
	t.Run("replied without the owner", func(t *testing.T) {
		c := &settleLark{groupLark: groupLark{placed: map[string]bool{}}}
		c.set(`{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},{"id":"m1060","pos":"1060","sender":"丁","text":"示例：我来答，下午部署"}]}`)
		_, _, model := settleModel(t, `{"state":"replied","reason":"已答复"}`)
		h, it := settleHub(t, c, model, true)
		h.settleAndWait()
		if it.open() || it.DoneBy != doneByManager {
			t.Fatalf("item %+v, want done_by manager", it)
		}
	})
	t.Run("failed call asked again", func(t *testing.T) {
		c := &settleLark{groupLark: groupLark{placed: map[string]bool{}}}
		c.set(`{"messages":[{"id":"m1051","pos":"1051","sender":"丙","text":"示例：什么时候部署？"},{"id":"m1060","pos":"1060","sender":"丁","text":"示例：我也想知道"}]}`)
		n, mu, model := settleModel(t, `not json at all`)
		h, it := settleHub(t, c, model, true)
		for range 2 {
			h.inbox.mu.Lock()
			h.inbox.settleAsked = map[string]time.Time{}
			h.inbox.mu.Unlock()
			h.settleAndWait()
		}
		mu.Lock()
		calls := *n
		mu.Unlock()
		if !it.open() || calls < 2 {
			t.Fatalf("open %v, %d model calls: a failed call must be asked again", it.open(), calls)
		}
	})
}

// A settled event keeps its own slot when the batch also opens a noise event: noise folding doesn't swallow it.
func TestInboxGroupSettledSurvivesNoiseFolding(t *testing.T) {
	answer := `{"assign":[{"message":"m1060","event":"new:1"},{"message":"m1061","event":"lark:F1:1051"}],
		"events":[{"event":"new:1","title":"示例闲聊","summary":"示例","tier":"noise","reason":"闲聊"},
		{"event":"lark:F1:1051","title":"示例 A","summary":"丙说不用了","tier":"noise","reason":"对方说不用了","settled":"over"}]}`
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	_, client := newGroupModel(t, answer)
	h, _ := groupHub(t, g, client)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	if it := h.inbox.items["lark:F1:1051"]; it.open() || it.DoneBy != doneByManager {
		t.Fatalf("settled event %+v", it)
	}
}

// What the manager filed an event on counts as the connector's reply report at or before that position: reopened by
// hand, a repeated report doesn't file it again; a report at a newer position, or mail / to-do evidence, doesn't count.
func TestGroupStateHasSelfReplyReadsSettledOf(t *testing.T) {
	it := &InboxItem{SettledOf: "1052"}
	if !groupStateHasSelfReply(it, "1051") || !groupStateHasSelfReply(it, "1052") || groupStateHasSelfReply(it, "1053") {
		t.Fatal("conversation evidence")
	}
	for _, ev := range []string{"todo:t1", "mail:7", ""} {
		if groupStateHasSelfReply(&InboxItem{SettledOf: ev}, "1") {
			t.Fatalf("%q counted", ev)
		}
	}
}
