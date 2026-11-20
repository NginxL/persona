package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskExecutionOverridePinsAcrossRestart(t *testing.T) {
	for _, moveComputer := range []bool{false, true} {
		name := "provider"
		if moveComputer {
			name = "provider_and_computer"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, field, value string
				old                ModelBinding
			}{
				{name: "model", field: "model", value: "chosen-model"},
				{name: "effort", field: "effort", value: "low"},
				{name: "equal_model", field: "model", value: "main-model"},
				{name: "equal_effort", field: "effort", value: "high"},
				{name: "legacy_model", field: "model", value: "chosen-model", old: ModelBinding{Model: "chosen-model"}},
				{name: "legacy_effort", field: "effort", value: "low", old: ModelBinding{Effort: "low"}},
				{name: "legacy_provider_pinned", field: "model", value: "chosen-model", old: ModelBinding{Provider: "launcher.claude", Model: "chosen-model"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := context.Background()
					f := newFakeHub().withMachine()
					f.sessions["remote"] = nil
					f.hello.Assistant.Machines = []string{"server", "remote"}
					f.doctor = map[string]string{"server": "2.1.308", "remote": "2.1.308"}
					f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}, {Name: "codex", Agent: "codex"}}
					st := newSettingsFaultStore()
					if err := st.PutSetting(ctx, tasksKey, []Task{}); err != nil {
						t.Fatal(err)
					}
					s := newServiceOn(t, f, st)
					setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "main-model", Effort: "high"})
					body := map[string]any{"title": "Check", "prompt": "Check files", "repeat": "interval", "every_minutes": 30, "enabled": true, tc.field: tc.value}
					if tc.name == "equal_effort" {
						// The execution editor distinguishes a deliberate equal-default choice from a legacy echo.
						body["model_revision"] = 0
					}
					method, path := "POST", "/api/v1/assistant/tasks"
					if tc.old != (ModelBinding{}) {
						setTasks(s, granted(Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true}))
						if err := st.PutSetting(ctx, modelRecordKey("task.job"), modelRecord{Revision: 7, Binding: tc.old}); err != nil {
							t.Fatal(err)
						}
						body["model_revision"] = 7
						method, path = "PUT", path+"/job"
					}
					raw, _ := json.Marshal(body)
					r := request(s, method, path, string(raw))
					if r.Code != 200 {
						t.Fatalf("save: %d %s", r.Code, r.Body.String())
					}
					var saved struct{ Task Task }
					if err := json.Unmarshal(r.Body.Bytes(), &saved); err != nil {
						t.Fatal(err)
					}
					id := saved.Task.ID
					before, err := s.taskExecution(ctx, saved.Task, false)
					if err != nil {
						t.Fatal(err)
					}
					// Run now saves first. Refuse its separate admission, leaving the same editor open.
					st.memStore.mu.Lock()
					st.failPut = errors.New("synthetic admission unavailable")
					st.memStore.mu.Unlock()
					failed := request(s, "POST", "/api/v1/assistant/tasks/"+id+"/run", `{}`)
					st.memStore.mu.Lock()
					st.failPut = nil
					st.memStore.mu.Unlock()
					if failed.Code != 503 || scheduleSendCount(f) != 0 {
						t.Fatalf("failed admission dispatched work: %d %s", failed.Code, failed.Body.String())
					}
					// Change both sources of inheritance only after saving, then read from the same store in a new service.
					setModel(t, s, "assistant", ModelBinding{Provider: "launcher.codex", Model: "other-model", Effort: "medium"})
					if moveComputer {
						setSettings(t, s, func(v *Settings) { v.MachineID = "remote" })
					}
					// The reconciled web form's unrelated edit is sparse: no clears or stale model revision.
					edit := `{"title":"Edited after failure","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true}`
					if r := request(s, "PUT", "/api/v1/assistant/tasks/"+id, edit); r.Code != 200 {
						t.Fatalf("second save: %d %s", r.Code, r.Body.String())
					}
					restarted := newServiceOn(t, f, st)
					main, err := restarted.resolveModel(ctx, "assistant")
					wantMainMachine := "server"
					if moveComputer {
						wantMainMachine = "remote"
					}
					if err != nil || main.Provider != "launcher.codex" || restarted.settings(ctx).MachineID != wantMainMachine {
						t.Fatalf("Assistant default did not change: %+v machine=%s err=%v", main, restarted.settings(ctx).MachineID, err)
					}
					record, err := restarted.modelRecord(ctx, "task."+id)
					if err != nil || record.Binding.Provider != "launcher.claude" {
						t.Fatalf("provider pin missing after restart: %+v %v", record, err)
					}
					if tc.old != (ModelBinding{}) {
						wantRevision := int64(8)
						if tc.old.Provider != "" {
							wantRevision = 7 // Only the task's inherited computer needs saving.
						}
						if record.Revision != wantRevision {
							t.Errorf("normalization revision = %d, want %d", record.Revision, wantRevision)
						}
					}
					stored, _ := taskByID(restarted, id)
					if stored.ExecutionMachineID != "server" || stored.Title != "Edited after failure" {
						t.Errorf("computer pin missing after restart: %+v", stored)
					}
					if stored.StartGrant != nil {
						t.Errorf("pinning the computer retained the old start grant: %+v", stored.StartGrant)
					}
					// Assert at the real outbound RPC, including a strict resume on the second run.
					sends := 0
					f.answerNode(DeviceAssistantSend, func(machine string, raw json.RawMessage) (any, error) {
						var p map[string]any
						_ = json.Unmarshal(raw, &p)
						sends++
						model, effort := "", ""
						if tc.field == "model" {
							model = tc.value
						} else {
							effort = tc.value
						}
						gotModel, _ := p["model"].(string)
						gotEffort, _ := p["effort"].(string)
						if machine != "server" || p["launcher"] != "claude" || gotModel != model || gotEffort != effort || p["task_execution"] != true {
							t.Errorf("saved override moved at send: machine=%s payload=%v", machine, p)
						}
						if sends == 2 && (p["session_id"] != "pinned-task" || p["resume_only"] != true) {
							t.Errorf("second run lost strict resume: %v", p)
						}
						return map[string]string{"session_id": "pinned-task"}, nil
					})
					if r := request(restarted, "POST", "/api/v1/assistant/tasks/"+id+"/run", `{}`); r.Code != 200 {
						t.Fatalf("manual run: %d %s", r.Code, r.Body.String())
					}
					waitRuns(t, restarted, 1)
					task, _ := taskByID(restarted, id)
					restarted.runDue(ctx, task.At)
					restarted.sched.inflight.Wait()
					for i, run := range waitRuns(t, restarted, 2) {
						if run.Execution == nil || run.Execution.MachineID != "server" || run.Execution.Launcher != "claude" || run.Manual != (i == 0) {
							t.Errorf("run history differs from saved target: %+v", run)
						}
					}
					if sends != 2 || before.MachineID != "server" || before.Launcher != "claude" {
						t.Fatalf("wrong saved execution or send count: %+v sends=%d", before, sends)
					}
				})
			}
		})
	}
}

func TestTaskExecutionComputerCanFollowAssistant(t *testing.T) {
	for _, tc := range []struct {
		name, patch, launcher, model string
		configured                   bool
	}{
		{"untouched", ``, "codex", "other-model", false},
		{"clear computer", `,"execution_machine_id":""`, "claude", "chosen-model", true},
		{"clear all", `,"execution_machine_id":"","launcher":"","model":"","effort":""`, "codex", "other-model", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFakeHub().withMachine()
			f.sessions["remote"] = nil
			f.hello.Assistant.Machines = []string{"server", "remote"}
			f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}, {Name: "codex", Agent: "codex"}}
			st := newSettingsFaultStore()
			s := newServiceOn(t, f, st)
			task := Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true}
			if tc.configured {
				task.ExecutionMachineID = "server"
				setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Model: "chosen-model"})
			}
			setTasks(s, task)
			body := `{"title":"Edited","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true` + tc.patch + `}`
			if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code != 200 {
				t.Fatalf("save: %d %s", r.Code, r.Body.String())
			}
			// A later unrelated edit must not undo the user's explicit return to inheritance.
			if r := request(s, "PUT", "/api/v1/assistant/tasks/job", `{"title":"Edited again","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true}`); r.Code != 200 {
				t.Fatalf("unrelated save: %d %s", r.Code, r.Body.String())
			}
			setSettings(t, s, func(v *Settings) { v.MachineID = "remote" })
			setModel(t, s, "assistant", ModelBinding{Provider: "launcher.codex", Model: "other-model"})
			restarted := newServiceOn(t, f, st)
			stored, _ := taskByID(restarted, "job")
			if stored.ExecutionMachineID != "" || restarted.settings(ctx).MachineID != "remote" {
				t.Fatalf("computer inheritance lost: %+v", stored)
			}
			run, err := runNow(t, restarted, "job")
			if err != nil {
				t.Fatal(err)
			}
			if p := f.lastSend(t); run.MachineID != "remote" || p["launcher"] != tc.launcher || p["model"] != tc.model {
				t.Fatalf("did not run with restored inheritance: %+v %v", run, p)
			}
		})
	}
}
