package hub

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The stop button in an app on a Feishu /agent conversation (a reporting one, #3308): the hub interrupts it and tells
// the assistant, which marks it stopped by the app (the plugin reads that in GET /api/v1/assistant/reporters).
// Stopping any other conversation marks nothing.
func TestInterruptingAReportingConversationMarksItStopped(t *testing.T) {
	h, n := conversationsHub(t)
	at := time.Now().UTC()
	if err := h.store.PutSetting(context.Background(), "assistant.task_reporters", map[string]any{
		"code-0003": map[string]any{"machine_id": "server", "at": at}}); err != nil {
		t.Fatal(err)
	}
	restartAssistant(t, h)
	mux := h.routes()
	tok := h.cfg.Token
	if code, out := call(t, mux, "POST", "/api/v1/sessions/server/trip-0002/interrupt", tok, ""); code != 200 {
		t.Fatalf("interrupt %d %v", code, out)
	}
	if code, out := call(t, mux, "POST", "/api/v1/sessions/server/code-0003/interrupt", tok, ""); code != 200 {
		t.Fatalf("interrupt %d %v", code, out)
	}
	if last := n.lastSend(t); last["interrupt"] != "code-0003" {
		t.Fatalf("last call to the node = %v", last)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		code, out := call(t, mux, "GET", "/api/v1/assistant/reporters", tok, "")
		list, _ := out["reporters"].([]any)
		if code != 200 || len(list) != 1 {
			t.Fatalf("reporters %d %v", code, out)
		}
		r, _ := list[0].(map[string]any)
		if r["session_id"] != "code-0003" {
			t.Fatalf("reporter %v", r)
		}
		if r["stopped_by"] == "app" && r["stopped_at"] != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not marked stopped: %v", r)
		}
	}
}

func managedRetryHub(t *testing.T) (*Hub, *db.DB, time.Time) {
	t.Helper()
	h, _, _ := newBusHub(t)
	n := newAssistantNode(t, h)
	now := time.Now().UTC()
	editSessions(n.m, func([]proto.Session) []proto.Session {
		return []proto.Session{{SessionID: "agent-0005", Cwd: "/code/app", Status: proto.StatusDone, State: proto.StateUnread,
			UpdatedAt: now, TurnError: "API Error: 503 Service Unavailable"}}
	})
	turnAt, wakeAt := now.Add(-time.Minute), now.Add(time.Minute)
	store := managerDB(t, h)
	if err := store.PutSessionWork(context.Background(), db.SessionWork{
		MachineID: "server", SessionID: "agent-0005", TurnAt: turnAt, WakeAt: &wakeAt,
		Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient, TurnAt: turnAt, At: turnAt, WakeAt: &wakeAt},
	}); err != nil {
		t.Fatal(err)
	}
	connectAssistant(t, h) // its first work snapshot restores the pending retry
	return h, store, now
}

func requireModelRetryRetired(t *testing.T, store *db.DB, since time.Time, action string) {
	t.Helper()
	// EventUserStopped launches UserStopped outside the bus event worker so a
	// slow node cannot hold that worker. Settle only drains the worker itself;
	// wait for the authoritative durable retirement, as an app observes it.
	eventually(t, 5*time.Second, action+" persisted the retry retirement", func() bool {
		rows, err := store.SessionWorks(context.Background(), since.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.MachineID == "server" && row.SessionID == "agent-0005" {
				return row.WakeAt == nil && row.Work.RetiredAt != nil
			}
		}
		return false
	})
}

func TestInterruptingManagedAgentRetiresModelRetry(t *testing.T) {
	h, store, now := managedRetryHub(t)
	if code, out := call(t, h.routes(), "POST", "/api/v1/sessions/server/agent-0005/interrupt", h.cfg.Token, ""); code != 200 {
		t.Fatalf("interrupt %d %v", code, out)
	}
	settleAssistant(h)
	requireModelRetryRetired(t, store, now, "managed-Agent Stop")
}

func TestExitManagedAgentRetiresModelRetry(t *testing.T) {
	h, store, now := managedRetryHub(t)
	code, phone := call(t, h.routes(), "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"iPhone","kind":"phone"}`)
	if code != 200 {
		t.Fatalf("device token %d %v", code, phone)
	}
	if code, out := call(t, h.routes(), "POST", "/api/v1/sessions/server/agent-0005/messages", phone["token"].(string), `{"text":"/exit"}`); code != 200 {
		t.Fatalf("exit %d %v", code, out)
	}
	settleAssistant(h)
	requireModelRetryRetired(t, store, now, "managed-Agent /exit")
}
