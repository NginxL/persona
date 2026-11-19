package claudeplugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// TestLiveClaudeCLI drives the real Claude Code CLI in a throwaway config directory: set PERSONA_CLAUDE_PLUGIN_LIVE=1
// (and have `claude` on PATH, or PERSONA_CLAUDE_PLUGIN_LIVE=<path to claude>). The user's own ~/.claude is never used:
// the directory is a temporary one, so every command names it in CLAUDE_CONFIG_DIR (commandEnv).
func TestLiveClaudeCLI(t *testing.T) {
	live := os.Getenv("PERSONA_CLAUDE_PLUGIN_LIVE")
	if live == "" {
		t.Skip("set PERSONA_CLAUDE_PLUGIN_LIVE=1 to run against the real claude")
	}
	exe := live
	if live == "1" {
		var err error
		if exe, err = exec.LookPath("claude"); err != nil {
			t.Skip("no claude on PATH")
		}
	}
	root := t.TempDir()
	dir, data := filepath.Join(root, "cfg"), filepath.Join(root, "persona")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if SameDir(dir, userClaudeDir()) {
		t.Fatal("the throwaway directory is the user's own")
	}
	// Settings of the user's own that must survive every step.
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"dark","env":{"KEEP":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := ExecRunner(data)
	m := &Manager{DataDir: data, Bundle: bundle(t, "0.1.0"), Dirs: []string{dir}, Run: run, Logf: t.Logf, DefaultAuto: true,
		FindClaude: func(ctx context.Context) (Claude, bool) {
			return Claude{Exe: exe, Version: Version(ctx, run, exe, dir)}, true
		}}
	ctx := context.Background()
	step := func(op Op, want string) proto.ClaudePlugin {
		t.Helper()
		st, err := m.Do(ctx, op)
		if err != nil || st.State != want {
			t.Fatalf("%s: %+v %v", op, st, err)
		}
		return st
	}
	if st := step(OpCheck, proto.ClaudePluginInstalled); st.Version != "0.1.0" || st.ClaudeVersion == "" {
		t.Fatalf("installed %+v", st)
	}
	m.Bundle = bundle(t, "0.2.0")
	step(OpStatus, proto.ClaudePluginOutdated)
	if st := step(OpCheck, proto.ClaudePluginInstalled); st.Version != "0.2.0" {
		t.Fatalf("updated %+v", st)
	}
	// Turned off by the user with Claude Code's own command: left off, not updated.
	if err := (cli{run: run, exe: exe, dir: dir}).do(ctx, "plugin", "disable", id); err != nil {
		t.Fatalf("disable: %v", err)
	}
	m.Bundle = bundle(t, "0.3.0")
	if st := step(OpCheck, proto.ClaudePluginDisabled); st.Version != "0.2.0" {
		t.Fatalf("disabled %+v", st)
	}
	if st := step(OpInstall, proto.ClaudePluginInstalled); st.Version != "0.3.0" {
		t.Fatalf("install %+v", st)
	}
	step(OpUninstall, proto.ClaudePluginNotInstalled)
	var settings map[string]any
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || json.Unmarshal(b, &settings) != nil {
		t.Fatalf("settings.json: %s %v", b, err)
	}
	if settings["theme"] != "dark" || settings["env"].(map[string]any)["KEEP"] != "1" {
		t.Fatalf("the user's settings changed: %s", b)
	}
	if m, _ := settings["extraKnownMarketplaces"].(map[string]any); m[DefaultName] != nil {
		t.Fatalf("marketplace left in settings: %s", b)
	}
	if e, _ := settings["enabledPlugins"].(map[string]any); e[id] != nil {
		t.Fatalf("plugin left in settings: %s", b)
	}
	t.Logf("settings.json after uninstall: %s", b)
}
