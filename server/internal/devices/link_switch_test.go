package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The pairing link the fake hub hands out: it must reach the computer's installer on stdin and nowhere else.
const (
	lsTestLink = "persona://link?hub=hub.example.com&token=SECRET-PAIRING-TOKEN-7f3a9c"
	lsTestHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// lsTestTimes are the steps' times, short.
var lsTestTimes = linkSwitchTimes{Probe: 20 * time.Second, Transfer: 20 * time.Second, Begin: 5 * time.Second,
	Install: 20 * time.Second, Connect: 3 * time.Second, EndBackoff: 20 * time.Millisecond, EndBackoffMax: 200 * time.Millisecond,
	EndGiveUp: time.Minute,
	Uninstall: 20 * time.Second, SSHBack: 3 * time.Second, Total: time.Minute, Resume: 300 * time.Millisecond,
	Shown: time.Hour, Poll: 20 * time.Millisecond}

// lsHub is the hub of a switch: it answers begin and end as the hub does (switching the entry's kind and handing the
// manager its new list), and anything else as fakeHost.
type lsHub struct {
	*fakeHost
	h *lsHarness

	mu     sync.Mutex
	cfg    devicewire.Config
	begins []devicewire.LinkSwitchBegin
	ends   []devicewire.LinkSwitchEnd
	refuse string // begin answers this code
	drop   bool   // begin switches the entry, its answer lost
	// endFail: how many ends fail first (-1: every one); endCode: every end answers this code; endHeld: every end
	// fails while set (the hub away), as many tries as there are.
	endFail int
	endHeld atomic.Bool
	endCode string
	onBegin func()
	onEnd   func(p devicewire.LinkSwitchEnd)
	// beginErr: begin fails with it after switching the entry (an error the hub did not code as a refusal).
	beginErr error
}

func remarshal(in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (hub *lsHub) Call(ctx context.Context, method string, params, out any) error {
	switch method {
	case devicewire.MethodLinkSwitchBegin:
		var p devicewire.LinkSwitchBegin
		_ = remarshal(params, &p)
		hub.mu.Lock()
		hub.begins = append(hub.begins, p)
		refuse, drop, hook, berr := hub.refuse, hub.drop, hub.onBegin, hub.beginErr
		hub.mu.Unlock()
		hub.h.note("hub begin")
		if refuse != "" {
			return &rpc.Error{Code: refuse, Message: "refused"}
		}
		if hook != nil {
			hook()
		}
		hub.setKind(kindLink)
		if drop {
			return rpc.ErrDropped
		}
		if berr != nil {
			return berr
		}
		return remarshal(devicewire.LinkSwitchBegun{Link: lsTestLink, PairHash: lsTestHash}, out)
	case devicewire.MethodLinkSwitchEnd:
		var p devicewire.LinkSwitchEnd
		_ = remarshal(params, &p)
		hub.mu.Lock()
		hub.ends = append(hub.ends, p)
		n, fail, code, hook := len(hub.ends), hub.endFail, hub.endCode, hub.onEnd
		hub.mu.Unlock()
		hub.h.note("hub end " + p.Outcome)
		if hook != nil {
			hook(p)
		}
		if fail < 0 || n <= fail || hub.endHeld.Load() {
			return errors.New("the hub is away")
		}
		if code != "" {
			return &rpc.Error{Code: code, Message: "refused"}
		}
		if p.Outcome == devicewire.LinkSwitchRolledBack {
			hub.setKind("ssh")
		}
		return nil
	}
	return hub.fakeHost.Call(ctx, method, params, out)
}

// setKind makes the entry of kind and hands the manager the list, as devices.config does.
func (hub *lsHub) setKind(kind string) {
	hub.mu.Lock()
	cfg := hub.cfg
	var mc map[string]any
	_ = json.Unmarshal(cfg.Machines[0], &mc)
	mc["kind"] = kind
	b, _ := json.Marshal(mc)
	cfg.Machines = []json.RawMessage{b}
	hub.cfg = cfg
	hub.mu.Unlock()
	hub.h.d.Configure(cfg)
}

func (hub *lsHub) kind() string {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var mc MachineConfig
	_ = json.Unmarshal(hub.cfg.Machines[0], &mc)
	return mc.Kind
}

func (hub *lsHub) calls() ([]devicewire.LinkSwitchBegin, []devicewire.LinkSwitchEnd) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return slices.Clone(hub.begins), slices.Clone(hub.ends)
}

type lsCall struct {
	what string
	argv []string
}

// lsHarness is a manager with one ssh computer, box: its ssh runs the switch's real scripts with sh in a temporary
// home (stubs for systemctl and, as the build sent, for persona-link), its node is an in-process one reached by the
// machine's kind (ssh while the entry says ssh, Persona Link once the stub installer said it connected).
type lsHarness struct {
	t                     *testing.T
	d                     *Devices
	hub                   *lsHub
	store                 *memStore
	node                  *testNode
	root, home, stubs     string
	runDir, pkg           string
	sshDown, nodeDown     atomic.Bool
	mu                    sync.Mutex
	calls                 []lsCall
	order                 []string
	gates                 map[string]chan struct{}
	force                 map[string]int
	forceOut              map[string]string // what a forced script prints
	onRun                 func(what string) // before each script runs
	pkgErr                error
	logs                  bytes.Buffer
	cancel                context.CancelFunc
	extraEnv              []string
	persona, units, binOf string
}

// lsStubLink is the build the tests send: persona-link's commands as a switch uses them, against $HOME. The
// installer writes what came on its stdin to .pair-got and puts a node service back from node-switch.backup, as
// persona-link's does; files in $HOME make it hang (.hang), fail before it paired (.fail-pair), after it paired
// (.fail-install), never connect (.no-connect) or fail to uninstall (.fail-uninstall).
const lsStubLink = `#!/bin/sh
D="$HOME/.persona"; U="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
case "$1 $2" in
"version "*) echo "persona-link test"; exit 0 ;;
"install --pair")
	[ "$3" = "-" ] || exit 2
	cat > "$D/.pair-got"
	if [ -e "$HOME/.hang" ]; then exec sleep 30; fi
	if [ -e "$HOME/.fail-pair" ]; then exit 1; fi
	mkdir -p "$U"
	echo '{}' > "$D/link.json"
	if [ -e "$D/node-switch.backup" ]; then mv "$D/node-switch.backup" "$U/personad-node.service"; fi
	[ -e "$U/personad-node.service" ] || echo written-by-the-installer > "$U/personad-node.service"
	echo unit > "$U/persona-link.service"
	if [ -e "$HOME/.fail-install" ]; then exit 1; fi
	if [ ! -e "$HOME/.no-connect" ]; then : > "$D/.connected"; fi
	exit 0 ;;
"uninstall --purge")
	if [ -e "$HOME/.fail-uninstall" ]; then exit 1; fi
	rm -f "$U/persona-link.service" "$D/link.json" "$D/bin/persona-link" "$D/.connected"
	exit 0 ;;
"node uninstall")
	rm -f "$U/personad-node.service"; exit 0 ;;
esac
exit 2
`

type lsOptions struct {
	kind  string // the entry's kind at the start ("ssh")
	stub  string // the build's content (lsStubLink)
	setup func(h *lsHarness)
}

func newLSHarness(t *testing.T, o lsOptions) *lsHarness {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the switch's scripts run on Linux computers")
	}
	for _, tool := range []string{"sh", "gzip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		if _, err := exec.LookPath("shasum"); err != nil {
			t.Skip("no sha256sum or shasum")
		}
	}
	o.kind = orDefault(o.kind, "ssh")
	o.stub = orDefault(o.stub, lsStubLink)
	root := t.TempDir()
	h := &lsHarness{t: t, root: root, home: filepath.Join(root, "home"), stubs: filepath.Join(root, "stubs"),
		runDir: filepath.Join(root, "run"), pkg: filepath.Join(root, "pkg", "persona-link-linux-amd64"),
		gates: map[string]chan struct{}{}, force: map[string]int{}, forceOut: map[string]string{}}
	h.persona = filepath.Join(h.home, ".persona")
	h.binOf = filepath.Join(h.persona, "bin")
	h.units = filepath.Join(h.home, ".config", "systemd", "user")
	me, err := user.Current()
	if err != nil {
		t.Skip("no user name")
	}
	for _, dir := range []string{h.binOf, h.stubs, h.runDir, filepath.Dir(h.pkg), filepath.Join(root, "systemd"), filepath.Join(root, "linger")} {
		mustMkdir(t, dir)
	}
	for _, dir := range []string{h.home, h.persona, h.binOf} { // writable by the user alone, whatever the umask here
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(root, "linger", me.Username), "", 0o644)
	mustWrite(t, filepath.Join(h.binOf, "personad"), "#!/bin/sh\nexit 0\n", 0o755)
	mustWrite(t, filepath.Join(h.stubs, "systemctl"), "#!/bin/sh\nif [ -e \"$HOME/.no-systemd\" ]; then exit 1; fi\nexit 0\n", 0o755)
	mustWrite(t, h.pkg, o.stub, 0o755)
	h.node = &testNode{info: &proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "amd64", Home: "/home/me",
		DataDir: "/home/me/.persona", Version: "test"}}

	h.store = newMemStore()
	h.hub = &lsHub{fakeHost: &fakeHost{}, h: h}
	logf := func(format string, args ...any) {
		h.mu.Lock()
		fmt.Fprintf(&h.logs, format+"\n", args...)
		h.mu.Unlock()
	}
	d := New(Options{Hub: h.hub, Store: h.store, Logf: logf})
	h.d = d
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	entry, _ := json.Marshal(MachineConfig{ID: "box", Name: "Box", Kind: o.kind, SSHHost: "box.example"})
	cfg := devicewire.Config{HubID: "hub-test", HubName: "test", HubVersion: "test", LinkSwitch: true,
		Endpoints: []devicewire.LinkEndpoint{{URL: "https://hub.example.com", Kind: "public"}}, Machines: []json.RawMessage{entry}}
	h.hub.cfg = cfg
	d.mu.Lock()
	d.ctx, d.got, d.cfg = ctx, true, cfg
	d.mu.Unlock()
	close(d.gotCh)
	d.switches.times = lsTestTimes
	d.switches.run = h.run
	d.switches.pkg = func(ctx context.Context, goos, arch string) (string, string, error) {
		if goos != "linux" || arch != "amd64" {
			return "", "", fmt.Errorf("asked for %s/%s", goos, arch)
		}
		if h.pkgErr != nil {
			return "", "", h.pkgErr
		}
		return h.pkg, "test", nil
	}
	d.switches.paths = lsPaths{systemd: filepath.Join(root, "systemd"), linger: filepath.Join(root, "linger")}
	d.opts.DialNode = h.dial
	if o.setup != nil {
		o.setup(h)
	}
	t.Cleanup(h.close)
	d.Configure(cfg)
	if o.kind == "ssh" {
		eventually(t, 5*time.Second, "box online over ssh", func() bool { return d.sshOnline("box") })
	}
	return h
}

func orDefault(s, or string) string {
	if s == "" {
		return or
	}
	return s
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (h *lsHarness) close() {
	h.cancel()
	done := make(chan struct{})
	go func() {
		h.d.switches.wg.Wait()
		h.d.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		h.t.Error("the switch or the machines did not stop")
	}
}

func (h *lsHarness) note(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.order = append(h.order, s)
}

func (h *lsHarness) orderSeen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.order)
}

func (h *lsHarness) whats() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		out = append(out, c.what)
	}
	return out
}

// hold keeps the script what from running until the returned func is called.
func (h *lsHarness) hold(what string) func() {
	gate := make(chan struct{})
	h.mu.Lock()
	h.gates[what] = gate
	h.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// dial is DialNode: made for each new machine with its entry's kind as the hub has it then.
func (h *lsHarness) dial(id string) func(context.Context) (net.Conn, error) {
	kind := h.hub.kind()
	return func(ctx context.Context) (net.Conn, error) {
		for {
			up := !h.nodeDown.Load()
			switch kind {
			case kindLink:
				_, err := os.Stat(filepath.Join(h.persona, ".connected"))
				up = up && err == nil
			default:
				up = up && !h.sshDown.Load()
			}
			if up {
				return h.node.dial(ctx)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}

// run is the computer's ssh: the remote command run by sh in the temporary home.
func (h *lsHarness) run(ctx context.Context, what string, argv []string, stdin io.Reader, stdout io.Writer) (int, error) {
	h.mu.Lock()
	h.calls = append(h.calls, lsCall{what: what, argv: slices.Clone(argv)})
	h.order = append(h.order, "ssh "+what)
	gate := h.gates[what]
	code, forced := h.force[what]
	forcedOut := h.forceOut[what]
	env := slices.Clone(h.extraEnv)
	onRun := h.onRun
	h.mu.Unlock()
	if onRun != nil {
		onRun(what)
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
	if forced {
		_, _ = io.Copy(io.Discard, stdin)
		_, _ = io.WriteString(stdout, forcedOut)
		return code, nil
	}
	if h.sshDown.Load() {
		return 255, nil
	}
	if argv[0] != "ssh" || argv[len(argv)-2] != "box.example" {
		h.t.Errorf("ssh to the wrong place: %q", argv)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", argv[len(argv)-1])
	cmd.Env = append(append(shellEnv(h.home, h.stubs), "XDG_RUNTIME_DIR="+h.runDir), env...)
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit) && exit.Exited():
		if s := strings.TrimSpace(stderr.String()); s != "" {
			h.t.Logf("%s exited %d: %s", what, exit.ExitCode(), s)
		}
		return exit.ExitCode(), nil
	}
	return -1, err
}

// post starts a switch over HTTP as a device of role (or the admin page: role "admin").
func (h *lsHarness) post(role string) (int, map[string]json.RawMessage, string) {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/machines/box/link-switch", nil)
	if role == "admin" {
		req.Header.Set(assistantwire.AdminHeader, "1")
	} else if role != "" {
		req.Header.Set(assistantwire.RoleHeader, role)
	}
	rec := httptest.NewRecorder()
	h.d.Handler(http.NotFoundHandler()).ServeHTTP(rec, req)
	var body map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body, rec.Body.String()
}

// start posts as a full device and wants it accepted.
func (h *lsHarness) start() proto.MachineLinkSwitch {
	h.t.Helper()
	code, body, raw := h.post("full")
	if code != http.StatusAccepted {
		h.t.Fatalf("POST link-switch: %d %s", code, raw)
	}
	var v proto.MachineLinkSwitch
	if err := json.Unmarshal(body["link_switch"], &v); err != nil {
		h.t.Fatalf("answer %s: %v", raw, err)
	}
	if strings.Contains(raw, lsTestLink) || strings.Contains(raw, "SECRET") {
		h.t.Fatal("the answer carries the pairing link")
	}
	return v
}

// finished waits for the switch to end and is its record.
func (h *lsHarness) finished() linkSwitchRecord {
	h.t.Helper()
	var rec *linkSwitchRecord
	eventually(h.t, 30*time.Second, "the switch ends", func() bool {
		rec = h.d.linkSwitchRecordOf("box")
		return rec.finished()
	})
	eventually(h.t, 10*time.Second, "its goroutine ends", func() bool {
		h.d.switches.mu.Lock()
		defer h.d.switches.mu.Unlock()
		return !h.d.switches.active["box"]
	})
	return *h.d.linkSwitchRecordOf("box") // as its goroutine left it
}

// stored is the record the settings have.
func (h *lsHarness) stored() linkSwitchRecord {
	h.t.Helper()
	var rec linkSwitchRecord
	if ok, err := h.store.Setting(context.Background(), linkSwitchKey("box"), &rec); !ok || err != nil {
		h.t.Fatalf("no stored record: %v", err)
	}
	return rec
}

// noSecret checks the pairing link is nowhere but the installer's stdin: not in the log, the settings, an ssh
// command line.
func (h *lsHarness) noSecret() {
	h.t.Helper()
	h.mu.Lock()
	logs := h.logs.String()
	calls := slices.Clone(h.calls)
	h.mu.Unlock()
	if strings.Contains(logs, lsTestLink) || strings.Contains(logs, "SECRET") {
		h.t.Errorf("the log has the pairing link:\n%s", logs)
	}
	for _, c := range calls {
		for _, a := range c.argv {
			if strings.Contains(a, lsTestLink) || strings.Contains(a, "SECRET") {
				h.t.Errorf("the pairing link is on ssh's command line (%s): %q", c.what, a)
			}
		}
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	for k, v := range h.store.kv {
		if strings.Contains(string(v), lsTestLink) || strings.Contains(string(v), "SECRET") {
			h.t.Errorf("the settings keep the pairing link in %s", k)
		}
	}
}

func (h *lsHarness) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(h.home, rel))
	return err == nil
}

// tree is every file under the home with its content.
func (h *lsHarness) tree() map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(h.home, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(h.home, p)
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "-> " + target
		case !e.IsDir():
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		default:
			out[rel+"/"] = ""
		}
		return nil
	})
	return out
}

func stagedName(op string) string { return filepath.Join(".persona", "bin", lsStagePrefix+op) }

// A switch that works: probe, stage, the hub's begin, the installer with the pairing link on its stdin, the node
// through Persona Link, the hub's end(succeeded). The computer keeps its id and is online on its link; nothing has
// the pairing link but the installer.
func TestLinkSwitchSucceeds(t *testing.T) {
	h := newLSHarness(t, lsOptions{})
	v := h.start()
	if v.Available || v.Reason != proto.LinkSwitchReasonInProgress || v.Phase != proto.LinkSwitchPhaseChecking {
		t.Fatalf("answered %+v", v)
	}
	rec := h.finished()
	if rec.Phase != proto.LinkSwitchPhaseSucceeded || rec.Error != "" || rec.RollbackUnconfirmed {
		t.Fatalf("record %+v", rec)
	}
	if !strings.HasPrefix(rec.Operation, "ls_") || len(rec.Operation) != 19 {
		t.Errorf("operation id %q", rec.Operation)
	}
	begins, ends := h.hub.calls()
	if len(begins) != 1 || begins[0] != (devicewire.LinkSwitchBegin{MachineID: "box", Operation: rec.Operation, SSHHost: "box.example", By: "device"}) {
		t.Errorf("begins %+v", begins)
	}
	if len(ends) != 1 || ends[0] != (devicewire.LinkSwitchEnd{MachineID: "box", Operation: rec.Operation, PairHash: lsTestHash, Outcome: devicewire.LinkSwitchSucceeded}) {
		t.Errorf("ends %+v", ends)
	}
	if got := h.whats(); !slices.Equal(got, []string{"probe", "stage", "install", "remove staged"}) {
		t.Errorf("ssh runs %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(h.persona, ".pair-got")); string(got) != lsTestLink+"\n" {
		t.Errorf("the installer read %q on its stdin", got)
	}
	if h.exists(stagedName(rec.Operation)) {
		t.Error("the staged build is left")
	}
	if b, err := os.ReadFile(filepath.Join(h.binOf, "persona-link")); err != nil || string(b) != lsStubLink {
		t.Errorf("bin/persona-link is not the build sent: %v", err)
	}
	if !h.d.linkOnline("box") || h.hub.kind() != kindLink {
		t.Error("box is not online on its Persona Link")
	}
	st := h.stored()
	if st.Phase != proto.LinkSwitchPhaseSucceeded || st.PairHash != lsTestHash || st.SHA256 == "" || st.Install != lsInstallPlaced ||
		st.InstallerExit == nil || *st.InstallerExit != 0 || st.Arch != "amd64" || st.By != "device" {
		t.Errorf("stored %+v", st)
	}
	m := h.d.cfgMachine("box")
	if s := m.snapshot(); s.LinkSwitch == nil || s.LinkSwitch.Phase != proto.LinkSwitchPhaseSucceeded || s.LinkSwitch.Available {
		t.Errorf("box shows %+v", s.LinkSwitch)
	}
	h.noSecret()
}

// Each thing the probe refuses on fails the switch with its reason before anything changed: nothing written on the
// computer, nothing asked of the hub, nothing sent.
func TestLinkSwitchProbeRefusals(t *testing.T) {
	cases := []struct {
		name, want string
		setup      func(h *lsHarness)
	}{
		{"mac", proto.LinkSwitchReasonUnsupportedOS, func(h *lsHarness) {
			mustWrite(t, filepath.Join(h.stubs, "uname"), "#!/bin/sh\ncase \"$1\" in -s) echo Darwin;; -m) echo arm64;; esac\n", 0o755)
		}},
		{"riscv", proto.LinkSwitchReasonUnsupportedOS, func(h *lsHarness) {
			mustWrite(t, filepath.Join(h.stubs, "uname"), "#!/bin/sh\ncase \"$1\" in -s) echo Linux;; -m) echo riscv64;; esac\n", 0o755)
		}},
		{"no user manager", proto.LinkSwitchReasonNoSystemd, func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".no-systemd"), "", 0o644) }},
		{"not booted with systemd", proto.LinkSwitchReasonNoSystemd, func(h *lsHarness) {
			_ = os.Remove(filepath.Join(h.root, "systemd"))
		}},
		{"no linger", proto.LinkSwitchReasonLingerOff, func(h *lsHarness) { _ = os.RemoveAll(filepath.Join(h.root, "linger")) }},
		{"persona-link", proto.LinkSwitchReasonExistingLink, func(h *lsHarness) { mustWrite(t, filepath.Join(h.binOf, "persona-link"), "x", 0o755) }},
		{"dangling persona-link", proto.LinkSwitchReasonExistingLink, func(h *lsHarness) {
			_ = os.Symlink(filepath.Join(h.root, "gone"), filepath.Join(h.binOf, "persona-link"))
		}},
		{"legacy link program", proto.LinkSwitchReasonExistingLink, func(h *lsHarness) {
			mustWrite(t, filepath.Join(h.binOf, compat.ConnectorBinary), "x", 0o755)
		}},
		{"paired", proto.LinkSwitchReasonExistingLink, func(h *lsHarness) { mustWrite(t, filepath.Join(h.persona, "link.json"), "{}", 0o600) }},
		{"paired in the legacy directory", proto.LinkSwitchReasonExistingLink, func(h *lsHarness) {
			mustWrite(t, filepath.Join(h.home, compat.StateDir, "link.json"), "{}", 0o600)
		}},
		{"link service", proto.LinkSwitchReasonExistingLink, func(h *lsHarness) { mustWrite(t, filepath.Join(h.units, "persona-link.service"), "x", 0o644) }},
		{"legacy link service", proto.LinkSwitchReasonExistingLink, func(h *lsHarness) {
			mustWrite(t, filepath.Join(h.units, compat.ConnectorBinary+".service"), "x", 0o644)
		}},
		{"legacy data directory", proto.LinkSwitchReasonCustomNode, func(h *lsHarness) { mustMkdir(t, filepath.Join(h.home, compat.StateDir)) }},
		{"data directory set", proto.LinkSwitchReasonCustomNode, func(h *lsHarness) { h.extraEnv = []string{"PERSONA_HOME=/srv/persona"} }},
		{"no node program", proto.LinkSwitchReasonNoNode, func(h *lsHarness) { _ = os.Remove(filepath.Join(h.binOf, "personad")) }},
		{"ssh fails", proto.LinkSwitchReasonOffline, func(h *lsHarness) { h.force["probe"] = 255 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{})
			c.setup(h)
			before := h.tree()
			h.start()
			rec := h.finished()
			if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != c.want || rec.RollbackUnconfirmed {
				t.Fatalf("record %+v, want failed %s", rec, c.want)
			}
			if begins, ends := h.hub.calls(); len(begins)+len(ends) > 0 {
				t.Errorf("the hub was asked: %+v %+v", begins, ends)
			}
			if got := h.whats(); !slices.Equal(got, []string{"probe"}) {
				t.Errorf("ssh runs %q", got)
			}
			after := h.tree()
			if fmt.Sprint(before) != fmt.Sprint(after) {
				t.Errorf("the computer changed:\nbefore %v\nafter  %v", before, after)
			}
			if !h.d.sshOnline("box") || h.hub.kind() != "ssh" {
				t.Error("box is not on ssh as before")
			}
			if s := h.d.cfgMachine("box").snapshot(); s.LinkSwitch == nil || s.LinkSwitch.Phase != proto.LinkSwitchPhaseFailed || s.LinkSwitch.Error != c.want {
				t.Errorf("box shows %+v", s.LinkSwitch)
			}
		})
	}
}

// A node service there already is the computer's: the probe passes on it alone (no node program needed) and records
// it, so a rollback never removes it.
func TestLinkSwitchProbeTakesANodeService(t *testing.T) {
	h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) {
		_ = os.Remove(filepath.Join(h.binOf, "personad"))
		mustWrite(t, filepath.Join(h.units, compat.DaemonBinary+"-node.service"), "legacy unit", 0o644)
		mustWrite(t, filepath.Join(h.home, ".no-connect"), "", 0o644)
	}})
	h.start()
	rec := h.finished()
	if rec.Error != proto.LinkSwitchReasonLinkTimeout || !rec.NodeUnitExisted {
		t.Fatalf("record %+v", rec)
	}
	if b, _ := os.ReadFile(filepath.Join(h.units, compat.DaemonBinary+"-node.service")); string(b) != "legacy unit" {
		t.Error("the computer's own node service was touched")
	}
}

// Sending the build fails (ssh drops, the build does not run there): nothing asked of the hub, the staged build
// removed, the computer on ssh as before.
func TestLinkSwitchTransferFails(t *testing.T) {
	for _, c := range []struct {
		name  string
		stub  string
		setup func(h *lsHarness)
	}{
		{"ssh drops", "", func(h *lsHarness) { h.force["stage"] = 255 }},
		{"the build does not run", "#!/bin/sh\nexit 1\n", nil},
		{"no build", "", func(h *lsHarness) { h.pkgErr = errNoConnector }},
		{"the release site is away", "", func(h *lsHarness) { h.pkgErr = errors.New("release site: connection refused") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{stub: c.stub, setup: c.setup})
			h.start()
			rec := h.finished()
			want := proto.LinkSwitchReasonTransferFailed
			if h.pkgErr != nil {
				want = proto.LinkSwitchReasonNoPackage
			}
			if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != want || rec.RollbackUnconfirmed || rec.BeginIntended {
				t.Fatalf("record %+v", rec)
			}
			if begins, ends := h.hub.calls(); len(begins)+len(ends) > 0 {
				t.Errorf("the hub was asked: %+v %+v", begins, ends)
			}
			wantRuns := []string{"probe", "stage", "remove staged"}
			if h.pkgErr != nil {
				wantRuns = []string{"probe"}
			}
			if got := h.whats(); !slices.Equal(got, wantRuns) {
				t.Errorf("ssh runs %q", got)
			}
			if h.exists(stagedName(rec.Operation)) || h.exists(filepath.Join(".persona", "bin", "persona-link")) {
				t.Error("the build is left on the computer")
			}
		})
	}
}

// Every refusal of the hub's begin fails the switch with its reason: the hub changed nothing, so end is never asked
// (an end without a pairing hash would revoke a credential the computer had before), the staged build is removed
// and the installer never runs.
func TestLinkSwitchBeginRefused(t *testing.T) {
	for code, want := range map[string]string{
		devicewire.LinkSwitchCodeNotSSH:       proto.LinkSwitchReasonConfigChanged,
		devicewire.LinkSwitchCodeChanged:      proto.LinkSwitchReasonConfigChanged,
		devicewire.LinkSwitchCodeNoMachine:    proto.LinkSwitchReasonConfigChanged,
		devicewire.LinkSwitchCodePending:      proto.LinkSwitchReasonPending,
		devicewire.LinkSwitchCodeUnavailable:  proto.LinkSwitchReasonLinkUnavailable,
		devicewire.LinkSwitchCodeExistingLink: proto.LinkSwitchReasonExistingLink,
		"unknown_method":                      proto.LinkSwitchReasonHubOld,
	} {
		t.Run(code, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{})
			h.hub.refuse = code
			h.start()
			rec := h.finished()
			if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != want || rec.RollbackUnconfirmed {
				t.Fatalf("record %+v, want %s", rec, want)
			}
			if begins, ends := h.hub.calls(); len(begins) != 1 || len(ends) != 0 {
				t.Errorf("begins %+v ends %+v", begins, ends)
			}
			if got := h.whats(); !slices.Equal(got, []string{"probe", "stage", "remove staged"}) {
				t.Errorf("ssh runs %q", got)
			}
			if h.exists(stagedName(rec.Operation)) || h.hub.kind() != "ssh" || !h.d.sshOnline("box") {
				t.Error("not as before")
			}
		})
	}
}

// The installer fails and the link never comes: the hub's end(rolled_back) with this switch's pairing hash, then
// only what this switch set up is removed (the node service there was none of, Persona Link), then the computer is
// back over ssh. A node service the computer had is kept.
func TestLinkSwitchRollsBackOnlyWhatItSetUp(t *testing.T) {
	for _, hadNode := range []bool{false, true} {
		t.Run(fmt.Sprintf("node service before %t", hadNode), func(t *testing.T) {
			h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) {
				mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644)
				if hadNode {
					mustWrite(t, filepath.Join(h.units, "personad-node.service"), "the computer's own", 0o644)
				}
			}})
			h.start()
			rec := h.finished()
			if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != proto.LinkSwitchReasonInstallFailed || rec.RollbackUnconfirmed {
				t.Fatalf("record %+v", rec)
			}
			if rec.InstallerExit == nil || *rec.InstallerExit != 1 || rec.NodeUnitExisted != hadNode {
				t.Errorf("record %+v", rec)
			}
			_, ends := h.hub.calls()
			if len(ends) != 1 || ends[0].Outcome != devicewire.LinkSwitchRolledBack || ends[0].PairHash != lsTestHash {
				t.Errorf("ends %+v", ends)
			}
			order := h.orderSeen()
			end, uninstall := slices.Index(order, "hub end rolled_back"), slices.Index(order, "ssh uninstall")
			if end < 0 || uninstall < end {
				t.Errorf("not end, then uninstall: %q", order)
			}
			for _, rel := range []string{".persona/bin/persona-link", ".persona/link.json", ".config/systemd/user/persona-link.service", stagedName(rec.Operation)} {
				if h.exists(rel) {
					t.Errorf("%s is left", rel)
				}
			}
			unit := filepath.Join(h.units, "personad-node.service")
			b, err := os.ReadFile(unit)
			switch {
			case hadNode && string(b) != "the computer's own":
				t.Errorf("the computer's node service was removed or changed: %q %v", b, err)
			case !hadNode && err == nil:
				t.Error("the node service the installer wrote is left")
			}
			if !h.exists(".persona/bin/personad") {
				t.Error("the node program was removed")
			}
			if !h.d.sshOnline("box") || h.hub.kind() != "ssh" {
				t.Error("box is not back over ssh")
			}
			h.noSecret()
		})
	}
}

// The installer says it worked but the link never connects: link_timeout, rolled back.
func TestLinkSwitchLinkTimeout(t *testing.T) {
	h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".no-connect"), "", 0o644) }})
	h.start()
	rec := h.finished()
	if rec.Error != proto.LinkSwitchReasonLinkTimeout || rec.RollbackUnconfirmed || *rec.InstallerExit != 0 {
		t.Fatalf("record %+v", rec)
	}
	if h.exists(".persona/link.json") || h.exists(".config/systemd/user/personad-node.service") || !h.d.sshOnline("box") {
		t.Error("not rolled back")
	}
}

// A rollback that cannot be shown to have worked says so, with the cause it rolled back for: the hub never answers
// end, the computer keeps what the switch set up, or it does not come back over ssh.
func TestLinkSwitchRollbackUnconfirmed(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(h *lsHarness)
	}{
		{"the uninstall fails", func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".fail-uninstall"), "", 0o644) }},
		{"ssh never comes back", func(h *lsHarness) { h.hub.onBegin = func() { h.nodeDown.Store(true) } }},
		{"ssh fails for the uninstall", func(h *lsHarness) { h.force["uninstall"] = 255 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) {
				mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644)
				c.setup(h)
			}})
			h.start()
			rec := h.finished()
			if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != proto.LinkSwitchReasonInstallFailed || !rec.RollbackUnconfirmed {
				t.Fatalf("record %+v", rec)
			}
			if st := h.stored(); !st.RollbackUnconfirmed {
				t.Errorf("stored %+v", st)
			}
			if s := h.d.cfgMachine("box").snapshot(); s.LinkSwitch == nil || !s.LinkSwitch.RollbackUnconfirmed {
				t.Errorf("box shows %+v", s.LinkSwitch)
			}
			if _, ends := h.hub.calls(); c.name == "the hub never answers end" && len(ends) < 2 {
				t.Errorf("end was asked %d times", len(ends))
			}
			h.noSecret()
		})
	}
}

// The hub's answer to begin is lost (it may have switched the entry and made a code): end(rolled_back) without a
// pairing hash, so the hub revokes whatever this machine has; nothing was installed.
func TestLinkSwitchLostBeginAnswer(t *testing.T) {
	h := newLSHarness(t, lsOptions{})
	h.hub.drop = true
	h.start()
	rec := h.finished()
	if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != proto.LinkSwitchReasonUnknown || rec.RollbackUnconfirmed {
		t.Fatalf("record %+v", rec)
	}
	_, ends := h.hub.calls()
	if len(ends) != 1 || ends[0].PairHash != "" || ends[0].Outcome != devicewire.LinkSwitchRolledBack {
		t.Errorf("ends %+v", ends)
	}
	if got := h.whats(); !slices.Equal(got, []string{"probe", "stage", "uninstall"}) {
		t.Errorf("ssh runs %q", got)
	}
	if h.exists(stagedName(rec.Operation)) || !h.d.sshOnline("box") {
		t.Error("not as before")
	}
}

// One switch per computer: a second start while one runs is 409 in_progress, and starts nothing.
func TestLinkSwitchSecondStartIsRefused(t *testing.T) {
	h := newLSHarness(t, lsOptions{})
	release := h.hold("probe")
	h.start()
	code, body, raw := h.post("full")
	var c string
	_ = json.Unmarshal(body["code"], &c)
	if code != http.StatusConflict || c != proto.LinkSwitchReasonInProgress {
		t.Fatalf("second start: %d %s", code, raw)
	}
	m := h.d.cfgMachine("box")
	if err := m.startDeploy(); !errors.Is(err, errLinkSwitchDeploy) {
		t.Errorf("a deploy during the switch: %v", err)
	}
	if s := m.snapshot(); s.CanDeploy == nil || *s.CanDeploy || s.LinkSwitch.Reason != proto.LinkSwitchReasonInProgress {
		t.Errorf("box shows can_deploy %v, %+v", s.CanDeploy, s.LinkSwitch)
	}
	release()
	if rec := h.finished(); rec.Phase != proto.LinkSwitchPhaseSucceeded {
		t.Fatalf("record %+v", rec)
	}
	if begins, _ := h.hub.calls(); len(begins) != 1 {
		t.Errorf("begins %+v", begins)
	}
}

// Only a full device or the admin page may start one; who did is recorded.
func TestLinkSwitchNeedsAFullDevice(t *testing.T) {
	h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) { _ = os.RemoveAll(filepath.Join(h.root, "linger")) }})
	for _, role := range []string{"", "read", "extension"} {
		if code, _, raw := h.post(role); code != http.StatusForbidden {
			t.Errorf("role %q: %d %s", role, code, raw)
		}
	}
	if rec := h.d.linkSwitchRecordOf("box"); rec != nil {
		t.Fatalf("started: %+v", rec)
	}
	if code, _, raw := h.post("admin"); code != http.StatusAccepted {
		t.Fatalf("admin: %d %s", code, raw)
	}
	if rec := h.finished(); rec.By != "admin" || rec.Error != proto.LinkSwitchReasonLingerOff {
		t.Errorf("record %+v", rec)
	}
}

// Not available: 409 with the reason, nothing started.
func TestLinkSwitchNotAvailable(t *testing.T) {
	h := newLSHarness(t, lsOptions{})
	h.d.mu.Lock()
	h.d.cfg.LinkSwitch = false
	h.d.mu.Unlock()
	code, body, raw := h.post("full")
	var c string
	_ = json.Unmarshal(body["code"], &c)
	if code != http.StatusConflict || c != proto.LinkSwitchReasonHubOld {
		t.Fatalf("%d %s", code, raw)
	}
	if rec := h.d.linkSwitchRecordOf("box"); rec != nil || len(h.whats()) > 0 {
		t.Errorf("started: %+v %q", rec, h.whats())
	}
}

// A manager that restarts during a switch finishes it: one that never reached the hub's begin fails as interrupted
// (its staged build removed, the hub not asked); one that did succeeds when the link is online, or rolls back.
func TestLinkSwitchResumesAfterARestart(t *testing.T) {
	rec := func(h *lsHarness, phase string, begun bool) linkSwitchRecord {
		sum, _ := fileSHA256(h.pkg)
		r := linkSwitchRecord{Operation: "ls_00112233aabbccdd", MachineID: "box", By: "device", Phase: phase,
			StartedAt: time.Now().Add(-time.Minute), At: time.Now(), Deadline: time.Now().Add(-time.Second), SSHHost: "box.example",
			Arch: "amd64", SHA256: sum, BeginIntended: begun}
		if begun {
			r.PairHash, r.Install = lsTestHash, lsInstallPlaced
		}
		return r
	}
	// What a switch's installer had left: Persona Link placed, paired, its services.
	installed := func(h *lsHarness, connected bool) {
		b, _ := os.ReadFile(h.pkg)
		mustWrite(t, filepath.Join(h.binOf, "persona-link"), string(b), 0o755)
		mustWrite(t, filepath.Join(h.persona, "link.json"), "{}", 0o600)
		mustWrite(t, filepath.Join(h.units, "persona-link.service"), "unit", 0o644)
		mustWrite(t, filepath.Join(h.units, "personad-node.service"), "unit", 0o644)
		if connected {
			mustWrite(t, filepath.Join(h.persona, ".connected"), "", 0o644)
		}
	}
	resume := func(h *lsHarness, r linkSwitchRecord) linkSwitchRecord {
		if err := h.store.PutSetting(context.Background(), linkSwitchKey("box"), r); err != nil {
			t.Fatal(err)
		}
		h.d.resumeLinkSwitches(context.Background())
		return h.finished()
	}

	t.Run("before begin", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{})
		r := rec(h, proto.LinkSwitchPhaseInstalling, false)
		mustWrite(t, filepath.Join(h.home, stagedName(r.Operation)), "partial", 0o755)
		got := resume(h, r)
		if got.Phase != proto.LinkSwitchPhaseFailed || got.Error != proto.LinkSwitchReasonInterrupted || got.RollbackUnconfirmed {
			t.Fatalf("record %+v", got)
		}
		if h.exists(stagedName(r.Operation)) {
			t.Error("the staged build is left")
		}
		if begins, ends := h.hub.calls(); len(begins)+len(ends) > 0 {
			t.Errorf("the hub was asked: %+v %+v", begins, ends)
		}
	})
	t.Run("after begin, link online", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{kind: kindLink, setup: func(h *lsHarness) { installed(h, true) }})
		got := resume(h, rec(h, proto.LinkSwitchPhaseConnecting, true))
		if got.Phase != proto.LinkSwitchPhaseSucceeded {
			t.Fatalf("record %+v", got)
		}
		if _, ends := h.hub.calls(); len(ends) != 1 || ends[0].Outcome != devicewire.LinkSwitchSucceeded || ends[0].PairHash != lsTestHash {
			t.Errorf("ends %+v", ends)
		}
		if !h.exists(".persona/link.json") || !slices.Equal(h.whats(), []string{"remove staged"}) {
			t.Errorf("touched the computer: %q", h.whats())
		}
	})
	t.Run("after begin, no link", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{kind: kindLink, setup: func(h *lsHarness) { installed(h, false) }})
		got := resume(h, rec(h, proto.LinkSwitchPhaseConnecting, true))
		if got.Phase != proto.LinkSwitchPhaseFailed || got.Error != proto.LinkSwitchReasonInterrupted || got.RollbackUnconfirmed {
			t.Fatalf("record %+v", got)
		}
		if _, ends := h.hub.calls(); len(ends) != 1 || ends[0].Outcome != devicewire.LinkSwitchRolledBack || ends[0].PairHash != lsTestHash {
			t.Errorf("ends %+v", ends)
		}
		for _, rel := range []string{".persona/bin/persona-link", ".persona/link.json", ".config/systemd/user/persona-link.service",
			".config/systemd/user/personad-node.service"} {
			if h.exists(rel) {
				t.Errorf("%s is left", rel)
			}
		}
		if !h.d.sshOnline("box") {
			t.Error("box is not back over ssh")
		}
	})
	t.Run("a finished one is left alone", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{})
		r := rec(h, proto.LinkSwitchPhaseFailed, true)
		if err := h.store.PutSetting(context.Background(), linkSwitchKey("box"), r); err != nil {
			t.Fatal(err)
		}
		h.d.resumeLinkSwitches(context.Background())
		h.switchesDone()
		if begins, ends := h.hub.calls(); len(begins)+len(ends) > 0 || len(h.whats()) > 0 {
			t.Errorf("acted on a finished switch: %q", h.whats())
		}
		// And a start sees it: a new switch may begin.
		if got := h.d.linkSwitchRecordOf("box"); got == nil || got.Phase != proto.LinkSwitchPhaseFailed {
			t.Errorf("record %+v", got)
		}
	})
}

// A stored unfinished switch blocks a start even before the restarted manager looked at it.
func TestLinkSwitchStartReadsTheStoredRecord(t *testing.T) {
	h := newLSHarness(t, lsOptions{})
	_ = h.store.PutSetting(context.Background(), linkSwitchKey("box"), linkSwitchRecord{Operation: "ls_1", MachineID: "box",
		Phase: proto.LinkSwitchPhaseConnecting, BeginIntended: true})
	code, body, raw := h.post("full")
	var c string
	_ = json.Unmarshal(body["code"], &c)
	if code != http.StatusConflict || c != proto.LinkSwitchReasonInProgress {
		t.Fatalf("%d %s", code, raw)
	}
}

// What the machine list says of a switch, cheaply: for an ssh computer this manager connects, whether it can start
// and why not, in a fixed order; for others nothing, but the switch that ran on them within the hour.
func TestLinkSwitchAvailability(t *testing.T) {
	linux := proto.NodeInfo{OS: "linux", Arch: "arm64", Home: "/home/me", DataDir: "/home/me/.persona"}
	type mc = MachineConfig
	cases := []struct {
		name   string
		cfg    mc
		info   proto.NodeInfo
		online bool
		edit   func(d *Devices, m *Machine)
		want   *proto.MachineLinkSwitch // nil: none shown
	}{
		{"available", mc{Kind: "ssh"}, linux, true, nil, &proto.MachineLinkSwitch{Available: true}},
		{"kind unset is ssh", mc{}, linux, true, nil, &proto.MachineLinkSwitch{Available: true}},
		{"a switch runs", mc{Kind: "ssh"}, linux, true, func(d *Devices, m *Machine) {
			d.switches.records = map[string]*linkSwitchRecord{"box": {MachineID: "box", Phase: proto.LinkSwitchPhaseInstalling}}
			d.cfg.LinkSwitch = false // the order: in_progress first
		}, &proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonInProgress, Phase: proto.LinkSwitchPhaseInstalling}},
		{"no Persona Link at the hub", mc{Kind: "ssh"}, linux, false, func(d *Devices, m *Machine) {
			d.cfg.LinkSwitch, d.cfg.Endpoints = false, nil
		}, &proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonLinkUnavailable}},
		{"older hub", mc{Kind: "ssh"}, linux, false, func(d *Devices, m *Machine) { d.cfg.LinkSwitch = false },
			&proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonHubOld}},
		{"pending", mc{Kind: "ssh"}, linux, false, func(d *Devices, m *Machine) { d.cfg.Pending = []string{"box"} },
			&proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonPending}},
		{"pending unknown", mc{Kind: "ssh"}, linux, true, func(d *Devices, m *Machine) { d.cfg.PendingUnknown = true },
			&proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonPending}},
		{"remote_bin", mc{Kind: "ssh", RemoteBin: "/opt/persona/personad"}, linux, false, nil,
			&proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonCustomNode}},
		{"default remote_bin", mc{Kind: "ssh", RemoteBin: "~/.persona/bin/personad"}, linux, true, nil, &proto.MachineLinkSwitch{Available: true}},
		{"data directory", mc{Kind: "ssh"}, proto.NodeInfo{OS: "linux", Arch: "amd64", Home: "/home/me", DataDir: "/srv/persona"}, true, nil,
			&proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonCustomNode}},
		{"data directory with a trailing slash", mc{Kind: "ssh"}, proto.NodeInfo{OS: "linux", Arch: "amd64", Home: "/home/me", DataDir: "/home/me/.persona/"},
			true, nil, &proto.MachineLinkSwitch{Available: true}},
		{"data directory not said (an older node: the probe looks)", mc{Kind: "ssh"}, proto.NodeInfo{OS: "linux", Arch: "amd64", Home: "/home/me"},
			true, nil, &proto.MachineLinkSwitch{Available: true}},
		{"offline", mc{Kind: "ssh"}, linux, false, nil, &proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonOffline}},
		{"never connected", mc{Kind: "ssh"}, proto.NodeInfo{}, false, nil, &proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonOffline}},
		{"mac", mc{Kind: "ssh"}, proto.NodeInfo{OS: "darwin", Arch: "arm64", Home: "/Users/me", DataDir: "/Users/me/.persona"}, true, nil,
			&proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonUnsupportedOS}},
		{"windows", mc{Kind: "ssh"}, proto.NodeInfo{OS: "windows", Arch: "amd64", Home: `C:\Users\me`, DataDir: `C:\Users\me\.persona`}, true, nil,
			&proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonUnsupportedOS}},
		{"32-bit", mc{Kind: "ssh"}, proto.NodeInfo{OS: "linux", Arch: "386", Home: "/home/me", DataDir: "/home/me/.persona"}, true, nil,
			&proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonUnsupportedOS}},
		{"updating", mc{Kind: "ssh"}, linux, true, func(d *Devices, m *Machine) { m.deploying = true },
			&proto.MachineLinkSwitch{Reason: proto.LinkSwitchReasonBusy}},
		{"failed earlier", mc{Kind: "ssh"}, linux, true, func(d *Devices, m *Machine) {
			d.switches.records = map[string]*linkSwitchRecord{"box": {MachineID: "box", Phase: proto.LinkSwitchPhaseFailed,
				Error: proto.LinkSwitchReasonLingerOff, At: time.Unix(1e9, 0)}}
		}, &proto.MachineLinkSwitch{Available: true}}, // over an hour ago: not shown
		{"failed just now", mc{Kind: "ssh"}, linux, true, func(d *Devices, m *Machine) {
			d.switches.records = map[string]*linkSwitchRecord{"box": {MachineID: "box", Phase: proto.LinkSwitchPhaseFailed,
				Error: proto.LinkSwitchReasonLinkTimeout, RollbackUnconfirmed: true, At: time.Unix(2e9, 0)}}
		}, &proto.MachineLinkSwitch{Available: true, Phase: proto.LinkSwitchPhaseFailed, Error: proto.LinkSwitchReasonLinkTimeout,
			RollbackUnconfirmed: true, At: time.Unix(2e9, 0)}},
		{"the hub's own computer", mc{Kind: "local"}, linux, true, nil, nil},
		{"Persona Link", mc{Kind: kindLink}, linux, true, nil, nil},
		{"Persona Link just switched", mc{Kind: kindLink}, linux, true, func(d *Devices, m *Machine) {
			d.switches.records = map[string]*linkSwitchRecord{"box": {MachineID: "box", Phase: proto.LinkSwitchPhaseSucceeded, At: time.Unix(2e9, 0)}}
		}, &proto.MachineLinkSwitch{Phase: proto.LinkSwitchPhaseSucceeded, At: time.Unix(2e9, 0)}},
		{"a member hub's computer", mc{Kind: "ssh"}, linux, true, func(d *Devices, m *Machine) { m.derived = true }, nil},
		{"this hub is a member", mc{Kind: "ssh"}, linux, true, func(d *Devices, m *Machine) { d.cfg.Mesh = &devicewire.Mesh{Member: true} }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newTestDevices(t)
			d.switches.times = lsTestTimes
			d.switches.times.Shown = time.Duration(1<<63 - 1)
			if c.name == "failed earlier" {
				d.switches.times.Shown = time.Hour
			}
			d.mu.Lock()
			d.cfg.LinkSwitch = true
			d.cfg.Endpoints = []devicewire.LinkEndpoint{{URL: "https://hub.example.com", Kind: "public"}}
			d.mu.Unlock()
			c.cfg.ID, c.cfg.Name = "box", "Box"
			m := newMachine(d, c.cfg)
			m.info, m.online = c.info, c.online
			d.addEntry(m)
			if c.edit != nil {
				d.mu.Lock()
				c.edit(d, m)
				d.mu.Unlock()
			}
			got := m.snapshot().LinkSwitch
			if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", c.want) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// The scripts read the same in every login shell a computer may have (sshd hands the command to it): the probe looks
// and the uninstall finds nothing to remove.
func TestLinkSwitchScriptsRunInEveryShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the switch's scripts run on Linux computers")
	}
	for _, shell := range loginShells(t) {
		home := t.TempDir()
		stubs := t.TempDir()
		mustWrite(t, filepath.Join(stubs, "systemctl"), "#!/bin/sh\nexit 0\n", 0o755)
		mustWrite(t, filepath.Join(home, ".persona", "bin", "personad"), "#!/bin/sh\n", 0o755)
		for _, script := range []string{lsProbeScript(lsPaths{systemd: home, linger: home}), lsUninstallScript("ls_0011", "", true, true)} {
			cmd := exec.Command(shell, "-c", script)
			cmd.Env = shellEnv(home, stubs)
			out, err := cmd.Output()
			kv := outputOf(out)
			if err != nil || (kv["end"] != "1" && kv["clean"] != "1") {
				t.Errorf("%s: %v %q", filepath.Base(shell), err, out)
			}
			if kv["end"] == "1" && (kv["node_bin"] != "1" || kv["link"] != "0" || kv["systemd"] != "1") {
				t.Errorf("%s: the probe found %v", filepath.Base(shell), kv)
			}
		}
	}
}

// A node update the manager would start by itself waits for the switch, with nothing recorded against the build: the
// next connection after the switch looks again (updateOnConnect).
func TestLinkSwitchHoldsAutomaticNodeUpdates(t *testing.T) {
	t.Setenv("PERSONA_HOME", filepath.Join(t.TempDir(), ".persona")) // the package cache, nowhere real
	h := newLSHarness(t, lsOptions{})
	release := h.hold("probe")
	h.start()
	h.d.mu.Lock()
	h.d.cfg.AutoUpdateNodes, h.d.cfg.HubVersion = true, "newer"
	h.d.mu.Unlock()
	m := h.d.cfgMachine("box")
	m.updateOnConnect(context.Background())
	h.mu.Lock()
	logs := h.logs.String()
	h.mu.Unlock()
	if strings.Contains(logs, "[box] node test") || strings.Contains(logs, "[box] update:") {
		t.Errorf("an update was looked for during the switch:\n%s", logs)
	}
	if ok, _ := h.store.Setting(context.Background(), m.deployRecordKey(), &nodeDeployRecord{}); ok {
		t.Error("a node update was recorded during the switch")
	}
	h.d.mu.Lock()
	h.d.cfg.AutoUpdateNodes, h.d.cfg.HubVersion = false, "test"
	h.d.mu.Unlock()
	release()
	h.finished()
}

// Persona Link appearing on the computer between the probe and the install (another install of the same build) is
// not this switch's: nothing is placed, and the rollback leaves it, the program the switch would have sent included.
func TestLinkSwitchKeepsALinkThatAppeared(t *testing.T) {
	h := newLSHarness(t, lsOptions{})
	h.hub.onBegin = func() {
		b, _ := os.ReadFile(h.pkg)
		mustWrite(t, filepath.Join(h.binOf, "persona-link"), string(b), 0o755)
		mustWrite(t, filepath.Join(h.persona, "link.json"), "theirs", 0o600)
		mustWrite(t, filepath.Join(h.units, "persona-link.service"), "theirs", 0o644)
	}
	h.start()
	rec := h.finished()
	if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != proto.LinkSwitchReasonExistingLink || rec.RollbackUnconfirmed ||
		rec.Install != lsInstallNotPlaced {
		t.Fatalf("record %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(h.persona, ".pair-got")); err == nil {
		t.Error("the installer ran")
	}
	for _, rel := range []string{".persona/bin/persona-link", ".persona/link.json", ".config/systemd/user/persona-link.service"} {
		if !h.exists(rel) {
			t.Errorf("%s, which the switch did not put there, was removed", rel)
		}
	}
	if h.exists(stagedName(rec.Operation)) || !h.d.sshOnline("box") {
		t.Error("not as before")
	}
}

// What each answer of the hub's end says: taken ({}; not_ssh to a rollback, the hub revoked before it looked at the
// kind), the switch succeeded (already_succeeded to a rollback), never to be taken (no_machine, invalid_params; not_ssh
// to a success), or asked again (no answer, any other code).
func TestLinkSwitchEndAnswers(t *testing.T) {
	for _, c := range []struct {
		code, outcome string
		want          endAnswer
	}{
		{"", devicewire.LinkSwitchRolledBack, endTaken},
		{devicewire.LinkSwitchCodeNotSSH, devicewire.LinkSwitchRolledBack, endTaken},
		{devicewire.LinkSwitchCodeSucceeded, devicewire.LinkSwitchRolledBack, endSucceeded},
		{devicewire.LinkSwitchCodeNoMachine, devicewire.LinkSwitchRolledBack, endRefused},
		{"invalid_params", devicewire.LinkSwitchRolledBack, endRefused},
		{devicewire.LinkSwitchCodeChanged, devicewire.LinkSwitchRolledBack, endNoAnswer},
		{"unknown_method", devicewire.LinkSwitchRolledBack, endUnknown},
		{devicewire.LinkSwitchCodeRolledBackGone, devicewire.LinkSwitchRolledBack, endGone},
		{"unknown_method", devicewire.LinkSwitchSucceeded, endUnknown},
		{"", devicewire.LinkSwitchSucceeded, endTaken},
		{devicewire.LinkSwitchCodeSucceeded, devicewire.LinkSwitchSucceeded, endTaken},
		{devicewire.LinkSwitchCodeNotSSH, devicewire.LinkSwitchSucceeded, endRefused},
		{"invalid_params", devicewire.LinkSwitchSucceeded, endRefused},
		{devicewire.LinkSwitchCodeChanged, devicewire.LinkSwitchSucceeded, endNoAnswer},
	} {
		t.Run(c.outcome+"/"+c.code, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{})
			h.hub.endCode = c.code
			r := &linkSwitchRun{d: h.d, t: lsTestTimes, rec: linkSwitchRecord{MachineID: "box", Operation: "ls_1", PairHash: lsTestHash}}
			if got := r.endOnce(context.Background(), c.outcome); got != c.want {
				t.Errorf("answer %d, want %d", got, c.want)
			}
		})
	}
	t.Run("not answered", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{})
		h.hub.endHeld.Store(true)
		r := &linkSwitchRun{d: h.d, t: lsTestTimes, rec: linkSwitchRecord{MachineID: "box", Operation: "ls_1"}}
		if got := r.endOnce(context.Background(), devicewire.LinkSwitchRolledBack); got != endNoAnswer {
			t.Errorf("answer %d", got)
		}
	})
}

// A hub that will never take the rollback (no_machine: the computer moved to another manager; invalid_params) is
// asked once; the computer keeps its Persona Link (the hub may still have it on kind link), failed and unconfirmed.
func TestLinkSwitchRollbackRefusedByTheHub(t *testing.T) {
	for _, code := range []string{"invalid_params", devicewire.LinkSwitchCodeNoMachine} {
		t.Run(code, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644) }})
			h.hub.endCode = code
			h.start()
			rec := h.finished()
			if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != proto.LinkSwitchReasonInstallFailed || !rec.RollbackUnconfirmed ||
				rec.HubEnded != lsEndedRefused {
				t.Fatalf("record %+v", rec)
			}
			if _, ends := h.hub.calls(); len(ends) != 1 {
				t.Errorf("end asked %d times", len(ends))
			}
			if slices.Contains(h.whats(), "uninstall") {
				t.Errorf("ssh runs %q", h.whats())
			}
			for _, rel := range []string{".persona/bin/persona-link", ".persona/link.json", ".config/systemd/user/persona-link.service"} {
				if !h.exists(rel) {
					t.Errorf("%s was removed", rel)
				}
			}
		})
	}
}

// installed puts on the computer what a switch's installer leaves: Persona Link placed (the build sent), paired, its
// service and the node service it set up.
func (h *lsHarness) installed() {
	b, _ := os.ReadFile(h.pkg)
	mustWrite(h.t, filepath.Join(h.binOf, "persona-link"), string(b), 0o755)
	mustWrite(h.t, filepath.Join(h.persona, "link.json"), "{}", 0o600)
	mustWrite(h.t, filepath.Join(h.units, "persona-link.service"), "unit", 0o644)
	mustWrite(h.t, filepath.Join(h.units, "personad-node.service"), "unit", 0o644)
}

// waitEnds waits until the hub was asked at least n ends.
// switchesDone waits until no goroutine of the manager runs a switch (one a resume may have started has ended).
func (h *lsHarness) switchesDone() {
	h.t.Helper()
	done := make(chan struct{})
	go func() { h.d.switches.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		h.t.Fatal("a switch still runs")
	}
}

func (h *lsHarness) waitEnds(n int) {
	h.t.Helper()
	eventually(h.t, 20*time.Second, fmt.Sprintf("%d ends asked", n), func() bool {
		_, ends := h.hub.calls()
		return len(ends) >= n
	})
}

// A rollback the hub does not answer is asked again for as long as it takes (well past the 2 minutes it once gave
// up after, in this test's scale: many tries with the backoff at its longest), and nothing on the computer is
// touched until it answers: the switch stays rolling_back, Persona Link in place. Then only what the switch set up
// goes. A manager that stops meanwhile leaves it rolling_back, and one that resumes it keeps asking the same way.
func TestLinkSwitchRollbackWaitsForTheHub(t *testing.T) {
	check := func(h *lsHarness) {
		h.t.Helper()
		h.waitEnds(12) // 20 ms doubling to 200 ms: past the longest wait several times
		// Three more asks at the longest wait: the hub stays away a while longer and still nothing is touched.
		_, asked := h.hub.calls()
		h.waitEnds(len(asked) + 3)
		if rec := h.d.linkSwitchRecordOf("box"); rec.Phase != proto.LinkSwitchPhaseRollingBack || rec.HubEnded != "" {
			h.t.Fatalf("record while the hub is away %+v", rec)
		}
		if slices.Contains(h.whats(), "uninstall") || !h.exists(".persona/link.json") || !h.exists(".persona/bin/persona-link") {
			h.t.Fatalf("the computer was touched before the hub answered: %q", h.whats())
		}
		h.hub.endHeld.Store(false)
		rec := h.finished()
		if rec.Phase != proto.LinkSwitchPhaseFailed || rec.RollbackUnconfirmed || rec.HubEnded != lsEndedRolledBack {
			h.t.Fatalf("record %+v", rec)
		}
		for _, rel := range []string{".persona/bin/persona-link", ".persona/link.json", ".config/systemd/user/persona-link.service"} {
			if h.exists(rel) {
				h.t.Errorf("%s is left", rel)
			}
		}
		if !h.d.sshOnline("box") {
			h.t.Error("box is not back over ssh")
		}
	}
	t.Run("running", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644) }})
		h.hub.endHeld.Store(true)
		h.start()
		check(h)
	})
	t.Run("the manager stops", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644) }})
		h.hub.endHeld.Store(true)
		h.start()
		h.waitEnds(3)
		h.close()
		if st := h.stored(); st.Phase != proto.LinkSwitchPhaseRollingBack || st.HubEnded != "" {
			t.Errorf("stored %+v", st)
		}
		if slices.Contains(h.whats(), "uninstall") || !h.exists(".persona/link.json") {
			t.Errorf("the computer was touched: %q", h.whats())
		}
	})
	t.Run("resumed after a restart", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{kind: kindLink, setup: func(h *lsHarness) { h.installed() }})
		sum, _ := fileSHA256(h.pkg)
		h.hub.endHeld.Store(true)
		_ = h.store.PutSetting(context.Background(), linkSwitchKey("box"), linkSwitchRecord{Operation: "ls_1", MachineID: "box",
			Phase: proto.LinkSwitchPhaseRollingBack, Error: proto.LinkSwitchReasonInstallFailed, Deadline: time.Now(),
			SSHHost: "box.example", SHA256: sum, BeginIntended: true, PairHash: lsTestHash, Install: lsInstallPlaced})
		h.d.resumeLinkSwitches(context.Background())
		check(h)
		if rec := h.d.linkSwitchRecordOf("box"); rec.Error != proto.LinkSwitchReasonInstallFailed {
			t.Errorf("record %+v", rec)
		}
	})
	t.Run("ended before the restart", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{kind: kindLink, setup: func(h *lsHarness) { h.installed() }})
		sum, _ := fileSHA256(h.pkg)
		_ = h.store.PutSetting(context.Background(), linkSwitchKey("box"), linkSwitchRecord{Operation: "ls_1", MachineID: "box",
			Phase: proto.LinkSwitchPhaseRollingBack, Error: proto.LinkSwitchReasonInstallFailed, Deadline: time.Now(),
			SSHHost: "box.example", SHA256: sum, BeginIntended: true, PairHash: lsTestHash, Install: lsInstallPlaced,
			HubEnded: lsEndedRolledBack})
		h.hub.setKind("ssh") // the hub had put it back
		h.d.resumeLinkSwitches(context.Background())
		rec := h.finished()
		if rec.Phase != proto.LinkSwitchPhaseFailed || rec.RollbackUnconfirmed {
			t.Fatalf("record %+v", rec)
		}
		if _, ends := h.hub.calls(); len(ends) != 0 {
			t.Errorf("the hub was asked again: %+v", ends)
		}
		if h.exists(".persona/link.json") {
			t.Error("not cleaned up")
		}
	})
}

// A success the hub does not take at once stays succeeded, and end(succeeded) is asked again in the background
// until it does, and again after a restart while the record says it did not.
func TestLinkSwitchSuccessEndIsAskedUntilTaken(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{})
		h.hub.endHeld.Store(true)
		h.start()
		eventually(t, 20*time.Second, "succeeded", func() bool {
			rec := h.d.linkSwitchRecordOf("box")
			return rec != nil && rec.Phase == proto.LinkSwitchPhaseSucceeded
		})
		h.waitEnds(8)
		if st := h.stored(); st.Phase != proto.LinkSwitchPhaseSucceeded || st.HubEnded != "" {
			t.Fatalf("stored while the hub is away %+v", st)
		}
		if !slices.Contains(h.whats(), "remove staged") {
			t.Error("the success's cleanup waited for the hub")
		}
		h.hub.endHeld.Store(false)
		rec := h.finished()
		if rec.Phase != proto.LinkSwitchPhaseSucceeded || rec.HubEnded != lsEndedSucceeded || h.stored().HubEnded != lsEndedSucceeded {
			t.Fatalf("record %+v", rec)
		}
		if _, ends := h.hub.calls(); slices.ContainsFunc(ends, func(e devicewire.LinkSwitchEnd) bool { return e.Outcome != devicewire.LinkSwitchSucceeded }) {
			t.Errorf("ends %+v", ends)
		}
	})
	t.Run("after a restart", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{kind: kindLink, setup: func(h *lsHarness) {
			h.installed()
			mustWrite(t, filepath.Join(h.persona, ".connected"), "", 0o644)
		}})
		h.hub.endFail = 4
		_ = h.store.PutSetting(context.Background(), linkSwitchKey("box"), linkSwitchRecord{Operation: "ls_1", MachineID: "box",
			Phase: proto.LinkSwitchPhaseSucceeded, At: time.Now(), SSHHost: "box.example", BeginIntended: true, PairHash: lsTestHash,
			Install: lsInstallPlaced})
		h.d.resumeLinkSwitches(context.Background())
		rec := h.finished()
		if rec.Phase != proto.LinkSwitchPhaseSucceeded || rec.HubEnded != lsEndedSucceeded {
			t.Fatalf("record %+v", rec)
		}
		if _, ends := h.hub.calls(); len(ends) != 5 {
			t.Errorf("end asked %d times, want 5", len(ends))
		}
		if !h.exists(".persona/link.json") || !slices.Equal(h.whats(), []string{"remove staged"}) {
			t.Errorf("touched the computer: %q", h.whats())
		}
	})
	t.Run("taken already", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{kind: kindLink})
		_ = h.store.PutSetting(context.Background(), linkSwitchKey("box"), linkSwitchRecord{Operation: "ls_1", MachineID: "box",
			Phase: proto.LinkSwitchPhaseSucceeded, At: time.Now(), HubEnded: lsEndedSucceeded})
		h.d.resumeLinkSwitches(context.Background())
		h.switchesDone()
		if _, ends := h.hub.calls(); len(ends) > 0 || len(h.whats()) > 0 {
			t.Errorf("asked again: %+v %q", ends, h.whats())
		}
	})
}

// The program a switch runs on the computer is the build it sent, from a directory only the user writes: a data
// directory or bin others may write to is refused before anything is written there (stage) or placed (install), and
// what was linked into place is checked again before it runs; a mismatch takes it out again and runs nothing.
func TestLinkSwitchRunsOnlyTheBuildItSent(t *testing.T) {
	for _, c := range []struct {
		name, want string
		setup      func(h *lsHarness)
	}{
		{"the staged build changed", proto.LinkSwitchReasonInstallFailed, func(h *lsHarness) {
			h.hub.onBegin = func() {
				staged, _ := filepath.Glob(filepath.Join(h.binOf, lsStagePrefix+"*"))
				for _, f := range staged {
					fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
					if err == nil {
						_, _ = fh.WriteString("echo swapped\n")
						_ = fh.Close()
					}
				}
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{setup: c.setup})
			h.start()
			rec := h.finished()
			if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != c.want || rec.RollbackUnconfirmed {
				t.Fatalf("record %+v, want %s", rec, c.want)
			}
			if c.want == proto.LinkSwitchReasonInstallFailed && rec.Install != lsInstallNotPlaced {
				t.Errorf("record %+v", rec)
			}
			if _, err := os.Stat(filepath.Join(h.persona, ".pair-got")); err == nil {
				t.Error("the installer ran")
			}
			if h.exists(".persona/bin/persona-link") || h.exists(stagedName(rec.Operation)) {
				t.Error("a build is left on the computer")
			}
		})
	}
}

// asUser has the computer's `id -un` say name (a stub first on PATH), with a linger file for it.
func (h *lsHarness) asUser(name string) {
	real, err := exec.LookPath("id")
	if err != nil {
		h.t.Skip("no id")
	}
	mustWrite(h.t, filepath.Join(h.stubs, "id"), "#!/bin/sh\nif [ \"$1\" = -un ]; then echo "+shellQuote(name)+"; exit 0; fi\nexec "+
		shellQuote(real)+" \"$@\"\n", 0o755)
	mustWrite(h.t, filepath.Join(h.root, "linger", name), "", 0o644)
}

// lsAs has the computer's user be user (asUser), and its ls say every directory is owned by user and of group
// (a stub first on PATH), but foreign ("": none), owned by someone else: the owners and groups a test cannot make.
func (h *lsHarness) lsAs(user, group, foreign string) {
	real, err := exec.LookPath("ls")
	if err != nil {
		h.t.Skip("no ls")
	}
	h.asUser(user)
	mustWrite(h.t, filepath.Join(h.stubs, "ls"), "#!/bin/sh\nfor a; do last=$a; done\n"+shellQuote(real)+
		` "$@" | awk -v p="$last" -v f="${LS_FOREIGN:-}" -v o="${LS_OWNER:-}" -v g="${LS_GROUP:-}" `+
		`'{ if (o != "") $3 = o; if (g != "") $4 = g; if (f != "" && p == f) $3 = "someone-else"; print }'`+"\n", 0o755)
	h.extraEnv = append(h.extraEnv, "LS_OWNER="+user, "LS_GROUP="+group, "LS_FOREIGN="+foreign)
}

// A ~/.persona or bin another user may write to (others; a group but the user's own private group) is refused in the
// probe, with nothing changed, and again by the stage and the install when it became so after the probe: its own
// reason, unsafe_permissions. The group a umask of 002 makes writable on distributions with a private group per user
// is the user's own, and is taken.
func TestLinkSwitchRefusesUnsafePermissions(t *testing.T) {
	chmod := func(h *lsHarness, dir string, mode os.FileMode) {
		if err := os.Chmod(dir, mode); err != nil {
			h.t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		setup func(h *lsHarness)
		want  string   // "": succeeds
		runs  []string // the ssh runs
	}{
		{"data directory writable by others", func(h *lsHarness) { chmod(h, h.persona, 0o757) },
			proto.LinkSwitchReasonUnsafePermissions, []string{"probe"}},
		{"bin writable by others", func(h *lsHarness) { chmod(h, h.binOf, 0o757) },
			proto.LinkSwitchReasonUnsafePermissions, []string{"probe"}},
		{"home folder writable by others", func(h *lsHarness) { chmod(h, h.home, 0o757) },
			proto.LinkSwitchReasonUnsafePermissions, []string{"probe"}},
		{"bin writable by a shared group", func(h *lsHarness) {
			chmod(h, h.binOf, 0o775)
			h.lsAs("me-test", "staff", "")
		}, proto.LinkSwitchReasonUnsafePermissions, []string{"probe"}},
		{"home folder writable by a shared group", func(h *lsHarness) {
			chmod(h, h.home, 0o775)
			h.lsAs("me-test", "staff", "")
		}, proto.LinkSwitchReasonUnsafePermissions, []string{"probe"}},
		{"umask 002 with the user's own private group", func(h *lsHarness) {
			for _, dir := range []string{h.home, h.persona, h.binOf} {
				chmod(h, dir, 0o775)
			}
			h.lsAs("me-test", "me-test", "")
		}, "", []string{"probe", "stage", "install", "remove staged"}},
		{"home folder not the user's", func(h *lsHarness) { h.lsAs("me-test", "me-test", h.home) },
			proto.LinkSwitchReasonUnsafePermissions, []string{"probe"}},
		{"data directory not the user's", func(h *lsHarness) { h.lsAs("me-test", "me-test", h.persona) },
			proto.LinkSwitchReasonUnsafePermissions, []string{"probe"}},
		{"bin not the user's", func(h *lsHarness) { h.lsAs("me-test", "me-test", h.binOf) },
			proto.LinkSwitchReasonUnsafePermissions, []string{"probe"}},
		{"the user's own, through the stub", func(h *lsHarness) { h.lsAs("me-test", "me-test", "") },
			"", []string{"probe", "stage", "install", "remove staged"}},
		{"writable by others since the probe, at the stage", func(h *lsHarness) {
			h.onRun = func(what string) {
				if what == "stage" {
					chmod(h, h.binOf, 0o757)
				}
			}
		}, proto.LinkSwitchReasonUnsafePermissions, []string{"probe", "stage", "remove staged"}},
		{"writable by others since the probe, at the install", func(h *lsHarness) {
			h.hub.onBegin = func() { chmod(h, h.binOf, 0o757) }
		}, proto.LinkSwitchReasonUnsafePermissions, []string{"probe", "stage", "install", "uninstall"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{setup: c.setup})
			before := h.tree()
			h.start()
			rec := h.finished()
			want := orDefault(c.want, proto.LinkSwitchPhaseSucceeded)
			if got := orDefault(rec.Error, rec.Phase); got != want || rec.RollbackUnconfirmed {
				t.Fatalf("record %+v, want %s", rec, want)
			}
			if got := h.whats(); !slices.Equal(got, c.runs) {
				t.Errorf("ssh runs %q, want %q", got, c.runs)
			}
			begins, _ := h.hub.calls()
			switch {
			case slices.Equal(c.runs, []string{"probe"}):
				if after := h.tree(); fmt.Sprint(before) != fmt.Sprint(after) || len(begins) > 0 {
					t.Errorf("the probe changed something: hub %+v\nbefore %v\nafter  %v", begins, before, after)
				}
			case c.want != "":
				if _, err := os.Stat(filepath.Join(h.persona, ".pair-got")); err == nil || h.exists(".persona/bin/persona-link") ||
					h.exists(stagedName(rec.Operation)) {
					t.Error("the installer ran, or a build is left")
				}
			}
		})
	}
}

// An installer that gave up before it paired (the hub not reachable from the computer) fails the switch at once:
// no link can come, so the rollback starts without waiting for one. One that paired, or whose ssh failed, may still
// connect and is waited for.
func TestLinkSwitchInstallerThatNeverPairedFailsAtOnce(t *testing.T) {
	const connect = 4 * time.Second
	for _, c := range []struct {
		name  string
		setup func(h *lsHarness)
		wait  bool
		want  string // "": install_failed
	}{
		{"never paired", func(h *lsHarness) { h.force["install"], h.forceOut["install"] = 1, "placed=1\n" }, false, ""},
		{"paired", func(h *lsHarness) { h.force["install"], h.forceOut["install"] = 1, "placed=1\npaired=1\n" }, true, ""},
		{"ssh failed after placing", func(h *lsHarness) { h.force["install"], h.forceOut["install"] = 255, "placed=1\n" }, true, ""},
		{"ssh failed before placing", func(h *lsHarness) { h.force["install"] = 255 }, true, proto.LinkSwitchReasonLinkTimeout},
		{"the installer, before pairing", func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".fail-pair"), "", 0o644) }, false, ""},
		{"the installer, after pairing", func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644) }, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) {
				h.d.switches.times.Connect = connect
				c.setup(h)
			}})
			begun := time.Now()
			h.start()
			var took time.Duration // until the rollback began
			eventually(t, 30*time.Second, "the rollback begins", func() bool {
				r := h.d.linkSwitchRecordOf("box")
				took = time.Since(begun)
				return r != nil && (r.Phase == proto.LinkSwitchPhaseRollingBack || r.finished())
			})
			rec := h.finished()
			if want := orDefault(c.want, proto.LinkSwitchReasonInstallFailed); rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != want || rec.RollbackUnconfirmed {
				t.Fatalf("record %+v, want %s", rec, want)
			}
			if c.wait && took < connect {
				t.Errorf("rolling back after %s, before the link had its %s", took, connect)
			}
			if !c.wait && took >= connect/2 {
				t.Errorf("waited %s for a link that cannot come", took)
			}
			if _, ends := h.hub.calls(); len(ends) != 1 || ends[0].Outcome != devicewire.LinkSwitchRolledBack {
				t.Errorf("ends %+v", ends)
			}
			if h.exists(".persona/bin/persona-link") || h.exists(".persona/link.json") || !h.d.sshOnline("box") {
				t.Error("not rolled back")
			}
		})
	}
}

// A node service the computer has, or one persona-link may put back from its node switch's backup, is never removed
// by a rollback: found by the probe, found by the install just before the installer ran (it appeared since), or
// marked on the computer by that install when the manager's record lost it (a restart).
func TestLinkSwitchRollbackKeepsANodeServiceThatWasThere(t *testing.T) {
	unit := func(h *lsHarness) string { return filepath.Join(h.units, "personad-node.service") }
	for _, c := range []struct {
		name  string
		setup func(h *lsHarness)
	}{
		{"a backup when it probed", func(h *lsHarness) {
			mustWrite(t, filepath.Join(h.persona, lsNodeBackup), "the computer's own", 0o644)
		}},
		{"a backup since", func(h *lsHarness) {
			h.hub.onBegin = func() { mustWrite(t, filepath.Join(h.persona, lsNodeBackup), "the computer's own", 0o644) }
		}},
		{"a service since", func(h *lsHarness) {
			h.hub.onBegin = func() { mustWrite(t, unit(h), "the computer's own", 0o644) }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) {
				mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644)
				c.setup(h)
			}})
			h.start()
			rec := h.finished()
			if rec.Error != proto.LinkSwitchReasonInstallFailed || rec.RollbackUnconfirmed || !rec.NodeUnitExisted {
				t.Fatalf("record %+v", rec)
			}
			if b, err := os.ReadFile(unit(h)); string(b) != "the computer's own" {
				t.Errorf("the computer's node service was removed: %q %v", b, err)
			}
			if h.exists(".persona/link.json") || h.exists(".persona/bin/persona-link") {
				t.Error("what the switch set up is left")
			}
		})
	}
	t.Run("marked on the computer, lost by the record", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{kind: kindLink})
		sum, _ := fileSHA256(h.pkg)
		op := "ls_00112233aabbccdd"
		b, _ := os.ReadFile(h.pkg)
		mustWrite(t, filepath.Join(h.binOf, "persona-link"), string(b), 0o755)
		mustWrite(t, filepath.Join(h.persona, "link.json"), "{}", 0o600)
		mustWrite(t, filepath.Join(h.units, "persona-link.service"), "unit", 0o644)
		mustWrite(t, unit(h), "the computer's own", 0o644)
		mustWrite(t, filepath.Join(h.home, stagedName(op)+".node"), "", 0o644)
		_ = h.store.PutSetting(context.Background(), linkSwitchKey("box"), linkSwitchRecord{Operation: op, MachineID: "box",
			Phase: proto.LinkSwitchPhaseConnecting, Deadline: time.Now(), SSHHost: "box.example", SHA256: sum, BeginIntended: true,
			PairHash: lsTestHash, Install: lsInstallRunning})
		h.d.resumeLinkSwitches(context.Background())
		rec := h.finished()
		if rec.Error != proto.LinkSwitchReasonInterrupted || rec.RollbackUnconfirmed || !rec.NodeUnitExisted {
			t.Fatalf("record %+v", rec)
		}
		if b, err := os.ReadFile(unit(h)); string(b) != "the computer's own" {
			t.Errorf("the computer's node service was removed: %q %v", b, err)
		}
		if h.exists(".persona/link.json") || h.exists(stagedName(op)+".node") {
			t.Error("not cleaned up")
		}
	})
}

// The record says succeeded before the hub is told, and a rollback the hub answers already_succeeded (a record that
// missed the success) ends succeeded with nothing removed.
func TestLinkSwitchNeverRollsBackASuccess(t *testing.T) {
	t.Run("recorded first", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{})
		var stored linkSwitchRecord
		h.hub.onEnd = func(devicewire.LinkSwitchEnd) {
			if stored.Phase == "" { // as it was when the hub was first told
				_, _ = h.store.Setting(context.Background(), linkSwitchKey("box"), &stored)
			}
		}
		h.start()
		if rec := h.finished(); rec.Phase != proto.LinkSwitchPhaseSucceeded {
			t.Fatalf("record %+v", rec)
		}
		if stored.Phase != proto.LinkSwitchPhaseSucceeded {
			t.Errorf("the hub was told before the record said succeeded: %+v", stored)
		}
	})
	t.Run("the hub saw it succeed", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{kind: kindLink})
		h.hub.endCode = devicewire.LinkSwitchCodeSucceeded
		b, _ := os.ReadFile(h.pkg)
		mustWrite(t, filepath.Join(h.binOf, "persona-link"), string(b), 0o755)
		mustWrite(t, filepath.Join(h.persona, "link.json"), "{}", 0o600)
		sum, _ := fileSHA256(h.pkg)
		_ = h.store.PutSetting(context.Background(), linkSwitchKey("box"), linkSwitchRecord{Operation: "ls_1", MachineID: "box",
			Phase: proto.LinkSwitchPhaseConnecting, Deadline: time.Now(), SSHHost: "box.example", SHA256: sum, BeginIntended: true,
			PairHash: lsTestHash, Install: lsInstallPlaced})
		h.d.resumeLinkSwitches(context.Background())
		rec := h.finished()
		if rec.Phase != proto.LinkSwitchPhaseSucceeded || rec.Error != "" {
			t.Fatalf("record %+v", rec)
		}
		if got := h.whats(); !slices.Equal(got, []string{"remove staged"}) {
			t.Errorf("ssh runs %q", got)
		}
		if !h.exists(".persona/link.json") || !h.exists(".persona/bin/persona-link") {
			t.Error("Persona Link was removed")
		}
	})
}

// The installer is bounded inside the install run (`timeout`), so it never runs on past it into the rollback.
func TestLinkSwitchInstallerIsBounded(t *testing.T) {
	if !strings.Contains(lsInstallScript("ls_1", "", 150*time.Second), "timeout 150 ") {
		t.Errorf("the default install is not bounded at 150 s: %s", lsInstallScript("ls_1", "", 150*time.Second))
	}
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no timeout here")
	}
	h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) {
		h.d.switches.times.Install = 4 * time.Second // the installer gets 2 s
		mustWrite(t, filepath.Join(h.home, ".hang"), "", 0o644)
	}})
	h.start()
	rec := h.finished()
	if rec.Error != proto.LinkSwitchReasonInstallFailed || rec.InstallerExit == nil || *rec.InstallerExit != 124 {
		t.Fatalf("the installer was not cut off by its own bound: %+v", rec)
	}
}

// Files an earlier switch left (a staged build, a marker) are removed by the next stage and by an uninstall.
func TestLinkSwitchRemovesStaleStagedFiles(t *testing.T) {
	stale := []string{stagedName("ls_old"), stagedName("ls_old") + ".node"}
	for _, c := range []struct {
		name  string
		setup func(h *lsHarness)
	}{
		{"by the stage", nil},
		{"by the uninstall", func(h *lsHarness) { h.force["stage"] = 255 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{setup: c.setup})
			for _, rel := range stale {
				mustWrite(t, filepath.Join(h.home, rel), "old", 0o644)
			}
			h.start()
			h.finished()
			for _, rel := range stale {
				if h.exists(rel) {
					t.Errorf("%s is left", rel)
				}
			}
		})
	}
}

// A restart that finds a switch whose installer never ran rolls back at once: no link can come.
func TestLinkSwitchResumeWithoutAnInstallRollsBackAtOnce(t *testing.T) {
	for _, install := range []string{"", lsInstallNotPlaced} {
		t.Run(orDefault(install, "none"), func(t *testing.T) {
			h := newLSHarness(t, lsOptions{kind: kindLink})
			_ = h.store.PutSetting(context.Background(), linkSwitchKey("box"), linkSwitchRecord{Operation: "ls_1", MachineID: "box",
				Phase: proto.LinkSwitchPhaseInstalling, Deadline: time.Now().Add(time.Minute), SSHHost: "box.example",
				BeginIntended: true, PairHash: lsTestHash, Install: install})
			begun := time.Now()
			h.d.resumeLinkSwitches(context.Background())
			rec := h.finished()
			if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != proto.LinkSwitchReasonInterrupted || rec.RollbackUnconfirmed {
				t.Fatalf("record %+v", rec)
			}
			if took := time.Since(begun); took > 15*time.Second {
				t.Errorf("waited %s for a link that cannot come", took)
			}
		})
	}
}

// A begin the hub failed without one of its refusals (an uncoded error) may have switched the entry: rolled back with
// end(rolled_back) without a pairing hash, never taken as nothing changed.
func TestLinkSwitchBeginFailingOtherwiseRollsBack(t *testing.T) {
	for _, err := range []error{&rpc.Error{Message: "database is down"}, &rpc.Error{Code: "something_new", Message: "new"}} {
		t.Run(err.(*rpc.Error).Code, func(t *testing.T) {
			h := newLSHarness(t, lsOptions{})
			h.hub.beginErr = err
			h.start()
			rec := h.finished()
			if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != proto.LinkSwitchReasonUnknown || rec.RollbackUnconfirmed {
				t.Fatalf("record %+v", rec)
			}
			if _, ends := h.hub.calls(); len(ends) != 1 || ends[0].PairHash != "" || ends[0].Outcome != devicewire.LinkSwitchRolledBack {
				t.Errorf("ends %+v", ends)
			}
			if got := h.whats(); slices.Contains(got, "install") {
				t.Errorf("ssh runs %q", got)
			}
			if h.hub.kind() != "ssh" || !h.d.sshOnline("box") {
				t.Error("not back on ssh")
			}
		})
	}
}

// A switch that succeeded on a computer with its own node service leaves no .node marker: once the record says
// succeeded and the hub was told, the files the switch staged or marked are removed over ssh. That failing leaves the
// switch succeeded, nothing rolled back.
func TestLinkSwitchSuccessRemovesWhatItStaged(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("removal fails %t", fail), func(t *testing.T) {
			var atRemoval linkSwitchRecord
			var endsAtRemoval int
			h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) {
				mustWrite(t, filepath.Join(h.units, "personad-node.service"), "the computer's own", 0o644)
				h.onRun = func(what string) {
					if what == "remove staged" && atRemoval.Phase == "" { // the first removal
						_, _ = h.store.Setting(context.Background(), linkSwitchKey("box"), &atRemoval)
						_, ends := h.hub.calls()
						endsAtRemoval = len(ends)
					}
				}
				if fail {
					h.force["remove staged"] = 255
				}
			}})
			h.start()
			rec := h.finished()
			eventually(t, 10*time.Second, "the removal ran", func() bool { return slices.Contains(h.whats(), "remove staged") })
			h.close() // the switch's goroutine is done
			if rec.Phase != proto.LinkSwitchPhaseSucceeded || !rec.NodeUnitExisted {
				t.Fatalf("record %+v", rec)
			}
			if got := h.d.linkSwitchRecordOf("box"); got.Phase != proto.LinkSwitchPhaseSucceeded || got.Error != "" {
				t.Errorf("record after the removal %+v", got)
			}
			if atRemoval.Phase != proto.LinkSwitchPhaseSucceeded || endsAtRemoval != 1 {
				t.Errorf("removed before the record said succeeded (%q) or before the hub was told (%d ends)", atRemoval.Phase, endsAtRemoval)
			}
			if _, ends := h.hub.calls(); len(ends) != 1 || ends[0].Outcome != devicewire.LinkSwitchSucceeded {
				t.Errorf("ends %+v", ends)
			}
			marker := h.exists(stagedName(rec.Operation) + ".node")
			if marker == !fail {
				t.Errorf("the .node marker left %t, removal failed %t", marker, fail)
			}
			if b, _ := os.ReadFile(filepath.Join(h.units, "personad-node.service")); string(b) != "the computer's own" || !h.exists(".persona/link.json") {
				t.Error("the success was undone")
			}
		})
	}
}

// A rollback the hub cannot take is not asked for ever, which would keep the switch unfinished and hold the
// computer's node updates: a hub that does not know end (an older build) is asked once, one that never answers
// until EndGiveUp from the first ask, counted across a restart. Either fails as hub_unanswered, the computer left as
// it is (it may still be set to Persona Link), and a node update may start again.
func TestLinkSwitchRollbackHubUnanswered(t *testing.T) {
	left := func(h *lsHarness) {
		h.t.Helper()
		if slices.Contains(h.whats(), "uninstall") || !h.exists(".persona/link.json") || !h.exists(".persona/bin/persona-link") {
			h.t.Errorf("the computer was touched: %q", h.whats())
		}
	}
	unanswered := func(h *lsHarness, rec linkSwitchRecord) {
		h.t.Helper()
		if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != proto.LinkSwitchReasonHubUnanswered || !rec.RollbackUnconfirmed {
			h.t.Fatalf("record %+v", rec)
		}
		if h.d.linkSwitchUnfinished("box") {
			h.t.Error("still unfinished")
		}
		if err := h.d.cfgMachine("box").startDeploy(); errors.Is(err, errLinkSwitchDeploy) {
			h.t.Error("a node update is still held")
		}
	}
	t.Run("the hub does not know end", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644) }})
		h.hub.endCode = "unknown_method"
		h.start()
		unanswered(h, h.finished())
		if _, ends := h.hub.calls(); len(ends) != 1 {
			t.Errorf("end asked %d times", len(ends))
		}
		left(h)
	})
	t.Run("the hub never answers", func(t *testing.T) {
		h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) {
			mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644)
			h.d.switches.times.EndGiveUp = 700 * time.Millisecond
		}})
		h.hub.endHeld.Store(true)
		h.start()
		rec := h.finished()
		unanswered(h, rec)
		if _, ends := h.hub.calls(); len(ends) < 3 {
			t.Errorf("end asked %d times", len(ends))
		}
		if st := h.stored(); st.EndSince.IsZero() || st.Phase != proto.LinkSwitchPhaseFailed {
			t.Errorf("stored %+v", st)
		}
		left(h)
	})
	t.Run("a restart keeps when it began asking", func(t *testing.T) {
		const giveUp = 3 * time.Second
		h := newLSHarness(t, lsOptions{kind: kindLink, setup: func(h *lsHarness) {
			h.installed()
			h.d.switches.times.EndGiveUp = giveUp
		}})
		h.hub.endHeld.Store(true)
		since := time.Now().Add(-giveUp + 300*time.Millisecond).UTC().Round(time.Millisecond)
		_ = h.store.PutSetting(context.Background(), linkSwitchKey("box"), linkSwitchRecord{Operation: "ls_1", MachineID: "box",
			Phase: proto.LinkSwitchPhaseRollingBack, Error: proto.LinkSwitchReasonInstallFailed, Deadline: time.Now(),
			SSHHost: "box.example", BeginIntended: true, PairHash: lsTestHash, Install: lsInstallPlaced, EndSince: since})
		begun := time.Now()
		h.d.resumeLinkSwitches(context.Background())
		rec := h.finished()
		if took := time.Since(begun); took > giveUp/2 {
			t.Errorf("gave up after %s: the bound began again at the restart", took)
		}
		unanswered(h, rec)
		if !rec.EndSince.Equal(since) {
			t.Errorf("the first ask moved: %s, was %s", rec.EndSince, since)
		}
		left(h)
	})
}

// A rollback the hub took for a computer that is another manager's now (rolled_back_gone): what this switch set up is
// removed as after {}, and there is no wait for the computer over ssh, which is that manager's to connect.
func TestLinkSwitchRollbackOfAComputerGone(t *testing.T) {
	h := newLSHarness(t, lsOptions{setup: func(h *lsHarness) { mustWrite(t, filepath.Join(h.home, ".fail-install"), "", 0o644) }})
	h.hub.endCode = devicewire.LinkSwitchCodeRolledBackGone
	h.start()
	rec := h.finished()
	if rec.Phase != proto.LinkSwitchPhaseFailed || rec.Error != proto.LinkSwitchReasonInstallFailed || rec.RollbackUnconfirmed ||
		rec.HubEnded != lsEndedRolledBackGone {
		t.Fatalf("record %+v", rec)
	}
	if _, ends := h.hub.calls(); len(ends) != 1 {
		t.Errorf("end asked %d times", len(ends))
	}
	for _, rel := range []string{".persona/bin/persona-link", ".persona/link.json", ".config/systemd/user/persona-link.service",
		".config/systemd/user/personad-node.service"} {
		if h.exists(rel) {
			t.Errorf("%s is left", rel)
		}
	}
}
