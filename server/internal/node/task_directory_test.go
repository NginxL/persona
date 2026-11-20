package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskDirectoryValidationNeverCreates(t *testing.T) {
	n, _ := testNode(t)
	dir := t.TempDir()
	check := func(path string) (any, error) {
		raw, _ := json.Marshal(map[string]any{"path": path})
		return n.handle(context.Background(), "assistant.directory", raw)
	}
	if _, err := check(dir); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	if _, err := check(missing); err == nil {
		t.Fatal("missing directory accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("directory created: %v", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, "relative/path", dir + " "} {
		if _, err := check(path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("validation wrote files: %v %v", entries, err)
	}
}

func TestTaskResumeDoesNotChangeDirectoryOrLauncher(t *testing.T) {
	n, _ := testNode(t)
	first, second := t.TempDir(), t.TempDir()
	id := newUUID()
	n.state.put(id, sessionState{Cwd: first, Launcher: "claude"})
	for _, launcher := range []string{"", "claude"} {
		if err := n.checkTaskDirectory(id, first, launcher); err != nil {
			t.Fatalf("valid resume with launcher %q refused: %v", launcher, err)
		}
	}
	for _, tc := range []struct{ dir, launcher string }{{second, ""}, {second, "claude"}, {first, "codex"}} {
		if err := n.checkTaskDirectory(id, tc.dir, tc.launcher); err == nil {
			t.Fatalf("incompatible context accepted: %+v", tc)
		}
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if err := n.checkTaskDirectory(id, first, "claude"); err == nil {
		t.Fatal("missing directory accepted")
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("directory recreated: %v", err)
	}
}

func TestTaskExecutionSendValidatesDirectoryAtRPCBoundary(t *testing.T) {
	n, _ := testNode(t)
	missing := filepath.Join(t.TempDir(), "missing")
	raw, _ := json.Marshal(map[string]any{"task_execution": true, "cwd": missing, "launcher": "claude", "text": "check files"})
	if _, err := n.handle(context.Background(), "assistant.send", raw); err == nil {
		t.Fatal("assistant.send ignored task execution validation")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("directory was created: %v", err)
	}
}

func TestTaskDirectoryKeepsProjectSkillFiles(t *testing.T) {
	n, _ := testNode(t)
	dir := t.TempDir()
	before := filepath.Join(dir, ".claude", "skills", "project-skill")
	if err := os.MkdirAll(before, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(before, "SKILL.md")
	if err := os.WriteFile(file, []byte("# Project skill"), 0600); err != nil {
		t.Fatal(err)
	}
	env, release := n.assistantLarkEnv(dir, "")
	defer release()
	_ = env
	got, err := os.ReadFile(file)
	if err != nil || string(got) != "# Project skill" {
		t.Fatalf("project skill changed: %q %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(before))
	if err != nil || len(entries) != 1 {
		t.Fatalf("assistant skills installed in project: %v %v", entries, err)
	}
}
