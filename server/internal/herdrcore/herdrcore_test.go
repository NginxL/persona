//go:build !windows

// The fake herdr-core here listens on a unix socket, which the Windows client does not dial (socket_windows_test.go).

package herdrcore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSocketPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	for _, v := range []string{"HC_SOCKET", "HC_SESSION", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(v, "")
	}
	runUser := isDir(fmt.Sprintf("/run/user/%d", os.Getuid()))
	stateDefault := filepath.Join(home, ".local", "state", "herdr-core", "default", "default.sock")
	if !runUser {
		if got := unixSocketPath(); got != stateDefault {
			t.Errorf("default = %q, want %q", got, stateDefault)
		}
		t.Setenv("HC_SESSION", "iso")
		if got, want := unixSocketPath(), filepath.Join(home, ".local", "state", "herdr-core", "iso", "iso.sock"); got != want {
			t.Errorf("HC_SESSION = %q, want %q", got, want)
		}
		t.Setenv("XDG_STATE_HOME", "/tmp/st")
		if got, want := unixSocketPath(), "/tmp/st/herdr-core/iso/iso.sock"; got != want {
			t.Errorf("XDG_STATE_HOME = %q, want %q", got, want)
		}
	}
	t.Setenv("HC_SESSION", "")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/run")
	if got, want := unixSocketPath(), "/tmp/run/herdr-core/default.sock"; got != want {
		t.Errorf("XDG_RUNTIME_DIR = %q, want %q", got, want)
	}
	cfg := filepath.Join(home, ".config", "herdr-core")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("notes = false\nsocket = \"/tmp/cfg.sock\" # mine\n[other]\nsocket = \"/no\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := unixSocketPath(); got != "/tmp/cfg.sock" {
		t.Errorf("config socket = %q", got)
	}
	t.Setenv("HC_SOCKET", "/tmp/env.sock")
	if got := unixSocketPath(); got != "/tmp/env.sock" {
		t.Errorf("HC_SOCKET = %q", got)
	}
}

func TestConfigSocket(t *testing.T) {
	dir := t.TempDir()
	for body, want := range map[string]string{
		"socket = '/a.sock'\n":                 "/a.sock",
		"  socket=\"/b.sock\"\n":               "/b.sock",
		"[table]\nsocket = \"/c.sock\"\n":      "",
		"sockets = \"/d.sock\"\n":              "",
		"# socket = \"/e.sock\"\nshell=[]\n":   "",
		"history_mb = 5\nsocket = \"/f.sock\"": "/f.sock",
	} {
		p := filepath.Join(dir, "c.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := configSocket(p); got != want {
			t.Errorf("%q: %q, want %q", body, got, want)
		}
	}
}

// fakeCore answers one request per connection from fn (an *Error result is sent as an error), or keeps an
// events.subscribe connection for push.
type fakeCore struct {
	sock string
	fn   func(method string, params map[string]any) any
	mu   sync.Mutex
	subs []net.Conn
	sub  chan struct{}
}

func newFakeCore(t *testing.T, fn func(method string, params map[string]any) any) *fakeCore {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hcc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeCore{sock: filepath.Join(dir, "c.sock"), fn: fn, sub: make(chan struct{}, 4)}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		for _, c := range f.subs {
			c.Close()
		}
		f.mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeCore) serve(c net.Conn) {
	line, _ := bufio.NewReader(c).ReadBytes('\n')
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params map[string]any  `json:"params"`
	}
	_ = json.Unmarshal(line, &req)
	if req.Method == "events.subscribe" {
		f.send(c, map[string]any{"jsonrpc": "2.0", "method": "heartbeat", "params": map[string]any{"seq": 0}}) // before the reply: skipped
		f.send(c, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"seq": 0, "terminals": []any{}}})
		f.mu.Lock()
		f.subs = append(f.subs, c)
		f.mu.Unlock()
		f.sub <- struct{}{}
		return
	}
	defer c.Close()
	res := f.fn(req.Method, req.Params)
	// A notification on a call's connection is skipped too.
	f.send(c, map[string]any{"jsonrpc": "2.0", "method": "heartbeat", "params": map[string]any{}})
	if e, ok := res.(*Error); ok {
		f.send(c, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": e.Code, "message": e.Message,
			"data": map[string]any{"kind": e.Kind}}})
		return
	}
	f.send(c, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": res})
}

func (f *fakeCore) send(c net.Conn, msg any) {
	b, _ := json.Marshal(msg)
	_, _ = c.Write(append(b, '\n'))
}

func (f *fakeCore) push(method string, params any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.subs {
		f.send(c, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
}

func TestCalls(t *testing.T) {
	var got []string
	var mu sync.Mutex
	f := newFakeCore(t, func(method string, p map[string]any) any {
		b, _ := json.Marshal(p)
		mu.Lock()
		got = append(got, method+" "+string(b))
		mu.Unlock()
		switch method {
		case "terminal.list":
			return map[string]any{"terminals": []any{map[string]any{"address": "t1", "state": "live", "pid": nil,
				"parent":     "t0",
				"status":     map[string]any{"app": "claude-code", "state": "blocked", "kind": "permission"},
				"foreground": map[string]any{"pid": 42, "name": "2.1.0", "argv": []string{"claude"}, "cwd": "/x"}},
				map[string]any{"address": "m/t2", "state": "live"}}}
		case "terminal.read":
			return map[string]any{"text": "screen\n", "complete": true, "how": "screen"}
		case "terminal.send":
			return &Error{Code: -32000, Message: "blocked", Kind: "blocked"}
		case "hello":
			return map[string]any{"version": "0.1.16", "generation": 1}
		case "doc.get":
			return map[string]any{"value": map[string]any{"spaces": []any{}}, "version": 3}
		}
		return map[string]any{}
	})
	c := &Client{Socket: f.sock}
	ctx := context.Background()
	terms, err := c.List(ctx)
	if err != nil || len(terms) != 2 || terms[0].Status.Kind != "permission" || terms[0].Foreground.Pid != 42 ||
		terms[0].Parent != "t0" || terms[1].Parent != "" ||
		terms[0].Remote() || !terms[1].Remote() {
		t.Fatalf("List = %+v, %v", terms, err)
	}
	if text, err := c.Read(ctx, "t1", 30); err != nil || text != "screen\n" {
		t.Errorf("Read = %q, %v", text, err)
	}
	if err := c.Send(ctx, "t1", "hi"); !IsKind(err, "blocked") || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("Send = %v", err)
	}
	if err := c.Keys(ctx, "t1", "esc", "enter"); err != nil {
		t.Error(err)
	}
	if v := c.Version(ctx); v != "0.1.16" {
		t.Errorf("Version = %q", v)
	}
	var doc struct {
		Spaces []any `json:"spaces"`
	}
	if err := c.Doc(ctx, "herdr-app", &doc); err != nil || doc.Spaces == nil {
		t.Errorf("Doc = %+v, %v", doc, err)
	}
	want := []string{
		`terminal.list {}`,
		`terminal.read {"lines":30,"terminal":"t1"}`,
		`terminal.send {"terminal":"t1","text":"hi"}`,
		`terminal.keys {"keys":["esc","enter"],"terminal":"t1"}`,
		`hello {}`,
		`doc.get {"name":"herdr-app"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Not running: ErrNoSocket at once, nothing logged; a core that was there and stops answering is logged once, and
// again when it is back.
func TestAbsentIsQuiet(t *testing.T) {
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	c := &Client{Socket: "/tmp/persona-no-such-dir/c.sock", Log: logf}
	start := time.Now()
	for range 50 {
		if _, err := c.List(context.Background()); !errors.Is(err, ErrNoSocket) {
			t.Fatalf("List = %v", err)
		}
	}
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Errorf("50 absent calls took %s", took)
	}
	if err := c.Subscribe(context.Background(), func() {}, func(Event) {}); !errors.Is(err, ErrNoSocket) {
		t.Errorf("Subscribe = %v", err)
	}
	if len(logged) != 0 {
		t.Errorf("logged %q", logged)
	}
	if (&Client{}).Version(context.Background()) != "" {
		t.Error("no socket has a version")
	}

	f := newFakeCore(t, func(string, map[string]any) any { return map[string]any{"terminals": []any{}} })
	c.Socket = f.sock
	if _, err := c.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	os.Remove(f.sock)
	for range 3 {
		_, _ = c.List(context.Background())
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "terminal.list failed") {
		t.Errorf("logged %q", logged)
	}
}

func TestSubscribe(t *testing.T) {
	f := newFakeCore(t, nil)
	c := &Client{Socket: f.sock}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	events := make(chan Event, 8)
	done := make(chan error, 1)
	go func() {
		done <- c.Subscribe(ctx, func() { started <- struct{}{} }, func(e Event) { events <- e })
	}()
	<-f.sub
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	f.push("heartbeat", map[string]any{"seq": 1})
	f.push("event", map[string]any{"type": "status", "terminal": "t1", "seq": 2, "data": map[string]any{"status": nil}})
	f.push("missed", map[string]any{"seq": 5, "terminals": []any{}})
	for _, want := range []Event{{Type: "status", Terminal: "t1"}, {Type: "missed"}} {
		select {
		case e := <-events:
			if e != want {
				t.Errorf("event %+v, want %+v", e, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %+v", want)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Subscribe ended with %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not end with its context")
	}
}

// A socket file nobody listens on is not running (refused); only that and a missing file are.
func TestRefusedIsNotRunning(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hcr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close()
	if _, err := (&Client{Socket: sock}).List(context.Background()); !errors.Is(err, ErrNoSocket) {
		t.Errorf("refused: %v", err)
	}
}
