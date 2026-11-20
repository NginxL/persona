package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A task's own conversation (started, then continued) runs on the main chat category (#1544: not Agents, whatever that
// is set to), never on the task's old fields; delivery into the conversation it came from is a turn of that
// conversation (#1518): its launcher, still none of the task's old fields, the task's permission mode.
func TestScheduledExecutionUsesMainConversationCategoryAndNoOldOverrides(t *testing.T) {
	h, _ := modelTestHub(t)
	n := newAssistantNode(t, h)
	n.m.mu.Lock()
	n.m.online = true
	n.m.mu.Unlock()
	n.mu.Lock()
	n.modelChecks = map[string]string{"claude\x00category-model": "claude"}
	n.mu.Unlock()
	putModel(t, h, "assistant", assistantwire.ModelBinding{Provider: "launcher.claude", Model: "category-model", Effort: "high"})
	putModel(t, h, "agent.new", assistantwire.ModelBinding{Provider: "launcher.agents", Model: "agents-model"})
	putModel(t, h, "schedule", assistantwire.ModelBinding{Provider: "launcher.old-schedule"})
	task := assistant.Task{ID: "one", Title: "任务", Prompt: "执行任务", Launcher: "old-task", Model: "old-model", Effort: "low", PermissionMode: "bypassPermissions", Repeat: "interval", EveryMinutes: 30, Enabled: true}
	fromSource := task // the same task from a conversation: its runs go there first
	fromSource.ID, fromSource.SessionID, fromSource.MachineID = "two", "source", "server"
	seedTasks(t, h, task, fromSource)
	connectAssistant(t, h)
	for i := 0; i < 2; i++ {
		if _, err := runNow(t, h, "one"); err != nil {
			t.Fatal(err)
		}
		p := n.lastSend(t)
		if p["launcher"] != "claude" || p["model"] != "category-model" || p["effort"] != "high" || p["permission_mode"] != "bypassPermissions" {
			t.Fatalf("task creation/resume kept an old override or changed permission: %+v", p)
		}
	}
	n.mu.Lock()
	n.known["source"] = true
	n.mu.Unlock()
	editSessions(n.m, func([]proto.Session) []proto.Session {
		return []proto.Session{{SessionID: "source", Launcher: "old-source"}}
	})
	if _, err := runNow(t, h, "two"); err != nil {
		t.Fatal(err)
	}
	p := n.lastSend(t)
	if p["session_id"] != "source" || p["launcher"] != "old-source" || p["model"] != nil || p["effort"] != nil || p["permission_mode"] != "bypassPermissions" {
		t.Fatalf("delivery into the source conversation changed its launcher or kept an old override: %+v", p)
	}
}
