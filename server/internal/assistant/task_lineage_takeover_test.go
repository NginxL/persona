package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Reusing an agent whose earlier card is closed creates a continuation, not an unrelated card. Both directions are
// visible in GET /api/v1/todos, with the hand-over note carried on the old card (#5732).
func TestAgentSuperviseAfterCloseLinksTheTakeoverLineage(t *testing.T) {
	s, f, old := newSupervisedService(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	raw, _ := json.Marshal(map[string]any{"todo_id": old.ID, "verdict": "accepted", "note": "Spec 已验收", "checks": completeReviewChecks(todoNow(s, old.ID))})
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, old.ID); got.DoneAt == nil {
		t.Fatal("the old card is not closed")
	}
	machine, ok := s.device(context.Background(), "box")
	if !ok {
		t.Fatal("task machine is missing")
	}
	agent, ok := s.session(context.Background(), "box", "s1")
	if !ok {
		t.Fatal("task agent is missing")
	}
	if _, made, err := s.adoptAgent(context.Background(), "conv1", machine, agent, "接手实现", "", "换 Agent 接手实现", nil, 0, true); err != nil || !made {
		t.Fatalf("adopt after close: made %v, err %v", made, err)
	}
	var current Todo
	for _, td := range s.todosOf(context.Background(), "*") {
		if td.DoneAt == nil {
			current = td
		}
	}
	if current.ID == "" || current.ID == old.ID {
		t.Fatalf("no new takeover card after %+v", old)
	}
	listed := listedTodos(t, s)
	steps := func(id string) []map[string]any {
		raw, _ := json.Marshal(listed[id]["lineage"])
		var out []map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	var oldBefore, newBefore map[string]any
	for _, step := range steps(old.ID) {
		if step["todo_id"] == old.ID {
			oldBefore = step
		}
	}
	for _, step := range steps(current.ID) {
		if step["todo_id"] == current.ID {
			newBefore = step
		}
	}
	if oldBefore["superseded_by"] != current.ID {
		t.Fatalf("old lineage = %+v", steps(old.ID))
	}
	if newBefore["continues"] != old.ID || newBefore["current"] != true {
		t.Fatalf("new lineage = %+v", steps(current.ID))
	}
	// The assistant's text snapshot exposes the same two directions and their result links, not only the API (#5732).
	shots := map[string]bool{}
	for _, task := range s.snapshotTasks(context.Background(), "https://hub.example.com") {
		shots[task.ID] = true
		switch task.ID {
		case old.ID:
			if task.SupersededBy != current.ID || task.SupersededByLink != "https://hub.example.com/todos/"+current.ID {
				t.Fatalf("old snapshot = %+v", task)
			}
		case current.ID:
			if task.Continues != old.ID || task.ContinuesLink != "https://hub.example.com/todos/"+old.ID {
				t.Fatalf("new snapshot = %+v", task)
			}
		}
	}
	if !shots[old.ID] || !shots[current.ID] {
		t.Fatalf("snapshot is missing a lineage card: %v", shots)
	}
	// The continuation now belongs to a replacement Agent. Re-supervising the old one must not open another continuation.
	s.todos.mu.Lock()
	delete(s.todos.activeBySession, [2]string{"box", "s1"})
	s.todos.activeBySession[[2]string{"box", "s2"}] = current.ID
	s.todos.todos[current.ID].Agents[0].SessionID = "s2"
	s.todos.mu.Unlock()
	before := todoCount(s)
	_, again, adoptErr := s.adoptAgent(context.Background(), "conv1", machine, agent, "再次接手", "", "重复接手", nil, 0, true)
	if adoptErr == nil || !strings.Contains(adoptErr.Error(), "already carried on by to-do "+current.ID) || again {
		t.Fatalf("reusing an existing continuation: made %v, err %v", again, adoptErr)
	}
	if got := todoCount(s); got != before {
		t.Fatalf("a repeated takeover made another card: before %d, after %d", before, got)
	}
}

// A newly titled agent_send task is new work, not an explicit takeover. It gets its own brief and acceptance criteria,
// leaves the closed card's result and scope alone, and creates no lineage in either direction (#5732).
func TestAgentSendAfterCloseStartsAnIndependentBrief(t *testing.T) {
	s, f, old := newSupervisedService(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	raw, _ := json.Marshal(map[string]any{"todo_id": old.ID, "verdict": "accepted", "note": "Spec 已验收", "checks": completeReviewChecks(todoNow(s, old.ID))})
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatal(err)
	}
	agent, ok := s.session(context.Background(), "box", "s1")
	if !ok {
		t.Fatal("closed task agent is missing")
	}
	agent.SessionID = "s1000000"
	f.setSessions("box", agent)
	s.todos.mu.Lock()
	s.todos.todos[old.ID].Agents[0].SessionID = agent.SessionID
	s.todos.mu.Unlock()
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{
		"machine": "box", "session_id": "s1000000", "text": "整理一份独立周报",
		"task": map[string]any{"title": "整理周报", "done_when": []string{"周报链接贴回来"}},
	}); err != nil {
		t.Fatal(err)
	}
	var current Todo
	for _, td := range s.todosOf(context.Background(), "*") {
		if td.DoneAt == nil {
			current = td
		}
	}
	if current.ID == "" || current.ID == old.ID || current.Supervision == nil ||
		len(current.Supervision.DoneWhen) != 1 || current.Supervision.DoneWhen[0] != "周报链接贴回来" {
		t.Fatalf("independent task = %+v", current)
	}
	oldBrief, oldOK := s.briefOfTodo(old.ID)
	newBrief, newOK := s.briefOfTodo(current.ID)
	if !oldOK || !newOK || oldBrief.ID == newBrief.ID {
		t.Fatalf("the new task inherited the closed task's brief: old=%+v new=%+v", oldBrief, newBrief)
	}
	if len(oldBrief.Criteria) != 2 || oldBrief.Criteria[0] != "PR 合并到 master" || oldBrief.Criteria[1] != "浅色 / 深色截图贴进 PR" {
		t.Fatalf("the closed brief's criteria changed: %+v", oldBrief.Criteria)
	}
	listed := listedTodos(t, s)
	oldLineage, _ := json.Marshal(listed[old.ID]["lineage"])
	newLineage, _ := json.Marshal(listed[current.ID]["lineage"])
	if strings.Contains(string(oldLineage), "superseded_by") || strings.Contains(string(newLineage), "continues") {
		t.Fatalf("unrelated work formed lineage: old=%s new=%s", oldLineage, newLineage)
	}
}

type failingTakeoverAgentStore struct {
	*db.DB
	fail        atomic.Bool
	failUnlink  atomic.Bool
	onFail      func()
	onBriefLink func()
}

func (s *failingTakeoverAgentStore) LinkTodoAgentWithSource(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) ([]string, error) {
	if s.fail.Load() {
		if s.onFail != nil {
			s.onFail()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("injected takeover Agent link failure")
	}
	return s.DB.LinkTodoAgentWithSource(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, linkedAt)
}

func (s *failingTakeoverAgentStore) UnlinkTaskBriefTodo(ctx context.Context, todoID string) error {
	if s.failUnlink.Load() {
		return errors.New("injected brief unlink failure")
	}
	return s.DB.UnlinkTaskBriefTodo(ctx, todoID)
}

func (s *failingTakeoverAgentStore) LinkTaskBriefTodo(ctx context.Context, link db.TaskBriefTodoRow) (bool, error) {
	linked, err := s.DB.LinkTaskBriefTodo(ctx, link)
	if err == nil && s.onBriefLink != nil {
		hook := s.onBriefLink
		s.onBriefLink = nil
		hook()
	}
	return linked, err
}

// failingPendingBriefStore holds a scope's durable write open until the test releases its failure.
type failingPendingBriefStore struct {
	*db.DB
	arm     atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *failingPendingBriefStore) PutTaskBriefState(ctx context.Context, id string, state json.RawMessage, at time.Time) (bool, error) {
	if !s.arm.Load() {
		return s.DB.PutTaskBriefState(ctx, id, state, at)
	}
	close(s.entered)
	<-s.release
	return false, errors.New("injected brief scope commit failure")
}

// outOfOrderBriefStore holds the first armed durable write while the second one completes, exposing whether changeBrief
// allows an older writer to overwrite a newer committed state.
type outOfOrderBriefStore struct {
	*db.DB
	arm     atomic.Bool
	calls   atomic.Int32
	first   chan struct{}
	second  chan struct{}
	release chan struct{}
}

func (s *outOfOrderBriefStore) PutTaskBriefState(ctx context.Context, id string, state json.RawMessage, at time.Time) (bool, error) {
	if !s.arm.Load() {
		return s.DB.PutTaskBriefState(ctx, id, state, at)
	}
	if s.calls.Add(1) == 1 {
		close(s.first)
		<-s.release
	} else {
		close(s.second)
	}
	return s.DB.PutTaskBriefState(ctx, id, state, at)
}

// A continuation whose Agent cannot be linked is closed, but it must not remain in the closed task's durable lineage.
func TestFailedTakeoverAgentLinkRollsBackLineage(t *testing.T) {
	st := &failingTakeoverAgentStore{DB: todoTestDB(t)}
	s, f, old := newSupervisedServiceOn(t, st)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	raw, _ := json.Marshal(map[string]any{"todo_id": old.ID, "verdict": "accepted", "note": "Spec 已验收", "checks": completeReviewChecks(todoNow(s, old.ID))})
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatal(err)
	}
	machine, ok := s.device(context.Background(), "box")
	if !ok {
		t.Fatal("task machine is missing")
	}
	agent, ok := s.session(context.Background(), "box", "s1")
	if !ok {
		t.Fatal("task agent is missing")
	}
	st.fail.Store(true)
	briefBefore, ok := s.briefOfTodo(old.ID)
	if !ok {
		t.Fatal("closed task has no brief")
	}
	if _, made, err := s.adoptAgent(context.Background(), "conv1", machine, agent, "接手实现", "", "换 Agent 接手实现", []string{"criterion from a failed takeover"}, 7, true); err == nil || made {
		t.Fatalf("adopt with a failed Agent link: made %v, err %v", made, err)
	}
	if lineage := s.taskLineage(old.ID); len(lineage) != 1 || lineage[0].SupersededBy != "" {
		t.Fatalf("failed takeover rewrote the previous task's lineage: %+v", lineage)
	}
	for _, todo := range s.todosOf(context.Background(), "*") {
		if todo.ID == old.ID {
			continue
		}
		if todo.DoneAt == nil {
			t.Fatalf("failed takeover left an open card: %+v", todo)
		}
		if _, linked := s.briefOfTodo(todo.ID); linked {
			t.Fatalf("failed takeover stayed on the previous task's brief: %+v", todo)
		}
	}
	_, links, err := st.ListTaskBriefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range links {
		if link.Continues == old.ID {
			t.Fatalf("failed takeover left a durable continuation: %+v", link)
		}
	}
	briefAfter, ok := s.briefOfTodo(old.ID)
	if !ok || !slices.Equal(briefAfter.Criteria, briefBefore.Criteria) || briefAfter.ExpectNow != briefBefore.ExpectNow || len(briefAfter.Log) != len(briefBefore.Log) {
		t.Fatalf("failed takeover changed the shared brief: before %+v, after %+v", briefBefore, briefAfter)
	}
}

// Caller cancellation during the Agent link must not stop the aborted continuation's own cleanup on real Postgres.
func TestCancelledTakeoverStillClosesItsCard(t *testing.T) {
	st := &failingTakeoverAgentStore{DB: todoTestDB(t)}
	s, f, old := newSupervisedServiceOn(t, st)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	raw, _ := json.Marshal(map[string]any{"todo_id": old.ID, "verdict": "accepted", "note": "Spec 已验收", "checks": completeReviewChecks(todoNow(s, old.ID))})
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatal(err)
	}
	machine, ok := s.device(context.Background(), "box")
	if !ok {
		t.Fatal("task machine is missing")
	}
	agent, ok := s.session(context.Background(), "box", "s1")
	if !ok {
		t.Fatal("task agent is missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.onFail = cancel
	st.fail.Store(true)
	if _, made, err := s.adoptAgent(ctx, "conv1", machine, agent, "接手实现", "", "换 Agent 接手实现", nil, 0, true); !errors.Is(err, context.Canceled) || made {
		t.Fatalf("cancelled adoption: made %v, err %v", made, err)
	}
	for _, todo := range s.todosOf(context.Background(), "*") {
		if todo.ID != old.ID && todo.DoneAt == nil {
			t.Fatalf("cancellation left an aborted takeover open: %+v", todo)
		}
	}
	if lineage := s.taskLineage(old.ID); len(lineage) != 1 || lineage[0].SupersededBy != "" {
		t.Fatalf("cancelled takeover rewrote the previous task's lineage: %+v", lineage)
	}
}

// A failed takeover rolls back every open ledger its scope change touched on the shared brief, not only the brief's log.
func TestFailedTakeoverRestoresSiblingLedger(t *testing.T) {
	st := &failingTakeoverAgentStore{DB: todoTestDB(t)}
	s, f, old := newSupervisedServiceOn(t, st)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	raw, _ := json.Marshal(map[string]any{"todo_id": old.ID, "verdict": "accepted", "note": "Spec 已验收", "checks": completeReviewChecks(todoNow(s, old.ID))})
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatal(err)
	}
	brief, ok := s.briefOfTodo(old.ID)
	if !ok {
		t.Fatal("closed task has no brief")
	}
	sibling, err := s.createTodo(context.Background(), "Open sibling on the same brief", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(context.Background(), sibling.ID, "conv1", brief.Criteria, brief.ExpectNow); err != nil {
		t.Fatal(err)
	}
	if err := s.linkTaskBrief(context.Background(), brief.ID, sibling.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	machine, ok := s.device(context.Background(), "box")
	if !ok {
		t.Fatal("task machine is missing")
	}
	agent, ok := s.session(context.Background(), "box", "s1")
	if !ok {
		t.Fatal("task agent is missing")
	}
	st.fail.Store(true)
	if _, made, err := s.adoptAgent(context.Background(), "conv1", machine, agent, "接手实现", "", "换 Agent 接手实现", []string{"criterion from a failed takeover"}, 7, true); err == nil || made {
		t.Fatalf("adopt with a failed Agent link: made %v, err %v", made, err)
	}
	got := todoNow(s, sibling.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, brief.Criteria) || got.ExpectMinutes != brief.ExpectNow {
		t.Fatalf("failed takeover left its scope on the sibling ledger: %+v, want criteria %v and %d minutes", got, brief.Criteria, brief.ExpectNow)
	}
}

// A legitimate sibling scope committed while the failed takeover is being cleaned up is not the takeover's to roll back.
func TestFailedTakeoverPreservesConcurrentSiblingScope(t *testing.T) {
	st := &failingTakeoverAgentStore{DB: todoTestDB(t)}
	s, f, old := newSupervisedServiceOn(t, st)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	raw, _ := json.Marshal(map[string]any{"todo_id": old.ID, "verdict": "accepted", "note": "Spec 已验收", "checks": completeReviewChecks(todoNow(s, old.ID))})
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatal(err)
	}
	brief, ok := s.briefOfTodo(old.ID)
	if !ok {
		t.Fatal("closed task has no brief")
	}
	sibling, err := s.createTodo(context.Background(), "Open sibling on the same brief", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(context.Background(), sibling.ID, "conv1", brief.Criteria, brief.ExpectNow); err != nil {
		t.Fatal(err)
	}
	if err := s.linkTaskBrief(context.Background(), brief.ID, sibling.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	machine, ok := s.device(context.Background(), "box")
	if !ok {
		t.Fatal("task machine is missing")
	}
	agent, ok := s.session(context.Background(), "box", "s1")
	if !ok {
		t.Fatal("task agent is missing")
	}
	concurrent := []string{"legitimate concurrent criterion"}
	st.onFail = func() {
		if _, err := s.rescope(context.Background(), sibling, "conv1", "conv1", "concurrent_test_scope", concurrent, 11); err != nil {
			t.Errorf("concurrent sibling scope: %v", err)
		}
	}
	st.fail.Store(true)
	if _, made, err := s.adoptAgent(context.Background(), "conv1", machine, agent, "接手实现", "", "换 Agent 接手实现", []string{"criterion from a failed takeover"}, 7, true); err == nil || made {
		t.Fatalf("adopt with a failed Agent link: made %v, err %v", made, err)
	}
	got := todoNow(s, sibling.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, concurrent) || got.ExpectMinutes != 11 {
		t.Fatalf("failed takeover rolled back a concurrent legitimate scope: %+v", got)
	}
	if after, ok := s.briefOfTodo(sibling.ID); !ok || !slices.Equal(after.Criteria, concurrent) || after.ExpectNow != 11 {
		t.Fatalf("brief and sibling ledger diverged after concurrent scope: brief %+v, ledger %+v", after, got)
	}
}

func continuationRollbackSetup(t *testing.T, st Store) (*Service, Todo, Todo, Todo, TaskBrief) {
	t.Helper()
	s, f, old := newSupervisedServiceOn(t, st)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	raw, _ := json.Marshal(map[string]any{"todo_id": old.ID, "verdict": "accepted", "note": "Spec 已验收", "checks": completeReviewChecks(todoNow(s, old.ID))})
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err != nil {
		t.Fatal(err)
	}
	brief, ok := s.briefOfTodo(old.ID)
	if !ok {
		t.Fatal("closed task has no brief")
	}
	sibling, err := s.createTodo(context.Background(), "Open sibling on the same brief", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(context.Background(), sibling.ID, "conv1", brief.Criteria, brief.ExpectNow); err != nil {
		t.Fatal(err)
	}
	if err := s.linkTaskBrief(context.Background(), brief.ID, sibling.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	failed, err := s.createTodo(context.Background(), "Aborted continuation", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	return s, old, failed, sibling, brief
}

func continuationRollbackFixtureOn(t *testing.T, st Store) (*Service, Todo, Todo, TaskBrief, *continuationRollback) {
	t.Helper()
	s, old, failed, sibling, brief := continuationRollbackSetup(t, st)
	_, rollback, err := s.continueBriefFor(context.Background(), "conv1", "conv1", old, failed, "test rollback", []string{"criterion from a failed takeover"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	return s, failed, sibling, brief, rollback
}

func continuationRollbackFixture(t *testing.T) (*Service, Todo, Todo, TaskBrief, *continuationRollback) {
	t.Helper()
	return continuationRollbackFixtureOn(t, todoTestDB(t))
}

// Even when a concurrent scope is included in the after snapshot, the brief's event history says it is not the failed
// takeover's own write and must survive the rollback.
func TestFailedTakeoverDoesNotOwnConcurrentScopeInAfterSnapshot(t *testing.T) {
	s, failed, sibling, _, rollback := continuationRollbackFixture(t)
	concurrent := []string{"legitimate concurrent criterion"}
	if _, err := s.rescope(context.Background(), sibling, "conv1", "conv1", "concurrent_test_scope", concurrent, 11); err != nil {
		t.Fatal(err)
	}
	rollback.after[sibling.ID] = todoNow(s, sibling.ID).Supervision.clone()
	s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
	got := todoNow(s, sibling.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, concurrent) || got.ExpectMinutes != 11 {
		t.Fatalf("after snapshot hid a concurrent scope from the rollback: %+v", got)
	}
	if brief, ok := s.briefOfTodo(sibling.ID); !ok || !slices.Equal(brief.Criteria, concurrent) || brief.ExpectNow != 11 {
		t.Fatalf("brief and concurrent ledger diverged: brief %+v, ledger %+v", brief, got)
	}
}

// A concurrent review is preserved, but it does not entitle the failed takeover's criteria to stay on the sibling.
func TestFailedTakeoverRestoresCriteriaAcrossConcurrentReview(t *testing.T) {
	s, failed, sibling, brief, rollback := continuationRollbackFixture(t)
	reviewTask(t, s, sibling.ID, map[string]any{"verdict": "needs_user", "reason": "Choose the rollout", "recommend": "Option A"})
	s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
	got := todoNow(s, sibling.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, brief.Criteria) || got.ExpectMinutes != brief.ExpectNow {
		t.Fatalf("concurrent review retained the failed takeover criteria: %+v", got)
	}
	if got.State != reviewNeedsUser || len(got.Reviews) != 1 || got.Reviews[0].Verdict != reviewNeedsUser {
		t.Fatalf("rollback discarded the concurrent review: %+v", got)
	}
}

// A scope committed after the rollback reads the restored brief must win over that stale snapshot.
func TestFailedTakeoverRechecksBriefAfterLedgerWrite(t *testing.T) {
	s, failed, sibling, _, rollback := continuationRollbackFixture(t)
	s.todos.writing.Lock()
	done := make(chan struct{})
	go func() {
		s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
		close(done)
	}()
	waitForSuperviseMutex(t, "updateSupervisionGuarded", &s.todos.writing)
	concurrent := []string{"scope after the restored brief snapshot"}
	if _, err := s.rescope(context.Background(), sibling, "conv1", "conv1", "concurrent_test_scope", concurrent, 11); err != nil {
		t.Fatal(err)
	}
	s.todos.writing.Unlock()
	<-done
	got := todoNow(s, sibling.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, concurrent) || got.ExpectMinutes != 11 {
		t.Fatalf("stale brief snapshot overrode a later scope: %+v", got)
	}
	if brief, ok := s.briefOfTodo(sibling.ID); !ok || !slices.Equal(brief.Criteria, concurrent) || brief.ExpectNow != 11 {
		t.Fatalf("brief and later ledger diverged: brief %+v, ledger %+v", brief, got)
	}
}

// A before snapshot can see a scope's log entry before that scope reaches the sibling ledger. Restoring the snapshot's
// old criteria would then contradict the brief even though no event appears after the snapshot's log baseline.
func TestFailedTakeoverDoesNotRestoreCriteriaFromIncompleteBeforeSnapshot(t *testing.T) {
	s, old, failed, sibling, _ := continuationRollbackSetup(t, todoTestDB(t))
	concurrent := []string{"scope between the before log and ledger snapshots"}
	if _, _, err := s.changeBrief(context.Background(), mustBriefOfTodo(t, s, old.ID).ID, func(tb *taskBrief) bool {
		tb.st.Log = append(tb.st.Log, BriefEvent{At: time.Now().UTC(), Kind: briefEventScope, Todo: sibling.ID,
			DoneWhen: concurrent, ExpectMinutes: 11})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	_, rollback, err := s.continueBriefFor(context.Background(), "conv1", "conv1", old, failed, "test rollback", []string{"criterion from a failed takeover"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
	got := todoNow(s, sibling.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, concurrent) || got.ExpectMinutes != 11 {
		t.Fatalf("incomplete before snapshot replaced the committed scope: %+v", got)
	}
}

// A member that joins after the before snapshot still has to follow the brief restored by the failed takeover.
func TestFailedTakeoverRestoresSiblingAddedAfterBeforeSnapshot(t *testing.T) {
	s, failed, sibling, brief, rollback := continuationRollbackFixture(t)
	late, err := s.createTodo(context.Background(), "Late sibling on the same brief", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	failedBrief, ok := s.briefOfTodo(sibling.ID)
	if !ok {
		t.Fatal("shared brief is missing")
	}
	if err := s.superviseTodo(context.Background(), late.ID, "conv1", failedBrief.Criteria, failedBrief.ExpectNow); err != nil {
		t.Fatal(err)
	}
	if err := s.linkTaskBrief(context.Background(), failedBrief.ID, late.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
	got := todoNow(s, late.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, brief.Criteria) || got.ExpectMinutes != brief.ExpectNow {
		t.Fatalf("late sibling retained the failed takeover criteria: %+v", got)
	}
}

// A legitimate scope on the aborted card is a separate event, not another copy of the takeover's scope to delete.
func TestFailedTakeoverRemovesOnlyItsOwnScopeEvent(t *testing.T) {
	s, failed, sibling, _, rollback := continuationRollbackFixture(t)
	concurrent := []string{"legitimate scope on the aborted card"}
	if _, err := s.rescope(context.Background(), todoNow(s, failed.ID), "conv1", "conv1", "concurrent_test_scope", concurrent, 11); err != nil {
		t.Fatal(err)
	}
	s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
	got := todoNow(s, sibling.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, concurrent) || got.ExpectMinutes != 11 {
		t.Fatalf("rollback removed a concurrent scope on the aborted card: %+v", got)
	}
	if brief, ok := s.briefOfTodo(sibling.ID); !ok || !slices.Equal(brief.Criteria, concurrent) || brief.ExpectNow != 11 {
		t.Fatalf("brief and concurrent aborted-card scope diverged: brief %+v, ledger %+v", brief, got)
	}
}

// Removing the scope and removing the link are separate durable steps. A link failure must not skip ledger restoration
// after the brief itself has already been restored.
func TestFailedTakeoverRestoresLedgersWhenUnlinkFails(t *testing.T) {
	st := &failingTakeoverAgentStore{DB: todoTestDB(t)}
	st.failUnlink.Store(true)
	s, failed, sibling, brief, rollback := continuationRollbackFixtureOn(t, st)
	s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
	if _, ok := s.briefOfTodo(failed.ID); !ok {
		t.Fatal("the injected unlink failure did not leave the continuation link in place")
	}
	got := todoNow(s, sibling.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, brief.Criteria) || got.ExpectMinutes != brief.ExpectNow {
		t.Fatalf("unlink failure skipped ledger restoration: %+v", got)
	}
}

func mustBriefOfTodo(t *testing.T, s *Service, id string) TaskBrief {
	t.Helper()
	brief, ok := s.briefOfTodo(id)
	if !ok {
		t.Fatalf("task %s has no brief", id)
	}
	return brief
}

// Starting a new supervision round clears acceptance without changing Round or Reviews at round zero. Rollback must not
// mistake that independent transition for the failed scope's own state reset.
func TestFailedTakeoverPreservesConcurrentSupervisionRound(t *testing.T) {
	st := &failingTakeoverAgentStore{DB: todoTestDB(t)}
	s, old, failed, sibling, _ := continuationRollbackSetup(t, st)
	if _, _, err := s.updateSupervision(context.Background(), sibling.ID, func(v *Supervision) bool {
		v.State = reviewAccepted
		return true
	}); err != nil {
		t.Fatal(err)
	}
	st.onBriefLink = func() { s.startSupervisionRound(context.Background(), sibling.ID) }
	_, rollback, err := s.continueBriefFor(context.Background(), "conv1", "conv1", old, failed, "test rollback", []string{"criterion from a failed takeover"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
	if got := todoNow(s, sibling.ID).Supervision; got == nil || got.State == reviewAccepted {
		t.Fatalf("rollback restored acceptance over a concurrent supervision round: %+v", got)
	}
}

// A revision read while PutTaskBriefState is still pending is not a committed brief. When that write fails, rollback
// must observe the reverted brief and realign the ledger instead of retaining the uncommitted scope.
func TestFailedTakeoverWaitsForBriefCommitToSettle(t *testing.T) {
	st := &failingPendingBriefStore{DB: todoTestDB(t), entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-st.release:
		default:
			close(st.release)
		}
	}()
	s, failed, sibling, brief, rollback := continuationRollbackFixtureOn(t, st)
	// Establish the post-unlink stage explicitly. The second unlink below is idempotent and does not need the brief
	// write lock, so the pending scope is the only writer and the test cannot deadlock its own fault injection.
	if err := s.unlinkTaskBrief(context.Background(), failed.ID, rollback.eventID); err != nil {
		t.Fatal(err)
	}
	st.arm.Store(true)
	concurrent := []string{"scope whose durable commit fails"}
	scopeDone := make(chan struct{})
	go func() {
		_, _ = s.rescope(context.Background(), sibling, "conv1", "conv1", "concurrent_test_scope", concurrent, 11)
		close(scopeDone)
	}()
	<-st.entered
	dropDone := make(chan struct{})
	go func() {
		s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
		close(dropDone)
	}()
	waitFor(t, "rollback to use the pending scope or wait for its commit", func() bool {
		if got := todoNow(s, sibling.ID).Supervision; got != nil && slices.Equal(got.DoneWhen, concurrent) {
			return true
		}
		for _, stack := range goroutineStacks() {
			if strings.Contains(stack, "(*Service).briefSnapshotByID(") && strings.Contains(stack, "(*Cond).Wait") {
				return true
			}
		}
		return false
	})
	close(st.release)
	<-dropDone
	<-scopeDone
	got := todoNow(s, sibling.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, brief.Criteria) || got.ExpectMinutes != brief.ExpectNow {
		t.Fatalf("failed brief commit left its uncommitted scope on the ledger: %+v", got)
	}
	if after, ok := s.briefOfTodo(sibling.ID); !ok || !slices.Equal(after.Criteria, brief.Criteria) || after.ExpectNow != brief.ExpectNow {
		t.Fatalf("brief and ledger diverged after the failed commit: brief %+v, ledger %+v", after, got)
	}
}

// A new round started after the failed scope already cleared acceptance is still a new round. Its epoch must advance
// even though startSupervisionRound has no further Supervision field to change.
func TestFailedTakeoverPreservesRoundStartedAfterScopeReset(t *testing.T) {
	s, old, failed, sibling, _ := continuationRollbackSetup(t, todoTestDB(t))
	if _, _, err := s.updateSupervision(context.Background(), sibling.ID, func(v *Supervision) bool {
		v.State = reviewAccepted
		return true
	}); err != nil {
		t.Fatal(err)
	}
	_, rollback, err := s.continueBriefFor(context.Background(), "conv1", "conv1", old, failed, "test rollback", []string{"criterion from a failed takeover"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	s.startSupervisionRound(context.Background(), sibling.ID)
	s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
	if got := todoNow(s, sibling.ID).Supervision; got == nil || got.State == reviewAccepted {
		t.Fatalf("rollback restored acceptance over a round started after the scope reset: %+v", got)
	}
}

// The durable write is part of a brief state's ordered transition, not something an older concurrent writer may finish
// after a newer one. The last committed memory and Postgres state must both be the second scope.
func TestBriefWritesSerializeDurableCommitOrder(t *testing.T) {
	st := &outOfOrderBriefStore{DB: todoTestDB(t), first: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-st.release:
		default:
			close(st.release)
		}
	}()
	s, _, old := newSupervisedServiceOn(t, st)
	brief := mustBriefOfTodo(t, s, old.ID)
	st.arm.Store(true)
	firstDone := make(chan struct{})
	go func() {
		_, _, _ = s.changeBrief(context.Background(), brief.ID, func(tb *taskBrief) bool {
			tb.st.Log = append(tb.st.Log, BriefEvent{At: time.Now().UTC(), Kind: briefEventScope, Todo: old.ID, DoneWhen: []string{"older blocked scope"}})
			return true
		})
		close(firstDone)
	}()
	<-st.first
	secondDone := make(chan struct{})
	go func() {
		_, _, _ = s.changeBrief(context.Background(), brief.ID, func(tb *taskBrief) bool {
			tb.st.Log = append(tb.st.Log, BriefEvent{At: time.Now().UTC(), Kind: briefEventScope, Todo: old.ID, DoneWhen: []string{"newer committed scope"}})
			return true
		})
		close(secondDone)
	}()
	waitFor(t, "the second writer to enter Postgres or wait for the brief write lock", func() bool {
		select {
		case <-st.second:
			return true
		default:
		}
		for _, stack := range goroutineStacks() {
			if strings.Contains(stack, "(*Service).changeBrief(") && strings.Contains(stack, "[sync.Mutex.Lock]") {
				return true
			}
		}
		return false
	})
	close(st.release)
	<-firstDone
	<-secondDone
	rows, _, err := st.ListTaskBriefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID != brief.ID {
			continue
		}
		var stored briefState
		if err := json.Unmarshal(row.State, &stored); err != nil {
			t.Fatal(err)
		}
		if len(stored.Log) == 0 || !slices.Equal(stored.Log[len(stored.Log)-1].DoneWhen, []string{"newer committed scope"}) {
			t.Fatalf("an older writer overwrote the newer durable scope: %+v", stored.Log)
		}
		return
	}
	t.Fatalf("brief %s is missing from Postgres", brief.ID)
}

// Committing a brief and propagating that scope to its ledgers is one ordered transition. An older writer paused after
// its commit must not write its older criteria after a newer scope has already reached the ledgers.
func TestScopeLedgerPropagationFollowsBriefCommitOrder(t *testing.T) {
	s, _, old := newSupervisedServiceOn(t, todoTestDB(t))
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	firstDone := make(chan struct{})
	go func() {
		_, _ = s.applyScope(context.Background(), old, scopeChange{DoneWhen: []string{"older propagated scope"}, Change: "older scope", afterCommit: func() {
			close(entered)
			<-release
		}})
		close(firstDone)
	}()
	<-entered
	secondDone := make(chan struct{})
	go func() {
		_, _ = s.applyScope(context.Background(), old, scopeChange{DoneWhen: []string{"newer propagated scope"}, Change: "newer scope"})
		close(secondDone)
	}()
	waitFor(t, "the second scope to finish or wait for the propagation boundary", func() bool {
		select {
		case <-secondDone:
			return true
		default:
		}
		for _, stack := range goroutineStacks() {
			if strings.Contains(stack, "(*Service).applyScope(") && strings.Contains(stack, "[sync.Mutex.Lock]") {
				return true
			}
		}
		return false
	})
	close(release)
	<-firstDone
	<-secondDone
	got := todoNow(s, old.ID).Supervision
	if got == nil || !slices.Equal(got.DoneWhen, []string{"newer propagated scope"}) {
		t.Fatalf("an older scope propagated after the newer scope: %+v", got)
	}
	if brief := mustBriefOfTodo(t, s, old.ID); !slices.Equal(brief.Criteria, got.DoneWhen) {
		t.Fatalf("brief and ledger diverged after ordered propagation: brief %+v, ledger %+v", brief, got)
	}
}

// A takeover that changes only the estimate logs an estimate event, not a scope event: when its Agent link fails, the
// rollback takes that event back too, so the brief, its clock and the sibling ledgers keep the estimate they had
// (review of #13130).
func TestFailedEstimateOnlyTakeoverRollsBack(t *testing.T) {
	for _, store := range []struct {
		name string
		open func(t *testing.T) Store
	}{
		{"memory", func(*testing.T) Store { return nil }},
		{"store", func(t *testing.T) Store { return &failingTakeoverAgentStore{DB: todoTestDB(t)} }},
	} {
		t.Run(store.name, func(t *testing.T) {
			s, old, failed, sibling, brief := continuationRollbackSetup(t, store.open(t))
			want, logged := brief.ExpectNow, len(mustBriefOfTodo(t, s, sibling.ID).Log)
			_, rollback, err := s.continueBriefFor(context.Background(), "conv1", "conv1", old, failed, "test rollback", nil, want+37)
			if err != nil {
				t.Fatal(err)
			}
			if b := mustBriefOfTodo(t, s, sibling.ID); b.ExpectNow != want+37 || b.Log[len(b.Log)-1].Kind != briefEventEstimate {
				t.Fatalf("the takeover's estimate was not logged as an estimate: %+v", b)
			}
			s.dropContinuation(context.Background(), failed.ID, rollback, errors.New("injected Agent link failure"))
			after := mustBriefOfTodo(t, s, sibling.ID)
			for _, ev := range after.Log {
				if ev.eventID == rollback.eventID {
					t.Errorf("the aborted takeover's %q event survived the rollback: %+v", ev.Kind, ev)
				}
			}
			if after.ExpectNow != want || len(after.Log) != logged {
				t.Errorf("brief after the rollback: expect %d with %d events, want %d with %d", after.ExpectNow, len(after.Log), want, logged)
			}
			if got := todoNow(s, sibling.ID).Supervision; got == nil || got.ExpectMinutes != want {
				t.Errorf("sibling ledger after the rollback: %+v, want %d minutes", got, want)
			}
		})
	}
}
