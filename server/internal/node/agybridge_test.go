package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgyBridgeSupervisesAndYieldsToExternalRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	marks := filepath.Join(dir, "starts")
	oldFind, oldCmd, oldMin, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeRestartMin, agyBridgeProbeEvery
	t.Cleanup(func() {
		findAgyExecutable, agyBridgeCommand = oldFind, oldCmd
		agyBridgeRestartMin, agyBridgeProbeEvery = oldMin, oldProbe
	})
	agyBridgeRestartMin, agyBridgeProbeEvery = 10*time.Millisecond, 10*time.Millisecond
	var probes atomic.Int32
	findAgyExecutable = func() string {
		probes.Add(1)
		return "/opt/agent/bin/agy"
	}
	agyBridgeCommand = func(ctx context.Context, rh *RuntimeHost, agyBin string) (*exec.Cmd, error) {
		script := fmt.Sprintf("echo %s >> %q; trap 'echo stopped >> %q; exit 0' TERM; while :; do sleep 0.02; done", agyBin, marks, marks)
		return exec.Command("/bin/sh", "-c", script), nil
	}

	n, hub := runtimeNode(t)
	assistantHub(hub)
	hello := assistantRuntimeHello()
	hello["runtime"], hello["agent"], hello["launcher"] = "agy", "agy", "agy"
	ext := dialRuntime(t, n, hello)

	// While an external agy runtime is connected, the built-in supervisor yields.
	n.runtimeHost.setAgyBridge(true)
	defer n.runtimeHost.setAgyBridge(false)
	waitFor(t, "probed while external agy runtime connected", func() bool { return probes.Load() >= 2 })
	if len(starts(marks)) != 0 {
		t.Fatalf("started while external agy runtime was connected: %v", starts(marks))
	}

	// Once the external runtime leaves, the built-in supervisor starts companion.mjs and stops cleanly.
	_ = ext.conn.Close()
	waitFor(t, "built-in agy companion started", func() bool { return len(starts(marks)) == 1 })
	if got := starts(marks); got[0] != "/opt/agent/bin/agy" {
		t.Fatalf("unexpected agy binary: %v", got)
	}
	n.runtimeHost.setAgyBridge(false)
	if got := starts(marks); len(got) != 2 || got[1] != "stopped" {
		t.Fatalf("companion was not stopped cleanly: %v", got)
	}
}

func TestAgyBridgeJSRuntimeRequiresSQLite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	writeFakeNode := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	noSQLite := writeFakeNode("node-20", "exit 1")
	t.Setenv("PERSONA_NODEJS", noSQLite)
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	if js, err := findAgyJSRuntime(context.Background()); err == nil && js.Path == noSQLite {
		t.Fatalf("accepted Node without node:sqlite: %+v", js)
	}

	withSQLite := writeFakeNode("node-22", "printf 22.14.0")
	t.Setenv("PERSONA_NODEJS", withSQLite)
	js, err := findAgyJSRuntime(context.Background())
	if err != nil || js.Path != withSQLite {
		t.Fatalf("findAgyJSRuntime = %+v, %v; want %s", js, err, withSQLite)
	}
}
