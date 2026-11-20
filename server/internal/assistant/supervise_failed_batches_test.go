package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func failedBatchSignal(todo Todo, at time.Time) superviseSignal {
	return superviseSignal{Todo: todo.ID, Kind: sigFailed, Key: "workspace was not created", Machine: "box", Launcher: "work",
		Detail: "workspace was not created", At: at, Generation: at}
}

func newFailedBatchService(t *testing.T, st Store) (*Service, *fakeHub, map[string]Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	f.known["lead1"], f.known["lead2"] = true, true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1", "lead2"} })
	todos := map[string]Todo{}
	for _, item := range []struct{ id, title, supervisor string }{
		{"todo_a", "first task", "lead1"}, {"todo_b", "second task", "lead1"},
		{"todo_c", "third task", "lead1"}, {"todo_d", "fourth task", "lead1"},
		{"todo_e", "fifth task", "lead1"}, {"todo_other", "other direction", "lead2"},
		{"todo_main", "main task", "conv1"},
	} {
		todo, err := s.newTodoAs(context.Background(), item.id, item.title, "do "+item.title, item.supervisor, "", false)
		if err != nil {
			t.Fatal(err)
		}
		s.superviseTodo(context.Background(), todo.ID, item.supervisor, nil, 0)
		todos[item.id] = todo
	}
	return s, f, todos
}

func queuedFailedStart(s *Service, id string) bool {
	for _, msg := range queuedRelays(s, id) {
		if msg.Tool == superviseTool && strings.Contains(msg.Text, "signal=failed") {
			return true
		}
	}
	return false
}

func TestLeaderFailedStartGoesOnlyToThatLeader(t *testing.T) {
	s, f, todo := newLeaderService(t)
	ctx := context.Background()
	if err := s.failTodo(ctx, todo.ID, "workspace was not created"); err != nil {
		t.Fatal(err)
	}
	todo = todoNow(s, todo.ID)
	s.taskStartFailed(ctx, todo, "box", nil, "workspace was not created")
	waitFor(t, "the leader's failed start", func() bool { return len(supervisionTo(f, "lead1", sigFailed)) == 1 })
	if got := supervisionTo(f, "conv1", sigFailed); len(got) != 0 || queuedFailedStart(s, "conv1") {
		t.Fatalf("the main conversation got the leader's failed start: %q queued=%v", got, queuedFailedStart(s, "conv1"))
	}
	if got := supervisionTo(f, "lead2", sigFailed); len(got) != 0 || queuedFailedStart(s, "lead2") {
		t.Fatalf("another leader got the failed start: %q queued=%v", got, queuedFailedStart(s, "lead2"))
	}
	if text := supervisionTo(f, "lead1", sigFailed)[0]; !strings.Contains(text, todo.ID) || !strings.Contains(text, "workspace was not created") {
		t.Fatalf("the leader's message lacks the task or reason: %s", text)
	}
}

func TestMainFailedStartKeepsItsExistingRoute(t *testing.T) {
	s, f := newQuietService(t)
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	todo, err := s.newTodoAs(context.Background(), "todo_main_route", "main task", "do it", "conv1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(context.Background(), todo.ID, "conv1", nil, 0)
	if err := s.failTodo(context.Background(), todo.ID, "workspace was not created"); err != nil {
		t.Fatal(err)
	}
	s.taskStartFailed(context.Background(), todoNow(s, todo.ID), "box", nil, "workspace was not created")
	waitFor(t, "the main conversation's failed start", func() bool { return len(supervisionTo(f, "conv1", sigFailed)) == 1 })
	if got := supervisionTo(f, "lead1", sigFailed); len(got) != 0 || queuedFailedStart(s, "lead1") {
		t.Fatalf("the leader got the main task's failed start: %q queued=%v", got, queuedFailedStart(s, "lead1"))
	}
}

func TestFailedStartBatchesShareOneRoundAndRecoverAfterRestart(t *testing.T) {
	st := &memStore{}
	now := time.Now().UTC()
	s, f, todos := newFailedBatchService(t, st)
	ctx := context.Background()
	first := failedBatchSignal(todos["todo_a"], now)
	if !s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{first}, "", now, time.Time{}) {
		t.Fatal("the first failure was not delivered")
	}
	second := failedBatchSignal(todos["todo_b"], now.Add(10*time.Minute))
	if s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{second}, "", now.Add(10*time.Minute), now) {
		t.Fatal("a same-group second failure delivered during the cooldown")
	}
	if got := len(supervisionTo(f, "lead1", sigFailed)); got != 1 {
		t.Fatalf("failed-start rounds during the cooldown = %d, want 1", got)
	}
	var doc failedStartBatchDoc
	if ok, err := st.Setting(ctx, failedStartBatchesKey, &doc); err != nil || !ok || len(doc.Batches) != 1 {
		t.Fatalf("persisted batches = %+v, ok=%v err=%v", doc, ok, err)
	}
	for _, batch := range doc.Batches {
		if len(batch.Deferred) != 1 || batch.Deferred["todo_b"].Todo != "todo_b" || !batch.Deferred["todo_b"].NotBefore.Equal(batch.Due) {
			t.Fatalf("deferred member = %+v, want todo_b at the boundary", batch)
		}
	}

	restarted, nextHub, _ := newFailedBatchService(t, st)
	if err := restarted.restoreFailedStartBatches(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.supervise.mu.Lock()
	pending := restarted.supervise.pending["todo_b|failed"]
	restarted.supervise.mu.Unlock()
	if pending.Todo != "todo_b" || pending.NotBefore.IsZero() {
		t.Fatalf("restart lost the deferred failure: %+v", pending)
	}
	if restarted.deliverDigest(ctx, restarted.settings(ctx), "lead1", []superviseSignal{pending}, "", now.Add(29*time.Minute+59*time.Second), now) {
		t.Fatal("the deferred failure delivered before the exact boundary")
	}
	if got := len(supervisionTo(nextHub, "lead1", sigFailed)); got != 0 {
		t.Fatalf("early rounds after restart = %d, want 0", got)
	}
	if !restarted.deliverDigest(ctx, restarted.settings(ctx), "lead1", []superviseSignal{pending}, "", now.Add(30*time.Minute), now) {
		t.Fatal("the deferred failure did not deliver at the boundary")
	}
	messages := supervisionTo(nextHub, "lead1", sigFailed)
	if len(messages) != 1 || !strings.Contains(messages[0], "todo_b") || !strings.Contains(messages[0], "second task") {
		t.Fatalf("boundary message = %q, want the affected second task", messages)
	}
}

func TestFailedStartBatchesKeepDifferentKeysAndRetriesImmediate(t *testing.T) {
	st := &memStore{}
	now := time.Now().UTC()
	s, f, todos := newFailedBatchService(t, st)
	ctx := context.Background()
	if !s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{failedBatchSignal(todos["todo_a"], now)}, "", now, time.Time{}) {
		t.Fatal("first delivery failed")
	}
	for _, tc := range []struct {
		name   string
		todo   string
		to     string
		mutate func(*superviseSignal)
	}{
		{"different machine", "todo_b", "lead1", func(g *superviseSignal) { g.Machine = "other" }},
		{"different launcher", "todo_c", "lead1", func(g *superviseSignal) { g.Launcher = "other" }},
		{"different reason", "todo_d", "lead1", func(g *superviseSignal) { g.Detail = "another cause" }},
		{"unknown launcher fails open", "todo_e", "lead1", func(g *superviseSignal) { g.Launcher = "" }},
		{"different recipient", "todo_other", "lead2", func(g *superviseSignal) {}},
		{"new retry attempt", "todo_a", "lead1", func(g *superviseSignal) { g.Key = "retry 1: workspace was not created" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := failedBatchSignal(todos[tc.todo], now.Add(time.Minute))
			tc.mutate(&g)
			before := len(supervisionTo(f, tc.to, sigFailed))
			if !s.deliverDigest(ctx, s.settings(ctx), tc.to, []superviseSignal{g}, "", now.Add(time.Minute), now) {
				t.Fatal("an independent or immediate failure was deferred")
			}
			if got := len(supervisionTo(f, tc.to, sigFailed)); got != before+1 {
				t.Fatalf("rounds = %d, want %d", got, before+1)
			}
		})
	}
}

func TestFailedStartBatchesSameGatherListsAllTasks(t *testing.T) {
	s, f, todos := newFailedBatchService(t, &memStore{})
	now := time.Now().UTC()
	list := []superviseSignal{failedBatchSignal(todos["todo_a"], now), failedBatchSignal(todos["todo_b"], now)}
	if !s.deliverDigest(context.Background(), s.settings(context.Background()), "lead1", list, "", now, time.Time{}) {
		t.Fatal("same-gather delivery failed")
	}
	messages := supervisionTo(f, "lead1", sigFailed)
	if len(messages) != 1 || !strings.Contains(messages[0], "todo_a") || !strings.Contains(messages[0], "todo_b") {
		t.Fatalf("same-gather message = %q, want both affected tasks in one round", messages)
	}
}

func TestLeaderCanUseOnlyItsOwnStartRemedies(t *testing.T) {
	s, f, todo := newLeaderService(t)
	ctx := context.Background()
	_, plan, index, err := s.remedyOf(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.patchPlanTask(ctx, plan.ID, index, func(pt *PlanTask) {
		pt.Start = &StartRemedy{State: remedying, Until: time.Now().Add(time.Minute), Machine: "box", Pane: "w1:p2"}
	}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.nodes = map[string]nodeAnswer{
		"session.screen": func(_ string, _ json.RawMessage) (any, error) {
			return map[string]any{"text": "startup question\n", "status": proto.StatusIdle}, nil
		},
		"start.keys": func(_ string, _ json.RawMessage) (any, error) { return map[string]any{}, nil },
	}
	f.mu.Unlock()
	for _, caller := range []string{"lead1", "conv1"} {
		for _, tool := range []string{"task_start_screen", "task_start_keys"} {
			args, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "keys": []string{"n"}, "reason": "answer the startup question"})
			if _, err := s.runTool(ctx, "server", caller, tool, args); err != nil {
				t.Fatalf("%s could not use %s: %v", caller, tool, err)
			}
		}
	}
	for _, caller := range []string{"lead2", "side1", "background1"} {
		for _, tool := range []string{"task_start_screen", "task_start_keys"} {
			args, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "keys": []string{"n"}, "reason": "answer the startup question"})
			if _, err := s.runTool(ctx, "server", caller, tool, args); err == nil {
				t.Fatalf("%s used %s on another task", caller, tool)
			}
		}
	}
}

func TestLeaderCanRetryItsFailedStart(t *testing.T) {
	s, f, todo := newLeaderService(t)
	ctx := context.Background()
	if err := s.failTodo(ctx, todo.ID, "workspace was not created"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "reason": "the workspace is ready now"})
	if _, err := s.toolTaskRetry(ctx, "lead2", args); err == nil {
		t.Fatal("another leader retried the task")
	}
	f.mu.Lock()
	f.failStart = nil
	f.mu.Unlock()
	if out, err := s.toolTaskRetry(ctx, "lead1", args); err != nil {
		t.Fatalf("the current leader could not retry: %v (%s)", err, out)
	}
}

func TestMainTaskStartRemediesRejectAnotherLeader(t *testing.T) {
	s, f := newQuietService(t)
	todo, _ := remedyingTask(t, s, f, keptRefusal(proto.CodeShellWaiting))
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	for _, tool := range []string{"task_start_screen", "task_start_keys", "task_retry"} {
		args, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "keys": []string{"n"}, "reason": "try the startup remedy"})
		if _, err := s.runTool(context.Background(), "server", "lead1", tool, args); err == nil {
			t.Fatalf("an unrelated leader used %s on the main conversation's task", tool)
		}
	}
	// The main conversation's successful use of all three entries is exercised end to end by
	// TestRemedyToolsAnswerAndRetryInTheSameTab; this test pins the new leader boundary on main-owned tasks.
}

func TestFailedStartSettingsWordingKeepsWordBoundaries(t *testing.T) {
	s, _ := newQuietService(t)
	want := map[string]string{
		"assistant.leaders":                   "needs_user still comes to the main conversation",
		"assistant.main_supervises_own_tasks": "failed starts with no current leader and of what",
	}
	for _, setting := range s.ownSettings() {
		fragment, ok := want[setting.Key]
		if !ok {
			continue
		}
		if !strings.Contains(setting.About, fragment) {
			t.Errorf("%s lost a word boundary in %q", setting.Key, setting.About)
		}
		if strings.Contains(setting.About, "themain") || strings.Contains(setting.About, "ofwhat") {
			t.Errorf("%s joined adjacent words: %q", setting.Key, setting.About)
		}
		delete(want, setting.Key)
	}
	for key := range want {
		t.Errorf("setting %s was not found", key)
	}
}

func TestFailedStartBatchStoreFailureFailsOpen(t *testing.T) {
	st := &memStore{}
	st.failKey(failedStartBatchesKey, errors.New("synthetic batch store outage"))
	s, f, todos := newFailedBatchService(t, st)
	now := time.Now().UTC()
	ctx := context.Background()
	for _, id := range []string{"todo_a", "todo_b"} {
		g := failedBatchSignal(todos[id], now)
		if !s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{g}, "", now, time.Time{}) {
			t.Fatalf("%s was not delivered with the batch store unavailable", id)
		}
	}
	if got := len(supervisionTo(f, "lead1", sigFailed)); got != 2 {
		t.Fatalf("rounds with the batch store unavailable = %d, want fail-open 2", got)
	}
}

func readFailedBatchDoc(t *testing.T, st Store) failedStartBatchDoc {
	t.Helper()
	var doc failedStartBatchDoc
	if _, err := st.Setting(context.Background(), failedStartBatchesKey, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestFailedStartBatchesBoundaryListsAllDeferredTasks(t *testing.T) {
	st := &memStore{}
	s, f, todos := newFailedBatchService(t, st)
	s.superviseTimes.gather = time.Hour
	ctx := context.Background()
	now := time.Now().UTC()
	if !s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{failedBatchSignal(todos["todo_a"], now)}, "", now, time.Time{}) {
		t.Fatal("A did not deliver immediately")
	}
	for _, step := range []struct {
		id string
		at time.Time
	}{{"todo_b", now.Add(5 * time.Minute)}, {"todo_c", now.Add(12 * time.Minute)}} {
		if s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{failedBatchSignal(todos[step.id], step.at)}, "", step.at, now) {
			t.Fatalf("%s delivered during A's cooldown", step.id)
		}
	}
	doc := readFailedBatchDoc(t, st)
	if len(doc.Batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(doc.Batches))
	}
	for _, batch := range doc.Batches {
		if len(batch.Deferred) != 2 || batch.Deferred["todo_b"].Todo == "" || batch.Deferred["todo_c"].Todo == "" {
			t.Fatalf("deferred = %+v, want B and C", batch.Deferred)
		}
	}
	boundary := []superviseSignal{failedBatchSignal(todos["todo_b"], now.Add(5*time.Minute)), failedBatchSignal(todos["todo_c"], now.Add(12*time.Minute))}
	for i := range boundary {
		boundary[i].NotBefore = now.Add(superviseDedupWindow)
	}
	if s.deliverDigest(ctx, s.settings(ctx), "lead1", boundary, "", now.Add(29*time.Minute+59*time.Second), now) {
		t.Fatal("B/C delivered before the exact boundary")
	}
	messages := supervisionTo(f, "lead1", sigFailed)
	if len(messages) != 1 || !strings.Contains(messages[0], "todo_a") {
		t.Fatalf("messages before boundary = %q, want only A", messages)
	}
	if !s.deliverDigest(ctx, s.settings(ctx), "lead1", boundary, "", now.Add(superviseDedupWindow), now) {
		t.Fatal("B/C did not deliver at the boundary")
	}
	messages = supervisionTo(f, "lead1", sigFailed)
	if len(messages) != 2 {
		t.Fatalf("rounds = %d, want A plus one B/C round: %q", len(messages), messages)
	}
	for _, want := range []string{"todo_b", "second task", "todo_c", "third task"} {
		if !strings.Contains(messages[1], want) {
			t.Errorf("boundary message lacks %q:\n%s", want, messages[1])
		}
	}
	if strings.Contains(messages[1], "todo_a") {
		t.Fatalf("A was repeated in the supplementary round:\n%s", messages[1])
	}
}

type failedBatchCASStore struct {
	Store
	compareMu     sync.Mutex
	beforeCompare func()
}

func (st *failedBatchCASStore) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	if st.beforeCompare != nil {
		st.beforeCompare()
	}
	st.compareMu.Lock()
	defer st.compareMu.Unlock()
	var current failedStartBatchDoc
	if _, err := st.Store.Setting(ctx, key, &current); err != nil {
		return false, err
	}
	if current.Revision != revision {
		return false, nil
	}
	if err := st.Store.PutSetting(ctx, key, value); err != nil {
		return false, err
	}
	return true, nil
}

func TestFailedStartBatchesFirstMessageCASAcrossManagers(t *testing.T) {
	st := &failedBatchCASStore{Store: &memStore{}}
	first, firstHub, firstTodos := newFailedBatchService(t, st)
	second, secondHub, secondTodos := newFailedBatchService(t, st)
	ctx := context.Background()
	now := time.Now().UTC()
	release := make(chan struct{})
	var barrierMu sync.Mutex
	arrivals := 0
	st.beforeCompare = func() {
		barrierMu.Lock()
		arrivals++
		n := arrivals
		barrierMu.Unlock()
		if n == 2 {
			close(release)
		}
		if n <= 2 {
			<-release
		}
	}
	type result struct{ delivered bool }
	results := make(chan result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results <- result{first.deliverDigest(ctx, first.settings(ctx), "lead1", []superviseSignal{failedBatchSignal(firstTodos["todo_a"], now)}, "", now, time.Time{})}
	}()
	go func() {
		defer wg.Done()
		results <- result{second.deliverDigest(ctx, second.settings(ctx), "lead1", []superviseSignal{failedBatchSignal(secondTodos["todo_b"], now)}, "", now, time.Time{})}
	}()
	wg.Wait()
	close(results)
	delivered := 0
	for result := range results {
		if result.delivered {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("accepted first deliveries = %d, want exactly one CAS winner", delivered)
	}
	messages := append(supervisionTo(firstHub, "lead1", sigFailed), supervisionTo(secondHub, "lead1", sigFailed)...)
	if len(messages) != 1 {
		t.Fatalf("external first messages = %d, want 1: %q", len(messages), messages)
	}
	doc := readFailedBatchDoc(t, st)
	if len(doc.Batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(doc.Batches))
	}
	for _, batch := range doc.Batches {
		if len(batch.Notified) != 1 || len(batch.Deferred) != 1 {
			t.Fatalf("after competing first sends: notified=%v deferred=%v, want one each", batch.Notified, batch.Deferred)
		}
	}
}

type failedBatchRelayFailStore struct {
	Store
	mu   sync.Mutex
	fail bool
}

func (st *failedBatchRelayFailStore) PutSetting(ctx context.Context, key string, value any) error {
	st.mu.Lock()
	fail := st.fail && key == relayKey
	st.mu.Unlock()
	if fail {
		return errors.New("synthetic relay persistence failure")
	}
	return st.Store.PutSetting(ctx, key, value)
}

func (st *failedBatchRelayFailStore) setFail(fail bool) {
	st.mu.Lock()
	st.fail = fail
	st.mu.Unlock()
}

func TestFailedStartBatchesRelaySaveFailureKeepsDeferred(t *testing.T) {
	st := &failedBatchRelayFailStore{Store: &memStore{}}
	s, f, todos := newFailedBatchService(t, st)
	s.superviseTimes.gather = time.Hour
	ctx := context.Background()
	now := time.Now().UTC()
	if !s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{failedBatchSignal(todos["todo_a"], now)}, "", now, time.Time{}) {
		t.Fatal("first delivery failed")
	}
	deferred := failedBatchSignal(todos["todo_b"], now.Add(time.Minute))
	if s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{deferred}, "", now.Add(time.Minute), now) {
		t.Fatal("B did not defer")
	}
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusWorking},
		proto.Session{SessionID: "lead2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	st.setFail(true)
	deferred.NotBefore = now.Add(superviseDedupWindow)
	if s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{deferred}, "", now.Add(superviseDedupWindow), now) {
		t.Fatal("an unsaved relay was accepted")
	}
	doc := readFailedBatchDoc(t, st)
	for _, batch := range doc.Batches {
		if batch.Deferred["todo_b"].Todo != "todo_b" {
			t.Fatalf("relay save failure deleted deferred B: %+v", batch.Deferred)
		}
	}
	s.supervise.mu.Lock()
	pending := s.supervise.pending["todo_b|failed"]
	s.supervise.mu.Unlock()
	if pending.Todo != "todo_b" {
		t.Fatalf("B was not restored to the ordinary scheduler: %+v", pending)
	}
	if queuedFailedStart(s, "lead1") {
		t.Fatal("the failed relay write remained visible in the in-memory queue")
	}
	st.setFail(false)
	if !s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{deferred}, "", now.Add(superviseDedupWindow), now) {
		t.Fatal("B did not queue after relay persistence recovered")
	}
	doc = readFailedBatchDoc(t, st)
	for _, batch := range doc.Batches {
		if _, ok := batch.Deferred["todo_b"]; ok {
			t.Fatalf("a durably queued B stayed deferred: %+v", batch.Deferred)
		}
	}
	if !queuedFailedStart(s, "lead1") {
		t.Fatal("the recovered relay was not queued")
	}
}

func TestFailedStartBatchesGenerationCleanupAndRestore(t *testing.T) {
	st := &memStore{}
	s, _, todos := newFailedBatchService(t, st)
	s.superviseTimes.gather = time.Hour
	ctx := context.Background()
	now := time.Now().UTC()
	older := failedBatchSignal(todos["todo_a"], now)
	older.Recipient = "lead1"
	newer := older
	newer.Key = "retry 1: workspace was not created"
	newer.Machine = "other"
	newer.At, newer.Generation = now.Add(time.Minute), now.Add(time.Minute)
	oldKey, _ := s.failedStartBatchKey(ctx, "lead1", older)
	newKey, _ := s.failedStartBatchKey(ctx, "lead1", newer)
	seed := func() {
		if err := st.PutSetting(ctx, failedStartBatchesKey, failedStartBatchDoc{Revision: 1, Batches: map[string]*failedStartBatch{
			oldKey: {Recipient: "lead1", Deferred: map[string]superviseSignal{"todo_a": older}, Claims: map[string]string{"todo_a": "old-manager"}},
			newKey: {Recipient: "lead1", Deferred: map[string]superviseSignal{"todo_a": newer}, Claims: map[string]string{"todo_a": s.epoch}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	seed()
	if err := s.restoreFailedStartBatches(ctx); err != nil {
		t.Fatal(err)
	}
	s.supervise.mu.Lock()
	pending := s.supervise.pending["todo_a|failed"]
	s.supervise.mu.Unlock()
	if pending.Key != newer.Key || pending.Generation != newer.Generation {
		t.Fatalf("restored attempt = %+v, want the newer generation", pending)
	}
	doc := readFailedBatchDoc(t, st)
	if _, ok := doc.Batches[oldKey].Deferred["todo_a"]; ok {
		t.Fatal("restore left the older cross-batch generation behind")
	}

	seed()
	older.failedClaim = "old-manager"
	s.finishFailedStartBatches(ctx, "lead1", []superviseSignal{older}, nil, now.Add(2*time.Minute))
	doc = readFailedBatchDoc(t, st)
	if doc.Batches[newKey].Deferred["todo_a"].Key != newer.Key {
		t.Fatal("an old accepted attempt deleted the newer deferred attempt")
	}
	seed()
	s.finishFailedStartBatches(ctx, "lead1", nil, []superviseSignal{older}, now.Add(2*time.Minute))
	doc = readFailedBatchDoc(t, st)
	if doc.Batches[newKey].Deferred["todo_a"].Key != newer.Key {
		t.Fatal("an old settled attempt deleted the newer deferred attempt")
	}
	seed()
	newer.failedClaim = s.epoch
	s.finishFailedStartBatches(ctx, "lead1", []superviseSignal{newer}, nil, now.Add(2*time.Minute))
	doc = readFailedBatchDoc(t, st)
	for _, key := range []string{oldKey, newKey} {
		if _, ok := doc.Batches[key].Deferred["todo_a"]; ok {
			t.Errorf("the accepted newest generation left deferred work in batch %s", key)
		}
	}
}

func TestFailedStartBatchesLeaderRemovalReleasesAndCleansDeferred(t *testing.T) {
	st := &memStore{}
	s, f, todos := newFailedBatchService(t, st)
	s.superviseTimes.gather = time.Hour
	ctx := context.Background()
	now := time.Now().UTC()
	if !s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{failedBatchSignal(todos["todo_a"], now)}, "", now, time.Time{}) {
		t.Fatal("first delivery failed")
	}
	deferred := failedBatchSignal(todos["todo_b"], now.Add(time.Minute))
	if s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{deferred}, "", now.Add(time.Minute), now) {
		t.Fatal("B did not defer")
	}
	setSettings(t, s, func(v *Settings) { v.Leaders = nil })
	s.supervise.mu.Lock()
	pending := s.supervise.pending["todo_b|failed"]
	s.supervise.mu.Unlock()
	if pending.Todo != "todo_b" || !pending.NotBefore.IsZero() {
		t.Fatalf("removed leader's deferred signal was not released: %+v", pending)
	}
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{pending}, "", now.Add(2*time.Minute), now) {
		t.Fatal("the former leader's failure did not fall back to main")
	}
	doc := readFailedBatchDoc(t, st)
	for _, batch := range doc.Batches {
		if _, ok := batch.Deferred["todo_b"]; ok {
			t.Fatalf("the main fallback left B deferred with the removed leader: %+v", batch.Deferred)
		}
	}
	if got := len(supervisionTo(f, "lead1", sigFailed)); got != 1 {
		t.Fatalf("the removed leader got another message: %d", got)
	}
	if got := supervisionTo(f, "conv1", sigFailed); len(got) != 1 || !strings.Contains(got[0], "todo_b") {
		t.Fatalf("main fallback = %q, want B", got)
	}
}

func TestFailedStartBatchesMixedImmediateKeepsLastDelivery(t *testing.T) {
	st := &memStore{}
	s, f, todos := newFailedBatchService(t, st)
	s.superviseTimes.gather = time.Hour
	ctx := context.Background()
	now := time.Now().UTC()
	if !s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{failedBatchSignal(todos["todo_a"], now)}, "", now, time.Time{}) {
		t.Fatal("first delivery failed")
	}
	deferred := failedBatchSignal(todos["todo_b"], now.Add(time.Minute))
	immediate := failedBatchSignal(todos["todo_c"], now.Add(time.Minute))
	immediate.Machine = "other"
	prev := now
	at := now.Add(time.Minute)
	s.supervise.mu.Lock()
	s.supervise.last = at
	s.supervise.mu.Unlock()
	if !s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{deferred, immediate}, "", at, prev) {
		t.Fatal("the immediate member of a mixed round did not deliver")
	}
	s.supervise.mu.Lock()
	last := s.supervise.last
	s.supervise.mu.Unlock()
	if !last.Equal(at) {
		t.Fatalf("mixed deferred+immediate rewound last to %v, want actual delivery at %v", last, at)
	}
	messages := supervisionTo(f, "lead1", sigFailed)
	if len(messages) != 2 || !strings.Contains(messages[1], "todo_c") || strings.Contains(messages[1], "todo_b") {
		t.Fatalf("mixed round = %q, want only immediate C after A", messages)
	}
}

func TestFailedStartBatchRecoveryAtServiceStartup(t *testing.T) {
	st := &memStore{}
	s, f, todos := newFailedBatchService(t, st)
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC()
	deferred := failedBatchSignal(todos["todo_b"], now.Add(-superviseDedupWindow))
	deferred.Recipient = "lead1"
	deferred.NotBefore = now.Add(-time.Second)
	key, eligible := s.failedStartBatchKey(ctx, "lead1", deferred)
	if !eligible {
		t.Fatal("the startup fixture is not batch-eligible")
	}
	if err := st.PutSetting(ctx, failedStartBatchesKey, failedStartBatchDoc{Revision: 1, Batches: map[string]*failedStartBatch{
		key: {Recipient: "lead1", Machine: deferred.Machine, Launcher: deferred.Launcher, Reason: deferred.Detail,
			NotifiedAt: now.Add(-superviseDedupWindow), Due: now.Add(-time.Second), Deferred: map[string]superviseSignal{"todo_b": deferred}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.failTodo(ctx, "todo_b", deferred.Detail); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	waitFor(t, "the startup-restored failed start", func() bool {
		messages := supervisionTo(f, "lead1", sigFailed)
		return len(messages) == 1 && strings.Contains(messages[0], "todo_b")
	})
	if got := supervisionTo(f, "conv1", sigFailed); len(got) != 0 {
		t.Fatalf("startup recovery leaked the leader failure to main: %q", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the service did not stop after startup recovery")
	}
}

func TestRemediableFailedStartBatchesWithoutDelayingRetry(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, first := newLeaderServiceOn(t, st)
	ctx := context.Background()
	reason := clip(oneLine(s.startFailureText(ctx, "box", keptRefusal(proto.CodeShellWaiting))), proto.CodedStartErrorMax)
	if err := s.failTodo(ctx, first.ID, reason); err != nil {
		t.Fatal(err)
	}
	s.taskStartFailed(ctx, todoNow(s, first.ID), "box", nil, reason)
	waitFor(t, "the first final failure", func() bool { return len(supervisionTo(f, "lead1", sigFailed)) == 1 })
	second, _ := remedyingTaskAs(t, s, f, keptRefusal(proto.CodeShellWaiting), "lead1", "plan_leader_remediable")
	var deferred superviseSignal
	waitFor(t, "the remediable notification to join the same deferred batch", func() bool {
		doc := readFailedBatchDoc(t, st)
		if len(doc.Batches) != 1 {
			return false
		}
		for _, batch := range doc.Batches {
			if g, ok := batch.Deferred[second.ID]; ok {
				deferred = g
				return true
			}
		}
		return false
	})
	if !deferred.Remedying {
		t.Fatalf("the kept-tab fixture is not remediable: %+v", deferred)
	}
	if got := len(supervisionTo(f, "lead1", sigFailed)); got != 1 {
		t.Fatalf("the remediable notification bypassed its batch: %d rounds", got)
	}
	f.mu.Lock()
	f.failStart = nil
	f.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"todo_id": second.ID, "reason": "the startup question is answered"})
	res, err := s.runTool(ctx, "server", "lead1", "task_retry", raw)
	if err != nil || !strings.Contains(res, "starting again") {
		t.Fatalf("the notification window delayed the leader's retry: %q, %v", res, err)
	}
	waitFor(t, "the retry to start before the notification boundary", func() bool { return len(todoNow(s, second.ID).Agents) == 1 })
	if got := len(supervisionTo(f, "lead1", sigFailed)); got != 1 {
		t.Fatalf("the successful retry released an old failure early: %d rounds", got)
	}
	if s.deliverDigest(ctx, s.settings(ctx), "lead1", []superviseSignal{deferred}, "", time.Now().Add(superviseDedupWindow), time.Now()) {
		t.Fatal("the old remediable notification survived its successful retry")
	}
	doc := readFailedBatchDoc(t, st)
	for _, batch := range doc.Batches {
		if _, ok := batch.Deferred[second.ID]; ok {
			t.Fatalf("the successful retry left its old remediable notification deferred: %+v", batch.Deferred)
		}
	}
	if got := len(supervisionTo(f, "lead1", sigFailed)); got != 1 {
		t.Fatalf("the moot remediable notification was sent at the boundary: %d rounds", got)
	}
}

func TestFinalRetryTransitionIsNotRemedying(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, _ := newLeaderServiceOn(t, st)
	todo, _ := remedyingTaskAs(t, s, f, keptRefusal(proto.CodeShellWaiting), "lead1", "plan_leader_final_retry")
	waitFor(t, "the initial remediable notification", func() bool { return len(supervisionTo(f, "lead1", sigFailed)) == 1 })
	s.superviseTimes.gather = time.Hour
	f.mu.Lock()
	f.failStart = errors.New("the kept tab closed before the agent started")
	f.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "reason": "try the kept tab once more"})
	if _, err := s.runTool(context.Background(), "server", "lead1", "task_retry", raw); err != nil {
		t.Fatal(err)
	}
	var pending superviseSignal
	waitFor(t, "the final retry failure signal", func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		pending = s.supervise.pending[todo.ID+"|failed"]
		return strings.HasPrefix(pending.Key, "retry 1:")
	})
	current := todoNow(s, todo.ID)
	if current.StartError == "" || current.DoneAt == nil {
		t.Fatalf("the real retry transition did not become final: %+v", current)
	}
	if pending.Remedying {
		t.Fatalf("StartError is final but the signal still says remedying: %+v", pending)
	}
	if pending.Generation.IsZero() {
		t.Fatalf("the final retry has no delivery generation: %+v", pending)
	}
	pending.Recipient = s.digestTarget(context.Background(), s.settings(context.Background()), pending)
	if pending.Recipient != "lead1" {
		t.Fatalf("the final retry routed to %q, want its leader", pending.Recipient)
	}
	if _, eligible := s.failedStartBatchKey(context.Background(), "lead1", pending); !eligible {
		t.Fatalf("the final retry is not eligible for failure grouping: %+v", pending)
	}
}
