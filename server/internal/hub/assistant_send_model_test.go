package hub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Which launcher and model a message for one of the assistant's conversations runs on (the assistant's sendModel,
// assistant/start.go): the phone's messages (assistant.send_params), the node's own, and the follow-ups the assistant
// runs.

// A follow-up the assistant scheduled for itself goes into its conversation on that conversation's launcher (#1518): the main conversation on Models › Main chat (even when it last ran on another launcher), another
// of its conversations on what it runs. On the Agents launcher (plain claude by default) a follow-up moved the main
// conversation off its launcher, and every message after stayed there.
func TestAFollowUpKeepsItsConversationsLauncher(t *testing.T) {
	h, _ := modelTestHub(t)
	n := newAssistantNode(t, h)
	ctx := context.Background()
	if err := h.store.PutSetting(ctx, "assistant", assistant.Settings{MachineID: "server", Main: "main"}); err != nil {
		t.Fatal(err)
	}
	putModel(t, h, "assistant", assistantwire.ModelBinding{Provider: "launcher.conv", Effort: "high"})
	putModel(t, h, "agent.new", assistantwire.ModelBinding{Provider: "launcher.agents"})
	n.m.mu.Lock()
	n.m.online = true // runTask's execution guard sees this in-process peer as an online machine.
	n.m.sessions = []proto.Session{
		{SessionID: "main", Launcher: "claude", Model: "stale", Assistant: true, Status: proto.StatusOffline},
		{SessionID: "side", Launcher: "side-l", Model: "side-m", Assistant: true, Status: proto.StatusOffline},
		// the morning brief's own conversation, moved onto plain claude by a follow-up before #1518 (#1544)
		{SessionID: "daily", Launcher: "claude", Assistant: true, Status: proto.StatusOffline},
	}
	n.m.mu.Unlock()
	n.mu.Lock()
	n.known["main"], n.known["side"], n.known["daily"] = true, true, true
	n.mu.Unlock()
	seedTasks(t, h,
		assistant.Task{ID: "fu_main", Prompt: "会前准备", Repeat: "once", Enabled: true, MachineID: "server", SessionID: "main", CreatedBy: "assistant"},
		assistant.Task{ID: "fu_side", Prompt: "再看一眼", Repeat: "once", Enabled: true, MachineID: "server", SessionID: "side", CreatedBy: "assistant"},
		assistant.Task{ID: "own", Prompt: "早报", Repeat: "daily", Time: "09:00", Enabled: true},
		assistant.Task{ID: "r", Prompt: "早报", Repeat: "daily", Time: "09:00", Enabled: true, RunMachineID: "server", RunSessionID: "daily", RunLauncher: "conv"},
		assistant.Task{ID: "fu_daily", Prompt: "会前准备", Repeat: "once", Enabled: true, MachineID: "server", SessionID: "daily", CreatedBy: "assistant"})
	connectAssistant(t, h) // the follow-ups are the assistant's, the launcher and model they run on its decision
	for _, c := range []struct{ task, session, launcher, model, effort string }{
		{"fu_main", "main", "conv", "", "high"},
		{"fu_side", "side", "side-l", "side-m", ""},
		// a scheduled task's own conversation follows the main chat like the main one, whatever it ran last (#1544)
		{"fu_daily", "daily", "conv", "", "high"},
	} {
		got, err := runNow(t, h, c.task)
		if err != nil {
			t.Fatal(err)
		}
		p := n.lastSend(t)
		model, _ := p["model"].(string)
		effort, _ := p["effort"].(string)
		if got.SessionID != c.session || p["session_id"] != c.session || p["launcher"] != c.launcher || model != c.model || effort != c.effort {
			t.Errorf("%s: into %s with %v, want %s on %s %q %q", c.task, got.SessionID, p, c.session, c.launcher, c.model, c.effort)
		}
		if fp, _ := p["model_fingerprint"].(string); fp == "" {
			t.Errorf("%s: no model fingerprint: %v", c.task, p)
		}
	}
	// A task with no conversation to go back to runs in its own, on the main chat's launcher (#1544), not the Agents one.
	if _, err := runNow(t, h, "own"); err != nil {
		t.Fatal(err)
	}
	if p := n.lastSend(t); p["launcher"] != "conv" || p["effort"] != "high" {
		t.Fatalf("a task's own conversation runs on 主对话's launcher: %v", p)
	}
}

// What the node asks before it tells a conversation by itself that a restart lost its background tasks (#1554): the
// launcher and model a message from the phone runs on. The main conversation follows Models › Main chat, not the model it
// last ran (which left the phone's next message on another model than the running process, refused while it was busy).
func TestAssistantModelIsWhatThePhonesMessageRunsOn(t *testing.T) {
	h, _ := modelTestHub(t)
	n := newAssistantNode(t, h)
	ctx := context.Background()
	if err := h.store.PutSetting(ctx, "assistant", assistant.Settings{MachineID: "server", Main: "main"}); err != nil {
		t.Fatal(err)
	}
	putModel(t, h, "assistant", assistantwire.ModelBinding{Provider: "launcher.conv", Effort: "high"})
	n.m.mu.Lock()
	n.m.sessions = []proto.Session{{SessionID: "main", Launcher: "conv", Model: "picked-once", Assistant: true, Status: proto.StatusIdle}}
	n.m.mu.Unlock()
	connectAssistant(t, h)
	params, _ := json.Marshal(map[string]any{"session_id": "main"})
	got, err := nodeAsks(ctx, n.m, "assistant.model", params)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := h.asst().SendParams(ctx, "server", "main", map[string]any{}) // what sendMessage asks
	if err != nil {
		t.Fatal(err)
	}
	p := got.(map[string]any)
	if p["launcher"] != "conv" || p["model"] != nil || p["effort"] != "high" || p["model_fingerprint"] == nil || p["model_fingerprint"] != phone["model_fingerprint"] || p["is_main"] != true || phone["is_main"] != true {
		t.Fatalf("assistant.model = %v, the phone's message runs on %v", p, phone)
	}
	// Another conversation the hub doesn't list yet (the node just started) keeps its own: no model to say, and an
	// explicitly empty supervision role, not an ordinary conversation's manual (#8251).
	params, _ = json.Marshal(map[string]any{"session_id": "side"})
	if got, err := nodeAsks(ctx, n.m, "assistant.model", params); err != nil || len(got.(map[string]any)) != 2 ||
		got.(map[string]any)["is_main"] != false || got.(map[string]any)["supervision_instructions"] != "" {
		t.Fatalf("an unlisted conversation: %v %v", got, err)
	}
}
