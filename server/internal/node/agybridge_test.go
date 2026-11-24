package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestAgyBridgeWakesOnExternalDisconnect(t *testing.T) {
	oldFind, oldCmd, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery
	t.Cleanup(func() {
		findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery = oldFind, oldCmd, oldProbe
	})
	agyBridgeProbeEvery = 30 * time.Second
	findAgyExecutable = func() string { return "agy" }
	synctest.Test(t, func(t *testing.T) {
		start := make(chan struct{}, 1)
		agyBridgeCommand = func(context.Context, *RuntimeHost, string, string) (*exec.Cmd, error) {
			start <- struct{}{}
			return nil, errors.New("test companion")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		host := NewRuntimeHost("test", "runtime.sock")
		client, hello := startAgyHello(t, ctx, host, "external")
		if err := <-hello; err != nil {
			t.Fatal(err)
		}
		host.setAgyBridge(true)
		defer host.setAgyBridge(false)
		synctest.Wait()
		select {
		case <-start:
			t.Fatal("started while external agy was connected")
		default:
		}
		_ = client.Close()
		select {
		case <-start:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("builtin agy did not resume within 100ms of external disconnect")
		}
	})
}

func startAgyHello(t *testing.T, ctx context.Context, host *RuntimeHost, instance string) (net.Conn, <-chan error) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go host.serve(ctx, server)
	peer := rpc.New("test", client)
	go peer.Serve(ctx, client)
	hello := make(chan error, 1)
	go func() {
		hello <- peer.Call(ctx, "runtime.hello", map[string]any{"runtime": "agy", "instance": instance}, nil)
	}()
	return client, hello
}

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
	agyBridgeCommand = func(ctx context.Context, rh *RuntimeHost, agyBin, instance string) (*exec.Cmd, error) {
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

func TestAgyBridgeYieldsWhenExternalRuntimeConnectsAfterStart(t *testing.T) {
	t.Run("settled", func(t *testing.T) { testAgyBridgeHandoff(t, false) })
	t.Run("deadline", func(t *testing.T) { testAgyBridgeHandoff(t, true) })
}

func testAgyBridgeHandoff(t *testing.T, expires bool) {
	t.Helper()
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
	instances := make(chan string, 4)
	agyBridgeCommand = func(ctx context.Context, rh *RuntimeHost, agyBin, instance string) (*exec.Cmd, error) {
		instances <- instance
		script := fmt.Sprintf("echo %s >> %q; trap 'echo stopped >> %q; exit 0' TERM; while :; do sleep 0.02; done", agyBin, marks, marks)
		return exec.Command("/bin/sh", "-c", script), nil
	}

	n, hub := runtimeNode(t)
	assistantHub(hub)
	if expires {
		n.runtimeHost.agy.yieldGrace = 100 * time.Millisecond
	}

	// Start the built-in companion first and register its socket connection as the supervised instance.
	n.runtimeHost.setAgyBridge(true)
	defer n.runtimeHost.setAgyBridge(false)
	builtinInstance := <-instances
	waitFor(t, "built-in agy companion started", func() bool { return len(starts(marks)) == 1 })

	builtinHello := assistantRuntimeHello()
	builtinHello["runtime"], builtinHello["agent"], builtinHello["launcher"], builtinHello["instance"] = "agy", "agy", "agy", builtinInstance
	builtin := dialRuntime(t, n, builtinHello)
	n.runtimeHost.reg.mu.Lock()
	handle := n.runtimeHost.reg.byRuntime(agyRuntimeID)
	n.runtimeHost.reg.mu.Unlock()
	const session = "active"
	setStatus := func(status string) {
		t.Helper()
		if err := builtin.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{{SessionID: session, Status: status}}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "session status "+status, func() bool {
			handle.mu.Lock()
			defer handle.mu.Unlock()
			return len(handle.sessions) == 1 && handle.sessions[0].Status == status
		})
	}
	setStatus(proto.StatusWorking)
	callCtx, cancelCall := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelCall()
	var reply chan struct{}
	var callDone <-chan error
	if !expires {
		reply, callDone = holdAgyCall(t, callCtx, builtin, handle)
	}

	started := time.Now()
	external, hello := startAgyHello(t, callCtx, n.runtimeHost, "external")
	if !expires {
		waitWithin(t, time.Second, "builtin agy stopped admitting new calls", func() bool {
			err := handle.peer.Call(callCtx, "test.probe", nil, nil)
			if errors.Is(err, rpc.ErrSuspended) {
				return true
			}
			if err != nil {
				t.Fatalf("builtin agy disconnected during a pending call: %v", err)
			}
			return false
		})
		close(reply)
		if err := <-callDone; err != nil {
			t.Fatalf("pending builtin call was interrupted: %v", err)
		}
		setStatus(proto.StatusIdle)
	}
	if err := <-hello; err != nil {
		t.Fatalf("external runtime registration: %v", err)
	}
	if expires && time.Since(started) < n.runtimeHost.agy.yieldGrace {
		t.Fatal("external runtime registered before the grace window elapsed")
	}

	waitWithin(t, time.Second, "built-in agy companion yielded and stopped", func() bool {
		got := starts(marks)
		return len(got) == 2 && got[1] == "stopped"
	})
	probesBefore := probes.Load()
	waitFor(t, "probed while external agy runtime stayed connected", func() bool { return probes.Load() >= probesBefore+2 })
	if got := starts(marks); len(got) != 2 {
		t.Fatalf("built-in companion restarted while external runtime was connected: %v", got)
	}
	n.runtimeHost.reg.mu.Lock()
	active := n.runtimeHost.reg.byRuntime(agyRuntimeID)
	var activeInst string
	if active != nil {
		active.mu.Lock()
		activeInst = active.hello.Instance
		active.mu.Unlock()
	}
	n.runtimeHost.reg.mu.Unlock()
	if activeInst != "external" {
		t.Fatalf("active agy instance = %q, want external", activeInst)
	}

	// Once the external runtime disconnects, the built-in supervisor resumes.
	_ = external.Close()
	waitFor(t, "built-in agy companion resumed after external disconnect", func() bool { return len(starts(marks)) == 3 })
	n.runtimeHost.setAgyBridge(false)
	if got := starts(marks); len(got) != 4 || got[3] != "stopped" {
		t.Fatalf("resumed companion was not stopped cleanly: %v", got)
	}
}

func holdAgyCall(t *testing.T, ctx context.Context, builtin *fakeRuntime, handle *runtimeConn) (chan struct{}, <-chan error) {
	t.Helper()
	started, reply := make(chan struct{}), make(chan struct{})
	builtin.mu.Lock()
	builtin.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "test.hold" {
			close(started)
			select {
			case <-reply:
			case <-builtin.peer.Done():
			}
		}
		return nil, nil
	}
	builtin.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- handle.peer.Call(ctx, "test.hold", nil, nil) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("builtin did not receive the pending call")
	}
	return reply, done
}

func TestAgyBridgeCancelsHandoffWhenExternalDisconnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		host := NewRuntimeHost("test", "runtime.sock")
		host.agy.instance = "builtin"
		builtin, hello := startAgyHello(t, ctx, host, "builtin")
		defer builtin.Close()
		if err := <-hello; err != nil {
			t.Fatal(err)
		}
		handle, _ := host.agyPeers("builtin")
		host.reg.setSessions(handle, []proto.Session{{SessionID: "active", Status: proto.StatusWorking}})
		external, hello := startAgyHello(t, ctx, host, "external")
		synctest.Wait()
		select {
		case err := <-hello:
			t.Fatalf("external runtime did not wait for the working session: %v", err)
		default:
		}
		if err := handle.peer.Call(ctx, "test.probe", nil, nil); !errors.Is(err, rpc.ErrSuspended) {
			t.Fatalf("builtin RPC during drain: %v, want suspended", err)
		}
		_ = external.Close()
		synctest.Wait()
		if err := handle.peer.Call(ctx, "test.probe", nil, nil); err == nil || err.Error() != "no handler for test.probe" {
			t.Fatalf("builtin RPC after canceled drain: %v", err)
		}
	})
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

func TestAgyProviderEnvFromManagedLauncher(t *testing.T) {
	n := &Node{}
	n.setCatalog([]proto.ManagedLauncher{
		{Name: "claude-gw", APIKey: "claude-key"},
		{Name: "b-agy", Agent: "agy", BaseURL: "https://gateway.example.com", APIKey: "key-b"},
		{Name: "a-agy", Agent: "agy", BaseURL: "https://gateway.example.com", APIKey: "key-a"},
		{Name: "agy-signin", Agent: "agy"},
	})
	env := n.agyProviderEnv()
	want := []string{"GOOGLE_GEMINI_BASE_URL=https://gateway.example.com", "GEMINI_API_KEY=key-a", "PERSONA_AGY_MODEL_PROVIDER=gemini"}
	if !slices.Equal(env, want) {
		t.Fatalf("provider env = %v, want %v", env, want)
	}
	n.setCatalog([]proto.ManagedLauncher{{Name: "agy-signin", Agent: "agy"}, {Name: "claude-gw", APIKey: "claude-key"}})
	if env := n.agyProviderEnv(); env != nil {
		t.Fatalf("a launcher without a key gives no provider, got %v", env)
	}
}

func TestAgyBridgeRestartsWhenTheProviderChanges(t *testing.T) {
	oldFind, oldCmd := findAgyExecutable, agyBridgeCommand
	t.Cleanup(func() { findAgyExecutable, agyBridgeCommand = oldFind, oldCmd })
	findAgyExecutable = func() string { return "agy" }
	envs := make(chan []string, 4)
	agyBridgeCommand = func(_ context.Context, rh *RuntimeHost, _, _ string) (*exec.Cmd, error) {
		envs <- rh.agyProviderEnv()
		return exec.Command("sleep", "30"), nil
	}
	host := NewRuntimeHost("test", filepath.Join(t.TempDir(), "runtime.sock"))
	host.logger = log.New(io.Discard, "", 0)
	host.setAgyBridge(true)
	defer host.setAgyBridge(false)
	next := func() []string {
		select {
		case env := <-envs:
			return env
		case <-time.After(10 * time.Second):
			t.Fatal("the companion was not (re)started")
			return nil
		}
	}
	if got := next(); got != nil {
		t.Fatalf("first start had provider env %v", got)
	}
	host.setAgyProviderEnv([]string{"GEMINI_API_KEY=k", "PERSONA_AGY_MODEL_PROVIDER=gemini"})
	if got := next(); len(got) != 2 {
		t.Fatalf("restart did not carry the provider env: %v", got)
	}
}

// A Persona-managed agy launcher with a key reaches a herdr pane the way a managed Claude launcher does: the pane
// types `personad launch <name>` (the name, never the key), and launch gets the launcher's environment from the node
// and starts agy with the Gemini provider setting (#13762).
func TestManagedAgyLauncherPaneStartAndResumeCarryNoSecret(t *testing.T) {
	const key = "pane-secret-key"
	l := proto.ManagedLauncher{Name: "agy-gw", Agent: "agy", BaseURL: "https://gateway.example.com", APIKey: key}
	n := &Node{bin: "/usr/local/bin/personad"}
	n.setCatalog([]proto.ManagedLauncher{l})

	env := managedEnv(l)
	if !slices.Equal(env, []string{"GOOGLE_GEMINI_BASE_URL=https://gateway.example.com", "GEMINI_API_KEY=" + key}) {
		t.Fatalf("launcher env = %v", env)
	}

	start := workAgentCommand(n.launcherCommand("agy-gw"), "agy", "gemini-4-neon-high", "high", proto.ModeBypass)
	if want := "/usr/local/bin/personad launch agy-gw --model gemini-4-neon-high --effort high --dangerously-skip-permissions"; start != want {
		t.Fatalf("start command = %q, want %q", start, want)
	}
	spec := agentLaunch{Agent: agyAgent, Launcher: "agy-gw", Model: "gemini-4-neon-high", Effort: "high", Mode: proto.ModeBypass, AgyConversation: "11111111-2222-3333-4444-555555555555"}
	argv, ok := n.agentResumeArgv(spec, "11111111-2222-3333-4444-555555555555")
	if !ok || !strings.Contains(argv[1], "launch agy-gw --model") || !strings.Contains(argv[1], "--conversation 11111111-2222-3333-4444-555555555555") {
		t.Fatalf("resume argv = %q, %v", argv, ok)
	}
	for _, typed := range []string{start, strings.Join(argv, " ")} {
		if strings.Contains(typed, key) || strings.Contains(typed, "gateway.example.com") {
			t.Fatalf("the pane's typed command carries the provider's secret or address: %q", typed)
		}
	}

	// What personad launch then execs: agy with the provider setting before the pane's own flags, the key in the env.
	got := launchArgv(agyAgent, []string{"--model", "gemini-4-neon-high", "--dangerously-skip-permissions"}, nil, "", "", env, "")
	want := []string{"agy", "--settings", `{"modelProvider":"gemini"}`, "--model", "gemini-4-neon-high", "--dangerously-skip-permissions"}
	if !slices.Equal(got, want) {
		t.Fatalf("launch argv = %q, want %q", got, want)
	}
	for _, a := range got {
		if strings.Contains(a, key) {
			t.Fatalf("argv carries the key: %q", got)
		}
	}
}

func TestCustomAntigravityRuntimeCoexistsWithBuiltinAgy(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)

	builtinHello := assistantRuntimeHello()
	builtinHello["runtime"], builtinHello["agent"], builtinHello["launcher"], builtinHello["instance"] = "agy", "agy", "agy", "builtin-1"
	_ = dialRuntime(t, n, builtinHello)

	customHello := assistantRuntimeHello()
	customHello["runtime"], customHello["agent"], customHello["launcher"], customHello["instance"] = "agy-wrap", "agy", "agy-wrap", "custom-1"
	customHello["name"] = "Antigravity Wrapped"
	customHello["launchers"] = []map[string]any{{"name": "agy-wrap", "agent": "agy", "display_name": "Antigravity Wrapped"}}
	_ = dialRuntime(t, n, customHello)

	if _, external := n.runtimeHost.agyPeers("builtin-1"); external {
		t.Fatal("custom runtime id agy-wrap must not be treated as an external agy peer that makes built-in yield")
	}
	if h := n.assistantRuntime("agy"); h == nil || h.hello.Runtime != "agy" {
		t.Fatalf("assistantRuntime(agy) = %v, want agy runtime", h)
	}
	if h := n.assistantRuntime("agy-wrap"); h == nil || h.hello.Runtime != "agy-wrap" {
		t.Fatalf("assistantRuntime(agy-wrap) = %v, want agy-wrap runtime", h)
	}
}

// writeAgy puts a new file at path the way an installer replaces one: written beside it, then renamed over it.
func writeAgy(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// The companion keeps the agy it started on until the node notices another (#14533): one replaced in place (an update)
// or installed elsewhere with the old one removed restarts it on the agy found now; an unchanged one never does.
func TestAgyBridgeRestartsOnANewAgyBinary(t *testing.T) {
	dir := t.TempDir()
	first, moved := filepath.Join(dir, "a", "agy"), filepath.Join(dir, "b", "agy")
	writeAgy(t, first, "v1")
	var where atomic.Value
	where.Store(first)
	oldFind, oldCmd, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery
	t.Cleanup(func() { findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery = oldFind, oldCmd, oldProbe })
	agyBridgeProbeEvery = 10 * time.Millisecond
	findAgyExecutable = func() string { return where.Load().(string) }
	started := make(chan string, 8)
	agyBridgeCommand = func(_ context.Context, _ *RuntimeHost, agyBin, _ string) (*exec.Cmd, error) {
		started <- agyBin
		return exec.Command("sleep", "30"), nil
	}
	host := NewRuntimeHost("test", filepath.Join(t.TempDir(), "runtime.sock"))
	logs := &syncBuffer{}
	host.logger = log.New(logs, "", 0)
	host.setAgyBridge(true)
	defer host.setAgyBridge(false)
	next := func(what string) string {
		t.Helper()
		select {
		case bin := <-started:
			return bin
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the companion was not (re)started; log:\n%s", what, logs.String())
			return ""
		}
	}
	if got := next("first start"); got != first {
		t.Fatalf("first start on %q, want %q", got, first)
	}
	select {
	case bin := <-started:
		t.Fatalf("restarted on an unchanged agy (%s); log:\n%s", bin, logs.String())
	case <-time.After(150 * time.Millisecond): // fifteen probes
	}

	writeAgy(t, first, "v2")
	if got := next("replaced in place"); got != first {
		t.Fatalf("restart after an update on %q, want %q", got, first)
	}
	if !strings.Contains(logs.String(), "agy at "+first+" was replaced; restarting") {
		t.Fatalf("the restart does not say why:\n%s", logs.String())
	}

	writeAgy(t, moved, "v3")
	where.Store(moved)
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if got := next("installed elsewhere"); got != moved {
		t.Fatalf("restart after a move on %q, want %q", got, moved)
	}
	if !strings.Contains(logs.String(), "agy moved from "+first+" to "+moved) {
		t.Fatalf("the restart does not say where agy went:\n%s", logs.String())
	}
}

// A new agy does not cut the companion's work: while one of its sessions is working the restart waits (the old file
// keeps running it), and it happens once the session is idle.
func TestAgyBridgeRestartWaitsForAWorkingSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	bin, marks := filepath.Join(dir, "agy"), filepath.Join(dir, "marks")
	writeAgy(t, bin, "v1")
	oldFind, oldCmd, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery
	t.Cleanup(func() { findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery = oldFind, oldCmd, oldProbe })
	agyBridgeProbeEvery = 10 * time.Millisecond
	findAgyExecutable = func() string { return bin }
	agyBridgeCommand = func(context.Context, *RuntimeHost, string, string) (*exec.Cmd, error) {
		script := fmt.Sprintf("echo start >> %q; trap 'echo stopped >> %q; exit 0' TERM; while :; do sleep 0.02; done", marks, marks)
		return exec.Command("/bin/sh", "-c", script), nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	host := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
	logs := &syncBuffer{}
	host.logger = log.New(logs, "", 0)
	host.setAgyBridge(true)
	defer host.setAgyBridge(false)
	waitFor(t, "the companion started", func() bool { return slices.Equal(starts(marks), []string{"start"}) })

	// the companion's own connection, with one session at work
	instance := host.builtinAgyInstance()
	client, hello := startAgyHello(t, ctx, host, instance)
	if err := <-hello; err != nil {
		t.Fatal(err)
	}
	var builtin *runtimeConn
	waitFor(t, "the companion registered", func() bool { builtin, _ = host.agyPeers(instance); return builtin != nil })
	status := func(s string) {
		builtin.mu.Lock()
		builtin.sessions = []proto.Session{{SessionID: "s1", Status: s}}
		builtin.mu.Unlock()
	}
	status(proto.StatusWorking)

	writeAgy(t, bin, "v2")
	waitFor(t, "the restart to wait for the session", func() bool {
		return strings.Contains(logs.String(), "agy at "+bin+" was replaced; restarting once no agy session is working")
	})
	if got := starts(marks); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("the companion was stopped while its session was working: %v", got)
	}

	status(proto.StatusIdle)
	waitFor(t, "the restart once the session is idle", func() bool { return slices.Contains(starts(marks), "stopped") })
	_ = client.Close() // the old companion's connection goes with it
	waitFor(t, "the companion started again", func() bool { m := starts(marks); return len(m) >= 3 && m[2] == "start" })
}

// What counts as a change: another path, another file at the path, none any more; the same one seen twice in a row.
func TestAgyChangeSettlesOnTwoProbes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agy")
	writeAgy(t, path, "v1")
	bin := statAgy(path)
	var c agyChange
	if c.see(bin, statAgy(path)) || c.seen != nil {
		t.Fatalf("the same file is a change: %+v", c)
	}
	writeAgy(t, path, "v2")
	if c.see(bin, statAgy(path)) {
		t.Fatal("one probe's change restarted")
	}
	if !c.see(bin, statAgy(path)) {
		t.Fatal("the same change on the next probe did not")
	}
	if c.see(bin, statAgy(path+"-gone")) || c.see(bin, statAgy("")) {
		t.Fatal("a different change must be seen twice of its own")
	}
	if !c.see(bin, statAgy("")) || c.describe(bin) != "agy at "+path+" is gone" {
		t.Fatalf("agy gone: %q", c.describe(bin))
	}
	if c.see(bin, bin) || c.seen != nil {
		t.Fatal("back to the companion's own agy is no change")
	}
}
