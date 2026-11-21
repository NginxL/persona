package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

type a2aTestHub struct {
	*fakeHub
	sends   []DeviceCall
	sendErr error
	wakes   chan DeviceCall
}

func (h *a2aTestHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	if call.Method == "session.a2a.send" {
		h.sends = append(h.sends, call)
		return json.RawMessage(`{}`), h.sendErr
	}
	if call.Method == "session.a2a.wake" && h.wakes != nil {
		h.wakes <- call
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(call.Params, &p)
		return json.Marshal(map[string]any{"session": proto.Session{SessionID: p.SessionID}})
	}
	return h.fakeHub.DeviceCall(ctx, call)
}

func a2aFixture(t *testing.T) (*Service, *a2aTestHub, *db.DB, assistantwire.A2AConfig, a2aCaller, a2aRef) {
	t.Helper()
	database, err := db.Open(t.Context(), newDatabase(t), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	f := newFakeHub()
	h := &a2aTestHub{fakeHub: f}
	cfg := assistantwire.A2AConfig{Machines: []string{"mac", "box"}, PublicURL: "https://persona.example.com"}
	f.hello.Agents.A2A = cfg
	from, to := a2aRef{"mac", "caller-0001"}, a2aRef{"box", "runtime.worker-0001"}
	f.sessions[from.Machine] = []proto.Session{{SessionID: from.Session, Status: proto.StatusWorking, Agent: "codex"}}
	f.sessions[to.Machine] = []proto.Session{{SessionID: to.Session, Status: proto.StatusIdle, Agent: "pi",
		SourceKind: "runtime", Capabilities: &proto.SessionCapabilities{Send: true, Read: true}}}
	return New(Options{Hub: h, Store: func() Store { return database }, Logf: testLogf(t)}), h, database, cfg, a2aSessionCaller(from), to
}

func a2aProcessOne(t *testing.T, s *Service, st *db.DB, cfg assistantwire.A2AConfig, id string) *a2aTask {
	t.Helper()
	ctx := t.Context()
	task, err := decodeA2A(st.A2AGet(ctx, id))
	if err != nil || task == nil {
		t.Fatalf("get task: %v %v", task, err)
	}
	if err := s.a2aProcess(ctx, st, cfg, task); err != nil {
		t.Fatal(err)
	}
	task, err = decodeA2A(st.A2AGet(ctx, id))
	if err != nil || task == nil {
		t.Fatalf("get processed task: %v %v", task, err)
	}
	return task
}

func TestA2ACrossHarnessTaskAndWakeAfterRestart(t *testing.T) {
	s, h, st, cfg, parent, child := a2aFixture(t)
	ctx := t.Context()
	tk, err := s.a2aStart(ctx, st, cfg, parent, child, "task-request", "", "Check this build.", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.a2aStart(ctx, st, cfg, parent, child, "task-request", "", "Check this build.", true, nil)
	if err != nil || again.ID != tk.ID {
		t.Fatalf("retry %v %v", again, err)
	}
	if _, err := s.a2aStart(ctx, st, cfg, parent, child, "task-request", "", "Different text.", true, nil); !errors.Is(err, errA2ADuplicate) {
		t.Fatalf("same request with different text: %v", err)
	}
	if _, err := s.a2aStart(ctx, st, cfg, parent, child, "second-task", "", "Other text.", true, nil); !errors.Is(err, db.ErrA2ATargetBusy) {
		t.Fatalf("concurrent task to same target: %v", err)
	}
	tk = a2aProcessOne(t, s, st, cfg, tk.ID)
	if tk.State != a2aWorking || len(h.sends) != 1 || h.sends[0].Device != child.Machine {
		t.Fatalf("child delivery state %s sends %+v", tk.State, h.sends)
	}
	worker := a2aSessionCaller(child)
	tk, err = s.a2aMutate(ctx, st, cfg, worker, tk.ID, func(tk *a2aTask) (bool, error) {
		return a2aReport(tk, child, "blocked-report", a2aInput, "Which target should I use?")
	})
	if err != nil || tk.State != a2aInput {
		t.Fatalf("blocked report: %v %v", tk, err)
	}
	restarted := New(Options{Hub: h, Store: func() Store { return st }})
	a2aProcessOne(t, restarted, st, cfg, tk.ID)
	if len(h.sends) != 1 {
		t.Fatal("busy parent was interrupted")
	}
	setStatus(h.fakeHub, parent.Ref.Machine, parent.Ref.Session, proto.StatusDone)
	tk = a2aProcessOne(t, restarted, st, cfg, tk.ID)
	if len(h.sends) != 2 || h.sends[1].Device != parent.Ref.Machine ||
		!strings.Contains(string(h.sends[1].Params), "Which target should I use?") {
		t.Fatalf("parent did not receive the durable blocker: %+v", h.sends)
	}
	a2aProcessOne(t, restarted, st, cfg, tk.ID)
	if len(h.sends) != 2 {
		t.Fatal("a delivered blocker replayed")
	}
	tk, err = restarted.a2aMutate(ctx, st, cfg, parent, tk.ID, func(tk *a2aTask) (bool, error) {
		return a2aContinue(tk, "answer", "Use the integration target.")
	})
	if err != nil {
		t.Fatal(err)
	}
	tk = a2aProcessOne(t, restarted, st, cfg, tk.ID)
	seq := tk.Events[len(tk.Events)-2].Seq
	h.setItems(proto.Item{Role: "user", ClientMsgID: a2aDeliveryID(tk.ID, seq)}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Integration target passed."})
	setStatus(h.fakeHub, child.Machine, child.Session, proto.StatusDone)
	tk = a2aProcessOne(t, restarted, st, cfg, tk.ID)
	if tk.State != a2aComplete || len(tk.wire(nil, true).Artifacts) != 1 {
		t.Fatalf("outcome: state=%s events=%+v", tk.State, tk.Events)
	}
	tk = a2aProcessOne(t, restarted, st, cfg, tk.ID)
	if len(h.sends) != 4 || h.sends[3].Device != parent.Ref.Machine || !strings.Contains(string(h.sends[3].Params), "Integration target passed.") {
		t.Fatalf("parent did not receive final artifact: %+v", h.sends)
	}
	if _, err := restarted.a2aGet(ctx, st, cfg, a2aSessionCaller(a2aRef{"mac", "unrelated"}), tk.ID); !errors.Is(err, errA2ANotFound) {
		t.Fatalf("unrelated session read a task: %v", err)
	}
}

func TestA2AAmbiguousSendIsReconciledWithoutReplay(t *testing.T) {
	s, h, st, cfg, parent, child := a2aFixture(t)
	tk, err := s.a2aStart(t.Context(), st, cfg, parent, child, "task-request", "", "Check this build.", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.sendErr = context.DeadlineExceeded
	a2aProcessOne(t, s, st, cfg, tk.ID)
	tk = a2aProcessOne(t, s, st, cfg, tk.ID)
	if len(h.sends) != 1 || tk.Events[0].Delivery != a2aUncertain {
		t.Fatalf("uncertain send retried: %+v", tk.Events)
	}
	tk.Events[0].Attempted = time.Now().Add(-a2aConfirmFor)
	if err := s.a2aSave(t.Context(), st, tk); err != nil {
		t.Fatal(err)
	}
	marked := prompts.RelayMessageText("A2A request", nil, a2aDeliveryID(tk.ID, 1))
	h.setItems(proto.Item{Role: "user", Text: marked}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Result."},
		proto.Item{Role: "user", Text: "An unrelated new prompt."}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Unrelated."})
	tk = a2aProcessOne(t, s, st, cfg, tk.ID)
	if len(h.sends) != 1 || tk.State != a2aWorking || tk.Events[0].Delivery != a2aDelivered {
		t.Fatalf("late delivery was not confirmed: %+v", tk.Events)
	}
	tk = a2aProcessOne(t, s, st, cfg, tk.ID)
	if len(h.sends) != 1 || tk.State != a2aFailed {
		t.Fatalf("unrelated turn was accepted as a result or the input was replayed: %+v", tk.Events)
	}
}

func TestA2ADeliveryWaitsForLogicalSwitch(t *testing.T) {
	s, _, st, _ := logicalPair(t, "pi")
	assertDeferred := func() {
		t.Helper()
		for _, method := range []string{"session.a2a.send", "session.a2a.wake"} {
			_, handled, err := s.logicalCall(t.Context(), "server", method, json.RawMessage(`{"session_id":"side-chat"}`))
			if !handled || rpcCode(err) != assistantwire.CodeConversationBusy || !a2aNoDelivery(err) {
				t.Fatalf("%s should wait for earlier owner input: handled=%v err=%v", method, handled, err)
			}
		}
	}
	assertDeferred()
	c := activatePair(t, st)
	if err := st.EnqueueConversationInput(t.Context(), "server", "side-chat", "owner-message", c.Epoch, json.RawMessage(`{"text":"Earlier owner input"}`)); err != nil {
		t.Fatal(err)
	}
	assertDeferred()
}

func TestA2ADeletedRecipientReleasesTask(t *testing.T) {
	s, h, st, cfg, parent, child := a2aFixture(t)
	ctx := t.Context()
	task, err := s.a2aStart(ctx, st, cfg, parent, child, "request", "", "Check the build.", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	tombstone, err := json.Marshal(deletedConversation{Machine: child.Machine, ID: child.Session, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutConversationDeletion(ctx, child.Machine, child.Session, tombstone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.a2aSession(ctx, cfg, child); !errors.Is(err, errA2AUnavailable) {
		t.Fatalf("deleted recipient remained eligible: %v", err)
	}
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	if task.State != a2aFailed || !task.TargetClosed || len(h.sends) != 0 {
		t.Fatalf("deleted recipient left an active or delivered task: %+v", task)
	}
}

func TestA2AUnconfirmedInputReleasesTheTarget(t *testing.T) {
	s, h, st, cfg, parent, child := a2aFixture(t)
	ctx := t.Context()
	task, err := s.a2aStart(ctx, st, cfg, parent, child, "initial", "", "Inspect the build.", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.sendErr = errors.New("lost send reply")
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	if task.Events[0].Delivery != a2aUncertain {
		t.Fatalf("delivery should remain uncertain: %+v", task.Events[0])
	}
	task.Events[0].Attempted = time.Now().Add(-a2aConfirmFor)
	if err := s.a2aSave(ctx, st, task); err != nil {
		t.Fatal(err)
	}
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	if task.State != a2aFailed || task.Events[0].Delivery != a2aExpired || len(h.sends) != 1 {
		t.Fatalf("expired task was replayed or still holds the target: %+v (%d sends)", task.Events, len(h.sends))
	}
	if _, err := s.a2aStart(ctx, st, cfg, parent, child, "next", "", "Inspect the next build.", true, nil); err != nil {
		t.Fatalf("expired task did not release the target: %v", err)
	}
}

func TestA2AArchiveClosesTheTaskAndTellsTheSender(t *testing.T) {
	s, h, st, cfg, parent, child := a2aFixture(t)
	ctx := t.Context()
	task, err := s.a2aStart(ctx, st, cfg, parent, child, "initial", "", "Inspect the build.", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	a2aProcessOne(t, s, st, cfg, task.ID)
	row := h.sessions[child.Machine][0]
	row.MachineID = child.Machine
	if err := s.noteAgentArchive(ctx, row, true); err != nil {
		t.Fatal(err)
	}
	setStatus(h.fakeHub, parent.Ref.Machine, parent.Ref.Session, proto.StatusIdle)
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	if !task.TargetClosed || task.State != a2aFailed {
		t.Fatalf("archive did not close task: %+v", task)
	}
	a2aProcessOne(t, s, st, cfg, task.ID)
	if len(h.sends) != 2 || h.sends[1].Device != parent.Ref.Machine || !strings.Contains(string(h.sends[1].Params), "receiving session was closed") {
		t.Fatalf("archive result did not reach the parent: %+v", h.sends)
	}
	if _, err := s.a2aGet(ctx, st, cfg, parent, task.ID); err != nil {
		t.Fatalf("sender could not read the closed task: %v", err)
	}
	if _, err := s.a2aGet(ctx, st, cfg, a2aSessionCaller(child), task.ID); !errors.Is(err, errA2ANotFound) {
		t.Fatalf("archived session could still read task: %v", err)
	}
}

func TestA2ALaunchesShareTheDispatchLimitAndDepth(t *testing.T) {
	s, h, st, cfg, parent, _ := a2aFixture(t)
	ctx := t.Context()
	h.hello.Agents.Dispatch.MaxActive = 1
	start := func(c a2aCaller, machine, id string) (*a2aTask, error) {
		return s.a2aStart(ctx, st, cfg, c, a2aRef{Machine: machine}, id, "", "Inspect the build.", true,
			&a2aLaunch{CWD: "/code/app", Pending: true})
	}
	task, err := start(parent, "box", "launch")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := start(parent, "box", "launch"); err != nil || again.ID != task.ID {
		t.Fatalf("an idempotent retry consumed capacity: %+v %v", again, err)
	}
	if _, err := start(parent, "box", "second-launch"); err == nil || !strings.Contains(err.Error(), "the limit is 1") {
		t.Fatalf("a second launch exceeded the dispatch limit: %v", err)
	}
	if _, err := start(a2aSessionCaller(task.target()), "mac", "child-launch"); err == nil || !strings.Contains(err.Error(), "does not dispatch") {
		t.Fatalf("a child could dispatch another child: %v", err)
	}
}

func TestA2ATextOnlyHarnessReturnsQuestionAndContinues(t *testing.T) {
	s, h, st, cfg, parent, child := a2aFixture(t)
	ctx := t.Context()
	task, err := s.a2aStart(ctx, st, cfg, parent, child, "text-task", "", "Check the build.", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	a2aProcessOne(t, s, st, cfg, task.ID)
	question := "A2A " + task.ID + " input_required\nWhich build target?"
	h.setItems(proto.Item{Role: "user", ClientMsgID: "different-message"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: question})
	if task = a2aProcessOne(t, s, st, cfg, task.ID); task.State != a2aWorking {
		t.Fatalf("unrelated turn changed the task: %s", task.State)
	}
	h.setItems(proto.Item{Role: "user", Text: prompts.RelayMessageText("Check the build.", nil, a2aDeliveryID(task.ID, 1))},
		proto.Item{Role: "user", Kind: proto.KindToolResult, Text: "The tool output appeared inside the same turn."},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: question}, proto.Item{Kind: "turn_duration", ID: "closed-turn"},
		proto.Item{Role: "user", ClientMsgID: "different-message"})
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	if last := task.Events[len(task.Events)-1]; task.State != a2aInput || last.Text != "Which build target?" || last.Delivery != a2aQueued {
		t.Fatalf("finished question was not returned: %+v", last)
	}
	h.sessions[parent.Ref.Machine][0].Status = proto.StatusOffline
	h.sessions[parent.Ref.Machine][0].SourceKind = proto.SourceRuntime
	h.sessions[parent.Ref.Machine][0].Capabilities = &proto.SessionCapabilities{Send: true, A2AWakeOnSend: true}
	a2aProcessOne(t, s, st, cfg, task.ID)
	var wake struct {
		Wake bool `json:"a2a_wake_on_send"`
	}
	if len(h.sends) != 2 || h.sends[1].Device != parent.Ref.Machine || json.Unmarshal(h.sends[1].Params, &wake) != nil || !wake.Wake {
		t.Fatalf("the offline runtime parent did not receive its update: %+v", h.sends)
	}
	questionSeq := task.Events[len(task.Events)-1].Seq
	h.sessions[parent.Ref.Machine][0].Status = proto.StatusIdle
	h.setItems(proto.Item{Role: "user", ClientMsgID: a2aDeliveryID(task.ID, questionSeq)},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: "A2A unrelated-task answer\nThis belongs to another task."})
	if task = a2aProcessOne(t, s, st, cfg, task.ID); task.State != a2aInput {
		t.Fatalf("another task's answer changed the task: %s", task.State)
	}
	h.setItems(proto.Item{Role: "user", ClientMsgID: a2aDeliveryID(task.ID, questionSeq)},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: "A2A " + task.ID + " answer\nUse the integration target."})
	guard := DispatchGuard{Enabled: true, Targets: []DispatchGuardTarget{{MachineID: parent.Ref.Machine, SessionID: parent.Ref.Session, Deny: []string{"conversation_send"}}}}
	if err := s.saveDispatchGuard(ctx, guard); err != nil {
		t.Fatal(err)
	}
	if task = a2aProcessOne(t, s, st, cfg, task.ID); task.State != a2aInput {
		t.Fatalf("a text reply bypassed the dispatch guard: %s", task.State)
	}
	args, _ := json.Marshal(map[string]string{"task_id": task.ID, "message_id": "guarded-answer", "text": "Use the integration target."})
	if _, err := s.a2aToolCall(ctx, st, cfg, parent, "a2a_send", args); err == nil {
		t.Fatal("a follow-up tool call bypassed the dispatch guard")
	}
	if err := s.saveDispatchGuard(ctx, DispatchGuard{}); err != nil {
		t.Fatal(err)
	}
	if task = a2aProcessOne(t, s, st, cfg, task.ID); task.State != a2aSubmitted {
		t.Fatalf("text answer was not accepted: %s", task.State)
	}
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	h.setItems(proto.Item{Role: "user", ClientMsgID: a2aDeliveryID(task.ID, len(task.Events)-1)},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: "The integration target passed."})
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	if last := task.Events[len(task.Events)-1]; task.State != a2aComplete || last.Text != "The integration target passed." {
		t.Fatalf("continued task did not finish: %+v", last)
	}
}

func TestA2AJSONRPCTaskOwnershipAndCancel(t *testing.T) {
	s, h, _, _, _, child := a2aFixture(t)
	path := "/api/v1/a2a/" + child.Machine + "/" + child.Session + "/rpc"
	call := func(device, method, params string) a2aRPCResponse {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
		r.Header.Set("A2A-Version", "1.0")
		r.Header.Set(extwire.HeaderDevice, device)
		r.Header.Set(extwire.HeaderRole, "scoped")
		r.Header.Set(extwire.HeaderScopes, assistantwire.ScopeA2A)
		w := httptest.NewRecorder()
		s.ServeSocket(w, r)
		var answer a2aRPCResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &answer) != nil {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		return answer
	}
	reply := call("first", "SendMessage", `{"configuration":{"returnImmediately":true},"message":{"messageId":"remote-message","role":"ROLE_USER","parts":[{"text":"Hello."}]}}`)
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	id := reply.Result.(map[string]any)["task"].(map[string]any)["id"].(string)
	params := `{"id":"` + id + `"}`
	if e := call("second", "GetTask", params).Error; e == nil || e.Code != -32001 {
		t.Fatalf("task available to different client: %+v", e)
	}
	page := call("first", "ListTasks", `{"historyLength":0,"statusTimestampAfter":"2020-01-01T00:00:00Z"}`)
	if page.Error != nil || page.Result.(map[string]any)["totalSize"] != float64(1) {
		t.Fatalf("list result: %+v", page)
	}
	cancel := call("first", "CancelTask", params)
	if cancel.Error != nil || cancel.Result.(map[string]any)["status"].(map[string]any)["state"] != "TASK_STATE_CANCELED" {
		t.Fatalf("cancel result: %+v", cancel)
	}
	row := h.sessions[child.Machine][0]
	row.MachineID = child.Machine
	if err := s.noteAgentArchive(t.Context(), row, true); err != nil {
		t.Fatal(err)
	}
	got := call("first", "GetTask", params)
	if got.Error != nil || got.Result.(map[string]any)["status"].(map[string]any)["state"] != "TASK_STATE_CANCELED" {
		t.Fatalf("owner could not read the final state after archive: %+v", got)
	}
	if e := call("second", "GetTask", params).Error; e == nil || e.Code != -32001 {
		t.Fatalf("archived task available to different client: %+v", e)
	}
}

func TestA2AStoreClaimsPersist(t *testing.T) {
	s, _, st, cfg, parent, child := a2aFixture(t)
	tk, err := s.a2aStart(t.Context(), st, cfg, parent, child, "task-request", "", "Work.", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.A2AClaim(t.Context(), 1)
	if err != nil || len(rows) != 1 || rows[0].ID != tk.ID || time.Since(rows[0].Created) > time.Minute {
		t.Fatalf("claim %+v %v", rows, err)
	}
	rows, err = st.A2AClaim(t.Context(), 1)
	if err != nil || len(rows) != 0 {
		t.Fatalf("duplicate lease %+v %v", rows, err)
	}
}

func TestA2AEnrolledSessionGetsTools(t *testing.T) {
	s, _, _, _, parent, child := a2aFixture(t)
	ctx := t.Context()
	tools, err := s.sessionMCPToolsChecked(ctx, parent.Ref.Machine, parent.Ref.Session, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range assistantwire.A2AToolNames {
		if !toolListed(tools, name) {
			t.Fatalf("MCP tool %q missing", name)
		}
	}
	r := s.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_sessions", json.RawMessage(`{}`), false)
	if r.IsError || !strings.Contains(resultText(r), child.Session) {
		t.Fatalf("discovery: %s", resultText(r))
	}
	if r := s.mcpCall(ctx, "unenrolled", parent.Ref.Session, "a2a_sessions", json.RawMessage(`{}`), false); !r.IsError {
		t.Fatal("an unenrolled machine could discover sessions")
	}
}

func TestA2AIsDefaultForTrustedSessionsAndDispatch(t *testing.T) {
	s, h, st, _, parent, _ := a2aFixture(t)
	ctx := t.Context()
	h.fakeModel(t)
	h.hello.Agents.A2A = assistantwire.A2AConfig{}
	on := true
	h.hello.Agents.Dispatch.Enabled = &on
	h.hello.Agents.Dispatch.MaxActive = 1
	h.startReply = func(args map[string]any, row *proto.Session) *proto.StartInstead {
		row.SessionID = args["session_id"].(string)
		return nil
	}
	h.onStart = func(f *fakeHub, args map[string]any) {
		f.sessions["box"] = append(f.sessions["box"], proto.Session{
			SessionID: args["session_id"].(string), Status: proto.StatusWorking, Agent: "codex",
		})
	}
	if tools := s.sessionMCPTools(ctx, parent.Ref.Machine, parent.Ref.Session, false, false); !toolListed(tools, "a2a_send") {
		t.Fatal("trusted session without an A2A stanza did not get the A2A tool")
	}
	// Some runtime plugins declare their tool catalog before they have a session.
	if tools := s.sessionMCPTools(ctx, parent.Ref.Machine, "", false, false); !toolListed(tools, "a2a_report") {
		t.Fatal("the runtime's machine-scoped catalog lacks A2A declarations")
	}
	if result := s.mcpCall(ctx, parent.Ref.Machine, "", "a2a_sessions", json.RawMessage(`{}`), false); !result.IsError {
		t.Fatal("a runtime without a bound session could invoke A2A")
	}
	fullPrompt := "Run the integration checks.\n" + strings.Repeat("Check a test case.\n", 800) + "Final instruction."
	request := map[string]any{"machine": "box", "cwd": "/code/app", "prompt": fullPrompt, "message_id": "dispatch-receipt",
		"worktree": map[string]string{"branch": "fix/linux"}}
	args, _ := json.Marshal(request)
	result := dispatchCall(s, parent.Ref.Machine, parent.Ref.Session, "dispatch_start", string(args))
	if result.IsError || !strings.Contains(resultText(result), "A2A task") {
		t.Fatalf("default dispatch: %s", resultText(result))
	}
	task, err := decodeA2A(st.A2ARequest(ctx, parent.Owner, "dispatch-receipt"))
	if err != nil || task == nil || task.Launch.Pending || !task.Wake || !task.Launch.Dispatch {
		t.Fatalf("durable dispatch: %+v %v", task, err)
	}
	if task.Launch.CWD != "/code/app" || task.Launch.Worktree != "/wt/app/fix-linux" {
		t.Fatalf("dispatch lost the original directory or the actual worktree: %+v", task.Launch)
	}
	if len(h.starts) != 1 || len(s.dispatched(ctx)) != 0 {
		t.Fatalf("unexpected start or legacy record: %+v", h.starts)
	}
	prompt := h.starts[0]["prompt"].(string)
	if prompts.RelayMessageID(prompt) != a2aDeliveryID(task.ID, 1) || !strings.Contains(prompt, fullPrompt) {
		t.Fatal("dispatch prompt lost its A2A identity or original content")
	}
	again := dispatchCall(s, parent.Ref.Machine, parent.Ref.Session, "dispatch_start", string(args))
	if again.IsError || !strings.Contains(resultText(again), task.ID) || len(h.starts) != 1 {
		t.Fatalf("retry at the capacity limit: %s, starts %d", resultText(again), len(h.starts))
	}
	request["prompt"] = fullPrompt + " Different ending."
	conflictArgs, _ := json.Marshal(request)
	conflict := dispatchCall(s, parent.Ref.Machine, parent.Ref.Session, "dispatch_start", string(conflictArgs))
	if !conflict.IsError || !strings.Contains(resultText(conflict), "message_id") || len(h.starts) != 1 {
		t.Fatalf("changed prompt was accepted on retry: %s", resultText(conflict))
	}
	cfg := s.a2aConfig(ctx)
	h.setItems(proto.Item{Role: "user", Text: prompt})
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	if task.State != a2aWorking {
		t.Fatalf("first prompt was not recognized: %+v", task.Events)
	}
	setStatus(h.fakeHub, task.TargetMachine, task.TargetSession, proto.StatusBlocked)
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	if task.State != a2aAuth {
		t.Fatalf("native blocker status: %s", task.State)
	}
	parentSession := parent.Ref.Session
	setStatus(h.fakeHub, parent.Ref.Machine, parentSession, proto.StatusDone)
	restarted := New(Options{Hub: h, Store: func() Store { return st }})
	a2aProcessOne(t, restarted, st, cfg, task.ID)
	if len(h.sends) != 1 || h.sends[0].Device != parent.Ref.Machine {
		t.Fatalf("default dispatch did not wake its parent: %+v", h.sends)
	}
	read := dispatchCall(restarted, parent.Ref.Machine, parentSession, "dispatch_read", `{"session_id":"`+task.ID+`"}`)
	if read.IsError || !strings.Contains(resultText(read), "waiting for the user") {
		t.Fatalf("compatibility read: %s", resultText(read))
	}
	setStatus(h.fakeHub, task.TargetMachine, task.TargetSession, proto.StatusWorking)
	if task = a2aProcessOne(t, restarted, st, cfg, task.ID); task.State != a2aWorking {
		t.Fatalf("task did not resume after approval: %s", task.State)
	}
	a2aProcessOne(t, restarted, st, cfg, task.ID)
	if len(h.sends) != 2 || h.sends[1].Device != parent.Ref.Machine || !strings.Contains(string(h.sends[1].Params), "resumed work") {
		t.Fatalf("resumed status was not sent to the parent: %+v", h.sends)
	}
}

func TestA2AExplicitEmptyScopeSurvivesJSON(t *testing.T) {
	cfg := assistantwire.A2AConfig{Machines: []string{}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded assistantwire.A2AConfig
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Machines == nil || len(decoded.Machines) != 0 {
		t.Fatalf("empty scope reverted to defaults: %s %v", raw, err)
	}
}

func TestA2ARevokedCredentialCannotDeliverQueuedInput(t *testing.T) {
	s, h, st, cfg, _, child := a2aFixture(t)
	task, err := s.a2aStart(t.Context(), st, cfg, a2aCaller{Owner: "device:remote"}, child, "request", "", "Queued work.", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.revokedDevices = map[string]bool{"remote": true}
	h.mu.Unlock()
	task = a2aProcessOne(t, s, st, cfg, task.ID)
	if len(h.sends) != 0 || task.State != a2aCanceled {
		t.Fatalf("state %s after credential revocation, sends %v", task.State, h.sends)
	}
}

func TestA2AWakesAnIdleStoppedParentAndLeavesAUserStopAlone(t *testing.T) {
	s, h, st, cfg, parent, child := a2aFixture(t)
	h.wakes = make(chan DeviceCall, 1)
	ctx := t.Context()
	task, err := s.a2aStart(ctx, st, cfg, parent, child, "wake-task", "", "Check the integration.", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	a2aProcessOne(t, s, st, cfg, task.ID)
	task, err = s.a2aMutate(ctx, st, cfg, a2aSessionCaller(child), task.ID, func(task *a2aTask) (bool, error) {
		return a2aReport(task, child, "done", a2aComplete, "Integration passed.")
	})
	if err != nil {
		t.Fatal(err)
	}
	setStop := func(cause proto.StopCause) {
		h.mu.Lock()
		h.sessions[parent.Ref.Machine][0].Status = proto.StatusOffline
		h.sessions[parent.Ref.Machine][0].Capabilities = &proto.SessionCapabilities{StopCause: cause}
		h.mu.Unlock()
	}
	setStop(proto.StopUser)
	a2aProcessOne(t, s, st, cfg, task.ID)
	select {
	case <-h.wakes:
		t.Fatal("a user-stopped session was resumed")
	default:
	}
	setStop(proto.StopIdle)
	a2aProcessOne(t, s, st, cfg, task.ID)
	select {
	case call := <-h.wakes:
		if call.Device != parent.Ref.Machine || !strings.Contains(string(call.Params), "Integration passed.") {
			t.Fatalf("resume call: %+v", call)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parent was not resumed")
	}
	waitFor(t, "the durable A2A wake receipt", func() bool {
		task, err := decodeA2A(st.A2AGet(ctx, task.ID))
		if err != nil {
			t.Fatal(err)
		}
		return task.Events[len(task.Events)-1].Delivery == a2aDelivered
	})
}

func TestA2AStandardStreamCarriesTaskAndArtifact(t *testing.T) {
	s, h, st, cfg, _, child := a2aFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(s.ServeSocket))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	path := srv.URL + "/api/v1/a2a/" + child.Machine + "/" + child.Session
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path+"/rpc", strings.NewReader(`{
		"jsonrpc":"2.0","id":"stream","method":"SendStreamingMessage","params":{"message":{
			"messageId":"remote-message","role":"ROLE_USER","parts":[{"text":"Hello."}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("A2A-Version", "1.0")
	req.Header.Set(extwire.HeaderDevice, "first")
	req.Header.Set(extwire.HeaderRole, "full")
	response, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response: %v", response)
	}
	scanner := bufio.NewScanner(response.Body)
	var id string
	artifact, complete := false, false
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var frame struct {
			ID     string `json:"id"`
			Result struct {
				Task           *a2aWireTask                    `json:"task"`
				ArtifactUpdate *struct{ Artifact a2aArtifact } `json:"artifactUpdate"`
				StatusUpdate   *struct{ Status a2aStatus }     `json:"statusUpdate"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(data), &frame); err != nil || frame.ID != "stream" {
			t.Fatalf("stream frame: %s, %v", data, err)
		}
		if frame.Result.Task != nil {
			id = frame.Result.Task.ID
			a2aProcessOne(t, s, st, cfg, id)
			h.setItems(proto.Item{Role: "user", ClientMsgID: a2aDeliveryID(id, 1)}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Done."})
			a2aProcessOne(t, s, st, cfg, id)
		}
		if a := frame.Result.ArtifactUpdate; a != nil {
			artifact = len(a.Artifact.Parts) == 1 && *a.Artifact.Parts[0].Text == "Done."
		}
		if status := frame.Result.StatusUpdate; status != nil {
			complete = complete || status.Status.State == "TASK_STATE_COMPLETED"
		}
	}
	if err := scanner.Err(); err != nil || id == "" || !artifact || !complete {
		t.Fatalf("stream id=%q artifact=%t complete=%t err=%v", id, artifact, complete, err)
	}
}

func TestA2AAcceptsOneLaunchPerMessageID(t *testing.T) {
	s, h, st, cfg, parent, _ := a2aFixture(t)
	h.fakeModel(t)
	h.startReply = func(args map[string]any, sess *proto.Session) *proto.StartInstead {
		sess.SessionID = args["session_id"].(string)
		return nil
	}
	start := func() (*a2aTask, error) {
		return s.a2aStart(t.Context(), st, cfg, parent, a2aRef{Machine: "box"}, "launch", "", "Inspect the build.", true,
			&a2aLaunch{CWD: "/code/project", Pending: true})
	}
	task, err := start()
	if err != nil {
		t.Fatal(err)
	}
	a2aProcessOne(t, s, st, cfg, task.ID)
	waitFor(t, "the A2A launch result", func() bool {
		task, err = decodeA2A(st.A2AGet(t.Context(), task.ID))
		if err != nil {
			t.Fatal(err)
		}
		return !task.Launch.Pending
	})
	if task.Launch.Pending || task.Events[0].Delivery != a2aUncertain || len(h.starts) != 1 ||
		h.starts[0]["permission_mode"] != "plan" {
		t.Fatalf("launch: %+v; starts %+v", task, h.starts)
	}
	retry, err := start()
	if err != nil || retry.ID != task.ID || retry.TargetSession != task.TargetSession {
		t.Fatalf("launch retry: %+v %v", retry, err)
	}
}
