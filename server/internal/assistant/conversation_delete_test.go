package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestDeletePersonaConversation(t *testing.T) {
	f := healthHub()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	id := "work-0000-0000"
	path := "/api/v1/assistant/conversations/" + id
	for i := 0; i < 2; i++ {
		w := serve(s, "DELETE", path, `{"machine_id":"server"}`)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"deleted":true`) {
			t.Fatalf("delete %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	for _, archived := range []bool{false, true} {
		for _, c := range s.view(context.Background(), archived).Conversations {
			if c.SessionID == id {
				t.Fatal("deleted conversation remains in list")
			}
		}
	}
	if w := serve(s, "POST", path+"/unarchive", "{}"); w.Code != 410 {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	if w := serve(s, "GET", path+"/feed", ""); w.Code != 410 {
		t.Fatalf("feed: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.toolConversationRead(context.Background(), "main-0000-0000", []byte(`{"id":"work-0000-0000"}`)); convCode(err) != "conversation_deleted" {
		t.Fatalf("read: %v", err)
	}
	// The source transcript and every unrelated session are untouched.
	if len(f.sessions["server"]) != 4 {
		t.Fatal("source sessions changed")
	}
	s2 := newServiceOn(t, f, s.store())
	if w := serve(s2, "POST", path+"/unarchive", "{}"); w.Code != 410 {
		t.Fatalf("restart restored deleted chat: %d", w.Code)
	}
}

func TestDeletePersonaFormerMainAndStoredReferences(t *testing.T) {
	f := healthHub()
	f.sessions["server"] = append(f.sessions["server"], proto.Session{MachineID: "server", SessionID: "delegated-agent", Status: proto.StatusWorking, Cwd: "/code/example", Agent: "claude"})
	s := newTestService(t, f)
	ctx := context.Background()
	id := "work-0000-0000"
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", id })
	todo, err := s.newTodo(ctx, "Synthetic task", "", id, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "server", "delegated-agent", "Synthetic task", "/code/example", "claude"); err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, id, []string{"Synthetic completion"}, 30)
	if old, err := s.clearMain(ctx, id); err != nil || old != id {
		t.Fatalf("clear: %s %v", old, err)
	}
	before := todoNow(s, todo.ID)
	if err := s.deleteConversation(ctx, "server", id); convCode(err) != "conversation_busy" {
		t.Fatalf("orphaned task owner: %v", err)
	}
	if after := todoNow(s, todo.ID); !reflect.DeepEqual(before, after) {
		t.Fatal("refused deletion changed its task")
	}
	// The existing main-chat handover moves live responsibility, while the
	// archived conversation id remains in Former for historical references.
	if moved := s.handOverMainTasks(ctx, id, "main-next"); moved != 1 {
		t.Fatalf("handover moved %d tasks", moved)
	}
	before = todoNow(s, todo.ID)
	if err := s.deleteConversation(ctx, "server", id); err != nil {
		t.Fatal(err)
	}
	if after := todoNow(s, todo.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("task changed: %+v => %+v", before, after)
	}
	if len(f.stopped) > 0 || f.sessions["server"][4].Status != proto.StatusWorking {
		t.Fatal("deletion stopped the delegated Agent")
	}
	if got := s.settings(ctx).Former; len(got) != 1 || got[0].SessionID != id {
		t.Fatalf("origin reference lost: %+v", got)
	}
	for _, method := range []string{DeviceSessionItems, DeviceAssistantSend, "session.send", "session.resume", "session.fork", "session.watch"} {
		if err := s.call(ctx, "server", method, map[string]any{"session_id": id}, nil); convCode(err) != "conversation_deleted" {
			t.Errorf("%s: %v", method, err)
		}
	}
	if err := s.call(ctx, "server", "session.start", map[string]any{"session_id": "new-id", "resume_session_id": id}, nil); convCode(err) != "conversation_deleted" {
		t.Fatalf("resume copied deleted transcript: %v", err)
	}
	if _, err := s.extensionMessages(ctx, assistantwire.AssistantMessagesParams{ID: id}); convCode(err) != "conversation_deleted" {
		t.Fatalf("extension read: %v", err)
	}
}

func TestDeletePersonaStorageFailure(t *testing.T) {
	f := healthHub()
	st := &memStore{vals: map[string][]byte{}}
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.MachineID = "server" })
	st.failPut = errors.New("synthetic storage unavailable")
	if err := s.deleteConversation(context.Background(), "server", "work-0000-0000"); err == nil {
		t.Fatal("failed storage acknowledged")
	}
	st.failPut = nil
	if err := s.conversationAvailable(context.Background(), "server", "work-0000-0000"); err != nil {
		t.Fatalf("failed delete changed memory: %v", err)
	}
	st.failKey(deletedConversationsKey, errors.New("synthetic read failure"))
	s2 := newServiceOn(t, f, st)
	if err := s2.deleteConversation(context.Background(), "server", "work-0000-0000"); err == nil {
		t.Fatal("unread ledger overwritten")
	}
	if err := s2.call(context.Background(), DeviceAll, DeviceDevicesList, map[string]any{}, nil); err != nil {
		t.Fatalf("unread deletion ledger blocked unrelated computer inventory: %v", err)
	}
}

func TestListedSessionUnreadDeletionLedger(t *testing.T) {
	ft := newFleetTest(t)
	setSettings(t, ft.s, func(v *Settings) { v.MachineID = "mac" })
	row := proto.Session{MachineID: "mac", SessionID: "chat", Assistant: true, Cwd: "/home/me/.persona/assistant", Status: proto.StatusIdle}
	ft.d.setSessions("mac", []proto.Session{row})
	if got, ok := ft.s.listedSession("mac", row.SessionID); !ok || !got.Assistant {
		t.Fatalf("fixture does not exercise an assistant row: %+v, %v", got, ok)
	}
	// A non-durable store caches the first read. Make the failure a fresh Service's first read of the same ledger.
	ft.s.deletedConversations.mu.Lock()
	ft.s.deletedConversations.loaded = false
	ft.s.deletedConversations.rows = nil
	ft.s.deletedConversations.mu.Unlock()
	ft.st.failKey(deletedConversationsKey, errors.New("synthetic read failure"))
	// The privacy-preserving filter leaves no assistant row; listedSession must not index it (#10305).
	if got, ok := ft.s.listedSession("mac", row.SessionID); ok || got.SessionID != "" {
		t.Fatalf("unread deletion ledger exposed a listed assistant row: %+v, %v", got, ok)
	}
}

func TestDeletePersonaFleetTombstone(t *testing.T) {
	ft := newFleetTest(t)
	setSettings(t, ft.s, func(v *Settings) { v.MachineID = "mac" })
	row := proto.Session{MachineID: "mac", SessionID: "old-chat", Assistant: true, Cwd: "/home/me/.persona/assistant", Status: proto.StatusIdle, Title: "private title", LastText: "private text"}
	ft.d.setSessions("mac", []proto.Session{row})
	if err := ft.s.deleteConversation(context.Background(), "mac", row.SessionID); err != nil {
		t.Fatal(err)
	}
	// A stale or newly changed node inventory cannot restore the conversation.
	row.UpdatedAt, row.Status = time.Now().Add(time.Hour), proto.StatusWorking
	ft.d.setSessions("mac", []proto.Session{row})
	for _, missing := range []bool{false, true} {
		if missing {
			ft.d.setSessions("mac", nil)
		}
		rows := ft.s.publishList("mac")
		if len(rows) != 1 || !rows[0].IsDeleted() || rows[0].Cwd != "" || rows[0].LastText != "" || rows[0].Title == "private title" {
			t.Fatalf("unsafe tombstone: %+v", rows)
		}
		raw, _ := json.Marshal(rows)
		if strings.Contains(string(raw), "private") {
			t.Fatalf("leaked: %s", raw)
		}
	}
	for _, event := range []string{"session.items", "session.delta", "session.screen"} {
		if ft.event("mac", event, map[string]any{"session_id": row.SessionID, "text": "private"}) {
			t.Fatalf("late %s forwarded", event)
		}
	}
	if _, _, err := ft.s.NodeAnswer(context.Background(), "mac", "assistant.context", json.RawMessage(`{"session_id":"old-chat"}`)); convCode(err) != "conversation_deleted" {
		t.Fatalf("context: %v", err)
	}
}

func TestDeletePersonaGuards(t *testing.T) {
	for _, tc := range []struct {
		name, id, status string
		starting         bool
		want             int
	}{
		{"main", "main-0000-0000", "", false, 400},
		{"working", "work-0000-0000", proto.StatusWorking, false, 409},
		{"blocked", "work-0000-0000", proto.StatusBlocked, false, 409},
		{"starting", "work-0000-0000", proto.StatusIdle, true, 409},
		{"unknown", "unknown", "", false, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := healthHub()
			f.sessions["server"][1].Status, f.sessions["server"][1].Starting = tc.status, tc.starting
			s := newTestService(t, f)
			setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
			w := serve(s, "DELETE", "/api/v1/assistant/conversations/"+tc.id, `{"machine_id":"server"}`)
			if w.Code != tc.want {
				t.Fatalf("%d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

func TestDeletePersonaWaitingAndAuthorization(t *testing.T) {
	for name, edit := range map[string]func(*proto.Session){
		"pending":    func(c *proto.Session) { c.PendingInteractions = 1 },
		"queue":      func(c *proto.Session) { c.Queued = []string{"synthetic waiting message"} },
		"background": func(c *proto.Session) { c.Background = []proto.BackgroundTask{{ID: "job"}} },
	} {
		t.Run(name, func(t *testing.T) {
			f := healthHub()
			edit(&f.sessions["server"][1])
			s := newTestService(t, f)
			if err := s.deleteConversation(context.Background(), "server", "work-0000-0000"); convCode(err) != "conversation_busy" {
				t.Fatalf("waiting chat deleted: %v", err)
			}
		})
	}
	f := healthHub()
	s := newTestService(t, f)
	for _, role := range []string{roleAnswer, roleExtension} {
		r := httptest.NewRequest("DELETE", "/api/v1/assistant/conversations/work-0000-0000", strings.NewReader(`{"machine_id":"server"}`))
		r.Header.Set(RoleHeader, role)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s delete: %d", role, w.Code)
		}
	}
}

func TestDeletePersonaDiscardsReadAlreadyInFlight(t *testing.T) {
	f := healthHub()
	s := newTestService(t, f)
	f.items = []proto.Item{{Text: "synthetic old transcript"}}
	f.hookReads(receiptHooks{after: func(read int) {
		if read == 1 {
			if err := s.deleteConversation(context.Background(), "server", "work-0000-0000"); err != nil {
				t.Fatal(err)
			}
		}
	}})
	var out struct {
		Items []proto.Item `json:"items"`
	}
	err := s.call(context.Background(), "server", DeviceSessionItems, map[string]any{"session_id": "work-0000-0000"}, &out)
	if convCode(err) != "conversation_deleted" || len(out.Items) != 0 {
		t.Fatalf("late read exposed: %+v %v", out, err)
	}
}

func TestDeletePersonaDoesNotReplayPendingMainHandoff(t *testing.T) {
	f := healthHub()
	s := newTestService(t, f)
	ctx := context.Background()
	h := mainHandoff{MachineID: "server", From: "work-0000-0000", Summary: "synthetic saved context"}
	if err := s.saveHandoff(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteConversation(ctx, "server", h.From); err != nil {
		t.Fatal(err)
	}
	if got := s.loadHandoff(ctx); got.From != "" || got.Summary != "" || len(got.Recent) > 0 {
		t.Fatalf("deleted history still queued: %+v", got)
	}
}

// The production settings column is jsonb: a synthetic map alone would accept
// keys containing a NUL escape that Postgres rejects.
func TestDeletePersonaPostgresRoundTrip(t *testing.T) {
	database := todoTestDB(t)
	f := healthHub()
	s := newServiceOn(t, f, database)
	ctx := context.Background()
	if err := s.deleteConversation(ctx, "server", "work-0000-0000"); err != nil {
		t.Fatal(err)
	}
	s2 := newServiceOn(t, f, database)
	if err := s2.conversationAvailable(ctx, "server", "work-0000-0000"); convCode(err) != "conversation_deleted" {
		t.Fatalf("persisted delete lost: %v", err)
	}
	if err := s2.deleteConversation(ctx, "server", "work-0000-0000"); err != nil {
		t.Fatalf("restart retry: %v", err)
	}
}

func TestDeletePersonaRefusesAnotherChatsUnfinishedSupervisor(t *testing.T) {
	f := healthHub()
	s := newTestService(t, f)
	ctx := context.Background()
	task, err := s.newTodo(ctx, "Synthetic supervised task", "", "another-chat", "", true)
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task.ID, "work-0000-0000", []string{"Synthetic completion"}, 30)
	before := todoNow(s, task.ID)
	if err := s.deleteConversation(ctx, "server", "work-0000-0000"); convCode(err) != "conversation_busy" {
		t.Fatalf("supervisor orphaned: %v", err)
	}
	if !reflect.DeepEqual(before, todoNow(s, task.ID)) {
		t.Fatal("refusal changed task")
	}
	if err := s.conversationAvailable(ctx, "server", "work-0000-0000"); err != nil {
		t.Fatalf("refusal hid supervisor: %v", err)
	}
}
