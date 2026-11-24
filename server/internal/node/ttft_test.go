package node

import (
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestTurnTTFTTrackerLiveMeasurementAndHistoricalOmission(t *testing.T) {
	var tr turnTTFTTracker
	t0 := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	// Historical session with no live turn observation: ttft_ms stays omitted.
	hist := []proto.Item{
		{ID: "u0#0", Seq: 1, TS: t0.Add(-time.Minute), Role: "user", Kind: proto.KindText, Text: "old"},
		{ID: "a0#0", Seq: 2, TS: t0.Add(-59 * time.Second), Role: "assistant", Kind: proto.KindText, Text: "old answer"},
	}
	if tr.annotate("s1", hist, true) || hist[1].TTFTMS != nil {
		t.Fatalf("historical replay must not get ttft_ms: %+v", hist[1].TTFTMS)
	}

	// Live streaming turn: arm -> first token (275ms later) -> subsequent chunks ignored.
	tr.arm("s1", t0, "p1")
	ms, ok := tr.firstToken("s1", t0.Add(275*time.Millisecond), "p1")
	if !ok || ms != 275 {
		t.Fatalf("firstToken = %d (%v), want 275 (true)", ms, ok)
	}
	if _, second := tr.firstToken("s1", t0.Add(900*time.Millisecond), "p1"); second {
		t.Fatal("second chunk in same turn must not overwrite TTFT")
	}

	// Annotate live items: old historical item before t0 is skipped; first new assistant item gets 275ms;
	// second assistant item in the same turn stays nil.
	batch := []proto.Item{
		{ID: "old#0", Seq: 1, TS: t0.Add(-30 * time.Second), Role: "assistant", Kind: proto.KindText, Text: "prior turn"},
		{ID: "u1#0", Seq: 2, TS: t0, Role: "user", Kind: proto.KindText, Text: "new prompt"},
		{ID: "a1#0", Seq: 3, TS: t0.Add(300 * time.Millisecond), Role: "assistant", Kind: proto.KindThinking, Text: "thinking"},
		{ID: "a1#1", Seq: 4, TS: t0.Add(500 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "answer"},
	}
	if !tr.annotate("s1", batch, true) {
		t.Fatal("expected annotate to attach ttft_ms")
	}
	if batch[0].TTFTMS != nil || batch[1].TTFTMS != nil {
		t.Fatalf("old/user items must not receive ttft_ms: %+v", batch)
	}
	if batch[2].TTFTMS == nil || *batch[2].TTFTMS != 275 {
		t.Fatalf("first assistant item TTFTMS = %v, want 275", batch[2].TTFTMS)
	}
	if batch[3].TTFTMS != nil {
		t.Fatalf("second assistant item must not duplicate ttft_ms: %v", *batch[3].TTFTMS)
	}

	// Re-fetching the bound item (even with allowBind=false) preserves the observed 275ms.
	refetch := []proto.Item{{ID: "a1#0", Seq: 3, TS: t0.Add(300 * time.Millisecond), Role: "assistant", Kind: proto.KindThinking, Text: "thinking"}}
	if !tr.annotate("s1", refetch, false) || refetch[0].TTFTMS == nil || *refetch[0].TTFTMS != 275 {
		t.Fatalf("refetch lost bound TTFTMS: %+v", refetch[0].TTFTMS)
	}

	// Turn that ends before producing any token never fabricates TTFT for a subsequent turn.
	tr.arm("s2", t0, "p-err")
	tr.clearPending("s2")
	if _, ok := tr.firstToken("s2", t0.Add(500*time.Millisecond), "p-err"); ok {
		t.Fatal("cleared turn must not emit TTFT")
	}
}

func TestTurnTTFTTrackerPreservesNativeEngineTTFT(t *testing.T) {
	var tr turnTTFTTracker
	t0 := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	tr.arm("s-native", t0, "")
	tr.firstToken("s-native", t0.Add(400*time.Millisecond), "")

	items := []proto.Item{
		{ID: "a1#0", Seq: 1, TS: t0.Add(410 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "hi", TTFTMS: proto.Int64(180)},
		{ID: "a2#0", Seq: 2, TS: t0.Add(600 * time.Millisecond), Role: "assistant", Kind: proto.KindText, Text: "more"},
	}
	tr.annotate("s-native", items, true)
	if items[0].TTFTMS == nil || *items[0].TTFTMS != 180 {
		t.Fatalf("native TTFTMS overwritten: %v", items[0].TTFTMS)
	}
	if items[1].TTFTMS != nil {
		t.Fatalf("pending TTFT leaked onto second item after native TTFT consumed turn: %v", *items[1].TTFTMS)
	}
}

func TestTurnTTFTTrackerRawRuntimeAnnotation(t *testing.T) {
	var tr turnTTFTTracker
	t0 := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{"session_id":"rt1","items":[{"id":"i1","seq":1,"ts":"2026-11-21T12:00:01Z","role":"assistant","kind":"text","text":"hello"}]}`)

	// No tracked TTFT: raw payload is returned untouched.
	if got := tr.annotateRaw("rt1", raw, "items", true); string(got) != string(raw) {
		t.Fatalf("untracked session should pass through raw bytes unchanged: %s", got)
	}

	tr.arm("rt1", t0, "")
	tr.firstToken("rt1", t0.Add(150*time.Millisecond), "")
	patched := tr.annotateRaw("rt1", raw, "items", true)
	if !strings.Contains(string(patched), `"ttft_ms":150`) {
		t.Fatalf("expected ttft_ms:150 in patched runtime payload: %s", patched)
	}
}

func TestLiveReplyHooksMeasureTTFT(t *testing.T) {
	const sid = "11111111-0000-4000-8000-000000000011"
	n := &Node{state: &stateFile{Sessions: map[string]sessionState{}}}
	n.liveTurnHook(hookInput{HookEventName: "UserPromptSubmit", SessionID: sid, PromptID: "prompt-1"})
	displayRaw, _ := json.Marshal(displayInput{
		HookEventName: "MessageDisplay",
		SessionID:     sid,
		PromptID:      "prompt-1",
		TurnID:        "turn-1",
		MessageID:     "msg-1",
		Index:         0,
		Delta:         "First chunk",
	})
	n.displayHook(displayRaw)

	items := []proto.Item{{ID: "a1#0", Seq: 1, TS: time.Now(), Role: "assistant", Kind: proto.KindText, Text: "First chunk"}}
	if !n.ttft.annotate(sid, items, true) || items[0].TTFTMS == nil || *items[0].TTFTMS < 0 {
		t.Fatalf("expected non-negative ttft_ms from live hook path, got %+v", items[0].TTFTMS)
	}
}

func TestCodexNotificationStreamingMeasuresTTFT(t *testing.T) {
	n := &Node{}
	p := &assistantProc{id: "cx-1", agent: "codex"}
	cx := &codexConn{n: n, p: p, threadID: "cx-1", files: map[string][]string{}}

	n.ttft.arm("cx-1", time.Now().Add(-85*time.Millisecond), "turn-1")
	cx.notification("item/agentMessage/delta", json.RawMessage(`{"threadId":"cx-1","turnId":"turn-1","delta":"Hello"}`))

	items := []proto.Item{{ID: "m1#0", Seq: 1, TS: time.Now(), Role: "assistant", Kind: proto.KindText, Text: "Hello"}}
	if !n.ttft.annotate("cx-1", items, true) || items[0].TTFTMS == nil || *items[0].TTFTMS < 80 {
		t.Fatalf("expected Codex streaming TTFT >= 80ms, got %+v", items[0].TTFTMS)
	}
}

func TestClaudeReadAssistantStreamEventMeasuresTTFT(t *testing.T) {
	const sid = "11111111-0000-4000-8000-000000000012"
	n := &Node{logger: log.New(io.Discard, "", 0)}
	p := &assistantProc{id: sid, agent: "claude", ready: make(chan struct{}), exited: make(chan struct{})}
	n.ttft.arm(sid, time.Now().Add(-60*time.Millisecond), "")

	stream := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":100}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"step 1"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"done"}}}`,
	}, "\n") + "\n"
	n.readAssistant(p, strings.NewReader(stream))

	items := []proto.Item{{ID: "c1#0", Seq: 1, TS: time.Now(), Role: "assistant", Kind: proto.KindThinking, Text: "step 1"}}
	if !n.ttft.annotate(sid, items, true) || items[0].TTFTMS == nil || *items[0].TTFTMS < 55 {
		t.Fatalf("expected Claude streaming TTFT >= 55ms, got %+v", items[0].TTFTMS)
	}
}

func TestRuntimeSendArmsBeforeRPCAndDisarmsOnRefusal(t *testing.T) {
	n, _ := runtimeNode(t)
	const (
		fastID    = "01a1b2c3-0000-7000-8000-000000000091"
		refusedID = "01a1b2c3-0000-7000-8000-000000000092"
		queuedID  = "01a1b2c3-0000-7000-8000-000000000093"
	)
	rt := dialRuntime(t, n, map[string]any{
		"runtime": "pi-web",
		"agent":   "pi",
		"caps":    map[string]any{"send": true},
	})
	_ = rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": fastID, "status": "idle"},
		{"session_id": refusedID, "status": "idle"},
		{"session_id": queuedID, "status": "working"},
	}})
	waitFor(t, "runtime sessions listed", func() bool {
		return n.runtimes.ownerOf(fastID) != nil && n.runtimes.ownerOf(refusedID) != nil && n.runtimes.ownerOf(queuedID) != nil
	})

	rt.answer = func(method string, params json.RawMessage) (any, error) {
		if method != "session.send" {
			return map[string]any{"ok": true}, nil
		}
		var body map[string]any
		_ = json.Unmarshal(params, &body)
		switch body["session_id"] {
		case fastID:
			// Emit streaming delta BEFORE session.send returns.
			_ = rt.peer.Notify("session.delta", map[string]any{"session_id": fastID, "text": "Early token"})
			waitFor(t, "node captured early runtime delta", func() bool { return n.ttft.has(fastID) })
			return map[string]any{"ok": true}, nil
		case refusedID:
			return nil, &rpc.Error{Code: "not_running", Message: "stopped", Data: json.RawMessage(`{"delivered":"none"}`)}
		case queuedID:
			return map[string]any{"ok": true, "queued": true}, nil
		}
		return map[string]any{"ok": true}, nil
	}

	// 1. Early delta during runtime session.send RPC is captured and survives noteRuntimeWorking.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": fastID, "text": "go"}); err != nil {
		t.Fatalf("fast runtime send: %v", err)
	}
	if h := n.runtimes.ownerOf(fastID); h != nil {
		n.noteRuntimeWorking(h, fastID, nil)
	}
	fastItems := []proto.Item{{ID: "r1#0", Seq: 1, TS: time.Now(), Role: "assistant", Kind: proto.KindText, Text: "Early token"}}
	if !n.ttft.annotate(fastID, fastItems, true) || fastItems[0].TTFTMS == nil || *fastItems[0].TTFTMS < 0 {
		t.Fatalf("expected runtime TTFT captured before send ack, got %+v", fastItems[0].TTFTMS)
	}

	// 2. Definitive send refusal disarms tracker so subsequent deltas are ignored.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": refusedID, "text": "go"}); err == nil {
		t.Fatal("expected refusal error")
	}
	if _, ok := n.ttft.firstToken(refusedID, time.Now(), ""); ok {
		t.Fatal("definitive runtime send refusal should have disarmed TTFT tracker")
	}

	// 3. Queued runtime send disarms tracker so it does not steal an active turn's TTFT.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": queuedID, "text": "next"}); err != nil {
		t.Fatalf("queued runtime send: %v", err)
	}
	if _, ok := n.ttft.firstToken(queuedID, time.Now(), ""); ok {
		t.Fatal("queued runtime send should have disarmed TTFT tracker")
	}
}
