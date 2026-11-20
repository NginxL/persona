package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskExecutionSaveAndRun(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["remote"] = nil
	f.zshrc = map[string][]proto.Launcher{"remote": {{Name: "codex", Agent: "codex"}}}
	f.answerNode("assistant.directory", func(machine string, raw json.RawMessage) (any, error) {
		if machine != "remote" {
			t.Errorf("directory checked on %s", machine)
		}
		return map[string]any{"cwd": "/work/project"}, nil
	})
	s := newTestService(t, f)
	body := `{"title":"Check files","prompt":"Summarize files","repeat":"interval","every_minutes":30,"enabled":true,"launcher":"codex","model":"test-model","execution_machine_id":"remote","cwd":"/work/project"}`
	r := request(s, "POST", "/api/v1/assistant/tasks", body)
	if r.Code != http.StatusOK {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	var got struct{ Task map[string]any }
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Task["launcher"] != "codex" || got.Task["model"] != "test-model" || got.Task["cwd"] != "/work/project" {
		t.Fatalf("saved configuration: %v", got.Task)
	}
	id := got.Task["id"].(string)
	if b, _ := s.resolveModel(context.Background(), "assistant"); b.Provider != "launcher.claude" {
		t.Fatalf("main changed: %+v", b)
	}
	first, err := runNow(t, s, id)
	if err != nil {
		t.Fatal(err)
	}
	if first.MachineID != "remote" {
		t.Fatalf("started on %s", first.MachineID)
	}
	p := f.lastSend(t)
	if p["cwd"] != "/work/project" || p["launcher"] != "codex" || p["model"] != "test-model" {
		t.Fatalf("execution: %v", p)
	}
	runs := waitRuns(t, s, 1)
	raw, _ := json.Marshal(runs[0])
	var history map[string]any
	_ = json.Unmarshal(raw, &history)
	execution, _ := history["execution"].(map[string]any)
	if execution["cwd"] != "/work/project" || execution["model"] != "test-model" {
		t.Fatalf("history: %s", raw)
	}
	// Omitted fields from an old client keep the configuration.
	r = request(s, "PUT", "/api/v1/assistant/tasks/"+id, `{"title":"Changed title","prompt":"Summarize files","repeat":"interval","every_minutes":30,"enabled":true}`)
	if r.Code != http.StatusOK {
		t.Fatalf("old edit: %s", r.Body.String())
	}
	second, err := runNow(t, s, id)
	if err != nil || second.SessionID != first.SessionID {
		t.Fatalf("compatible resume: %+v %v", second, err)
	}
	// A directory change starts a new conversation and keeps the old run's evidence.
	f.answerNode("assistant.directory", func(string, json.RawMessage) (any, error) { return map[string]any{"cwd": "/work/other"}, nil })
	r = request(s, "PUT", "/api/v1/assistant/tasks/"+id, `{"title":"Changed title","prompt":"Summarize files","repeat":"interval","every_minutes":30,"enabled":true,"cwd":"/work/other"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("directory edit: %s", r.Body.String())
	}
	r = request(s, "POST", "/api/v1/assistant/tasks/"+id+"/run", `{}`)
	if r.Code != http.StatusOK {
		t.Fatalf("manual run: %s", r.Body.String())
	}
	var third struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &third); err != nil {
		t.Fatal(err)
	}
	if third.SessionID == first.SessionID {
		t.Fatalf("manual run reused incompatible conversation: %+v", third)
	}
	historyRuns := waitRuns(t, s, 3)
	if !historyRuns[2].Manual || historyRuns[2].Execution.Cwd != "/work/other" {
		t.Fatalf("manual configuration absent: %+v", historyRuns[2])
	}
}

func TestTaskExecutionUnavailableDoesNotSend(t *testing.T) {
	for _, failure := range []string{"directory", "model", "offline"} {
		t.Run(failure, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
			r := request(s, "PUT", "/api/v1/assistant/tasks/job", `{"title":"Check","prompt":"Check","repeat":"interval","every_minutes":30,"enabled":true,"cwd":"/work/project"}`)
			// The fake validates the directory without touching this machine's filesystem.
			if r.Code != http.StatusOK {
				t.Fatalf("save: %s", r.Body.String())
			}
			switch failure {
			case "directory":
				f.answerNode("assistant.directory", func(string, json.RawMessage) (any, error) { return nil, errors.New("directory missing") })
			case "model":
				setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Model: "missing-model"})
				f.answerNode("launchers.model.check", func(string, json.RawMessage) (any, error) { return nil, errors.New("model unavailable") })
			case "offline":
				f.devices = []Device{{ID: "server", Online: false}}
			}
			before := f.sent()
			if _, err := runNow(t, s, "job"); err == nil {
				t.Fatal("unavailable configuration ran")
			}
			if f.sent() != before {
				t.Fatal("sent with unavailable configuration")
			}
		})
	}
}

func TestTaskExecutionSaveIsAtomicAndRevisionChecked(t *testing.T) {
	f := newFakeHub().withMachine()
	st := newSettingsFaultStore()
	s := newServiceOn(t, f, st)
	setTasks(s, Task{ID: "job", Title: "Before", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	body := `{"title":"After","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"model":"test-model","model_revision":0,"cwd":"/work/project"}`
	st.failBatch = tasksKey
	if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code == http.StatusOK {
		t.Fatal("failed transaction was accepted")
	}
	task, _ := taskByID(s, "job")
	record, err := s.modelRecord(context.Background(), "task.job")
	if err != nil || task.Title != "Before" || task.Cwd != "" || task.ExecutionMachineID != "" || record.Revision != 0 {
		t.Fatalf("partial save: %+v %+v %v", task, record, err)
	}
	st.failBatch = ""
	if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code != http.StatusOK {
		t.Fatalf("save: %s", r.Body.String())
	}
	if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code != http.StatusConflict {
		t.Fatalf("stale edit: %d %s", r.Code, r.Body.String())
	}
	// Reconstruct the service to prove both settings survive a restart.
	restarted := newServiceOn(t, f, st)
	view, ok := restarted.taskView(context.Background(), "job")
	if !ok || view.Model != "test-model" || view.Launcher != "claude" || view.Cwd != "/work/project" || view.ExecutionMachineID != "server" || view.ModelRevision != 1 {
		t.Fatalf("restart: %+v", view)
	}
}

func TestTaskExecutionRevisionOnlyEditRejectsStaleSave(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	setTasks(s, Task{ID: "job", Title: "Before", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Model: "test-model"})
	before := modelRecordOf(t, s, "task.job")
	edit := func(revision int64) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"title":"After","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"model_revision":%d}`, revision)
		return request(s, "PUT", "/api/v1/assistant/tasks/job", body)
	}
	if r := edit(before.Revision - 1); r.Code != http.StatusConflict {
		t.Fatalf("stale revision-only edit: %d %s", r.Code, r.Body)
	}
	if task, _ := taskByID(s, "job"); task.Title != "Before" {
		t.Fatalf("stale edit saved the title: %+v", task)
	}
	if r := edit(before.Revision); r.Code != http.StatusOK {
		t.Fatalf("current revision-only edit: %d %s", r.Code, r.Body)
	}
	if task, _ := taskByID(s, "job"); task.Title != "After" {
		t.Fatalf("current edit did not save the title: %+v", task)
	}
	if after := modelRecordOf(t, s, "task.job"); after != before {
		t.Fatalf("title edit changed the model record: before=%+v after=%+v", before, after)
	}
}

type taskDirectoryWaitHub struct {
	*fakeHub
	entered chan struct{}
	release chan struct{}
}

type taskEditReadKey struct{}

type taskEditReadStore struct {
	*memStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (st *taskEditReadStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if ctx.Value(taskEditReadKey{}) == true && key == modelRecordKey("task.job") {
		st.once.Do(func() { close(st.entered) })
		select {
		case <-st.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return st.memStore.Setting(ctx, key, out)
}

func TestTaskExecutionLegacyEditMergesConcurrentRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{
		{"runs while editing", http.StatusOK},
		{"deleted while editing", http.StatusNotFound},
		{"retimed while editing", http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := readyHub()
			st := &taskEditReadStore{memStore: &memStore{}, entered: make(chan struct{}), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(st.release) })
			defer release()
			s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)}))
			now := time.Now().UTC()
			setTasks(s, Task{ID: "job", Title: "Before", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, At: now})
			const path = "/api/v1/assistant/tasks/job"
			r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"title":"Updated","prompt":"New prompt","repeat":"interval","every_minutes":30,"enabled":true}`))
			r = r.WithContext(context.WithValue(r.Context(), taskEditReadKey{}, true))
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { s.ServeHTTP(response, r); close(done) }()
			select {
			case <-st.entered:
			case <-done:
				t.Fatalf("edit returned before its snapshot was held: %d %s", response.Code, response.Body)
			case <-time.After(3 * time.Second):
				t.Fatal("edit did not reach the model record")
			}

			switch tc.name {
			case "runs while editing":
				s.runDue(t.Context(), now)
				s.sched.inflight.Wait()
				if run := waitRuns(t, s, 1)[0]; run.Status != runOK || run.Manual || run.SessionID == "" {
					t.Fatalf("concurrent due run: %+v", run)
				}
			case "deleted while editing":
				if deleted := request(s, "DELETE", path, ""); deleted.Code != http.StatusOK {
					t.Fatalf("delete: %d %s", deleted.Code, deleted.Body)
				}
			case "retimed while editing":
				body := fmt.Sprintf(`{"title":"Before","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"at":%q}`, now.Add(5*time.Minute).Format(time.RFC3339))
				if retimed := request(s, "PUT", path, body); retimed.Code != http.StatusOK {
					t.Fatalf("retime: %d %s", retimed.Code, retimed.Body)
				}
			}
			beforeRelease, exists := taskByID(s, "job")
			release()
			<-done
			if response.Code != tc.want {
				t.Fatalf("edit: got %d, want %d: %s", response.Code, tc.want, response.Body)
			}
			after, stillExists := taskByID(s, "job")
			if exists != stillExists {
				t.Fatalf("edit changed task existence: %+v", after)
			}
			if tc.want == http.StatusOK {
				if after.Title != "Updated" || after.Prompt != "New prompt" || after.RunExecution == nil ||
					after.RunSessionID == "" || after.LastSessionID != after.RunSessionID || !after.LastRun.Equal(now) ||
					!after.At.Equal(now.Add(30*time.Minute)) {
					t.Fatalf("edit lost the schedule or run: %+v", after)
				}
				beforeRelease.Title, beforeRelease.Prompt = after.Title, after.Prompt
			}
			if stillExists && !reflect.DeepEqual(beforeRelease, after) {
				t.Fatalf("edit lost concurrent task state: before=%+v after=%+v", beforeRelease, after)
			}
		})
	}
}

func (h taskDirectoryWaitHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	if call.Method == assistantwire.DeviceAssistantDirectory {
		close(h.entered)
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.fakeHub.DeviceCall(ctx, call)
}

func TestTaskExecutionSaveAllowsConcurrentScheduleActivity(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		want               int
	}{
		{"create", "POST", "/api/v1/assistant/tasks", http.StatusOK},
		{"same task runs", "PUT", "/api/v1/assistant/tasks/job", http.StatusOK},
		{"task changed", "PUT", "/api/v1/assistant/tasks/job", http.StatusConflict},
		{"model changed", "PUT", "/api/v1/assistant/tasks/job", http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			h := taskDirectoryWaitHub{f, make(chan struct{}), make(chan struct{})}
			st := &memStore{}
			s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return st }, Logf: testLogf(t)}))
			now := time.Now()
			jobRuns := tc.name == "same task runs"
			setTasks(s,
				Task{ID: "job", Title: "Before", Prompt: "Check", Repeat: repeatInterval, EveryMinutes: 30, Enabled: jobRuns, At: now},
				Task{ID: "manual", Title: "Manual", Prompt: "Check", Repeat: repeatInterval, EveryMinutes: 30},
				Task{ID: "due", Title: "Due", Prompt: "Check", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, At: now})
			body := fmt.Sprintf(`{"title":"Slow edit","prompt":"Check","repeat":"interval","every_minutes":30,"enabled":%t,"cwd":"/work/project","model":"slow-model","model_revision":0}`, jobRuns)
			slow := make(chan *httptest.ResponseRecorder, 1)
			go func() { slow <- request(s, tc.method, tc.path, body) }()
			release := sync.OnceFunc(func() { close(h.release) })
			defer release()
			select {
			case <-h.entered:
			case r := <-slow:
				t.Fatalf("save ended before node validation: %d %s", r.Code, r.Body)
			case <-time.After(3 * time.Second):
				t.Fatal("save did not reach the node")
			}
			progress := make(chan error, 1)
			go func() {
				if r := request(s, "GET", "/api/v1/assistant/tasks", ""); r.Code != http.StatusOK {
					progress <- fmt.Errorf("task list: %d %s", r.Code, r.Body)
					return
				}
				if _, err := runNow(t, s, "manual"); err != nil {
					progress <- err
					return
				}
				s.runDue(context.Background(), now)
				s.sched.inflight.Wait()
				wantSends := 2
				if jobRuns {
					wantSends++
				}
				if f.sent() != wantSends {
					progress <- fmt.Errorf("manual and due tasks did not both send: %d sends", f.sent())
					return
				}
				switch tc.name {
				case "task changed":
					r := request(s, "PUT", tc.path, `{"title":"Concurrent","prompt":"Check","repeat":"interval","every_minutes":30}`)
					if r.Code != http.StatusOK {
						progress <- fmt.Errorf("concurrent edit: %d %s", r.Code, r.Body)
						return
					}
				case "model changed":
					if err := s.saveModel(context.Background(), "task.job", modelRecord{Binding: ModelBinding{Provider: "launcher.claude", Model: "concurrent-model"}}); err != nil {
						progress <- err
						return
					}
				}
				progress <- nil
			}()
			select {
			case err := <-progress:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				release()
				<-progress
				<-slow
				t.Fatal("schedule activity waited for the task edit's node validation")
			}
			release()
			r := <-slow
			if r.Code != tc.want {
				t.Fatalf("slow save: %d %s", r.Code, r.Body)
			}
			task, _ := taskByID(s, "job")
			model := modelRecordOf(t, s, "task.job")
			if jobRuns {
				if task.Title != "Slow edit" || task.Cwd != "/work/project" || model.Binding.Model != "slow-model" ||
					model.Revision != 1 || task.RunExecution == nil || task.LastSessionID != task.RunSessionID ||
					!task.At.Equal(now.Add(30*time.Minute)) {
					t.Fatalf("validated edit lost scheduling state or settings: %+v %+v", task, model)
				}
				return
			}
			wantTitle, wantRevision := "Before", int64(0)
			if tc.name == "task changed" {
				wantTitle = "Concurrent"
			}
			if tc.name == "model changed" {
				wantRevision = 1
				if model.Binding.Model != "concurrent-model" {
					t.Fatalf("lost concurrent model: %+v", model)
				}
			}
			if task.Title != wantTitle || task.Cwd != "" || model.Revision != wantRevision {
				t.Fatalf("stale edit changed persisted state: %+v %+v", task, model)
			}
		})
	}
}

func TestTaskExecutionModelAndComputerChangesStartFresh(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["remote"] = nil
	s := newTestService(t, f)
	setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	first, err := runNow(t, s, "job")
	if err != nil {
		t.Fatal(err)
	}
	setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Model: "test-model"})
	second, err := runNow(t, s, "job")
	if err != nil || first.SessionID == second.SessionID {
		t.Fatalf("model change resumed: %+v %v", second, err)
	}
	r := request(s, "PUT", "/api/v1/assistant/tasks/job", `{"title":"Check","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"execution_machine_id":"remote"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("computer edit: %s", r.Body.String())
	}
	third, err := runNow(t, s, "job")
	if err != nil || third.SessionID == second.SessionID || third.MachineID != "remote" {
		t.Fatalf("computer change: %+v %v", third, err)
	}
	runs := waitRuns(t, s, 3)
	if runs[1].ConversationReason != "execution_changed" || runs[2].ConversationReason != "execution_changed" {
		t.Fatalf("context transitions absent from history: %+v", runs)
	}
}

func TestTaskExecutionChecksAdvertisedModelEffort(t *testing.T) {
	f := newFakeHub().withMachine()
	f.agentModels = map[string][]proto.AgentModel{"claude": {{ID: "test-model", Efforts: []string{"low"}}}}
	s := newTestService(t, f)
	body := `{"title":"Check","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"model":"test-model","effort":"high"}`
	if r := request(s, "POST", "/api/v1/assistant/tasks", body); r.Code != http.StatusBadRequest {
		t.Fatalf("unsupported effort saved: %d %s", r.Code, r.Body.String())
	}
}

func TestTaskExecutionLegacySDKContextStartsFreshCLI(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}}
	st := newSettingsFaultStore()
	s := newServiceOn(t, f, st)
	setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	first, err := runNow(t, s, "job")
	if err != nil {
		t.Fatal(err)
	}
	waitRuns(t, s, 1)
	stored, _ := taskByID(s, "job")
	if stored.RunExecution == nil {
		t.Fatal("missing execution snapshot")
	}
	stored.RunExecution.Runtime = "agentsdk" // an older task's saved execution context
	if err := st.PutSetting(ctx, tasksKey, []Task{stored}); err != nil {
		t.Fatal(err)
	}
	restarted := newServiceOn(t, f, st)
	second, err := runNow(t, restarted, "job")
	if err != nil || second.SessionID == first.SessionID {
		t.Fatalf("legacy SDK task context resumed: %+v %v", second, err)
	}
	runs := waitRuns(t, restarted, 2)
	if runs[1].ConversationReason != "execution_changed" || runs[1].Execution == nil || runs[1].Execution.Runtime != "" {
		t.Fatalf("runtime transition missing from history: %+v", runs)
	}
	if _, sent := f.lastSend(t)["runtime"]; sent {
		t.Fatal("new task sent a removed Claude runtime choice")
	}
	r := request(restarted, "GET", "/api/v1/machines/server/launchers?purpose=task", "")
	var catalog struct {
		Default map[string]any `json:"default"`
	}
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &catalog) != nil || catalog.Default == nil {
		t.Fatalf("task defaults: %d %s", r.Code, r.Body)
	}
	if _, offered := catalog.Default["runtime"]; offered {
		t.Fatal("task defaults still offer a removed Claude runtime choice")
	}
}

func TestTaskExecutionLegacyDefaultsRemainInherited(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	st := newSettingsFaultStore()
	// Persist a pre-configuration record with none of the new execution fields.
	legacy := []map[string]any{{"id": "legacy", "title": "Old task", "prompt": "Check files", "repeat": "interval", "every_minutes": 30, "enabled": true}}
	if err := st.PutSetting(ctx, tasksKey, legacy); err != nil {
		t.Fatal(err)
	}
	s := newServiceOn(t, f, st)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "inherited-model", Effort: "low"})
	main, err := s.resolveModel(ctx, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	view, ok := s.taskView(ctx, "legacy")
	if !ok || view.Execution == nil || view.Execution.Launcher != "claude" || view.Execution.Model != main.Model || view.Execution.Effort != main.Effort {
		t.Fatalf("legacy defaults: %+v", view)
	}
	r := request(s, "PUT", "/api/v1/assistant/tasks/legacy", `{"title":"Edited old task","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true}`)
	if r.Code != http.StatusOK {
		t.Fatalf("legacy edit: %s", r.Body.String())
	}
	var override modelRecord
	if exists, err := st.Setting(ctx, modelRecordKey("task.legacy"), &override); err != nil || exists {
		t.Fatalf("legacy edit created an override: %v %v %+v", exists, err, override)
	}
	first, err := runNow(t, s, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	waitRuns(t, s, 1)
	// An older stored conversation has no execution snapshot; defaults still resume it after a restart.
	stored, _ := taskByID(s, "legacy")
	stored.RunExecution = nil
	if err := st.PutSetting(ctx, tasksKey, []Task{stored}); err != nil {
		t.Fatal(err)
	}
	restarted := newServiceOn(t, f, st)
	second, err := runNow(t, restarted, "legacy")
	if err != nil || second.SessionID != first.SessionID {
		t.Fatalf("legacy reuse: %+v %v", second, err)
	}
	if got := f.lastSend(t); got["launcher"] != "claude" || got["model"] != "inherited-model" || got["effort"] != "low" || got["resume_only"] != true {
		t.Fatalf("legacy execution: %v", got)
	}
}

func TestTaskExecutionLegacyExplicitChoicesStartFresh(t *testing.T) {
	for _, tc := range []struct {
		name, machine, cwd string
		binding            ModelBinding
	}{
		{name: "backend", binding: ModelBinding{Provider: "launcher.claude"}},
		{name: "model", binding: ModelBinding{Model: "explicit-model"}},
		{name: "effort", binding: ModelBinding{Effort: "low"}},
		{name: "computer", machine: "server"},
		{name: "directory", cwd: "/work/project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.known["legacy-chat"] = true
			s := newTestService(t, f)
			setTasks(s, Task{ID: "legacy", Title: "Old task", Prompt: "Check", Repeat: repeatInterval, EveryMinutes: 30,
				RunSessionID: "legacy-chat", RunMachineID: "server", RunLauncher: "claude", ExecutionMachineID: tc.machine, Cwd: tc.cwd})
			if tc.binding != (ModelBinding{}) {
				setModel(t, s, "task.legacy", tc.binding)
			}
			session, err := runNow(t, s, "legacy")
			if err != nil || session.SessionID == "legacy-chat" || session.SessionID == "" {
				t.Fatalf("explicit choice reused legacy conversation: %+v %v", session, err)
			}
			if run := waitRuns(t, s, 1)[0]; run.ConversationReason != "legacy_context" || run.Conversation != "new" {
				t.Fatalf("legacy decision missing from history: %+v", run)
			}
		})
	}
}
