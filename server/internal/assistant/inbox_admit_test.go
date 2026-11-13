package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/connector"
)

// extensionLark is the plugin that took Feishu over (connector lark), on made-up things.
type extensionLark struct {
	things []connector.InboxThing
	got    []connector.InboxReply
}

func (*extensionLark) ID() string   { return "lark" }
func (*extensionLark) Name() string { return "飞书" }
func (*extensionLark) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox}, Kinds: []string{KindDM, KindDoc}}
}
func (c *extensionLark) Collect(context.Context) ([]connector.InboxThing, error) {
	return c.things, nil
}
func (c *extensionLark) Reply(_ context.Context, r connector.InboxReply) error {
	c.got = append(c.got, r)
	return nil
}

// triageModelStub is a triage model whose answer the test switches: ok (reply), fail (HTTP 500), quota (429, quota
// used up, back after a second), busy (503, no channel for the model), gone (404), slow (answers after delay). It
// keeps the triage requests (not the judge's).
type triageModelStub struct {
	mode  atomic.Value // string
	reply atomic.Value // string
	delay time.Duration
	mu    sync.Mutex
	calls []capturedCall
}

func newTriageModel(t *testing.T, mode, reply string) (*triageModelStub, *ai.Client) {
	t.Helper()
	m := &triageModelStub{}
	m.mode.Store(mode)
	m.reply.Store(reply)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			System   string `json:"system"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !strings.Contains(in.System, "替用户分拣") { // the judge (model 2): nothing to hand over
			_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"handle":false,"reason":"示例"}`}}})
			return
		}
		c := capturedCall{System: in.System}
		if len(in.Messages) > 0 {
			c.User = in.Messages[0].Content
		}
		m.mu.Lock()
		m.calls = append(m.calls, c)
		m.mu.Unlock()
		switch m.mode.Load().(string) {
		case "fail":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"example failure"}}`))
			return
		case "quota":
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","code":"account_quota_exhausted","message":"Account usage limit reached"}}`))
			return
		case "busy":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"type":"service_unavailable","message":"no channel is available for the model"}}`))
			return
		case "gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"not_found_error","message":"model: m"}}`))
			return
		case "slow":
			time.Sleep(m.delay)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": m.reply.Load().(string)}}})
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	return m, ai.New(ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "m"})
}

func (m *triageModelStub) asked() []capturedCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]capturedCall(nil), m.calls...)
}

// admitHub is an assistant with an inbox, a store that records writes, a triage model and the given connectors (a
// plugin's each).
func admitHub(t *testing.T, model *ai.Client, cs ...connector.Connector) (*inboxHub, *inboxMemStore) {
	t.Helper()
	h := newInboxHub(t)
	h.in.model = model
	for _, c := range cs {
		h.addConnector(c, true)
	}
	return h, h.st
}

// collectAndAdmit is one round, and the triage of its new plugin items (in the background) done.
func collectAndAdmit(h *inboxHub) {
	h.collectInbox(context.Background(), roundPoll)
	h.inbox.admitting.Lock()
	h.inbox.admitting.Unlock() //nolint:staticcheck // waiting for the admission
}

// genericThing is a connector's thing its rules leave to the model (no tier: no fallback).
func genericThing(thread, last, text string) connector.InboxThing {
	return connector.InboxThing{Thread: thread, Kind: "review_requested", Title: "示例 " + thread, Sender: "bob", Preview: text,
		Context: text, At: time.Now().Add(-time.Minute), Last: last}
}

func itemOf(h *inboxHub, source, thread string) *InboxItem {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	for _, it := range h.inbox.items {
		if it.Source == source && it.Connector != nil && it.Connector.Thread == thread {
			return it
		}
	}
	return nil
}

// inboxTriageOf is GET /api/v1/inbox?sources=all: the items and the triage status.
func inboxTriageOf(t *testing.T, h *inboxHub) ([]any, InboxTriage) {
	t.Helper()
	code, out := inboxCall(t, h.routes(), "GET", "/api/v1/inbox?sources=all", "")
	if code != 200 {
		t.Fatalf("GET /api/v1/inbox: %d", code)
	}
	var st InboxTriage
	raw, _ := json.Marshal(out["triage"])
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("triage %s: %v", raw, err)
	}
	items, _ := out["items"].([]any)
	return items, st
}

// A new item the model can't sort and that has no fallback tier is not written (not listed, not stored); the status
// says how many wait and why, recomputed each round; the next round asks again, and once the model works it comes in.
func TestInboxHoldsNewItemsWithoutFallback(t *testing.T) {
	model, client := newTriageModel(t, "quota", `{"tier":"action","summary":"bob 请你看 PR","reason":"请你 review"}`)
	th := genericThing("example-org/example-repo#3", "a", "示例：请你看一下这个 PR")
	c := &inboxConnector{things: []connector.InboxThing{th}}
	h, st := admitHub(t, client, c)

	collectAndAdmit(h)
	if it := itemOf(h, "example", th.Thread); it != nil {
		t.Fatalf("an untriaged item without a fallback came in: %+v", it)
	}
	items, status := inboxTriageOf(t, h)
	want := InboxTriageSource{Source: "example", Name: "Example", State: triagePaused, Reason: "模型没有额度", Waiting: 1}
	if len(items) != 0 || status.State != triagePaused || status.Reason != "模型没有额度" || status.Waiting != 1 ||
		len(status.Sources) != 1 || status.Sources[0] != want {
		t.Fatalf("%d items, triage %+v", len(items), status)
	}
	for id := range st.ids {
		t.Fatalf("stored %s", id)
	}

	// The next round (the model still held): the same thing again, still one waiting, nothing stored.
	collectAndAdmit(h)
	if _, status = inboxTriageOf(t, h); status.Waiting != 1 || itemOf(h, "example", th.Thread) != nil || len(st.ids) != 0 {
		t.Fatalf("second round: triage %+v, stored %v", status, st.ids)
	}

	// The model is back: the next round sorts it and it comes in, stored once; nothing waits.
	model.mode.Store("ok")
	time.Sleep(1100 * time.Millisecond) // Retry-After: 1
	before := len(model.asked())
	collectAndAdmit(h)
	it := itemOf(h, "example", th.Thread)
	if it == nil || it.Tier != TierAction || it.Summary != "bob 请你看 PR" || it.TriagedAt != it.Last || !st.saved(it.ID) {
		t.Fatalf("after the model is back: %+v", it)
	}
	if n := len(model.asked()) - before; n != 1 {
		t.Fatalf("%d triage call(s) once back", n)
	}
	if _, status = inboxTriageOf(t, h); status.State != "ok" || status.Waiting != 0 || len(status.Sources) != 0 {
		t.Fatalf("after: triage %+v", status)
	}
}

// sevenThings are seven things of a plugin's connector its rules leave to the model.
func sevenThings() []connector.InboxThing {
	var things []connector.InboxThing
	for _, n := range []string{"21", "22", "23", "24", "25", "26", "27"} {
		things = append(things, genericThing("example-org/example-repo#"+n, "a", "示例 "+n))
	}
	return things
}

// pauseOver waits until the pause on client's connection has run out (it stays held until a request finds out).
func pauseOver(t *testing.T, client *ai.Client) {
	t.Helper()
	waitFor(t, "the end of the pause", func() bool {
		p := ai.Held(client.Config())
		return p != nil && !time.Now().Before(p.Until)
	})
}

// A short outage of the triage model's upstream (#6698): it answers 503, the connection is held, and once the pause
// is over it answers 404 for a while, then works again. The round after it is healthy sorts every item that
// waited: nothing is left reported as "the model is at its concurrency limit" with nothing in flight. Before, only
// the one request finding out went and the others were put off at once, so that round let one item in and left the
// rest held as busy until a later round; and the 404s were reported as busy too.
func TestInboxTriageRecoversInTheRoundAfterAnOutage(t *testing.T) {
	ai.SetBusyPauseForTest(t, 50*time.Millisecond)
	model, client := newTriageModel(t, "busy", `{"tier":"fyi","summary":"示例","reason":"示例"}`)
	things := sevenThings()
	h, _ := admitHub(t, client, &inboxConnector{things: things})

	collectAndAdmit(h)
	if _, status := inboxTriageOf(t, h); status.State != triagePaused || status.Reason != "模型并发已满" || status.Waiting != len(things) {
		t.Fatalf("while the upstream answers 503: triage %+v", status)
	}
	if ai.Held(client.Config()) == nil {
		t.Fatal("the connection is not held after the 503s")
	}

	// The pause is over and the upstream answers 404: every item is asked about (one finds out after the other), and
	// the status says the model failed, not that it is busy.
	pauseOver(t, client)
	model.mode.Store("gone")
	before := len(model.asked())
	collectAndAdmit(h)
	if _, status := inboxTriageOf(t, h); status.State != triageFailed || status.Waiting != len(things) {
		t.Fatalf("while the upstream answers 404: triage %+v", status)
	}
	if n := len(model.asked()) - before; n != len(things) {
		t.Fatalf("%d of %d items asked about while the upstream answers 404", n, len(things))
	}

	// Healthy again: one round, and all seven are in.
	model.mode.Store("ok")
	collectAndAdmit(h)
	in := 0
	for _, th := range things {
		if it := itemOf(h, "example", th.Thread); it != nil && it.Tier == TierFYI {
			in++
		}
	}
	_, status := inboxTriageOf(t, h)
	if in != len(things) || status.State != "ok" || status.Waiting != 0 || len(status.Sources) != 0 {
		t.Fatalf("the round after the model is healthy: %d of %d in, triage %+v, held %+v", in, len(things), status, ai.Held(client.Config()))
	}
	if p := ai.Held(client.Config()); p != nil {
		t.Fatalf("still held: %+v", p)
	}
}

// The control: while a request really is out on a connection whose pause is over and the upstream has not answered
// it yet, a round's items wait for it as long as they may and are then held as busy; when that request is answered
// the connection opens, and the next round sorts them all.
func TestInboxTriageIsBusyWhileTheModelIs(t *testing.T) {
	ai.SetBusyPauseForTest(t, 50*time.Millisecond)
	window, timeout := admitWindow, admitTimeout
	admitWindow, admitTimeout = 5*time.Second, 150*time.Millisecond
	t.Cleanup(func() { admitWindow, admitTimeout = window, timeout })
	model, client := newTriageModel(t, "busy", `{"tier":"fyi","summary":"示例","reason":"示例"}`)
	model.delay = 600 * time.Millisecond
	things := sevenThings()
	h, _ := admitHub(t, client, &inboxConnector{things: things})
	var out struct{ Tier string }
	if err := client.JSON(context.Background(), "替用户分拣", "示例", &out); !ai.IsPaused(err) {
		t.Fatalf("the busy answer: %v", err)
	}
	pauseOver(t, client)

	// A slow request goes out (it finds out); the upstream takes its time to answer it.
	model.mode.Store("slow")
	slow := make(chan error, 1)
	go func() {
		var out struct{ Tier string }
		slow <- client.JSON(context.Background(), "替用户分拣", "示例：一个慢请求", &out)
	}()
	waitFor(t, "the slow request at the upstream", func() bool { return len(model.asked()) >= 2 })
	before := len(model.asked())
	collectAndAdmit(h)
	if _, status := inboxTriageOf(t, h); status.State != triagePaused || status.Reason != "模型并发已满" || status.Waiting != len(things) {
		t.Fatalf("while a request is out: triage %+v", status)
	}
	if n := len(model.asked()) - before; n != 0 {
		t.Fatalf("%d triage request(s) sent while the one finding out was not answered", n)
	}

	// It is answered: the connection opens, and the next round sorts everything.
	if err := <-slow; err != nil {
		t.Fatalf("the slow request: %v", err)
	}
	model.mode.Store("ok")
	admitTimeout = 10 * time.Second
	collectAndAdmit(h)
	in := 0
	for _, th := range things {
		if itemOf(h, "example", th.Thread) != nil {
			in++
		}
	}
	if _, status := inboxTriageOf(t, h); in != len(things) || status.State != "ok" || status.Waiting != 0 {
		t.Fatalf("after the request finished: %d of %d in, triage %+v", in, len(things), status)
	}
}

// An item with a fallback tier comes in with it when the model fails ("By rule") and that version is final: the model
// coming back does not sort it again. Meanwhile one without a fallback waits.
func TestInboxFallbackIsFinal(t *testing.T) {
	model, client := newTriageModel(t, "fail", `{"tier":"noise","summary":"示例","reason":"示例"}`)
	now := time.Now().Add(-time.Minute)
	lark := &extensionLark{things: []connector.InboxThing{{Thread: "chat:10", Kind: KindDM, Title: "Alice", Preview: "在吗", Context: "Alice：在吗",
		At: now, Last: "5", CanReply: true, Extra: &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "飞书", LegacyID: "lark:10:5",
			Count: 1, StartedAt: now, Lark: rawJSON(InboxLark{AccountID: 1, FeedID: "10", StartPosition: "5"})}}}}
	th := genericThing("example-org/example-repo#4", "a", "示例：PR 上的评论")
	gen := &inboxConnector{things: []connector.InboxThing{th}}
	h, st := admitHub(t, client, lark, gen)

	collectAndAdmit(h)
	dm := h.inbox.items["lark:10:5"]
	if dm == nil || dm.Tier != TierAction || dm.Reason != "按规则" || dm.TriagedAt != dm.Last || !st.saved(dm.ID) {
		t.Fatalf("the DM with a fallback: %+v", dm)
	}
	if itemOf(h, "example", th.Thread) != nil {
		t.Fatal("the item without a fallback came in")
	}
	if _, status := inboxTriageOf(t, h); status.State != triageFailed || status.Waiting != 1 || status.Sources[0].Source != "example" {
		t.Fatalf("triage %+v", status)
	}

	model.mode.Store("ok")
	before := len(model.asked())
	collectAndAdmit(h)
	h.inbox.triaging.Lock() // the pass for items in the list runs in the background: wait for it
	h.inbox.triaging.Unlock()
	calls := model.asked()[before:]
	if len(calls) != 1 || !strings.Contains(calls[0].User, "PR 上的评论") {
		t.Fatalf("triage calls after the model is back: %+v", calls)
	}
	if dm.Tier != TierAction || dm.Reason != "按规则" || dm.Summary != "" {
		t.Fatalf("the DM was sorted again: %+v", dm)
	}
	if it := itemOf(h, "example", th.Thread); it == nil || it.Tier != TierNoise {
		t.Fatalf("the waiting item: %+v", it)
	}
}

// A plugin connector's item in the list without a fallback whose new version the model can't sort keeps what it
// shows (no silent "Noise") and is asked again by the next pass.
func TestInboxConnectorItemIsNotSilentlyNoise(t *testing.T) {
	model, client := newTriageModel(t, "fail", `{"tier":"fyi","summary":"示例","reason":"示例"}`)
	h, _ := admitHub(t, client)
	h.asExtension("example", true)
	it := &InboxItem{ID: "example:1", Source: "example", Kind: "review_requested", Tier: TierAction, Reason: "示例", Last: "b", TriagedAt: "a",
		Context: "示例：新评论", UpdatedAt: time.Now(), Connector: &InboxConnector{Thread: "example-org/example-repo#5"}}
	h.inbox.items[it.ID] = it
	if h.triageOne(context.Background(), it) || it.Tier != TierAction || it.TriagedAt != "a" {
		t.Fatalf("after a failure: %+v", it)
	}
	model.mode.Store("ok")
	if !h.triageOne(context.Background(), it) || it.Tier != TierFYI || it.TriagedAt != "b" {
		t.Fatalf("after the model is back: %+v", it)
	}
}

// With no model set up, an item without a fallback waits and the status says why.
func TestInboxTriageUnconfigured(t *testing.T) {
	th := genericThing("example-org/example-repo#6", "a", "示例")
	h, _ := admitHub(t, nil, &inboxConnector{things: []connector.InboxThing{th}})
	collectAndAdmit(h)
	if _, status := inboxTriageOf(t, h); status.State != triageUnconfigured || status.Reason != "没有配置分拣模型" || status.Waiting != 1 {
		t.Fatalf("triage %+v", status)
	}
}

// inboxPushes counts the {"type":"inbox"} messages the apps were sent (one for each inbox.changed the hub was told).
func inboxPushes(h *inboxHub) int { return h.in.pushes() }

// The apps are told when the triage status moves even though no item came in (their banner follows it, #1773), and
// not again while it stays the same.
func TestInboxTriageStatusIsPushed(t *testing.T) {
	th := genericThing("example-org/example-repo#7", "a", "示例")
	c := &inboxConnector{things: []connector.InboxThing{th}}
	h, _ := admitHub(t, nil, c)
	collectAndAdmit(h)
	if n := inboxPushes(h); n != 1 {
		t.Fatalf("%d inbox push(es) after the first hold", n)
	}
	collectAndAdmit(h)
	if n := inboxPushes(h); n != 1 {
		t.Fatalf("%d inbox push(es) with the status unchanged", n)
	}
	c.things = nil // the connector has nothing new any more: nothing waits
	collectAndAdmit(h)
	if _, status := inboxTriageOf(t, h); status.State != "ok" {
		t.Fatalf("triage %+v", status)
	}
	if n := inboxPushes(h); n != 2 {
		t.Fatalf("%d inbox push(es) once nothing waits", n)
	}
}

// More new items than a round sorts in its window: the ones started come in, the rest wait for the next round,
// which is asked for at once.
func TestInboxAdmitWindow(t *testing.T) {
	window, timeout := admitWindow, admitTimeout
	admitWindow, admitTimeout = 50*time.Millisecond, 5*time.Second
	t.Cleanup(func() { admitWindow, admitTimeout = window, timeout })
	model, client := newTriageModel(t, "slow", `{"tier":"fyi","summary":"示例","reason":"示例"}`)
	model.delay = 300 * time.Millisecond
	var things []connector.InboxThing
	for _, n := range []string{"11", "12", "13", "14", "15", "16"} {
		things = append(things, genericThing("example-org/example-repo#"+n, "a", "示例 "+n))
	}
	h, _ := admitHub(t, client, &inboxConnector{things: things})
	collectAndAdmit(h)
	if n := len(model.asked()); n != triageConcurrency {
		t.Fatalf("%d asked in the window", n)
	}
	in := 0
	for _, th := range things {
		if itemOf(h, "example", th.Thread) != nil {
			in++
		}
	}
	if _, status := inboxTriageOf(t, h); in != triageConcurrency || status.State != triageBehind || status.Waiting != len(things)-in {
		t.Fatalf("%d in, triage %+v", in, status)
	}
	if h.in.kicks == 0 {
		t.Fatal("the next round was not asked for")
	}
}

// The hub's own sources (here a connector no plugin provides) go as they always did: a new item is listed at once,
// untriaged, and the triage pass afterwards gives one without a rule tier its fallback, "Noise", when the model fails;
// nothing is held back.
func TestInboxBuiltinSourcesAsBefore(t *testing.T) {
	model, client := newTriageModel(t, "fail", `{"tier":"action","summary":"示例","reason":"示例"}`)
	th := genericThing("example-org/example-repo#7", "a", "示例：一条评论")
	c := &inboxConnector{things: []connector.InboxThing{th}}
	h, st := admitHub(t, client, c)
	h.asExtension(c.ID(), false)
	h.collectInbox(context.Background(), roundPoll)
	it := itemOf(h, "example", th.Thread)
	if it == nil || !st.saved(it.ID) {
		t.Fatal("a built-in source's new item did not come in at once")
	}
	h.inbox.triaging.Lock() // the triage pass runs in the background: wait for it
	h.inbox.triaging.Unlock()
	h.inbox.mu.Lock()
	tier, reason, triaged := it.Tier, it.Reason, it.TriagedAt == it.Last
	h.inbox.mu.Unlock()
	if tier != TierNoise || reason != "按规则" || !triaged || len(model.asked()) != 1 {
		t.Fatalf("after the pass: tier %q reason %q triaged %v, %d call(s)", tier, reason, triaged, len(model.asked()))
	}
	if _, status := inboxTriageOf(t, h); status.State != "ok" || status.Waiting != 0 {
		t.Fatalf("triage %+v", status)
	}
}

// An item read elsewhere while untriaged is settled by triage, but one the user filed while the model was thinking
// stays filed as the user filed it.
func TestInboxTriageKeepsWhatTheUserFiled(t *testing.T) {
	model, client := newTriageModel(t, "slow", `{"tier":"fyi","summary":"示例","reason":"示例"}`)
	model.delay = 300 * time.Millisecond
	h, _ := admitHub(t, client)
	it := &InboxItem{ID: "mail:1", Source: "mail", Kind: KindMail, Last: "a", Read: true, Context: "示例：周报",
		UpdatedAt: time.Now(), Mail: &InboxMail{AccountID: "acc1"}}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.mu.Unlock()
	done := make(chan bool, 1)
	go func() { done <- h.triageOne(context.Background(), it) }()
	time.Sleep(100 * time.Millisecond) // the model is thinking
	if _, err := h.setInboxDone(context.Background(), it.ID, true, false); err != nil {
		t.Fatal(err)
	}
	<-done
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.DoneBy != "user" {
		t.Fatalf("filed by the user, then triage said %q", it.DoneBy)
	}
}
