package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// runDevs answers model.run as a node would, or with err.
type runDevs struct {
	viaDevs
	saw   []proto.ModelRunParams
	usage ai.Usage
}

func (d *runDevs) CallNode(ctx context.Context, id, method string, params json.RawMessage) (json.RawMessage, error) {
	if method != proto.MethodModelRun || id != "server" {
		return nil, fmt.Errorf("unexpected %s on %s", method, id)
	}
	var p proto.ModelRunParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	d.saw = append(d.saw, p)
	if d.err != nil {
		return nil, d.err
	}
	return json.Marshal(proto.ModelRunAnswer{Text: "answer to " + p.Prompt, Model: "m-run", Usage: d.usage})
}

// An API purpose may pick plain claude, codex, or agy (no gateway, no key): Model configuration offers them with the
// main chat's computer, and requests run there as model.run with the system prompt, the turns and the chosen model;
// "off" leaves the effort to the launcher. Offline, logged out and old nodes say so; with no main chat computer the
// status says to choose one; unsupported CLIs (such as omp) are not offered, and a launcher since deleted is an error,
// not a run.
func TestAPIPurposeRunsOnALauncher(t *testing.T) {
	s, f, _ := modelService(t)
	devs := &runDevs{}
	s.devs = devs

	p := purposeOf(t, s, "kb")
	if p.MachineID != "server" || !slices.Contains(p.ProviderIDs, "launcher.claude") {
		t.Fatalf("kb offers %v on %q", p.ProviderIDs, p.MachineID)
	}
	if r := modelRequest(s, "PATCH", "kb", `{"revision":0,"provider":"launcher.claude","model":"bad model"}`); r.Code != 400 {
		t.Fatalf("bad model id: %d %s", r.Code, r.Body.String())
	}
	if r := modelRequest(s, "PATCH", "kb", `{"revision":0,"provider":"launcher.claude","model":"m-1"}`); r.Code != 200 {
		t.Fatalf("pick claude: %d %s", r.Code, r.Body.String())
	}
	if p := purposeOf(t, s, "kb"); p.Status != modelUnverified {
		t.Fatalf("status: %q", p.Status)
	}
	text, err := s.purposeModel("kb.compress").Complete(context.Background(), "sys", "hi", 64)
	if err != nil || text != "answer to hi" || len(devs.saw) != 1 {
		t.Fatalf("complete: %q %v %d", text, err, len(devs.saw))
	}
	if got := devs.saw[0]; got.Launcher != "claude" || got.Model != "m-1" || got.System != "sys" || got.TimeoutMS <= 0 {
		t.Fatalf("params: %+v", got)
	}
	reply, err := s.purposeModel("kb").Chat(context.Background(), ai.Request{Messages: []ai.Message{
		{Role: ai.RoleUser, Content: "first"}, {Role: ai.RoleAssistant, Content: "reply"}, {Role: ai.RoleUser, Content: "second"}}})
	if err != nil || reply.Model != "m-run" {
		t.Fatalf("chat: %+v %v", reply, err)
	}
	if in := devs.saw[1].Prompt; !strings.Contains(in, "User:\nfirst") || !strings.Contains(in, "Assistant:\nreply") || !strings.HasSuffix(strings.TrimSpace(strings.Split(in, "second")[0]), "User:") {
		t.Fatalf("transcript: %q", in)
	}

	// Triage defaults to "off", an API word: the launcher's own effort.
	if r := modelRequest(s, "PATCH", "inbox.triage", `{"revision":0,"provider":"launcher.claude"}`); r.Code != 200 {
		t.Fatalf("triage: %d %s", r.Code, r.Body.String())
	}
	if _, err := s.purposeModel("inbox.judge").Complete(context.Background(), "", "x", 16); err != nil || devs.saw[2].Effort != "" {
		t.Fatalf("triage run: %v %+v", err, devs.saw[2])
	}

	lang := s.lang(context.Background())
	args := i18n.Args{"name": "server", "launcher": "claude"}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("server: %w", apierr.ErrOffline), i18n.T(lang, "assistant-models.err_run_offline", args)},
		{&rpc.Error{Code: proto.CodeModelRunLoggedOut, Message: "x"}, i18n.T(lang, "assistant-models.err_run_logged_out", args)},
		{&rpc.Error{Code: proto.CodeModelRunTimeout, Message: "x"}, i18n.T(lang, "assistant-models.err_run_timeout", args)},
		{&rpc.Error{Code: proto.CodeNodeOutdated, Message: "Update the node"}, "Update the node"},
	} {
		devs.err = tc.err
		if _, err := s.purposeModel("kb").Complete(context.Background(), "", "x", 16); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: got %v, want %q", tc.err, err, tc.want)
		}
	}

	// Codex and Antigravity launchers are offered and callable via model.run (#12027); unsupported CLIs (such as omp)
	// and deleted launchers remain rejected.
	devs.err = nil
	putLaunchers(t, s,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "anti", Agent: "agy", API: "responses"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "cx", Agent: "codex", API: "responses"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "myomp", Agent: "omp", API: "responses"}},
	)
	p = purposeOf(t, s, "kb")
	for _, wantID := range []string{"launcher.claude", "launcher.anti", "launcher.cx"} {
		if !slices.Contains(p.ProviderIDs, wantID) {
			t.Fatalf("%s not offered: %v", wantID, p.ProviderIDs)
		}
	}
	if slices.Contains(p.ProviderIDs, "launcher.myomp") {
		t.Fatalf("unsupported omp launcher offered: %v", p.ProviderIDs)
	}
	for _, tc := range []struct {
		name, model, effort string
	}{
		{name: "cx", model: "gpt-6.1-terra", effort: "high"},
		{name: "anti", model: "gemini-4-hydrogen-low", effort: "low"},
	} {
		cur := purposeOf(t, s, "kb")
		body := fmt.Sprintf(`{"revision":%d,"provider":"launcher.%s","model":%q,"effort":%q}`, cur.Revision, tc.name, tc.model, tc.effort)
		if r := modelRequest(s, "PATCH", "kb", body); r.Code != 200 {
			t.Fatalf("pick %s: %d %s", tc.name, r.Code, r.Body.String())
		}
		before := len(devs.saw)
		text, err := s.purposeModel("kb").Complete(context.Background(), "sys-"+tc.name, "hi-"+tc.name, 64)
		if err != nil || text != "answer to hi-"+tc.name || len(devs.saw) != before+1 {
			t.Fatalf("%s complete: %q %v", tc.name, text, err)
		}
		got := devs.saw[len(devs.saw)-1]
		if got.Launcher != tc.name || got.Model != tc.model || got.Effort != tc.effort || got.System != "sys-"+tc.name {
			t.Fatalf("%s params: %+v", tc.name, got)
		}
	}
	// Restore kb back to launcher.claude for the no-machine status check below.
	cur := purposeOf(t, s, "kb")
	if r := modelRequest(s, "PATCH", "kb", fmt.Sprintf(`{"revision":%d,"provider":"launcher.claude","model":"m-1"}`, cur.Revision)); r.Code != 200 {
		t.Fatalf("restore claude: %d %s", r.Code, r.Body.String())
	}
	for _, name := range []string{"myomp", "gone"} {
		if _, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher." + name}); err == nil {
			t.Fatalf("%s runs", name)
		}
	}

	// No main chat computer (the only one is a work computer): still offered, and the status names the setting.
	f.hello.Assistant.WorkMachines = []string{"server"}
	s.settingsChanged(context.Background())
	if p := purposeOf(t, s, "kb"); !slices.Contains(p.ProviderIDs, "launcher.claude") || p.Status != i18n.T(lang, "assistant-models.err_run_no_machine") {
		t.Fatalf("no machine: %v %q", p.ProviderIDs, p.Status)
	}
}

func TestInboxGroupUsageReportsBothRoutes(t *testing.T) {
	s, hub, _ := modelService(t)
	ctx := context.Background()
	var logs logBuffer
	s.logf = logs.logf
	usage := ai.Usage{InputTokens: 14, OutputTokens: 7, CacheReadInputTokens: 95, CacheCreationInputTokens: 83}
	s.devs = &runDevs{usage: usage}

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "relay-model", "usage": usage,
			"content": []map[string]string{{"type": "text", "text": "relay reply"}},
		})
	}))
	defer relay.Close()
	hub.hello.Models.AI = ai.Config{BaseURL: relay.URL, APIKey: "private-test-key", Model: "relay-model"}

	call := func(want string) {
		t.Helper()
		text, err := s.purposeModelFor(purposeTriage, "inbox_group").Complete(ctx, "", "private-test-prompt", 64)
		if err != nil || text != want {
			t.Fatalf("reply: %q %v, want %q", text, err, want)
		}
	}
	call("relay reply")
	if r := modelRequest(s, "PATCH", "inbox.triage", `{"revision":0,"provider":"launcher.claude"}`); r.Code != http.StatusOK {
		t.Fatalf("pick claude: %d %s", r.Code, r.Body.String())
	}
	call("answer to private-test-prompt")

	got := logs.String()
	if strings.Count(got, "model_usage ") != 2 || strings.Contains(got, "private-test-") {
		t.Fatalf("usage log: %s", got)
	}
	const fields = `result=ok usage_reported=true input_tokens=14 output_tokens=7 cache_read_input_tokens=95 cache_creation_input_tokens=83`
	for _, want := range []string{
		`model_usage purpose="inbox.triage" operation="inbox_group" route=api provider="legacy.triage" model="relay-model" ` + fields,
		`model_usage purpose="inbox.triage" operation="inbox_group" route=cli provider="launcher.claude" model="m-run" ` + fields,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// An unconfigured API purpose uses the neutral no-model explanation (listing Claude Code, Codex, and Antigravity
// launchers as explicit choices alongside keyed API connections) in each language, even when Codex is installed.
func TestCodexOnlyAPIPurposeStatusExplainsTheLimitation(t *testing.T) {
	s, _, _ := modelService(t)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "codex", Agent: "codex", API: "responses"}})
	for _, lang := range []i18n.Lang{i18n.Default, i18n.En, i18n.Lang("ja")} {
		ctx := i18n.With(context.Background(), lang)
		p, err := s.modelPurpose(ctx, "inbox.judge", s.modelProviders(ctx))
		if err != nil {
			t.Fatal(err)
		}
		want := mc(lang, modelNoAPIStatus)
		if p.Status != want || !strings.Contains(p.Status, "Codex") || !strings.Contains(p.Status, "Claude Code") || !strings.Contains(p.Status, "Antigravity") {
			t.Errorf("%s: status %q, want %q naming Claude Code, Codex, and Antigravity", lang, p.Status, want)
		}
	}
}

// noAPIStatus does not trigger an online node refresh.
func TestNoAPIStatusUsesStoredNodeLauncherSnapshot(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	if err := s.store().PutSetting(ctx, launchersZshrcKey, map[string][]proto.Launcher{
		"server": {{Name: "cx", Source: "zshrc", Agent: "codex"}},
	}); err != nil {
		t.Fatal(err)
	}
	before := len(f.calls)
	if got := s.noAPIStatus(ctx); got != modelNoAPIStatus {
		t.Fatalf("status key %q, want %q", got, modelNoAPIStatus)
	}
	for _, call := range f.calls[before:] {
		if call.Method == DeviceLaunchersList {
			t.Fatalf("status selection refreshed launchers on %s", call.Device)
		}
	}
}

// Without a model the done judge leaves a visible waiting reason on the task instead of only logging it.
func TestUnconfiguredDoneJudgeLeavesTheReasonOnTheTask(t *testing.T) {
	s, f, _ := modelService(t)
	f.hello.Locale = "en"
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "codex", Agent: "codex", API: "responses"}})
	ctx := i18n.With(context.Background(), i18n.En)
	todo, err := s.createTodo(ctx, "Check the result", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	_, judgeErr := s.purposeModel("inbox.judge").Complete(ctx, "", "reply", 16)
	if !ai.IsNotConfigured(judgeErr) {
		t.Fatalf("judge error: %v", judgeErr)
	}
	s.applyTaskDone(ctx, todo, todo.CreatedAt, false, taskDoneCheck{}, judgeErr)
	got := todoNow(s, todo.ID)
	want := mc(i18n.En, modelNoAPIStatus)
	if got.DoneAt != nil || got.Check == nil || got.Check.Verdict != verdictWaiting || got.Check.Reason != want {
		t.Fatalf("task: done %v, check %+v, want waiting with %q", got.DoneAt, got.Check, want)
	}
}

// An install without Codex gets the same no-model explanation naming all supported launcher choices.
func TestNoAPIStatusDoesNotBlameCodexWithoutIt(t *testing.T) {
	s, _, _ := modelService(t)
	for _, lang := range []i18n.Lang{i18n.Default, i18n.En, i18n.Lang("ja")} {
		ctx := i18n.With(context.Background(), lang)
		p, err := s.modelPurpose(ctx, "inbox.judge", s.modelProviders(ctx))
		if err != nil {
			t.Fatal(err)
		}
		if want := mc(lang, modelNoAPIStatus); p.Status != want {
			t.Errorf("%s: status %q, want %q", lang, p.Status, want)
		}
	}
}

// The task reason uses the neutral no-model explanation when no model is configured.
func TestUnconfiguredDoneJudgeUsesNeutralReasonWithoutCodex(t *testing.T) {
	s, f, _ := modelService(t)
	f.hello.Locale = "en"
	ctx := i18n.With(context.Background(), i18n.En)
	todo, err := s.createTodo(ctx, "Check the result", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	_, judgeErr := s.purposeModel("inbox.judge").Complete(ctx, "", "reply", 16)
	if !ai.IsNotConfigured(judgeErr) {
		t.Fatalf("judge error: %v", judgeErr)
	}
	s.applyTaskDone(ctx, todo, todo.CreatedAt, false, taskDoneCheck{}, judgeErr)
	got := todoNow(s, todo.ID)
	if got.Check == nil || got.Check.Verdict != verdictWaiting || got.Check.Reason != mc(i18n.En, modelNoAPIStatus) {
		t.Fatalf("task check: %+v", got.Check)
	}
}

// The done judge does not refresh nodes just to explain an unavailable model.
func TestUnconfiguredDoneJudgeDoesNotRefreshLaunchers(t *testing.T) {
	s, f, _ := modelService(t)
	f.hello.Locale = "en"
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "codex", Agent: "codex", API: "responses"}})
	ctx := i18n.With(context.Background(), i18n.En)
	todo, err := s.createTodo(ctx, "Check the result", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	_, judgeErr := s.purposeModel("inbox.judge").Complete(ctx, "", "reply", 16)
	if !ai.IsNotConfigured(judgeErr) {
		t.Fatalf("judge error: %v", judgeErr)
	}
	before := len(f.calls)
	s.applyTaskDone(ctx, todo, todo.CreatedAt, false, taskDoneCheck{}, judgeErr)
	for _, call := range f.calls[before:] {
		if call.Method == DeviceLaunchersList {
			t.Fatalf("done judge refreshed launchers on %s just to choose its reason", call.Device)
		}
	}
	if got := todoNow(s, todo.ID); got.Check == nil || got.Check.Reason != mc(i18n.En, modelNoAPIStatus) {
		t.Fatalf("task check: %+v", got.Check)
	}
}

// Several turns go to the CLI as one transcript (tools/launcher_run_input).
func TestGoldenLauncherRunInput(t *testing.T) {
	golden(t, "launcher_run_input", runPrompt([]ai.Message{{Role: ai.RoleUser, Content: "first"}, {Role: ai.RoleAssistant, Content: "reply"}, {Role: ai.RoleUser, Content: "second"}}))
}

// Runtime provenance (#12043 P1): only the verified built-in Antigravity companion (or shell/PATH/managed CLIs) is
// offered in provider_ids and admitted by apiConfig; external agy runtimes and other runtimes advertising agy or codex
// are excluded from provider_ids, apiConfig, and modelLauncherCompatible.
func TestAPIPurposeRuntimeProvenanceFiltering(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	byMachine := map[string][]proto.Launcher{
		"server": {
			{Name: "agy", Source: "agy", Agent: "agy", BuiltinCompanion: true},
			{Name: "ext-agy", Source: "agy", Agent: "agy", BuiltinCompanion: false},
			{Name: "paseo-agy", Source: "paseo", Agent: "agy"},
			{Name: "paseo-codex", Source: "paseo", Agent: "codex"},
			{Name: "codex", Source: "zshrc", Agent: "codex"},
		},
		"m2": {
			{Name: "mixed-agy", Source: "agy", Agent: "agy", BuiltinCompanion: true},
		},
	}
	f.devices = []Device{{ID: "server", Name: "server", Online: true}, {ID: "m2", Name: "m2", Online: true}}
	f.sessions["m2"] = nil
	f.hello.Assistant.Machines = []string{"server", "m2"}
	f.answerNode(DeviceLaunchersList, func(device string, _ json.RawMessage) (any, error) {
		return map[string]any{"launchers": byMachine[device]}, nil
	})

	p := purposeOf(t, s, "kb")
	for _, want := range []string{"launcher.agy", "launcher.codex"} {
		if !slices.Contains(p.ProviderIDs, want) {
			t.Fatalf("expected %s in provider_ids, got %v", want, p.ProviderIDs)
		}
	}
	for _, reject := range []string{"launcher.ext-agy", "launcher.paseo-agy", "launcher.paseo-codex", "launcher.mixed-agy"} {
		if slices.Contains(p.ProviderIDs, reject) {
			t.Fatalf("unsupported runtime provider %s offered in provider_ids: %v", reject, p.ProviderIDs)
		}
		if _, err := s.apiConfig(ctx, ModelBinding{Provider: reject}); err == nil {
			t.Fatalf("apiConfig unexpectedly admitted %s", reject)
		}
		if r := modelRequest(s, http.MethodPatch, "kb", fmt.Sprintf(`{"revision":%d,"provider":%q}`, p.Revision, reject)); r.Code != http.StatusBadRequest {
			t.Fatalf("PATCH unexpectedly admitted %s: %d %s", reject, r.Code, r.Body.String())
		}
	}

	// Built-in Antigravity companion is accepted by apiConfig and PATCH.
	if c, err := s.apiConfig(ctx, ModelBinding{Provider: "launcher.agy"}); err != nil || c.RunLauncher != "agy" {
		t.Fatalf("apiConfig for built-in agy companion: %+v %v", c, err)
	}
	if r := modelRequest(s, http.MethodPatch, "kb", fmt.Sprintf(`{"revision":%d,"provider":"launcher.agy"}`, p.Revision)); r.Code != http.StatusOK {
		t.Fatalf("PATCH built-in agy companion: %d %s", r.Code, r.Body.String())
	}

	// modelLauncherCompatible also rejects external agy and runtime-backed codex for API purposes while admitting
	// built-in companion agy and shell codex.
	apiPurpose, err := s.modelPurposeDef(ctx, "kb")
	if err != nil {
		t.Fatal(err)
	}
	apiPurpose.MachineID = "server"
	for _, tc := range []struct {
		name string
		l    ConfiguredLauncher
		want bool
	}{
		{"builtin agy companion", ConfiguredLauncher{Launcher: proto.Launcher{Name: "agy", Source: "agy", Agent: "agy", BuiltinCompanion: true}}, true},
		{"external agy runtime", ConfiguredLauncher{Launcher: proto.Launcher{Name: "agy", Source: "agy", Agent: "agy", BuiltinCompanion: false}}, false},
		{"paseo agy runtime", ConfiguredLauncher{Launcher: proto.Launcher{Name: "paseo-agy", Source: "paseo", Agent: "agy"}}, false},
		{"paseo codex runtime", ConfiguredLauncher{Launcher: proto.Launcher{Name: "paseo-codex", Source: "paseo", Agent: "codex"}}, false},
		{"zshrc codex launcher", ConfiguredLauncher{Launcher: proto.Launcher{Name: "codex", Source: "zshrc", Agent: "codex"}}, true},
	} {
		if got := s.modelLauncherCompatible(apiPurpose, tc.l, nil); got != tc.want {
			t.Errorf("modelLauncherCompatible(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Multi-machine provenance: same launcher name backed by external runtime on "server" and built-in companion on "m2"
	// is excluded when the main chat machine is "server", and admitted only when the main chat machine is "m2".
	t.Run("multi-machine provenance", func(t *testing.T) {
		ms, mf, _ := modelService(t)
		multi := map[string][]proto.Launcher{
			"server": {{Name: "shared-agy", Source: "agy", Agent: "agy", BuiltinCompanion: false}},
			"m2":     {{Name: "shared-agy", Source: "agy", Agent: "agy", BuiltinCompanion: true}},
		}
		mf.devices = []Device{{ID: "server", Name: "server", Online: true}, {ID: "m2", Name: "m2", Online: true}}
		mf.sessions["m2"] = nil
		mf.hello.Assistant.Machines = []string{"server", "m2"}
		mf.answerNode(DeviceLaunchersList, func(device string, _ json.RawMessage) (any, error) {
			return map[string]any{"launchers": multi[device]}, nil
		})
		if p := purposeOf(t, ms, "kb"); slices.Contains(p.ProviderIDs, "launcher.shared-agy") {
			t.Fatalf("external runtime on main machine server should not be offered: %v", p.ProviderIDs)
		}
		if _, err := ms.apiConfig(ctx, ModelBinding{Provider: "launcher.shared-agy"}); err == nil {
			t.Fatal("apiConfig admitted external runtime on main machine server")
		}
		mf.hello.Assistant.MachineID = "m2"
		ms.settingsChanged(ctx)
		if p := purposeOf(t, ms, "kb"); !slices.Contains(p.ProviderIDs, "launcher.shared-agy") {
			t.Fatalf("built-in companion on main machine m2 should be offered: %v", p.ProviderIDs)
		}
		if c, err := ms.apiConfig(ctx, ModelBinding{Provider: "launcher.shared-agy"}); err != nil || c.RunLauncher != "shared-agy" || c.ViaMachine != "m2" {
			t.Fatalf("apiConfig for built-in companion on m2: %+v %v", c, err)
		}
	})
}

// Codex-only efforts (#12043 P2): saving and running minimal and ultra on an API purpose is allowed for Codex
// launchers (managed and shell), while remaining rejected for Claude, Antigravity, and direct HTTP API providers.
func TestAPIPurposeCodexEffortsValidationAndRun(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	devs := &runDevs{}
	s.devs = devs

	putLaunchers(t, s,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "cx", Agent: "codex", API: "responses"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "cx-msg-unkeyed", Agent: "codex"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "cx-msg-explicit", Agent: "codex", API: "messages"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "cx-msg-keyed", Agent: "codex", API: "messages", BaseURL: "https://api.example.com", Model: "m-1"}, KeySealed: "sealed"},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "anti", Agent: "agy", API: "responses"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "direct", Kind: "api", API: "responses", BaseURL: "https://api.example.com", Model: "m-1"}, KeySealed: "sealed"},
	)
	if err := s.store().PutSetting(ctx, launchersZshrcKey, map[string][]proto.Launcher{
		"server": {
			{Name: "codex", Source: "zshrc", Agent: "codex"},
			{Name: "agy", Source: "zshrc", Agent: "agy"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Both managed and shell Codex launchers accept and forward minimal and ultra.
	for _, launcher := range []string{"cx", "codex"} {
		for _, effort := range []string{"minimal", "ultra"} {
			cur := purposeOf(t, s, "kb")
			body := fmt.Sprintf(`{"revision":%d,"provider":"launcher.%s","model":"gpt-6.1-terra","effort":%q}`, cur.Revision, launcher, effort)
			if r := modelRequest(s, http.MethodPatch, "kb", body); r.Code != http.StatusOK {
				t.Fatalf("PATCH %s effort=%s: %d %s", launcher, effort, r.Code, r.Body.String())
			}
			before := len(devs.saw)
			text, err := s.purposeModel("kb").Complete(ctx, "sys", "hello-"+effort, 64)
			if err != nil || text != "answer to hello-"+effort || len(devs.saw) != before+1 {
				t.Fatalf("Complete %s effort=%s: %q %v", launcher, effort, text, err)
			}
			got := devs.saw[len(devs.saw)-1]
			if got.Launcher != launcher || got.Effort != effort {
				t.Fatalf("model.run params for %s effort=%s: %+v", launcher, effort, got)
			}
		}
	}

	// Managed Messages-backed Codex launchers (both unkeyed and keyed), Claude, Antigravity (managed and shell), and
	// direct HTTP API providers reject minimal and ultra.
	for _, provider := range []string{"launcher.cx-msg-unkeyed", "launcher.cx-msg-explicit", "launcher.cx-msg-keyed", "launcher.claude", "launcher.anti", "launcher.agy", "launcher.direct"} {
		for _, effort := range []string{"minimal", "ultra"} {
			cur := purposeOf(t, s, "kb")
			body := fmt.Sprintf(`{"revision":%d,"provider":%q,"effort":%q}`, cur.Revision, provider, effort)
			if r := modelRequest(s, http.MethodPatch, "kb", body); r.Code != http.StatusBadRequest {
				t.Fatalf("PATCH %s effort=%s unexpectedly succeeded: %d %s", provider, effort, r.Code, r.Body.String())
			}
		}
	}

	// Regression test (#12043 review 5996865114 P1): an existing stored Claude or Antigravity configuration carrying
	// Codex-only effort (minimal or ultra) must NOT forward that effort to model.run; it omits Effort so the launcher
	// uses its own default rather than failing with "unknown effort level" on the node.
	for _, tc := range []struct {
		launcher string
		effort   string
	}{
		{"claude", "ultra"},
		{"claude", "minimal"},
		{"anti", "ultra"},
		{"agy", "minimal"},
	} {
		before := len(devs.saw)
		cfg := ai.Config{RunLauncher: tc.launcher, ViaMachine: "server", ReasoningEffort: tc.effort}
		if _, err := s.runClient(cfg).Complete(ctx, "sys", "legacy-"+tc.launcher+"-"+tc.effort, 64); err != nil {
			t.Fatalf("runClient(%s, %s): %v", tc.launcher, tc.effort, err)
		}
		if len(devs.saw) != before+1 {
			t.Fatalf("expected 1 model.run call for %s, got %d", tc.launcher, len(devs.saw)-before)
		}
		if got := devs.saw[len(devs.saw)-1]; got.Effort != "" {
			t.Fatalf("model.run forwarded Codex-only effort %q to non-Codex launcher %q: %+v", tc.effort, tc.launcher, got)
		}
	}

	// ModelProvider exposes Source so Web/clients can distinguish Persona-managed empty-API Messages launchers from
	// shell/PATH launchers without inferring from HasKey (#12120 P2).
	providers := s.modelProviders(ctx)
	checkSource := func(id, want string) {
		t.Helper()
		i := slices.IndexFunc(providers, func(p ModelProvider) bool { return p.ID == id })
		if i < 0 || providers[i].Source != want {
			t.Fatalf("provider %s source = %q (idx=%d), want %q", id, func() string {
				if i < 0 {
					return ""
				}
				return providers[i].Source
			}(), i, want)
		}
	}
	checkSource("launcher.cx-msg-unkeyed", "persona")
	checkSource("launcher.codex", "zshrc")

	// Machine-specific agent resolution (#12120 P2 comment 4647228894): when equally named shell launchers back
	// different agents on different machines, effort validation must follow the actual run machine (main chat machine),
	// not the cross-machine first-merged ModelProvider.Agent.
	t.Run("multi-machine same launcher name different agents", func(t *testing.T) {
		ms, mf, _ := modelService(t)
		mDevs := &runDevs{}
		ms.devs = mDevs
		byMachine := map[string][]proto.Launcher{
			"a-box": {
				{Name: "dual", Source: "zshrc", Agent: "claude"},
				{Name: "dual-rev", Source: "zshrc", Agent: "codex"},
			},
			"server": {
				{Name: "dual", Source: "zshrc", Agent: "codex"},
				{Name: "dual-rev", Source: "zshrc", Agent: "claude"},
			},
		}
		mf.devices = []Device{{ID: "a-box", Name: "a-box", Online: true}, {ID: "server", Name: "server", Online: true}}
		mf.sessions["a-box"] = nil
		mf.hello.Assistant.Machines = []string{"a-box", "server"}
		mf.hello.Assistant.MachineID = "server"
		mf.answerNode(DeviceLaunchersList, func(device string, _ json.RawMessage) (any, error) {
			return map[string]any{"launchers": byMachine[device]}, nil
		})

		// "dual" is Claude on earlier machine a-box, but Codex on main chat machine server -> minimal & ultra accepted.
		for _, effort := range []string{"minimal", "ultra"} {
			cur := purposeOf(t, ms, "kb")
			body := fmt.Sprintf(`{"revision":%d,"provider":"launcher.dual","effort":%q}`, cur.Revision, effort)
			if r := modelRequest(ms, http.MethodPatch, "kb", body); r.Code != http.StatusOK {
				t.Fatalf("PATCH dual (Codex on main machine server) effort=%s: %d %s", effort, r.Code, r.Body.String())
			}
		}
		// "dual-rev" is Codex on earlier machine a-box, but Claude on main chat machine server -> minimal & ultra rejected.
		for _, effort := range []string{"minimal", "ultra"} {
			cur := purposeOf(t, ms, "kb")
			body := fmt.Sprintf(`{"revision":%d,"provider":"launcher.dual-rev","effort":%q}`, cur.Revision, effort)
			if r := modelRequest(ms, http.MethodPatch, "kb", body); r.Code != http.StatusBadRequest {
				t.Fatalf("PATCH dual-rev (Claude on main machine server) effort=%s unexpectedly succeeded: %d %s", effort, r.Code, r.Body.String())
			}
		}
	})
}
