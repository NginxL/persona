package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Task delivery tracking (#5732), through the real hub HTTP stack and assistant bus: a side chat dispatches to main,
// judged work waits for acceptance, owner-only work pushes once, and cancellation / takeover stay visible in both
// directions. The only computer is a fake node; the hub and assistant share the test's isolated Postgres.
func TestTaskDeliveryTrackingOverTheBus(t *testing.T) {
	h, srv, _ := newBusHub(t)
	store := managerDB(t, h)
	ctx := context.Background()
	keys := []string{"assistant", "assistant.tasks", "assistant.routines", "assistant.followups", "assistant.runs",
		"assistant.titles", "assistant.archived", "assistant.relay"}
	clear := func() {
		for _, k := range keys {
			_ = store.PutSetting(context.Background(), k, nil)
		}
		if list, err := store.ListTodos(context.Background()); err == nil {
			for _, r := range list {
				_ = store.DeleteTodo(context.Background(), r.ID)
			}
		}
	}
	clear()
	t.Cleanup(clear)
	old := taskSettleDelay
	taskSettleDelay = func(NotifyConfig) time.Duration { return 0 }
	t.Cleanup(func() { taskSettleDelay = old })
	t.Cleanup(assistant.SetSuperviseTimings(50*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond))

	var verdict atomic.Value
	verdict.Store(`{"verdict":"done","reason":"实现和验证均已交付"}`)
	judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": verdict.Load().(string)}}})
	}))
	t.Cleanup(judge.Close)
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	setManagerJSON(t, h, "inbox", map[string]any{"triage_ai": ai.Config{BaseURL: judge.URL, APIKeyFile: key, Model: "m"}})

	n := newAssistantNode(t, h)
	n.m.info.Home = "/home/me"
	now := time.Now().UTC()
	editSessions(n.m, func([]proto.Session) []proto.Session {
		return []proto.Session{{SessionID: "main-1", Cwd: convDir, Title: "Persona", UpdatedAt: now},
			{SessionID: "side-2", Cwd: convDir, Title: "Side chat", UpdatedAt: now.Add(-time.Minute)}}
	})
	n.known["main-1"], n.known["side-2"] = true, true
	taskNode := connectTaskNode(t, h, "box")
	taskNode.nextID = "agent-old-1"
	// Leave the legacy switch off: new side-chat work must still be supervised by main, never by a new background stream.
	if err := store.PutSetting(ctx, "assistant", assistant.Settings{MachineID: "server", Main: "main-1", Timezone: "Asia/Shanghai"}); err != nil {
		t.Fatal(err)
	}
	app := dialTestApp(t, h, srv)
	dialAssistant(t, h, srv.URL, true)

	mcp := func(conversation, name string, args any) string {
		t.Helper()
		raw, _ := json.Marshal(args)
		params, _ := json.Marshal(map[string]any{"name": name, "args": json.RawMessage(raw), "session_id": conversation, "assistant": true})
		res, err := nodeAsks(ctx, n.m, "mcp.call", params)
		if err != nil {
			t.Fatal(err)
		}
		r := res.(proto.ToolResult)
		if r.IsError {
			t.Fatalf("%s: %s", name, textOf(r))
		}
		return textOf(r)
	}
	digests := func(conversation string) []string {
		n.mu.Lock()
		defer n.mu.Unlock()
		var out []string
		for _, s := range n.sends {
			if s["session_id"] != conversation {
				continue
			}
			text, _ := s["text"].(string)
			in := prompts.ParseInjected(text)
			source := prompts.ItemSource(proto.Item{Role: "user", Kind: proto.KindText, Text: text})
			if in != nil && in.State == "supervise" && source != nil && source.Kind == proto.SourceSupervision {
				out = append(out, text)
			}
		}
		return out
	}
	plan := func(title string, continues string) string {
		task := map[string]any{"title": title, "prompt": title, "machine": "box", "cwd": "/code/app", "readonly": false,
			"permission": "bypassPermissions", "user_requested": true, "why": "synthetic dispatch", "role": "worker",
			"done_when": []string{"实现并验证"}, "expect_minutes": 10}
		if continues != "" {
			task["continues"] = continues
			task["continues_why"] = "按调整后的方案接手"
		}
		return mcp("side-2", "task_plan", map[string]any{"tasks": []map[string]any{task}})
	}
	agent := func(todo Todo, status, text string) {
		var current assistant.TodoAgent
		for _, a := range todo.Agents {
			if a.ReleasedAt == nil {
				current = a
				break
			}
		}
		if current.SessionID == "" {
			t.Fatalf("no current agent on %+v", todo)
		}
		state := proto.StateUnread
		if status == proto.StatusWorking {
			state = proto.StateWorking
		}
		h.byID["box"].setSessions([]proto.Session{{SessionID: current.SessionID, Cwd: "/code/app", Agent: "claude", Status: status, State: state,
			LastText: text, UpdatedAt: time.Now()}})
	}
	drainApp := func() {
		for {
			select {
			case <-app.msgs:
			default:
				return
			}
		}
	}

	if res := plan("子对话交付验证", ""); !strings.Contains(res, "main conversation") {
		t.Fatalf("side dispatch does not name main: %s", res)
	}
	var first Todo
	eventually(t, 10*time.Second, "the side-chat task and its agent", func() bool {
		for _, todo := range taskTodos(h) {
			if todo.Title == "子对话交付验证" && len(todo.Agents) == 1 {
				first = todo
				return true
			}
		}
		return false
	})
	if first.Origin != "side-2" || first.Supervision == nil || first.Supervision.Supervisor != "main-1" {
		t.Fatalf("origin / supervisor = %+v", first)
	}
	snapshot := func() string {
		if a, ok := testAssistants.Load(h); ok {
			a.(*testAssistant).svc.ProfileChanged(context.Background())
		}
		res, err := h.asst().Context(context.Background(), assistant.ContextParams{SessionID: "main-1"})
		if err != nil {
			t.Fatal(err)
		}
		return res.Text
	}

	agent(first, proto.StatusWorking, "")
	agent(first, proto.StatusDone, "实现和验证均已完成")
	eventually(t, 10*time.Second, "main's delivery digest", func() bool { return len(digests("main-1")) == 1 })
	if got := len(digests("side-2")); got != 0 {
		t.Fatalf("side chat received %d supervision digests", got)
	}
	first = todoNow(h, first.ID)
	if first.DoneAt != nil || first.Supervision == nil || first.Supervision.State != "pending_review" {
		t.Fatalf("judged delivery closed or misplaced the task: %+v", first)
	}
	if d := digests("main-1")[0]; !strings.Contains(d, "待逐项复核，不代表完成") || !strings.Contains(d, "实现和验证均已交付") {
		t.Fatalf("delivery digest:\n%s", d)
	}
	if text := snapshot(); !strings.Contains(text, "已交付·待看") {
		t.Fatalf("snapshot delivery:\n%s", text)
	}
	t.Logf("delivery: todo=%s done_at=%v state=%s main_digests=%d side_digests=%d snapshot_awaiting_review=true", first.ID, first.DoneAt,
		first.Supervision.State, len(digests("main-1")), len(digests("side-2")))

	// A new stretch its agent says only the owner can move goes to main, the supervisor, not to the owner: no push,
	// no needs_user state (decision rights), and the wait holds so the work cannot be accepted meanwhile.
	drainApp()
	verdict.Store(`{"verdict":"needs_user","reason":"需要你本人操作确认发布"}`)
	agent(first, proto.StatusWorking, "")
	agent(first, proto.StatusDone, "下一步由你发起发布")
	eventually(t, 10*time.Second, "the owner wait the supervisor holds", func() bool {
		cur := todoNow(h, first.ID)
		return cur.Supervision != nil && cur.Supervision.Waits != nil && cur.Supervision.State != "needs_user" && cur.DoneAt == nil
	})
	deadline := time.After(500 * time.Millisecond)
	for pushed := false; ; {
		select {
		case m := <-app.msgs:
			if m["type"] == "notify" && strings.Contains(m["raw"].(string), "需要你本人操作确认发布") {
				pushed = true
			}
		case <-deadline:
			if pushed {
				t.Fatal("an agent's owner wait was pushed to the user")
			}
			goto pushChecked
		}
	}

pushChecked:
	if text := snapshot(); !strings.Contains(text, "先判断是不是用户的事") || !strings.Contains(text, first.ID) {
		t.Fatalf("snapshot owner wait:\n%s", text)
	}
	t.Logf("owner wait: todo=%s done_at=%v waits=present state=%s push=0", first.ID, todoNow(h, first.ID).DoneAt, todoNow(h, first.ID).Supervision.State)
	// Cancellation is its own outcome in the API and in main's live snapshot.
	mcp("main-1", "task_review", map[string]any{"todo_id": first.ID, "verdict": "cancelled", "note": "用户调整方案，不再按原计划发布"})
	eventually(t, 10*time.Second, "the cancellation projection", func() bool {
		cur := todoNow(h, first.ID)
		// The API keeps done_by=review for old clients; cancelled_at / cancelled_reason carry the distinct outcome.
		return cur.DoneAt != nil && cur.DoneBy == "review" && cur.CancelledAt != nil && cur.CancelledReason == "用户调整方案，不再按原计划发布"
	})
	stored, err := store.ListTodos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storedCancelled := false
	for _, row := range stored {
		if row.ID == first.ID && row.DoneBy == "cancelled" && row.DoneAt != nil {
			storedCancelled = true
		}
	}
	if !storedCancelled {
		t.Fatalf("stored cancellation = %+v", stored)
	}
	if text := snapshot(); !strings.Contains(text, "已取消") || !strings.Contains(text, "用户调整方案，不再按原计划发布") {
		t.Fatalf("snapshot cancellation:\n%s", text)
	}
	t.Logf("cancel: todo=%s api_done_by=review stored_done_by=cancelled reason_and_snapshot=present", first.ID)

	// task_plan.continues keeps the cancelled work on its original to-do (#6820), releases the predecessor and links
	// exactly one replacement Agent. It neither creates a successor nor renames the preserved brief's card.
	taskNode.nextID = "agent-new-1"
	plan("按新方案接手验证", first.ID)
	activeAgents := func(todo Todo) (out []assistant.TodoAgent) {
		for _, a := range todo.Agents {
			if a.ReleasedAt == nil {
				out = append(out, a)
			}
		}
		return out
	}
	var continued Todo
	eventually(t, 10*time.Second, "the same-card continuation and its replacement agent", func() bool {
		cur := todoNow(h, first.ID)
		active := activeAgents(cur)
		if cur.DoneAt == nil && len(active) == 1 && active[0].SessionID == "agent-new-1" {
			continued = cur
			return true
		}
		return false
	})
	if continued.ID != first.ID || continued.Title != first.Title || continued.Origin != "side-2" ||
		continued.Supervision == nil || continued.Supervision.Supervisor != "main-1" || len(continued.Agents) != 2 {
		t.Fatalf("same-card continuation = %+v", continued)
	}
	predecessorReleased := false
	for _, a := range continued.Agents {
		if a.SessionID == first.Agents[0].SessionID && a.ReleasedAt != nil {
			predecessorReleased = true
		}
	}
	if !predecessorReleased {
		t.Fatalf("continuation predecessor = %+v", continued.Agents)
	}
	if got := len(taskTodos(h)); got != 1 {
		t.Fatalf("continuation created a successor: %d todos", got)
	}
	t.Logf("continuation: todo=%s same_card=true predecessor_released=true active_agents=1 successor_todos=0", continued.ID)
	sessionID := activeAgents(continued)[0].SessionID

	// Adopting the closed card's replacement Agent is the explicit card-replacement path. It creates one linked
	// history: the old card points forward and the new card points back.
	mcp("main-1", "task_review", map[string]any{"todo_id": first.ID, "verdict": "cancelled", "note": "改为独立新卡接手"})
	eventually(t, 10*time.Second, "the second cancellation before card replacement", func() bool {
		return todoNow(h, first.ID).DoneAt != nil
	})
	h.byID["box"].setSessions([]proto.Session{{SessionID: sessionID, Cwd: "/code/app", Agent: "claude", Title: "按新方案接手验证",
		Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now()}})
	mcp("main-1", "agent_supervise", map[string]any{"machine": "box", "session_id": sessionID,
		"done_when": []string{"实现并验证"}, "expect_minutes": 10})
	var second Todo
	eventually(t, 10*time.Second, "the takeover and its agent", func() bool {
		for _, todo := range taskTodos(h) {
			if todo.Title == "按新方案接手验证" && len(todo.Agents) == 1 {
				second = todo
				return true
			}
		}
		return false
	})
	if second.ID == first.ID || second.Origin != "main-1" || second.Supervision == nil || second.Supervision.Supervisor != "main-1" {
		t.Fatalf("takeover = %+v", second)
	}
	oldLineage, newLineage := todoNow(h, first.ID).Lineage, todoNow(h, second.ID).Lineage
	oldLinked := false
	for _, step := range oldLineage {
		if step.TodoID == first.ID && step.SupersededBy == second.ID {
			oldLinked = true
		}
	}
	newLinked := false
	for _, step := range newLineage {
		if step.TodoID == second.ID && step.Continues == first.ID && step.Current {
			newLinked = true
		}
	}
	if !oldLinked || !newLinked {
		t.Fatalf("lineage old=%+v new=%+v", oldLineage, newLineage)
	}
	text := snapshot()
	if !strings.Contains(text, "已由新卡 "+second.ID+" 接手") || !strings.Contains(text, "接手自 "+first.ID) {
		t.Fatalf("snapshot lineage:\n%s", text)
	}
	t.Logf("takeover: old=%s new=%s old_superseded_by=%s new_continues=%s snapshot_bidirectional=true", first.ID, second.ID, second.ID, first.ID)
}
