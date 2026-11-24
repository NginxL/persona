package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestHubSessionContextBreakdownPreservedInPushAndSummary(t *testing.T) {
	now := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	bd := &proto.ContextBreakdown{
		Source:               proto.ContextBreakdownEstimated,
		SystemAndRules:       proto.Int(18000),
		Conversation:         proto.Int(4000),
		ToolOutputs:          proto.Int(3000),
		Free:                 proto.Int(175000),
		AutoCompactThreshold: proto.Int(167000),
	}
	s := proto.Session{
		MachineID:     "m1",
		SessionID:     "s1",
		Title:         "Session with breakdown",
		Cwd:           "/work",
		Status:        proto.StatusIdle,
		State:         proto.StateIdle,
		ContextTokens: 25000,
		ContextWindow: 200000,
		UpdatedAt:     now,
	}
	s.SetContextBreakdown(bd)

	// Full push encoding via encodeSessions -> appendSession.
	encoded := encodeSessions([]proto.Session{s})
	if len(encoded) != 1 {
		t.Fatalf("expected 1 encoded session, got %d", len(encoded))
	}
	for _, want := range []string{
		`"context_breakdown":{`,
		`"source":"estimated"`,
		`"system_and_rules":18000`,
		`"conversation":4000`,
		`"tool_outputs":3000`,
		`"free":175000`,
		`"auto_compact_threshold":167000`,
	} {
		if !strings.Contains(string(encoded[0].raw), want) {
			t.Fatalf("encoded push row missing %s: %s", want, encoded[0].raw)
		}
	}
	if strings.Contains(string(encoded[0].raw), `"tools_and_mcp"`) {
		t.Fatalf("unavailable tools_and_mcp must stay omitted: %s", encoded[0].raw)
	}

	// Summary row encoding via runtimeSummary must also retain context_breakdown.
	sumRaw, err := json.Marshal(runtimeSummary(s))
	if err != nil {
		t.Fatalf("marshal runtimeSummary: %v", err)
	}
	var decoded proto.Session
	if err := json.Unmarshal(sumRaw, &decoded); err != nil {
		t.Fatalf("unmarshal runtimeSummary: %v", err)
	}
	if !decoded.ContextBreakdown().Equal(bd) {
		t.Fatalf("runtimeSummary lost ContextBreakdown:\ngot  %+v\nwant %+v", decoded.ContextBreakdown(), bd)
	}
}

func TestHubTTFTFallbackAndNodePassthrough(t *testing.T) {
	var tr hubTTFTTracker
	t0 := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)

	// Historical replay without live send/delta stays omitted.
	hist := []proto.Item{{ID: "h1#0", Seq: 1, TS: t0, Role: "assistant", Kind: proto.KindText, Text: "historical"}}
	if tr.annotate("m1", "s1", hist, false) || hist[0].TTFTMS != nil {
		t.Fatalf("historical replay must omit ttft_ms: %v", hist[0].TTFTMS)
	}

	// Hub fallback path: send armed -> first delta -> session.items without node ttft_ms.
	tr.arm("m1", "s1", t0)
	ms, ok := tr.firstToken("m1", "s1", t0.Add(310*time.Millisecond))
	if !ok || ms != 310 {
		t.Fatalf("hub firstToken = %d (%v), want 310 (true)", ms, ok)
	}
	live := []proto.Item{
		{ID: "u1#0", Seq: 1, TS: t0, Role: "user", Kind: proto.KindText, Text: "hello"},
		{ID: "a1#0", Seq: 2, TS: t0.Add(320 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "world"},
	}
	if !tr.annotate("m1", "s1", live, true) || live[1].TTFTMS == nil || *live[1].TTFTMS != 310 {
		t.Fatalf("hub fallback failed to attach ttft_ms: %+v", live[1].TTFTMS)
	}

	// Node-provided TTFTMS takes precedence and is never overwritten by Hub fallback.
	tr.arm("m1", "s2", t0)
	tr.firstToken("m1", "s2", t0.Add(500*time.Millisecond))
	fromNode := []proto.Item{
		{ID: "a2#0", Seq: 1, TS: t0.Add(510 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "fast", TTFTMS: proto.Int64(190)},
		{ID: "a2#1", Seq: 2, TS: t0.Add(700 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "tail"},
	}
	tr.annotate("m1", "s2", fromNode, true)
	if fromNode[0].TTFTMS == nil || *fromNode[0].TTFTMS != 190 {
		t.Fatalf("node-provided ttft_ms overwritten: %v", fromNode[0].TTFTMS)
	}
	if fromNode[1].TTFTMS != nil {
		t.Fatalf("second item should stay nil after node ttft_ms consumed turn: %v", *fromNode[1].TTFTMS)
	}
}

func TestHubMachineEventsAttachTTFTToStreamedItemsAndOmitOnUnarmed(t *testing.T) {
	h := &Hub{
		clients: map[*client]struct{}{},
	}
	sub := &client{
		subs: map[[2]string]bool{{"m1", "s-live"}: true, {"m1", "s-hist"}: true},
		out:  make(chan []byte, 8),
	}
	addTestClient(h, sub)
	m := &Machine{
		hub:         h,
		cfg:         MachineConfig{ID: "m1"},
		lastSeq:     map[string]int{},
		lastHistory: map[string]string{},
	}

	// Armed turn: delta arrives -> items event pushes ttft_ms to subscribers.
	h.ttft.arm("m1", "s-live", time.Now().Add(-120*time.Millisecond))
	m.onEvent("session.delta", json.RawMessage(`{"session_id":"s-live","text":"Hi"}`))
	// Drain delta frame.
	<-sub.out

	liveItemTS := time.Now().UTC().Format(time.RFC3339Nano)
	m.onEvent("session.items", json.RawMessage(`{"session_id":"s-live","items":[{"id":"a1#0","seq":1,"ts":"`+liveItemTS+`","role":"assistant","kind":"text","text":"Hi there"}]}`))
	liveFrame := <-sub.out
	if !strings.Contains(string(liveFrame), `"ttft_ms":`) {
		t.Fatalf("expected ttft_ms on live streamed item push, got %s", liveFrame)
	}

	// Unarmed session (historical/background without live turn): ttft_ms must be omitted.
	m.onEvent("session.items", json.RawMessage(`{"session_id":"s-hist","items":[{"id":"h1#0","seq":1,"ts":"`+liveItemTS+`","role":"assistant","kind":"text","text":"Replayed"}]}`))
	histFrame := <-sub.out
	if strings.Contains(string(histFrame), `"ttft_ms"`) {
		t.Fatalf("unarmed session push must omit ttft_ms, got %s", histFrame)
	}
}

func TestHubTTFTItemBeforeDeltaAndLatestPageBind(t *testing.T) {
	var tr hubTTFTTracker
	t0 := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

	// Item-before-delta ordering: session.items arrives first while armed, then session.delta arrives.
	tr.arm("m1", "s-order", t0)
	pushed := []proto.Item{
		{ID: "old#0", Seq: 1, TS: t0.Add(-30 * time.Second), Role: "assistant", Kind: proto.KindText, Text: "prior"},
		{ID: "a1#0", Seq: 2, TS: t0.Add(200 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "reply"},
	}
	tr.annotate("m1", "s-order", pushed, true)
	ms, ok := tr.firstToken("m1", "s-order", t0.Add(240*time.Millisecond))
	if !ok || ms != 240 {
		t.Fatalf("firstToken = %d (%v), want 240 (true)", ms, ok)
	}
	// Follow-up read of a1#0 (even with allowBind=false) returns 240ms; second item stays nil.
	reread := []proto.Item{
		{ID: "a1#0", Seq: 2, TS: t0.Add(200 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "reply"},
		{ID: "a1#1", Seq: 3, TS: t0.Add(500 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "extra"},
	}
	if !tr.annotate("m1", "s-order", reread, false) || reread[0].TTFTMS == nil || *reread[0].TTFTMS != 240 {
		t.Fatalf("item-before-delta lost ttft_ms on reread: %+v", reread[0].TTFTMS)
	}
	if reread[1].TTFTMS != nil {
		t.Fatalf("second item must stay nil after awaiting item bound: %v", *reread[1].TTFTMS)
	}

	// Native node ttft_ms arriving while armed consumes the turn so a late delta is ignored.
	tr.arm("m1", "s-native", t0)
	nativeItems := []proto.Item{
		{ID: "n1#0", Seq: 1, TS: t0.Add(150 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "ok", TTFTMS: proto.Int64(145)},
	}
	tr.annotate("m1", "s-native", nativeItems, true)
	if _, late := tr.firstToken("m1", "s-native", t0.Add(400*time.Millisecond)); late {
		t.Fatal("late delta should be ignored after native ttft_ms consumed armed turn")
	}
}

func TestHubDeliverOnceArmsBeforeSendAndDisarmsOnRefusal(t *testing.T) {
	h := newTestHub(t)
	hubSide, nodeSide := net.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubSide), rpc.New("n", nodeSide)
	t.Cleanup(func() { hubSide.Close(); nodeSide.Close() })

	m := newMachine(h, MachineConfig{ID: "m1", Name: "Test Machine"})
	m.peer = hubPeer

	deltaSeen := make(chan struct{}, 1)
	var mode string
	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "session.send" {
			return nil, &rpc.Error{Code: "unknown", Message: "unexpected"}
		}
		switch mode {
		case "early_delta":
			// Emit streaming delta BEFORE session.send returns.
			if err := nodePeer.Notify("session.delta", map[string]any{"session_id": "s-fast", "text": "Hi"}); err != nil {
				return nil, err
			}
			// Wait for Hub to observe the delta before acking session.send.
			select {
			case <-deltaSeen:
			case <-time.After(3 * time.Second):
				t.Errorf("timed out waiting for Hub to observe early delta")
			}
			return map[string]any{"ok": true}, nil
		case "refused":
			return nil, &rpc.Error{Code: "refused", Message: "not running"}
		case "queued":
			return map[string]any{"ok": true, "queued": true}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	hubPeer.OnEvent = func(event string, params json.RawMessage) {
		m.onEvent(event, params)
		if event == "session.delta" {
			select {
			case deltaSeen <- struct{}{}:
			default:
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = hubPeer.Serve(ctx, hubSide) }()
	go func() { _ = nodePeer.Serve(ctx, nodeSide) }()

	// 1. Early delta emitted during session.send RPC is captured because Hub arms before dispatch.
	mode = "early_delta"
	status, _, _ := h.deliverOnce(ctx, m, map[string]any{"session_id": "s-fast", "text": "hello"})
	if status != http.StatusOK {
		t.Fatalf("deliverOnce status = %d, want 200", status)
	}
	items := []proto.Item{{ID: "f1#0", Seq: 1, TS: time.Now(), Role: "assistant", Kind: proto.KindText, Text: "Hi"}}
	if !h.ttft.annotate("m1", "s-fast", items, true) || items[0].TTFTMS == nil || *items[0].TTFTMS < 0 {
		t.Fatalf("expected Hub fallback ttft_ms from early delta during send RPC, got %+v", items[0].TTFTMS)
	}

	// 2. Definitive send refusal disarms tracker so a subsequent delta does not record TTFT.
	mode = "refused"
	status, _, outcome := h.deliverOnce(ctx, m, map[string]any{"session_id": "s-refused", "text": "hello"})
	if status == http.StatusOK || outcome != "" {
		t.Fatalf("expected definitive refusal, got status=%d outcome=%q", status, outcome)
	}
	if _, ok := h.ttft.firstToken("m1", "s-refused", time.Now()); ok {
		t.Fatal("refused send should have disarmed Hub TTFT tracker")
	}

	// 3. Queued send disarms tracker so it does not claim an already-running turn's TTFT.
	mode = "queued"
	status, _, _ = h.deliverOnce(ctx, m, map[string]any{"session_id": "s-queued", "text": "follow-up"})
	if status != http.StatusOK {
		t.Fatalf("queued send status = %d, want 200", status)
	}
	if _, ok := h.ttft.firstToken("m1", "s-queued", time.Now()); ok {
		t.Fatal("queued send should have disarmed Hub TTFT tracker")
	}
}

func TestHubHTTPSessionItemsBindsPendingOnLatestPageOnly(t *testing.T) {
	h := newTestHub(t)
	hubSide, nodeSide := net.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubSide), rpc.New("n", nodeSide)
	t.Cleanup(func() { hubSide.Close(); nodeSide.Close() })

	t0 := time.Now()
	oldItem := proto.Item{ID: "old#0", Seq: 1, TS: t0.Add(-2 * time.Minute), Role: "assistant", Kind: proto.KindText, Text: "old"}
	newItem := proto.Item{ID: "new#0", Seq: 2, TS: t0.Add(150 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "new"}

	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "session.items" {
			return nil, &rpc.Error{Code: "unknown", Message: "unexpected"}
		}
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		if _, hasBefore := p["before_seq"]; hasBefore {
			return map[string]any{"items": []proto.Item{newItem}, "has_more": true}, nil
		}
		if _, hasThrough := p["through_seq"]; hasThrough {
			return map[string]any{"items": []proto.Item{newItem}, "more": false}, nil
		}
		return map[string]any{"items": []proto.Item{oldItem, newItem}, "has_more": false}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = hubPeer.Serve(ctx, hubSide) }()
	go func() { _ = nodePeer.Serve(ctx, nodeSide) }()

	m := newMachine(h, MachineConfig{ID: "m1", Name: "Test Machine"})
	m.peer = hubPeer

	// Arm and capture pending TTFT without pushing session.items.
	h.ttft.arm("m1", "s1", t0)
	if ms, ok := h.ttft.firstToken("m1", "s1", t0.Add(120*time.Millisecond)); !ok || ms != 120 {
		t.Fatalf("firstToken = %d (%v), want 120 (true)", ms, ok)
	}

	fetch := func(rawQuery string) []proto.Item {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/machines/m1/sessions/s1/items"+rawQuery, nil)
		req.SetPathValue("sid", "s1")
		rec := httptest.NewRecorder()
		h.sessionItems(rec, req, m)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s -> %d: %s", rawQuery, rec.Code, rec.Body.String())
		}
		var resp struct {
			Items []proto.Item `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s: %v", rawQuery, err)
		}
		return resp.Items
	}

	// Historical page (?before_seq=10) and fold expansion (?from_seq=1&through_seq=5) must not bind pending TTFT.
	if items := fetch("?before_seq=10"); len(items) != 1 || items[0].TTFTMS != nil {
		t.Fatalf("historical page must not bind pending ttft_ms: %+v", items)
	}
	if items := fetch("?from_seq=1&through_seq=5"); len(items) != 1 || items[0].TTFTMS != nil {
		t.Fatalf("fold expansion must not bind pending ttft_ms: %+v", items)
	}

	// Latest page (no before_seq/through_seq) binds pending TTFT to the new turn item and skips old historical items.
	latest := fetch("")
	if len(latest) != 2 {
		t.Fatalf("latest len = %d, want 2", len(latest))
	}
	if latest[0].TTFTMS != nil {
		t.Fatalf("old historical item on latest page must not get ttft_ms: %v", *latest[0].TTFTMS)
	}
	if latest[1].TTFTMS == nil || *latest[1].TTFTMS != 120 {
		t.Fatalf("new turn item on latest page TTFTMS = %v, want 120", latest[1].TTFTMS)
	}
}
