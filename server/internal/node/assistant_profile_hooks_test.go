package node

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// cliProfileTest uses the same CLI process as cliTest, but selects the login shell
// available on this machine instead of requiring zsh.
func cliProfileTest(t *testing.T) *cliFixture {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+fakeCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, ".zshrc"), filepath.Join(home, ".bashrc")} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"hasCompletedOnboarding":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	shell := "/bin/bash"
	if runtime.GOOS == "windows" {
		shell = loginShellProgram()
		if shell == "" {
			t.Skip("Git Bash is required for the assistant launcher")
		}
	}
	t.Setenv("SHELL", shell)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, key := range []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_MAX_CONTEXT_TOKENS", "ENABLE_TOOL_SEARCH"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	dir := t.TempDir()
	t.Setenv("CLI_FAKE_DIR", dir)
	n := codexTestNode(t)
	n.dir = t.TempDir()
	buf := &syncBuffer{}
	n.logger = log.New(buf, "", 0)
	fh := withFakeHub(t, n)
	t.Cleanup(func() {
		n.mu.Lock()
		ids := make([]string, 0, len(n.procs))
		for id := range n.procs {
			ids = append(ids, id)
		}
		spare := n.spare
		n.spare = nil
		n.mu.Unlock()
		var procs []*assistantProc
		for _, id := range ids {
			n.cancelAssistantRecovery(id)
			if p := n.takeAssistant(id); p != nil {
				procs = append(procs, p)
			}
		}
		if spare != nil {
			procs = append(procs, spare)
		}
		stopAndWait(procs)
	})
	return &cliFixture{n: n, fh: fh, dir: dir, log: buf}
}

// A new profile must have the existing opt-in hook before Claude's first
// startup. Other hooks and settings never leak into the private profile.
func TestAssistantNewProfileSeedsBypassHook(t *testing.T) {
	f := cliProfileTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	source := filepath.Join(n.home, ".claude")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	command := "'/opt/extension/0.2.30/bin/agent-bypass-allow' hook --config-dir '" + source + "' --data '/tmp/extension data' --persona-extension=claude-bypass-allow"
	settings := map[string]any{
		"env":         map[string]any{"DO_NOT_COPY": "yes"},
		"permissions": map[string]any{"defaultMode": "plan"},
		"hooks": map[string]any{"PermissionRequest": []any{
			map[string]any{"matcher": "Bash", "hooks": []any{
				map[string]any{"type": "command", "command": command, "timeout": 10},
				map[string]any{"type": "command", "command": "unrelated-hook", "timeout": 2},
			}},
			map[string]any{"matcher": "Write", "hooks": []any{
				map[string]any{"type": "command", "command": "another-unrelated-hook", "timeout": 2},
			}},
		}},
	}
	body, _ := json.MarshalIndent(settings, "", "  ")
	sourcePath := filepath.Join(source, "settings.json")
	if err := os.WriteFile(sourcePath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(sourcePath)
	id := "a5500000-0000-4000-8000-000000000558"
	p, err := n.spawnClaude(id, t.TempDir(), "claude", false, false, cliExec())
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()

	profilePath := filepath.Join(n.dir, "profiles", "claude", "settings.json")
	raw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("new assistant profile has no startup hook: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["hooks"] == nil {
		t.Fatalf("profile copied unrelated settings: %s", raw)
	}
	groups := got["hooks"].(map[string]any)["PermissionRequest"].([]any)
	if len(groups) != 1 {
		t.Fatalf("profile copied unrelated groups: %s", raw)
	}
	hooks := groups[0].(map[string]any)["hooks"].([]any)
	if len(hooks) != 1 {
		t.Fatalf("mixed group was not reduced to the tagged entry: %s", raw)
	}
	entry := hooks[0].(map[string]any)
	if entry["command"] != command || entry["timeout"] != float64(10) {
		t.Fatalf("tagged entry changed: %+v", entry)
	}
	after, _ := os.ReadFile(sourcePath)
	if string(after) != string(before) {
		t.Fatal("source settings were modified")
	}
}
