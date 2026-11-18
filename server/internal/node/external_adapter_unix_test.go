//go:build !windows

package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const externalAdapterHelperFlag = "-test.run=^TestExternalAdapterHelperProcess$"

type externalAdapterHelperState struct {
	Mode      string          `json:"mode"`
	Payload   json.RawMessage `json:"payload"`
	RawOutput string          `json:"raw_output"`
	BlockAddr string          `json:"block_addr"`
	Calls     string          `json:"calls"`
}

type externalAdapterHelperCall struct {
	Args  []string `json:"args"`
	Dir   string   `json:"dir"`
	Input string   `json:"input"`
	Mark  string   `json:"mark"`
}

// The test binary doubles as an adapter. Its inventory is wholly synthetic, and its exec
// records argv/stdin without starting a user CLI, reading a login, or contacting a model.
func TestExternalAdapterHelperProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[1] != externalAdapterHelperFlag {
		return
	}
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		os.Exit(91)
	}
	var state externalAdapterHelperState
	if json.Unmarshal(data, &state) != nil {
		os.Exit(92)
	}
	args := os.Args[3:]
	if args[0] == "hold-child" {
		externalAdapterHelperHold(state.BlockAddr)
		os.Exit(0)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(93)
	}
	dir, _ := os.Getwd()
	call := externalAdapterHelperCall{Args: args, Dir: dir, Input: string(input), Mark: os.Getenv("EXAMPLE_ADAPTER_MARK")}
	line, _ := json.Marshal(call)
	f, err := os.OpenFile(state.Calls, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(94)
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
	if args[0] == "exec" {
		_, _ = os.Stdout.Write(line)
		os.Exit(0)
	}
	if !slices.Equal(args, []string{"inventory", "--protocol", "1", "--offline"}) || len(input) != 0 {
		os.Exit(95)
	}
	switch state.Mode {
	case "hold":
		externalAdapterHelperHold(state.BlockAddr)
	case "child":
		child := exec.Command(os.Args[0], externalAdapterHelperFlag, os.Args[2], "hold-child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(96)
		}
	case "secret-stderr":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("EXAMPLE_SECRET_NEVER_REPORT ", externalAdapterStderrLimit))
		os.Exit(17)
	case "oversize":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", externalAdapterStdoutLimit+1))
		os.Exit(0)
	}
	if state.RawOutput != "" {
		_, _ = io.WriteString(os.Stdout, state.RawOutput)
	} else {
		_, _ = os.Stdout.Write(state.Payload)
	}
	os.Exit(0)
}

func externalAdapterHelperHold(address string) {
	c, err := net.DialTimeout("unix", address, 2*time.Second)
	if err != nil {
		os.Exit(97)
	}
	_, _ = io.WriteString(c, "ready\n")
	var release [1]byte
	_, _ = c.Read(release[:])
	_ = c.Close()
}

type externalAdapterFixture struct {
	dir        string
	configPath string
	statePath  string
	callsPath  string
	config     externalAdapterConfig
	state      externalAdapterHelperState
}

func newExternalAdapterFixture(t *testing.T) *externalAdapterFixture {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &externalAdapterFixture{dir: dir, configPath: filepath.Join(dir, "registry.json"),
		statePath: filepath.Join(dir, "fixture.json"), callsPath: filepath.Join(dir, "calls.jsonl")}
	f.config = externalAdapterConfig{Version: 1, ID: "local",
		Command: []string{self, externalAdapterHelperFlag, f.statePath}, NativeProgram: self}
	f.state = externalAdapterHelperState{Payload: externalAdapterTestJSON(t, externalAdapterTestInventory()), Calls: f.callsPath}
	f.saveState(t)
	f.saveConfig(t)
	t.Setenv(externalAdapterConfigEnv, f.configPath)
	return f
}

func (f *externalAdapterFixture) saveConfig(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.configPath, externalAdapterTestJSON(t, f.config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *externalAdapterFixture) saveState(t *testing.T) {
	t.Helper()
	next := f.statePath + ".next"
	if err := os.WriteFile(next, externalAdapterTestJSON(t, f.state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, f.statePath); err != nil {
		t.Fatal(err)
	}
}

func (f *externalAdapterFixture) load(t *testing.T) *externalAdapterRegistry {
	t.Helper()
	r, err := loadExternalAdapterRegistry()
	if err != nil || r == nil {
		t.Fatalf("load fixture: %v", err)
	}
	t.Cleanup(r.Close)
	return r
}

func (f *externalAdapterFixture) calls(t *testing.T) []externalAdapterHelperCall {
	t.Helper()
	data, err := os.ReadFile(f.callsPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []externalAdapterHelperCall
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var call externalAdapterHelperCall
		if json.Unmarshal(line, &call) != nil {
			t.Fatalf("invalid fake call: %q", line)
		}
		calls = append(calls, call)
	}
	return calls
}

func (f *externalAdapterFixture) hold(t *testing.T, mode string) *net.UnixListener {
	t.Helper()
	// Keep the socket below macOS's path bound, outside t.TempDir's long test name.
	dir, err := os.MkdirTemp("", "pea-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: filepath.Join(dir, "hold")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	_ = l.SetDeadline(time.Now().Add(5 * time.Second))
	f.state.Mode, f.state.BlockAddr = mode, l.Addr().String()
	f.saveState(t)
	return l
}

func externalAdapterAccept(t *testing.T, l *net.UnixListener) net.Conn {
	t.Helper()
	c, err := l.Accept()
	if err != nil {
		t.Fatalf("fake adapter did not enter hold: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("fake hold handshake = %q, %v", line, err)
	}
	return c
}

func externalAdapterWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for adapter operation")
		var zero T
		return zero
	}
}

func externalAdapterAssertClosed(t *testing.T, c net.Conn) {
	t.Helper()
	var b [1]byte
	if n, err := c.Read(b[:]); n != 0 || err != io.EOF {
		t.Fatalf("adapter process still holds its connection: %d, %v", n, err)
	}
}

func externalAdapterBinding(v proto.ExternalLauncher) *proto.ExternalExecution {
	return &proto.ExternalExecution{Scope: proto.ExternalMainChatScope, SessionID: "example-session",
		AdapterID: v.AdapterID, TargetID: v.ID, Revision: v.Revision}
}

func TestExternalAdapterLoadIsLocalAndDoesNotExecute(t *testing.T) {
	f := newExternalAdapterFixture(t)
	t.Setenv("PATH", "")
	r := f.load(t)
	if len(f.calls(t)) != 0 {
		t.Fatal("loading a registration executed its program")
	}
	if r.timeout != 5*time.Second || r.ttl != externalAdapterTTL {
		t.Fatal("default cache or execution bounds changed")
	}
	// Changing the environment after initialization cannot replace this registry.
	t.Setenv(externalAdapterConfigEnv, filepath.Join(f.dir, "not-present"))
	got := r.launchers(context.Background(), true)
	if len(got) != 1 || got[0].External == nil || got[0].Source != proto.ExternalLauncherSource ||
		got[0].External.AdapterID != "local" || got[0].External.Stale {
		t.Fatalf("launchers = %#v", got)
	}
	if calls := f.calls(t); len(calls) != 1 || !slices.Equal(calls[0].Args, []string{"inventory", "--protocol", "1", "--offline"}) {
		t.Fatalf("inventory calls = %#v", calls)
	}
}

func TestExternalAdapterRejectsInvalidLocalConfig(t *testing.T) {
	cases := map[string]func(*testing.T, *externalAdapterFixture){
		"relative registry": func(t *testing.T, _ *externalAdapterFixture) { t.Setenv(externalAdapterConfigEnv, "registry.json") },
		"missing registry": func(t *testing.T, f *externalAdapterFixture) {
			t.Setenv(externalAdapterConfigEnv, filepath.Join(f.dir, "missing.json"))
		},
		"directory registry": func(t *testing.T, f *externalAdapterFixture) { t.Setenv(externalAdapterConfigEnv, f.dir) },
		"FIFO registry": func(t *testing.T, f *externalAdapterFixture) {
			fifo := filepath.Join(f.dir, "registry.fifo")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(externalAdapterConfigEnv, fifo)
		},
		"link loop": func(t *testing.T, f *externalAdapterFixture) {
			link := filepath.Join(f.dir, "loop")
			if err := os.Symlink(link, link); err != nil {
				t.Fatal(err)
			}
			f.config.NativeProgram = link
			f.saveConfig(t)
		},
		"registry symlink": func(t *testing.T, f *externalAdapterFixture) {
			link := filepath.Join(f.dir, "link.json")
			if err := os.Symlink(f.configPath, link); err != nil {
				t.Fatal(err)
			}
			t.Setenv(externalAdapterConfigEnv, link)
		},
		"registry group writable": func(t *testing.T, f *externalAdapterFixture) {
			if err := os.Chmod(f.configPath, 0o660); err != nil {
				t.Fatal(err)
			}
		},
		"registry other writable": func(t *testing.T, f *externalAdapterFixture) {
			if err := os.Chmod(f.configPath, 0o602); err != nil {
				t.Fatal(err)
			}
		},
		"group writable ancestor": func(t *testing.T, f *externalAdapterFixture) {
			if err := os.Chmod(f.dir, 0o770); err != nil {
				t.Fatal(err)
			}
		},
		"unsafe ancestor": func(t *testing.T, f *externalAdapterFixture) {
			if err := os.Chmod(f.dir, 0o707); err != nil {
				t.Fatal(err)
			}
		},
		"registry invalid JSON": func(t *testing.T, f *externalAdapterFixture) {
			if err := os.WriteFile(f.configPath, []byte(`{"secret":"EXAMPLE_SECRET_NEVER_REPORT"`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"registry unknown field": func(t *testing.T, f *externalAdapterFixture) {
			data := strings.TrimSuffix(string(externalAdapterTestJSON(t, f.config)), "}") + `,"env":{"KEY":"private"}}`
			if err := os.WriteFile(f.configPath, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"registry oversize": func(t *testing.T, f *externalAdapterFixture) {
			if err := os.WriteFile(f.configPath, bytes.Repeat([]byte{'x'}, externalAdapterConfigLimit+1), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"version":  func(t *testing.T, f *externalAdapterFixture) { f.config.Version = 2; f.saveConfig(t) },
		"empty id": func(t *testing.T, f *externalAdapterFixture) { f.config.ID = ""; f.saveConfig(t) },
		"id injection": func(t *testing.T, f *externalAdapterFixture) {
			f.config.ID = "local --target another"
			f.saveConfig(t)
		},
		"empty command":   func(t *testing.T, f *externalAdapterFixture) { f.config.Command = nil; f.saveConfig(t) },
		"PATH executable": func(t *testing.T, f *externalAdapterFixture) { f.config.Command[0] = "bun"; f.saveConfig(t) },
		"relative script": func(t *testing.T, f *externalAdapterFixture) {
			f.config.Command = append(f.config.Command, "relative/adapter.ts")
			f.saveConfig(t)
		},
		"directory executable": func(t *testing.T, f *externalAdapterFixture) { f.config.Command[0] = f.dir; f.saveConfig(t) },
		"relative native":      func(t *testing.T, f *externalAdapterFixture) { f.config.NativeProgram = "claude"; f.saveConfig(t) },
		"missing native": func(t *testing.T, f *externalAdapterFixture) {
			f.config.NativeProgram = filepath.Join(f.dir, "missing-native")
			f.saveConfig(t)
		},
		"missing script": func(t *testing.T, f *externalAdapterFixture) {
			f.config.Command = append(f.config.Command, filepath.Join(f.dir, "missing-script"))
			f.saveConfig(t)
		},
		"non executable native": func(t *testing.T, f *externalAdapterFixture) {
			f.config.NativeProgram = f.statePath
			f.saveConfig(t)
		},
		"group writable script": func(t *testing.T, f *externalAdapterFixture) {
			if err := os.Chmod(f.statePath, 0o660); err != nil {
				t.Fatal(err)
			}
		},
		"unsafe script": func(t *testing.T, f *externalAdapterFixture) {
			if err := os.Chmod(f.statePath, 0o602); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newExternalAdapterFixture(t)
			change(t, f)
			r, err := loadExternalAdapterRegistry()
			if r != nil {
				r.Close()
				t.Fatal("invalid registration was loaded")
			}
			externalAdapterTestCode(t, err, "adapter_config_conflict")
			if len(f.calls(t)) != 0 {
				t.Fatal("invalid config executed its adapter")
			}
		})
	}
}

func TestExternalAdapterTrustedLinksAndDirectories(t *testing.T) {
	f := newExternalAdapterFixture(t)
	link := filepath.Join(f.dir, "program link")
	if err := os.Symlink(f.config.Command[0], link); err != nil {
		t.Fatal(err)
	}
	f.config.Command[0], f.config.NativeProgram = link, link
	f.saveConfig(t)
	alias := filepath.Join(f.dir, "trusted parent alias")
	if err := os.Symlink(f.dir, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv(externalAdapterConfigEnv, filepath.Join(alias, filepath.Base(f.configPath)))
	r := f.load(t)
	if r.config.Command[0] == link || r.config.NativeProgram == link || !filepath.IsAbs(r.config.NativeProgram) {
		t.Fatal("trusted executable links were not pinned to a canonical absolute path")
	}
	if got := r.launchers(context.Background(), true); len(got) != 1 {
		t.Fatal("trusted linked registration did not run")
	}
}

type externalAdapterFakeFileInfo struct {
	os.FileInfo
	stat syscall.Stat_t
	mode os.FileMode
}

func (i externalAdapterFakeFileInfo) Sys() any          { return &i.stat }
func (i externalAdapterFakeFileInfo) Mode() os.FileMode { return i.mode }
func (i externalAdapterFakeFileInfo) IsDir() bool       { return i.mode.IsDir() }

func TestExternalAdapterOwnerChecks(t *testing.T) {
	f := newExternalAdapterFixture(t)
	info, err := os.Stat(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		uid  uint32
		mode os.FileMode
		dir  bool
		want bool
	}{
		{"current owner", uint32(os.Geteuid()), 0o600, false, true},
		{"root owner", 0, 0o644, false, true},
		{"other owner", uint32(os.Geteuid()) + 10, 0o600, false, false},
		{"group writable", uint32(os.Geteuid()), 0o660, false, false},
		{"group writable ancestor", uint32(os.Geteuid()), os.ModeDir | 0o770, true, false},
		{"other writable", uint32(os.Geteuid()), 0o602, false, false},
		{"trusted temp ancestor", 0, os.ModeDir | os.ModeSticky | 0o777, true, true},
		{"unsafe ancestor", 0, os.ModeDir | 0o777, true, false},
		{"untrusted sticky ancestor", uint32(os.Geteuid()) + 10, os.ModeDir | os.ModeSticky | 0o777, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := externalAdapterFakeFileInfo{FileInfo: info, stat: syscall.Stat_t{Uid: tc.uid}, mode: tc.mode}
			if got := externalAdapterTrustedInfo(fake, tc.dir, false); got != tc.want {
				t.Fatalf("trusted = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExternalAdapterInventoryFailuresAreSanitized(t *testing.T) {
	for _, tc := range []struct {
		name, mode, payload, code string
	}{
		{"invalid JSON", "", `{"private":"EXAMPLE_SECRET_NEVER_REPORT"`, "adapter_inventory_invalid"},
		{"protocol", "", `{"protocol":2,"targets":[]}`, "adapter_protocol_unsupported"},
		{"oversize", "oversize", "", "adapter_inventory_invalid"},
		{"secret stderr", "secret-stderr", "", "adapter_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExternalAdapterFixture(t)
			f.state.Mode, f.state.RawOutput = tc.mode, tc.payload
			f.saveState(t)
			r := f.load(t)
			_, err := r.models(context.Background(), "missing", true)
			externalAdapterTestCode(t, err, tc.code)
			if got := r.launchers(context.Background(), false); len(got) != 0 {
				t.Fatal("failed inventory published targets")
			}
			if strings.Contains(fmt.Sprintf("%+v", err), "EXAMPLE_SECRET") {
				t.Fatal("raw adapter output escaped its error")
			}
		})
	}
}

func TestExternalAdapterCacheColdReadAndSingleFlight(t *testing.T) {
	f := newExternalAdapterFixture(t)
	l := f.hold(t, "hold")
	r := f.load(t)
	changed := make(chan struct{}, 4)
	r.SetOnChange(func() { changed <- struct{}{} })
	if got := r.launchers(context.Background(), false); len(got) != 0 {
		t.Fatal("cold cache unexpectedly has an entry")
	}
	c := externalAdapterAccept(t, l)
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			if got := r.launchers(context.Background(), false); len(got) != 0 {
				t.Error("held inventory should not publish yet")
			}
		})
	}
	wg.Wait()
	if calls := f.calls(t); len(calls) != 1 {
		t.Fatalf("concurrent cold reads executed %d inventories", len(calls))
	}
	_, _ = c.Write([]byte{'r'})
	externalAdapterWait(t, changed)
	got := r.launchers(context.Background(), false)
	if len(got) != 1 || got[0].External.Stale {
		t.Fatalf("background snapshot = %#v", got)
	}
	if len(f.calls(t)) != 1 {
		t.Fatal("fresh reads started another inventory")
	}
}

func TestExternalAdapterRefreshStaleRecoveryAndNotifications(t *testing.T) {
	f := newExternalAdapterFixture(t)
	r := f.load(t)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	r.now = func() time.Time { return time.Unix(0, clock.Load()) }
	r.ttl = 2 * time.Minute
	changed := make(chan struct{}, 8)
	r.SetOnChange(func() { changed <- struct{}{} })
	got := r.launchers(context.Background(), true)
	externalAdapterWait(t, changed)
	name := got[0].Name
	old, _ := r.lookup(name)
	if len(f.calls(t)) != 1 {
		t.Fatal("initial refresh did not run once")
	}
	// An unchanged explicit refresh runs once, without inventing a change notification.
	_ = r.launchers(context.Background(), true)
	select {
	case <-changed:
		t.Fatal("unchanged inventory notified")
	default:
	}
	if len(f.calls(t)) != 2 {
		t.Fatal("explicit refresh reused the TTL instead of inventory")
	}
	f.state.Mode = "secret-stderr"
	f.saveState(t)
	_, err := r.models(context.Background(), name, true)
	externalAdapterTestCode(t, err, "adapter_unavailable")
	externalAdapterWait(t, changed)
	stale, ok := r.lookup(name)
	if !ok || !stale.Stale || stale.Revision != old.Revision {
		t.Fatalf("failed refresh discarded or rewrote the snapshot: %#v", stale)
	}
	if _, err := r.command(name, externalAdapterBinding(old), old.Models[0].ID, f.dir, nil, nil); err == nil {
		t.Fatal("failed refresh still authorized a new process")
	}
	for range 10 {
		_ = r.launchers(context.Background(), false)
	}
	if len(f.calls(t)) != 3 {
		t.Fatal("failed inventory ignored background retry backoff")
	}
	models, err := r.models(context.Background(), name, false)
	if err != nil || !reflect.DeepEqual(models.Models, old.Models) {
		t.Fatal("stale models were not available to a fast directory read")
	}
	f.state.Mode = ""
	next := externalAdapterTestInventory()
	next.Targets[0].Revision = "sha256:" + strings.Repeat("b", 64)
	f.state.Payload = externalAdapterTestJSON(t, next)
	f.saveState(t)
	got = r.launchers(context.Background(), true) // an explicit retry bypasses failure backoff
	externalAdapterWait(t, changed)
	if len(got) != 1 || got[0].External.Stale || got[0].External.Revision == old.Revision {
		t.Fatalf("recovered snapshot = %#v", got)
	}
	_, err = r.command(name, externalAdapterBinding(old), old.Models[0].ID, f.dir, nil, nil)
	externalAdapterTestCode(t, err, "adapter_revision_mismatch")
	clock.Add(int64(r.ttl))
	if target, _ := r.lookup(name); !target.Stale {
		t.Fatal("expired snapshot was presented as fresh")
	}
	l := f.hold(t, "hold")
	fast := r.launchers(context.Background(), false)
	if len(fast) != 1 || !fast[0].External.Stale {
		t.Fatal("expired cache did not return immediately as stale")
	}
	c := externalAdapterAccept(t, l)
	_, _ = c.Write([]byte{'r'})
	externalAdapterWait(t, changed)
	if fresh, _ := r.lookup(name); fresh.Stale {
		t.Fatal("background refresh did not clear stale")
	}
}

func TestExternalAdapterCopiesSnapshotsAndSeparatesIdentities(t *testing.T) {
	f := newExternalAdapterFixture(t)
	v := externalAdapterTestInventory()
	price := 0.5
	v.Targets[0].Models[0].Price = &proto.ModelPrice{Input: 1, Output: 2, CacheRead: &price}
	v.Targets[0].Models[0].ServiceTiers = []proto.ServiceTier{{ID: "fast", Name: "Fast"}}
	twin := externalAdapterCloneTarget(v.Targets[0])
	twin.ID, twin.Models[0].ID = "another-target", "another-model"
	v.Targets = append(v.Targets, twin) // the same display name and revision are legitimate
	f.state.Payload = externalAdapterTestJSON(t, v)
	f.saveState(t)
	r := f.load(t)
	got := r.launchers(context.Background(), true)
	if len(got) != 2 || got[0].Name == got[1].Name || !proto.IsExternalLauncher(got[0].Name) {
		t.Fatal("display names collided in the external namespace")
	}
	name := got[0].Name
	got[0].External.Models[0].ID = "overwritten"
	got[0].External.Models[0].Efforts[0] = "overwritten"
	got[0].External.Models[0].ServiceTiers[0].ID = "overwritten"
	*got[0].External.Models[0].Price.CacheRead = 100
	got[0].External.Capabilities.Contexts[0] = "overwritten"
	lookup, _ := r.lookup(name)
	if !reflect.DeepEqual(lookup.ExternalTarget, v.Targets[0]) {
		t.Fatal("advertisement mutations changed the registry")
	}
	lookup.Models[0].ID = "overwritten-again"
	models, err := r.models(context.Background(), name, false)
	if err != nil || !reflect.DeepEqual(models.Models, v.Targets[0].Models) {
		t.Fatal("lookup mutations changed the model list")
	}
	for _, unknown := range []string{v.Targets[0].Name, "claude", "local", proto.ExternalLauncherName("another-adapter", v.Targets[0].ID)} {
		if _, ok := r.lookup(unknown); ok {
			t.Fatalf("resolved another source's identity %q", unknown)
		}
	}
}

func TestExternalAdapterCommandPreservesArgvAndInput(t *testing.T) {
	f := newExternalAdapterFixture(t)
	// A trusted absolute native path with spaces and apostrophes is data, not shell syntax.
	native := filepath.Join(f.dir, "native program's path")
	if err := os.WriteFile(native, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.config.NativeProgram = native
	f.saveConfig(t)
	r := f.load(t)
	launcher := r.launchers(context.Background(), true)[0]
	binding := externalAdapterBinding(*launcher.External)
	args := []string{"--print", "--mcp-config", `{"path":"a b","quote":"'\""}`, "", "$(touch should-not-exist)", "line one\nline two", "--"}
	env := append(os.Environ(), "EXAMPLE_ADAPTER_MARK=literal ' value")
	cmd, err := r.command(launcher.Name, binding, launcher.External.Models[0].ID, f.dir, args, env)
	if err != nil {
		t.Fatal(err)
	}
	want := append(slices.Clone(r.config.Command), "exec", "--protocol", "1", "--context", "main-chat", "--runtime", "cli",
		"--target", binding.TargetID, "--model", launcher.External.Models[0].ID, "--revision", binding.Revision,
		"--native-program", r.config.NativeProgram, "--")
	want = append(want, args...)
	if !slices.Equal(cmd.Args, want) || cmd.Dir != f.dir || !slices.Equal(cmd.Env, env) || cmd.Stdin != nil || cmd.Process != nil {
		t.Fatalf("prepared command did not preserve argv/cwd/env or started early: %#v", cmd)
	}
	args[0], env[0] = "changed", "CHANGED=1"
	if !slices.Equal(cmd.Args, want) || cmd.Env[0] == "CHANGED=1" {
		t.Fatal("caller mutation changed the prepared command")
	}
	input := "first prompt\nsecond prompt\n"
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var call externalAdapterHelperCall
	if json.Unmarshal(out, &call) != nil || !slices.Equal(call.Args, want[len(r.config.Command):]) ||
		call.Input != input || call.Mark != "literal ' value" {
		t.Fatalf("native argv/stdin/env roundtrip = %s", out)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("a literal argument ran as shell syntax")
	}
}

func TestExternalAdapterCommandRefusals(t *testing.T) {
	f := newExternalAdapterFixture(t)
	r := f.load(t)
	launcher := r.launchers(context.Background(), true)[0]
	valid := *externalAdapterBinding(*launcher.External)
	cases := []struct {
		name, launcher, model, code string
		binding                     *proto.ExternalExecution
	}{
		{"missing binding", launcher.Name, launcher.External.Models[0].ID, "adapter_purpose_unsupported", nil},
		{"wrong scope", launcher.Name, launcher.External.Models[0].ID, "adapter_purpose_unsupported", &proto.ExternalExecution{Scope: "agent-session", SessionID: "example"}},
		{"missing session", launcher.Name, launcher.External.Models[0].ID, "adapter_purpose_unsupported", &proto.ExternalExecution{Scope: proto.ExternalMainChatScope}},
		{"unknown external", proto.ExternalLauncherName("local", "missing"), launcher.External.Models[0].ID, "adapter_target_not_found", &valid},
		{"same name shell", launcher.External.Name, launcher.External.Models[0].ID, "adapter_target_not_found", &valid},
		{"model alias", launcher.Name, "default", "adapter_model_not_found", &valid},
		{"empty model", launcher.Name, "", "adapter_model_not_found", &valid},
		{"different adapter", launcher.Name, launcher.External.Models[0].ID, "adapter_target_not_found", func() *proto.ExternalExecution { b := valid; b.AdapterID = "other"; return &b }()},
		{"different target", launcher.Name, launcher.External.Models[0].ID, "adapter_target_not_found", func() *proto.ExternalExecution { b := valid; b.TargetID = "other"; return &b }()},
		{"different revision", launcher.Name, launcher.External.Models[0].ID, "adapter_revision_mismatch", func() *proto.ExternalExecution { b := valid; b.Revision = "older"; return &b }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := r.command(tc.launcher, tc.binding, tc.model, f.dir, []string{"--print"}, nil)
			if cmd != nil {
				t.Fatal("refused command was prepared")
			}
			externalAdapterTestCode(t, err, tc.code)
		})
	}
	if len(f.calls(t)) != 1 {
		t.Fatal("command validation executed or probed an adapter")
	}
}

func TestExternalAdapterCommandAvailabilityAndRevalidation(t *testing.T) {
	for _, tc := range []struct {
		name, credential, code string
	}{
		{"configured is not a health probe", "configured", ""},
		{"unknown is an explicit trial", "unknown", ""},
		{"missing is refused", "missing", "adapter_credentials_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExternalAdapterFixture(t)
			v := externalAdapterTestInventory()
			v.Targets[0].CredentialState = tc.credential
			f.state.Payload = externalAdapterTestJSON(t, v)
			f.saveState(t)
			r := f.load(t)
			launcher := r.launchers(context.Background(), true)[0]
			cmd, err := r.command(launcher.Name, externalAdapterBinding(*launcher.External), launcher.External.Models[0].ID, f.dir, nil, nil)
			if tc.code == "" {
				if err != nil || cmd == nil {
					t.Fatalf("trial could not be prepared: %v", err)
				}
			} else {
				externalAdapterTestCode(t, err, tc.code)
			}
			if len(f.calls(t)) != 1 {
				t.Fatal("credential state caused a shell or model probe")
			}
		})
	}
	f := newExternalAdapterFixture(t)
	r := f.load(t)
	launcher := r.launchers(context.Background(), true)[0]
	if err := os.Chmod(f.statePath, 0o602); err != nil {
		t.Fatal(err)
	}
	_, err := r.command(launcher.Name, externalAdapterBinding(*launcher.External), launcher.External.Models[0].ID, f.dir, nil, nil)
	externalAdapterTestCode(t, err, "adapter_config_conflict")
}

func TestExternalAdapterUnsupportedTargetsRemainVisible(t *testing.T) {
	f := newExternalAdapterFixture(t)
	v := externalAdapterTestInventory()
	v.Targets[0].Engine = "other-engine"
	v.Targets[0].Capabilities.Runtimes = []string{"agentsdk"}
	f.state.Payload = externalAdapterTestJSON(t, v)
	f.saveState(t)
	r := f.load(t)
	got := r.launchers(context.Background(), true)
	if len(got) != 1 || got[0].External.UnavailableCode != "adapter_runtime_unsupported" {
		t.Fatal("unsupported target was dropped or lacked a safe diagnostic")
	}
	target := *got[0].External
	_, err := r.command(got[0].Name, externalAdapterBinding(target), target.Models[0].ID, f.dir, nil, nil)
	externalAdapterTestCode(t, err, "adapter_runtime_unsupported")
	if len(f.calls(t)) != 1 {
		t.Fatal("unsupported target was shell-probed")
	}
}

func TestExternalAdapterRemovedTargetAndCanonicalPathReplacement(t *testing.T) {
	t.Run("removed target", func(t *testing.T) {
		f := newExternalAdapterFixture(t)
		r := f.load(t)
		old := r.launchers(context.Background(), true)[0]
		f.state.Payload = externalAdapterTestJSON(t, proto.ExternalInventory{Protocol: 1, Targets: []proto.ExternalTarget{}})
		f.saveState(t)
		if got := r.launchers(context.Background(), true); len(got) != 0 {
			t.Fatal("removed target remained advertised")
		}
		_, err := r.command(old.Name, externalAdapterBinding(*old.External), old.External.Models[0].ID, f.dir, nil, nil)
		externalAdapterTestCode(t, err, "adapter_target_not_found")
	})
	t.Run("canonical script replaced with a link", func(t *testing.T) {
		f := newExternalAdapterFixture(t)
		r := f.load(t)
		old := r.launchers(context.Background(), true)[0]
		moved := f.statePath + ".moved"
		if err := os.Rename(f.statePath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, f.statePath); err != nil {
			t.Fatal(err)
		}
		_, err := r.command(old.Name, externalAdapterBinding(*old.External), old.External.Models[0].ID, f.dir, nil, nil)
		externalAdapterTestCode(t, err, "adapter_config_conflict")
		_, err = r.models(context.Background(), old.Name, true)
		externalAdapterTestCode(t, err, "adapter_config_conflict")
		if len(f.calls(t)) != 1 {
			t.Fatal("a replaced script was executed")
		}
	})
}

// Done is evaluated only after an explicit refresh has joined the flight. This makes
// the cancellation test deterministic without sleeps or polling shared internal state.
type externalAdapterObservedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *externalAdapterObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestExternalAdapterCanceledWaiterDoesNotCancelOtherReaders(t *testing.T) {
	f := newExternalAdapterFixture(t)
	l := f.hold(t, "hold")
	r := f.load(t)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &externalAdapterObservedContext{Context: base, entered: make(chan struct{})}
	second := &externalAdapterObservedContext{Context: context.Background(), entered: make(chan struct{})}
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	name := proto.ExternalLauncherName(f.config.ID, externalAdapterTestInventory().Targets[0].ID)
	go func() { _, err := r.models(first, name, true); firstDone <- err }()
	c := externalAdapterAccept(t, l)
	externalAdapterWait(t, first.entered)
	go func() { _, err := r.models(second, name, true); secondDone <- err }()
	externalAdapterWait(t, second.entered)
	cancel()
	externalAdapterTestCode(t, externalAdapterWait(t, firstDone), "adapter_unavailable")
	_, _ = c.Write([]byte{'r'})
	if err := externalAdapterWait(t, secondDone); err != nil {
		t.Fatalf("canceling a joined waiter canceled the shared inventory: %v", err)
	}
	if len(f.calls(t)) != 1 {
		t.Fatal("joined refresh started another process")
	}
}

func TestExternalAdapterColdFailureBackoffAndRecovery(t *testing.T) {
	f := newExternalAdapterFixture(t)
	f.state.Mode = "secret-stderr"
	f.saveState(t)
	r := f.load(t)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	r.now = func() time.Time { return time.Unix(0, clock.Load()) }
	name := proto.ExternalLauncherName(f.config.ID, externalAdapterTestInventory().Targets[0].ID)
	_, err := r.models(context.Background(), name, true)
	externalAdapterTestCode(t, err, "adapter_unavailable")
	for range 12 {
		if len(r.launchers(context.Background(), false)) != 0 {
			t.Fatal("cold failure invented an advertisement")
		}
	}
	if len(f.calls(t)) != 1 {
		t.Fatal("cold failure was retried inside its backoff")
	}
	f.state.Mode = ""
	f.saveState(t)
	changed := make(chan struct{}, 1)
	r.SetOnChange(func() { changed <- struct{}{} })
	clock.Add(int64(externalAdapterRetryMin))
	_ = r.launchers(context.Background(), false)
	externalAdapterWait(t, changed)
	if got := r.launchers(context.Background(), false); len(got) != 1 || got[0].External.Stale {
		t.Fatal("retry after backoff did not recover the cold cache")
	}
	if len(f.calls(t)) != 2 {
		t.Fatal("background recovery did not run exactly once")
	}
}

func TestExternalAdapterCancellationAndCloseReapInventory(t *testing.T) {
	for _, operation := range []string{"cancel waiter", "close background", "timeout", "parent leaves child"} {
		t.Run(operation, func(t *testing.T) {
			f := newExternalAdapterFixture(t)
			mode := "hold"
			if operation == "parent leaves child" {
				mode = "child"
			}
			l := f.hold(t, mode)
			r := f.load(t)
			if operation == "timeout" {
				r.timeout = 2 * time.Second
			}
			done := make(chan error, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if operation == "close background" {
				_ = r.launchers(ctx, false)
			} else {
				go func() { _, err := r.models(ctx, "missing", true); done <- err }()
			}
			c := externalAdapterAccept(t, l)
			switch operation {
			case "cancel waiter":
				cancel()
				externalAdapterTestCode(t, externalAdapterWait(t, done), "adapter_unavailable")
			case "close background":
				r.Close()
			case "timeout":
				externalAdapterTestCode(t, externalAdapterWait(t, done), "adapter_timeout")
			case "parent leaves child":
				externalAdapterTestCode(t, externalAdapterWait(t, done), "adapter_unavailable")
			}
			externalAdapterAssertClosed(t, c)
			r.Close()
			if _, err := r.models(context.Background(), "missing", true); err == nil {
				t.Fatal("closed registry restarted its adapter")
			}
		})
	}
}
