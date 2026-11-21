package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/runtimepkg"
)

func startOpencodeHello(t *testing.T, ctx context.Context, host *RuntimeHost, runtimeID, instance string) (net.Conn, <-chan error) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go host.serve(ctx, server)
	peer := rpc.New("test", client)
	go peer.Serve(ctx, client)
	hello := make(chan error, 1)
	go func() {
		hello <- peer.Call(ctx, "runtime.hello", map[string]any{
			"runtime":  runtimeID,
			"agent":    "opencode",
			"name":     "opencode",
			"instance": instance,
		}, nil)
	}()
	return client, hello
}

func TestFindOpencodeExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable mode fixture")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	localBinDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	localBin := filepath.Join(localBinDir, "opencode")
	if err := os.WriteFile(localBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	envBin := filepath.Join(dir, "custom-opencode")
	if err := os.WriteFile(envBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	altBin := filepath.Join(dir, "alt-opencode")
	if err := os.WriteFile(altBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(dir, "empty-path"))
	t.Setenv("PERSONA_OPENCODE_BIN", envBin)
	t.Setenv("OPENCODE_BIN", altBin)
	if got := findOpencodeExecutable(); got != envBin {
		t.Fatalf("PERSONA_OPENCODE_BIN: got %q, want %q", got, envBin)
	}

	t.Setenv("PERSONA_OPENCODE_BIN", "")
	if got := findOpencodeExecutable(); got != altBin {
		t.Fatalf("OPENCODE_BIN: got %q, want %q", got, altBin)
	}

	t.Setenv("OPENCODE_BIN", "")
	if got := findOpencodeExecutable(); got != localBin {
		t.Fatalf("~/.local/bin/opencode: got %q, want %q", got, localBin)
	}
}

func TestOpencodeBridgeDisabledSwitch(t *testing.T) {
	oldFind, oldCmd, oldProbe := findOpencodeExecutable, opencodeBridgeCommand, opencodeBridgeProbeEvery
	t.Cleanup(func() {
		findOpencodeExecutable, opencodeBridgeCommand, opencodeBridgeProbeEvery = oldFind, oldCmd, oldProbe
	})
	opencodeBridgeProbeEvery = 30 * time.Second
	findOpencodeExecutable = func() string { return "opencode" }
	t.Setenv("PERSONA_BUILTIN_OPENCODE", "0")

	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{}, 1)
		opencodeBridgeCommand = func(context.Context, *RuntimeHost, string, string) (*exec.Cmd, error) {
			started <- struct{}{}
			return nil, errors.New("should not start")
		}
		host := NewRuntimeHost("test", "runtime.sock")
		host.setOpencodeBridge(true)
		defer host.setOpencodeBridge(false)
		synctest.Wait()
		select {
		case <-started:
			t.Fatal("started while PERSONA_BUILTIN_OPENCODE=0")
		default:
		}
	})
}

func TestOpencodeBridgeWakesOnExternalDisconnect(t *testing.T) {
	oldFind, oldCmd, oldProbe := findOpencodeExecutable, opencodeBridgeCommand, opencodeBridgeProbeEvery
	t.Cleanup(func() {
		findOpencodeExecutable, opencodeBridgeCommand, opencodeBridgeProbeEvery = oldFind, oldCmd, oldProbe
	})
	opencodeBridgeProbeEvery = 30 * time.Second
	findOpencodeExecutable = func() string { return "opencode" }
	t.Setenv("PERSONA_BUILTIN_OPENCODE", "")

	synctest.Test(t, func(t *testing.T) {
		start := make(chan struct{}, 1)
		opencodeBridgeCommand = func(context.Context, *RuntimeHost, string, string) (*exec.Cmd, error) {
			start <- struct{}{}
			return nil, errors.New("test companion")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		host := NewRuntimeHost("test", "runtime.sock")
		client, hello := startOpencodeHello(t, ctx, host, "opencode", "external")
		if err := <-hello; err != nil {
			t.Fatal(err)
		}
		host.setOpencodeBridge(true)
		defer host.setOpencodeBridge(false)
		synctest.Wait()
		select {
		case <-start:
			t.Fatal("started while external opencode companion was connected")
		default:
		}
		_ = client.Close()
		select {
		case <-start:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("builtin opencode did not resume within 100ms of external disconnect")
		}
	})
}

func TestOpencodeBridgeYieldsToServePlugin(t *testing.T) {
	oldFind, oldCmd, oldProbe := findOpencodeExecutable, opencodeBridgeCommand, opencodeBridgeProbeEvery
	t.Cleanup(func() {
		findOpencodeExecutable, opencodeBridgeCommand, opencodeBridgeProbeEvery = oldFind, oldCmd, oldProbe
	})
	opencodeBridgeProbeEvery = 30 * time.Second
	findOpencodeExecutable = func() string { return "opencode" }
	t.Setenv("PERSONA_BUILTIN_OPENCODE", "")

	synctest.Test(t, func(t *testing.T) {
		start := make(chan struct{}, 1)
		opencodeBridgeCommand = func(context.Context, *RuntimeHost, string, string) (*exec.Cmd, error) {
			start <- struct{}{}
			return nil, errors.New("test companion")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		host := NewRuntimeHost("test", "runtime.sock")
		plugin, hello := startOpencodeHello(t, ctx, host, "opencode-4242", "opencode-4242")
		if err := <-hello; err != nil {
			t.Fatal(err)
		}
		host.setOpencodeBridge(true)
		defer host.setOpencodeBridge(false)
		synctest.Wait()
		select {
		case <-start:
			t.Fatal("started while active opencode serve plugin was connected")
		default:
		}
		_ = plugin.Close()
		select {
		case <-start:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("builtin opencode did not resume within 100ms of plugin disconnect")
		}
	})
}

func TestOpencodeBridgeSupervisesAndYieldsToExternalAndPlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	marks := filepath.Join(dir, "starts")
	oldFind, oldCmd, oldMin, oldProbe := findOpencodeExecutable, opencodeBridgeCommand, opencodeBridgeRestartMin, opencodeBridgeProbeEvery
	t.Cleanup(func() {
		findOpencodeExecutable, opencodeBridgeCommand = oldFind, oldCmd
		opencodeBridgeRestartMin, opencodeBridgeProbeEvery = oldMin, oldProbe
	})
	opencodeBridgeRestartMin, opencodeBridgeProbeEvery = 10*time.Millisecond, 10*time.Millisecond
	t.Setenv("PERSONA_BUILTIN_OPENCODE", "")
	var probes atomic.Int32
	findOpencodeExecutable = func() string {
		probes.Add(1)
		return "/opt/agent/bin/opencode"
	}
	instances := make(chan string, 4)
	opencodeBridgeCommand = func(ctx context.Context, rh *RuntimeHost, opencodeBin, instance string) (*exec.Cmd, error) {
		instances <- instance
		script := fmt.Sprintf("echo %s >> %q; trap 'echo stopped >> %q; exit 0' TERM; while :; do sleep 0.02; done", opencodeBin, marks, marks)
		return exec.Command("/bin/sh", "-c", script), nil
	}

	n, hub := runtimeNode(t)
	assistantHub(hub)
	n.runtimeHost.setOpencodeBridge(true)
	defer n.runtimeHost.setOpencodeBridge(false)

	builtinInstance := <-instances
	waitFor(t, "built-in opencode companion started", func() bool { return len(starts(marks)) == 1 })
	if got := starts(marks); got[0] != "/opt/agent/bin/opencode" {
		t.Fatalf("unexpected opencode binary: %v", got)
	}

	// Register the built-in companion connection.
	builtinHello := assistantRuntimeHello()
	builtinHello["runtime"], builtinHello["agent"], builtinHello["launcher"], builtinHello["instance"] = "opencode", "opencode", "opencode", builtinInstance
	builtin := dialRuntime(t, n, builtinHello)
	defer builtin.conn.Close()

	// An active opencode serve plugin (`opencode-7777`) connects: built-in yields and stops cleanly.
	pluginHello := map[string]any{
		"runtime":  "opencode-7777",
		"agent":    "opencode",
		"name":     "opencode",
		"instance": "opencode-7777",
		"caps":     map[string]any{"send": true, "interrupt": true},
	}
	plugin := dialRuntime(t, n, pluginHello)
	waitWithin(t, time.Second, "built-in opencode companion yielded to plugin", func() bool {
		got := starts(marks)
		return len(got) == 2 && got[1] == "stopped"
	})
	// The yielded built-in runtime connection must be evicted so opencodeConnected does not misclassify it as external.
	waitWithin(t, time.Second, "yielded built-in opencode connection closed", func() bool {
		select {
		case <-builtin.peer.Done():
			return true
		default:
			return false
		}
	})

	// While plugin stays connected, built-in stays off.
	probesBefore := probes.Load()
	waitFor(t, "probed while opencode plugin connected", func() bool { return probes.Load() >= probesBefore+2 })
	if got := starts(marks); len(got) != 2 {
		t.Fatalf("built-in companion restarted while plugin connected: %v", got)
	}

	// Once plugin disconnects, built-in resumes.
	_ = plugin.conn.Close()
	waitFor(t, "built-in opencode companion resumed after plugin disconnect", func() bool { return len(starts(marks)) == 3 })
	n.runtimeHost.setOpencodeBridge(false)
	if got := starts(marks); len(got) != 4 || got[3] != "stopped" {
		t.Fatalf("resumed opencode companion was not stopped cleanly: %v", got)
	}
}

func TestOpencodeBridgeCommandExtractsAndPassesFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	pkgRoot := filepath.Join(dir, "pkgs", opencodePackage)
	for rel, body := range map[string]string{
		"opencode/package.json":     `{"type":"module"}`,
		"opencode/index.js":         "export default {}\n",
		"opencode/companion.mjs":    "console.log('ok')\n",
		"opencode/lib/companion.js": "export {}\n",
		"runtime-sdk/package.json":  `{"type":"module"}`,
		"runtime-sdk/index.js":      "export {}\n",
	} {
		p := filepath.Join(pkgRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fakeNode := filepath.Join(dir, "node-22")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\nprintf 22.19.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimepkg.OverrideEnv, filepath.Join(dir, "pkgs"))
	t.Setenv("PERSONA_NODEJS", fakeNode)

	sock := filepath.Join(dir, "state", "runtime.sock")
	rh := NewRuntimeHost("test", sock)
	cmd, err := opencodeBridgeCommand(context.Background(), rh, "/usr/local/bin/opencode", "builtin-opencode-1-1")
	if err != nil {
		t.Fatalf("opencodeBridgeCommand: %v", err)
	}
	wantPrefix := filepath.Join(dir, "state", "runtime-packages", opencodePackage) + string(filepath.Separator)
	if !strings.HasPrefix(cmd.Dir, wantPrefix) {
		t.Fatalf("cmd.Dir = %q, want prefix %q", cmd.Dir, wantPrefix)
	}
	if _, err := os.Stat(filepath.Join(cmd.Dir, "opencode", "companion.mjs")); err != nil {
		t.Fatalf("unpacked companion.mjs missing: %v", err)
	}
	for _, flag := range []string{"--socket", sock, "--bin", "/usr/local/bin/opencode", "--no-update", "--parent-stdin", "--instance", "builtin-opencode-1-1"} {
		if !slices.Contains(cmd.Args, flag) {
			t.Fatalf("cmd.Args %v missing %q", cmd.Args, flag)
		}
	}
}
