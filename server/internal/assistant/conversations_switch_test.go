package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// These tests deliberately use only the existing service, database and fake-hub
// APIs. On the baseline the new collection route is absent, so they fail by
// behavior (404), not because a test references an implementation that does not
// compile yet.

type conversationSwitchTestExecution struct {
	Machine        string `json:"machine"`
	Launcher       string `json:"launcher"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	PermissionMode string `json:"permission_mode"`
}

type conversationSwitchTestItem struct {
	Machine        string                          `json:"machine"`
	ConversationID string                          `json:"conversation_id"`
	ExpectedEpoch  int64                           `json:"expected_epoch"`
	Before         conversationSwitchTestExecution `json:"before"`
	Target         conversationSwitchTestExecution `json:"target"`
	OperationID    string                          `json:"operation_id"`
	Outcome        string                          `json:"outcome"`
	Reason         string                          `json:"reason"`
}

type conversationSwitchTestReport struct {
	BatchID   string                       `json:"batch_id"`
	Preview   bool                         `json:"preview"`
	RestoreOf string                       `json:"restore_of"`
	Items     []conversationSwitchTestItem `json:"items"`
}

type conversationSwitchTestRig struct {
	s *Service
	f *fakeHub
	d *db.DB

	mu                   sync.Mutex
	preparations         []map[string]any
	allowNonBatchPrepare bool
}

func newConversationSwitchTestRig(t *testing.T) *conversationSwitchTestRig {
	t.Helper()
	s, f, d, _ := historyFixture(t, func(string, ...any) {})
	r := &conversationSwitchTestRig{s: s, f: f, d: d}
	for _, id := range []string{"fresh-chat", "busy-chat", "target-chat", "restored-chat", "independent-chat"} {
		f.known[id] = true
	}
	r.setTurn(proto.StatusWorking)
	f.answerNode("assistant.segment.check", func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		engine := "codex"
		if p["launcher"] == "sub" || p["launcher"] == "claude" {
			engine = "claude"
		}
		return segmentTarget{Registered: true, Engine: engine, SupervisionPolicy: true, Prepare: true}, nil
	})
	f.answerNode("assistant.segment.prepare", func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		r.mu.Lock()
		allowNonBatch := r.allowNonBatchPrepare
		r.mu.Unlock()
		var state struct {
			Batches []struct {
				Items []conversationSwitchTestItem `json:"items"`
			} `json:"batches"`
		}
		found, err := r.d.Setting(context.Background(), "assistant.conversation_switches", &state)
		if err != nil || (!found && !allowNonBatch) {
			return nil, fmt.Errorf("prepare started before the batch was durable: found=%v err=%v", found, err)
		}
		durableTarget := false
		for _, batch := range state.Batches {
			for _, item := range batch.Items {
				if item.OperationID != "" && item.Target.Launcher == fmt.Sprint(p["launcher"]) && item.Target.Model == fmt.Sprint(p["model"]) && item.Target.Effort == fmt.Sprint(p["effort"]) {
					durableTarget = true
				}
			}
		}
		if !durableTarget && !allowNonBatch {
			return nil, fmt.Errorf("prepare started before its target and operation ID were durable: %+v", p)
		}
		r.mu.Lock()
		r.preparations = append(r.preparations, p)
		id := fmt.Sprintf("prepared-chat-%d", len(r.preparations))
		r.mu.Unlock()
		launcher := fmt.Sprint(p["launcher"])
		agent, model := "codex", fmt.Sprint(p["model"])
		if launcher == "sub" || launcher == "claude" {
			agent = "claude"
		}
		// answerNode handlers already run under f.mu.
		r.f.known[id] = true
		r.f.sessions["server"] = append(r.f.sessions["server"], proto.Session{SessionID: id, MachineID: "server", Assistant: true, Agent: agent, Launcher: launcher, Model: model, RequestedModel: &model, Effort: fmt.Sprint(p["effort"]), PermissionMode: fmt.Sprint(p["permission_mode"]), Status: proto.StatusIdle, State: proto.StateIdle, Rev: 100})
		return map[string]any{"session_id": id, "prepared": true}, nil
	})
	for _, method := range []string{"assistant.segment.retire", "assistant.segment.restore"} {
		f.answerNode(method, func(string, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
	}
	return r
}

func (r *conversationSwitchTestRig) setTurn(busyStatus string) {
	oldModel := "old-model"
	busyState := proto.StateIdle
	if busyStatus == proto.StatusWorking {
		busyState = proto.StateWorking
	}
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	rows := r.f.sessions["server"]
	haveFresh, haveBusy := false, false
	for i := range rows {
		switch rows[i].SessionID {
		case "fresh-chat":
			haveFresh = true
		case "busy-chat":
			haveBusy = true
			rows[i].Status, rows[i].State = busyStatus, busyState
		}
	}
	if !haveFresh {
		rows = append(rows, proto.Session{SessionID: "fresh-chat", MachineID: "server", Assistant: true, Agent: "claude", Launcher: "sub", Model: "transcript-model", RequestedModel: &oldModel, Effort: "high", Status: proto.StatusIdle, State: proto.StateIdle, Rev: 100})
	}
	if !haveBusy {
		rows = append(rows, proto.Session{SessionID: "busy-chat", MachineID: "server", Assistant: true, Agent: "claude", Launcher: "sub", Model: "transcript-model", RequestedModel: &oldModel, Effort: "medium", Status: busyStatus, State: busyState, Rev: 100})
	}
	r.f.sessions["server"] = rows
}

func (r *conversationSwitchTestRig) moveBusyIndependently() {
	r.setTurn(proto.StatusIdle)
	r.f.mu.Lock()
	for i := range r.f.sessions["server"] {
		if r.f.sessions["server"][i].SessionID == "busy-chat" {
			r.f.sessions["server"][i].Launcher = "someone-elses-launcher"
			r.f.sessions["server"][i].Effort = "low"
		}
	}
	r.f.mu.Unlock()
}

func conversationSwitchTestCall(t *testing.T, s *Service, method, path, role, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func conversationSwitchTestDecode(t *testing.T, code int, raw []byte) conversationSwitchTestReport {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("conversation switch HTTP status %d: %s", code, raw)
	}
	var out conversationSwitchTestReport
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("conversation switch response %s: %v", raw, err)
	}
	return out
}

func conversationSwitchTestItemFor(t *testing.T, report conversationSwitchTestReport, id string) conversationSwitchTestItem {
	t.Helper()
	for _, item := range report.Items {
		if item.ConversationID == id {
			return item
		}
	}
	t.Fatalf("no item for %s: %+v", id, report)
	return conversationSwitchTestItem{}
}

func conversationSwitchTestStatus(t *testing.T, s *Service, id string) conversationSwitchTestReport {
	t.Helper()
	code, raw := conversationSwitchTestCall(t, s, http.MethodGet, "/api/v1/assistant/conversation-switches/"+id, "", "")
	return conversationSwitchTestDecode(t, code, raw)
}

func conversationSwitchTestDrive(t *testing.T, s *Service, id string, outcomes map[string]string) conversationSwitchTestReport {
	t.Helper()
	var report conversationSwitchTestReport
	defer func() {
		if t.Failed() {
			t.Logf("last conversation batch report: %+v", report)
			if st, ok := s.store().(logicalSwitchStore); ok {
				for _, item := range report.Items {
					op, opErr := st.ConversationSwitch(context.Background(), item.Machine, item.ConversationID, item.OperationID)
					logical, logicalErr := st.LogicalConversation(context.Background(), item.Machine, item.ConversationID)
					pending, pendingErr := st.ConversationInputPending(context.Background(), item.Machine, item.ConversationID)
					t.Logf("completion state for %s: op=%+v opErr=%v logical=%+v logicalErr=%v pending=%v pendingErr=%v", item.ConversationID, op, opErr, logical, logicalErr, pending, pendingErr)
				}
			}
		}
	}()
	waitFor(t, "conversation batch "+id+" to reach "+fmt.Sprint(outcomes), func() bool {
		s.logicalSweep(context.Background())
		report = conversationSwitchTestStatus(t, s, id)
		for conversation, want := range outcomes {
			if conversationSwitchTestItemFor(t, report, conversation).Outcome != want {
				return false
			}
		}
		return true
	})
	return report
}

func conversationSwitchTestPreparations(r *conversationSwitchTestRig) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]any, len(r.preparations))
	copy(out, r.preparations)
	return out
}

func conversationSwitchTestPreparationFor(t *testing.T, r *conversationSwitchTestRig, item conversationSwitchTestItem) map[string]any {
	t.Helper()
	operation := logicalNodeOperation(&db.LogicalConversation{MachineID: item.Machine, SessionID: item.ConversationID}, item.OperationID)
	var found []map[string]any
	for _, p := range conversationSwitchTestPreparations(r) {
		if p["operation_id"] == operation {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		t.Fatalf("prepare calls for %s operation %s: %+v", item.ConversationID, item.OperationID, found)
	}
	return found[0]
}

func conversationSwitchTestAllCalls(r *conversationSwitchTestRig) []DeviceCall {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	out := make([]DeviceCall, len(r.f.calls))
	copy(out, r.f.calls)
	return out
}

func conversationSwitchTestNodeOperation(call DeviceCall) bool {
	if strings.HasPrefix(call.Method, "assistant.segment.") {
		return true
	}
	switch call.Method {
	case DeviceSessionModes, DeviceSessionSend, DeviceSessionSwitch, DeviceAssistantSend,
		"session.close", "session.stop", "session.delete", "session.retire", "session.compact":
		return true
	default:
		return false
	}
}

func conversationSwitchTestUnexpectedPreviewCalls(r *conversationSwitchTestRig) []DeviceCall {
	var out []DeviceCall
	for _, call := range conversationSwitchTestAllCalls(r) {
		if conversationSwitchTestNodeOperation(call) {
			out = append(out, call)
		}
	}
	return out
}

func conversationSwitchTestOperationCallsFor(r *conversationSwitchTestRig, id string) []DeviceCall {
	var out []DeviceCall
	for _, call := range conversationSwitchTestAllCalls(r) {
		if conversationSwitchTestNodeOperation(call) && strings.Contains(string(call.Params), id) {
			out = append(out, call)
		}
	}
	return out
}

func conversationSwitchTestNoSyntheticMessage(t *testing.T, r *conversationSwitchTestRig) {
	t.Helper()
	for _, call := range conversationSwitchTestAllCalls(r) {
		if call.Method == DeviceAssistantSend || call.Method == DeviceSessionSend {
			t.Fatalf("synthetic message sent: %+v", call)
		}
	}
}

func TestConversationSwitchesPreviewQueueAndRestore(t *testing.T) {
	r := newConversationSwitchTestRig(t)
	ctx := context.Background()
	forward := `{"conversation_ids":["fresh-chat","busy-chat"],"launcher":"fallback","model":"new-model","effort":"low","permission_mode":"plan"}`

	code, raw := conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"conversation_ids":["fresh-chat"],"launcher":"fallback","preview":true,"unknown":true}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", code, raw)
	}
	code, raw = conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "answer", forward)
	if code != http.StatusForbidden {
		t.Fatalf("read-only role: %d %s", code, raw)
	}

	code, raw = conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"restore_batch_id":"missing-batch","preview":true}`)
	if code != http.StatusNotFound {
		t.Fatalf("missing restore preview batch: %d %s", code, raw)
	}
	code, raw = conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"restore_batch_id":"missing-batch"}`)
	if code != http.StatusNotFound {
		t.Fatalf("missing restore execution batch: %d %s", code, raw)
	}

	code, raw = conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"conversation_ids":["fresh-chat","busy-chat"],"launcher":"fallback","model":"new-model","effort":"low","permission_mode":"plan","preview":true}`)
	preview := conversationSwitchTestDecode(t, code, raw)
	if !preview.Preview || preview.BatchID != "" || conversationSwitchTestItemFor(t, preview, "fresh-chat").Outcome != "ready" || conversationSwitchTestItemFor(t, preview, "busy-chat").Outcome != "queued" {
		t.Fatalf("preview: %+v", preview)
	}
	var state json.RawMessage
	if found, err := r.d.Setting(ctx, "assistant.conversation_switches", &state); err != nil || found {
		t.Fatalf("preview persisted state: found=%v err=%v", found, err)
	}
	if c, err := r.d.LogicalConversation(ctx, "server", "fresh-chat"); err != nil || c != nil {
		t.Fatalf("preview started a logical conversation: %+v %v", c, err)
	}
	if calls := conversationSwitchTestUnexpectedPreviewCalls(r); len(calls) != 0 {
		t.Fatalf("preview reached a node: %+v", calls)
	}
	foundTool := false
	for _, tool := range r.s.Tools(ctx) {
		foundTool = foundTool || tool.Name == "conversations_switch"
	}
	if !foundTool {
		t.Fatal("conversations_switch is not an Assistant MCP tool")
	}

	code, raw = conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", forward)
	started := conversationSwitchTestDecode(t, code, raw)
	if started.BatchID == "" {
		t.Fatalf("execution has no batch id: %+v", started)
	}
	fresh := conversationSwitchTestItemFor(t, started, "fresh-chat")
	busy := conversationSwitchTestItemFor(t, started, "busy-chat")
	if fresh.Outcome != "switching" || busy.Outcome != "queued" || fresh.OperationID == "" || busy.OperationID == "" || fresh.OperationID == busy.OperationID {
		t.Fatalf("initial outcomes: %+v", started)
	}
	if fresh.Before.Launcher != "sub" || fresh.Before.Model != "old-model" || fresh.Before.Effort != "high" || fresh.Before.PermissionMode != "" || fresh.Target.PermissionMode != "plan" {
		t.Fatalf("exact before/target image: %+v", fresh)
	}
	if found, err := r.d.Setting(ctx, "assistant.conversation_switches", &state); err != nil || !found || len(state) == 0 {
		t.Fatalf("batch was not persisted first: found=%v err=%v", found, err)
	}
	if c, err := r.d.LogicalConversation(ctx, "server", "busy-chat"); err != nil || c != nil {
		t.Fatalf("busy conversation was adopted or interrupted: %+v %v", c, err)
	}
	if calls := conversationSwitchTestOperationCallsFor(r, "busy-chat"); len(calls) != 0 {
		t.Fatalf("busy conversation reached its node: %v", calls)
	}

	conversationSwitchTestDrive(t, r.s, started.BatchID, map[string]string{"fresh-chat": "switched", "busy-chat": "queued"})
	conversationSwitchTestNoSyntheticMessage(t, r)

	code, raw = conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"restore_batch_id":"`+started.BatchID+`","preview":true}`)
	restorePreview := conversationSwitchTestDecode(t, code, raw)
	if restorePreview.BatchID != "" || conversationSwitchTestItemFor(t, restorePreview, "fresh-chat").Target.Model != "old-model" || conversationSwitchTestItemFor(t, restorePreview, "busy-chat").Outcome != "refused" {
		t.Fatalf("restore preview: %+v", restorePreview)
	}

	r.setTurn(proto.StatusIdle)
	conversationSwitchTestDrive(t, r.s, started.BatchID, map[string]string{"fresh-chat": "switched", "busy-chat": "switched"})
	code, raw = conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"restore_batch_id":"`+started.BatchID+`"}`)
	restoring := conversationSwitchTestDecode(t, code, raw)
	if restoring.BatchID == "" || restoring.RestoreOf != started.BatchID || restoring.BatchID == started.BatchID {
		t.Fatalf("restore batch: %+v", restoring)
	}
	restored := conversationSwitchTestDrive(t, r.s, restoring.BatchID, map[string]string{"fresh-chat": "switched", "busy-chat": "switched"})

	preparations := conversationSwitchTestPreparations(r)
	if len(preparations) != 4 {
		t.Fatalf("prepare calls: %+v", preparations)
	}
	for _, check := range []struct {
		report conversationSwitchTestReport
		id     string
	}{
		{started, "fresh-chat"}, {started, "busy-chat"},
		{restored, "fresh-chat"}, {restored, "busy-chat"},
	} {
		item := conversationSwitchTestItemFor(t, check.report, check.id)
		p := conversationSwitchTestPreparationFor(t, r, item)
		permission := item.Target.PermissionMode
		if permission == "" {
			permission = "default"
		}
		if p["launcher"] != item.Target.Launcher || p["model"] != item.Target.Model || p["effort"] != item.Target.Effort || p["permission_mode"] != permission {
			t.Errorf("prepare did not use %s's exact target: item=%+v prepare=%+v", check.id, item, p)
		}
	}
	conversationSwitchTestNoSyntheticMessage(t, r)
}

func TestConversationSwitchesIsolatedHubDemo(t *testing.T) {
	r := newConversationSwitchTestRig(t)
	hub := httptest.NewServer(r.s)
	defer hub.Close()
	call := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, hub.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := hub.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, raw
	}
	drive := func(id string) conversationSwitchTestReport {
		t.Helper()
		var report conversationSwitchTestReport
		waitFor(t, "isolated hub batch "+id, func() bool {
			r.s.logicalSweep(context.Background())
			code, raw := call(http.MethodGet, "/api/v1/assistant/conversation-switches/"+id, "")
			report = conversationSwitchTestDecode(t, code, raw)
			return conversationSwitchTestItemFor(t, report, "fresh-chat").Outcome == "switched"
		})
		return report
	}

	code, raw := call(http.MethodPost, "/api/v1/assistant/conversation-switches", `{"conversation_ids":["fresh-chat"],"launcher":"fallback","model":"new-model","effort":"low","preview":true}`)
	preview := conversationSwitchTestDecode(t, code, raw)
	t.Logf("isolated hub preview: %s", raw)
	if !preview.Preview || preview.BatchID != "" || conversationSwitchTestItemFor(t, preview, "fresh-chat").Outcome != "ready" {
		t.Fatalf("isolated preview: %+v", preview)
	}

	code, raw = call(http.MethodPost, "/api/v1/assistant/conversation-switches", `{"conversation_ids":["fresh-chat"],"launcher":"fallback","model":"new-model","effort":"low"}`)
	started := conversationSwitchTestDecode(t, code, raw)
	t.Logf("isolated hub switch requested: %s", raw)
	switched := drive(started.BatchID)
	encoded, _ := json.Marshal(switched)
	t.Logf("isolated hub switched: %s", encoded)

	code, raw = call(http.MethodPost, "/api/v1/assistant/conversation-switches", `{"restore_batch_id":"`+started.BatchID+`"}`)
	restoring := conversationSwitchTestDecode(t, code, raw)
	t.Logf("isolated hub restore requested: %s", raw)
	restored := drive(restoring.BatchID)
	encoded, _ = json.Marshal(restored)
	t.Logf("isolated hub restored: %s", encoded)

	preparations := conversationSwitchTestPreparations(r)
	if len(preparations) != 2 || preparations[0]["model"] != "new-model" || preparations[1]["model"] != "old-model" || preparations[1]["launcher"] != "sub" || preparations[1]["effort"] != "high" || preparations[1]["permission_mode"] != "default" {
		t.Fatalf("isolated hub did not restore the before-image: %+v", preparations)
	}
	conversationSwitchTestNoSyntheticMessage(t, r)
}

func TestConversationSwitchesSurviveRestart(t *testing.T) {
	r := newConversationSwitchTestRig(t)
	code, raw := conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"conversation_ids":["busy-chat"],"launcher":"fallback","model":"new-model"}`)
	started := conversationSwitchTestDecode(t, code, raw)
	operation := conversationSwitchTestItemFor(t, started, "busy-chat").OperationID
	if started.BatchID == "" || operation == "" || conversationSwitchTestItemFor(t, started, "busy-chat").Outcome != "queued" {
		t.Fatalf("queued batch: %+v", started)
	}

	restarted := withSettings(t, r.f, New(Options{Hub: r.f, Store: func() Store { return r.d }, Logf: testLogf(t)}))
	after := conversationSwitchTestStatus(t, restarted, started.BatchID)
	if conversationSwitchTestItemFor(t, after, "busy-chat").Outcome != "queued" || conversationSwitchTestItemFor(t, after, "busy-chat").OperationID != operation {
		t.Fatalf("restart lost the durable item: %+v", after)
	}
	r.setTurn(proto.StatusIdle)
	conversationSwitchTestDrive(t, restarted, started.BatchID, map[string]string{"busy-chat": "switched"})
	if got := len(conversationSwitchTestPreparations(r)); got != 1 {
		t.Fatalf("restart replayed preparation %d times", got)
	}
}

func TestConversationSwitchesRefuseIndependentChanges(t *testing.T) {
	t.Run("queued configuration", func(t *testing.T) {
		r := newConversationSwitchTestRig(t)
		code, raw := conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"conversation_ids":["busy-chat"],"launcher":"fallback","model":"new-model"}`)
		started := conversationSwitchTestDecode(t, code, raw)
		r.moveBusyIndependently()
		after := conversationSwitchTestStatus(t, r.s, started.BatchID)
		if item := conversationSwitchTestItemFor(t, after, "busy-chat"); item.Outcome != "refused" || item.Reason == "" {
			t.Fatalf("independently changed item: %+v", item)
		}
		if c, err := r.d.LogicalConversation(context.Background(), "server", "busy-chat"); err != nil || c != nil {
			t.Fatalf("independent configuration was overwritten: %+v %v", c, err)
		}
		if calls := conversationSwitchTestOperationCallsFor(r, "busy-chat"); len(calls) != 0 {
			t.Fatalf("refused item reached its node: %v", calls)
		}
	})

	t.Run("restore target fence", func(t *testing.T) {
		r := newConversationSwitchTestRig(t)
		code, raw := conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"conversation_ids":["fresh-chat"],"launcher":"fallback","model":"new-model"}`)
		started := conversationSwitchTestDecode(t, code, raw)
		conversationSwitchTestDrive(t, r.s, started.BatchID, map[string]string{"fresh-chat": "switched"})

		// This deliberately independent operation has no batch before-image. The
		// fixture's durable-before-prepare gate applies only to batch operations.
		r.mu.Lock()
		r.allowNonBatchPrepare = true
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			r.allowNonBatchPrepare = false
			r.mu.Unlock()
		}()
		code, raw = conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversations/fresh-chat/switch", "", `{"source_machine_id":"server","operation_id":"independent-operation","expected_epoch":2,"machine_id":"server","launcher":"other","model":"independent-model"}`)
		if code != http.StatusOK {
			t.Fatalf("independent single switch: %d %s", code, raw)
		}
		waitFor(t, "the independent switch to activate", func() bool {
			r.s.logicalSweep(context.Background())
			c, err := r.d.LogicalConversation(context.Background(), "server", "fresh-chat")
			return err == nil && c != nil && c.Switch != nil && c.Switch.OperationID == "independent-operation" && c.Switch.Phase == "active"
		})
		waitFor(t, "the independent switch to accept", func() bool {
			err := r.d.ResolveConversationSwitch(context.Background(), "server", "fresh-chat", "independent-operation", "accepted", "")
			if errors.Is(err, db.ErrConversationSwitchBusy) {
				return false // NOWAIT can overlap the sweep that just activated it.
			}
			if err != nil {
				t.Fatal(err)
			}
			return true
		})
		code, raw = conversationSwitchTestCall(t, r.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"restore_batch_id":"`+started.BatchID+`"}`)
		refused := conversationSwitchTestDecode(t, code, raw)
		if item := conversationSwitchTestItemFor(t, refused, "fresh-chat"); item.Outcome != "refused" || item.Reason == "" || refused.BatchID != "" || refused.Preview {
			t.Fatalf("restore crossed its fence or mislabeled execution as preview: %+v", refused)
		}
		c, err := r.d.LogicalConversation(context.Background(), "server", "fresh-chat")
		active, ok := c.Active()
		if err != nil || !ok || c.Epoch != 3 || active.Launcher != "other" || active.Model != "independent-model" {
			t.Fatalf("restore overwrote the independent switch: %+v %v", c, err)
		}
	})
}

func TestConversationSwitchHTTPStatusReadsSpaceBatch(t *testing.T) {
	r := newConversationSwitchTestRig(t)
	state := map[string]any{"batches": []any{map[string]any{
		"id": "space-batch", "space": "another-space", "created_at": time.Now().UTC(),
		"items": []any{map[string]any{"machine": "server", "conversation_id": "fresh-chat", "outcome": "switched"}},
	}}}
	if err := r.d.PutSetting(context.Background(), "assistant.conversation_switches", state); err != nil {
		t.Fatal(err)
	}
	report := conversationSwitchTestStatus(t, r.s, "space-batch")
	if report.BatchID != "space-batch" || conversationSwitchTestItemFor(t, report, "fresh-chat").Outcome != "switched" {
		t.Fatalf("full-role HTTP status: %+v", report)
	}
	args := json.RawMessage(`{"batch_id":"space-batch"}`)
	if text, err := r.s.runTool(context.Background(), "server", "main-chat", "conversations_switch", args); err == nil {
		t.Fatalf("main-space MCP read another space's batch: %s", text)
	}
	args = json.RawMessage(`{"restore_batch_id":"space-batch","preview":true}`)
	if text, err := r.s.runTool(context.Background(), "server", "main-chat", "conversations_switch", args); err == nil {
		t.Fatalf("main-space MCP previewed another space's restore: %s", text)
	}
}

func TestConversationSwitchToolPreviewIsReadOnly(t *testing.T) {
	r := newConversationSwitchTestRig(t)
	args := json.RawMessage(`{"conversation_ids":["fresh-chat"],"launcher":"fallback","preview":true}`)
	before := time.Now()
	text, err := r.s.runTool(context.Background(), "server", "main-chat", "conversations_switch", args)
	if err != nil || !strings.Contains(text, "Preview") || !strings.Contains(text, "fresh-chat") {
		t.Fatalf("tool preview: %q %v", text, err)
	}
	text, err = r.s.runTool(context.Background(), "server", "main-chat", "conversations_switch", json.RawMessage(`{"conversation_ids":["fresh-cha"],"launcher":"fallback","preview":true}`))
	if err != nil || !strings.Contains(text, `"conversation_id": "fresh-chat"`) {
		t.Fatalf("prefix did not resolve to the durable full ID: %q %v", text, err)
	}
	if text, err = r.s.runTool(context.Background(), "server", "main-chat", "conversations_switch", json.RawMessage(`{"conversation_ids":["fresh-cha","fresh-chat"],"launcher":"fallback","preview":true}`)); err == nil {
		t.Fatalf("two aliases resolved to one batch item twice: %s", text)
	}
	r.f.mu.Lock()
	r.f.known["other-chat"] = true
	r.f.sessions["other"] = []proto.Session{{SessionID: "other-chat", MachineID: "other", Assistant: true, Agent: "claude", Launcher: "sub", Model: "old-model", Status: proto.StatusIdle, State: proto.StateIdle}}
	r.f.mu.Unlock()
	text, err = r.s.runTool(context.Background(), "server", "main-chat", "conversations_switch", json.RawMessage(`{"conversation_ids":["other-chat"],"machine_id":"other","launcher":"fallback","preview":true}`))
	if err != nil || !strings.Contains(text, `"machine": "other"`) || !strings.Contains(text, `"outcome": "ready"`) {
		t.Fatalf("selected machine was not used for space-scoped lookup: %q %v", text, err)
	}
	var state json.RawMessage
	if found, err := r.d.Setting(context.Background(), "assistant.conversation_switches", &state); err != nil || found {
		t.Fatalf("tool preview persisted state: found=%v err=%v", found, err)
	}
	for _, ix := range r.s.pendingInteractions() {
		if !ix.CreatedAt.Before(before) {
			t.Fatalf("tool preview created a confirmation: %+v", ix)
		}
	}
	if calls := conversationSwitchTestUnexpectedPreviewCalls(r); len(calls) != 0 {
		t.Fatalf("tool preview reached a node: %+v", calls)
	}
}
