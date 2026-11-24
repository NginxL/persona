package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/runtimepkg"
)

// The Antigravity (agy) companion runtime (docs/integrations/runtimes.md "Antigravity CLI"). When the machine has
// Antigravity CLI (`agy`) installed and no external `agy` runtime is connected on runtime.sock, a production RuntimeHost
// writes out the embedded persona-antigravity-runtime package and supervises `antigravity/companion.mjs` itself so the
// Persona assistant and Agents work out of the box without a manual launchd/systemd setup.

const (
	agyPackage    = "persona-antigravity-runtime"
	agyRuntimeID  = "agy"
	agyYieldGrace = 5 * time.Second
)

var (
	agyBridgeRestartMin = time.Second
	agyBridgeRestartMax = time.Minute
	agyBridgeProbeEvery = 30 * time.Second
	agyBridgeSteady     = 5 * time.Minute
	agyBridgeStopWait   = 3 * time.Second
)

var agyInstanceSeq atomic.Uint64

type agyBridge struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	instance   string
	bin        string
	wakeCh     chan struct{}
	yieldGrace time.Duration
	takeover   sync.Mutex
	// providerEnv is the Gemini API key provider a Persona-managed agy launcher gives the companion (#13762): the
	// launcher's GOOGLE_GEMINI_BASE_URL / GEMINI_API_KEY plus PERSONA_AGY_MODEL_PROVIDER=gemini, nil without one. It
	// only ever goes into the companion's environment, never into its arguments, logs or a file.
	providerEnv []string
}

// agyProviderEnv is what the managed agy launchers say about the provider: the first one (by name) that carries a key
// is the machine's, since one companion serves every agy session here. None when agy runs on its own sign-in.
func (n *Node) agyProviderEnv() []string {
	n.managed.mu.Lock()
	names := make([]string, 0, len(n.managed.byID))
	for name, l := range n.managed.byID {
		if l.AgentType() == "agy" {
			names = append(names, name)
		}
	}
	n.managed.mu.Unlock()
	sort.Strings(names)
	for _, name := range names {
		if l, ok := n.managedLauncher(name); ok && l.APIKey != "" {
			return append(managedEnv(l), "PERSONA_AGY_MODEL_PROVIDER=gemini")
		}
	}
	return nil
}

// syncAgyProvider hands the companion the provider the managed launchers name now; it restarts when that changed.
func (n *Node) syncAgyProvider() {
	if n.runtimeHost != nil {
		n.runtimeHost.setAgyProviderEnv(n.agyProviderEnv())
	}
}

func (rh *RuntimeHost) setAgyProviderEnv(env []string) {
	rh.agy.mu.Lock()
	changed := !slices.Equal(rh.agy.providerEnv, env)
	rh.agy.providerEnv = env
	rh.agy.mu.Unlock()
	if changed {
		rh.wakeAgyBridge()
	}
}

func (rh *RuntimeHost) agyProviderEnv() []string {
	rh.agy.mu.Lock()
	defer rh.agy.mu.Unlock()
	return slices.Clone(rh.agy.providerEnv)
}

func (rh *RuntimeHost) builtinAgyInstance() string {
	rh.agy.mu.Lock()
	defer rh.agy.mu.Unlock()
	return rh.agy.instance
}

func (rh *RuntimeHost) builtinAgyExecutable() string {
	rh.agy.mu.Lock()
	bin := rh.agy.bin
	rh.agy.mu.Unlock()
	if bin != "" {
		return bin
	}
	return findAgyExecutable()
}

func (rh *RuntimeHost) wakeAgyBridge() {
	rh.agy.mu.Lock()
	wake := rh.agy.wakeCh
	rh.agy.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

// EnableAutoCompanions turns on automatic supervision of built-in CLI companions (Antigravity `agy` and `opencode`)
// once Listen runs. Production nodes (`personad node --attach` and `personad node --stdio`) enable this; unit test
// fixtures leave it off so the host machine's installed CLIs never join synthetic runtime tests.
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
	go rh.agyBridgeLoop(ctx, b.done, b.wakeCh)
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

var agyBridgeCommand = func(ctx context.Context, rh *RuntimeHost, agyBin, instance string) (*exec.Cmd, error) {
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
	args := []string{filepath.Join(dir, "antigravity", "companion.mjs"),
		"--socket", rh.path, "--agy-bin", agyBin, "--no-update", "--parent-stdin"}
	if instance != "" {
		args = append(args, "--instance", instance)
	}
	cmd := exec.Command(js.Path, args...)
	env := append(os.Environ(), "PERSONA_RUNTIME_SOCKET="+rh.path)
	cmd.Env = js.env(append(env, rh.agyProviderEnv()...))
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

func (rh *RuntimeHost) agyBridgeLoop(ctx context.Context, done chan struct{}, wake <-chan struct{}) {
	defer close(done)
	wait := agyBridgeRestartMin
	loggedMissingJS := false
	for ctx.Err() == nil {
		agyBin := findAgyExecutable()
		if agyBin == "" || rh.agyConnected() {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-time.After(agyBridgeProbeEvery):
			}
			continue
		}
		started := time.Now()
		yielded, err := rh.runAgyBridge(ctx, agyBin, wake)
		if ctx.Err() != nil {
			return
		}
		if yielded {
			wait = agyBridgeRestartMin
			continue
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

func (rh *RuntimeHost) runAgyBridge(ctx context.Context, agyBin string, wake <-chan struct{}) (bool, error) {
	instance := fmt.Sprintf("builtin-agy-%d-%d", os.Getpid(), agyInstanceSeq.Add(1))
	rh.agy.mu.Lock()
	rh.agy.instance = instance
	rh.agy.bin = agyBin
	rh.agy.mu.Unlock()
	defer func() {
		rh.agy.mu.Lock()
		if rh.agy.instance == instance {
			rh.agy.instance = ""
			rh.agy.bin = ""
		}
		rh.agy.mu.Unlock()
	}()

	started := rh.agyProviderEnv() // read before the command is made: a change in between restarts it
	cmd, err := agyBridgeCommand(ctx, rh, agyBin, instance)
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
	stopChild := func() {
		_ = osproc.StopGroup(cmd.Process.Pid, false)
		select {
		case <-exited:
		case <-time.After(agyBridgeStopWait):
			_ = osproc.StopGroup(cmd.Process.Pid, true)
			<-exited
		}
	}
	for {
		if !slices.Equal(started, rh.agyProviderEnv()) {
			rh.logger.Printf("agy runtime: the agy launcher's model provider changed; restarting")
			stopChild()
			return true, nil
		}
		if _, external := rh.agyPeers(instance); external {
			rh.logger.Printf("agy runtime: yielding to external agy runtime")
			stopChild()
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

func (rh *RuntimeHost) agyPeers(instance string) (builtin *runtimeConn, external bool) {
	rh.reg.mu.Lock()
	defer rh.reg.mu.Unlock()
	for _, conn := range rh.reg.conns {
		select {
		case <-conn.peer.Done():
			continue
		default:
		}
		conn.mu.Lock()
		ready, hello := conn.ready, conn.hello
		conn.mu.Unlock()
		if ready && hello.Runtime == agyRuntimeID {
			if hello.Instance != instance {
				external = true
			} else if builtin == nil || conn.serial > builtin.serial {
				builtin = conn
			}
		}
	}
	return builtin, external
}

func (h *runtimeConn) waitAgyIdle(ctx context.Context) {
	if h.peer.WaitPending(ctx) != nil {
		return
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for h.agyWorking() {
		select {
		case <-ctx.Done():
			return
		case <-h.peer.Done():
			return
		case <-tick.C:
		}
	}
}

func (h *runtimeConn) agyWorking() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, session := range h.sessions {
		if session.Status == proto.StatusWorking {
			return true
		}
	}
	return false
}
