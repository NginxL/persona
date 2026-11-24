package node

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func TestResourceCLICallsUseVerifiedProcessSession(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires Unix peer credentials")
	}
	n, hub := runtimeNode(t)
	n.mu.Lock()
	n.procs["verified-session"] = &assistantProc{id: "verified-session", cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}}
	n.panes = map[string]string{"w1:p1": "another-session"}
	n.mu.Unlock()
	ask := mcpRelay(t, n, hub)
	for _, name := range []string{"resource_status", "resource_acquire", "resource_get", "resource_renew", "resource_release", "resource_cancel"} {
		got := ask(map[string]any{"kind": "cli.call", "name": name, "args": map[string]any{}, "pane": "w1:p1", "session_id": "forged"})
		if got["name"] != name || got["session_id"] != "verified-session" || got["assistant"] != true {
			t.Fatalf("%s did not use process identity: %v", name, got)
		}
	}
}
