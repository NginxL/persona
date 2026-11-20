package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension/egress"
)

// TestMain: this test binary is also the sandbox's first process (RunCommand) and the program it runs (probe).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case RunCommand:
			os.Exit(Run(os.Args[2:], os.Stderr))
		case AgentExecCommand:
			if err := RunAgentExec(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "probe":
			os.Exit(probeInside(os.Args[2:]))
		case "--version": // stands in for personad --version (agent_test.go)
			fmt.Println("personad test")
			os.Exit(0)
		case "agent-dial": // connects to a unix socket (agent_test.go)
			os.Exit(agentDial(os.Args[2]))
		case "agent-socket-read": // reads the listening socket's generation (agent_linux_test.go)
			out, err := readAgentSocket(os.Args[2])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Print(out)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

const homeFixtureName = ".persona-sandbox-home-fixture"

// probeInside tries what a sandboxed program may and may not do, and prints a line for each.
func probeInside(args []string) int {
	data, home := args[0], args[1]
	say := func(what string, err error) {
		if err != nil {
			fmt.Printf("%s: no\n", what)
		} else {
			fmt.Printf("%s: yes\n", what)
		}
	}
	say("write data", os.WriteFile(filepath.Join(data, "x"), []byte("x"), 0o600))
	say("write home", os.WriteFile(filepath.Join(home, ".persona-sandbox-probe"), []byte("x"), 0o600))
	say("write tmp", os.WriteFile(filepath.Join(os.TempDir(), "x"), []byte("x"), 0o600))
	_, err := os.ReadFile(filepath.Join(home, homeFixtureName))
	say("read home", err)
	c, err := net.DialTimeout("tcp", "192.0.2.1:80", 2*time.Second)
	if c != nil {
		c.Close()
	}
	say("dial out", err)
	for _, host := range []string{"allowed.test", "denied.test"} {
		resp, err := http.Get("http://" + host + "/")
		if err == nil && resp.StatusCode != 200 {
			err = fmt.Errorf("%d", resp.StatusCode)
		}
		if resp != nil {
			resp.Body.Close()
		}
		say("get "+host, err)
	}
	// A service on the hub machine's loopback: Go's net/http never sends a loopback address through the proxy, so
	// only LocalHost reaches it (#2942).
	if len(args) > 2 {
		port := args[2]
		for _, host := range []string{"127.0.0.1", egress.LocalHost} {
			resp, err := http.Get("http://" + net.JoinHostPort(host, port) + "/")
			if err == nil && resp.StatusCode != 200 {
				err = fmt.Errorf("%d", resp.StatusCode)
			}
			if resp != nil {
				resp.Body.Close()
			}
			say("get loopback as "+host, err)
		}
	}
	return 0
}

// testProxy is an egress proxy whose open internet is the test server at target; the loopback is the machine's own.
func testProxy(t *testing.T, target string) *egress.Proxy {
	t.Helper()
	px := &egress.Proxy{HubAddr: target, Upstream: func(*url.URL) (*url.URL, error) { return nil, nil },
		Resolve: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("192.0.2.1")}, nil },
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if h, _, _ := net.SplitHostPort(addr); h != "127.0.0.1" {
				addr = target
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}}
	if err := px.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(px.Close)
	return px
}

// localPort is a service on this machine's loopback, for LocalHost.
func localPort(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "local") }))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return port
}

func TestLinuxCommandLine(t *testing.T) {
	plan := Plan{ID: "notes", Command: "/x/pkg/bin/notes", Args: []string{"serve"}, Pkg: "/x/pkg", Data: "/x/data", Run: "/x/run",
		ProxySocket: "/x/run/proxy.sock", Self: "/usr/local/bin/personad", MemoryMB: 512, CPUPercent: 50,
		Env: []string{"XDG_RUNTIME_DIR=/run/user/1", "SECRET=1"}}
	name, args, env := Linux("/usr/bin/bwrap", plan)
	if name != "/usr/bin/bwrap" || slices.Contains(args, "systemd-run") {
		t.Fatalf("%s %v", name, args)
	}
	line := strings.Join(args, " ")
	for _, want := range []string{"--unshare-net", "--die-with-parent", "--ro-bind /x/pkg /x/pkg", "--bind /x/data /x/data",
		"--bind /x/run /x/run", "--ro-bind /usr/local/bin/personad " + SelfPath, "--setenv HOME /x/data",
		"-- " + SelfPath + " " + RunCommand + " --proxy-socket /x/run/proxy.sock --listen 127.0.0.1:3128 -- /x/pkg/bin/notes serve"} {
		if !strings.Contains(line, want) {
			t.Errorf("no %q in %s", want, line)
		}
	}
	if strings.Contains(line, os.Getenv("HOME")+" ") && os.Getenv("HOME") != "" {
		t.Errorf("the home directory is bound: %s", line)
	}
	if !slices.Contains(env, "HTTPS_PROXY=http://127.0.0.1:3128") || !slices.Contains(env, "NO_PROXY=") {
		t.Errorf("env %v", env)
	}
	plan.Scope = true
	name, args, env = Linux("/usr/bin/bwrap", plan)
	line = strings.Join(args, " ")
	if name != "systemd-run" || !strings.Contains(line, "--user --scope") || !strings.Contains(line, "MemoryMax=512M") ||
		!strings.Contains(line, "CPUQuota=50%") || !strings.Contains(line, "-- /usr/bin/bwrap --die-with-parent") {
		t.Fatalf("%s %s", name, line)
	}
	if !slices.Contains(env, "XDG_RUNTIME_DIR=/run/user/1") || slices.Contains(env, "SECRET=1") {
		t.Errorf("env %v", env)
	}
}

// TestProbeMountsAsStart: the probe runs true after the same bwrap options a start puts before its program, so a
// machine that cannot make a start's mounts (/proc, the tmpfs mounts, the read-only remount) has no sandbox.
func TestProbeMountsAsStart(t *testing.T) {
	plan := Plan{ID: "notes", Command: "/x/pkg/bin/notes", Pkg: "/x/pkg", Data: "/x/data", Run: "/x/run", Tools: "/x/tools",
		ProxySocket: "/x/run/proxy.sock", Self: "/usr/local/bin/personad"}
	setup := linuxSetup(plan)
	_, args, _ := Linux("/usr/bin/bwrap", plan)
	if len(args) <= len(setup) || !slices.Equal(args[:len(setup)], setup) || args[len(setup)] != "--" {
		t.Fatalf("a start's options are not the probe's:\n%v\n%v", setup, args)
	}
	line := strings.Join(setup, " ")
	for _, want := range []string{"--proc /proc", "--dev /dev", "--tmpfs /tmp", "--tmpfs /run", "--ro-bind /usr/local/bin/personad " + SelfPath,
		"--remount-ro /", "--chdir /x/pkg"} {
		if !strings.Contains(line, want) {
			t.Errorf("no %q in %s", want, line)
		}
	}
}

func TestDarwinProfile(t *testing.T) {
	data := t.TempDir()
	plan := Plan{Command: "/x/pkg/bin/notes", Pkg: "/x/pkg", Data: data, Run: "/x/run", ProxyURL: "http://ext:c@127.0.0.1:40001"}
	name, args, env := Darwin(plan)
	if name != "/usr/bin/sandbox-exec" || args[0] != "-p" || args[2] != "/x/pkg/bin/notes" {
		t.Fatalf("%s %v", name, args)
	}
	for _, want := range []string{"(deny network-outbound)", `(remote ip "localhost:40001")`, "(deny file-write*)", `(subpath "/x/pkg")`} {
		if !strings.Contains(args[1], want) {
			t.Errorf("no %s in\n%s", want, args[1])
		}
	}
	if !slices.Contains(env, "HTTPS_PROXY=http://ext:c@127.0.0.1:40001") || !slices.Contains(env, "HOME="+data) {
		t.Errorf("env %v", env)
	}
}

// TestLinuxSandboxForReal runs this binary in bubblewrap with the egress proxy: it writes its data, not the home
// directory, cannot dial out, and reaches through the proxy only the host it may, and the machine's loopback only as
// egress.LocalHost.
func TestLinuxSandboxForReal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	sup := Probe()
	if sup.Bwrap == "" {
		t.Skip("no sandbox here: " + sup.Why)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	target := strings.TrimPrefix(srv.URL, "http://")
	px := testProxy(t, target)
	port := localPort(t)
	px.Register("probe", egress.Rule{Hosts: []string{"allowed.test", "127.0.0.1"}})
	self, _ := os.Executable()
	run, data := t.TempDir(), t.TempDir()
	sock := filepath.Join(run, "proxy.sock")
	if err := px.ListenUnix("probe", sock); err != nil {
		t.Fatal(err)
	}
	// A sibling fixture, never the real user home or a parent made inside the sandbox for its mounts.
	home := t.TempDir()
	fixture := filepath.Join(home, homeFixtureName)
	const contents = "synthetic protected home contents"
	if err := os.WriteFile(fixture, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := Plan{ID: "probe", Command: self, Args: []string{"probe", data, home, port}, Pkg: filepath.Dir(self), Data: data, Run: run,
		ProxySocket: sock, Self: self}
	name, args, env := Linux(sup.Bwrap, plan)
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{"write data: yes", "write home: no", "write tmp: yes", "read home: no", "dial out: no", "get allowed.test: yes", "get denied.test: no",
		"get loopback as 127.0.0.1: no", "get loopback as " + egress.LocalHost + ": yes"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "x")); err != nil {
		t.Errorf("its data: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".persona-sandbox-probe")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sandbox wrote to the protected host fixture: %v", err)
	}
	if got, err := os.ReadFile(fixture); err != nil || string(got) != contents {
		t.Errorf("protected host fixture changed: %v", err)
	}
	if st := px.Stats("probe"); st.Allowed != 2 || st.Denied != 1 {
		t.Errorf("proxy counts %+v", st)
	}
}

func TestLinksOn(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "state", "extensions", "data"), 0o700))
	must(os.MkdirAll(filepath.Join(root, "dotfiles", "real"), 0o700))
	must(os.Symlink("state", filepath.Join(root, ".persona")))
	must(os.Symlink(filepath.Join(root, "dotfiles", "real"), filepath.Join(root, ".persona", "extensions", "data", "x")))
	got := linksOn(filepath.Join(root, ".persona", "extensions", "data", "x", "tmp"))
	// t.TempDir may itself be reached through a link (/var -> private/var on macOS); every link is named by its real
	// directory.
	real, err := filepath.EvalSymlinks(root)
	must(err)
	want := append(linksOn(root), filepath.Join(real, ".persona"), filepath.Join(real, "state", "extensions", "data", "x"))
	if !slices.Equal(got, want) {
		t.Errorf("linksOn = %v, want %v", got, want)
	}
	if got := linksOn(filepath.Join(real, "state", "extensions")); len(got) != 0 {
		t.Errorf("a real path has links %v", got)
	}
	if got := linksOn("relative/path"); got != nil {
		t.Errorf("relative %v", got)
	}
	must(os.Symlink("loop", filepath.Join(real, "loop")))
	if got := linksOn(filepath.Join(real, "loop", "x")); len(got) != 1 {
		t.Errorf("loop %v", got)
	}
}

// TestDarwinSandboxForReal runs a copy of this binary under sandbox-exec from a home directory whose state directory
// is a symlink to a user-chosen state directory: it runs, writes its data and temp directories, cannot read
// or write the rest of the home directory (also not through the link), cannot dial out, and reaches through the
// proxy only the host it may, and the machine's loopback only as egress.LocalHost (Go's http.Get straight to
// 127.0.0.1 skips the proxy and is refused, #2942).
func TestDarwinSandboxForReal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only")
	}
	if sup := Probe(); !sup.SandboxExec {
		t.Skip("no sandbox here: " + sup.Why)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".persona")
	pkg, data, run := filepath.Join(state, "extensions", "installed", "probe"), filepath.Join(state, "extensions", "data", "probe"),
		filepath.Join(state, "extensions", "run", "probe")
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("state", state); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{pkg, data, run} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The protected fixture is in the real state directory, named through the link.
	const contents = "synthetic protected home contents"
	fixture := filepath.Join(home, "state", homeFixtureName)
	if err := os.WriteFile(fixture, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(pkg, "probe")
	if err := os.WriteFile(command, bin, 0o700); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	target := strings.TrimPrefix(srv.URL, "http://")
	px := testProxy(t, target)
	port := localPort(t)
	proxyURL := px.Register("probe", egress.Rule{Hosts: []string{"allowed.test", "127.0.0.1"}})

	plan := Plan{ID: "probe", Command: command, Args: []string{"probe", data, state, port}, Pkg: pkg, Data: data, Run: run, ProxyURL: proxyURL}
	name, args, env := Darwin(plan)
	cmd := exec.Command(name, args...)
	cmd.Dir = pkg
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s\nprofile:\n%s", err, out, args[1])
	}
	got := string(out)
	for _, want := range []string{"write data: yes", "write home: no", "write tmp: yes", "read home: no", "dial out: no", "get allowed.test: yes", "get denied.test: no",
		"get loopback as 127.0.0.1: no", "get loopback as " + egress.LocalHost + ": yes"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "x")); err != nil {
		t.Errorf("its data: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, ".persona-sandbox-probe")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sandbox wrote to the protected home: %v", err)
	}
	if got, err := os.ReadFile(fixture); err != nil || string(got) != contents {
		t.Errorf("protected home fixture changed: %v", err)
	}
	if st := px.Stats("probe"); st.Allowed != 2 || st.Denied != 1 {
		t.Errorf("proxy counts %+v", st)
	}
}

// An extension's command-line programs (manifest.tools) are mounted read-only; without any nothing is added.
func TestToolsMounted(t *testing.T) {
	plan := Plan{ID: "notes", Command: "/x/pkg/bin/notes", Pkg: "/x/pkg", Data: "/x/data", Run: "/x/run", ProxySocket: "/x/run/p.sock",
		Self: "/usr/local/bin/personad", Tools: "/x/tools/notes"}
	_, args, _ := Linux("/usr/bin/bwrap", plan)
	if line := strings.Join(args, " "); !strings.Contains(line, "--ro-bind-try /x/tools/notes /x/tools/notes") {
		t.Fatal(line)
	}
	plan.Tools = ""
	if _, args, _ := Linux("/usr/bin/bwrap", plan); strings.Contains(strings.Join(args, " "), "/x/tools") {
		t.Fatal(args)
	}
	plan.Tools, plan.Data, plan.ProxyURL = "/x/tools/notes", t.TempDir(), "http://ext:c@127.0.0.1:40001"
	if _, args, _ := Darwin(plan); !strings.Contains(args[1], `(allow file-read* (subpath "/x/tools/notes"))`) {
		t.Fatal(args[1])
	}
}

func TestBwrapProbeArgs(t *testing.T) {
	pkg, data, run := "/tmp/test-pkg", "/tmp/test-data", "/tmp/test-run"
	args := bwrapProbeArgs(pkg, data, run, "/usr/bin/true")
	line := strings.Join(args, " ")
	for _, want := range []string{
		"--die-with-parent",
		"--proc /proc",
		"--dev /dev",
		"--tmpfs /tmp",
		"--tmpfs /run",
		"--ro-bind " + pkg + " " + pkg,
		"--bind " + data + " " + data,
		"--bind " + run + " " + run,
		"--remount-ro /",
		"--chdir " + pkg,
		"-- /usr/bin/true",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("want %q in probe args %s", want, line)
		}
	}
	for _, ns := range bwrapNamespaces() {
		if !slices.Contains(args, ns) {
			t.Errorf("missing namespace %q in %v", ns, args)
		}
	}
}
