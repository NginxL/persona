package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Compatibility contract between the server-side projection (todo/projection) and legacy consumers:
//   - Direction tree five-state placement (needs_you / review / stuck / working / planned) on shared facts
//   - Backward-compatible GET /api/v1/todos & GET /api/v1/todos/{id} JSON wire contract for unmodified iOS/Android clients
//
// See issue #14541.

func TestProjectionDirectionTreeFiveStateCompat(t *testing.T) {
	e := treeEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	old := ago(30 * time.Hour)
	recent := ago(10 * time.Minute)
	expired := ago(3 * time.Hour)

	e.f.setSessions("mac",
		proto.Session{MachineID: "mac", SessionID: "s-ask", Title: "Ask run", Status: proto.StatusBlocked, State: proto.StateNeedsYou, StateSince: recent, UpdatedAt: recent, PendingInteractions: 1, Asks: "Which option?"},
		proto.Session{MachineID: "mac", SessionID: "s-held", Title: "Held run", Status: proto.StatusBlocked, State: proto.StateNeedsYou, StateSince: recent, UpdatedAt: recent, PendingInteractions: 1, Asks: "Need supervisor"},
		proto.Session{MachineID: "mac", SessionID: "s-run", Title: "Work run", Status: proto.StatusWorking, State: proto.StateWorking, StateSince: recent, UpdatedAt: recent, Activity: "writing tests"},
		proto.Session{MachineID: "mac", SessionID: "s-idle-recent", Title: "Idle recent", Status: proto.StatusIdle, State: proto.StateIdle, StateSince: recent, UpdatedAt: recent},
		proto.Session{MachineID: "mac", SessionID: "s-idle-old", Title: "Idle old", Status: proto.StatusIdle, State: proto.StateIdle, StateSince: old, UpdatedAt: old},
	)

	// Mark s-held's pending card as delegating (held by supervisor).
	e.s.fleet.mu.Lock()
	if e.s.fleet.interactions == nil {
		e.s.fleet.interactions = map[string]*pending{}
	}
	e.s.fleet.interactions["ix_compat_held"] = &pending{ix: proto.Interaction{
		ID: "ix_compat_held", MachineID: "mac", SessionID: "s-held",
		Kind: proto.IxQuestion, ToolName: "AskUserQuestion", Delegating: true,
		CreatedAt: recent, ExpiresAt: now.Add(time.Hour),
	}}
	e.s.fleet.mu.Unlock()

	lead := func(fns ...func(*Todo)) func(*Todo) {
		return func(td *Todo) {
			td.Supervision = &Supervision{Supervisor: "conv_lead_a", UpdatedAt: recent}
			for _, fn := range fns {
				fn(td)
			}
		}
	}
	withAgent := func(machine, sid string) func(*Todo) {
		return func(td *Todo) {
			td.Agents = []TodoAgent{{MachineID: machine, SessionID: sid, Title: sid, LinkedAt: old}}
		}
	}

	// 1. Active agent ask -> projection needs_you/agent_asks <-> tree needs_you
	idAgentAsk := e.task("Agent asks", lead(withAgent("mac", "s-ask")))
	// 2. Owner decision -> projection needs_you/owner_decides <-> tree needs_you
	idOwnerDecide := e.task("Owner decides", lead(withAgent("mac", "s-idle-recent"), func(td *Todo) {
		td.Supervision.State = reviewNeedsUser
		td.Supervision.Reviews = []TaskReview{{At: recent, Verdict: reviewNeedsUser, Reason: "Choose A or B"}}
	}))
	// 3. can_wait deferred decision -> projection can_wait/can_wait <-> tree needs_you (legacy waiting)
	deadline := now.Add(24 * time.Hour)
	reversible := true
	canWaitOptions := []DecisionOption{{Label: "Option A", LeadsTo: "Fast"}, {Label: "Option B", LeadsTo: "Slow"}}
	idCanWait := e.task("Can wait decision", lead(withAgent("mac", "s-idle-recent"), func(td *Todo) {
		td.Supervision.State = reviewNeedsUser
		td.Supervision.Reviews = []TaskReview{{
			At: recent, Verdict: reviewNeedsUser, Reason: "Pick later", Recommend: "Option A",
			Options: canWaitOptions, Deadline: &deadline, Reversible: &reversible,
		}}
	}))
	// 4. Supervisor-held ask (delegating) -> projection working/persona_working <-> tree needs_you (legacy triage lacks card delegation)
	idSupHeld := e.task("Supervisor held ask", lead(withAgent("mac", "s-held")))
	// 5. Pending review (idle agent) -> projection just_finished/finished_unseen <-> tree review
	idReview := e.task("Pending review", lead(withAgent("mac", "s-idle-recent"), func(td *Todo) {
		td.Supervision.State = reviewPending
		td.Supervision.PendingSince = &recent
	}))
	// 6. Pending review with running agent -> projection working/agent_running <-> tree working
	idReviewRunning := e.task("Pending review still working", lead(withAgent("mac", "s-run"), func(td *Todo) {
		td.Supervision.State = reviewPending
	}))
	// 7. Working agent -> projection working/agent_running <-> tree working
	idWorking := e.task("Working run", lead(withAgent("mac", "s-run")))
	// 8. Parked (future until, recent) -> projection waiting/parked <-> tree working
	idParkedActive := e.task("Parked recent", lead(withAgent("mac", "s-idle-recent"), func(td *Todo) {
		td.Supervision.State = reviewParked
		td.Supervision.Parked = &Parked{Reason: "Waiting on vendor", Until: now.Add(2 * time.Hour), WaitingOn: parkExternalEvent}
		td.Supervision.Reviews = []TaskReview{{At: recent, Verdict: reviewParked}}
	}))
	// 9. Parked (expired until, idle > 2h) -> projection waiting/parked <-> tree stuck
	idParkedExpired := e.task("Parked expired stale", lead(withAgent("mac", "s-idle-old"), func(td *Todo) {
		td.CreatedAt, td.UpdatedAt = old, old
		td.Supervision.UpdatedAt = old
		td.Supervision.State = reviewParked
		td.Supervision.Parked = &Parked{Reason: "Waiting on vendor", Until: expired, WaitingOn: parkExternalEvent}
		td.Supervision.Reviews = []TaskReview{{At: old, Verdict: reviewParked}}
	}))
	// 10. Parked with real wake (agent running) -> projection working/agent_running <-> tree working
	idParkedWake := e.task("Parked real wake", lead(withAgent("mac", "s-run"), func(td *Todo) {
		td.Supervision.State = reviewParked
		td.Supervision.Parked = &Parked{Reason: "Waiting on vendor", Until: expired, WaitingOn: parkExternalEvent}
		td.Supervision.Reviews = []TaskReview{{At: old, Verdict: reviewParked}}
	}))
	// 11. Delivery merged (not live, idle > 2h) -> projection todo/rest <-> tree stuck
	idMergedStale := e.task("Delivery merged stale", lead(withAgent("mac", "s-idle-old"), func(td *Todo) {
		td.CreatedAt, td.UpdatedAt = old, old
		td.Supervision.UpdatedAt = old
		td.Delivery = &TodoDelivery{State: deliveryMerged, At: expired}
	}))
	// 12. Delivery live (unseen, idle) -> projection just_finished/finished_unseen <-> tree working
	idLive := e.task("Delivery live", lead(withAgent("mac", "s-idle-recent"), func(td *Todo) {
		td.Delivery = &TodoDelivery{State: deliveryLive, At: recent}
	}))
	// 13. Planned (not started: no agents) -> projection todo/rest <-> tree planned
	idPlanned := e.task("Planned not started", lead())
	// 14. Stale PR open (> 2h silence) -> projection waiting/pr_open <-> tree stuck
	idStalePR := e.task("Stale open PR", lead(withAgent("mac", "s-idle-old"), func(td *Todo) {
		td.CreatedAt, td.UpdatedAt = old, old
		td.Supervision.UpdatedAt = old
		td.PRURL = "https://github.com/example/app/pull/99"
		td.PRStatus = &TodoPRStatus{URL: td.PRURL, State: "open", CheckedAt: now}
	}))
	// 15. Unreached machine with stale task -> projection todo/rest (no inactive mark) <-> tree working (not stuck)
	idUnreached := e.task("Unreached machine", lead(withAgent("gone", "s-x"), func(td *Todo) {
		td.CreatedAt, td.UpdatedAt = old, old
		td.Supervision.UpdatedAt = old
		td.PRURL = "https://github.com/example/app/pull/100"
	}))

	// Build listed rows the same way GET /api/v1/todos does, then evaluate both projection and direction tree.
	e.s.todos.mu.Lock()
	var listed []listedTodo
	for _, td := range e.s.todos.todos {
		listed = append(listed, listedTodo{Todo: formatTodo(*td)})
	}
	e.s.todos.mu.Unlock()
	// Seed the open acceptAsk for idCanWait so both todoAsks() and directionTree() read the exact same deferred decision.
	e.s.accept.mu.Lock()
	e.s.accept.loaded = true
	if e.s.accept.asks == nil {
		e.s.accept.asks = map[string]*acceptAsk{}
	}
	e.s.accept.asks[idCanWait] = &acceptAsk{
		Conclusion:  true,
		Outcome:     outcomeDecide,
		Reason:      "Pick later",
		Recommend:   "Option A",
		Options:     canWaitOptions,
		AskedAt:     recent,
		Deadline:    deadline,
		Reversible:  true,
		ForBriefing: true,
	}
	e.s.accept.mu.Unlock()

	asks := e.s.todoAsks(ctx)
	for i := range listed {
		if a, ok := asks[listed[i].ID]; ok {
			a := a
			listed[i].Ask = &a
		}
	}

	proj := e.s.todoProjectionOf(ctx, listed, now).byID()
	tree := e.s.directionTree(ctx, now)
	treeState := map[string]string{}
	for _, tk := range tree.Directions[0].Tasks {
		treeState[tk.ID] = tk.State
	}

	cases := []struct {
		name        string
		id          string
		wantSection string
		wantReason  string
		wantTree    string
	}{
		{"agent_ask", idAgentAsk, assistantwire.TodoListSectionNeedsYou, assistantwire.TodoReasonAgentAsks, treeNeedsYou},
		{"owner_decide", idOwnerDecide, assistantwire.TodoListSectionNeedsYou, assistantwire.TodoReasonOwnerDecides, treeNeedsYou},
		{"can_wait", idCanWait, assistantwire.TodoListSectionCanWait, assistantwire.TodoReasonCanWait, treeNeedsYou},
		{"supervisor_held", idSupHeld, assistantwire.TodoListSectionWorking, assistantwire.TodoReasonPersonaWorking, treeNeedsYou},
		{"pending_review", idReview, assistantwire.TodoListSectionJustFinished, assistantwire.TodoReasonFinishedUnseen, treeReview},
		{"pending_review_running", idReviewRunning, assistantwire.TodoListSectionWorking, assistantwire.TodoReasonAgentRunning, treeWorking},
		{"working", idWorking, assistantwire.TodoListSectionWorking, assistantwire.TodoReasonAgentRunning, treeWorking},
		{"parked_active", idParkedActive, assistantwire.TodoListSectionWaiting, assistantwire.TodoReasonParked, treeWorking},
		{"parked_expired", idParkedExpired, assistantwire.TodoListSectionWaiting, assistantwire.TodoReasonParked, treeStuck},
		{"parked_real_wake", idParkedWake, assistantwire.TodoListSectionWorking, assistantwire.TodoReasonAgentRunning, treeWorking},
		{"delivery_merged_stale", idMergedStale, assistantwire.TodoListSectionTodo, assistantwire.TodoReasonRest, treeStuck},
		{"delivery_live", idLive, assistantwire.TodoListSectionJustFinished, assistantwire.TodoReasonFinishedUnseen, treeWorking},
		{"planned", idPlanned, assistantwire.TodoListSectionTodo, assistantwire.TodoReasonRest, treePlanned},
		{"stale_pr_open", idStalePR, assistantwire.TodoListSectionWaiting, assistantwire.TodoReasonPrOpen, treeStuck},
		{"unreached_machine", idUnreached, assistantwire.TodoListSectionTodo, assistantwire.TodoReasonRest, treeWorking},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := proj[tc.id]
			if !ok {
				t.Fatalf("missing projection view for %s", tc.id)
			}
			if v.Section != tc.wantSection || v.Reason != tc.wantReason {
				t.Errorf("projection: got %s/%s, want %s/%s", v.Section, v.Reason, tc.wantSection, tc.wantReason)
			}
			if gotTree := treeState[tc.id]; gotTree != tc.wantTree {
				t.Errorf("direction tree: got %q, want %q", gotTree, tc.wantTree)
			}
		})
	}
	if !proj[idUnreached].Inactive.IsZero() {
		t.Errorf("unreached machine should not get inactive mark: %+v", proj[idUnreached])
	}
}

// TestTodosWireBackwardCompatibilityContract pins the exact JSON keys and types returned by GET /api/v1/todos and
// GET /api/v1/todos/{id} so unmodified iOS/Android clients continue to decode every legacy field alongside `view` and `counts`.
func TestTodosWireBackwardCompatibilityContract(t *testing.T) {
	e := newTodoEnv(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	seen := now.Add(-20 * time.Minute)
	restored := now.Add(-15 * time.Minute)
	asked := now.Add(-10 * time.Minute)
	doneAt := now.Add(-5 * time.Minute)

	e.sessions("server", proto.Session{
		SessionID: "s-wire", Title: "Wire run", Status: proto.StatusIdle,
		State: proto.StateIdle, StateSince: asked, UpdatedAt: asked,
	})

	openRow := e.create("Rich open task")
	e.must("PUT", "/api/v1/todos/"+openRow.ID+"/agents/server/s-wire", "")
	e.s.todos.mu.Lock()
	td := e.s.todos.todos[openRow.ID]
	td.Stage = "review"
	td.Delivery = &TodoDelivery{State: deliveryMerged, At: asked}
	td.SeenAt = &seen
	td.RestoredAt = &restored
	td.Supervision = &Supervision{
		Supervisor: "conv_main",
		State:      reviewNeedsUser,
		Reviews: []TaskReview{{
			At:        asked,
			Verdict:   reviewNeedsUser,
			Reason:    "Ship now?",
			Recommend: "Yes",
		}},
		UpdatedAt: asked,
	}
	e.s.todos.mu.Unlock()
	e.s.accept.mu.Lock()
	e.s.accept.loaded = true
	if e.s.accept.asks == nil {
		e.s.accept.asks = map[string]*acceptAsk{}
	}
	e.s.accept.asks[openRow.ID] = &acceptAsk{
		Conclusion: true,
		Outcome:    outcomeDecide,
		Reason:     "Ship now?",
		Recommend:  "Yes",
		AskedAt:    asked,
	}
	e.s.accept.mu.Unlock()

	doneRow := e.create("Completed task")
	e.s.todos.mu.Lock()
	e.s.todos.todos[doneRow.ID].DoneAt = &doneAt
	e.s.todos.todos[doneRow.ID].DoneBy = db.DoneByAuto
	e.s.todos.mu.Unlock()

	cancelRow := e.create("Cancelled task")
	e.s.todos.mu.Lock()
	e.s.todos.todos[cancelRow.ID].DoneAt = &doneAt
	e.s.todos.todos[cancelRow.ID].DoneBy = db.DoneByCancelled
	e.s.todos.todos[cancelRow.ID].CancelledAt = &doneAt
	e.s.todos.todos[cancelRow.ID].CancelledReason = "No longer needed"
	e.s.todos.mu.Unlock()

	rawBody := e.must("GET", "/api/v1/todos", "").Body.Bytes()
	var listPayload map[string]any
	if err := json.Unmarshal(rawBody, &listPayload); err != nil {
		t.Fatalf("decode GET /api/v1/todos: %v", err)
	}

	// Top-level counts contract
	counts, ok := listPayload["counts"].(map[string]any)
	if !ok {
		t.Fatalf("missing or non-object top-level counts: %T", listPayload["counts"])
	}
	for _, k := range []string{"open", "needs_you", "in_progress"} {
		if _, ok := counts[k].(float64); !ok {
			t.Errorf("counts.%s: got %T, want number", k, counts[k])
		}
	}
	if _, ok := counts["sections"].(map[string]any); !ok {
		t.Errorf("counts.sections: got %T, want object", counts["sections"])
	}

	todosRaw, ok := listPayload["todos"].([]any)
	if !ok || len(todosRaw) != 3 {
		t.Fatalf("todos array: len=%d, ok=%v", len(todosRaw), ok)
	}
	byID := map[string]map[string]any{}
	for _, item := range todosRaw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("todo row is %T, want object", item)
		}
		id, _ := m["id"].(string)
		byID[id] = m
	}

	assertString := func(t *testing.T, obj map[string]any, key string) {
		t.Helper()
		v, ok := obj[key].(string)
		if !ok || v == "" {
			t.Errorf("key %q: got %#v, want non-empty string", key, obj[key])
		}
	}
	assertRFC3339 := func(t *testing.T, obj map[string]any, key string) {
		t.Helper()
		v, ok := obj[key].(string)
		if !ok {
			t.Errorf("key %q: got %T, want RFC3339 string", key, obj[key])
			return
		}
		if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
			t.Errorf("key %q=%q: invalid RFC3339: %v", key, v, err)
		}
	}

	// Open rich row: done_at=null, stage, delivery, supervision, agents, ask/asked_at, seen_at, restored_at, view
	openObj := byID[openRow.ID]
	if v, exists := openObj["done_at"]; !exists || v != nil {
		t.Errorf("open row done_at: exists=%v val=%#v, want explicit null", exists, v)
	}
	assertString(t, openObj, "stage")
	assertRFC3339(t, openObj, "seen_at")
	assertRFC3339(t, openObj, "restored_at")

	delivery, ok := openObj["delivery"].(map[string]any)
	if !ok {
		t.Fatalf("open row delivery: got %T, want object", openObj["delivery"])
	}
	assertString(t, delivery, "state")
	assertRFC3339(t, delivery, "at")

	supervision, ok := openObj["supervision"].(map[string]any)
	if !ok {
		t.Fatalf("open row supervision: got %T, want object", openObj["supervision"])
	}
	assertString(t, supervision, "supervisor")
	assertString(t, supervision, "state")
	if _, ok := supervision["reviews"].([]any); !ok {
		t.Errorf("supervision.reviews: got %T, want array", supervision["reviews"])
	}

	agents, ok := openObj["agents"].([]any)
	if !ok || len(agents) != 1 {
		t.Fatalf("open row agents: got %#v, want 1-element array", openObj["agents"])
	}
	agent0, _ := agents[0].(map[string]any)
	assertString(t, agent0, "machine_id")
	assertString(t, agent0, "session_id")

	ask, ok := openObj["ask"].(map[string]any)
	if !ok {
		t.Fatalf("open row ask: got %T, want object", openObj["ask"])
	}
	assertString(t, ask, "outcome")
	assertString(t, ask, "verdict")
	assertRFC3339(t, ask, "asked_at")
	assertString(t, ask, "risk")

	viewObj, ok := openObj["view"].(map[string]any)
	if !ok {
		t.Fatalf("open row view: got %T, want object", openObj["view"])
	}
	assertString(t, viewObj, "phase")
	assertString(t, viewObj, "section")
	assertString(t, viewObj, "reason")
	if _, ok := viewObj["rank"].(float64); !ok {
		t.Errorf("view.rank: got %T, want number", viewObj["rank"])
	}

	// Done row: done_at + done_by
	doneObj := byID[doneRow.ID]
	assertRFC3339(t, doneObj, "done_at")
	assertString(t, doneObj, "done_by")

	// Cancelled row: done_at + done_by + cancelled_at + cancelled_reason
	cancelObj := byID[cancelRow.ID]
	assertRFC3339(t, cancelObj, "done_at")
	assertString(t, cancelObj, "done_by")
	assertRFC3339(t, cancelObj, "cancelled_at")
	assertString(t, cancelObj, "cancelled_reason")

	// Single-item endpoint carries the exact same wire keys
	var singlePayload map[string]any
	if err := json.Unmarshal(e.must("GET", "/api/v1/todos/"+openRow.ID, "").Body.Bytes(), &singlePayload); err != nil {
		t.Fatalf("decode GET /api/v1/todos/{id}: %v", err)
	}
	singleTodo, ok := singlePayload["todo"].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/v1/todos/{id} missing todo object: %T", singlePayload["todo"])
	}
	for _, key := range []string{"done_at", "stage", "delivery", "supervision", "agents", "ask", "seen_at", "restored_at", "view"} {
		if _, exists := singleTodo[key]; !exists {
			t.Errorf("GET /api/v1/todos/{id} missing backward-compat key %q", key)
		}
	}
}
