//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise the current manager/Node wire shape, not just direct admission helpers.
func externalMergeParams(launcher string, execution modelExecution, text string) map[string]any {
	params := map[string]any{
		"session_id": execution.External.SessionID, "launcher": launcher, "text": text,
		"model": execution.Model, "effort": execution.Effort, "model_fingerprint": execution.Fingerprint,
		"runtime": execution.Runtime, "external": execution.External, "is_main": execution.IsMain,
	}
	if execution.ConfigDir != "" {
		params["config_dir"] = execution.ConfigDir
	}
	if execution.SupervisionResolved || execution.SupervisionInstructions != "" {
		params["supervision_instructions"] = execution.SupervisionInstructions
	}
	return params
}

func externalMergeModelReply(f *cliFixture, launcher string, execution modelExecution) {
	params := externalMergeParams(launcher, execution, "")
	delete(params, "text")
	f.fh.mu.Lock()
	f.fh.model = params
	f.fh.mu.Unlock()
}

func externalMergeRecordNative(t *testing.T, f *cliFixture) {
	t.Helper()
	body := strings.Replace(externalTestNative, "#!/bin/sh\n", `#!/bin/sh
printf '%s\n' "$CLAUDE_CONFIG_DIR" >> "$CLI_FAKE_DIR/external-profile"
printf '%s\n' "$@" > "$CLI_FAKE_DIR/external-native-args"
`, 1)
	if err := os.WriteFile(filepath.Join(f.dir, "example-native"), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func externalMergeStart(t *testing.T, f *cliFixture, launcher, cwd string, execution modelExecution) *assistantProc {
	t.Helper()
	params := externalMergeParams(launcher, execution, "first main-chat message")
	delete(params, "session_id")
	params["new_session_id"], params["cwd"], params["permission_mode"] = execution.External.SessionID, cwd, "default"
	answer, err := nodeCall(t, f.n, "assistant.send", params)
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(answer, &started); err != nil || started.SessionID != execution.External.SessionID {
		t.Fatalf("new main-chat identity = %s (%v)", answer, err)
	}
	f.idle(t, started.SessionID)
	p := f.proc(started.SessionID)
	if p == nil {
		t.Fatal("successful send did not register its native process")
	}
	return p
}

func TestExternalMergeModelLookupPreservesBindingAndPolicy(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t, false)
	for _, policy := range []string{"Review task evidence before accepting completion.", ""} {
		execution.SupervisionInstructions, execution.SupervisionResolved = policy, true
		externalMergeModelReply(f, launcher, execution)
		gotLauncher, got := f.n.assistantModel(t.Context(), execution.External.SessionID)
		if gotLauncher != launcher || got.External == nil || *got.External != *execution.External ||
			got.Model != execution.Model || got.Fingerprint != execution.Fingerprint || !got.UseEffort || !got.IsMain ||
			!got.SupervisionResolved || got.SupervisionInstructions != policy {
			t.Fatal("manager model lookup lost the external binding or explicit role policy")
		}
		fingerprint := f.n.executionFingerprint(launcher, got)
		got.SupervisionResolved = false
		got.ConfigDir = t.TempDir()
		if f.n.executionFingerprint(launcher, got) != fingerprint {
			t.Fatal("policy presence or profile metadata changed the model fingerprint")
		}
		binding := *got.External
		binding.Revision = "sha256:" + strings.Repeat("2", 64)
		got.External = &binding
		if f.n.executionFingerprint(launcher, got) == fingerprint {
			t.Fatal("an unpinned execution fingerprint lost its external revision")
		}
	}
	if _, ok := f.n.external.lookup(launcher); ok {
		t.Fatal("reading manager execution metadata unexpectedly loaded an adapter inventory")
	}
}

func TestExternalMergeRPCPreservesProfileBindingAndPolicy(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t, false)
	externalMergeRecordNative(t, f)
	profile := t.TempDir()
	settings := `{"model":"profile-model","permissions":{"defaultMode":"plan"},"env":{"PROFILE_MARKER":"profile-value"}}`
	path := filepath.Join(profile, "settings.json")
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	execution.ConfigDir = profile
	execution.SupervisionInstructions, execution.SupervisionResolved = "Review task evidence before accepting completion.", true
	id, cwd := execution.External.SessionID, t.TempDir()
	p := externalMergeStart(t, f, launcher, cwd, execution)
	for _, method := range []string{"assistant.send", "session.send"} {
		if _, err := nodeCall(t, f.n, method, externalMergeParams(launcher, execution, method+" follow-up")); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		f.idle(t, id)
		if current := f.proc(id); current != p {
			t.Fatalf("%s replaced an unchanged binding/profile/policy", method)
		}
	}
	p.mu.Lock()
	model := p.model
	p.mu.Unlock()
	if model.External == nil || *model.External != *execution.External || model.ConfigDir != profile ||
		model.SupervisionInstructions != execution.SupervisionInstructions || !model.IsMain {
		t.Fatal("RPC dispatch lost explicit CLI execution metadata")
	}
	st, _ := f.n.state.get(id)
	if st.ConfigDir != profile || st.SupervisionInstructions != execution.SupervisionInstructions || st.Launcher != launcher {
		t.Fatal("saved conversation metadata diverged from its native process")
	}
	args := strings.Split(strings.TrimSpace(f.file("external-native-args")), "\n")
	for flag, want := range map[string]string{"--model": execution.Model, "--append-system-prompt": execution.SupervisionInstructions} {
		i := slices.Index(args, flag)
		if i < 0 || i+1 >= len(args) || args[i+1] != want {
			t.Fatalf("profile settings displaced native %s", flag)
		}
	}
	if strings.Count(f.file("external-native-args"), "--settings\n") != 1 ||
		strings.TrimSpace(f.file("external-profile")) != profile {
		t.Fatal("native start lost its selected profile or received competing settings arguments")
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != settings {
		t.Fatal("native startup rewrote the selected profile's own settings")
	}
	output := strings.TrimSpace(f.file("external-settings-path"))
	if dir, err := os.Stat(filepath.Dir(output)); err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatal("the node's adapter settings directory is not private")
	}
	if file, err := os.Stat(output); err != nil || file.Mode().Perm() != 0o600 {
		t.Fatal("the adapter's settings output is not private")
	}
	initialized := false
	for _, line := range strings.Split(strings.TrimSpace(f.file("external-stdin")), "\n") {
		var event struct {
			Type    string `json:"type"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "control_request" && event.Request.Subtype == "initialize" {
			initialized = true
		}
		if event.Type == "user" && !initialized {
			t.Fatal("native user input preceded acknowledged initialization")
		}
	}
	if !initialized {
		t.Fatal("native external start omitted initialization")
	}
	answer, err := nodeCall(t, f.n, "assistant.execution", map[string]any{"session_id": id})
	if err != nil {
		t.Fatal(err)
	}
	var actual proto.AssistantExecution
	if err := json.Unmarshal(answer, &actual); err != nil || actual.State != proto.ExecutionRunning || actual.Source != "native" || actual.Harness != "claude" {
		t.Fatalf("registered native execution report = %s (%v)", answer, err)
	}
	if f.file("start.json") != "" || f.file("cli-argv") != "" {
		t.Fatal("external execution leaked into the SDK or ordinary CLI fallback")
	}
}

func TestExternalMergeRPCRejectsUnsupportedRuntimeBeforeFallback(t *testing.T) {
	for _, method := range []string{"assistant.send", "session.send"} {
		for _, runtime := range []string{"agentsdk", "unknown-runtime"} {
			t.Run(method+"/"+runtime, func(t *testing.T) {
				f, launcher, execution, _ := externalAssistantFixture(t, false)
				id := execution.External.SessionID
				f.n.state.put(id, sessionState{Launcher: launcher, Cwd: identity.AssistantDir(f.n.home), IsMain: true})
				execution.Runtime = runtime
				params := externalMergeParams(launcher, execution, "must not be delivered")
				delete(params, "launcher") // infer the external launcher from the conversation before runtime normalization
				_, err := nodeCall(t, f.n, method, params)
				requireExternalCode(t, err, "adapter_runtime_unsupported")
				if f.file("external-argv") != "" || f.file("start.json") != "" || f.file("cli-argv") != "" {
					t.Fatal("unsupported runtime started a child or fell back to ordinary CLI")
				}
			})
		}
	}
}

func TestExternalMergeCompactRefreshesTrustedBindingAndKeepsProfile(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*modelExecution)
		code string
	}{
		{"current_binding", func(*modelExecution) {}, ""},
		{"missing_scope", func(m *modelExecution) { m.External = nil }, "adapter_scope_required"},
		{"forged_session", func(m *modelExecution) { m.External.SessionID = newUUID() }, "adapter_scope_required"},
		{"wrong_scope", func(m *modelExecution) { m.External.Scope = "other-scope" }, "adapter_scope_required"},
		{"wrong_target", func(m *modelExecution) { m.External.TargetID += "-other" }, "adapter_target_not_found"},
		{"sdk", func(m *modelExecution) { m.Runtime = "agentsdk" }, "adapter_runtime_unsupported"},
		{"unknown_runtime", func(m *modelExecution) { m.Runtime = "unknown-runtime" }, "adapter_runtime_unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, launcher, execution, _ := externalAssistantFixture(t, false)
			externalMergeRecordNative(t, f)
			id, profile, cwd := execution.External.SessionID, t.TempDir(), t.TempDir()
			f.n.state.put(id, sessionState{Launcher: launcher, Cwd: cwd, Model: "old-model", ModelFingerprint: "old-fingerprint",
				IsMain: true, ConfigDir: profile, SupervisionInstructions: "Previous policy."})
			controlTranscript(t, f, id)
			execution.SupervisionInstructions, execution.SupervisionResolved = "Current manager-authorized policy.", true
			params := externalMergeParams(launcher, execution, "")
			test.edit(&execution)
			params["external"], params["runtime"] = execution.External, execution.Runtime
			f.fh.mu.Lock()
			f.fh.model = params
			f.fh.mu.Unlock()
			_, err := nodeCall(t, f.n, "assistant.compact", map[string]any{"session_id": id})
			if test.code != "" {
				requireExternalCode(t, err, test.code)
				if f.proc(id) != nil || f.file("external-argv") != "" || f.file("start.json") != "" || f.file("cli-argv") != "" {
					t.Fatal("a refused cold compaction started a child")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cliWait(t, "native compact input", func() bool { return strings.Contains(f.file("external-stdin"), "/compact") })
			p := f.proc(id)
			if p == nil {
				t.Fatal("cold compaction did not start its native process")
			}
			p.mu.Lock()
			model := p.model
			p.mu.Unlock()
			if model.External == nil || *model.External != *execution.External || model.Model != execution.Model ||
				model.ConfigDir != profile || model.SupervisionInstructions != execution.SupervisionInstructions {
				t.Fatal("compaction lost the refreshed trusted binding, saved profile or current policy")
			}
			if strings.TrimSpace(f.file("external-profile")) != profile || !strings.Contains(f.file("external-argv"), "--resume "+id) {
				t.Fatal("cold compaction did not resume inside the saved profile")
			}
			if st, _ := f.n.state.get(id); st.ConfigDir != profile || st.SupervisionInstructions != execution.SupervisionInstructions {
				t.Fatal("compaction did not preserve the profile and persist the current role")
			}
			f.fh.mu.Lock()
			queried := f.fh.queried["assistant.model"]
			f.fh.mu.Unlock()
			if queried != id {
				t.Fatal("compaction did not refresh authority for its exact session")
			}
		})
	}
}

func TestExternalMergeBusyRoleChangeRefusesWithoutDelivery(t *testing.T) {
	for _, next := range []string{"Changed manager-authorized policy.", ""} {
		t.Run("policy="+next, func(t *testing.T) {
			f, launcher, execution, update := externalAssistantFixture(t)
			execution.SupervisionInstructions, execution.SupervisionResolved = "Original manager-authorized policy.", true
			p := externalMergeStart(t, f, launcher, t.TempDir(), execution)
			binding := *execution.External
			binding.Revision = "sha256:" + strings.Repeat("2", 64)
			execution.External = &binding
			update(binding.Revision)
			f.n.external.launchers(t.Context(), true)
			p.mu.Lock()
			p.busy = true
			p.mu.Unlock()
			before := f.file("external-stdin")
			execution.SupervisionInstructions = next
			_, err := nodeCall(t, f.n, "session.send", externalMergeParams(launcher, execution, "must not run under the old policy"))
			requireExternalCode(t, err, "adapter_busy")
			if f.file("external-stdin") != before || f.proc(p.id) != p || !p.alive() {
				t.Fatal("a refused role change delivered to or interrupted the old busy process")
			}
			p.mu.Lock()
			policy := p.model.SupervisionInstructions
			p.mu.Unlock()
			if policy != "Original manager-authorized policy." {
				t.Fatal("a refused role change changed the busy process's authority")
			}
		})
	}
}

func TestExternalMergePolicyChangeUsesCurrentRevision(t *testing.T) {
	for _, policy := range []string{"Changed manager-authorized policy.", ""} {
		t.Run("policy="+policy, func(t *testing.T) {
			f, launcher, execution, update := externalAssistantFixture(t)
			execution.ConfigDir = t.TempDir()
			execution.SupervisionInstructions, execution.SupervisionResolved = "Original manager-authorized policy.", true
			id, cwd := execution.External.SessionID, t.TempDir()
			old := externalMergeStart(t, f, launcher, cwd, execution)
			controlTranscript(t, f, id)
			binding := *execution.External
			binding.Revision = "sha256:" + strings.Repeat("2", 64)
			execution.External, execution.SupervisionInstructions = &binding, policy
			update(binding.Revision)
			f.n.external.launchers(t.Context(), true)
			if _, err := nodeCall(t, f.n, "session.send", externalMergeParams(launcher, execution, "continue under the current policy")); err != nil {
				t.Fatalf("an idle policy change pinned the old revision instead of starting the current one: %v", err)
			}
			f.idle(t, id)
			p := f.proc(id)
			if p == nil || p == old || p.cmd.Process.Pid == old.cmd.Process.Pid {
				t.Fatal("an idle role change reused the old system policy")
			}
			p.mu.Lock()
			model := p.model
			p.mu.Unlock()
			if model.External == nil || *model.External != binding || model.SupervisionInstructions != policy || model.ConfigDir != execution.ConfigDir {
				t.Fatal("the replacement process lost the current revision, resolved policy or saved profile")
			}
		})
	}
}

func TestExternalMergePinComparesEveryEffectivePolicyField(t *testing.T) {
	binding := proto.ExternalExecution{Scope: proto.ExternalMainChatScope, SessionID: "11111111-2222-4333-8444-555555555555", AdapterID: "example", TargetID: "example-target", Revision: "old-revision"}
	launcher := proto.ExternalLauncherName(binding.AdapterID, binding.TargetID)
	original := modelExecution{Model: "example-model", Fingerprint: "explicit-pick", IsMain: true, External: &binding, ConfigDir: "/example/profile", Origin: "human"}
	p := &assistantProc{launcher: launcher, model: original, exited: make(chan struct{})}
	for _, test := range []struct {
		name string
		edit func(*modelExecution)
	}{
		{"model", func(m *modelExecution) { m.Model = "other-model" }},
		{"effort", func(m *modelExecution) { m.Effort = "high" }},
		{"use_effort", func(m *modelExecution) { m.UseEffort = true }},
		{"follows_setting", func(m *modelExecution) { m.FollowsSetting = true }},
		{"compact_window", func(m *modelExecution) { m.MainCompactWindow = 400000 }},
		{"role_policy", func(m *modelExecution) { m.SupervisionInstructions = "Changed policy." }},
		{"fingerprint", func(m *modelExecution) { m.Fingerprint = "changed-pick" }},
		{"profile", func(m *modelExecution) { m.ConfigDir = "/example/other-profile" }},
		{"origin", func(m *modelExecution) { m.Origin = "task" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := original
			current := binding
			current.Revision = "current-revision"
			next.External = &current
			test.edit(&next)
			if got := pinExternalExecution(p, launcher, next); got.External.Revision != current.Revision {
				t.Fatal("an effective process-policy change retained the stale catalog revision")
			}
		})
	}
	next := original
	current := binding
	current.Revision = "current-revision"
	next.External, next.Runtime, next.SupervisionResolved = &current, "cli", true
	if got := pinExternalExecution(p, launcher, next); got.External.Revision != binding.Revision {
		t.Fatal("non-policy request metadata prevented a retained process from pinning its actual revision")
	}
}

func TestExternalMergeRequiredRestartUsesCurrentRevision(t *testing.T) {
	for _, reason := range []string{"instructions", "permissions", "tools"} {
		t.Run(reason, func(t *testing.T) {
			f, launcher, execution, update := externalAssistantFixture(t)
			execution.ConfigDir = t.TempDir()
			id, cwd := execution.External.SessionID, t.TempDir()
			old := externalMergeStart(t, f, launcher, cwd, execution)
			controlTranscript(t, f, id)
			binding := *execution.External
			binding.Revision = "sha256:" + strings.Repeat("2", 64)
			execution.External = &binding
			update(binding.Revision)
			f.n.external.launchers(t.Context(), true)
			params := externalMergeParams(launcher, execution, "continue with current connection settings")
			switch reason {
			case "instructions":
				if err := os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), []byte("Use the updated conversation instructions.\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				params["permission_mode"] = proto.ModeBypass
			case "tools":
				old.mu.Lock()
				old.toolSet = "limited-tools"
				old.mu.Unlock()
			}
			if _, err := nodeCall(t, f.n, "assistant.send", params); err != nil {
				t.Fatalf("restart for %s retained a stale external revision: %v", reason, err)
			}
			f.idle(t, id)
			p := f.proc(id)
			if p == nil || p == old || p.cmd.Process.Pid == old.cmd.Process.Pid {
				t.Fatalf("required %s restart retained the old process", reason)
			}
			p.mu.Lock()
			actual := p.model
			p.mu.Unlock()
			if actual.External == nil || *actual.External != binding || actual.ConfigDir != execution.ConfigDir {
				t.Fatal("new process lost the current binding or selected profile")
			}
		})
	}
}

func TestExternalMergeCatalogRevisionKeepsActualProcess(t *testing.T) {
	f, launcher, execution, update := externalAssistantFixture(t)
	execution.ConfigDir = t.TempDir()
	execution.SupervisionInstructions, execution.SupervisionResolved = "Keep the same authorized policy.", true
	id, cwd := execution.External.SessionID, t.TempDir()
	p := externalMergeStart(t, f, launcher, cwd, execution)
	state, _ := f.n.state.get(id)
	keeper := newAssistantKeeper()
	f.n.keeper = keeper
	f.n.handOverAssistants()
	f.n.external.Close()
	t.Cleanup(keeper.stopAll)
	binding := *execution.External
	binding.Revision = "sha256:" + strings.Repeat("2", 64)
	execution.External = &binding
	update(binding.Revision)

	next := codexTestNode(t)
	next.home, next.dir, next.bin, next.keeper = f.n.home, f.n.dir, f.n.bin, keeper
	next.state.put(id, state)
	g := &cliFixture{n: next, fh: withFakeHub(t, next), dir: f.dir}
	externalMergeModelReply(g, launcher, execution)
	var err error
	next.external, err = loadExternalAdapterRegistry()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.external.Close)
	t.Cleanup(func() { next.stopAssistant(id) })
	if adopted := keeper.adopt(next); len(adopted) != 1 || adopted[0] != p {
		t.Fatal("resident handover lost the native external process")
	}
	resolved, current := next.assistantModel(context.Background(), id)
	if resolved != launcher || current.External == nil || current.External.Revision != binding.Revision ||
		!current.SupervisionResolved || current.SupervisionInstructions != execution.SupervisionInstructions {
		t.Fatal("resident reconnect did not retrieve current trusted authority and role policy")
	}
	if _, warm := next.external.lookup(launcher); warm {
		t.Fatal("resident fixture prewarmed the new connection's registry")
	}
	if _, err := nodeCall(t, next, "session.send", externalMergeParams(resolved, current, "after resident reconnect")); err != nil {
		t.Fatal(err)
	}
	g.idle(t, id)
	// An omitted policy inherits the same saved semantic value before revision pinning.
	current.SupervisionInstructions, current.SupervisionResolved = "", false
	if _, err := nodeCall(t, next, "session.send", externalMergeParams(resolved, current, "with the inherited policy")); err != nil {
		t.Fatal(err)
	}
	g.idle(t, id)
	if g.proc(id) != p || !p.alive() || p.ownerOr(f.n) != next {
		t.Fatal("a catalog-only change restarted the resident native process")
	}
	p.mu.Lock()
	actual := p.model
	p.mu.Unlock()
	if actual.External == nil || actual.External.Revision == binding.Revision || actual.ConfigDir != state.ConfigDir ||
		actual.SupervisionInstructions != execution.SupervisionInstructions {
		t.Fatal("resident send replaced the actual revision, profile or role with catalog metadata")
	}
	if report := next.assistantExecution(t.Context(), id); report.State != proto.ExecutionRunning || report.Source != "native" || report.Harness != "claude" {
		t.Fatal("resident execution metadata stopped describing the actual native process")
	}
}
