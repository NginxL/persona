package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/runtimepkg"
)

// The opencode ACP companion runtime (docs/integrations/opencode.md). When the machine has `opencode` installed and no
// external `opencode` companion or active `opencode serve` plugin is connected on runtime.sock, a production
// RuntimeHost writes out the embedded persona-opencode-runtime package and supervises `opencode/companion.mjs` itself
// so opencode Agent and Assistant sessions work out of the box without a manual companion or daemon setup.

const (
	opencodePackage    = "persona-opencode-runtime"
	opencodeRuntimeID  = "opencode"
	opencodeYieldGrace = 5 * time.Second
)

var (
	opencodeBridgeRestartMin = time.Second
	opencodeBridgeRestartMax = time.Minute
	opencodeBridgeProbeEvery = 30 * time.Second
	opencodeBridgeSteady     = 5 * time.Minute
	opencodeBridgeStopWait   = 3 * time.Second
)

var opencodeInstanceSeq atomic.Uint64

type opencodeBridge struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	instance   string
	bin        string
	wakeCh     chan struct{}
	yieldGrace time.Duration
	takeover   sync.Mutex
}

func (rh *RuntimeHost) builtinOpencodeInstance() string {
	rh.opencode.mu.Lock()
	defer rh.opencode.mu.Unlock()
	return rh.opencode.instance
}

func (rh *RuntimeHost) wakeOpencodeBridge() {
	rh.opencode.mu.Lock()
	wake := rh.opencode.wakeCh
	rh.opencode.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (rh *RuntimeHost) setOpencodeBridge(on bool) {
	b := &rh.opencode
	b.mu.Lock()
	defer b.mu.Unlock()
	if on == (b.cancel != nil) {
		return
	}
	if !on {
		b.cancel()
		b.cancel = nil
		b.wakeCh = nil
		done := b.done
		b.mu.Unlock()
		<-done
		b.mu.Lock()
		return
	}
	base := rh.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	b.cancel, b.done = cancel, make(chan struct{})
	b.wakeCh = make(chan struct{}, 1)
	go rh.opencodeBridgeLoop(ctx, b.done, b.wakeCh)
}

func opencodeBridgeDisabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("PERSONA_BUILTIN_OPENCODE")))
	return v == "0" || v == "false" || v == "no" || v == "off"
}

func isOpencodeExecutableFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && (st.Mode().Perm()&0o111 != 0 || runtime.GOOS == "windows")
}

// findOpencodeExecutable finds an installed `opencode` CLI binary without spawning a login shell.
var findOpencodeExecutable = func() string {
	for _, envKey := range []string{"PERSONA_OPENCODE_BIN", "OPENCODE_BIN"} {
		if p := strings.TrimSpace(os.Getenv(envKey)); p != "" {
			if isOpencodeExecutableFile(p) {
				return p
			}
			if looked, err := exec.LookPath(p); err == nil {
				return looked
			}
		}
	}
	bin := "opencode"
	if runtime.GOOS == "windows" {
		bin = "opencode.exe"
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	home := userHome()
	candidates := []string{
		filepath.Join(home, ".local", "bin", bin),
		filepath.Join(home, ".opencode", "bin", bin),
	}
	for _, dir := range agentSystemDirs {
		candidates = append(candidates, filepath.Join(dir, bin))
	}
	candidates = append(candidates,
		filepath.Join(home, ".volta", "bin", bin),
		filepath.Join(home, ".bun", "bin", bin),
	)
	nvmDirs, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", bin))
	sort.Sort(sort.Reverse(sort.StringSlice(nvmDirs)))
	candidates = append(candidates, nvmDirs...)
	for _, p := range candidates {
		if isOpencodeExecutableFile(p) {
			return p
		}
	}
	return ""
}

var opencodeBridgeCommand = func(ctx context.Context, rh *RuntimeHost, opencodeBin, instance string) (*exec.Cmd, error) {
	pkg, err := runtimepkg.Load(opencodePackage)
	if err != nil {
		return nil, err
	}
	if pkg == nil {
		return nil, errors.New("this personad carries no opencode runtime package (built without go generate ./internal/runtimepkg)")
	}
	dir, err := writePackage(filepath.Join(filepath.Dir(rh.path), "runtime-packages", opencodePackage), pkg)
	if err != nil {
		return nil, fmt.Errorf("write opencode runtime: %w", err)
	}
	js, err := findAgyJSRuntime(ctx)
	if err != nil {
		return nil, err
	}
	args := []string{filepath.Join(dir, "opencode", "companion.mjs"),
		"--socket", rh.path, "--bin", opencodeBin, "--no-update", "--parent-stdin"}
	if instance != "" {
		args = append(args, "--instance", instance)
	}
	cmd := exec.Command(js.Path, args...)
	cmd.Env = js.env(append(os.Environ(), "PERSONA_RUNTIME_SOCKET="+rh.path))
	cmd.Dir = dir
	return cmd, nil
}

// isOpencodeRuntimeHello reports whether hello is an opencode companion (`opencode`) or an active `opencode serve`
// plugin (`opencode-<pid>` or agent `opencode`).
func isOpencodeRuntimeHello(hello runtimeHello) bool {
	return hello.Runtime == opencodeRuntimeID ||
		strings.HasPrefix(hello.Runtime, opencodeRuntimeID+"-") ||
		hello.Agent == opencodeAgent
}

func (h *runtimeConn) isOpencodeRuntime() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.daemon == "" && isOpencodeRuntimeHello(h.hello)
}

func (rh *RuntimeHost) opencodeConnected() bool {
	_, external := rh.opencodePeers("")
	return external
}

func (rh *RuntimeHost) opencodeBridgeLoop(ctx context.Context, done chan struct{}, wake <-chan struct{}) {
	defer close(done)
	wait := opencodeBridgeRestartMin
	loggedMissingJS := false
	for ctx.Err() == nil {
		if opencodeBridgeDisabled() {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-time.After(opencodeBridgeProbeEvery):
			}
			continue
		}
		opencodeBin := findOpencodeExecutable()
		if opencodeBin == "" || rh.opencodeConnected() {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-time.After(opencodeBridgeProbeEvery):
			}
			continue
		}
		started := time.Now()
		yielded, err := rh.runOpencodeBridge(ctx, opencodeBin, wake)
		if ctx.Err() != nil {
			return
		}
		if yielded {
			wait = opencodeBridgeRestartMin
			continue
		}
		if time.Since(started) > opencodeBridgeSteady {
			wait = opencodeBridgeRestartMin
		}
		if err != nil {
			if strings.Contains(err.Error(), "Node.js") {
				if !loggedMissingJS {
					loggedMissingJS = true
					rh.logger.Printf("opencode runtime: %v", err)
				}
			} else {
				rh.logger.Printf("opencode runtime: %v (again in %s)", err, wait)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, opencodeBridgeRestartMax)
	}
}

func (rh *RuntimeHost) runOpencodeBridge(ctx context.Context, opencodeBin string, wake <-chan struct{}) (bool, error) {
	instance := fmt.Sprintf("builtin-opencode-%d-%d", os.Getpid(), opencodeInstanceSeq.Add(1))
	rh.opencode.mu.Lock()
	rh.opencode.instance = instance
	rh.opencode.bin = opencodeBin
	rh.opencode.mu.Unlock()
	defer func() {
		rh.opencode.mu.Lock()
		if rh.opencode.instance == instance {
			rh.opencode.instance = ""
			rh.opencode.bin = ""
		}
		rh.opencode.mu.Unlock()
	}()

	cmd, err := opencodeBridgeCommand(ctx, rh, opencodeBin, instance)
	if err != nil {
		return false, err
	}
	cmd.SysProcAttr = osproc.GroupAttr()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	cmd.Stderr = cmd.Stdout
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false, err
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start: %w", err)
	}
	rh.logger.Printf("opencode runtime: running (pid %d, opencode %s)", cmd.Process.Pid, opencodeBin)
	logged := make(chan struct{})
	go func() {
		defer close(logged)
		logLines(out, func(line string) {
			rh.logger.Printf("opencode runtime: %s", line)
		})
	}()
	exited := make(chan error, 1)
	go func() {
		<-logged
		exited <- cmd.Wait()
	}()
	stopChild := func() {
		_ = osproc.StopGroup(cmd.Process.Pid, false)
		select {
		case <-exited:
		case <-time.After(opencodeBridgeStopWait):
			_ = osproc.StopGroup(cmd.Process.Pid, true)
			<-exited
		}
	}
	for {
		if builtin, external := rh.opencodePeers(instance); external {
			rh.logger.Printf("opencode runtime: yielding to external opencode runtime")
			stopChild()
			if builtin != nil {
				builtin.mu.Lock()
				drop := builtin.drop
				builtin.mu.Unlock()
				builtin.peer.Close(errors.New("opencode runtime yielded to external runtime"))
				if drop != nil {
					drop()
				}
			}
			return true, nil
		}
		select {
		case err := <-exited:
			return false, err
		case <-wake:
		case <-ctx.Done():
			stopChild()
			return false, ctx.Err()
		}
	}
}

func (rh *RuntimeHost) opencodePeers(instance string) (builtin *runtimeConn, external bool) {
	if rh.reg == nil {
		return nil, false
	}
	rh.reg.mu.Lock()
	defer rh.reg.mu.Unlock()
	for _, conn := range rh.reg.conns {
		select {
		case <-conn.peer.Done():
			continue
		default:
		}
		conn.mu.Lock()
		ready, hello, isDaemon := conn.ready, conn.hello, conn.daemon != ""
		conn.mu.Unlock()
		if !ready || isDaemon || !isOpencodeRuntimeHello(hello) {
			continue
		}
		if hello.Runtime == opencodeRuntimeID && instance != "" && hello.Instance == instance {
			if builtin == nil || conn.serial > builtin.serial {
				builtin = conn
			}
		} else {
			external = true
		}
	}
	return builtin, external
}
