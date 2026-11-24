// Package sandbox runs an extension's program walled off from the hub's machine (docs/extensions/extensions.md "Sandbox"): on Linux
// in bubblewrap (the system read-only, its package read-only, its own data and run directories writable, no network
// but the hub's egress proxy) inside a systemd user scope that caps its memory and CPU; on macOS under sandbox-exec.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/acceptloop"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/osproc"
)

// RunCommand is personad's hidden subcommand that is the sandbox's first process on Linux: it carries the proxy's
// unix socket to a port on the sandbox's own loopback, then runs the program.
const RunCommand = "__sandbox-run"

// ProxyPort is that port inside the sandbox (its network namespace is its own, so it is the same in every one).
const ProxyPort = 3128

// SelfPath is where personad is inside a Linux sandbox.
const SelfPath = "/run/persona/personad"

// Plan is one start of a program in a sandbox.
type Plan struct {
	ID      string
	Command string
	Args    []string
	// Pkg is the package (read-only), Data its own data (writable), Run its sockets (writable: its HTTP socket and
	// the proxy's, ProxySocket).
	Pkg, Data, Run string
	// Tools are its command-line programs (manifest.tools), read-only; "" when it has none.
	Tools       string
	ProxySocket string
	// ProxyURL (macOS) is the hub's proxy on loopback with the extension's credential; its port is the only one the
	// profile lets the program reach (Darwin).
	ProxyURL string
	// Self is personad (Linux: the sandbox's first process).
	Self string
	// MemoryMB and CPUPercent cap it (Linux, with a systemd user scope).
	MemoryMB, CPUPercent int
	// Scope: wrap it in systemd-run --user --scope for the caps.
	Scope bool
	// Env is the hub's environment, for what systemd-run needs to reach the user's manager.
	Env []string
	// Strict starts only after verifying independent kernel resource controls.
	// ReadOnly replaces broad system/package/data mounts with exact runtime files.
	Strict   bool
	ReadOnly []Mount
	// Development replaces only unavailable resource scopes in a tagged binary.
	// It never changes linuxSetup's strict mount/network namespace options.
	Development *DevelopmentLimits
}

type Mount struct {
	Source, Target string
	// DataFD is consumed by bubblewrap into a read-only mount. No artifact
	// pathname or writable descriptor is exposed in the guest namespace.
	DataFD int
}

// StatusReason identifies Persona-owned status text without deriving meaning from its rendered wording. Why remains
// the default-language string for logs and old readers; Reason lets a later reader render the same fact in its language.
type StatusReason struct {
	Key  string    `json:"key"`
	Args i18n.Args `json:"args,omitempty"`
}

// NewStatusReason makes a structured, localizable status reason. Its Error text is the legacy default wording.
func NewStatusReason(key string, args i18n.Args) *StatusReason {
	return &StatusReason{Key: key, Args: args}
}

// Error keeps the canonical default-language text used by logs and persisted why fields.
func (r *StatusReason) Error() string { return r.Text(i18n.Default) }

// Text renders the reason in lang. An unsupported requested language follows the product's English fallback.
func (r *StatusReason) Text(lang i18n.Lang) string {
	if r == nil {
		return ""
	}
	if lang != "" {
		lang = i18n.Resolve(string(lang))
	}
	return i18n.T(lang, r.Key, r.Args)
}

func statusWhy(lang i18n.Lang, why string, reason *StatusReason) string {
	if reason != nil && reason.Key != "" {
		return reason.Text(lang)
	}
	return why
}

// Support is what this machine can do.
type Support struct {
	OS          string
	Bwrap       string // its path ("" none)
	Scope       bool   // systemd-run --user --scope works
	SandboxExec bool
	Why         string        // why there is no sandbox, when there is none
	Reason      *StatusReason `json:"reason,omitempty"` // structured form of Persona-owned Why, when available
}

// WhyIn renders Persona-owned probe reasons in lang. A probe without metadata keeps its original diagnostic verbatim.
func (s Support) WhyIn(lang i18n.Lang) string { return statusWhy(lang, s.Why, s.Reason) }

func (s *Support) setReason(key string, args i18n.Args) {
	s.Reason = NewStatusReason(key, args)
	s.Why = s.Reason.Error()
}

// Available says whether programs can run in a sandbox here.
func (s Support) Available() bool {
	switch s.OS {
	case "linux":
		return s.Bwrap != ""
	case "darwin":
		return s.SandboxExec
	}
	return false
}

var (
	// probeMu guards the probe's cache; probeFn runs the probe itself (tests replace it).
	probeMu  sync.Mutex
	probeFn  = probe
	probed   Support
	probedAt time.Time
	probedOK bool
)

// probeRetryInterval is how long a failed probe is reused before it is tried again. A missing bwrap is the exception
// (see Probe): LookPath is cheap, so it is tried on every call and recovers the moment bwrap is installed.
const probeRetryInterval = 2 * time.Second

// Probe finds out what this machine can do. A machine that can sandbox is probed once. A machine that cannot is
// probed again at most every probeRetryInterval (a broken mount stays cached so list endpoints do not spawn bwrap over
// and over), except that a missing bwrap is checked on every call: LookPath costs microseconds and one installed a
// moment ago is picked up at once (#13540).
func Probe() Support {
	probeMu.Lock()
	defer probeMu.Unlock()
	if probedOK {
		return probed
	}
	if !probedAt.IsZero() && (probed.Reason == nil || probed.Reason.Key != "extension-status.sandbox_no_bwrap") && time.Since(probedAt) < probeRetryInterval {
		return probed
	}
	probed, probedAt, probedOK = probeFn(), time.Now(), false
	probedOK = probed.Available()
	return probed
}

func probe() Support {
	s := Support{OS: runtime.GOOS}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "linux":
		path, err := exec.LookPath("bwrap")
		if err != nil {
			s.setReason("extension-status.sandbox_no_bwrap", nil)
			return s
		}
		dir, err := os.MkdirTemp("", "persona-sandbox-probe-*")
		if err != nil {
			s.setReason("extension-sandbox.probe_tmp", i18n.Args{"err": err.Error()})
			return s
		}
		defer os.RemoveAll(dir)
		pkg, data, run := filepath.Join(dir, "pkg"), filepath.Join(dir, "data"), filepath.Join(dir, "run")
		for _, d := range []string{pkg, data, run} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				s.setReason("extension-sandbox.probe_tmp", i18n.Args{"err": err.Error()})
				return s
			}
		}
		trueBin := "true"
		if p, err := exec.LookPath("true"); err == nil {
			trueBin = p
		}
		out, err := exec.CommandContext(ctx, path, bwrapProbeArgs(pkg, data, run, trueBin)...).CombinedOutput()
		if err != nil {
			s.setReason("extension-status.sandbox_bwrap_start", i18n.Args{"err": firstLine(out, err)})
			return s
		}
		// The namespaces alone are not enough: a container or a restricted user namespace may let bwrap make them and
		// still refuse /proc, the tmpfs mounts or the read-only remount. Try the very mounts a start makes.
		if reason := probeMounts(ctx, path); reason != nil {
			s.Why, s.Reason = reason.Error(), reason
			return s
		}
		s.Bwrap = path
		if p, err := exec.LookPath("systemd-run"); err == nil {
			if exec.CommandContext(ctx, p, "--user", "--scope", "--quiet", "--collect", "--", "true").Run() == nil {
				s.Scope = true
			}
		}
	case "darwin":
		if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
			s.setReason("extension-status.sandbox_no_exec", nil)
			return s
		}
		if out, err := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
			s.setReason("extension-status.sandbox_exec_start", i18n.Args{"err": firstLine(out, err)})
			return s
		}
		s.SandboxExec = true
	default:
		s.setReason("extension-status.sandbox_os", i18n.Args{"os": runtime.GOOS})
	}
	return s
}

// probeMounts runs true in a sandbox set up exactly as Linux sets one up, with throwaway directories for the
// package, data, run directory and personad; nil when it starts, else a structured reason why not.
func probeMounts(ctx context.Context, bwrap string) *StatusReason {
	dir, err := os.MkdirTemp("", "persona-sandbox-probe-")
	if err != nil {
		return NewStatusReason("extension-status.sandbox_probe_dir", i18n.Args{"err": err.Error()})
	}
	defer os.RemoveAll(dir)
	plan := Plan{Pkg: filepath.Join(dir, "pkg"), Data: filepath.Join(dir, "data"), Run: filepath.Join(dir, "run"), Self: filepath.Join(dir, "self")}
	for _, d := range []string{plan.Pkg, plan.Data, plan.Run} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return NewStatusReason("extension-status.sandbox_probe_dir", i18n.Args{"err": err.Error()})
		}
	}
	if err := os.WriteFile(plan.Self, nil, 0o600); err != nil {
		return NewStatusReason("extension-status.sandbox_probe_dir", i18n.Args{"err": err.Error()})
	}
	if out, err := exec.CommandContext(ctx, bwrap, append(linuxSetup(plan), "--", "true")...).CombinedOutput(); err != nil {
		return NewStatusReason("extension-status.sandbox_mount", i18n.Args{"err": firstLine(out, err)})
	}
	return nil
}

func firstLine(out []byte, err error) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if line == "" {
		return err.Error()
	}
	return line
}

func bwrapNamespaces() []string {
	return []string{"--unshare-user-try", "--unshare-ipc", "--unshare-pid", "--unshare-net", "--unshare-uts", "--unshare-cgroup-try"}
}

// bwrapProbeArgs tests whether bwrap can create the same mount points as Linux(…).
func bwrapProbeArgs(pkg, data, run, trueBin string) []string {
	args := []string{"--die-with-parent"}
	args = append(args, bwrapNamespaces()...)
	for _, d := range systemDirs {
		args = append(args, "--ro-bind-try", d, d)
	}
	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/run",
		"--ro-bind", pkg, pkg,
		"--bind", data, data,
		"--bind", run, run,
		"--remount-ro", "/",
		"--chdir", pkg,
		"--", trueBin,
	)
	return args
}

// systemDirs are bound read-only into a Linux sandbox when they exist: the programs, libraries and settings (CA
// certificates, time zones) a program needs; never the user's home.
var systemDirs = []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/opt", "/nix/store"}

// Linux is the command line that runs plan in bubblewrap (in a systemd scope with plan.Scope), and the environment it
// adds. The program sees HTTP(S)_PROXY on its loopback (RunCommand carries it to plan.ProxySocket), HOME its data.
func Linux(bwrap string, plan Plan) (string, []string, []string) {
	launch := []string{SelfPath, RunCommand, "--proxy-socket", plan.ProxySocket, "--listen", "127.0.0.1:" + strconv.Itoa(ProxyPort)}
	if plan.Development != nil {
		launch = append(launch, "--development-limits")
		launch = append(launch, plan.Development.args()...)
	}
	launch = append(launch, "--", plan.Command)
	args := append(linuxSetup(plan), append([]string{"--"}, launch...)...)
	args = append(args, plan.Args...)
	proxy := "http://127.0.0.1:" + strconv.Itoa(ProxyPort)
	env := ProxyEnv(proxy)
	if plan.Strict {
		env = ProxyEnv("")
	} // the adapter explicitly addresses the fixed-identity broker
	if !plan.Scope {
		return bwrap, args, env
	}
	unit := fmt.Sprintf("persona-ext-%s-%d", plan.ID, time.Now().UnixNano())
	launcher := []string{bwrap}
	if plan.Strict {
		launcher = []string{plan.Self, RunCommand, "--resource-check", strconv.Itoa(plan.MemoryMB), strconv.Itoa(plan.CPUPercent), unit + ".scope", "--", bwrap}
	}
	scope := []string{"--user", "--scope", "--quiet", "--collect", "--unit", unit,
		"-p", "MemoryMax=" + strconv.Itoa(plan.MemoryMB) + "M", "-p", "MemorySwapMax=0",
		"-p", "CPUQuota=" + strconv.Itoa(plan.CPUPercent) + "%", "--"}
	scope = append(scope, launcher...)
	for _, kv := range plan.Env { // systemd-run reaches the user's manager with these; bwrap drops them again
		if strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") || strings.HasPrefix(kv, "DBUS_SESSION_BUS_ADDRESS=") {
			env = append(env, kv)
		}
	}
	return "systemd-run", append(scope, args...), env
}

// linuxSetup is bwrap's options for plan, everything before the program: Linux runs the program after them, probe
// runs true after them, so the probe tries what a start does.
func linuxSetup(plan Plan) []string {
	args := []string{"--die-with-parent"}
	if plan.Strict {
		args = append(args, "--unshare-user", "--unshare-ipc", "--unshare-pid", "--unshare-net", "--unshare-uts", "--cap-drop", "ALL",
			"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run",
			"--ro-bind", plan.Self, SelfPath, "--ro-bind", plan.ProxySocket, plan.ProxySocket)
		for _, m := range plan.ReadOnly {
			if m.DataFD > 0 {
				args = append(args, "--ro-bind-data", strconv.Itoa(m.DataFD), m.Target)
			} else {
				args = append(args, "--ro-bind", m.Source, m.Target)
			}
		}
		return append(args, "--setenv", "HOME", "/tmp", "--setenv", "TMPDIR", "/tmp", "--unsetenv", "DBUS_SESSION_BUS_ADDRESS", "--unsetenv", "XDG_RUNTIME_DIR", "--remount-ro", "/", "--chdir", "/tmp")
	}
	args = append(args, bwrapNamespaces()...)
	for _, d := range systemDirs {
		args = append(args, "--ro-bind-try", d, d)
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/run",
		"--ro-bind", plan.Pkg, plan.Pkg,
		"--bind", plan.Data, plan.Data,
		"--bind", plan.Run, plan.Run,
		"--ro-bind", plan.Self, SelfPath,
	)
	if plan.Tools != "" {
		args = append(args, "--ro-bind-try", plan.Tools, plan.Tools)
	}
	args = append(args,
		"--setenv", "HOME", plan.Data, "--setenv", "TMPDIR", "/tmp",
		"--unsetenv", "DBUS_SESSION_BUS_ADDRESS", "--unsetenv", "XDG_RUNTIME_DIR",
		// The sandbox's own root (the directories made for the mounts) read-only too: only its data, run and /tmp take
		// writes.
		"--remount-ro", "/",
		"--chdir", plan.Pkg)
	return args
}

// ProxyEnv points every common HTTP client at proxy, and nothing past it.
func ProxyEnv(proxy string) []string {
	return []string{"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy, "http_proxy=" + proxy, "https_proxy=" + proxy,
		"ALL_PROXY=" + proxy, "all_proxy=" + proxy, "NO_PROXY=", "no_proxy="}
}

// toolsRule lets the program read its command-line programs (manifest.tools), when it has any.
func toolsRule(tools string, real func(string) string) string {
	if tools == "" {
		return ""
	}
	return `(allow file-read* (subpath ` + strconv.Quote(real(tools)) + `))`
}

// Darwin is the command line that runs plan under sandbox-exec, and the environment it adds: no network but the hub's
// proxy on loopback, no writing but its data and run directories, no reading the home directory but its package,
// data and run directories.
func Darwin(plan Plan) (string, []string, []string) {
	home, _ := os.UserHomeDir()
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	port := "0"
	if i := strings.LastIndex(plan.ProxyURL, ":"); i >= 0 {
		port = plan.ProxyURL[i+1:]
	}
	tmp := filepath.Join(plan.Data, "tmp")
	_ = os.MkdirAll(tmp, 0o700)
	q := strconv.Quote
	// The hub names these directories, and the program and its arguments, by paths that may go through a symlink in
	// the home directory (~/.persona -> .persona). sandbox-exec checks the link itself as a read under the home
	// directory, so without these every such path fails (execvp: Operation not permitted). Each link is allowed on
	// its own (literal): reading it gives only where it points, and what is behind it is checked by its real path.
	var links []string
	for _, p := range []string{plan.Pkg, plan.Data, plan.Run, tmp, plan.Command, plan.Tools} {
		if p == "" {
			continue
		}
		for _, l := range linksOn(p) {
			if !slices.Contains(links, l) {
				links = append(links, l)
			}
		}
	}
	linkRule := ""
	if len(links) > 0 {
		lits := make([]string, len(links))
		for i, l := range links {
			lits[i] = "(literal " + q(l) + ")"
		}
		linkRule = "(allow file-read* " + strings.Join(lits, " ") + ")"
	}
	profile := strings.Join([]string{
		"(version 1)",
		"(allow default)",
		"(deny network-outbound)",
		`(allow network-outbound (remote ip ` + q("localhost:"+port) + `))`,
		`(allow network-outbound (remote unix-socket (subpath ` + q(real(plan.Run)) + `)))`,
		"(deny file-write*)",
		`(allow file-write* (subpath ` + q(real(plan.Data)) + `) (subpath ` + q(real(plan.Run)) + `) (subpath "/dev"))`,
		`(deny file-read* (subpath ` + q(real(home)) + `))`,
		`(allow file-read* (subpath ` + q(real(plan.Pkg)) + `) (subpath ` + q(real(plan.Data)) + `) (subpath ` + q(real(plan.Run)) + `))`,
		toolsRule(plan.Tools, real),
		linkRule,
	}, "\n")
	args := append([]string{"-p", profile, plan.Command}, plan.Args...)
	env := append(ProxyEnv(plan.ProxyURL), "HOME="+plan.Data, "TMPDIR="+tmp)
	return "/usr/bin/sandbox-exec", args, env
}

// linksOn is every symlink met on the way to the absolute path p, following them as the kernel does (a link's target
// may hold links too). Each is named by its real directory and its own name, as sandbox-exec sees it.
func linksOn(p string) []string {
	if !filepath.IsAbs(p) {
		return nil
	}
	var links []string
	at := "/"
	parts := strings.Split(p, "/")
	for hops := 0; len(parts) > 0; {
		name := parts[0]
		parts = parts[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			at = filepath.Dir(at)
			continue
		}
		next := filepath.Join(at, name)
		fi, err := os.Lstat(next)
		if err != nil {
			break
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			at = next
			continue
		}
		if hops++; hops > 40 { // a loop: the kernel gives up too
			break
		}
		if !slices.Contains(links, next) {
			links = append(links, next)
		}
		target, err := os.Readlink(next)
		if err != nil {
			break
		}
		if filepath.IsAbs(target) {
			at = "/"
		}
		parts = append(strings.Split(target, "/"), parts...)
	}
	return links
}

// Run is RunCommand: carry the sandbox's loopback port to the proxy's socket, run the program with this process's
// stdin, stdout and stderr, pass on the signals the hub sends, and exit as it does.
func Run(args []string, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "--development-exec" {
		return runDevelopmentExec(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "--resource-check" {
		if len(args) < 6 || args[4] != "--" {
			return 2
		}
		memory, mErr := strconv.Atoi(args[1])
		cpu, cErr := strconv.Atoi(args[2])
		if mErr != nil || cErr != nil {
			return 2
		}
		if err := VerifyResources(memory, cpu, args[3]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		cmd := exec.Command(args[5], args[6:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	var sock, listen string
	var dev []string
	for len(args) > 0 && args[0] != "--" {
		if args[0] == "--development-limits" {
			if !developmentResourcesBuilt || len(args) < 4 {
				return 2
			}
			dev, args = args[1:4], args[4:]
			continue
		}
		if len(args) < 2 {
			fmt.Fprintln(stderr, "sandbox: --proxy-socket <path> --listen <addr> -- <command> …")
			return 2
		}
		switch args[0] {
		case "--proxy-socket":
			sock = args[1]
		case "--listen":
			listen = args[1]
		default:
			fmt.Fprintf(stderr, "sandbox: unknown %s\n", args[0])
			return 2
		}
		args = args[2:]
	}
	if len(args) < 2 || sock == "" || listen == "" {
		fmt.Fprintln(stderr, "sandbox: --proxy-socket <path> --listen <addr> -- <command> …")
		return 2
	}
	ln, err := osproc.Listen("tcp", listen)
	if err != nil {
		fmt.Fprintf(stderr, "sandbox: %v\n", err)
		return 1
	}
	defer ln.Close()
	go carry(ln, sock, stderr)
	if dev != nil {
		return runDevelopmentChild(append(dev, args...), SelfPath, stderr)
	}
	cmd := exec.Command(args[1], args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "sandbox: %v\n", err)
		return 127
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if st, ok := exit.Sys().(syscall.WaitStatus); ok && st.Signaled() {
			return 128 + int(st.Signal())
		}
		return exit.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

// carry relays each connection on ln to the proxy's socket sock until ln is closed. A run of Accept failures is
// logged to stderr, with the sandbox's own errors.
func carry(ln net.Listener, sock string, stderr io.Writer) {
	logf := func(format string, args ...any) { fmt.Fprintf(stderr, "sandbox: "+format+"\n", args...) }
	acceptloop.Run(context.Background(), ln, logf, func(c net.Conn) {
		go func() {
			defer c.Close()
			u, err := net.Dial("unix", sock)
			if err != nil {
				return
			}
			defer u.Close()
			go func() { _, _ = io.Copy(u, c); _ = u.(*net.UnixConn).CloseWrite() }()
			_, _ = io.Copy(c, u)
		}()
	})
}
