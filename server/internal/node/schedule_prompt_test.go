package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The in-process hub supplies the single test machine and routes device calls to
// the real node handler. No model or hub service is needed by the Claude fixture.
type schedulePromptHub struct {
	node    *Node
	offline bool
	locale  i18n.Lang
}

func (h schedulePromptHub) Hello(context.Context) (assistant.Hello, error) {
	id := h.node.machineID
	return assistant.Hello{Assistant: assistant.Placement{MachineID: id, Machines: []string{id}, Launcher: "claude"}, Locale: string(h.locale)}, nil
}

func (h schedulePromptHub) DeviceCall(ctx context.Context, p assistant.DeviceCall) (json.RawMessage, error) {
	if p.Device == assistant.DeviceAll && p.Method == assistant.DeviceDevicesList {
		d := assistant.Device{ID: h.node.machineID, Name: "Fixture", Home: h.node.home, DataDir: h.node.dir,
			Online: !h.offline, Reachable: !h.offline, ModelConfiguration: true}
		return json.Marshal(map[string]any{"devices": []assistant.Device{d}})
	}
	if p.Device != h.node.machineID {
		return nil, fmt.Errorf("test machine does not exist: %s", p.Device)
	}
	// The hub normally answers this from the node's session inventory.
	if p.Method == assistant.DeviceSessionGet {
		var req struct {
			ID string `json:"session_id"`
		}
		if err := json.Unmarshal(p.Params, &req); err != nil {
			return nil, err
		}
		for _, session := range h.node.sessionList(ctx, true) {
			if session.SessionID == req.ID {
				session.Assistant = h.node.isAssistantSession(req.ID)
				return json.Marshal(map[string]any{"ok": true, "session": session})
			}
		}
		return json.RawMessage(`{"ok":false}`), nil
	}
	result, err := h.node.handle(ctx, p.Method, p.Params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (schedulePromptHub) CredentialRevoked(context.Context, string) (bool, error) { return false, nil }
func (schedulePromptHub) AppCall(context.Context, assistant.AppCall) (json.RawMessage, error) {
	return json.RawMessage("{}"), nil
}
func (schedulePromptHub) NotifyRequest(context.Context, assistant.NotifyRequest) (assistant.NotifyResult, error) {
	return assistant.NotifyResult{}, nil
}
func (schedulePromptHub) Broadcast(context.Context, assistant.Broadcast) {}
func (schedulePromptHub) Snapshot(context.Context, assistant.Snapshot) (assistant.SnapshotResult, error) {
	return assistant.SnapshotResult{}, nil
}
func (schedulePromptHub) InboxChanged(context.Context, assistantwire.InboxChanged) {}
func (schedulePromptHub) Tell(string, any) error                                   { return nil }

func TestScheduledPromptDeliveryThroughNode(t *testing.T) {
	f := cliTestBash(t)
	f.n.machineID = "fixture"
	cwd := t.TempDir()
	name := "Q&A [review] 資料.txt"
	file := filepath.Join(cwd, name)
	selection := fmt.Sprintf(`<persona-context kind="file" path="%s" machine="%s" cwd="%s" launcher="claude">Notes</persona-context>`,
		html.EscapeString(name), f.n.machineID, html.EscapeString(cwd))
	const notes = "Explain this example from our documentation."
	literal := selection + "\n\n" + notes
	service := assistant.New(assistant.Options{Hub: schedulePromptHub{node: f.n}})

	request := func(method, route string, body any, result any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, route, bytes.NewReader(raw)).WithContext(t.Context())
		service.ServeHTTP(w, r)
		if err := json.Unmarshal(w.Body.Bytes(), result); w.Code != http.StatusOK || err != nil {
			t.Fatalf("%s %s: %d %s (%v)", method, route, w.Code, w.Body, err)
		}
	}
	create := func(field, prompt string) string {
		t.Helper()
		var res struct{ Task assistant.Task }
		request(http.MethodPost, "/api/v1/assistant/tasks", map[string]any{
			"title": "Review", "repeat": "daily", "time": "09:00", "enabled": true, "cwd": cwd, field: prompt,
		}, &res)
		if res.Task.ID == "" || res.Task.Prompt != prompt || res.Task.PromptSelections != (field == "structured_prompt") {
			t.Fatalf("saved task lost prompt provenance: %+v", res.Task)
		}
		return res.Task.ID
	}
	run := func(taskID string) string {
		t.Helper()
		var res struct {
			SessionID string `json:"session_id"`
		}
		request(http.MethodPost, "/api/v1/assistant/tasks/"+taskID+"/run", nil, &res)
		if res.SessionID == "" {
			t.Fatal("task did not run")
		}
		f.idle(t, res.SessionID)
		return res.SessionID
	}
	input := func(want int) string {
		t.Helper()
		in := emptyStartUserInputs(t, f.file("stdin.jsonl"))
		if len(in) != want {
			t.Fatalf("got %d turns, want %d", len(in), want)
		}
		var text strings.Builder
		for _, content := range in[want-1].Message.Content {
			text.WriteString(content.Text)
		}
		return text.String()
	}

	task := create("prompt", literal)
	sid := run(task)
	if text := input(1); !strings.Contains(text, literal) || strings.Contains(text, "file-instructions") {
		t.Fatalf("scheduled start lost the literal example or added file instructions: %q", text)
	}
	if resumed := run(task); resumed != sid {
		t.Fatalf("scheduled resume started a new conversation: %q, want %q", resumed, sid)
	}
	if text := input(2); !strings.Contains(text, literal) || strings.Contains(text, "file-instructions") {
		t.Fatalf("scheduled resume lost the literal example or added file instructions: %q", text)
	}

	followupPrompt := selection + "\n\nExplain the file example in a follow-up note."
	args, err := json.Marshal(map[string]string{"at": "30m", "title": "Review example", "prompt": followupPrompt})
	if err != nil {
		t.Fatal(err)
	}
	if result := service.Tool(t.Context(), assistant.ToolCall{
		Name: "schedule_followup", Arguments: args, MachineID: f.n.machineID, SessionID: sid,
	}); result.IsError {
		t.Fatalf("schedule follow-up: %+v", result)
	}
	var list struct{ Followups []assistant.Task }
	request(http.MethodGet, "/api/v1/assistant/followups", nil, &list)
	if len(list.Followups) != 1 || list.Followups[0].Prompt != followupPrompt || list.Followups[0].PromptSelections {
		t.Fatalf("tool did not save a literal follow-up: %+v", list)
	}
	if continued := run(list.Followups[0].ID); continued != sid {
		t.Fatalf("follow-up started a new conversation: %q, want %q", continued, sid)
	}
	if text := input(3); !strings.Contains(text, followupPrompt) || strings.Contains(text, "file-instructions") {
		t.Fatalf("follow-up lost the literal example or added file instructions: %q", text)
	}

	// The documentation example needed no file. An approved selection
	// uses the fixture on disk and receives the corresponding instructions.
	writeFile(t, file, "Synthetic meeting notes")
	selectedPrompt := selection + "\n\nRead the selected notes."
	turns := 3
	for _, locale := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		service = assistant.New(assistant.Options{Hub: schedulePromptHub{node: f.n, locale: locale}})
		run(create("structured_prompt", selectedPrompt))
		turns++
		text := input(turns)
		start := strings.Index(text, `<persona-context kind="file-instructions">`)
		physical := resolved(t, file)
		if start < 0 || !strings.Contains(text[start:], physical) {
			t.Fatalf("the selected task did not deliver the exact file path: %q", text)
		}
		goldenNode(t, "selected_files."+string(locale), strings.ReplaceAll(text[start:], filepath.Dir(physical), "/fixture"))
	}

	for _, tc := range []struct {
		name    string
		options map[string]any
	}{
		{"ordinary chat", nil},
		{"guest", map[string]any{"guest": map[string]any{"tools": []string{"lark_agent_reply"}}}},
		{"public", map[string]any{"public": true}},
		{"Inbox", map[string]any{"origin": originInbox, "origin_known": true}},
	} {
		t.Run(tc.name+" refuses a standalone file selection", func(t *testing.T) {
			params := map[string]any{"cwd": cwd, "launcher": "claude", "text": selectedPrompt}
			for key, value := range tc.options {
				params[key] = value
			}
			_, err := nodeCall(t, f.n, "assistant.send", params)
			var refusal *rpc.Error
			if !errors.As(err, &refusal) || refusal.Code != "file_unavailable" {
				t.Fatalf("file selection was not refused: %v", err)
			}
			input(turns)
		})
	}
}

func TestScheduledPromptFirstUseOfDefaultDirectory(t *testing.T) {
	f := cliTestBash(t)
	// The preview checks "claude" against the launchers the node lists, which the package caches for a minute. A test
	// before this one in the same process may have listed another shell's (TestOrcaResumesAnOfflineSession lists the
	// machine's own, which has no plain claude): list this fixture's shell instead.
	launcherCache.Lock()
	at, list := launcherCache.at, launcherCache.list
	codex, aliases, settings, scripts := launcherCache.codex, launcherCache.codexAliases, launcherCache.settings, launcherCache.scripts
	launcherCache.at, launcherCache.list = time.Time{}, nil
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = at, list
		launcherCache.codex, launcherCache.codexAliases, launcherCache.settings, launcherCache.scripts = codex, aliases, settings, scripts
		launcherCache.Unlock()
	})
	f.n.machineID = "remote-fixture"
	hub := &schedulePromptHub{node: f.n}
	service := assistant.New(assistant.Options{Hub: hub})
	cwd := filepath.Join(resolved(t, f.n.dir), "assistant")
	skillDir := filepath.Join(f.n.home, ".claude", "skills", "fixture-review")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "---\nname: fixture-review\ndescription: Review fixture notes\n---\nRead the notes.\n")

	call := func(method, route string, fields map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(method, route, bytes.NewReader(raw)).WithContext(t.Context()))
		return rec
	}
	preview := func(directory string) *httptest.ResponseRecorder {
		t.Helper()
		return call(http.MethodPost, "/api/v1/assistant/tasks/input-context", map[string]any{
			"execution_machine_id": f.n.machineID, "launcher": "claude", "cwd": directory, "catalog": true,
		})
	}
	decode := func(rec *httptest.ResponseRecorder, out any) {
		t.Helper()
		if err := json.Unmarshal(rec.Body.Bytes(), out); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("%d %s (%v)", rec.Code, rec.Body, err)
		}
	}
	assertMissing := func(dir string) {
		t.Helper()
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("directory %q should not be created: %v", dir, err)
		}
	}

	assertMissing(cwd)
	hub.offline = true
	if rec := preview(""); rec.Code == http.StatusOK {
		t.Fatalf("offline preview accepted: %s", rec.Body)
	}
	hub.offline = false
	assertMissing(cwd)

	custom := t.TempDir()
	missing := filepath.Join(custom, "missing")
	if rec := preview(missing); rec.Code == http.StatusOK {
		t.Fatalf("missing custom directory accepted: %s", rec.Body)
	}
	assertMissing(missing)
	assertMissing(cwd)
	type inputContext struct {
		Execution assistant.TaskExecution
		TaskInput bool `json:"task_input"`
		Catalog   assistantskills.Catalog
	}
	var selected inputContext
	decode(preview(custom), &selected)
	if !selected.TaskInput || selected.Execution.Cwd != resolved(t, custom) || f.n.assistantDirectoryPrepared(custom) {
		t.Fatalf("custom directory lost its own environment or was prepared: %+v", selected)
	}
	assertMissing(cwd)

	var first inputContext
	decode(preview(""), &first)
	if !first.TaskInput || first.Execution.Cwd != cwd || first.Execution.Launcher != "claude" || first.Execution.MachineID != f.n.machineID {
		t.Fatalf("default preview picked the wrong environment: %+v", first)
	}
	var skill string
	for _, entry := range first.Catalog.Skills {
		if entry.Name == "fixture-review" {
			skill = entry.ID
		}
	}
	if skill == "" || !f.n.assistantDirectoryPrepared(cwd) {
		t.Fatalf("default preview did not prepare the directory and discover the fixture skill: %+v", first.Catalog)
	}
	// Reopen an existing selection after the managed directory has disappeared.
	// The selected user Skill still exists in the same profile.
	if err := os.Rename(cwd, filepath.Join(f.n.dir, "parked-assistant")); err != nil {
		t.Fatal(err)
	}
	assertMissing(cwd)
	prompt := fmt.Sprintf(`<persona-context kind="skill" ref="%s">fixture-review</persona-context>`, skill)
	var saved struct{ Task assistant.Task }
	decode(call(http.MethodPost, "/api/v1/assistant/tasks", map[string]any{
		"title": "Review", "repeat": "daily", "time": "09:00", "enabled": true,
		"execution_machine_id": f.n.machineID, "structured_prompt": prompt,
	}), &saved)
	if saved.Task.ID == "" || saved.Task.Cwd != "" || !saved.Task.PromptSelections {
		t.Fatalf("save did not retain the default-directory binding: %+v", saved.Task)
	}
	file := filepath.Join(cwd, "notes.txt")
	writeFile(t, file, "Synthetic notes")
	prompt += "\n" + fmt.Sprintf(`<persona-context kind="file" path="notes.txt" machine="%s" cwd="%s" launcher="claude">Notes</persona-context>`, f.n.machineID, cwd)
	decode(call(http.MethodPut, "/api/v1/assistant/tasks/"+saved.Task.ID, map[string]any{"structured_prompt": prompt}), &saved)
	var run struct {
		SessionID string `json:"session_id"`
	}
	decode(call(http.MethodPost, "/api/v1/assistant/tasks/"+saved.Task.ID+"/run", nil), &run)
	if run.SessionID == "" {
		t.Fatal("task did not run")
	}
	f.idle(t, run.SessionID)
	inputs := emptyStartUserInputs(t, f.file("stdin.jsonl"))
	if len(inputs) != 1 {
		t.Fatalf("input discovery and validation should not start conversations, got %d turns", len(inputs))
	}
	gotFile := false
	for _, content := range inputs[0].Message.Content {
		gotFile = gotFile || strings.Contains(content.Text, "file-instructions") && strings.Contains(content.Text, resolved(t, file))
	}
	if !gotFile {
		t.Fatal("the run did not resolve the file selected from the default directory")
	}
}

func TestScheduledInboxSourceRefusesSelectedFilesWithoutChangingTask(t *testing.T) {
	f := cliTestBash(t)
	f.n.machineID = "inbox-fixture"
	cwd := resolved(t, t.TempDir())
	writeFile(t, filepath.Join(cwd, "notes.txt"), "Synthetic notes")
	selection := fmt.Sprintf(`<persona-context kind="file" path="notes.txt" machine="%s" cwd="%s" launcher="claude">Notes</persona-context>`, f.n.machineID, cwd)
	raw, err := nodeCall(t, f.n, "assistant.send", map[string]any{"cwd": cwd, "launcher": "claude", "text": "Start a fixture chat"})
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &source); err != nil || source.SessionID == "" {
		t.Fatalf("start fixture: %s (%v)", raw, err)
	}
	f.idle(t, source.SessionID)

	service := assistant.New(assistant.Options{Hub: schedulePromptHub{node: f.n}})
	result := service.Tool(t.Context(), assistant.ToolCall{
		Name: "schedule_followup", MachineID: f.n.machineID, SessionID: source.SessionID,
		Arguments: json.RawMessage(`{"at":"30m","title":"Review the fixture","prompt":"Review notes later"}`),
	})
	if result.IsError {
		t.Fatalf("schedule the existing follow-up: %+v", result)
	}
	call := func(method, route string, fields map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(method, route, bytes.NewReader(raw)).WithContext(t.Context()))
		return rec
	}
	getTask := func() assistant.Task {
		t.Helper()
		rec := call(http.MethodGet, "/api/v1/assistant/followups", nil)
		var list struct{ Followups []assistant.Task }
		if err := json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != http.StatusOK || err != nil || len(list.Followups) != 1 {
			t.Fatalf("follow-ups: %d %s (%v)", rec.Code, rec.Body, err)
		}
		return list.Followups[0]
	}
	task := getTask()
	if task.SessionID != source.SessionID {
		t.Fatalf("fixture task did not keep its source: %+v", task)
	}
	route := "/api/v1/assistant/tasks/" + task.ID
	original := selection + "\n\nReview the selected notes."
	if rec := call(http.MethodPut, route, map[string]any{"structured_prompt": original}); rec.Code != http.StatusOK {
		t.Fatalf("ordinary source could not save a file selection: %d %s", rec.Code, rec.Body)
	}

	// An existing saved selection is checked again when the source acquires the Inbox policy.
	state, ok := f.n.state.get(source.SessionID)
	if !ok {
		t.Fatal("source has no Node state")
	}
	state.Origin, state.OriginKnown = originInbox, true
	f.n.state.put(source.SessionID, state)
	if rec := call(http.MethodPut, route, map[string]any{"structured_prompt": selection + "\n\nEdited prompt"}); rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "file_unavailable") {
		t.Errorf("Inbox source did not refuse the edit with the file error: %d %s", rec.Code, rec.Body)
	}
	if kept := getTask(); kept.Prompt != original || !kept.PromptSelections || kept.SessionID != source.SessionID || kept.Title != task.Title {
		t.Errorf("refused edit changed the saved task: %+v", kept)
	}
	if rec := call(http.MethodPost, route+"/run", nil); rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "file_unavailable") {
		t.Errorf("Inbox source did not refuse to run the saved selection: %d %s", rec.Code, rec.Body)
	}
	if inputs := emptyStartUserInputs(t, f.file("stdin.jsonl")); len(inputs) != 1 {
		t.Fatalf("file selection reached a turn: %+v", inputs)
	}

	const plain = "Review the fixture without a file"
	if rec := call(http.MethodPut, route, map[string]any{"prompt": plain}); rec.Code != http.StatusOK {
		t.Fatalf("ordinary prompt save: %d %s", rec.Code, rec.Body)
	}
	rec := call(http.MethodPost, route+"/run", nil)
	var run struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &run); rec.Code != http.StatusOK || err != nil || run.SessionID != source.SessionID {
		t.Fatalf("ordinary prompt did not continue its Inbox source: %d %s (%v)", rec.Code, rec.Body, err)
	}
	f.idle(t, run.SessionID)
	if inputs := emptyStartUserInputs(t, f.file("stdin.jsonl")); len(inputs) != 2 {
		t.Fatalf("expected one initial and one ordinary follow-up turn, got %d", len(inputs))
	}
}

func TestScheduledRuntimeSourceRejectsFileSelection(t *testing.T) {
	n, hub := runtimeNode(t)
	n.machineID = "runtime-fixture"
	assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	cwd := filepath.Join(n.home, ".persona", "assistant")
	const id = "session-a1"
	host.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": id, "cwd": cwd, "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	service := assistant.New(assistant.Options{Hub: schedulePromptHub{node: n}})
	source, err := service.Start(t.Context(), assistant.StartParams{Launcher: "dsh", MachineID: n.machineID,
		Cwd: cwd, Text: "Review the notes later", Untitled: true})
	if err != nil || source.SessionID != id {
		t.Fatalf("start runtime source: %+v (%v)", source, err)
	}
	writeFile(t, filepath.Join(cwd, "notes.txt"), "Synthetic notes")

	uuid := newUUID()
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": id, "cwd": cwd, "started_by_persona": true, "status": "idle"},
		{"session_id": uuid, "cwd": cwd, "started_by_persona": true, "status": "idle"},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both runtime conversations listed", func() bool {
		_, _, first := n.runtimes.rowOf(id)
		_, _, second := n.runtimes.rowOf(uuid)
		return first && second
	})

	const refusalKey = "assistant-schedule.file_runtime_unsupported"
	want := i18n.T(i18n.Default, refusalKey)
	selection := func(launcher string) string {
		return fmt.Sprintf(`<persona-context kind="file" path="notes.txt" machine="%s" cwd="%s" launcher="%s">Notes</persona-context>`, n.machineID, cwd, launcher)
	}
	for _, sid := range []string{id, uuid} {
		_, err := nodeCall(t, n, "assistant.send", map[string]any{
			"session_id": sid, "cwd": cwd, "text": selection("dsh"),
			"task_execution": true, "task_input": true, "resume_only": true,
		})
		if refusal := assertFileRefusalDelivery(t, err); !strings.Contains(refusal.Message, want) {
			t.Errorf("selected send on a runtime-owned conversation: %v", err)
		}
		_, err = nodeCall(t, n, "assistant.content", map[string]any{
			"session_id": sid, "cwd": cwd, "launcher": "claude", "text": selection("claude"), "task_execution": true,
		})
		if refusal := assertFileRefusalDelivery(t, err); !strings.Contains(refusal.Message, want) {
			t.Errorf("content approval on a runtime-owned conversation: %v", err)
		}
	}

	args := json.RawMessage(`{"at":"30m","title":"Review","prompt":"Review the notes"}`)
	if result := service.Tool(t.Context(), assistant.ToolCall{
		Name: "schedule_followup", Arguments: args, MachineID: n.machineID, SessionID: id,
	}); result.IsError {
		t.Fatalf("could not save an ordinary follow-up: %+v", result)
	}
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(payload)).WithContext(t.Context())
		req.Header.Set("Accept-Language", string(i18n.Default))
		w := httptest.NewRecorder()
		service.ServeHTTP(w, req)
		return w
	}
	getTask := func() assistant.Task {
		t.Helper()
		w := request(http.MethodGet, "/api/v1/assistant/followups", nil)
		var list struct{ Followups []assistant.Task }
		if err := json.Unmarshal(w.Body.Bytes(), &list); w.Code != 200 || err != nil || len(list.Followups) != 1 {
			t.Fatalf("follow-ups: %d %s (%v)", w.Code, w.Body, err)
		}
		return list.Followups[0]
	}
	task := getTask()
	if task.SessionID != id {
		t.Fatalf("follow-up did not keep the runtime source: %+v", task)
	}

	preview := request(http.MethodPost, "/api/v1/assistant/tasks/input-context", map[string]any{"id": task.ID, "catalog": true})
	var result struct {
		TaskInput *bool  `json:"task_input"`
		Error     string `json:"input_error"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &result); preview.Code != 200 || err != nil || result.TaskInput == nil || *result.TaskInput || !strings.Contains(result.Error, want) {
		t.Errorf("preview did not explain the unavailable picker: %d %s (%v)", preview.Code, preview.Body, err)
	}
	path := "/api/v1/assistant/tasks/" + task.ID
	save := request(http.MethodPut, path, map[string]any{"structured_prompt": selection("dsh")})
	if save.Code == 200 || !strings.Contains(save.Body.String(), want) {
		t.Fatalf("save did not refuse with the runtime reason: %d %s", save.Code, save.Body)
	}
	if kept := getTask(); kept.Prompt != task.Prompt || kept.PromptSelections {
		t.Fatal("file selection modified the saved task")
	}

	before := len(host.got("session.send"))
	run := request(http.MethodPost, path+"/run", nil)
	sends := host.got("session.send")
	if run.Code != 200 || len(sends) != before+1 || !strings.Contains(string(sends[before]), task.Prompt) || len(host.got("session.start")) != 1 {
		t.Fatalf("plain follow-up did not continue its runtime: %d %s", run.Code, run.Body)
	}
}
