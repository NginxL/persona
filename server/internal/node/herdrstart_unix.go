//go:build !windows

package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/osproc"
)

// launchHerdrServer starts `herdr server` in the user's clean login shell (herdrServerArgv), in its own session so it
// outlives the node: launchd and `systemctl --user restart` of the node must not take herdr and every agent in it
// along. Under a systemd user manager it runs as a transient unit of its own (systemd-run --user): a stop of the node's
// unit kills its whole cgroup, a new session or not. exited reports when the launched process ends (the server
// itself; systemd-run, which returns once the unit started).
func launchHerdrServer(ctx context.Context, bin, sock string) (<-chan error, error) {
	shell := userShell()
	session := herdr.Session()
	env := withHerdrSession(herdrServerEnv(os.Environ(), shell, herdrToolDirs(homeDir())), session)
	argv := herdrServerArgv(shell, bin, env)
	sd, viaSystemd := systemdRun()
	if viaSystemd {
		argv = append([]string{sd, "--user", "--quiet", "--collect", "--unit=" + herdrUnitOf(session)}, argv...)
	}
	logDir, err := privateCacheDir("herdr")
	if err != nil {
		return nil, fmt.Errorf("herdr server log: %w", err)
	}
	log := filepath.Join(logDir, herdrUnitOf(session)+".log")
	out, err := openHerdrLog(log)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...) // not CommandContext: the server outlives the request that started it
	cmd.Env = env
	cmd.Dir = homeDir()
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = osproc.DetachedAttr()
	if err := cmd.Start(); err != nil {
		out.Close()
		return nil, fmt.Errorf("start herdr server: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		out.Close()
		if err != nil {
			if tail := logTail(log, 600); tail != "" {
				err = fmt.Errorf("%w: %s", err, tail)
			}
		}
		if err != nil || !viaSystemd { // systemd-run returning nil only means the unit started
			exited <- err
		}
	}()
	return exited, nil
}

// openHerdrLog opens the server's log for one start, emptied. It does not follow a symlink, does not wait on a FIFO
// and refuses anything but a regular file of this user, so a name someone else put there can neither redirect the
// output nor make the open hang. O_NONBLOCK is only for the open: it is cleared before the file becomes the server's
// stdout and stderr, which would otherwise inherit it.
func openHerdrLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, fmt.Errorf("herdr server log: %w", err)
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || !osproc.OwnedByMe(st) {
		f.Close()
		return nil, fmt.Errorf("herdr server log: %s is not a regular file of this user", path)
	}
	err = syscall.SetNonblock(int(f.Fd()), false)
	if err == nil {
		err = f.Truncate(0)
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("herdr server log: %w", err)
	}
	return f, nil
}

// herdrUnit is the transient systemd unit the default server runs as: one name, so a second start while it runs fails.
// A server of a herdr session (a custom data directory's, herdr.Session) is a unit of its own, so the servers of
// two nodes on one account do not stop each other from starting; its log file is too.
const herdrUnit = "persona-herdr-server"

// herdrUnitOf is the unit (and log file) name of the server of session ("" the default one).
func herdrUnitOf(session string) string {
	if session == "" {
		return herdrUnit
	}
	return herdrUnit + "-" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_' // systemd unit names allow little more than these
	}, session)
}

// systemdRun is systemd-run when the node runs under a systemd user manager (Linux).
func systemdRun() (string, bool) {
	if runtime.GOOS != "linux" {
		return "", false
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(rt, "systemd", "private")); err != nil {
		return "", false
	}
	p, err := exec.LookPath("systemd-run")
	return p, err == nil
}

// herdrPaneShell is the shell herdr's new panes run, which reads what the node types there (typedHerdrAgent): herdr
// opens panes with its $SHELL, userShell when the node started herdr, the user's own login shell otherwise (a
// [terminal] default_shell in herdr's config is not looked at). A var for tests.
var herdrPaneShell = sync.OnceValue(userShell)

// userShell is the user's login shell: $SHELL, else the account's (launchd does not set SHELL), else zsh / sh.
func userShell() string {
	if s := os.Getenv("SHELL"); strings.HasPrefix(s, "/") {
		return s
	}
	if s := accountShell(); s != "" {
		return s
	}
	for _, s := range []string{"/bin/zsh", "/bin/bash"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}

// accountShell is the login shell of the account the node runs as, from the directory service (macOS) or passwd.
func accountShell() string {
	user := os.Getenv("USER")
	if runtime.GOOS == "darwin" && user != "" {
		out, err := exec.Command("/usr/bin/dscl", ".", "-read", "/Users/"+user, "UserShell").Output()
		if err == nil {
			if f := strings.Fields(string(out)); len(f) == 2 && strings.HasPrefix(f[1], "/") {
				return f[1]
			}
		}
		return ""
	}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	uid := fmt.Sprint(os.Getuid())
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Split(l, ":"); len(f) >= 7 && f[2] == uid && strings.HasPrefix(f[6], "/") {
			return f[6]
		}
	}
	return ""
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// logTail is the last n bytes of a file, on one line.
func logTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// lockHerdrDir locks herdr's config directory for one start (flock on the directory: no lock file left behind).
func lockHerdrDir(dir string) (func(), error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
