package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

func TestLiveContextSnapshotIncludesOpenTodoRoster(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	ids := []string{"todo_personal_live", "todo_parked_live", "todo_old_live"}
	for _, id := range ids {
		if _, err := s.newTodoAs(ctx, id, "live fixture "+id, "", "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	s.todos.mu.Lock()
	s.todos.todos["todo_old_live"].UpdatedAt = time.Now().Add(-30 * 24 * time.Hour)
	s.todos.todos["todo_parked_live"].Supervision = &Supervision{State: reviewParked, UpdatedAt: time.Now(), Supervisor: "conversation"}
	s.todos.mu.Unlock()

	done := make(chan struct{})
	go s.takeSnapshot(done)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("takeSnapshot exceeded 10 seconds")
	}
	got := s.contextSnapshot(ctx, "", false)
	if !strings.Contains(got, "开放待办：共 3 件（完整列表用 todo_list）") {
		t.Fatalf("live context snapshot lacks open count and retrieval hint:\n%s", got)
	}
	for _, id := range ids {
		if !strings.Contains(got, id+"「live fixture "+id+"」") {
			t.Errorf("live context snapshot lacks %s:\n%s", id, got)
		}
	}
}

func TestSnapshotOpenTodoRosterIncludesOldAdoptedPersonalAndParked(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	now := time.Now().UTC()
	ids := []string{"todo_legacy", "todo_adopted", "todo_personal", "todo_parked"}
	for i, id := range ids {
		origin := "conversation"
		if id == "todo_personal" {
			origin = ""
		}
		if _, err := s.newTodoAs(ctx, id, "fixture "+id, "", origin, "", false); err != nil {
			t.Fatal(err)
		}
		s.todos.mu.Lock()
		row := s.todos.todos[id]
		row.CreatedAt = now.Add(-time.Duration(i+1) * 24 * time.Hour)
		row.UpdatedAt = now.Add(-30 * 24 * time.Hour)
		if id == "todo_parked" {
			row.Supervision = &Supervision{State: reviewParked, UpdatedAt: now, Supervisor: "conversation"}
		}
		s.todos.mu.Unlock()
	}
	for i := 0; i < 35; i++ {
		id := fmt.Sprintf("todo_recent_%02d", i)
		if _, err := s.newTodoAs(ctx, id, "recent fixture", "", "conversation", "", false); err != nil {
			t.Fatal(err)
		}
	}
	rows, total := s.snapshotOpenTodos(ctx, "")
	if total != 39 {
		t.Fatalf("open count = %d, want 39", total)
	}
	text := fitSnapshot(prompts.Snapshot{Now: now, Zone: "UTC", OpenTodos: rows, OpenTodosTotal: total, OpenTodosLoaded: true})
	for _, id := range ids {
		if !strings.Contains(text, id+"「fixture "+id+"」") {
			t.Errorf("rendered open-work roster lacks %s:\n%s", id, text)
		}
	}
	if !strings.Contains(text, "开放待办：共 39 件（完整列表用 todo_list）") {
		t.Errorf("open count missing:\n%s", text)
	}
}

func TestSnapshotTaskDetailsKeepOldAndOriginlessOpenTodos(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	ids := []string{"todo_legacy", "todo_adopted", "todo_old", "todo_personal"}
	for _, id := range ids {
		origin := "conversation"
		if id != "todo_old" {
			origin = ""
		}
		if _, err := s.newTodoAs(ctx, id, "fixture "+id, "", origin, "", false); err != nil {
			t.Fatal(err)
		}
		if id == "todo_old" {
			s.todos.mu.Lock()
			s.todos.todos[id].UpdatedAt = time.Now().Add(-30 * 24 * time.Hour)
			s.todos.mu.Unlock()
		}
	}
	got := s.snapshotTasks(ctx, "")
	seen := map[string]bool{}
	for _, task := range got {
		seen[task.ID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("open todo %s is absent from the full-detail selection", id)
		}
	}
}

func TestSnapshotOpenTodoRosterCapsHugeListsWithTodoListHint(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	for i := 0; i < snapshotOpenTodoLimit+7; i++ {
		id := fmt.Sprintf("todo_large_%03d", i)
		if _, err := s.newTodoAs(ctx, id, "large fixture", "", "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	rows, total := s.snapshotOpenTodos(ctx, "")
	snap := scopeSnapshot(prompts.Snapshot{Now: time.Now(), Zone: "UTC", OpenTodos: rows, OpenTodosTotal: total, OpenTodosLoaded: true}, "")
	text := fitSnapshot(snap)
	if rows = snap.OpenTodos; total != snapshotOpenTodoLimit+7 || len(rows) != snapshotOpenTodoLimit {
		t.Fatalf("rows/count = %d/%d", len(rows), total)
	}
	if !strings.Contains(text, "开放待办：共 107 件（完整列表用 todo_list）") || !strings.Contains(text, "其余用 todo_list 查") {
		t.Fatalf("large roster lacks total or todo_list entrance:\n%s", text)
	}
	if runes(text) > snapshotBudget {
		t.Fatalf("snapshot exceeds budget: %d", runes(text))
	}
}

func TestSnapshotTaskDetailsPrioritizeOpenTodosAndKeepTwelveDetailLimit(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 14; i++ {
		id := fmt.Sprintf("todo_open_%02d", i)
		if _, err := s.newTodoAs(ctx, id, "open fixture", "", "", "", false); err != nil {
			t.Fatal(err)
		}
		s.todos.mu.Lock()
		s.todos.todos[id].CreatedAt = now.Add(-time.Duration(i) * time.Hour)
		s.todos.todos[id].UpdatedAt = now.Add(-30 * 24 * time.Hour)
		s.todos.mu.Unlock()
	}
	closed, err := s.newTodoAs(ctx, "todo_closed", "closed fixture", "", "conversation", "", false)
	if err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	closedAt := now.Add(-time.Hour)
	s.todos.todos[closed.ID].DoneAt = &closedAt
	s.todos.todos[closed.ID].UpdatedAt = now
	s.todos.mu.Unlock()

	got := scopeSnapshot(prompts.Snapshot{Tasks: s.snapshotTasks(ctx, "")}, "").Tasks
	if len(got) != snapshotTaskLimit {
		t.Fatalf("task details = %d, want %d", len(got), snapshotTaskLimit)
	}
	for _, task := range got {
		if !strings.HasPrefix(task.ID, "todo_open_") {
			t.Errorf("non-open task outranked open tasks: %s", task.ID)
		}
	}
}

func TestFitSnapshotKeepsOpenTodoRosterWhenTaskDetailsAreOmitted(t *testing.T) {
	rows := make([]prompts.SnapshotOpenTodo, 26)
	for i := range rows {
		rows[i] = prompts.SnapshotOpenTodo{ID: fmt.Sprintf("todo_roster_%02d", i), Title: "compact title"}
	}
	s := bigSnapshot(fitNow)
	s.OpenTodos, s.OpenTodosTotal, s.OpenTodosLoaded = rows, len(rows), true
	got := fitSnapshot(s)
	if !strings.Contains(got, "开放待办：共 26 件（完整列表用 todo_list）") {
		t.Fatalf("open count disappeared:\n%s", got)
	}
	for _, row := range rows {
		if !strings.Contains(got, row.ID) {
			t.Errorf("open ID %s was lost when details were omitted", row.ID)
		}
	}
	if !strings.Contains(got, "task_plan") || !strings.Contains(got, "todo_roster_25") {
		t.Fatalf("task retrieval hint or roster was lost:\n%s", got)
	}
	if !strings.Contains(got, "12 个") {
		t.Fatalf("full task details were not replaced by their count:\n%s", got)
	}
	if runes(got) > snapshotBudget {
		t.Fatalf("snapshot exceeds budget: %d", runes(got))
	}
}

func TestFitSnapshotCompactsOpenRosterBeforeOmittingTaskDetails(t *testing.T) {
	rows := make([]prompts.SnapshotOpenTodo, snapshotOpenTodoLimit)
	for i := range rows {
		rows[i] = prompts.SnapshotOpenTodo{ID: fmt.Sprintf("todo_compact_%03d", i), Title: strings.Repeat("long title ", 4), Link: "https://hub.example/todos/a-long-link"}
	}
	s := bigSnapshot(fitNow)
	s.OpenTodos, s.OpenTodosTotal, s.OpenTodosLoaded = rows, snapshotOpenTodoLimit, true
	got := fitSnapshot(s)
	if !strings.Contains(got, "仅列 ID") || !strings.Contains(got, "todo_compact_099") {
		t.Fatalf("compact roster did not preserve its tail ID:\n%s", got)
	}
	if !strings.Contains(got, "12 个") {
		t.Fatalf("task detail section was not omitted after roster compaction:\n%s", got)
	}
	if runes(got) > snapshotBudget {
		t.Fatalf("snapshot exceeds budget: %d", runes(got))
	}
}

// A question to-do stays out of the roster and the task list only while the snapshot's own question line shows it.
// Once its ask record is pruned (askTodoKeep, askTodoMax) it is an ordinary to-do: listed and counted like any other,
// not lost from both.
func TestSnapshotListsAQuestionTodoOnceItsAskRecordIsPruned(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	live, pruned := askTodoPrefix+"live", askTodoPrefix+"pruned"
	for _, id := range []string{live, pruned} {
		if _, err := s.newTodoAs(ctx, id, "question "+id, "", "conversation", "", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.keepAskTodo(ctx, askTodo{TodoID: live, Questions: []string{"Which one?"}, AskedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Asked longer ago than askTodoKeep: saving the records drops it at once, as pruning would.
	if err := s.keepAskTodo(ctx, askTodo{TodoID: pruned, Questions: []string{"Old one?"}, AskedAt: time.Now().Add(-askTodoKeep - time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.askTodoOf(ctx, pruned); ok {
		t.Fatal("fixture: the old record was not pruned")
	}
	roster, total := s.snapshotOpenTodos(ctx, "")
	ids := map[string]bool{}
	for _, r := range roster {
		ids[r.ID] = true
	}
	if !ids[pruned] || ids[live] || total != 1 {
		t.Fatalf("roster = %+v (total %d), want the pruned question listed and the live one left to its own line", roster, total)
	}
	tasks := map[string]bool{}
	for _, task := range s.snapshotTasks(ctx, "") {
		tasks[task.ID] = true
	}
	if !tasks[pruned] || tasks[live] {
		t.Fatalf("snapshot tasks = %v, want the pruned question listed and the live one not", tasks)
	}
	asked := s.snapshotAsked(ctx)
	if len(asked) != 1 || asked[0].TodoID != live {
		t.Fatalf("question line = %+v", asked)
	}
}

// A failed start is stored done with its start error, but the app's To-dos count it open while it waits on the owner
// (triageCountsAsOpen): the snapshot's open count and roster do the same and mark it, so one failing to start no
// longer takes one off the count and puts it back when it is taken up again (#10365). One older than the triage keeps
// it waiting, one whose row's work finished in a related to-do (superseded), and a cancelled one stay out.
func TestSnapshotCountsAFailedStartOpenAsTheAppDoes(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"todo_open", "todo_failed", "todo_failed_old", "todo_failed_superseded", "todo_superseder", "todo_failed_cancelled"} {
		title := "fixture " + id
		if id == "todo_failed_superseded" || id == "todo_superseder" {
			title = "Release the tablet layout"
		}
		if _, err := s.newTodoAs(ctx, id, title, "", "conversation", "", false); err != nil {
			t.Fatal(err)
		}
	}
	s.todos.mu.Lock()
	fail := func(id string, at time.Time) {
		row := s.todos.todos[id]
		row.DoneAt, row.StartError, row.UpdatedAt, row.CreatedAt = &at, "没开起来", at, at
	}
	fail("todo_failed", now.Add(-time.Hour))
	fail("todo_failed_old", now.Add(-72*time.Hour))
	fail("todo_failed_superseded", now.Add(-time.Hour))
	fail("todo_failed_cancelled", now.Add(-time.Hour))
	s.todos.todos["todo_failed_cancelled"].CancelledAt = &now
	finished := now.Add(-time.Minute)
	s.todos.todos["todo_superseder"].DoneAt = &finished
	s.todos.mu.Unlock()

	rows, total := s.snapshotOpenTodos(ctx, "")
	if total != 2 || len(rows) != 2 {
		t.Fatalf("open count = %d (%v), want todo_open and todo_failed", total, rows)
	}
	text := fitSnapshot(prompts.Snapshot{Now: now, Zone: "UTC", OpenTodos: rows, OpenTodosTotal: total, OpenTodosLoaded: true})
	if !strings.Contains(text, "开放待办：共 2 件") || !strings.Contains(text, "todo_failed「fixture todo_failed」（没开起来）") ||
		!strings.Contains(text, "todo_open「fixture todo_open」") || strings.Contains(text, "todo_open「fixture todo_open」（没开起来）") {
		t.Fatalf("roster:\n%s", text)
	}
}
