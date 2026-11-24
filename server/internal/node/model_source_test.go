package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestClaudeModelSourceMergesLauncherHeaders(t *testing.T) {
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	fakeClaudeManagedSettings(t)
	n := codexTestNode(t)
	n.dir = t.TempDir()
	cwd := t.TempDir()
	own := filepath.Join(cwd, "provider.json")
	before := `{"env":{"ANTHROPIC_CUSTOM_HEADERS":"X-Team: blue\nx-persona-todo: todo_old","OTHER_ENV":"preserved"},"permissions":{"defaultMode":"default"}}`
	writeFile(t, own, before)
	n.setManaged([]proto.ManagedLauncher{{Name: "provider", BaseURL: "https://gw.example.com", Headers: "X-Team: green", Args: []string{"--settings", own}}})
	for _, role := range []string{"main", "leader", "routine", "task_agent", "background"} {
		path, err := n.sourceClaudeSettings(context.Background(), "provider", cwd, "", "", modelhttp.Source{Role: role, Todo: "todo_current"})
		if err != nil {
			t.Fatal(err)
		}
		settings, err := readLauncherSettings(path, cwd, n.home)
		if err != nil {
			t.Fatal(err)
		}
		env := settings["env"].(map[string]any)
		headers := env["ANTHROPIC_CUSTOM_HEADERS"].(string)
		if !strings.Contains(headers, "X-Team: blue") || !strings.Contains(headers, "X-Persona-Role: "+role) || !strings.Contains(headers, "X-Persona-Todo: todo_current") || strings.Contains(headers, "todo_old") || env["OTHER_ENV"] != "preserved" {
			t.Fatalf("bad generated settings: %v", env)
		}
	}
	if raw, _ := os.ReadFile(own); string(raw) != before {
		t.Fatal("user settings changed")
	}
}

func TestClaudeModelSourceUsesEffectiveAddress(t *testing.T) {
	fakeClaudeManagedSettings(t)
	n := codexTestNode(t)
	n.dir = t.TempDir()
	cwd := t.TempDir()
	for _, tc := range []struct {
		hosts, configured, effective string
		injected                     bool
	}{
		{"", "https://gw.example.com", "", false},
		{"*.example.com", "https://api.anthropic.com", "", false},
		{"*.example.com", "https://gw.example.com", "https://api.anthropic.com", false},
		{"*.example.com", "https://api.anthropic.com", "https://gw.example.com", true},
	} {
		t.Setenv(modelhttp.SourceHostsEnv, tc.hosts)
		env := ""
		if tc.effective != "" {
			env = "ANTHROPIC_BASE_URL=" + tc.effective
		}
		n.setManaged([]proto.ManagedLauncher{{Name: "same-name", BaseURL: tc.configured, Headers: "X-Team: blue", Env: env}})
		path, err := n.sourceClaudeSettings(context.Background(), "same-name", cwd, "", "", modelhttp.Source{Role: "main"})
		if err != nil {
			t.Fatal(err)
		}
		if (path != "") != tc.injected {
			t.Fatalf("injected=%v, want %v", path != "", tc.injected)
		}
	}
}

func TestClaudeAssistantStartCarriesKnownSessionSource(t *testing.T) {
	f := cliTest(t)
	f.fakeLaunch(t)
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	f.n.setManaged([]proto.ManagedLauncher{{Name: "gateway", BaseURL: "https://gw.example.com", Headers: "X-Team: blue"}})
	p, err := f.n.spawnClaude(testThread, t.TempDir(), "gateway", false, false, modelExecution{Source: modelhttp.Source{Role: "leader", Todo: "todo_123"}})
	if err != nil {
		t.Fatal(err)
	}
	defer f.n.stopAssistant(p.id)
	settings, err := readLauncherSettings(settingsArg(p.cmd.Args), p.cwd, f.n.home)
	if err != nil {
		t.Fatal(err)
	}
	headers := settings["env"].(map[string]any)["ANTHROPIC_CUSTOM_HEADERS"].(string)
	for _, line := range []string{"X-Team: blue", "X-Persona-Role: leader", "X-Persona-Todo: todo_123", "X-Persona-Session: " + testThread} {
		if !strings.Contains(headers, line) {
			t.Fatalf("missing %s", line)
		}
	}
}

func TestModelSourceChangesProcessFingerprintAndSurvivesResume(t *testing.T) {
	n := codexTestNode(t)
	n.dir = t.TempDir()
	e := modelExecution{Source: modelhttp.Source{Role: "main"}}
	first := n.executionFingerprint("claude", e)
	e.Source = modelhttp.Source{Role: "routine", Routine: "routine_1"}
	second := n.executionFingerprint("claude", e)
	if first == second {
		t.Fatal("source change reuses process")
	}
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	if second == n.executionFingerprint("claude", e) {
		t.Fatal("allowlist change reuses process")
	}
	n.state.put(testThread, sessionState{Launcher: "claude", Cwd: t.TempDir(), ModelSource: e.Source})
	resumed := n.resumeWith(testThread, sendOpts{})
	if resumed.execution.Source != e.Source {
		t.Fatalf("resume source=%+v", resumed.execution.Source)
	}
	raw, _ := json.Marshal(e)
	var restored modelExecution
	if json.Unmarshal(raw, &restored) != nil || restored.Source != e.Source {
		t.Fatal("source lost in model state")
	}
}
