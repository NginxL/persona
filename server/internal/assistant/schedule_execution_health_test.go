package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

// Use a node-shaped prepare response: each space is prepared under the selected computer's own home.
func healthExecutionService(t *testing.T) (*Service, *fakeHub, *[]map[string]string) {
	t.Helper()
	f := healthHub()
	f.devices[0].Online = true
	f.devices = append(f.devices, Device{ID: "remote", Name: "Other computer", Home: "/home/other", Online: true})
	f.sessions["remote"] = nil
	f.answerApp(AppPlugins, PluginsAssistant, func(json.RawMessage) (any, error) {
		return map[string]any{"instructions": "# Main-space plugin marker", "skills": nil}, nil
	})
	f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}, {Name: "codex", Agent: "codex"}}
	f.zshrc = map[string][]proto.Launcher{"remote": {{Name: "claude", Agent: "claude"}, {Name: "codex", Agent: "codex"}}}
	prepared := []map[string]string{}
	f.answerNode(DeviceAssistantPrepare, func(machine string, raw json.RawMessage) (any, error) {
		var p struct {
			Dir string `json:"dir"`
			MD  string `json:"claude_md"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		home := "/home/me"
		if machine == "remote" {
			home = "/home/other"
		}
		cwd := path.Join(home, p.Dir)
		prepared = append(prepared, map[string]string{"machine": machine, "dir": p.Dir, "md": p.MD, "cwd": cwd})
		return map[string]any{"cwd": cwd}, nil
	})
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	return s, f, &prepared
}

func TestHealthStartCannotSkipCanonicalPreparation(t *testing.T) {
	s, f, prepared := healthExecutionService(t)
	sess, err := s.Start(context.Background(), StartParams{Text: "Check the health summary", Space: SpaceHealth, Cwd: "/work/project", TaskExecution: true, TaskModel: &ModelBinding{Provider: "launcher.claude"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "/home/me/" + compat.StateDir + "/health"
	if p := f.lastSend(t); p["cwd"] != want || sess.Space != SpaceHealth || sess.Cwd != want || len(*prepared) == 0 {
		t.Fatalf("health start bypassed its directory/instructions: send=%v session=%+v prepared=%v", p, sess, *prepared)
	}
	if p := (*prepared)[len(*prepared)-1]; p["dir"] != s.healthRel() || p["md"] != s.healthClaudeMD(context.Background(), "server") {
		t.Fatalf("not the health preparation: %+v", p)
	}
}

func TestTaskExecutionHealthRejectsDirectoryOnSave(t *testing.T) {
	s, f, _ := healthExecutionService(t)
	setTasks(s, Task{ID: "health", Title: "Health check", Prompt: "Check the summary", Space: SpaceHealth, Repeat: repeatDaily, Time: "09:00", Enabled: true})
	r := request(s, "PUT", "/api/v1/assistant/tasks/health", `{"title":"Changed","prompt":"Check the summary","repeat":"daily","time":"09:00","enabled":true,"cwd":"/work/project","model":"test-model","model_revision":0}`)
	t.Logf("save result: %d %s", r.Code, r.Body)
	if r.Code != http.StatusBadRequest {
		t.Fatalf("health directory edit accepted: %d %s", r.Code, r.Body)
	}
	task, _ := taskByID(s, "health")
	record, err := s.modelRecord(context.Background(), "task.health")
	if err != nil || task.Title != "Health check" || task.Cwd != "" || record.Revision != 0 || f.sent() != 0 {
		t.Fatalf("rejection changed configuration: %+v %+v %v", task, record, err)
	}
}

func TestTaskExecutionHealthRejectsStoredDirectoryBeforeDispatch(t *testing.T) {
	for _, manual := range []bool{true, false} {
		name := "scheduled"
		if manual {
			name = "manual"
		}
		t.Run(name, func(t *testing.T) {
			s, f, prepared := healthExecutionService(t)
			task := Task{ID: "health", Title: "Health check", Prompt: "Check the summary", Space: SpaceHealth, Cwd: "/work/project", Repeat: repeatOnce, At: time.Now(), Enabled: true}
			setTasks(s, task)
			_, err := s.runTask(task, time.Now(), time.Now(), manual, false)
			t.Logf("run result: %v", err)
			if err == nil || f.sent() != 0 || len(*prepared) != 0 {
				t.Fatalf("stored health override reached execution: err=%v sends=%d prepared=%v", err, f.sent(), *prepared)
			}
		})
	}
}

func TestTaskExecutionHealthCanClearDirectoryAndKeepExecutionChoices(t *testing.T) {
	s, f, prepared := healthExecutionService(t)
	setTasks(s, Task{ID: "health", Title: "Health check", Prompt: "Check the summary", Space: SpaceHealth, Cwd: "/old/project", Repeat: repeatDaily, Time: "09:00", Enabled: true})
	r := request(s, "PUT", "/api/v1/assistant/tasks/health", `{"title":"Health check","prompt":"Check the summary","repeat":"daily","time":"09:00","enabled":true,"cwd":"","execution_machine_id":"remote","launcher":"codex","model":"test-model","effort":"low","model_revision":0}`)
	if r.Code != http.StatusOK {
		t.Fatalf("clear and configure: %d %s", r.Code, r.Body)
	}
	task, _ := taskByID(s, "health")
	sess, err := s.runTask(task, time.Now(), time.Now(), true, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "/home/other/" + compat.StateDir + "/health"
	p := f.lastSend(t)
	if p["cwd"] != want || p["launcher"] != "codex" || p["model"] != "test-model" || p["effort"] != "low" || p["task_execution"] != true || sess.MachineID != "remote" || sess.Space != SpaceHealth {
		t.Fatalf("health lost its context or explicit execution choices: %v %+v", p, sess)
	}
	if len(*prepared) == 0 {
		t.Fatal("health directory was not prepared")
	}
	for _, prep := range *prepared {
		if prep["machine"] != "remote" || prep["cwd"] != want || !strings.Contains(prep["md"], "health") || strings.Contains(prep["md"], "Main-space plugin marker") {
			t.Fatalf("wrong health preparation: %+v", prep)
		}
	}
	runs := waitRuns(t, s, 1)
	if runs[0].Execution == nil || runs[0].Execution.Cwd != want || runs[0].Execution.MachineID != "remote" {
		t.Fatalf("wrong run evidence: %+v", runs)
	}
}

// Cwd is not a schedule-tool option. A forged extra field cannot move a health task out of its space, and model
// choices already supported by those tools continue to work through the real task and start paths.
func TestTaskExecutionHealthToolCreationKeepsSpaceDirectory(t *testing.T) {
	s, f, prepared := healthExecutionService(t)
	for _, tool := range []struct{ name, args string }{
		{"schedule_followup", `{"at":"1h","prompt":"Check the summary","new_conversation":true,"launcher":"codex","model":"test-model","effort":"low","cwd":"/work/project"}`},
		{"schedule_routine", `{"title":"Health check","prompt":"Check the summary","time":"09:00","launcher":"codex","model":"test-model","effort":"low","cwd":"/work/project"}`},
	} {
		if out, failed := spaceTool(t, s, "sleep-000-0000", tool.name, tool.args); failed {
			t.Fatalf("%s: %s", tool.name, out)
		}
	}
	sc := s.loadSchedule(context.Background())
	sc.mu.Lock()
	tasks := append([]Task(nil), sc.tasks...)
	sc.mu.Unlock()
	count := 0
	for _, task := range tasks {
		if task.Space != SpaceHealth {
			continue
		}
		count++
		if task.Cwd != "" || task.SessionID != "" {
			t.Fatalf("tool retained an arbitrary execution directory: %+v", task)
		}
		manual := task.Repeat != repeatOnce
		if _, err := s.runTask(task, time.Now(), time.Now(), manual, false); err != nil {
			t.Fatal(err)
		}
		p := f.lastSend(t)
		if p["cwd"] != path.Join("/home/me", s.healthRel()) || p["launcher"] != "codex" || p["model"] != "test-model" || p["effort"] != "low" {
			t.Fatalf("tool-created health task: %v", p)
		}
	}
	if count != 2 {
		t.Fatalf("health tasks created: %d", count)
	}
	for _, p := range *prepared {
		if p["md"] != s.healthClaudeMD(context.Background(), p["machine"]) || strings.Contains(p["md"], "Main-space plugin marker") {
			t.Fatalf("health inherited ordinary instructions: %+v", p)
		}
	}
}

func TestTaskExecutionLegacyHealthPreparesBeforeResume(t *testing.T) {
	s, f, prepared := healthExecutionService(t)
	task := Task{ID: "health", Title: "Health check", Prompt: "Check the summary", Space: SpaceHealth, Repeat: repeatDaily, Time: "09:00", Enabled: true}
	setTasks(s, task)
	first, err := s.runTask(task, time.Now(), time.Now(), true, false)
	if err != nil {
		t.Fatal(err)
	}
	before := len(*prepared)
	second, err := s.runTask(task, time.Now(), time.Now(), true, false)
	if err != nil || second.SessionID != first.SessionID || len(*prepared) <= before {
		t.Fatalf("legacy health resume: %+v %v preparations=%d", second, err, len(*prepared))
	}
	if p := f.lastSend(t); p["cwd"] != path.Join("/home/me", s.healthRel()) || p["resume_only"] != true || p["task_execution"] != true {
		t.Fatalf("health resume lost isolation: %v", p)
	}
}

// A health follow-up may resume only a Health assistant with the canonical physical directory.
func TestTaskExecutionHealthChecksSourceDirectory(t *testing.T) {
	for _, tc := range []struct {
		name, space                          string
		assistant, sameDirectory, wantResume bool
	}{
		{name: "wrong directory", space: SpaceHealth, assistant: true},
		{name: "ordinary assistant in health directory", assistant: true, sameDirectory: true},
		{name: "non-assistant Health session", space: SpaceHealth, sameDirectory: true},
		{name: "canonical Health assistant", space: SpaceHealth, assistant: true, sameDirectory: true, wantResume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, prepared := healthExecutionService(t)
			cwd := "/work/project"
			if tc.sameDirectory {
				cwd = path.Join("/home/me", s.healthRel())
			}
			f.sessions["server"] = append(f.sessions["server"], proto.Session{SessionID: "legacy-source", Cwd: cwd, Space: tc.space, Assistant: tc.assistant, Agent: "claude"})
			f.known["legacy-source"] = true
			task := Task{ID: "health", Title: "Health check", Prompt: "Check the summary", Space: SpaceHealth, MachineID: "server", SessionID: "legacy-source", Repeat: repeatOnce, At: time.Now(), Enabled: true}
			setTasks(s, task)
			_, err := s.runTask(task, time.Now(), time.Now(), true, false)
			if err != nil {
				t.Fatal(err)
			}
			p := f.lastSend(t)
			if tc.wantResume {
				if p["session_id"] != "legacy-source" || len(*prepared) == 0 || p["cwd"] != cwd || p["task_execution"] != true {
					t.Fatalf("safe source was not prepared and continued: session=%v preparations=%d", p["session_id"], len(*prepared))
				}
			} else {
				if p["session_id"] == "legacy-source" || p["cwd"] != path.Join("/home/me", s.healthRel()) || p["task_execution"] != true {
					t.Fatalf("health continued an incompatible source: session=%v cwd=%v strict=%v", p["session_id"], p["cwd"], p["task_execution"])
				}
				runs := waitRuns(t, s, 1)
				if runs[0].ConversationReason != "source_unavailable" {
					t.Fatalf("missing source replacement reason: %+v", runs[0])
				}
			}
		})
	}
}
