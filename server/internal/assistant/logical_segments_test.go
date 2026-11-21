package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func logicalPair(t *testing.T, engine string, source ...string) (*Service, *fakeHub, *db.DB, *db.LogicalConversation) {
	t.Helper()
	s, f, d, _ := historyFixture(t, func(string, ...any) {})
	src := "claude"
	if len(source) > 0 {
		src = source[0]
	}
	ctx := t.Context()
	for _, method := range []string{"assistant.segment.restore", "assistant.segment.retire"} {
		f.answerNode(method, func(string, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
	}
	f.setSessions("server", proto.Session{SessionID: "side-chat", Assistant: true, Agent: src, Rev: 100}, proto.Session{SessionID: "target-chat", Assistant: true, Agent: engine, Rev: 100})
	if err := d.AdoptConversation(ctx, "server", "side-chat", "one", src, "h1"); err != nil {
		t.Fatal(err)
	}
	p, _ := json.Marshal(logicalSwitchRequest{OperationID: "operation", ExpectedEpoch: 1, MachineID: "server", Launcher: engine, Model: "target-model", Effort: "medium"})
	if err := d.BeginConversationSwitch(ctx, "server", "side-chat", db.ConversationSwitch{OperationID: "operation", ExpectedEpoch: 1, SourceSegment: "one", Request: p}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationHandoff(ctx, "server", "side-chat", "operation", json.RawMessage(`{"summary":"fake source summary","recent":[{"role":"user","text":"same conversation"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.PrepareConversationSegment(ctx, "server", "side-chat", "operation", conversationlog.Segment{ID: "two", MachineID: "server", SessionID: "target-chat", Engine: engine, SourceHistory: "h2", Launcher: engine, Model: "target-model", Effort: "medium"}, 6); err != nil {
		t.Fatal(err)
	}
	c, err := d.LogicalConversation(ctx, "server", "side-chat")
	if err != nil {
		t.Fatal(err)
	}
	return s, f, d, c
}
func activatePair(t *testing.T, d *db.DB) *db.LogicalConversation {
	t.Helper()
	ctx := t.Context()
	if err := d.ActivateConversationSegment(ctx, "server", "side-chat", "operation", 1); err != nil {
		t.Fatal(err)
	}
	c, err := d.LogicalConversation(ctx, "server", "side-chat")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLogicalHistoryProjectsDatabaseAcrossSegmentsAndFrozenTail(t *testing.T) {
	s, _, d, _ := logicalPair(t, "pi")
	ctx := t.Context()
	first := []proto.Item{{ID: "line1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: "first"}, {ID: "line6#0", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "last"}, {ID: "line9#0", Seq: 9, Role: "assistant", Kind: proto.KindText, Text: "retired append"}}
	second := []proto.Item{{ID: "line1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: "next"}, {ID: "line4#0", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "reply", Image: &proto.Image{Mime: "image/png", Data: "eA=="}}}
	for _, p := range []db.ConversationPage{{MachineID: "server", SessionID: "side-chat", History: "h1", Revision: 100, ThroughSeq: 9, Tail: true, Items: first}, {MachineID: "server", SessionID: "target-chat", History: "h2", Revision: 100, ThroughSeq: 4, Tail: true, Items: second}} {
		if err := d.PutConversationPage(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	c := activatePair(t, d)
	after := 0
	page, err := s.logicalHistoryPage(ctx, d, c, historyParams{After: &after, LazyImages: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 || page.Items[0].ID != "line1#0" || page.Items[2].ID != "segment:two:line1#0" || page.Items[2].Seq != 7 || intValue(page.ToSeq) != 10 || page.MoreAfter {
		t.Fatalf("projection: %+v", page)
	}
	for _, it := range page.Items {
		if it.Text == "retired append" {
			t.Fatal("retired tail leaked")
		}
	}
	if err = d.PutSetting(ctx, conversationHistoryKey, map[string]bool{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	page, err = s.logicalHistoryPage(ctx, d, c, historyParams{After: &after, LazyImages: true})
	if err != nil || len(page.Items) != 4 {
		t.Fatalf("phase-1 rollback disabled logical reads: %+v %v", page, err)
	}
	raw, err := s.logicalFullItem(ctx, c, "session.image", historyParams{ItemID: "segment:two:line4#0"})
	if err != nil || !strings.Contains(string(raw), "eA==") {
		t.Fatalf("logical image: %s %v", raw, err)
	}
	before := 8
	page, err = s.logicalHistoryPage(ctx, d, c, historyParams{Before: &before, Turns: 2, FoldWork: true})
	if err != nil {
		t.Fatal(err)
	}
	if intValue(page.ToSeq) > 7 || len(page.Items) == 0 {
		t.Fatalf("cross-boundary backwards page: %+v", page)
	}
	// A new manager reconstructs exactly the same stable projection from Postgres.
	restarted := withSettings(t, s.hub.(*fakeHub), New(Options{Hub: s.hub, Store: func() Store { return d }}))
	raw, handled, err := restarted.logicalCall(ctx, "server", DeviceSessionItems, json.RawMessage(`{"session_id":"side-chat","after_seq":6}`))
	if !handled || err != nil || !strings.Contains(string(raw), "segment:two:line1#0") {
		t.Fatalf("restart: %s %v %v", raw, handled, err)
	}
	if _, handled, err := s.logicalCall(ctx, "server", DeviceSessionItems, json.RawMessage(`{"session_id":"target-chat"}`)); !handled || err == nil {
		t.Fatal("private target became public")
	}
}

func TestLogicalFirstInputPolicyAndForwardRollback(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			s, f, d, _ := logicalPair(t, "codex")
			ctx := t.Context()
			activatePair(t, d)
			setSettings(t, s, func(cfg *Settings) { cfg.Leaders = []string{"side-chat"} })
			sends := 0
			f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
				sends++
				var p map[string]any
				_ = json.Unmarshal(raw, &p)
				if p["session_id"] != "target-chat" || p["client_msg_id"] != "original-id" || p["supervision_instructions"] == "" || !strings.Contains(fmt.Sprint(p["text"]), "fake source summary") {
					t.Fatalf("first delivery lost logical role/context/identity: %s", raw)
				}
				if refused {
					return nil, &rpc.Error{Code: "assistant_supervision_runtime", Message: "fake policy refusal", Data: json.RawMessage(`{"delivered":"none"}`)}
				}
				return map[string]any{"session_id": "target-chat"}, nil
			})
			raw, handled, err := s.logicalCall(ctx, "server", DeviceAssistantSend, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"original-id","text":"continue"}`))
			if err != nil || !handled || !strings.Contains(string(raw), `"queued":true`) {
				t.Fatalf("queue receipt: %s %v", raw, err)
			}
			c, _ := d.LogicalConversation(ctx, "server", "side-chat")
			err = s.drainLogicalInput(ctx, d, c)
			if refused && err == nil || !refused && err != nil {
				t.Fatalf("delivery result: %v", err)
			}
			c, _ = d.LogicalConversation(ctx, "server", "side-chat")
			a, ok := c.Active()
			if !ok || sends != 1 {
				t.Fatalf("delivery repeated: %+v sends %d", c, sends)
			}
			if refused {
				if c.Epoch != 3 || a.ID != "one" {
					t.Fatalf("rollback did not go forward: %+v", c)
				}
			} else {
				if c.Epoch != 2 || a.ID != "two" || !c.Segments[0].ClosePending {
					t.Fatalf("accepted source closure: %+v", c)
				}
			}
		})
	}
}

func TestLogicalRetiredAuthorityAndRealTimeProjection(t *testing.T) {
	s, f, d, _ := logicalPair(t, "agy")
	ctx := t.Context()
	c := activatePair(t, d)
	if _, _, _, err := s.logicalCaller(ctx, "server", "side-chat"); err == nil {
		t.Fatal("retired caller retained logical authority")
	}
	m, id, _, err := s.logicalCaller(ctx, "server", "target-chat")
	if err != nil || m != "server" || id != "side-chat" {
		t.Fatalf("active caller: %s/%s %v", m, id, err)
	}
	if !s.NodeEvent("server", "session.echo", json.RawMessage(`{"session_id":"target-chat","client_msg_id":"persona-a2a-task-1"}`)) {
		t.Fatal("A2A echoes should retain the physical session address")
	}
	s.NodeEvent("server", "session.items", json.RawMessage(`{"session_id":"target-chat","history":"h2","from_seq":0,"to_seq":2,"items":[{"id":"line1#0","seq":1,"kind":"text","role":"assistant","text":"live"}]}`))
	f.mu.Lock()
	told := append([]told(nil), f.told...)
	f.mu.Unlock()
	found := false
	for _, event := range told {
		if event.event != devicewire.EventNode {
			continue
		}
		var p devicewire.NodeEvent
		_ = json.Unmarshal(event.params, &p)
		if strings.Contains(string(p.Params), "segment:two:line1#0") {
			var page struct {
				SessionID string `json:"session_id"`
				historyPage
			}
			_ = json.Unmarshal(p.Params, &page)
			if p.MachineID != "server" || page.SessionID != "side-chat" || intValue(page.FromSeq) != 6 || intValue(page.ToSeq) != 8 || page.History != c.History {
				t.Fatalf("live span: %s", p.Params)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("active segment live event did not reach public transport")
	}
	f.mu.Lock()
	count := len(f.told)
	f.mu.Unlock()
	s.NodeEvent("server", "hook.event", json.RawMessage(`{"session_id":"side-chat","event":"Stop"}`))
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.told) != count {
		t.Fatal("retired hook settled the active turn")
	}
}

func TestLogicalUnknownTargetAndPausedSwitchSetting(t *testing.T) {
	s, f, d, _ := logicalPair(t, "pi")
	ctx := t.Context()
	// Resolve the prepared operation before trying an explicit new one.
	if err := d.ResolveConversationSwitch(ctx, "server", "side-chat", "operation", "rejected", ""); err != nil {
		t.Fatal(err)
	}
	f.answerNode("assistant.segment.check", func(_ string, _ json.RawMessage) (any, error) {
		return segmentTarget{Registered: true, Engine: "future-unknown", Prepare: true, SupervisionPolicy: true}, nil
	})
	_, err := s.beginLogicalSwitch(ctx, "server", "side-chat", logicalSwitchRequest{OperationID: "new", ExpectedEpoch: 1, MachineID: "server", Launcher: "unknown"})
	if err == nil {
		t.Fatal("unknown engine allowed")
	}
	if err = d.PutSetting(ctx, conversationSegmentsKey, map[string]bool{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	_, err = s.beginLogicalSwitch(ctx, "server", "side-chat", logicalSwitchRequest{OperationID: "new", ExpectedEpoch: 1, MachineID: "server", Launcher: "pi"})
	if err == nil {
		t.Fatal("switch-disable did not stop new switches")
	}
	if c, err := d.LogicalConversation(ctx, "server", "side-chat"); err != nil || c == nil {
		t.Fatal("switch-disable removed mappings")
	}
}

func TestLogicalInDoubtSendIsNeverRepeated(t *testing.T) {
	s, f, d, _ := logicalPair(t, "pi")
	ctx := t.Context()
	activatePair(t, d)
	count := 0
	f.answerNode(DeviceAssistantSend, func(_ string, _ json.RawMessage) (any, error) {
		count++
		return nil, errors.New("fake connection lost after acceptance")
	})
	f.answerNode("assistant.segment.input", func(_ string, _ json.RawMessage) (any, error) { return map[string]bool{"accepted": false}, nil })
	for _, client := range []string{"one", "two"} {
		raw, _ := json.Marshal(map[string]any{"session_id": "side-chat", "client_msg_id": client, "text": "same words"})
		if _, _, err := s.logicalCall(ctx, "server", DeviceAssistantSend, raw); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := d.LogicalConversation(ctx, "server", "side-chat")
	_ = s.drainLogicalInput(ctx, d, c)
	for range 3 {
		c, _ = d.LogicalConversation(ctx, "server", "side-chat")
		if err := s.drainLogicalInput(context.Background(), d, c); err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("ambiguous input repeated or overtaken %d", count)
	}
}

func TestLogicalSummaryWaitsOnPausedFakeModel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := waitLogicalSummary(ctx, func() (assistantwire.ModelCompleteResult, error) {
		calls++
		cancel()
		return assistantwire.ModelCompleteResult{}, &rpc.Error{Code: assistantwire.CodeModelPaused, Message: "fake quota pause", Data: json.RawMessage(`{"until":"2099-01-01T00:00:00Z"}`)}
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("pause polled the fake model: %d %v", calls, err)
	}
	calls = 0
	out, err := waitLogicalSummary(t.Context(), func() (assistantwire.ModelCompleteResult, error) {
		calls++
		return assistantwire.ModelCompleteResult{Text: "fake summary"}, nil
	})
	if err != nil || calls != 1 || out.Text != "fake summary" {
		t.Fatalf("summary: %+v %v", out, err)
	}
}

func TestLogicalRuntimeEngineTransitionsPreserveRoleAndMessageIdentity(t *testing.T) {
	engines := []string{"claude", "codex", "agy", "pi", "opencode"}
	for i, source := range engines {
		target := engines[(i+1)%len(engines)]
		t.Run(source+"-to-"+target, func(t *testing.T) {
			s, f, d, _ := logicalPair(t, target, source)
			ctx := t.Context()
			setSettings(t, s, func(cfg *Settings) { cfg.Leaders = []string{"side-chat"} })
			c := activatePair(t, d)
			delivered := 0
			f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
				delivered++
				var p map[string]any
				_ = json.Unmarshal(raw, &p)
				if p["session_id"] != "target-chat" || p["client_msg_id"] != "preserved" || p["supervision_instructions"] == "" {
					t.Fatalf("contract input: %s", raw)
				}
				return map[string]string{"session_id": "target-chat"}, nil
			})
			queued, _ := json.Marshal(logicalInput{Method: DeviceAssistantSend, Params: map[string]any{"text": "continue", "client_msg_id": "preserved"}})
			if err := d.EnqueueConversationInput(ctx, c.MachineID, c.SessionID, "preserved", c.Epoch, queued); err != nil {
				t.Fatal(err)
			}
			if err := s.drainLogicalInput(ctx, d, c); err != nil {
				t.Fatal(err)
			}
			restored, err := d.LogicalConversation(ctx, "server", "target-chat")
			if err != nil {
				t.Fatal(err)
			}
			active, _ := restored.Active()
			if delivered != 1 || active.Engine != target || restored.SessionID != "side-chat" || s.settings(ctx).Leaders[0] != "side-chat" {
				t.Fatalf("logical role changed: %+v", restored)
			}
		})
	}
}

func TestLogicalOperationReceiptIsScopedToConversation(t *testing.T) {
	first := &db.LogicalConversation{MachineID: "m", SessionID: "first"}
	second := &db.LogicalConversation{MachineID: "m", SessionID: "second"}
	if logicalNodeOperation(first, "same-id") == logicalNodeOperation(second, "same-id") {
		t.Fatal("two conversations shared a physical start receipt")
	}
	if logicalNodeOperation(first, "same-id") != logicalNodeOperation(first, "same-id") {
		t.Fatal("receipt changed after restart")
	}
}

func TestLogicalQueuedInputsKeepTheTargetConfigAfterFirstAcceptance(t *testing.T) {
	s, f, d, _ := logicalPair(t, "codex")
	ctx := t.Context()
	c := activatePair(t, d)
	for _, id := range []string{"first", "second"} {
		payload, _ := json.Marshal(logicalInput{Method: DeviceAssistantSend, Params: map[string]any{"text": "continue", "client_msg_id": id, "launcher": "old-claude", "model": "old-model", "effort": "high", "config_dir": "/fake/source/config", "runtime": "old-runtime"}})
		if err := d.EnqueueConversationInput(ctx, c.MachineID, c.SessionID, id, c.Epoch, payload); err != nil {
			t.Fatal(err)
		}
	}
	var received []string
	f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["launcher"] != "codex" || p["model"] != "target-model" || p["effort"] != "medium" || p["config_dir"] != nil || p["runtime"] != nil {
			t.Fatalf("retired execution config leaked: %s", raw)
		}
		received = append(received, p["client_msg_id"].(string))
		return map[string]bool{"ok": true}, nil
	})
	for range 2 {
		if err := s.drainLogicalInput(ctx, d, c); err != nil {
			t.Fatal(err)
		}
		var err error
		c, err = d.LogicalConversation(ctx, "server", "side-chat")
		if err != nil {
			t.Fatal(err)
		}
		if c.Switch != nil {
			t.Fatal("acceptance did not settle switch")
		}
	}
	if fmt.Sprint(received) != "[first second]" {
		t.Fatalf("delivery order: %v", received)
	}
}

func TestLogicalModesPersistOnlyAfterPhysicalAcknowledgement(t *testing.T) {
	s, f, d, _ := logicalPair(t, "codex")
	ctx := t.Context()
	activatePair(t, d)
	if err := d.ResolveConversationSwitch(ctx, "server", "side-chat", "operation", "accepted", ""); err != nil {
		t.Fatal(err)
	}
	fail := true
	f.answerNode("session.modes", func(_ string, raw json.RawMessage) (any, error) {
		if !strings.Contains(string(raw), `"session_id":"target-chat"`) {
			t.Fatalf("physical route: %s", raw)
		}
		if fail {
			return nil, errors.New("fake mode refusal")
		}
		return map[string]bool{"ok": true}, nil
	})
	for _, refused := range []bool{true, false} {
		fail = refused
		_, handled, err := s.logicalCall(ctx, "server", "session.modes", json.RawMessage(`{"session_id":"side-chat","model":"new-model","effort":"high","permission_mode":"default"}`))
		if !handled || (err != nil) != refused {
			t.Fatalf("modes result: %v %v", handled, err)
		}
		c, _ := d.LogicalConversation(ctx, "server", "side-chat")
		a, _ := c.Active()
		if refused && a.Model != "target-model" || !refused && (a.Model != "new-model" || a.Effort != "high" || a.PermissionMode != "default") {
			t.Fatalf("persisted modes: %+v", a)
		}
	}
}

func TestLogicalPreparationRecoversTheDurablePositiveReceipt(t *testing.T) {
	s, f, d, _ := logicalPair(t, "codex")
	ctx := t.Context()
	if err := d.ResolveConversationSwitch(ctx, "server", "side-chat", "operation", "rejected", ""); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(logicalSwitchRequest{OperationID: "recover", ExpectedEpoch: 1, MachineID: "server", Launcher: "codex"})
	if err := d.BeginConversationSwitch(ctx, "server", "side-chat", db.ConversationSwitch{OperationID: "recover", ExpectedEpoch: 1, SourceSegment: "one", Request: request}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationHandoff(ctx, "server", "side-chat", "recover", json.RawMessage(`{"summary":"saved summary"}`)); err != nil {
		t.Fatal(err)
	}
	if won, err := d.ClaimConversationPreparation(ctx, "server", "side-chat", "recover"); err != nil || !won {
		t.Fatalf("claim: %v %v", won, err)
	}
	if err := d.RecordConversationPreparedCandidate(ctx, db.ConversationPreparedCandidate{MachineID: "server", SessionID: "recovered-target", LogicalMachine: "server", LogicalSession: "side-chat", OperationID: "recover"}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"assistant.segment.prepare", "assistant.segment.receipt"} {
		f.answerNode(method, func(string, json.RawMessage) (any, error) {
			t.Fatal("persisted positive receipt was ignored")
			return nil, errors.New("node receipt cache lost")
		})
	}
	f.answerNode("assistant.segment.check", func(string, json.RawMessage) (any, error) {
		return segmentTarget{Registered: true, Engine: "codex", Prepare: true, SupervisionPolicy: true}, nil
	})
	// Recreate the manager's projection book; no in-memory start receipt survives.
	s.logical = logicalDeviceState{}
	c, _ := d.LogicalConversation(ctx, "server", "side-chat")
	if err := s.advanceLogicalConversation(ctx, d, c); err != nil {
		t.Fatal(err)
	}
	c, _ = d.LogicalConversation(ctx, "server", "side-chat")
	a, _ := c.Active()
	if a.SessionID != "recovered-target" || c.Epoch != 2 {
		t.Fatalf("recovery: %+v", c)
	}
}

func TestLogicalExecutionReferencesNeverSelectRetiredItems(t *testing.T) {
	s, f, d, _ := logicalPair(t, "codex")
	activatePair(t, d)
	calls := 0
	f.answerNode("session.fork", func(_ string, raw json.RawMessage) (any, error) {
		calls++
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["session_id"] != "target-chat" || p["at_item_id"] != "line1#0" {
			t.Fatalf("source control reference: %s", raw)
		}
		return map[string]string{"session_id": "new-branch"}, nil
	})
	for _, ref := range []string{"line1#0", "segment:retired:line1#0", "segment:two:line1#0"} {
		raw, _ := json.Marshal(map[string]any{"session_id": "side-chat", "at_item_id": ref})
		reply, handled, err := s.logicalCall(t.Context(), "server", "session.fork", raw)
		if err == nil && !strings.Contains(string(reply), "new-branch") {
			t.Fatalf("fork identity was rewritten: %s", reply)
		}
		if !handled || (err == nil) != (ref == "segment:two:line1#0") {
			t.Fatalf("reference %q: %v %v", ref, handled, err)
		}
	}
	if calls != 1 {
		t.Fatalf("physical calls: %d", calls)
	}
}

func TestLogicalSendOnceStaysOnPublicHubLedger(t *testing.T) {
	s, _, d, _ := logicalPair(t, "codex")
	activatePair(t, d)
	for _, method := range []string{DeviceSessionSendOnce, assistantwire.DeviceAssistantAnswerAgain} {
		_, handled, err := s.logicalCall(t.Context(), "server", method, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"public-ledger"}`))
		if handled || err != nil {
			t.Fatalf("%s bypassed hub ledger: %v %v", method, handled, err)
		}
	}
}

func TestLogicalRootTransportSurvivesItsPhysicalMachine(t *testing.T) {
	s, _, d, _ := logicalPair(t, "codex")
	ctx := t.Context()
	if err := d.AdoptConversation(ctx, "removed-source", "retained-chat", "retained-first", "claude", "retained-history"); err != nil {
		t.Fatal(err)
	}
	if err := d.BeginConversationSwitch(ctx, "removed-source", "retained-chat", db.ConversationSwitch{OperationID: "switch", ExpectedEpoch: 1, SourceSegment: "retained-first", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := d.PrepareConversationSegment(ctx, "removed-source", "retained-chat", "switch", conversationlog.Segment{ID: "retained-second", MachineID: "server", SessionID: "active-retained", Engine: "codex"}, 7); err != nil {
		t.Fatal(err)
	}
	if err := d.ActivateConversationSegment(ctx, "removed-source", "retained-chat", "switch", 1); err != nil {
		t.Fatal(err)
	}
	raw := devicewire.States{Machines: []devicewire.State{{Machine: proto.Machine{ID: "server"}, Connected: true}}}
	s.logicalChanged() // direct DB writes above stand in for a completed manager activation
	projected := s.logicalProjectStates(raw)
	if len(projected.Machines) != 2 || projected.Machines[1].Machine.ID != "removed-source" || projected.Machines[1].TransportFor != "server" || !projected.Machines[1].Connected {
		t.Fatalf("root transport: %+v", projected)
	}
	if len(raw.Machines) != 1 || raw.Machines[0].Machine.ID != "server" {
		t.Fatal("shared physical snapshot mutated")
	}
	physical := newFleetDevs()
	physical.add("server", "Target")
	physical.add("removed-source", "Source").away = true
	logical := &logicalDevices{Devices: physical, s: s}
	if summary, ok := logical.Summary("removed-source"); !ok || !summary.Online || summary.Name != "Source" {
		t.Fatalf("retained offline source hid active transport: %+v", summary)
	}
	for _, summary := range logical.List() {
		if summary.ID == "removed-source" && !summary.Online {
			t.Fatal("conversation inventory remained offline")
		}
	}
	if shown, ok := logical.Shown("removed-source"); !ok || !shown.Listed {
		t.Fatalf("active target's list was hidden: %+v", shown)
	}
}

func TestLogicalForwardRollbackRestoresALaterSourceSegment(t *testing.T) {
	s, f, d, _ := logicalPair(t, "codex")
	ctx := t.Context()
	activatePair(t, d)
	if err := d.ResolveConversationSwitch(ctx, "server", "side-chat", "operation", "accepted", ""); err != nil {
		t.Fatal(err)
	}
	// Start the next switch from segment two, rather than from the original root.
	p, _ := json.Marshal(logicalSwitchRequest{OperationID: "second-switch", ExpectedEpoch: 2, MachineID: "server", Launcher: "pi"})
	if err := d.BeginConversationSwitch(ctx, "server", "side-chat", db.ConversationSwitch{OperationID: "second-switch", ExpectedEpoch: 2, SourceSegment: "two", Request: p}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationHandoff(ctx, "server", "side-chat", "second-switch", json.RawMessage(`{"summary":"second summary"}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.PrepareConversationSegment(ctx, "server", "side-chat", "second-switch", conversationlog.Segment{ID: "three", MachineID: "server", SessionID: "third-chat", Engine: "pi", Launcher: "pi"}, 3); err != nil {
		t.Fatal(err)
	}
	if err := d.ActivateConversationSegment(ctx, "server", "side-chat", "second-switch", 2); err != nil {
		t.Fatal(err)
	}
	sourceRetired := true
	f.answerNode("assistant.segment.restore", func(_ string, raw json.RawMessage) (any, error) {
		if strings.Contains(string(raw), `"session_id":"target-chat"`) {
			sourceRetired = false
		}
		return map[string]bool{"ok": true}, nil
	})
	delivered := 0
	f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		if params["session_id"] == "third-chat" {
			return nil, &rpc.Error{Code: "assistant_supervision_runtime", Message: "fake refusal", Data: json.RawMessage(`{"delivered":"none"}`)}
		}
		if params["session_id"] != "target-chat" || sourceRetired || params["launcher"] != "codex" {
			t.Fatalf("unrestored rollback: %s", raw)
		}
		delivered++
		return map[string]bool{"ok": true}, nil
	})
	_, _, err := s.logicalCall(ctx, "server", DeviceAssistantSend, json.RawMessage(`{"session_id":"side-chat","text":"continue","client_msg_id":"recovered-input"}`))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := d.LogicalConversation(ctx, "server", "side-chat")
	if err = s.drainLogicalInput(ctx, d, c); err == nil {
		t.Fatal("expected target refusal")
	}
	c, _ = d.LogicalConversation(ctx, "server", "side-chat")
	a, _ := c.Active()
	if a.ID != "two" || c.Epoch != 4 || sourceRetired {
		t.Fatalf("later-source rollback: %+v retired=%v", c, sourceRetired)
	}
	if err = s.drainLogicalInput(ctx, d, c); err != nil || delivered != 1 {
		t.Fatalf("restored delivery: %d %v", delivered, err)
	}
}

func TestLogicalInteractionEndEventsAreFencedByPhysicalEpoch(t *testing.T) {
	s, _, d, _ := logicalPair(t, "codex")
	s.devs = &fakeDevices{} // enable the manager-owned card lifecycle
	ctx := t.Context()
	if err := d.PutConversationInteraction(ctx, "retired-card", "server", "side-chat", "server", "side-chat", 1); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishConversationInteraction(ctx, "retired-card"); err != nil {
		t.Fatal(err)
	}
	activatePair(t, d)
	old := proto.Interaction{ID: "retired-card", MachineID: "server", SessionID: "side-chat", Kind: proto.IxPermission}
	active := proto.Interaction{ID: "active-card", MachineID: "server", SessionID: "side-chat", Kind: proto.IxPermission}
	if err := d.ReserveConversationInteraction(ctx, active, "server", "side-chat", "server", "target-chat", 2); err != nil {
		t.Fatal(err)
	}
	s.fleet.mu.Lock()
	s.ixMapLocked()[old.ID] = &pending{ix: old, done: make(chan proto.Resolution, 1)}
	s.ixMapLocked()[active.ID] = &pending{ix: active, done: make(chan proto.Resolution, 1)}
	s.fleet.mu.Unlock()
	s.NodeEvent("server", "interaction.cancel", json.RawMessage(`{"id":"retired-card"}`))
	s.NodeEvent("other-node", "interaction.cancel", json.RawMessage(`{"id":"active-card"}`))
	s.NodeEvent("server", "interaction.cancel", json.RawMessage(`{"id":"active-card","session_id":"side-chat"}`))
	if len(s.pendingInteractions()) != 2 {
		t.Fatal("stale or wrong-source event settled a card")
	}
	s.NodeEvent("server", "interaction.cancel", json.RawMessage(`{"id":"active-card","session_id":"target-chat"}`))
	s.fleet.mu.Lock()
	oldPending, activePending := s.fleet.interactions[old.ID] != nil, s.fleet.interactions[active.ID] != nil
	s.fleet.mu.Unlock()
	if !oldPending || activePending {
		t.Fatal("active physical cancellation was not isolated")
	}
}

func TestLogicalToolPermissionIsVerifiedBeforeRemapping(t *testing.T) {
	s, _, d, _ := logicalPair(t, "codex")
	activatePair(t, d)
	args := json.RawMessage(`{"detail":"fake report"}`)
	for _, session := range []string{"target-chat", "side-chat", "another-session"} {
		t.Run(session, func(t *testing.T) {
			proof := testSourcePermission(session, "report_to_supervisor", args, "bypass")
			raw, _ := json.Marshal(map[string]any{"session_id": "target-chat", "name": "report_to_supervisor", "args": args, "source_permission": proof})
			_, handled, err := s.logicalNodeRequest(t.Context(), "server", "mcp.call", raw, func(_ context.Context, machine, method string, raw json.RawMessage) (any, bool, error) {
				var p struct {
					Session string                 `json:"session_id"`
					Proof   proto.SourcePermission `json:"source_permission"`
				}
				_ = json.Unmarshal(raw, &p)
				if machine != "server" || p.Session != "side-chat" {
					t.Fatalf("logical tool route: %s", raw)
				}
				if p.Proof.Matches("side-chat", "report_to_supervisor", args) != (session == "target-chat") {
					t.Fatalf("unbound proof acquired logical authority: %s", raw)
				}
				return nil, true, nil
			})
			if err != nil || !handled {
				t.Fatalf("tool: %v %v", handled, err)
			}
		})
	}
}

func TestLogicalPhysicalDeliveryRequiresPositiveRefusalEvidence(t *testing.T) {
	for _, tc := range []struct {
		err     error
		refused bool
	}{
		{errors.New("connection lost after physical dispatch"), false},
		{context.DeadlineExceeded, false},
		{rpc.ErrClosed, false},
		{apierr.ErrOffline, true},
		{rpc.ErrNotSent, true},
		{&rpc.Error{Code: "assistant_supervision_runtime", Message: "policy refused", Data: json.RawMessage(`{"delivered":"none"}`)}, true},
	} {
		if got := logicalNotTaken(tc.err); got != tc.refused {
			t.Fatalf("%v: refused=%v want=%v", tc.err, got, tc.refused)
		}
	}
}

func TestLogicalApprovalPublicationIsSanitizedAndEpochFenced(t *testing.T) {
	s, _, d, _ := logicalPair(t, "codex")
	ctx := context.WithValue(t.Context(), logicalAuthenticatedKey{}, logicalAuthenticated{Machine: "server", Session: "side-chat", PhysicalMachine: "server", PhysicalSession: "side-chat", Epoch: 1})
	// A reservation before a pending switch is inert. No raw card is published.
	ix := proto.Interaction{ID: "late-source-card", MachineID: "server", SessionID: "side-chat", Kind: proto.IxPermission}
	if err := d.ReserveConversationInteraction(ctx, ix, "server", "side-chat", "server", "side-chat", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.logicalPublishInteraction(ctx, ix); err == nil {
		t.Fatal("source card appeared after switch began")
	}
	if row, err := d.InteractionRecord(ctx, ix.ID); err != nil || row != nil {
		t.Fatalf("rejected publication persisted: %+v %v", row, err)
	}
	activatePair(t, d)
	ctx = context.WithValue(t.Context(), logicalAuthenticatedKey{}, logicalAuthenticated{Machine: "server", Session: "side-chat", PhysicalMachine: "server", PhysicalSession: "target-chat", Epoch: 2})
	ix.ID, ix.Summary, ix.CreatedAt = "active-resumable-card", "sanitized summary", time.Now()
	ix.Resumable = true
	if err := d.ReserveConversationInteraction(ctx, ix, "server", "side-chat", "server", "target-chat", 2); err != nil {
		t.Fatal(err)
	}
	for _, wantFresh := range []bool{true, false} {
		fresh, err := s.logicalPublishInteraction(ctx, ix)
		if err != nil || fresh != wantFresh {
			t.Fatalf("fresh publication=%v want=%v err=%v", fresh, wantFresh, err)
		}
	}
	stored, err := d.InteractionRecord(ctx, ix.ID)
	if err != nil || stored == nil || stored.Interaction.Summary != "sanitized summary" {
		t.Fatalf("published card: %+v %v", stored, err)
	}
}

func TestLogicalSwitchSerializesExecutionConfiguration(t *testing.T) {
	s, f, d, _ := logicalPair(t, "codex")
	for _, method := range []string{"session.modes", "session.switch", "session.resume", DeviceAssistantWarm} {
		f.answerNode(method, func(string, json.RawMessage) (any, error) {
			t.Fatal("execution changed during switch")
			return nil, nil
		})
	}
	for phase := range 2 {
		for _, method := range []string{"session.modes", "session.switch", "session.resume", DeviceAssistantWarm} {
			_, handled, err := s.logicalCall(t.Context(), "server", method, json.RawMessage(`{"session_id":"side-chat","model":"changed"}`))
			var refused *rpc.Error
			if !handled || !errors.As(err, &refused) || refused.Code != assistantwire.CodeConversationBusy {
				t.Fatalf("%s during switch: %v %v", method, handled, err)
			}
		}
		if phase == 0 {
			activatePair(t, d)
		}
	}
}

func TestLogicalRetiredCardControlsNeverReachTheActiveSegment(t *testing.T) {
	s, _, d, _ := logicalPair(t, "codex")
	ctx := t.Context()
	if err := d.PutConversationInteraction(ctx, "old-card", "server", "side-chat", "server", "side-chat", 1); err != nil {
		t.Fatal(err)
	}
	activatePair(t, d)
	allow := true
	for _, resolution := range []proto.Resolution{{Allow: &allow}, {Dismiss: true}, {Cancel: true}} {
		called := false
		err := s.logicalInteractionAnswer(ctx, "old-card", resolution, func() error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("retired control escaped its epoch: %+v %v", resolution, err)
		}
	}
}

func TestLogicalPublicationLeavesOrdinaryCardLifetimeUnchanged(t *testing.T) {
	s, _, _, _ := logicalPair(t, "codex")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if fresh, err := s.logicalPublishInteraction(ctx, proto.Interaction{ID: "owner-card", MachineID: "server", SessionID: "side-chat"}); err != nil || fresh {
		t.Fatalf("ordinary card inherited runtime cancellation: %v %v", fresh, err)
	}
	called := false
	if err := s.logicalInteractionAnswer(ctx, "ordinary-owner-card", proto.Resolution{Dismiss: true}, func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("ordinary card cancellation changed: %v %v", called, err)
	}
}

func TestLogicalProviderScopeUsesTheActualExecutionMachine(t *testing.T) {
	s, f, d, _ := logicalPair(t, "codex")
	ctx := t.Context()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "target-only", Agent: "codex", BaseURL: "https://provider.example/v1"}, Share: shareSelected, ScopeMachines: []string{"server"}})
	if err := d.AdoptConversation(ctx, "former-machine", "stable-chat", "scoped-source", "claude", "h"); err != nil {
		t.Fatal(err)
	}
	if err := d.BeginConversationSwitch(ctx, "former-machine", "stable-chat", db.ConversationSwitch{OperationID: "switch", ExpectedEpoch: 1, SourceSegment: "scoped-source", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := d.PrepareConversationSegment(ctx, "former-machine", "stable-chat", "switch", conversationlog.Segment{ID: "scoped-target", MachineID: "server", SessionID: "physical-scoped", Engine: "codex", Launcher: "target-only"}, 2); err != nil {
		t.Fatal(err)
	}
	if err := d.ActivateConversationSegment(ctx, "former-machine", "stable-chat", "switch", 1); err != nil {
		t.Fatal(err)
	}
	// The provider is unavailable on the public root's former machine, but it is
	// valid on the target. Internal sends must not validate against that old root.
	if err := s.call(ctx, "former-machine", DeviceSessionSend, map[string]any{"session_id": "stable-chat", "launcher": "target-only", "text": "continue", "client_msg_id": "scope-message"}, nil); err != nil {
		t.Fatalf("checked scope on retired machine: %v", err)
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "target-only", Agent: "codex", BaseURL: "https://provider.example/v1"}, Share: shareNone})
	f.answerNode("assistant.segment.check", func(string, json.RawMessage) (any, error) {
		t.Fatal("out-of-scope target reached node")
		return nil, nil
	})
	f.answerNode(DeviceAssistantSend, func(string, json.RawMessage) (any, error) {
		t.Fatal("revoked provider reached runtime")
		return nil, nil
	})
	if _, err := s.logicalTarget(ctx, "server", "target-only"); codeOf(err) != proto.CodeLauncherOutOfScope {
		t.Fatalf("target scope: %v", err)
	}
	if _, err := s.logicalPhysical(ctx, "server", DeviceAssistantSend, map[string]any{"session_id": "physical-scoped", "launcher": "target-only", "text": "must not send"}); codeOf(err) != proto.CodeLauncherOutOfScope {
		t.Fatalf("release scope: %v", err)
	}
}

func TestLogicalPreDispatchRestoreFailureRecoversSourceAndUnsentQueue(t *testing.T) {
	s, f, st, _ := logicalPair(t, "codex")
	ctx := t.Context()
	c := activatePair(t, st)
	payload, _ := json.Marshal(logicalInput{Method: DeviceAssistantSend, Params: map[string]any{"text": "continue", "client_msg_id": "unsent"}})
	if err := st.EnqueueConversationInput(ctx, c.MachineID, c.SessionID, "unsent", c.Epoch, payload); err != nil {
		t.Fatal(err)
	}
	f.answerNode("assistant.segment.restore", func(_ string, raw json.RawMessage) (any, error) {
		if strings.Contains(string(raw), "target-chat") {
			return nil, errors.New("fake restore failed before input")
		}
		return map[string]any{"ok": true}, nil
	})
	sends := 0
	f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
		sends++
		if !strings.Contains(string(raw), `"session_id":"side-chat"`) || !strings.Contains(string(raw), `"client_msg_id":"unsent"`) {
			t.Fatalf("unsent input lost original identity: %s", raw)
		}
		return map[string]any{"ok": true}, nil
	})
	if err := s.drainLogicalInput(ctx, st, c); err == nil {
		t.Fatal("restore failure was hidden")
	}
	c, err := st.LogicalConversation(ctx, "server", "side-chat")
	if err != nil || c.Epoch != 3 || c.Switch != nil {
		t.Fatalf("pre-dispatch recovery: %+v %v", c, err)
	}
	if active, ok := c.Active(); !ok || active.ID != "one" || sends != 0 {
		t.Fatalf("source not recovered before input: %+v sends=%d", active, sends)
	}
	if err := s.drainLogicalInput(ctx, st, c); err != nil || sends != 1 {
		t.Fatalf("unsent input was stranded: sends=%d %v", sends, err)
	}
}

func TestLogicalQueuePersistsOnePayloadAndAuthenticatedProvenance(t *testing.T) {
	s, _, st, _ := logicalPair(t, "codex")
	request := devicewire.NodeCall{MachineID: "server", Method: DeviceSessionSend, Params: json.RawMessage(`{"text":"duplicate body"}`), Request: "ephemeral-request", Caller: &devicewire.Caller{Kind: "device", CredentialID: "test-device"}, Submission: &devicewire.Submission{ID: "submission", Kind: "owner", OriginalText: "continue"}, Fallback: &devicewire.NodeCall{MachineID: "retired", Method: DeviceSessionSend}}
	ctx := context.WithValue(t.Context(), logicalTransportKey{}, request)
	if _, handled, err := s.logicalCall(ctx, "server", DeviceSessionSend, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"original","text":"continue"}`)); err != nil || !handled {
		t.Fatalf("queue: %v %v", handled, err)
	}
	activatePair(t, st)
	row, err := st.ClaimConversationInput(t.Context(), "server", "side-chat", 2)
	if err != nil || row == nil {
		t.Fatalf("claim: %+v %v", row, err)
	}
	var queued logicalInput
	if err := json.Unmarshal(row.Payload, &queued); err != nil {
		t.Fatal(err)
	}
	if queued.Transport == nil || len(queued.Transport.Params) != 0 || queued.Transport.Fallback != nil || queued.Transport.Request != "" {
		t.Fatalf("durable queue kept transient transport or duplicate body: %s", row.Payload)
	}
	if queued.Params["text"] != "continue" || queued.Transport.Caller.CredentialID != "test-device" || queued.Transport.Submission.ID != "submission" {
		t.Fatalf("content/provenance changed: %s", row.Payload)
	}
}

func TestLogicalQueueRetryIgnoresRefreshedRoleAndDoesNotReplayAcceptedInput(t *testing.T) {
	s, f, st, _ := logicalPair(t, "codex")
	ctx := t.Context()
	raw := json.RawMessage(`{"session_id":"side-chat","client_msg_id":"retry-role","text":"continue"}`)
	if _, _, err := s.logicalCall(ctx, "server", DeviceSessionSend, raw); err != nil {
		t.Fatal(err)
	}
	// Context inserted by the manager on a later attempt must not conflict with
	// the same client input. The current policy is re-attested during dispatch.
	changed := json.RawMessage(`{"session_id":"side-chat","client_msg_id":"retry-role","text":"continue","supervision_instructions":"changed automatic context"}`)
	if _, _, err := s.logicalCall(ctx, "server", DeviceSessionSend, changed); err != nil {
		t.Fatalf("role refresh changed input identity: %v", err)
	}
	activatePair(t, st)
	input, err := st.ClaimConversationInput(ctx, "server", "side-chat", 2)
	if err != nil || input == nil {
		t.Fatalf("claim: %+v %v", input, err)
	}
	if err := st.ResolveConversationDelivery(ctx, "server", "side-chat", "retry-role", 2, "operation", "accepted"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	before := len(f.sends)
	f.mu.Unlock()
	if _, handled, err := s.logicalCall(ctx, "server", DeviceSessionSend, raw); err != nil || !handled {
		t.Fatalf("receipt replay: %v %v", handled, err)
	}
	f.mu.Lock()
	after := len(f.sends)
	f.mu.Unlock()
	if after != before {
		t.Fatal("accepted input was dispatched again")
	}
}

func TestLogicalInputIdentityDoesNotIncludeAutomaticRole(t *testing.T) {
	first, err := marshalLogicalInput(t.Context(), DeviceSessionSend, map[string]any{"text": "same content", "client_msg_id": "same-id", "supervision_instructions": "old role"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalLogicalInput(t.Context(), DeviceSessionSend, map[string]any{"text": "same content", "client_msg_id": "same-id", "supervision_instructions": "new role"})
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || strings.Contains(string(first), "role") {
		t.Fatalf("automatic policy retained in input identity: %s / %s", first, second)
	}
}

func TestSegmentReadExposesEpochWithoutPhysicalIdsOrStartingAnything(t *testing.T) {
	s, f, d, _ := logicalPair(t, "codex")
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"side-chat"} })
	r := request(s, "GET", "/api/v1/assistant/conversations/side-chat/segments?machine_id=server", "")
	if r.Code != 200 {
		t.Fatalf("snapshot: %d %s", r.Code, r.Body.String())
	}
	var state struct {
		SupervisionRequired bool  `json:"supervision_required"`
		Epoch               int64 `json:"epoch"`
		Switch              struct {
			OperationID string `json:"operation_id"`
		} `json:"switch"`
	}
	if json.Unmarshal(r.Body.Bytes(), &state) != nil || state.Epoch != 1 || !state.SupervisionRequired || state.Switch.OperationID != "operation" || strings.Contains(r.Body.String(), "target-chat") {
		t.Fatalf("snapshot leaked physical identity: %s", r.Body.String())
	}
	activatePair(t, d)
	r = request(s, "GET", "/api/v1/assistant/conversations/side-chat/segments?machine_id=server", "")
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &state) != nil || state.Epoch != 2 {
		t.Fatalf("activation epoch: %d %s", r.Code, r.Body.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) != 0 {
		t.Fatal("snapshot read sent model input")
	}
}
