//go:build unix

package node

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

func TestBackendWorkspaceCrashHelper(t *testing.T) {
	path := os.Getenv("PERSONA_WORKSPACE_CRASH_INPUT")
	if path == "" {
		t.Skip("subprocess helper")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var req taskworkspace.Request
	if err = json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: req.Plan.Node.Home, dir: req.Plan.Node.PersonaHome}
	workspaceCall(t, n, "prepare", req)
	t.Fatal("expected process to be killed at the Git boundary")
}
func TestBackendWorkspaceKilledNodeKeepsCommandLockAndRecovers(t *testing.T) {
	home := workspaceTemp(t)
	remote := workspaceRemote(t, "trunk")
	req := taskworkspace.Request{WorkspaceID: "ws_crash", OperationID: "op_crash", Plan: workspacePlan(t, home, repository.LayoutRoot, remote)}
	dir := workspaceTemp(t)
	input := filepath.Join(dir, "input.json")
	raw, _ := json.Marshal(req)
	os.WriteFile(input, raw, 0600)
	wrapper := filepath.Join(dir, "git")
	marker := filepath.Join(dir, "completed")
	release := filepath.Join(dir, "release")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$*" in
 *"worktree add"*)
  "$PERSONA_WORKSPACE_REAL_GIT" "$@" || exit "$?"
  kill -KILL "$PPID"
  : > "$PERSONA_WORKSPACE_CRASH_MARKER"
  attempts=0
  while [ ! -f "$PERSONA_WORKSPACE_CRASH_RELEASE" ] && [ "$attempts" -lt 500 ]; do sleep 0.02; attempts=$((attempts + 1)); done
  exit 0
  ;;
esac
exec "$PERSONA_WORKSPACE_REAL_GIT" "$@"
`
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.WriteFile(release, nil, 0600) })
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackendWorkspaceCrashHelper$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "PERSONA_WORKSPACE_CRASH_INPUT="+input, "PERSONA_WORKSPACE_REAL_GIT="+realGit, "PERSONA_WORKSPACE_CRASH_MARKER="+marker, "PERSONA_WORKSPACE_CRASH_RELEASE="+release)
	log, err := os.Create(filepath.Join(dir, "helper.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		if _, err = os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			cmd.Process.Kill()
			data, _ := os.ReadFile(log.Name())
			t.Fatalf("Git boundary not reached: %s", data)
		}
	}
	if err = <-exited; err == nil {
		t.Fatal("helper unexpectedly succeeded")
	}
	n := &Node{home: home, dir: req.Plan.Node.PersonaHome}
	busy := workspaceCall(t, n, "prepare", req)
	if busy.Error == nil || busy.Error.Code != "operation_busy" {
		t.Fatal("restarted Node replayed while Git still owned the lock", busy)
	}
	os.WriteFile(filepath.Join(req.Plan.RootPath, "keep.txt"), []byte("keep"), 0600)
	os.WriteFile(release, nil, 0600)
	for {
		r := workspaceCall(t, n, "prepare", req)
		if r.State == "ready" {
			if _, err = os.Stat(filepath.Join(req.Plan.RootPath, "keep.txt")); err != nil {
				t.Fatal(err)
			}
			if r.Repositories[0].ResolvedCommit == "" || !r.Repositories[0].Dirty {
				t.Fatal(r)
			}
			break
		}
		if r.Error == nil || r.Error.Code != "operation_busy" {
			t.Fatal(r)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("command lock was not released")
		}
	}
}
