package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// historyRuntime is a fake normalized runtime. No CLI or model process is started.
type historyRuntime struct {
	mu      sync.Mutex
	items   []proto.Item
	history string
	offline bool
	reads   int
	after   []int
}

func (rt *historyRuntime) page(_ string, raw json.RawMessage) (any, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.reads++
	if rt.offline {
		return nil, errors.New("fake runtime is offline")
	}
	var p historyParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Strict {
		return map[string]any{"items": rt.items, "strict_read": true}, nil
	}
	from, end := 0, 0
	if p.After != nil {
		from = *p.After
		rt.after = append(rt.after, from)
	}
	for _, it := range rt.items {
		end = max(end, it.Seq)
	}
	end = max(end, from)
	items := []proto.Item{}
	for _, it := range rt.items {
		if it.Seq <= from || (p.Before != nil && *p.Before > 0 && it.Seq >= *p.Before) {
			continue
		}
		items = append(items, it)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 500
	}
	if p.Turns > 0 {
		limit = p.Turns * 2
	}
	more, hasMore := false, false
	if len(items) > limit {
		if p.After != nil {
			items = items[:limit]
			end = items[len(items)-1].Seq
			more = true
		} else {
			items = items[len(items)-limit:]
			from = items[0].Seq - 1
			hasMore = true
		}
	}
	if p.Before != nil && *p.Before > 0 {
		end = *p.Before - 1
	}
	return map[string]any{"items": items, "history": rt.history, "from_seq": from, "to_seq": end,
		"has_more": hasMore, "more_after": more}, nil
}

func historyFixture(t *testing.T, logs ...func(string, ...any)) (*Service, *fakeHub, *db.DB, *historyRuntime) {
	t.Helper()
	database := todoTestDB(t)
	logf := testLogf(t)
	if len(logs) > 0 {
		logf = logs[0]
	}
	f := newFakeHub().withMachine()
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return database }, Logf: logf}))
	setSettings(t, s, func(cfg *Settings) { cfg.Main = "main-chat"; cfg.Leaders = []string{"lead-chat"} })
	for _, id := range []string{"main-chat", "lead-chat", "side-chat", "coding-agent"} {
		f.known[id] = true
	}
	f.setSessions("server",
		proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100},
		proto.Session{SessionID: "lead-chat", Assistant: true, Rev: 100},
		proto.Session{SessionID: "side-chat", Assistant: true, Archived: true, Rev: 100},
		proto.Session{SessionID: "coding-agent", PaneID: "pane-1", Rev: 100})
	rt := &historyRuntime{history: "h1", items: []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "question one"},
		{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "answer one"},
		{ID: "u2", Seq: 3, Role: "user", Kind: proto.KindText, Text: "same words"},
		{ID: "a2", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "answer two"},
		{ID: "u3", Seq: 5, Role: "user", Kind: proto.KindText, Text: "same words"},
		{ID: "a3", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "answer three"},
	}}
	f.answerNode(DeviceSessionItems, rt.page)
	f.answerNode("session.item", func(_ string, raw json.RawMessage) (any, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.offline {
			return nil, errors.New("fake runtime is offline")
		}
		var p historyParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		for _, it := range rt.items {
			if it.ID == p.ItemID {
				it.FullSize = 0
				return it, nil
			}
		}
		return nil, errors.New("fake source item not found")
	})
	f.answerNode(DeviceAssistantHandoff, func(_ string, _ json.RawMessage) (any, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.offline {
			return nil, errors.New("fake runtime is offline")
		}
		return map[string]any{"summary": "source engine compaction"}, nil
	})
	return s, f, database, rt
}

func readHistory(t *testing.T, s *Service, sid string, params map[string]any) historyPage {
	t.Helper()
	params["session_id"] = sid
	var page historyPage
	if err := s.call(t.Context(), "server", DeviceSessionItems, params, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestDurableHistoryDefaultOnAssistantScopeAndOfflineRestart(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	if s.historyDB(t.Context()) == nil {
		t.Fatal("missing setting must enable database-first history")
	}
	s.historySweep(t.Context(), database) // no UI watch or subscription
	stats, err := database.ConversationHistoryStats(t.Context())
	if err != nil || stats.Conversations != 3 || stats.Items != 18 {
		t.Fatalf("assistant scope %+v %v", stats, err)
	}
	if h, err := database.ConversationHistory(t.Context(), "server", "coding-agent"); err != nil || h != nil {
		t.Fatalf("coding Agent enrolled: %+v %v", h, err)
	}
	rt.mu.Lock()
	rt.offline = true
	reads := rt.reads
	rt.mu.Unlock()
	for _, sid := range []string{"main-chat", "lead-chat", "side-chat"} {
		p := readHistory(t, s, sid, map[string]any{"turns": 2, "lazy_images": true})
		if len(p.Items) != 4 || p.Items[0].ID != "u2" || p.Items[2].ID != "u3" {
			t.Fatalf("recent rounds/dedup: %+v", p)
		}
		var h mainHandoff
		if err := s.call(t.Context(), "server", DeviceAssistantHandoff, map[string]any{"session_id": sid}, &h); err != nil || h.Summary != "source engine compaction" || len(h.Recent) != 6 {
			t.Fatalf("offline handoff %+v %v", h, err)
		}
	}
	rt.mu.Lock()
	after := rt.reads
	rt.mu.Unlock()
	if after != reads {
		t.Fatalf("covered history queried offline runtime: %d -> %d", reads, after)
	}
	restarted := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return database }, Logf: testLogf(t)}))
	p := readHistory(t, restarted, "main-chat", map[string]any{"limit": 100, "lazy_images": true})
	if len(p.Items) != 6 || p.HasMore == nil || *p.HasMore || p.Partial {
		t.Fatalf("restart history %+v", p)
	}
}

func TestDurableHistoryIncrementalGenerationAndRollback(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	c := proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.items = append(rt.items, proto.Item{ID: "a4", Seq: 7, Role: "assistant", Kind: proto.KindText, Text: "new words"})
	rt.mu.Unlock()
	c.Rev = 200
	f.setSessions("server", c)
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	cursor := append([]int(nil), rt.after...)
	rt.mu.Unlock()
	if fmt.Sprint(cursor) != "[0 6 6]" {
		t.Fatalf("not incremental: %v", cursor)
	}
	if p := readHistory(t, s, "main-chat", map[string]any{"after_seq": 6, "lazy_images": true}); len(p.Items) != 1 || p.Items[0].ID != "a4" {
		t.Fatalf("catch-up %+v", p)
	}
	if r := request(s, "PUT", "/api/v1/assistant/history", `{"enabled":false}`); r.Code != 200 {
		t.Fatalf("rollback %d %s", r.Code, r.Body.String())
	}
	if s.historyDB(t.Context()) != nil {
		t.Fatal("rollback did not disable reads/sync")
	}
	rt.mu.Lock()
	rt.history = "h2"
	rt.items = []proto.Item{{ID: "replacement", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "rewound"}}
	rt.mu.Unlock()
	if p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true}); len(p.Items) != 1 || p.Items[0].ID != "replacement" {
		t.Fatalf("rollback runtime path %+v", p)
	}
	if r := request(s, "PUT", "/api/v1/assistant/history", `{"enabled":true}`); r.Code != 200 {
		t.Fatal(r.Code)
	}
	c.Rev = 50
	f.setSessions("server", c)
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	h, err := database.ConversationHistory(t.Context(), "server", "main-chat")
	if err != nil || h.History != "h2" {
		t.Fatalf("re-enable generation %+v %v", h, err)
	}
	rt.mu.Lock()
	rt.offline = true
	rt.mu.Unlock()
	if p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true}); len(p.Items) != 1 || p.Items[0].ID != "replacement" {
		t.Fatalf("stale generation mixed %+v", p)
	}
}

func TestDurableHistoryLegacyTailCoverageAndIncompleteReferences(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.items[5].Text = strings.Repeat("x", 100000)
	rt.items[5].FullSize = 100000
	rt.mu.Unlock()
	p := readHistory(t, s, "side-chat", map[string]any{"turns": 1, "lazy_images": true})
	if len(p.Items) != 2 {
		t.Fatalf("legacy source %+v", p)
	}
	h, err := database.ConversationHistory(t.Context(), "server", "side-chat")
	if err != nil || h == nil || conversationlog.Covers(h.Coverage, 0, 6) {
		t.Fatalf("partial tail misrepresented %+v %v", h, err)
	}
	rt.mu.Lock()
	rt.offline = true
	rt.mu.Unlock()
	p = readHistory(t, s, "side-chat", map[string]any{"turns": 1, "lazy_images": true})
	if !p.Partial || p.HasMore == nil || !*p.HasMore || len(p.Items) != 2 || p.Items[1].Stored == nil || p.Items[1].Stored.Complete {
		t.Fatalf("missing incomplete/coverage metadata %+v", p)
	}
	for _, method := range []string{"session.item", DeviceSessionItems} {
		params := map[string]any{"session_id": "side-chat", "item_id": "a3", "strict_read": true}
		if err := s.call(t.Context(), "server", method, params, &map[string]any{}); err == nil {
			t.Fatalf("strict source read %s fabricated complete content", method)
		}
	}
	if err := s.call(t.Context(), "server", DeviceSessionItems, map[string]any{"session_id": "side-chat", "before_seq": 5, "lazy_images": true}, &historyPage{}); err == nil {
		t.Fatal("uncovered older page must not claim empty complete history")
	}
}

type failedHistoryStore struct{ *db.DB }

func (failedHistoryStore) ConversationHistory(context.Context, string, string) (*db.ConversationHistory, error) {
	return nil, errors.New("fake database failure")
}

func TestDurableHistoryDatabaseFailureFallsBackToRuntime(t *testing.T) {
	_, f, database, rt := historyFixture(t)
	st := failedHistoryStore{database}
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)}))
	p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true})
	if len(p.Items) != 6 {
		t.Fatalf("runtime fallback %+v", p)
	}
	rt.mu.Lock()
	reads := rt.reads
	rt.mu.Unlock()
	if reads == 0 {
		t.Fatal("database failure did not use runtime")
	}
}

func TestDurableHistoryHubNodeCallReadsDatabase(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	if err := s.historySync(t.Context(), database, "server", proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.offline = true
	rt.mu.Unlock()
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "test-only", Devices: &fakeDevices{}})
	b.helloOnce.Do(func() { close(b.helloed) })
	b.svc = s
	p, _ := json.Marshal(devicewire.NodeCall{MachineID: "server", Method: DeviceSessionItems, Params: json.RawMessage(`{"session_id":"main-chat","lazy_images":true}`)})
	out, err := b.request(t.Context(), devicewire.MethodCall, p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var page historyPage
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Items) != 6 {
		t.Fatalf("hub relay did not use durable history: %s %v", raw, err)
	}
}

type gatedHistoryStore struct {
	*db.DB
	entered, release chan struct{}
}

func (st gatedHistoryStore) ConversationItems(ctx context.Context, machine, sid, history string, after, through, limit int, newest bool) ([]conversationlog.Item, error) {
	rows, err := st.DB.ConversationItems(ctx, machine, sid, history, after, through, limit, newest)
	close(st.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-st.release:
	}
	return rows, err
}

func TestDurableHistoryDeletionDuringCachedReadDropsContent(t *testing.T) {
	s, f, database, _ := historyFixture(t)
	if err := s.historySync(t.Context(), database, "server", proto.Session{SessionID: "side-chat", Assistant: true, Rev: 100}); err != nil {
		t.Fatal(err)
	}
	st := gatedHistoryStore{database, make(chan struct{}), make(chan struct{})}
	reader := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)}))
	type result struct {
		raw json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		raw, _, err := reader.historyCall(t.Context(), "server", DeviceSessionItems, json.RawMessage(`{"session_id":"side-chat","lazy_images":true}`))
		done <- result{raw, err}
	}()
	select {
	case <-st.entered:
	case early := <-done:
		t.Fatalf("read ended before gate: %v", early.err)
	case <-time.After(5 * time.Second):
		t.Fatal("cached read did not reach gate")
	}
	if err := database.PutConversationDeletion(t.Context(), "server", "side-chat", json.RawMessage(`{"session_id":"side-chat"}`)); err != nil {
		t.Fatal(err)
	}
	close(st.release)
	got := <-done
	if got.err == nil || len(got.raw) != 0 {
		t.Fatalf("deleted content escaped cached read: %s %v", got.raw, got.err)
	}
}

func TestDurableHistoryUsesNormalizedContractAcrossEngines(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	for _, engine := range []string{"claude", "codex", "pi", "omp", "dsh", "agy", "cursor", "traex", "opencode", "grok"} {
		c := proto.Session{SessionID: "engine-" + engine, Agent: engine, Assistant: true, Rev: 100}
		if err := s.historySync(t.Context(), database, "server", c); err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		h, err := database.ConversationHistory(t.Context(), "server", c.SessionID)
		if err != nil || h == nil || h.TailSeq == nil || *h.TailSeq != 6 {
			t.Fatalf("%s normalized coverage %+v %v", engine, h, err)
		}
	}
	rt.mu.Lock()
	reads := rt.reads
	rt.mu.Unlock()
	if reads != 20 {
		t.Fatalf("fake runtime page reads %d, want ingestion plus summary-generation verification per engine", reads)
	}
}

func TestDurableHistorySameRevisionRewriteAndLateNotification(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	c := proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.history = "h2"
	rt.items = []proto.Item{{ID: "replaced", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "rewritten"}}
	rt.mu.Unlock()
	f.setSessions("server", c)
	s.NodeEvent("server", "session.items", json.RawMessage(`{"session_id":"main-chat","history":"h2","items":[]}`))
	if p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true}); len(p.Items) != 1 || p.Items[0].ID != "replaced" {
		t.Fatalf("same-revision stale cache %+v", p)
	}
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	s.NodeEvent("server", "session.items", json.RawMessage(`{"session_id":"main-chat","history":"h1","items":[{"id":"old-event","seq":8}]}`))
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	h, err := database.ConversationHistory(t.Context(), "server", "main-chat")
	if err != nil || h.History != "h2" {
		t.Fatalf("late notification resurrected old generation %+v %v", h, err)
	}
	rows, err := database.ConversationItems(t.Context(), "server", "main-chat", "h2", 0, 100, 100, false)
	if err != nil || len(rows) != 1 || rows[0].Item.ID != "replaced" {
		t.Fatalf("late event was stored %+v %v", rows, err)
	}
}

func TestDurableHistoryDoesNotHydrateFromAnotherGeneration(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.items[5].Text = "preview"
	rt.items[5].FullSize = 100
	rt.mu.Unlock()
	f.answerNode("session.item", func(_ string, _ json.RawMessage) (any, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		rt.history = "h2"
		rt.items = []proto.Item{{ID: "replacement", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "current generation"}}
		return proto.Item{ID: "a3", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "wrong generation hydration"}, nil
	})
	c := proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}
	if err := s.historySync(t.Context(), database, "server", c); !errors.Is(err, db.ErrConversationHistoryChanged) {
		t.Fatalf("mixed-generation hydration accepted: %v", err)
	}
	if h, err := database.ConversationHistory(t.Context(), "server", c.SessionID); err != nil || h != nil {
		t.Fatalf("invalid hydrated page was committed %+v %v", h, err)
	}
	if err := s.historySync(t.Context(), database, "server", c); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ConversationItems(t.Context(), "server", c.SessionID, "h2", 0, 100, 100, false)
	if err != nil || len(rows) != 1 || rows[0].Item.ID != "replacement" {
		t.Fatalf("retry mixed source generations %+v %v", rows, err)
	}
}

func TestDurableHistoryBacksOffUnsupportedSourcesUntilNewActivity(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.history = ""
	rt.mu.Unlock()
	s.historySweep(t.Context(), database)
	rt.mu.Lock()
	first := rt.reads
	rt.mu.Unlock()
	s.historySweep(t.Context(), database)
	rt.mu.Lock()
	second := rt.reads
	rt.history = "h1"
	rt.mu.Unlock()
	if first != 3 || second != first {
		t.Fatalf("legacy source polled repeatedly: %d -> %d", first, second)
	}
	s.NodeEvent("server", "sessions.revs", json.RawMessage(`{"revs":{"main-chat":100}}`))
	s.historySweep(t.Context(), database)
	h, err := database.ConversationHistory(t.Context(), "server", "main-chat")
	if err != nil || h == nil || h.History != "h1" {
		t.Fatalf("new activity did not release source backoff %+v %v", h, err)
	}
}

func TestDurableHistoryMissingGenerationKeepsLegacyRuntimeFallback(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.history = ""
	rt.mu.Unlock()
	if page := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true}); len(page.Items) != 6 {
		t.Fatalf("legacy runtime fallback %+v", page)
	}
	if h, err := database.ConversationHistory(t.Context(), "server", "main-chat"); err != nil || h != nil {
		t.Fatalf("invented coverage without source identity %+v %v", h, err)
	}
}

func TestDurableHistorySummaryReadCannotCrossARewrite(t *testing.T) {
	s, f, database, rt := historyFixture(t)
	f.answerNode(DeviceAssistantHandoff, func(_ string, _ json.RawMessage) (any, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		rt.history = "h2"
		rt.items = []proto.Item{{ID: "replacement", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "new generation"}}
		return map[string]any{"summary": "summary after the rewrite"}, nil
	})
	err := s.historySync(t.Context(), database, "server", proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100})
	if !errors.Is(err, db.ErrConversationHistoryChanged) {
		t.Fatalf("cross-generation summary accepted: %v", err)
	}
	h, err := database.ConversationHistory(t.Context(), "server", "main-chat")
	if err != nil || h == nil || h.History != "h1" || h.SummaryKnown || h.Summary != "" {
		t.Fatalf("summary leaked across generation %+v %v", h, err)
	}
}

func TestDurableHistoryKeepsLatestNarrationAndUnfoldedCatchup(t *testing.T) {
	s, _, _, rt := historyFixture(t)
	rt.mu.Lock()
	rt.items = []proto.Item{
		{ID: "question", Seq: 1, Role: "user", Kind: proto.KindText, Text: "check progress"},
		{ID: "thinking", Seq: 2, Role: "assistant", Kind: proto.KindThinking, Text: "checking"},
		{ID: "tool", Seq: 3, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "call-1", ToolName: "Read"},
		{ID: "result", Seq: 4, Role: "user", Kind: proto.KindToolResult, ToolUseID: "call-1", Text: "result"},
		{ID: "narration", Seq: 5, Role: "assistant", Kind: proto.KindText, Narration: true, Text: "current progress"},
	}
	rt.mu.Unlock()
	for range 2 {
		p := readHistory(t, s, "side-chat", map[string]any{"fold_work": true, "lazy_images": true})
		if len(p.Items) < 2 || p.Items[len(p.Items)-1].ID != "narration" {
			t.Fatalf("latest commentary disappeared %+v", p.Items)
		}
	}
	p := readHistory(t, s, "side-chat", map[string]any{"after_seq": 0, "fold_work": true, "lazy_images": true})
	if len(p.Items) != 5 || p.HasMore != nil {
		t.Fatalf("catch-up folded or made an older-page claim %+v", p)
	}
}

func TestDurableHistoryPagingRetainsSourceLimits(t *testing.T) {
	s, _, database, rt := historyFixture(t)
	rt.mu.Lock()
	rt.items = nil
	for seq := 1; seq <= 200; seq++ {
		role := "assistant"
		if seq%2 == 1 {
			role = "user"
		}
		rt.items = append(rt.items, proto.Item{ID: fmt.Sprintf("i%d", seq), Seq: seq, Role: role, Kind: proto.KindText, Text: "words"})
	}
	rt.mu.Unlock()
	if err := s.historySync(t.Context(), database, "server", proto.Session{SessionID: "main-chat", Assistant: true, Rev: 100}); err != nil {
		t.Fatal(err)
	}
	p := readHistory(t, s, "main-chat", map[string]any{"lazy_images": true})
	if len(p.Items) != 80 || p.Items[0].Seq != 121 || p.HasMore == nil || !*p.HasMore {
		t.Fatalf("default latest-page contract %+v", p)
	}
	p = readHistory(t, s, "main-chat", map[string]any{"turns": 50, "lazy_images": true})
	if len(p.Items) != 20 || p.Items[0].Seq != 181 {
		t.Fatalf("turn cap changed %+v", p)
	}
	p = readHistory(t, s, "main-chat", map[string]any{"after_seq": 0, "lazy_images": true})
	if len(p.Items) != 200 || p.MoreAfter || p.HasMore != nil {
		t.Fatalf("catch-up cursor contract %+v", p)
	}
}

func TestDurableHistoryPartialTailFillsLargerOnlineWindow(t *testing.T) {
	for _, params := range []map[string]any{
		{"turns": 3, "lazy_images": true},
		{"limit": 80, "lazy_images": true},
	} {
		t.Run(fmt.Sprint(params), func(t *testing.T) {
			s, _, _, rt := historyFixture(t)
			if p := readHistory(t, s, "side-chat", map[string]any{"turns": 1, "lazy_images": true}); len(p.Items) != 2 {
				t.Fatalf("seed tail %+v", p)
			}
			rt.mu.Lock()
			before := rt.reads
			rt.mu.Unlock()
			p := readHistory(t, s, "side-chat", params)
			rt.mu.Lock()
			after := rt.reads
			rt.mu.Unlock()
			if len(p.Items) != 6 || p.Items[0].ID != "u1" || after <= before {
				t.Fatalf("partial cache hid available source: %+v, reads %d -> %d", p, before, after)
			}
		})
	}
}

func TestDurableHistoryPartialHandoffPrefersSourceAndMarksOfflineFallback(t *testing.T) {
	s, _, _, rt := historyFixture(t)
	readHistory(t, s, "side-chat", map[string]any{"turns": 1, "lazy_images": true})
	var handoff mainHandoff
	params := map[string]any{"session_id": "side-chat"}
	if err := s.call(t.Context(), "server", DeviceAssistantHandoff, params, &handoff); err != nil || handoff.Summary != "source engine compaction" {
		t.Fatalf("partial cache hid source handoff: %+v %v", handoff, err)
	}
	rt.mu.Lock()
	rt.offline = true
	rt.mu.Unlock()
	handoff = mainHandoff{}
	if err := s.call(t.Context(), "server", DeviceAssistantHandoff, params, &handoff); err != nil || !handoff.Partial || len(handoff.Recent) != 2 {
		t.Fatalf("partial offline handoff misrepresented: %+v %v", handoff, err)
	}
}
