package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise manager admission and actual node delivery, not just picker visibility.
// Pi's companion supplies the isolated policy capability; no engine is executed.
func TestCodexAndPiAssistantConversationRoles(t *testing.T) {
	for _, engine := range []string{"codex", "pi"} {
		for _, role := range []string{"main", "background_supervisor", "direction_supervisor", "scheduled_task"} {
			t.Run(engine+"/"+role, func(t *testing.T) {
				ctx := context.Background()
				s, f := mainChatFixture(t)
				launcher := "codex-gw"
				if engine == "pi" {
					launcher = "pi-web"
					f.launcherList = append(f.launcherList, proto.Launcher{Name: launcher, Agent: "pi", Source: "pi-web", Assistant: true, SupervisionPolicy: true})
				}
				binding := ModelBinding{Provider: "launcher." + launcher}
				for _, purpose := range []string{"assistant", purposeMain, purposeSupervise} {
					setModel(t, s, purpose, binding)
				}
				yes := role != "background_supervisor"
				setSettings(t, s, func(v *Settings) { v.Main = ""; v.MainSupervisesOwnTasks = &yes })
				var id string
				wantPolicy := true
				switch role {
				case "main":
					code, session := startConv(t, s, `{"prompt":"Check the current status.","main":true}`)
					if code != http.StatusOK || session.SessionID == "" {
						t.Fatalf("main refused: %d", code)
					}
					id = session.SessionID
					if s.settings(ctx).Main != id || f.lastSend(t)["is_main"] != true {
						t.Fatal("main identity was not retained")
					}
				case "background_supervisor":
					s.noBackground = false
					if !s.backgroundOn(s.settings(ctx)) {
						t.Fatal("background supervisor is not enabled")
					}
					var err error
					id, err = s.startBackground(ctx)
					if err != nil || id == "" {
						t.Fatalf("background supervisor refused: %v", err)
					}
					if s.settings(ctx).BackgroundSupervisor != id {
						t.Fatal("background role was not retained")
					}
				case "direction_supervisor":
					code, session := startConv(t, s, `{"prompt":"Follow this direction."}`)
					if code != http.StatusOK || session.SessionID == "" {
						t.Fatalf("direction start refused: %d", code)
					}
					id = session.SessionID
					row := proto.Session{SessionID: id, MachineID: "server", Assistant: true, Agent: engine, Launcher: launcher, Status: proto.StatusIdle}
					if engine == "pi" {
						row.SourceKind = proto.SourceRuntime
						row.SourceInstanceID = "pi-web"
						row.Capabilities = &proto.SessionCapabilities{SupervisionPolicy: true}
					}
					f.setSessions("server", row)
					raw, _ := json.Marshal([]string{id})
					change, err := s.setLeaders(ctx, raw)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = change.apply(ctx); err != nil {
						t.Fatal(err)
					}
					p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: id, Human: true, Params: map[string]any{"text": "Review the next step.", "client_msg_id": "direction-follow-up"}})
					if err != nil {
						t.Fatalf("direction follow-up refused: %v", err)
					}
					p["session_id"] = id
					if err = s.call(ctx, "server", DeviceAssistantSend, p, nil); err != nil {
						t.Fatal(err)
					}
					if !s.isLeader(ctx, id) {
						t.Fatal("direction supervisor lost its role")
					}
				case "scheduled_task":
					wantPolicy = false
					setTasks(s, Task{ID: "engine-check", Prompt: "Check the current status.", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
					setModel(t, s, "task.engine-check", binding)
					first, err := runNow(t, s, "engine-check")
					if err != nil || first.SessionID == "" {
						t.Fatalf("scheduled start refused: %v", err)
					}
					id = first.SessionID
					second, err := runNow(t, s, "engine-check")
					if err != nil || second.SessionID != id {
						t.Fatalf("scheduled resume refused or replaced: %v", err)
					}
					task, _ := taskByID(s, "engine-check")
					if task.RunLauncher != launcher || task.RunSessionID != id || task.RunExecution == nil || task.RunExecution.Agent != engine {
						t.Fatal("scheduled execution changed its engine or identity")
					}
					if p := f.lastSend(t); p["resume_only"] != true || p["task_execution"] != true {
						t.Fatal("second run did not resume its scheduled conversation")
					}
				}
				p := f.lastSend(t)
				// Runtime follow-ups retain their existing owner rather than selecting a new launcher.
				if role != "direction_supervisor" && p["launcher"] != launcher {
					t.Fatalf("wrong launcher: %v", p["launcher"])
				}
				if id == "" || f.sent() == 0 {
					t.Fatal("the hub did not deliver a conversation input")
				}
				if wantPolicy && p["supervision_instructions"] != prompts.RenderSupervisionInstructions() {
					t.Fatal("authorized supervisor input did not carry the exact policy")
				}
			})
		}
	}
}
