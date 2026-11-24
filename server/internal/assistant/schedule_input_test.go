package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/promptcontent"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskInputContextUsesDraftExecutionWithoutSaving(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["remote"] = nil
	f.zshrc = map[string][]proto.Launcher{"remote": {{Name: "codex", Agent: "codex"}}}
	f.answerNode("assistant.directory", func(machine string, raw json.RawMessage) (any, error) {
		if machine != "remote" {
			t.Fatalf("directory checked on %s", machine)
		}
		return map[string]any{"cwd": "/work/project"}, nil
	})
	f.answerNode("assistant.content", func(string, json.RawMessage) (any, error) { return map[string]bool{"valid": true}, nil })
	var catalog map[string]any
	f.answerNode("skills.list", func(machine string, raw json.RawMessage) (any, error) {
		if machine != "remote" {
			t.Fatalf("catalog on %s", machine)
		}
		_ = json.Unmarshal(raw, &catalog)
		return assistantskills.Catalog{Version: 1, Complete: true}, nil
	})
	s := newTestService(t, f)
	setTasks(s)
	r := request(s, "POST", "/api/v1/assistant/tasks/input-context", `{"launcher":"codex","model":"test-model","execution_machine_id":"remote","cwd":"/work/project","catalog":true}`)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"task_input":true`) || !strings.Contains(r.Body.String(), `"machine_id":"remote"`) || catalog["cwd"] != "/work/project" || catalog["launcher"] != "codex" {
		t.Fatalf("context: %d %s %+v", r.Code, r.Body.String(), catalog)
	}
	if len(s.tasksView(context.Background(), nil)) != 0 {
		t.Fatal("discovery saved a task")
	}
	b, _ := s.modelRecord(context.Background(), "task.__input_draft")
	if b.Revision != 0 {
		t.Fatal("discovery saved a model override")
	}
}

func TestTaskInputSourceUsesOneSkillEnvironment(t *testing.T) {
	f := readyHub()
	s := newTestService(t, f)
	const (
		machine  = "server"
		source   = "source-chat"
		cwd      = "/work/review"
		launcher = "claude-review"
	)
	f.known[source] = true
	f.setSessions(machine, proto.Session{SessionID: source, Cwd: cwd, Launcher: launcher, Assistant: true})
	f.zshrc = map[string][]proto.Launcher{machine: {{Name: "claude", Agent: "claude"}, {Name: launcher, Agent: "claude"}}}
	task := Task{ID: "review", Title: "Review", Prompt: "Read the notes", Repeat: repeatOnce,
		At: time.Now().Add(time.Hour), Enabled: true, MachineID: machine, SessionID: source}
	setTasks(s, task)

	// The source owns its profile on the node. A task-owned chat without this session
	// would have a different inventory, even if it exposed a skill with the same name.
	profile := t.TempDir()
	catalog := assistantskills.Catalog{Version: 1, Complete: true,
		Skills: assistantskills.Bind(profile, []assistantskills.Entry{{Name: "Review notes", Source: "user"}})}
	readContext := func(node string, raw json.RawMessage) map[string]any {
		t.Helper()
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if node != machine || p["session_id"] != source || p["cwd"] != cwd || p["launcher"] != launcher ||
			p["config_dir"] != nil || p["skill_owner"] != s.skillOwner() {
			t.Fatalf("source skill context differs: %s %v", node, p)
		}
		return p
	}
	contentCalls, catalogCalls := 0, 0
	f.answerNode("assistant.content", func(node string, raw json.RawMessage) (any, error) {
		p := readContext(node, raw)
		if len(p) != 5 {
			t.Errorf("assistant.content received unexpected fields: %v", p)
		}
		if text, _ := p["text"].(string); contentCalls > 0 {
			if _, err := assistantskills.Resolve(text, catalog.Skills); err != nil || text == "" {
				t.Fatalf("save did not validate the selected source skill: %q, %v", text, err)
			}
		}
		contentCalls++
		return map[string]bool{"valid": true}, nil
	})
	f.answerNode("skills.list", func(node string, raw json.RawMessage) (any, error) {
		p := readContext(node, raw)
		if len(p) != 6 || p["catalog"] != true || p["machine_id"] != machine {
			t.Fatalf("catalog received unexpected fields: %v", p)
		}
		catalogCalls++
		return catalog, nil
	})

	w := request(s, "POST", "/api/v1/assistant/tasks/input-context", `{"id":"review","catalog":true}`)
	var preview struct {
		TaskInput bool `json:"task_input"`
		Catalog   assistantskills.Catalog
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); w.Code != 200 || err != nil || !preview.TaskInput || len(preview.Catalog.Skills) != 1 {
		t.Fatalf("preview: %d %s (%v)", w.Code, w.Body, err)
	}
	prompt := `<persona-context kind="skill" ref="` + preview.Catalog.Skills[0].ID + `">Review notes</persona-context>` + "\n\nReview the notes"
	body, _ := json.Marshal(map[string]any{"structured_prompt": prompt})
	if r := request(s, "PUT", "/api/v1/assistant/tasks/review", string(body)); r.Code != 200 {
		t.Fatalf("save: %d %s", r.Code, r.Body)
	}
	if r := request(s, "POST", "/api/v1/assistant/tasks/review/run", "{}"); r.Code != 200 {
		t.Fatalf("run: %d %s", r.Code, r.Body)
	}
	last := f.lastSend(t)
	if last["session_id"] != source || last["launcher"] != launcher || last["config_dir"] != nil ||
		last["task_input"] != true || last["skill_owner"] != s.skillOwner() || contentCalls != 2 || catalogCalls != 2 {
		t.Fatalf("dispatch did not retain the previewed source: %v; content %d, catalog %d", last, contentCalls, catalogCalls)
	}
}

func TestTaskInputContentUsesManagerOriginDecision(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newStorelessService(t, f)
	const machine, source = "server", "inbox-source"
	target := taskInputTarget{TaskExecution: TaskExecution{MachineID: machine, Cwd: "/work", Launcher: "claude"}, session: source}
	for _, tc := range []struct{ stored, expected string }{{OriginInbox, OriginInbox}, {OriginUser, ""}} {
		s.originSet(t.Context(), machine, source, tc.stored, "", "")
		f.answerNode("assistant.content", func(node string, raw json.RawMessage) (any, error) {
			var params map[string]any
			if err := json.Unmarshal(raw, &params); err != nil {
				t.Fatal(err)
			}
			if node != machine || params["session_id"] != source || params["origin"] != tc.expected || params["origin_known"] != true {
				t.Fatalf("content check used a different origin decision: %s %v", node, params)
			}
			return map[string]bool{"valid": true}, nil
		})
		if err := s.checkTaskContent(t.Context(), target, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTaskInputPreviewMatchesSavedCronDestination(t *testing.T) {
	const oldCron = "0 9 * * *"
	const ownDir = "/home/me/.persona/assistant"
	const sourceDir = "/work/source"
	scopes := make(map[string]string)
	for _, tc := range []struct {
		name, repeat string
		cron         *string
		wantRule     string
		wantSession  string
		wantDir      string
	}{
		{"legacy one-off echo", repeatOnce, nil, oldCron, "", ownDir},
		{"explicit one-off", repeatOnce, new(""), "", "origin", sourceDir},
		{"explicit cron update", repeatCron, new("@hourly"), "@hourly", "", ownDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := readyHub()
			f.known["origin"] = true
			f.setSessions("server", proto.Session{SessionID: "origin", Assistant: true, Cwd: sourceDir, Launcher: "claude"})
			f.answerNode("assistant.directory", func(string, json.RawMessage) (any, error) {
				return map[string]string{"cwd": ownDir}, nil
			})
			f.answerNode("skills.list", func(string, json.RawMessage) (any, error) {
				return assistantskills.Catalog{Version: 1, Complete: true, Skills: []assistantskills.Entry{{ID: "review", Name: "Review"}}}, nil
			})
			contentCalls := 0
			f.answerNode("assistant.content", func(_ string, raw json.RawMessage) (any, error) {
				var target struct {
					SessionID string `json:"session_id"`
					Cwd       string `json:"cwd"`
				}
				if err := json.Unmarshal(raw, &target); err != nil {
					return nil, err
				}
				if target.SessionID != tc.wantSession || target.Cwd != tc.wantDir {
					t.Errorf("content check %d resolved session %q and cwd %q, want %q and %q", contentCalls+1, target.SessionID, target.Cwd, tc.wantSession, tc.wantDir)
				}
				contentCalls++
				return map[string]bool{"valid": true}, nil
			})
			s := newTestService(t, f)
			old := Task{ID: "job", Title: "Review", Prompt: "Original", Repeat: repeatCron, Cron: oldCron,
				Enabled: true, MachineID: "server", SessionID: "origin", FreshEachRun: true}
			setTasks(s, old)

			draft := map[string]any{"id": old.ID, "repeat": tc.repeat, "session_id": old.SessionID}
			if tc.cron != nil {
				draft["cron"] = *tc.cron
			}
			body, _ := json.Marshal(draft)
			preview := request(s, "POST", "/api/v1/assistant/tasks/input-context", string(body))
			var context struct {
				TaskInput  bool   `json:"task_input"`
				SkillScope string `json:"skill_scope"`
			}
			if err := json.Unmarshal(preview.Body.Bytes(), &context); err != nil || preview.Code != http.StatusOK ||
				!context.TaskInput || context.SkillScope == "" || contentCalls != 1 {
				t.Fatalf("context preview: %d %s", preview.Code, preview.Body.String())
			}
			scopes[tc.name] = context.SkillScope
			if kept, _ := taskByID(s, old.ID); kept.Repeat != old.Repeat || kept.Cron != old.Cron || kept.Prompt != old.Prompt {
				t.Fatal("preview changed the stored task")
			}

			draft["title"] = old.Title
			draft["at"] = time.Now().Add(time.Hour).UTC()
			draft["enabled"] = true
			draft["structured_prompt"] = `<persona-context kind="skill" ref="review">Review</persona-context>` + "\n\nCheck the notes"
			body, _ = json.Marshal(draft)
			saved := request(s, "PUT", "/api/v1/assistant/tasks/"+old.ID, string(body))
			if saved.Code != http.StatusOK || contentCalls != 2 {
				t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
			}
			task, _ := taskByID(s, old.ID)
			if task.Cron != tc.wantRule || task.continuesSource() != (tc.wantSession != "") || !task.PromptSelections {
				t.Fatalf("saved rule or destination differs from preview: repeat %q, cron %q, continues source %t", task.Repeat, task.Cron, task.continuesSource())
			}

			run := request(s, "POST", "/api/v1/assistant/tasks/"+old.ID+"/run", "{}")
			if run.Code != http.StatusOK {
				t.Fatalf("Run now: %d %s", run.Code, run.Body.String())
			}
			send := f.lastSend(t)
			session, _ := send["session_id"].(string)
			if session != tc.wantSession || send["task_input"] != true {
				t.Fatalf("run used the wrong session or selection mode: session %q, task_input %v", session, send["task_input"])
			}
		})
	}
	if scopes["legacy one-off echo"] != scopes["explicit cron update"] || scopes["explicit one-off"] == scopes["explicit cron update"] {
		t.Fatal("catalog scope must follow the resolved conversation, including when the schedule changes")
	}
}

func TestTaskPromptKeepsSelectionsBeforeScheduleHeader(t *testing.T) {
	block := `<persona-context kind="skill" ref="selected">Review</persona-context>`
	got := taskPrompt(Task{Title: "Review", PromptSelections: true, Prompt: block + "\n\nCheck files", Repeat: repeatInterval, EveryMinutes: 30}, time.Now(), time.Now(), time.UTC, false, false, false)
	refs, err := assistantskills.References(got)
	if err != nil || len(refs) != 1 || refs[0] != "selected" || !strings.Contains(got, "Check files") {
		t.Fatalf("lost selection: %s %v", got, err)
	}
}

func TestTaskInputSaveReopenAndBothRunPaths(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["remote"] = nil
	f.zshrc = map[string][]proto.Launcher{"remote": {{Name: "claude", Agent: "claude"}}}
	s := newTestService(t, f)
	setTasks(s)
	content := 0
	var resumedSession string
	f.answerNode("assistant.content", func(machine string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["text"] == "" {
			return map[string]bool{"valid": true}, nil
		}
		content++
		if machine != "remote" || p["cwd"] != "/work/project" || p["launcher"] != "claude" || p["skill_owner"] != s.skillOwner() || p["config_dir"] != meetingProfile || strings.Contains(p["text"].(string), "Literal reference") {
			t.Fatalf("save context: %s %v", machine, p)
		}
		return map[string]any{"valid": true}, nil
	})
	f.answerNode("skills.list", func(machine string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if machine != "remote" || p["cwd"] != "/work/project" {
			t.Fatalf("run catalog: %s %v", machine, p)
		}
		catalog := assistantskills.Catalog{Version: 1, Complete: true}
		if (p["session_id"] == "" && p["config_dir"] == meetingProfile) || (resumedSession != "" && p["session_id"] == resumedSession) {
			catalog.Skills = []assistantskills.Entry{{ID: "review", Name: "Review"}}
		}
		return catalog, nil
	})
	prompt := `<persona-context kind="skill" ref="review">Review</persona-context>` + "\n" + `<persona-context kind="file" path="notes.txt" machine="remote" cwd="/work/project" launcher="claude" runtime="">Notes</persona-context>` + "\n\n" + `<persona-context kind="skill" ref="literal">Literal reference</persona-context>` + "\nReview files"
	preview := func(payload map[string]any) {
		t.Helper()
		payload["catalog"] = true
		body, _ := json.Marshal(payload)
		got := request(s, "POST", "/api/v1/assistant/tasks/input-context", string(body))
		if got.Code != 200 || !strings.Contains(got.Body.String(), `"id":"review"`) {
			t.Fatalf("profile-specific preview: %d %s", got.Code, got.Body.String())
		}
	}
	preview(map[string]any{"execution_machine_id": "remote", "cwd": "/work/project", "model": "test-model"})
	body, _ := json.Marshal(map[string]any{"title": "Review", "structured_prompt": prompt, "repeat": "interval", "every_minutes": 30, "enabled": true, "execution_machine_id": "remote", "cwd": "/work/project", "model": "test-model"})
	r := request(s, "POST", "/api/v1/assistant/tasks", string(body))
	if r.Code != 200 {
		t.Fatalf("save: %d %s", r.Code, r.Body.String())
	}
	var reply struct{ Task Task }
	_ = json.Unmarshal(r.Body.Bytes(), &reply)
	if reply.Task.Prompt != prompt || !reply.Task.PromptSelections || content != 1 {
		t.Fatalf("save lost references: %+v", reply)
	}
	restarted := newServiceOn(t, f, s.store())
	reopened := restarted.tasksView(context.Background(), nil)
	if len(reopened) != 1 || reopened[0].Prompt != prompt || !reopened[0].PromptSelections {
		t.Fatalf("reopen: %+v", reopened)
	}
	manual := request(s, "POST", "/api/v1/assistant/tasks/"+reply.Task.ID+"/run", "{}")
	if manual.Code != 200 {
		t.Fatal(manual.Body.String())
	}
	var first proto.Session
	err := json.Unmarshal(manual.Body.Bytes(), &first)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.lastSend(t); got["task_input"] != true || got["skill_owner"] != s.skillOwner() || !strings.Contains(got["text"].(string), `path="notes.txt"`) || got["cwd"] != "/work/project" || got["model"] != "test-model" || got["config_dir"] != meetingProfile {
		t.Fatalf("manual start lost selection or execution parameters: %v", got)
	}
	f.setSessions("remote", proto.Session{SessionID: first.SessionID, Assistant: true, Cwd: "/work/project", Launcher: "claude", Agent: "claude"})
	resumedSession = first.SessionID
	preview(map[string]any{"id": reply.Task.ID})
	task, _ := taskByID(s, reply.Task.ID)
	if _, err := s.runTask(task, time.Now(), time.Now(), false, false); err != nil {
		t.Fatal(err)
	}
	if got := f.lastSend(t); got["task_input"] != true || got["skill_owner"] != s.skillOwner() || got["session_id"] != resumedSession || !strings.Contains(got["text"].(string), `ref="review"`) {
		t.Fatalf("resume lost refs: %v", got)
	}
	f.answerNode("assistant.content", func(string, json.RawMessage) (any, error) { return nil, errors.New("reference changed") })
	before, err := s.modelRecord(t.Context(), "task."+reply.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	edit := strings.NewReplacer(`"title":"Review"`, `"title":"Changed"`, `"model":"test-model"`, `"model":"new-model"`).Replace(string(body))
	r = request(s, "PUT", "/api/v1/assistant/tasks/"+reply.Task.ID, edit)
	if r.Code == 200 {
		t.Fatal("stale reference accepted")
	}
	kept, _ := taskByID(s, reply.Task.ID)
	after, err := s.modelRecord(t.Context(), "task."+reply.Task.ID)
	if err != nil || before != after || kept.Prompt != prompt || kept.Title != "Review" {
		t.Fatal("failed save changed task")
	}
}

func TestTaskInputDoesNotGrantUnattendedStarts(t *testing.T) {
	for _, kind := range []string{"file", "skill"} {
		t.Run(kind, func(t *testing.T) {
			task := Task{PromptSelections: true, Prompt: `<persona-context kind="` + kind + `">Selection</persona-context> Read the notes`}
			if task.grantEligible() {
				t.Fatal("mutable reference admitted a clean-round start grant")
			}
			plain := task
			plain.PromptSelections = false
			plain.StartGrant = &StartGrant{Basis: plain.grantBasis(), Kinds: []string{"edit"}}
			task.StartGrant = plain.StartGrant
			updated := []Task{task}
			dropEditedTaskGrants([]Task{plain}, updated)
			if updated[0].StartGrant != nil {
				t.Fatal("authorizing a reference retained its former literal prompt grant")
			}
		})
	}
}

func TestTaskInputOfflineRefusalIsAtomic(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	setTasks(s, Task{ID: "offline", Title: "Plain", Prompt: "Notes", Repeat: repeatInterval, EveryMinutes: 30})
	f.devices = []Device{{ID: "server", Online: false}}
	prompt := `<persona-context kind="file" machine="server" path="notes" cwd="/work" launcher="claude">Notes</persona-context>`
	body, _ := json.Marshal(map[string]any{"title": "Changed", "structured_prompt": prompt, "repeat": "interval", "every_minutes": 30})
	r := request(s, "PUT", "/api/v1/assistant/tasks/offline", string(body))
	task, _ := taskByID(s, "offline")
	if r.Code == 200 || task.Prompt != "Notes" || task.Title != "Plain" || f.sent() != 0 {
		t.Fatalf("offline edit: %d %+v", r.Code, task)
	}
}

func TestTaskInputLiteralPromptsStayLiteral(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	setTasks(s)
	const block = `<persona-context kind="skill" ref="review">Review</persona-context>`
	const prompt = block + "\n\nExplain the example"
	body, _ := json.Marshal(map[string]any{"prompt": prompt, "prompt_selections": true, "repeat": "interval", "every_minutes": 30})
	got := request(s, "POST", "/api/v1/assistant/tasks", string(body))
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	routine, _ := json.Marshal(map[string]any{"title": "Routine", "prompt": prompt, "time": "12:00"})
	if _, err := s.toolScheduleRoutine(t.Context(), "server", "", routine); err != nil {
		t.Fatal(err)
	}
	followup, _ := json.Marshal(map[string]any{"prompt": prompt, "at": "30m"})
	if _, err := s.toolScheduleFollowup(t.Context(), "server", "source", followup); err != nil {
		t.Fatal(err)
	}
	tasks := s.tasksView(t.Context(), nil)
	if len(tasks) != 3 {
		t.Fatalf("expected three independent tasks, got %d", len(tasks))
	}
	for _, task := range tasks {
		msg := taskPrompt(task, time.Now(), time.Now(), time.UTC, true, false, false)
		refs, err := assistantskills.References(msg)
		if task.Prompt != prompt || task.PromptSelections || !strings.Contains(msg, prompt) || err != nil || len(refs) != 0 {
			t.Fatalf("literal task promoted to a selection: %+v %s", task, msg)
		}
	}
	body, _ = json.Marshal(map[string]any{"structured_prompt": "Read the notes", "repeat": "interval", "every_minutes": 30})
	if r := request(s, "POST", "/api/v1/assistant/tasks", string(body)); r.Code != 400 {
		t.Fatalf("structured writes require selections: %d %s", r.Code, r.Body.String())
	}
}

func TestTaskInputDispatchKeepsLiteralFileExample(t *testing.T) {
	const prompt = `<persona-context kind="file" title="Notes" path="notes.txt" machine="server" cwd="/work" launcher="claude">Notes</persona-context>` +
		"\n\nExplain this example from our documentation."

	check := func(t *testing.T, send map[string]any, session string) {
		t.Helper()
		text, _ := send["text"].(string)
		gotSession, _ := send["session_id"].(string)
		if gotSession != session || !strings.Contains(text, prompt) {
			t.Fatalf("literal task content or destination changed: session %q, text %q", gotSession, text)
		}
		files, err := promptcontent.Files(text)
		if err != nil || len(files) != 0 || send["task_input"] != nil || send["skill_owner"] != nil {
			t.Fatalf("literal file example was sent as a selection: task_input %v, skill_owner %v, files %d, error %v",
				send["task_input"], send["skill_owner"], len(files), err)
		}
	}

	t.Run("saved task starts and resumes its own chat", func(t *testing.T) {
		f := newFakeHub().withMachine()
		s := newStorelessService(t, f)
		task := Task{ID: "legacy", Title: "Review", Prompt: prompt, Repeat: repeatDaily, Time: "12:00", Enabled: true}
		setTasks(s, task)
		first, err := runNow(t, s, task.ID)
		if err != nil || first.SessionID == "" {
			t.Fatalf("start: %+v, %v", first, err)
		}
		check(t, f.lastSend(t), "")
		resumed, err := runNow(t, s, task.ID)
		if err != nil || resumed.SessionID != first.SessionID {
			t.Fatalf("resume: %+v, %v", resumed, err)
		}
		check(t, f.lastSend(t), first.SessionID)
	})

	t.Run("tool-authored follow-up continues its source", func(t *testing.T) {
		f := readyHub()
		s := newStorelessService(t, f)
		const source = "source"
		f.known[source] = true
		f.setSessions("server", proto.Session{SessionID: source, Assistant: true, Cwd: "/work", Launcher: "claude"})
		setTasks(s)
		args, _ := json.Marshal(map[string]any{"title": "Review", "prompt": prompt, "at": "30m"})
		if _, err := s.toolScheduleFollowup(t.Context(), "server", source, args); err != nil {
			t.Fatal(err)
		}
		tasks := s.tasksView(t.Context(), nil)
		if len(tasks) != 1 || tasks[0].Prompt != prompt || tasks[0].PromptSelections || tasks[0].SessionID != source {
			t.Fatalf("tool did not save the literal follow-up: %+v", tasks)
		}
		resumed, err := runNow(t, s, tasks[0].ID)
		if err != nil || resumed.SessionID != source {
			t.Fatalf("source: %+v, %v", resumed, err)
		}
		check(t, f.lastSend(t), source)
	})
}

func TestTaskInputToolPromptEditsAreLiteral(t *testing.T) {
	for _, tool := range []string{"followup", "routine"} {
		t.Run(tool, func(t *testing.T) {
			s := newStorelessService(t, newFakeHub())
			const prefix = `<persona-context kind="skill" ref="example">Example</persona-context>`
			task := Task{ID: "job", Title: "Review", Prompt: prefix + "\n\nReview notes", PromptSelections: true, Repeat: repeatDaily, Time: "12:00", Enabled: true}
			setTasks(s, task)
			run := ScheduleRun{Kind: "routine", RefID: task.ID, Title: task.Title, Status: runOK, MachineID: "server", SessionID: "run"}
			s.recordRun(run)
			rendered := taskPrompt(task, time.Now(), time.Now(), time.UTC, false, false, false)
			if !sameText(rendered, task.Prompt) || !copiesRun(run, "", rendered) {
				t.Fatalf("run header no longer identifies its task: %q", rendered)
			}

			metadata, _ := json.Marshal(map[string]any{"id": task.ID, "title": task.Title})
			if _, err := s.toolScheduleRoutine(t.Context(), "server", "run", metadata); err != nil {
				t.Fatal(err)
			}
			kept, _ := taskByID(s, task.ID)
			if kept.Title != task.Title || kept.Prompt != task.Prompt || !kept.PromptSelections {
				t.Fatal("a metadata-only edit removed the owner's selections")
			}

			if tool == "followup" {
				args, _ := json.Marshal(map[string]any{"at": "30m", "prompt": rendered})
				if _, err := s.toolScheduleFollowup(t.Context(), "server", "run", args); err != nil {
					t.Fatal(err)
				}
			} else {
				args, _ := json.Marshal(map[string]any{"id": task.ID, "prompt": task.Prompt})
				if _, err := s.toolScheduleRoutine(t.Context(), "server", "run", args); err != nil {
					t.Fatal(err)
				}
			}
			tasks := s.tasksView(t.Context(), nil)
			if len(tasks) != 1 || tasks[0].ID != task.ID || tasks[0].Prompt != task.Prompt || tasks[0].PromptSelections {
				t.Fatalf("tool write must save literal text on the existing task: %+v", tasks)
			}
		})
	}
}

func TestTaskInputToolCannotChangeApprovedRun(t *testing.T) {
	s := newStorelessService(t, newFakeHub())
	task := Task{ID: "job", Title: "Review", Prompt: "Example selection", PromptSelections: true, Repeat: repeatDaily, Time: "12:00", Enabled: true, Until: time.Now().Add(-time.Hour)}
	setTasks(s, task)
	for _, edit := range []struct {
		field string
		value any
	}{
		{"title", "Updated"},
		{"time", "13:00"},
		{"model", "different-model"},
		{"fresh_each_run", true},
		{"inbox", "every"},
	} {
		t.Run(edit.field, func(t *testing.T) {
			args, _ := json.Marshal(map[string]any{"id": task.ID, edit.field: edit.value})
			if _, err := s.toolScheduleRoutine(t.Context(), "server", "", args); !errors.Is(err, errTaskSelectionsOwner) {
				t.Fatalf("task run settings require owner authorization, got %v", err)
			}
			kept, _ := taskByID(s, task.ID)
			if kept.Title != task.Title || kept.Time != task.Time || !kept.PromptSelections {
				t.Fatal("refused edit changed the task")
			}
		})
	}
	if _, err := s.toolScheduleRoutine(t.Context(), "server", "", json.RawMessage(`{"id":"job","enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	disabled, _ := taskByID(s, task.ID)
	if disabled.Enabled || !disabled.PromptSelections || !disabled.Until.Equal(task.Until) {
		t.Fatal("disabling the task changed the approved run settings")
	}
	if _, err := s.toolScheduleRoutine(t.Context(), "server", "", json.RawMessage(`{"id":"job","enabled":true}`)); !errors.Is(err, errTaskSelectionsOwner) {
		t.Fatalf("reactivation requires owner authorization, got %v", err)
	}
	// This is the last model-write check after the schedule lock was released.
	// The owner may have saved selections in between those operations.
	inherit := ""
	if err := s.setTaskModel(t.Context(), task.ID, &inherit, nil, nil); !errors.Is(err, errTaskSelectionsOwner) {
		t.Fatalf("late model write was not refused: %v", err)
	}
}

func TestTaskInputOwnerMetadataKeepsSelections(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	task := Task{ID: "job", Title: "Review", Prompt: `<persona-context kind="skill" ref="example">Example</persona-context>` + "\n\nReview notes",
		PromptSelections: true, Repeat: repeatDaily, Time: "12:00", Enabled: true}
	setTasks(s, task)
	f.answerNode("assistant.directory", func(string, json.RawMessage) (any, error) {
		return map[string]string{"cwd": "/work/project"}, nil
	})
	f.answerNode("assistant.content", func(string, json.RawMessage) (any, error) { return map[string]bool{"valid": true}, nil })

	updates := []struct {
		name string
		path string
		body any
	}{
		{"existing client", "/api/v1/assistant/routines", map[string]any{"routines": []Task{task}}},
		{"omitted prompt", "/api/v1/assistant/tasks/" + task.ID, map[string]any{"title": "Updated", "repeat": task.Repeat, "time": task.Time, "enabled": true}},
	}
	for _, update := range updates {
		t.Run(update.name, func(t *testing.T) {
			body, _ := json.Marshal(update.body)
			r := request(s, "PUT", update.path, string(body))
			kept, _ := taskByID(s, task.ID)
			if r.Code != 200 || kept.Prompt != task.Prompt || !kept.PromptSelections {
				t.Fatalf("metadata edit lost the owner's selections: %d %s", r.Code, r.Body.String())
			}
		})
	}
}

func TestTaskInputOwnerCanPauseWithUnavailableReferences(t *testing.T) {
	const prompt = `<persona-context kind="skill" ref="review">Review</persona-context>` + "\n\nReview notes"
	const path = "/api/v1/assistant/tasks/job"
	for _, unavailable := range []string{"offline", "stale"} {
		t.Run(unavailable, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			if unavailable == "offline" {
				f.devices = []Device{{ID: "server", Online: false}}
			}
			f.answerNode("assistant.directory", func(string, json.RawMessage) (any, error) {
				return map[string]string{"cwd": "/work"}, nil
			})
			checks := 0
			f.answerNode("assistant.content", func(string, json.RawMessage) (any, error) {
				checks++
				return nil, errors.New("the selected skill was removed")
			})
			task := Task{ID: "job", Title: "Review", Prompt: prompt, PromptSelections: true, Enabled: true,
				Repeat: repeatDaily, Time: "12:00", Weekdays: []int{1, 3}, ExecutionMachineID: "server"}
			for _, edit := range []struct {
				name, path string
				body       any
				enabled    bool
			}{
				{"Web toggle", path, map[string]any{"title": task.Title, "structured_prompt": prompt, "repeat": task.Repeat, "cron": "",
					"session_id": "", "time": task.Time, "weekdays": task.Weekdays, "enabled": false, "every_minutes": 0, "permission_mode": ""}, false},
				{"legacy task client", path, map[string]any{"title": task.Title, "prompt": prompt, "repeat": task.Repeat,
					"time": task.Time, "weekdays": task.Weekdays, "execution_machine_id": task.ExecutionMachineID, "enabled": false}, false},
				{"legacy routines client", "/api/v1/assistant/routines", map[string]any{"routines": []Task{{
					ID: task.ID, Title: task.Title, Prompt: prompt, Time: task.Time, Weekdays: task.Weekdays, Enabled: false,
				}}}, false},
				{"sparse pause", path, map[string]any{"enabled": false}, false},
				{"sparse rename", path, map[string]any{"title": "Renamed"}, true},
			} {
				t.Run(edit.name, func(t *testing.T) {
					setTasks(s, task)
					before := checks
					body, _ := json.Marshal(edit.body)
					r := request(s, "PUT", edit.path, string(body))
					kept, _ := taskByID(s, task.ID)
					if r.Code != 200 || kept.Enabled != edit.enabled || kept.DisabledAt.IsZero() != edit.enabled ||
						kept.Prompt != prompt || !kept.PromptSelections || kept.Repeat != task.Repeat || kept.Time != task.Time ||
						!slices.Equal(kept.Weekdays, task.Weekdays) || kept.ExecutionMachineID != task.ExecutionMachineID || checks != before || f.sent() != 0 {
						t.Fatalf("edit: %d %s; stored %+v; validation calls %d", r.Code, r.Body, kept, checks-before)
					}
					if edit.name == "sparse rename" && kept.Title != "Renamed" {
						t.Fatal("rename was not saved")
					}
				})
			}

			for _, edit := range []struct {
				name string
				body map[string]any
			}{
				{"enable", map[string]any{"enabled": true}},
				{"cadence", map[string]any{"enabled": false, "time": "13:00"}},
				{"new reference", map[string]any{"enabled": false, "structured_prompt": strings.ReplaceAll(prompt, "review", "other")}},
				{"execution context", map[string]any{"enabled": false, "cwd": "/work/new"}},
				{"delivery", map[string]any{"enabled": false, "inbox": "every"}},
			} {
				t.Run(edit.name, func(t *testing.T) {
					old := task
					if edit.name == "enable" {
						old.Enabled = false
					}
					setTasks(s, old)
					before := checks
					body, _ := json.Marshal(edit.body)
					r := request(s, "PUT", path, string(body))
					kept, _ := taskByID(s, task.ID)
					if r.Code == 200 || !reflect.DeepEqual(kept, old) || f.sent() != 0 {
						t.Fatalf("changed run settings with unavailable references: %d %s; stored %+v", r.Code, r.Body, kept)
					}
					if unavailable == "stale" && checks != before+1 {
						t.Fatalf("the stale reference was not validated: %d %s", r.Code, r.Body)
					}
				})
			}
		})
	}
}

func TestTaskInputPauseKeepsSourceConversation(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	f.devices = []Device{{ID: "server", Online: false}}
	setSettings(t, s, func(settings *Settings) { settings.Main = "source" })
	task := Task{ID: "job", Title: "Review", Prompt: `<persona-context kind="skill" ref="review">Review</persona-context>` + "\n\nReview notes",
		PromptSelections: true, Enabled: true, Repeat: repeatDaily, Time: "12:00", MachineID: "server", SessionID: "source"}
	setTasks(s, task)
	if !s.mayBeMain(t.Context(), task.SessionID) {
		t.Fatal("the fixture must be unable to exclude the source from main chat")
	}
	r := request(s, "PUT", "/api/v1/assistant/tasks/job", `{"enabled":false}`)
	kept, _ := taskByID(s, task.ID)
	if r.Code != 200 || kept.Enabled || kept.SessionID != task.SessionID || !kept.PromptSelections {
		t.Fatalf("pause changed the selected task's source: %d %s; stored %+v", r.Code, r.Body, kept)
	}
}

func TestTaskInputOwnerEnableValidatesSavedSelections(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	const prefix = `<persona-context kind="skill" ref="review">Review</persona-context>`
	task := Task{ID: "job", Title: "Review", Prompt: prefix + "\n\nReview notes", PromptSelections: true, Repeat: repeatDaily, Time: "12:00"}
	setTasks(s, task)
	f.answerNode("assistant.directory", func(string, json.RawMessage) (any, error) {
		return map[string]string{"cwd": "/work"}, nil
	})
	checks := 0
	f.answerNode("assistant.content", func(_ string, raw json.RawMessage) (any, error) {
		checks++
		var params map[string]any
		if err := json.Unmarshal(raw, &params); err != nil || params["text"] != prefix {
			t.Fatalf("unexpected validation request: %s (%v)", raw, err)
		}
		return map[string]bool{"valid": true}, nil
	})

	legacy := task
	legacy.Enabled = true
	body, _ := json.Marshal(map[string]any{"routines": []Task{legacy}})
	r := request(s, "PUT", "/api/v1/assistant/routines", string(body))
	kept, _ := taskByID(s, task.ID)
	if r.Code != 400 || kept.Enabled || checks != 0 {
		t.Fatalf("legacy bulk edit changed the approved schedule: %d %s; stored %+v", r.Code, r.Body, kept)
	}

	r = request(s, "PUT", "/api/v1/assistant/tasks/job", `{"enabled":true}`)
	kept, _ = taskByID(s, task.ID)
	if r.Code != 200 || !kept.Enabled || !kept.PromptSelections || kept.Prompt != task.Prompt || checks != 1 || f.sent() != 0 {
		t.Fatalf("enable: %d %s; stored %+v; validation calls %d", r.Code, r.Body, kept, checks)
	}
}

func TestTaskInputLegacyBatchKeepsStoredApproval(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	const approved = `<persona-context kind="skill" ref="review">Review</persona-context>` + "\n\nReview notes"
	task := Task{ID: "review", Title: "Review", Prompt: approved, PromptSelections: true, Repeat: repeatDaily, Time: "12:00", Enabled: true}
	plain := Task{ID: "plain", Title: "Plain", Prompt: "Summarize yesterday", Repeat: repeatDaily, Time: "09:00", Enabled: true}
	setTasks(s, task, plain)
	batch := func(tasks ...Task) int {
		body, _ := json.Marshal(map[string]any{"routines": tasks})
		return request(s, "PUT", "/api/v1/assistant/routines", string(body)).Code
	}

	changed := task
	changed.Prompt = "Summarize the review notes"
	changed.Time = "13:00"
	renamed := plain
	renamed.Title = "Summary"
	status := batch(renamed, changed)
	kept, _ := taskByID(s, task.ID)
	keptPlain, _ := taskByID(s, plain.ID)
	if status != 400 || !reflect.DeepEqual(kept, task) || !reflect.DeepEqual(keptPlain, plain) {
		t.Fatalf("legacy batch changed an approved routine or partially saved: %d", status)
	}

	paused := task
	paused.Title, paused.Enabled = "Daily review", false
	renamed.Prompt = "Summarize today"
	status = batch(renamed, paused)
	kept, _ = taskByID(s, task.ID)
	keptPlain, _ = taskByID(s, plain.ID)
	if status != 200 || kept.Enabled || kept.Title != paused.Title || !kept.PromptSelections || kept.Prompt != approved ||
		keptPlain.Title != renamed.Title || keptPlain.Prompt != renamed.Prompt {
		t.Fatalf("legacy metadata or plain edit failed: %d", status)
	}

	body, _ := json.Marshal(map[string]any{"prompt": changed.Prompt, "time": changed.Time})
	r := request(s, "PUT", "/api/v1/assistant/tasks/review", string(body))
	kept, _ = taskByID(s, task.ID)
	if r.Code != 200 || kept.PromptSelections || kept.Prompt != changed.Prompt || kept.Time != changed.Time || f.sent() != 0 {
		t.Fatalf("single-task plain replacement failed: %d %s", r.Code, r.Body)
	}
}

func TestTaskInputDiscoveryNeedsTargetAcknowledgement(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	setTasks(s)
	f.answerNode("assistant.content", func(string, json.RawMessage) (any, error) { return map[string]bool{}, nil })
	f.answerNode("skills.list", func(string, json.RawMessage) (any, error) {
		t.Fatal("discovery exposed a catalog without scheduled-input support")
		return nil, nil
	})
	for _, catalog := range []bool{false, true} {
		payload, _ := json.Marshal(map[string]any{"catalog": catalog, "cwd": "/work/project"})
		w := request(s, "POST", "/api/v1/assistant/tasks/input-context", string(payload))
		var reply struct {
			Execution  TaskExecution
			TaskInput  *bool  `json:"task_input"`
			InputError string `json:"input_error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); w.Code != 200 || err != nil || reply.TaskInput == nil || *reply.TaskInput || !strings.Contains(reply.InputError, "Persona") || reply.Execution.MachineID != "server" {
			t.Fatalf("discovery did not explain the unavailable picker: %d %s", w.Code, w.Body.String())
		}
	}
	w := request(s, "POST", "/api/v1/assistant/tasks", `{"title":"Ordinary","prompt":"Check notes","repeat":"interval","every_minutes":30}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestScheduledPromptLabelsKeepReferenceOnlyTasksVisible(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	refs := `<persona-context kind="skill" ref="review">Review</persona-context>`
	for _, tc := range []struct {
		name, repeat, title, prompt, label string
		selections                         bool
	}{
		{"interval with only references", repeatInterval, "", refs, "Scheduled task", true},
		{"cron with only references", repeatCron, "", refs + "\n\n  ", "Scheduled task", true},
		{"explicit title wins", repeatInterval, "My review", refs, "My review", true},
		{"selected prompt with text", repeatCron, "", refs + "\n\nReview drafts", "Review drafts", true},
		{"ordinary text", repeatInterval, "", "Check the build", "Check the build", false},
		{"follow-up", repeatOnce, "", refs, "Follow-up", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := Task{ID: "review", Title: tc.title, Repeat: tc.repeat, Enabled: true, Prompt: tc.prompt,
				PromptSelections: tc.selections, EveryMinutes: 30, Cron: "@hourly"}
			if got := task.labelIn(i18n.En); got != tc.label {
				t.Errorf("display label = %q, want %q", got, tc.label)
			}
			label := taskMessageSource(task).Title
			setTasks(s, task)
			if views := s.taskViews(t.Context()); len(views) != 1 || views[0].Label != label {
				t.Errorf("task snapshot does not carry run label %q: %+v", label, views)
			}
			now := time.Now()
			header := taskPrompt(task, now, now, time.UTC, false, false, false)
			if label == "" || !strings.Contains(header, label) {
				t.Errorf("schedule header %q does not carry run label %q", header, label)
			}
		})
	}
}
