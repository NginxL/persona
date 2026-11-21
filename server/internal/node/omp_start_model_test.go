package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise normal session.start through the fake herdr pane and inspect the same start metadata the clients read.
func TestOmpStartUsesSelectedModel(t *testing.T) {
	fakeCLIHome(t)
	withPaneShell(t, "/bin/zsh")
	withPiFamilyGrace(t, 0)
	for _, tc := range []struct {
		name, model, prompt, command string
	}{
		{"default", "", "hello", "omp hello"},
		{"fuzzy", "opus", "", "omp --model opus"},
		{"qualified model and dash prompt", "example/quality[1m]", "- Summarize my notes", "omp --model 'example/quality[1m]' -- '- Summarize my notes'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			ompStartHerdr(f)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := requestCtx(t, ctx, n, "session.start", map[string]any{
				"cwd": home, "workspace_id": "w1", "launcher": "omp", "model": tc.model, "prompt": tc.prompt,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := call("pane.send_input", map[string]any{"pane_id": "w1:p2", "keys": []string{"Enter"},
				"text": inAgentEnv(n.dir, tc.command, shellQuote)})
			if got := f.trace("pane.send_input"); len(got) != 1 || got[0] != want {
				t.Fatalf("launch: %v, want %s", got, want)
			}
			s := result["session"].(map[string]any)
			st, ok := n.state.get(s["session_id"].(string))
			if !ok || st.Launcher != "omp" || st.Model != tc.model || s["requested_model"] != tc.model {
				t.Fatalf("model in start record %q and response %v, want %q", st.Model, s["requested_model"], tc.model)
			}
		})
	}
}

func TestOmpStartPassesModelAndPromptAsArguments(t *testing.T) {
	home := fakeCLIHome(t)
	withPaneShell(t, "/bin/zsh")
	withPiFamilyGrace(t, 0)
	fakeProgram(t, filepath.Join(home, "bin", "omp"), `printf '%s\0' "$@" >> "$OMP_TEST_ARGV"`)
	argv := filepath.Join(home, "argv")
	n, f, cwd := herdrNode(t)
	ompStartHerdr(f)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" {
			return
		}
		// Run the line sent to the pane while its start file still exists.
		line := c.Typed
		if line == "" {
			line = str(c.Params["text"])
		}
		shell := exec.CommandContext(ctx, "zsh", "-f", "-c", line)
		shell.Dir = cwd
		shell.Env = append(shell.Environ(), "OMP_TEST_ARGV="+argv)
		if out, err := shell.CombinedOutput(); err != nil {
			t.Errorf("running the pane command: %v\n%s", err, out)
		}
	})
	const model, prompt = "vendor/standard", "Please summarize these notes"
	if _, err := requestCtx(t, ctx, n, "session.start", map[string]any{
		"cwd": cwd, "workspace_id": "w1", "launcher": "omp", "model": model, "prompt": prompt,
	}); err != nil {
		t.Fatal(err)
	}
	received, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join([]string{"--model", model, "--", prompt, ""}, "\x00"); string(received) != want {
		t.Fatalf("omp received %q, want %q", received, want)
	}
}

func TestOmpModelChecksPrecedeTab(t *testing.T) {
	t.Run("invalid model ID", func(t *testing.T) {
		n, f, home := herdrNode(t)
		_, err := request(t, n, "session.start", map[string]any{"cwd": home, "launcher": "omp", "model": "two words"})
		if err == nil || !strings.Contains(err.Error(), "invalid model") || len(f.trace("tab.create")) != 0 {
			t.Fatalf("start error = %v, tabs = %v", err, f.trace("tab.create"))
		}
	})
	t.Run("launcher runs Codex", func(t *testing.T) {
		withCodexLaunchers(t, map[string][]string{"omp": {"codex"}})
		n, f, home := herdrNode(t)
		_, err := request(t, n, "session.start", map[string]any{"cwd": home, "launcher": "omp", "agent_type": "omp", "model": "opus"})
		if code, _ := refusalOf(t, err); code != "omp_options" || len(f.trace("tab.create")) != 0 {
			t.Fatalf("start error = %v, tabs = %v", err, f.trace("tab.create"))
		}
	})
	t.Run("unsupported host", func(t *testing.T) {
		n, herdr, orca := startNode(t)
		_, err := request(t, n, "session.start", map[string]any{"cwd": t.TempDir(), "launcher": "omp", "host": "orca", "model": "opus"})
		if code, _ := refusalOf(t, err); code != "omp_options" || herdr.asked() != 0 || orca.asked() != 0 {
			t.Fatalf("start error = %v, herdr = %d, Orca = %d", err, herdr.asked(), orca.asked())
		}
	})
	t.Run("managed launcher named omp", func(t *testing.T) {
		n, f, home := herdrNode(t)
		n.setManaged([]proto.ManagedLauncher{{Name: "omp", Agent: "omp", Kind: "api"}})
		got, err := request(t, n, "launchers.models", map[string]any{"name": "omp", "agent_type": "omp", "configured": true})
		if lock, _ := got["locked"].(string); err != nil || lock == "" || got["supports_start_model"] != nil {
			t.Fatalf("managed launcher models = %v, %v", got, err)
		}
		_, err = request(t, n, "session.start", map[string]any{"cwd": home, "launcher": "omp", "model": "opus"})
		if code, _ := refusalOf(t, err); code != "omp_options" || len(f.trace("tab.create")) != 0 {
			t.Fatalf("start error = %v, tabs = %v", err, f.trace("tab.create"))
		}
	})
}

func TestOmpModelPickerReportsAgent(t *testing.T) {
	fakeCLIHome(t)
	n, _ := testNode(t)
	for _, declared := range []string{"", "omp"} {
		got, err := request(t, n, "launchers.models", map[string]any{"name": "omp", "agent_type": declared, "configured": true})
		models, ok := got["models"].([]any)
		if err != nil || got["agent"] != "omp" || !ok || len(models) != 0 || got["locked"] != nil || got["supports_start_model"] != true {
			t.Fatalf("launcher models (declared %q) = %v, %v", declared, got, err)
		}
	}
	got, err := request(t, n, "launchers.model.check", map[string]any{"name": "omp", "model": "vendor/model", "configured": true})
	if err != nil || got["agent"] != "omp" {
		t.Fatalf("launcher model check = %v, %v", got, err)
	}
}

func TestOmpModelPickerLocksUnsupportedLaunch(t *testing.T) {
	home := fakeCLIHome(t)
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("alias omp='omp --model fixed-model'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, f, cwd := herdrNode(t)
	for _, tc := range []struct{ launcher, host string }{{"omp", "herdr"}, {"my-omp", "herdr"}, {"omp", "orca"}} {
		got, err := request(t, n, "launchers.models", map[string]any{"name": tc.launcher, "agent_type": "omp", "host": tc.host})
		if _, advertised := got["supports_start_model"]; err != nil || got["locked"] == nil || advertised {
			t.Errorf("%s on %s: launcher models = %v, %v", tc.launcher, tc.host, got, err)
		}
	}
	_, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "omp", "model": "other-model"})
	if err == nil || len(f.trace("tab.create")) != 0 {
		t.Fatalf("pinned alias start error = %v, tabs = %v", err, f.trace("tab.create"))
	}
}

func TestOmpStartModelCapabilityIsOptionalOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		wire      string
		supported bool
	}{
		{`{"agent":"omp","models":[]}`, false},
		{`{"agent":"omp","models":[],"supports_start_model":true}`, true},
	} {
		var response proto.LauncherModels
		if err := json.Unmarshal([]byte(tc.wire), &response); err != nil || response.SupportsStartModel != tc.supported {
			t.Fatalf("decoded %s as %+v: %v", tc.wire, response, err)
		}
		encoded, err := json.Marshal(response)
		if err != nil || string(encoded) != tc.wire {
			t.Fatalf("encoded %+v as %s: %v", response, encoded, err)
		}
	}
}

func TestOmpRuntimeKeepsItsOwnModelCatalog(t *testing.T) {
	fakeCLIHome(t)
	n, _ := runtimeNode(t)
	runtime := dialRuntime(t, n, map[string]any{"runtime": "omp-provider", "agent": "omp", "name": "omp",
		"launcher": "omp", "caps": map[string]any{"start": true, "models": true}})
	runtime.mu.Lock()
	runtime.answer = func(method string, _ json.RawMessage) (any, error) {
		return proto.LauncherModels{Agent: "omp", Models: []proto.AgentModel{{ID: "runtime-model"}}, SupportsStartModel: true}, nil
	}
	runtime.mu.Unlock()
	got, err := request(t, n, "launchers.models", map[string]any{"name": "omp"})
	models, _ := got["models"].([]any)
	if _, advertised := got["supports_start_model"]; err != nil || advertised || len(models) != 1 || len(runtime.got("launchers.models")) != 1 {
		t.Fatalf("runtime catalog = %v, %v", got, err)
	}
	if code, _ := refusalOf(t, n.checkOmpStartModel(t.Context(), "omp", "opus")); code != "omp_options" {
		t.Fatalf("runtime launcher accepted a native omp override: %s", code)
	}
}

func TestOmpResumeKeepsRecordedModel(t *testing.T) {
	const id = "01b10000-0000-7000-8000-000000000034"
	n, f, _, cwd, _ := stoppedOmpForStart(t, id)
	n.state.put(id, sessionState{Launcher: "omp", Model: "vendor/saved", Cwd: cwd})
	got, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "resume_session_id": id})
	if err != nil {
		t.Fatal(err)
	}
	s := got["session"].(map[string]any)
	if s["session_id"] != id || s["requested_model"] != "vendor/saved" || len(f.trace("tab.create")) != 1 {
		t.Fatalf("resumed session = %v, tabs = %v", s, f.trace("tab.create"))
	}
}
