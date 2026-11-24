package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type olderTaskNodeHub struct{ *fakeHub }

func (h olderTaskNodeHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	if call.Method == DeviceAssistantSend || call.Method == assistantwire.DeviceAssistantDirectory {
		var params map[string]json.RawMessage
		if err := json.Unmarshal(call.Params, &params); err != nil {
			return nil, err
		}
		if call.Method == assistantwire.DeviceAssistantDirectory || string(params["task_execution"]) == "true" && string(params[devicewire.LegacyTaskExecution]) != "true" {
			return nil, &rpc.Error{Code: proto.CodeNodeOutdated, Message: "node lacks scheduled task execution"}
		}
		if string(params["task_execution"]) == "true" && string(params[devicewire.LegacyTaskExecution]) == "true" {
			devicewire.MarkLegacyTaskExecution(ctx)
		}
		delete(params, "task_execution")
		delete(params, devicewire.LegacyTaskExecution)
		if len(params["session_id"]) != 0 {
			delete(params, "cwd")
		}
		call.Params, _ = json.Marshal(params)
	}
	return h.fakeHub.DeviceCall(ctx, call)
}

func TestTaskExecutionStrictContextsRefuseAnOlderNode(t *testing.T) {
	for _, name := range []string{"directory", "computer", "launcher", "model", "effort", "health", "strict snapshot"} {
		t.Run(name, func(t *testing.T) {
			f := readyHub()
			f.hello.Assistant.TaskExecutionWaitSeconds = ptr(1)
			st := &memStore{}
			s := withSettings(t, f, New(Options{Hub: olderTaskNodeHub{f}, Store: func() Store { return st }, Logf: testLogf(t)}))
			task := Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true}
			switch name {
			case "directory":
				task.Cwd = "/work/project"
			case "computer":
				task.ExecutionMachineID = "server"
			case "launcher":
				setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude"})
			case "model":
				setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Model: "chosen-model"})
			case "effort":
				setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Effort: "low"})
			case "health":
				task.Space = SpaceHealth
			case "strict snapshot":
				f.known["own"] = true
				task.RunSessionID, task.RunMachineID, task.RunLauncher = "own", "server", "claude"
				// A previous run's snapshot has no opt-in to the old-node protocol.
				ex, err := s.taskExecution(t.Context(), task, true, ModelBinding{Provider: "launcher.claude"})
				if err != nil {
					t.Fatal(err)
				}
				ex.Cwd, err = s.prepareSpace(t.Context(), "server", "")
				if err != nil {
					t.Fatal(err)
				}
				task.RunExecution = &ex
			}
			setTasks(s, task)
			synctest.Test(t, func(t *testing.T) {
				type response struct {
					code int
					body string
				}
				done := make(chan response, 1)
				go func() {
					r := request(s, "POST", "/api/v1/assistant/tasks/job/run", `{}`)
					done <- response{code: r.Code, body: r.Body.String()}
				}()
				<-time.After(2 * time.Second)
				synctest.Wait()
				r := <-done
				if r.code == http.StatusOK || !strings.Contains(r.body, "task_execution_wait_timeout") || f.sent() != 0 {
					t.Fatalf("strict task used old-node dispatch: %d %s, sent %d", r.code, r.body, f.sent())
				}
			})
		})
	}
}

func TestTaskExecutionDefaultRunsOnAnOlderNode(t *testing.T) {
	for _, oldNode := range []bool{true, false} {
		t.Run(fmt.Sprintf("older node %t", oldNode), func(t *testing.T) {
			f := readyHub()
			var h Hub = f
			if oldNode {
				h = olderTaskNodeHub{f}
			}
			s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return nil }, Logf: testLogf(t)}))
			setTasks(s, Task{ID: "job", Title: "Default task", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, At: time.Now()})
			var session string
			for i := 1; i <= 3; i++ {
				task, _ := taskByID(s, "job")
				if i == 3 {
					// Tasks saved before execution snapshots still own their recorded conversation.
					task.RunExecution = nil
					setTasks(s, task)
				}
				s.runDue(t.Context(), task.At)
				s.sched.inflight.Wait()
				got := waitRuns(t, s, i)[i-1]
				if got.Status != runOK || got.SessionID == "" || i > 1 && (got.SessionID != session || got.Conversation != "resumed") {
					t.Fatalf("default scheduled run %d: %+v", i, got)
				}
				sent := f.lastSend(t)
				if (sent["task_execution"] == true) == oldNode || i > 1 && sent["resume_only"] != true {
					t.Fatalf("default task used the wrong protocol: %v", sent)
				}
				session = got.SessionID
			}
		})
	}
}

func TestTaskExecutionUndiscoveredCatalogSaveAndDispatch(t *testing.T) {
	for _, catalog := range []string{"omitted", "null", "failed"} {
		for _, existing := range []bool{false, true} {
			name := "save"
			if existing {
				name = "stored"
			}
			t.Run(catalog+"/"+name, func(t *testing.T) {
				f := newFakeHub().withMachine()
				f.answerNode(DeviceLaunchersList, func(string, json.RawMessage) (any, error) {
					switch catalog {
					case "null":
						return map[string]any{"launchers": nil}, nil
					case "failed":
						return nil, errors.New("synthetic catalog unavailable")
					default:
						return map[string]any{}, nil
					}
				})
				s := newTestService(t, f)
				id := "job"
				if existing {
					setTasks(s, Task{ID: id, Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
					setModel(t, s, "task."+id, ModelBinding{Provider: "launcher.unseen-custom", Model: "test-model", Effort: "low"})
				} else {
					r := request(s, "POST", "/api/v1/assistant/tasks", `{"title":"Check","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"launcher":"unseen-custom","model":"test-model","effort":"low","model_revision":0}`)
					if r.Code != http.StatusOK {
						t.Fatalf("save undiscovered launcher: %d %s", r.Code, r.Body.String())
					}
					var saved struct{ Task Task }
					if err := json.Unmarshal(r.Body.Bytes(), &saved); err != nil {
						t.Fatal(err)
					}
					id = saved.Task.ID
					// Only the saved task, as the stored case has: runDue below looks half an hour ahead, and the
					// built-in morning routine (weekdays 09:00, machine time) would run too when this starts at 08:30
					// (#13630).
					saved.Task, _ = taskByID(s, id)
					setTasks(s, saved.Task)
				}
				if r := request(s, "POST", "/api/v1/assistant/tasks/"+id+"/run", `{}`); r.Code != http.StatusOK {
					t.Fatalf("manual undiscovered launcher: %d %s", r.Code, r.Body.String())
				}
				waitRuns(t, s, 1)
				task, _ := taskByID(s, id)
				s.runDue(context.Background(), task.At)
				s.sched.inflight.Wait()
				for i, run := range waitRuns(t, s, 2) {
					if run.Execution == nil || run.Execution.Launcher != "unseen-custom" || run.Execution.Model != "test-model" || run.Execution.Effort != "low" || run.Manual != (i == 0) {
						t.Errorf("dispatch history substituted the configured binding: %+v", run)
					}
				}
				if p := f.lastSend(t); p["launcher"] != "unseen-custom" || p["model"] != "test-model" || p["effort"] != "low" || p["task_execution"] != true {
					t.Fatalf("outbound binding: %v", p)
				}
			})
		}
	}
}

func TestTaskExecutionLegacySourceEcho(t *testing.T) {
	for _, binding := range []ModelBinding{{Provider: "launcher.claude"}, {Effort: "high"}, {Provider: "launcher.claude", Effort: "high"}, {}} {
		name := binding.Provider + "/" + binding.Effort
		t.Run(name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}, {Name: "codex", Agent: "codex"}}
			f.setSessions("server", proto.Session{SessionID: "origin", Launcher: "codex", Model: "source-model"})
			f.known["origin"] = true
			s := newTestService(t, f)
			setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Effort: "high"})
			setTasks(s, Task{ID: "job", Title: "Before", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, MachineID: "server", SessionID: "origin"})
			if binding != (ModelBinding{}) {
				setModel(t, s, "task.job", binding)
			}
			old, err := s.modelRecord(context.Background(), "task.job")
			if err != nil {
				t.Fatal(err)
			}
			body := `{"title":"After","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"session_id":"origin","launcher":"claude","effort":"high"}`
			if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code != http.StatusOK {
				t.Fatalf("legacy title edit: %d %s", r.Code, r.Body.String())
			}
			task, _ := taskByID(s, "job")
			after, err := s.modelRecord(context.Background(), "task.job")
			if err != nil || after != old || task.Title != "After" || task.SessionID != "origin" || task.ExecutionMachineID != "" {
				t.Fatalf("title edit changed execution: task=%+v record=%+v old=%+v error=%v", task, after, old, err)
			}
			for _, field := range []string{`"launcher":"codex"`, `"effort":"low"`, `"model":"chosen-model"`, `"cwd":"/work/project"`, `"execution_machine_id":"server"`} {
				body := fmt.Sprintf(`{"title":"Refused","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"session_id":"origin","model_revision":%d,%s}`, old.Revision, field)
				if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "原来的对话") {
					t.Fatalf("explicit source override accepted: %s: %d %s", field, r.Code, r.Body.String())
				}
			}
			if current, err := s.modelRecord(context.Background(), "task.job"); err != nil || current != old || f.sent() != 0 {
				t.Fatalf("refused edit changed or sent execution: %+v %v sends=%d", current, err, f.sent())
			}
			if _, err := runNow(t, s, "job"); err != nil {
				t.Fatalf("legacy source run: %v", err)
			}
			if p := f.lastSend(t); p["session_id"] != "origin" || p["resume_only"] != true || p["launcher"] != "codex" || p["model"] != "source-model" {
				t.Fatalf("legacy echo changed source execution: %v", p)
			}
		})
	}
}

func TestTaskViewShowsManagedExecutionAgentWithoutNodeCatalog(t *testing.T) {
	f := newFakeHub().withMachine()
	f.answerNode(DeviceLaunchersList, func(string, json.RawMessage) (any, error) {
		return nil, errors.New("synthetic catalog unavailable")
	})
	s := newTestService(t, f)
	putLaunchers(t, s, storedLauncher{
		ManagedLauncher: proto.ManagedLauncher{Name: "task-wrapper", Agent: "codex", BaseURL: "https://provider.example"},
		Share:           shareAll,
	})
	setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	setModel(t, s, "task.job", ModelBinding{Provider: "launcher.task-wrapper", Model: "test-model"})

	view, ok := s.taskView(t.Context(), "job")
	if !ok || view.ExecutionError != "" || view.Execution == nil ||
		view.Execution.Launcher != "task-wrapper" || view.Execution.Agent != "codex" {
		t.Fatalf("task execution view: found=%t execution=%+v error=%q", ok, view.Execution, view.ExecutionError)
	}
}

func TestTaskExecutionUnknownCatalogRetainsValidation(t *testing.T) {
	for _, check := range []string{"model-id", "model-check", "effort", "model-effort", "scope"} {
		t.Run(check, func(t *testing.T) {
			f := newFakeHub().withMachine() // Launcher discovery fails, with no global last-seen entry.
			s := newTestService(t, f)
			b := ModelBinding{Provider: "launcher.unseen-custom", Model: "test-model", Effort: "low"}
			switch check {
			case "model-id":
				b.Model = "invalid\nmodel"
			case "model-check":
				f.answerNode(assistantwire.DeviceLaunchersModelCheck, func(string, json.RawMessage) (any, error) { return nil, errors.New("synthetic model refused") })
			case "effort":
				b.Effort = "unsupported"
			case "model-effort":
				f.agentModels = map[string][]proto.AgentModel{"unseen-custom": {{ID: "test-model", Efforts: []string{"high"}}}}
			case "scope":
				putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "unseen-custom", Agent: "claude", BaseURL: "https://provider.example"}, Share: shareSelected, ScopeMachines: []string{"remote"}})
			}
			body, _ := json.Marshal(map[string]any{"title": "Check", "prompt": "Check files", "repeat": "interval", "every_minutes": 30, "launcher": "unseen-custom", "model": b.Model, "effort": b.Effort, "model_revision": 0})
			if r := request(s, "POST", "/api/v1/assistant/tasks", string(body)); r.Code != http.StatusBadRequest {
				t.Fatalf("invalid override saved: %d %s", r.Code, r.Body.String())
			}
			setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
			setModel(t, s, "task.job", b)
			if _, err := runNow(t, s, "job"); err == nil || f.sent() != 0 {
				t.Fatalf("invalid stored override sent: error=%v sends=%d", err, f.sent())
			}
		})
	}
}

func TestTaskExecutionLegacyCreationKeepsComputerInherited(t *testing.T) {
	for _, fields := range []string{`"launcher":"claude"`, `"effort":"high"`, `"launcher":"claude","effort":"high"`} {
		t.Run(fields, func(t *testing.T) {
			ctx := context.Background()
			f := newFakeHub().withMachine()
			f.sessions["remote"] = nil
			f.hello.Assistant.Machines = []string{"server", "remote"}
			f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}}
			s := newTestService(t, f)
			setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Effort: "high"})
			r := request(s, "POST", "/api/v1/assistant/tasks", `{"title":"Check","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,`+fields+`}`)
			if r.Code != http.StatusOK {
				t.Fatalf("legacy create: %d %s", r.Code, r.Body.String())
			}
			var saved struct{ Task Task }
			if err := json.Unmarshal(r.Body.Bytes(), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Task.ExecutionMachineID != "" {
				t.Errorf("legacy creation pinned the default computer: %+v", saved.Task)
			}
			if record, err := s.modelRecord(ctx, "task."+saved.Task.ID); err != nil || record.Binding != (ModelBinding{}) || record.Revision != 0 {
				t.Fatalf("inherited legacy echo created a model override: %+v %v", record, err)
			}
			setSettings(t, s, func(v *Settings) { v.MachineID = "remote" })
			if got, err := runNow(t, s, saved.Task.ID); err != nil || got.MachineID != "remote" {
				t.Fatalf("task did not follow the new computer: %+v %v", got, err)
			}
			stored, _ := taskByID(s, saved.Task.ID)
			if stored.ExecutionMachineID != "" || s.taskMachine(ctx, stored) != "remote" {
				t.Fatalf("run persisted an inherited computer: %+v", stored)
			}
		})
	}
}

func TestTaskExecutionLegacyExplicitClear(t *testing.T) {
	for _, source := range []string{"", "origin"} {
		for _, field := range []string{"launcher", "effort"} {
			t.Run(source+"/"+field, func(t *testing.T) {
				f := newFakeHub().withMachine()
				s := newTestService(t, f)
				setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Effort: "high"})
				setTasks(s, Task{ID: "job", Title: "Before", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, MachineID: "server", SessionID: source})
				binding := ModelBinding{Effort: "high"}
				if field == "launcher" {
					binding.Provider = "launcher.claude"
				}
				setModel(t, s, "task.job", binding)
				before, _ := s.modelRecord(context.Background(), "task.job")
				body := fmt.Sprintf(`{"title":"After","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"session_id":%q,%q:""}`, source, field)
				if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code != http.StatusOK {
					t.Fatalf("legacy clear: %d %s", r.Code, r.Body.String())
				}
				after, err := s.modelRecord(context.Background(), "task.job")
				task, _ := taskByID(s, "job")
				if err != nil || after.Binding != (ModelBinding{}) || after.Revision != before.Revision+1 || task.ExecutionMachineID != "" || task.SessionID != source {
					t.Fatalf("explicit clear became an echo or pin: task=%+v before=%+v after=%+v error=%v", task, before, after, err)
				}
			})
		}
	}
}
