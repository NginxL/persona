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
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/runtimepkg"
)

// The Antigravity (agy) companion runtime (docs/integrations/runtimes.md "Antigravity CLI"). When the machine has
// Antigravity CLI (`agy`) installed and no external `agy` runtime is connected on runtime.sock, a production RuntimeHost
// writes out the embedded persona-antigravity-runtime package and supervises `antigravity/companion.mjs` itself so the
// Persona assistant and Agents work out of the box without a manual launchd/systemd setup.

const (
	agyPackage   = "persona-antigravity-runtime"
	agyRuntimeID = "agy"
)

var (
	agyBridgeRestartMin = time.Second
	agyBridgeRestartMax = time.Minute
	agyBridgeProbeEvery = 30 * time.Second
	agyBridgeSteady     = 5 * time.Minute
	agyBridgeStopWait   = 3 * time.Second
)

type agyBridge struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// EnableAutoCompanions turns on automatic supervision of built-in CLI companions (Antigravity `agy`) once Listen runs.
// Production nodes (`personad node --attach` and `personad node --stdio`) enable this; unit test fixtures leave it off
// so the host machine's installed CLIs never join synthetic runtime tests.
func (rh *RuntimeHost) EnableAutoCompanions() {
	rh.autoCompanions = true
}

func (rh *RuntimeHost) setAgyBridge(on bool) {
	b := &rh.agy
	b.mu.Lock()
	defer b.mu.Unlock()
	if on == (b.cancel != nil) {
		return
	}
	if !on {
		b.cancel()
		b.cancel = nil
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
	go rh.agyBridgeLoop(ctx, b.done)
}

// findAgyExecutable finds an installed `agy` CLI binary without spawning a login shell.
var findAgyExecutable = func() string {
	bin := "agy"
	if runtime.GOOS == "windows" {
		bin = "agy.exe"
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	home := userHome()
	candidates := []string{filepath.Join(home, ".local", "bin", bin)}
	for _, dir := range agentSystemDirs {
		candidates = append(candidates, filepath.Join(dir, bin))
	}
	candidates = append(candidates, filepath.Join(home, ".volta", "bin", bin))
	nvmDirs, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", bin))
	sort.Sort(sort.Reverse(sort.StringSlice(nvmDirs)))
	candidates = append(candidates, nvmDirs...)
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && (st.Mode().Perm()&0o111 != 0 || runtime.GOOS == "windows") {
			return p
		}
	}
	return ""
}

func agyJSVersion(ctx context.Context, js jsRuntime) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, js.Path, "-e", "import('node:sqlite').then(()=>process.stdout.write(process.versions.node),()=>process.exit(1))")
	cmd.Env = js.env(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("Node.js 22.5+ with node:sqlite required")
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", errors.New("no version")
	}
	return v, nil
}

func findAgyJSRuntime(ctx context.Context) (jsRuntime, error) {
	var cands []jsRuntime
	if p := strings.TrimSpace(os.Getenv("PERSONA_NODEJS")); p != "" {
		cands = append(cands, jsRuntime{Path: p, Electron: !isNodeBinary(p), From: "PERSONA_NODEJS"})
	}
	if p, err := exec.LookPath("node"); err == nil {
		cands = append(cands, jsRuntime{Path: p, From: "PATH"})
	}
	for _, p := range commonNodePaths() {
		cands = append(cands, jsRuntime{Path: p, From: "installed"})
	}
	seen := map[string]bool{}
	var tried []string
	for _, c := range cands {
		if seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		if st, err := os.Stat(c.Path); err != nil || st.IsDir() {
			continue
		}
		v, err := agyJSVersion(ctx, c)
		if err != nil {
			tried = append(tried, c.Path+": "+err.Error())
			continue
		}
		c.Version = v
		return c, nil
	}
	if len(tried) > 0 {
		return jsRuntime{}, fmt.Errorf("no usable Node.js (%s)", strings.Join(tried, "; "))
	}
	return jsRuntime{}, errors.New("no Node.js found (set PERSONA_NODEJS)")
}

var agyBridgeCommand = func(ctx context.Context, rh *RuntimeHost, agyBin string) (*exec.Cmd, error) {
	pkg, err := runtimepkg.Load(agyPackage)
	if err != nil {
		return nil, err
	}
	if pkg == nil {
		return nil, errors.New("this personad carries no Antigravity runtime package (built without go generate ./internal/runtimepkg)")
	}
	dir, err := writePackage(filepath.Join(filepath.Dir(rh.path), "runtime-packages", agyPackage), pkg)
	if err != nil {
		return nil, fmt.Errorf("write Antigravity runtime: %w", err)
	}
	js, err := findAgyJSRuntime(ctx)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(js.Path, filepath.Join(dir, "antigravity", "companion.mjs"),
		"--socket", rh.path, "--agy-bin", agyBin, "--no-update", "--parent-stdin")
	cmd.Env = js.env(append(os.Environ(), "PERSONA_RUNTIME_SOCKET="+rh.path))
	cmd.Dir = dir
	return cmd, nil
}

func (rh *RuntimeHost) agyConnected() bool {
	if rh.reg == nil {
		return false
	}
	rh.reg.mu.Lock()
	defer rh.reg.mu.Unlock()
	return rh.reg.byRuntime(agyRuntimeID) != nil
}

func (rh *RuntimeHost) agyBridgeLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	wait := agyBridgeRestartMin
	loggedMissingJS := false
	for ctx.Err() == nil {
		agyBin := findAgyExecutable()
		if agyBin == "" || rh.agyConnected() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(agyBridgeProbeEvery):
			}
			continue
		}
		started := time.Now()
		err := rh.runAgyBridge(ctx, agyBin)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > agyBridgeSteady {
			wait = agyBridgeRestartMin
		}
		if err != nil {
			if strings.Contains(err.Error(), "Node.js") {
				if !loggedMissingJS {
					loggedMissingJS = true
					rh.logger.Printf("agy runtime: %v", err)
				}
			} else {
				rh.logger.Printf("agy runtime: %v (again in %s)", err, wait)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, agyBridgeRestartMax)
	}
}

func (rh *RuntimeHost) runAgyBridge(ctx context.Context, agyBin string) error {
	cmd, err := agyBridgeCommand(ctx, rh, agyBin)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = osproc.GroupAttr()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	rh.logger.Printf("agy runtime: running (pid %d, agy %s)", cmd.Process.Pid, agyBin)
	logged := make(chan struct{})
	go func() {
		defer close(logged)
		logLines(out, func(line string) {
			rh.logger.Printf("agy runtime: %s", line)
		})
	}()
	exited := make(chan error, 1)
	go func() {
		<-logged
		exited <- cmd.Wait()
	}()
	select {
	case err := <-exited:
		return err
	case <-ctx.Done():
		_ = osproc.StopGroup(cmd.Process.Pid, false)
		select {
		case <-exited:
		case <-time.After(agyBridgeStopWait):
			_ = osproc.StopGroup(cmd.Process.Pid, true)
			<-exited
		}
		return ctx.Err()
	}
}
