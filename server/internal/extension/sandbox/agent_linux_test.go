//go:build linux

package sandbox

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// preflightLinuxAgentForReal uses the same full-profile probe as a real start before a test invokes bwrap itself.
// Production stays fail closed when the alias cannot be mounted; only that specific missing capability skips the test.
func preflightLinuxAgentForReal(t *testing.T, plan AgentPlan) {
	t.Helper()
	_, _, st := WrapAgent(plan, []string{"/bin/true"}, nil)
	requireLinuxAgentForReal(t, st)
}

func requireLinuxAgentForReal(t *testing.T, st AgentStatus) {
	t.Helper()
	if st.State == AgentActive {
		return
	}
	if linuxAgentAliasOpenTreeUnavailable(st) {
		t.Skip("no Linux agent alias mount here: " + st.Why)
	}
	t.Fatalf("WrapAgent did not wrap: %+v", st)
}

func linuxAgentAliasOpenTreeUnavailable(st AgentStatus) bool {
	if st.State != AgentUnavailable || st.Reason == nil || st.Reason.Key != "extension-status.sandbox_bwrap_start" {
		return false
	}
	err, _ := st.Reason.Args["err"].(string)
	return strings.HasPrefix(err, "agent sandbox alias ") && strings.HasSuffix(err, " open_tree: operation not permitted")
}

func TestLinuxAgentAliasOpenTreeUnavailable(t *testing.T) {
	const (
		startReason = "extension-status.sandbox_bwrap_start"
		aliasEPERM  = `agent sandbox alias "/tmp/pas-1/.state-link" open_tree: operation not permitted`
	)
	status := func(state, key string, err any) AgentStatus {
		return agentStatusReason(state, key, i18n.Args{"err": err})
	}
	tests := []struct {
		name string
		st   AgentStatus
		want bool
	}{
		{name: "alias open_tree EPERM", st: status(AgentUnavailable, startReason, aliasEPERM), want: true},
		{name: "active", st: status(AgentActive, startReason, aliasEPERM)},
		{name: "different reason", st: status(AgentUnavailable, "extension-sandbox.agent_profile_refused", aliasEPERM)},
		{name: "move_mount EPERM", st: status(AgentUnavailable, startReason, `agent sandbox alias "/tmp/pas-1/.state-link" move_mount: operation not permitted`)},
		{name: "different open_tree error", st: status(AgentUnavailable, startReason, `agent sandbox alias "/tmp/pas-1/.state-link" open_tree: permission denied`)},
		{name: "unrelated EPERM", st: status(AgentUnavailable, startReason, "bwrap: operation not permitted")},
		{name: "extra diagnostic", st: status(AgentUnavailable, startReason, aliasEPERM+"\nother failure")},
		{name: "non-string error", st: status(AgentUnavailable, startReason, 1)},
		{name: "missing structured reason", st: AgentStatus{State: AgentUnavailable, Why: aliasEPERM}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linuxAgentAliasOpenTreeUnavailable(tt.st); got != tt.want {
				t.Fatalf("linuxAgentAliasOpenTreeUnavailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLinuxAgentSandboxForReal runs the Darwin must-fail / must-work contract under real bubblewrap 0.8.x. The data
// directory and its explicit alias are masked views; the control uses the same user/mount namespace with no views, so
// every refusal is caused by the profile rather than by an absent tool or an unwritable fixture.
func TestLinuxAgentSandboxForReal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	sup := AgentProbe()
	if st := AgentSupport(sup); st.State != AgentActive {
		t.Skip("no Linux agent sandbox here: " + st.Why)
	}
	requireAliasMount(t, sup.Bwrap)
	t.Logf("agent probe: %+v", sup)
	f := newAgentFixture(t)
	D, alias := f.dir, f.alias
	self, _ := os.Executable()
	self = realPath(self)
	bin := filepath.Join(D, "bin", "personad")
	if b, err := os.ReadFile(self); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(bin, b, 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(f.home, "link-to-key")
	if err := os.Symlink(filepath.Join(D, "secret.key"), elsewhere); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"hub.json.d/inside":            "{}", // a whole directory whose top-level name starts with a DataSecrets entry
		"extensions/staging/old/token": "t",
		"node-plugins/old/token":       "t",
		"notes":                        "home",
	} {
		path := filepath.Join(D, rel)
		if rel == "notes" {
			path = filepath.Join(f.home, rel)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.MkdirAll(filepath.Join(f.home, "public-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, "public-dir", "note"), []byte("public-dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.home, "public-dir", "old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, "public-dir", "old", "token"), []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"plugins":                               "../public-dir",
		"innocent-key":                          "secret.key",
		"innocent-settings":                     "launcher-settings/other-claude-settings.json",
		"our-settings-link":                     "launcher-settings/claude-ours.json",
		"public-link":                           "../notes",
		"public-dir-link":                       "../public-dir",
		filepath.Join("extensions", "innocent"): "../managers",
		filepath.Join("launcher-settings", "link-settings.json"): "other-claude-settings.json",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(D, name)); err != nil {
			t.Fatal(err)
		}
	}

	sock := func(path string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				conn.Close()
			}
		}()
	}
	nodeSock := filepath.Join(D, "node.sock")
	proxySock := filepath.Join(D, "extensions", "run", "notes", "p.sock")
	edgeSock := filepath.Join(D, "run", "e.sock")
	sock(nodeSock)
	sock(proxySock)
	sock(edgeSock)

	work := filepath.Join(f.home, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := f.plan()
	plan.Cwd = work
	plan.LauncherSettings = append(plan.LauncherSettings, filepath.Join(D, "launcher-settings", "link-settings.json"))
	preflightLinuxAgentForReal(t, plan)
	prefix, err := LinuxAgent(sup.Bwrap, plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bwrap profile: %s", strings.Join(prefix, " "))
	control := []string{sup.Bwrap, "--die-with-parent", "--unshare-user", "--bind", "/", "/", "--dev-bind", "/dev", "/dev", "--chdir", work}
	run := func(prefix []string, script string) (string, error) {
		command, err := linuxAgentCommand(prefix, []string{"/bin/sh", "-c", script})
		if err != nil {
			return "", err
		}
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "HOME="+f.home)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	q := shellQuote
	mustFail := map[string]string{
		"cat secret.key":                     "cat " + q(D+"/secret.key"),
		"cat secret.key through .state-link": "cat " + q(alias+"/secret.key"),
		"cat secret.key through a link":      "cat " + q(elsewhere),
		"cat secret through a view link":     "cat " + q(D+"/innocent-key"),
		"cat managers through a nested link": "cat " + q(D+"/extensions/innocent/m1/manager.json"),
		"cat settings through a view link":   "cat " + q(D+"/innocent-settings"),
		"cat an allowed settings symlink":    "cat " + q(D+"/launcher-settings/link-settings.json"),
		"cat secret.key.bak":                 "cat " + q(D+"/secret.key.bak"),
		"cat handles.key":                    "cat " + q(D+"/handles.key"),
		"cat launch.key":                     "cat " + q(D+"/launch.key"),
		"cat hub.json":                       "cat " + q(D+"/hub.json"),
		"cat link.json":                      "cat " + q(D+"/link.json"),
		"cat hub.json.bak":                   "cat " + q(D+"/hub.json.bak"),
		"cat .hub.json.12345":                "cat " + q(D+"/.hub.json.12345"),
		"cat hub.json.tmp-12345":             "cat " + q(D+"/hub.json.tmp-12345"),
		"cat hub.json.d/inside":              "cat " + q(D+"/hub.json.d/inside"),
		"cat link-tls/id.key":                "cat " + q(D+"/link-tls/id.key"),
		"cat tls/hub.key":                    "cat " + q(D+"/tls/hub.key"),
		"cat tls/.tls-1":                     "cat " + q(D+"/tls/.tls-1"),
		"cat managers/m1/manager.json":       "cat " + q(D+"/managers/m1/manager.json"),
		"cat extensions/data":                "cat " + q(D+"/extensions/data/notes/token"),
		"cat extensions/staging":             "cat " + q(D+"/extensions/staging/old/token"),
		"cat plugins":                        "cat " + q(D+"/plugins/old/token"),
		"cat public through a plugins link":  "cat " + q(D+"/plugins/note"),
		"cat node-plugins":                   "cat " + q(D+"/node-plugins/old/token"),
		"cat relay-tunnel.token":             "cat " + q(D+"/relay-tunnel.token"),
		"cat docker/db-password":             "cat " + q(D+"/docker/db-password"),
		"cat edge.token":                     "cat " + q(D+"/edge.token"),
		"cat edge.secret":                    "cat " + q(D+"/edge.secret"),
		"cat edge.password":                  "cat " + q(D+"/edge.password"),
		"cat another launcher's settings":    "cat " + q(f.theirs),
		"write our launcher settings":        "echo x >> " + q(f.ours),
		"cp secret.key":                      "cp " + q(D+"/secret.key") + " " + q(work+"/k"),
		"create a top-level secret backup":   "echo new > " + q(D+"/hub.json.new") + " && cat " + q(D+"/hub.json.new"),
		"mv the data directory":              "mv " + q(D) + " " + q(D+"2") + " && mv " + q(D+"2") + " " + q(D),
		"mv the folder above it":             "mv " + q(f.home) + " " + q(f.home+"2") + " && mv " + q(f.home+"2") + " " + q(f.home),
		"replace .state-link":                "rm " + q(alias) + " && ln -s .persona " + q(alias),
		"rm the switch":                      "rm " + q(D+"/"+AgentOnFile) + " && touch " + q(D+"/"+AgentOnFile),
		"dial an extension's proxy":          q(self) + " agent-dial " + q(proxySock),
		"dial the hub's edge socket":         q(self) + " agent-dial " + q(edgeSock),
		"mv extensions/ and read its data":   "mv " + q(D+"/extensions") + " " + q(D+"/ext2") + " && cat " + q(D+"/ext2/data/notes/token") + " && mv " + q(D+"/ext2") + " " + q(D+"/extensions"),
	}
	if _, err := exec.LookPath("python3"); err == nil {
		mustFail["python reads secret.key"] = "python3 -c " + q("open('"+D+"/secret.key').read()")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(server.Close)
	mustWork := map[string]string{
		"cat the switch":            "cat " + q(D+"/"+AgentOnFile),
		"cat mcp.json":              "cat " + q(D+"/mcp.json"),
		"cat through .state-link":   "cat " + q(alias+"/claude-settings.json"),
		"cat our launcher settings": "cat " + q(f.ours),
		"cat our settings via link": "cat " + q(D+"/our-settings-link"),
		"cat a public file symlink": "cat " + q(D+"/public-link"),
		"cat a public dir symlink":  "cat " + q(D+"/public-dir-link/note"),
		"cat data/talk.key":         "cat " + q(D+"/data/talk.key"),
		"cat data/proj/link.json":   "cat " + q(D+"/data/proj/link.json"),
		"cat data/proj/edge.token":  "cat " + q(D+"/data/proj/edge.token"),
		"write assistant/":          "echo x > " + q(D+"/assistant/x") + " && cat " + q(D+"/assistant/x"),
		"write knowledge base":      "echo x > " + q(D+"/data/new") + " && cat " + q(D+"/data/new"),
		"read and write the home":   "cat " + q(f.home+"/notes") + " && echo x > " + q(f.home+"/x") + " && mkdir -p " + q(f.home+"/d") + " && mv " + q(f.home+"/x") + " " + q(f.home+"/y"),
		"write the working dir":     "echo cwd > cwd-file && cat cwd-file",
		"dial node.sock":            q(self) + " agent-dial " + q(nodeSock),
		"personad --version":        q(self) + " --version",
		"<D>/bin/personad":          q(bin) + " --version",
		"git":                       "git init -q . && git -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m x && git log --oneline",
	}
	if _, err := exec.LookPath("curl"); err == nil {
		mustWork["network"] = "curl --noproxy '*' -sS -o /dev/null -m 5 " + q(server.URL)
	} else {
		t.Log("skip network: curl is not installed")
	}
	if _, err := exec.LookPath("ssh"); err == nil {
		mustWork["ssh"] = "ssh -V"
	}
	if _, err := exec.LookPath("cc"); err == nil {
		mustWork["compiler"] = "printf 'int main(void){return 0;}\\n' > a.c && cc a.c -o a.out && ./a.out"
	}

	t.Logf("running %d must-fail and %d must-work commands, then the unmasked controls", len(mustFail), len(mustWork))
	for name, script := range mustFail {
		if out, err := run(prefix, script); err == nil {
			t.Errorf("%s: worked in the sandbox: %s", name, out)
		}
	}
	for name, script := range mustWork {
		if out, err := run(prefix, script); err != nil {
			t.Errorf("%s: failed in the sandbox: %v %s", name, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(f.home, "y")); err != nil {
		t.Errorf("home write did not reach the host: %v", err)
	}
	if _, err := os.Stat(filepath.Join(D, "data", "new")); err != nil {
		t.Errorf("knowledge-base write did not reach the host: %v", err)
	}

	// Control: every refusal above must succeed with the same namespaces and no data-directory views. The destructive
	// move/remove commands restore their fixture as part of the command, exactly as in the Darwin test.
	for name, script := range mustFail {
		if out, err := run(control, script); err != nil {
			t.Errorf("control %s: failed with no views: %v %s", name, err, out)
		}
	}
	if b, err := os.ReadFile(filepath.Join(work, "k")); err != nil || string(b) != "k" {
		t.Errorf("control cp: %q, %v", b, err)
	}
	// The control created hub.json.new only after the profile was generated. It is still absent from a fresh mount of
	// that profile: coverage comes from the name rules and the read-only view root, not a one-time list of old files.
	if out, err := run(prefix, "cat "+q(D+"/hub.json.new")); err == nil {
		t.Errorf("secret created after profile generation was readable: %s", out)
	}

	// WrapAgent's real preflight leaves bwrap as the direct supervisor of the agent in the same host pid namespace:
	// the node-started pid remains the agent's PPID, and no hidden pid translation or extra intermediary is involved.
	wrapped, env, st := WrapAgent(plan, []string{"/bin/sh", "-c", `printf "%s %s" "$$" "$PPID"`}, withoutKey(os.Environ(), AgentEnv))
	requireLinuxAgentForReal(t, st)
	cmd := exec.Command(wrapped[0], wrapped[1:]...)
	cmd.Dir = work
	cmd.Env = env
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("pid command: %v: %s", err, output.String())
	}
	fields := strings.Fields(output.String())
	if len(fields) != 2 {
		t.Fatalf("pid command returned %q", output.String())
	}
	agentPid, parseErr := strconv.Atoi(fields[0])
	if parseErr != nil || agentPid <= 0 || fields[1] != strconv.Itoa(pid) {
		t.Errorf("agent is not a direct host-pid-namespace child of node-started bwrap pid %d: %q", pid, output.String())
	}

	// Special read-only entries never follow a symlink to a different file. The regular switch was checked above;
	// replace it only after all other cases and verify that its linked form is closed instead.
	switchPath := filepath.Join(D, AgentOnFile)
	if err := os.Remove(switchPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("mcp.json", switchPath); err != nil {
		t.Fatal(err)
	}
	linkedPrefix, err := LinuxAgent(sup.Bwrap, plan)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run(linkedPrefix, "cat "+q(switchPath)); err == nil {
		t.Errorf("a symlinked switch file was imported into the view: %s", out)
	}
	if out, err := run(control, "cat "+q(switchPath)); err != nil {
		t.Errorf("control symlinked switch: %v %s", err, out)
	}

	// A symlink at a partially closed directory is closed as a whole: neither its public entries nor the entries that
	// would have been closed in a real extensions/ directory are imported. Controls prove both targets are readable.
	external := filepath.Join(f.home, "external-extensions")
	if err := os.MkdirAll(filepath.Join(external, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{"public-note": "public", "data/token": "closed"} {
		if err := os.WriteFile(filepath.Join(external, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(filepath.Join(D, "extensions"), filepath.Join(f.home, "extensions-real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../external-extensions", filepath.Join(D, "extensions")); err != nil {
		t.Fatal(err)
	}
	extensionsPrefix, err := LinuxAgent(sup.Bwrap, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"public-note", "data/token"} {
		path := filepath.Join(D, "extensions", rel)
		if out, err := run(extensionsPrefix, "cat "+q(path)); err == nil {
			t.Errorf("extensions directory symlink imported %s: %s", rel, out)
		}
		if out, err := run(control, "cat "+q(path)); err != nil {
			t.Errorf("control extensions directory symlink %s: %v %s", rel, err, out)
		}
	}
}

// A name found through a relative PATH entry runs exactly as it did when bwrap performed the exec itself.
func TestLinuxAgentSandboxRelativePathForReal(t *testing.T) {
	sup := AgentProbe()
	if st := AgentSupport(sup); st.State != AgentActive {
		t.Skip("no Linux agent sandbox here: " + st.Why)
	}
	requireAliasMount(t, sup.Bwrap)
	f := newAgentFixture(t)
	work := filepath.Join(f.home, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := f.plan()
	plan.Cwd = work
	if err := os.WriteFile(filepath.Join(work, "relative-agent"), []byte("#!/bin/sh\nprintf 'relative-agent-ok\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := withoutKey(withoutKey(os.Environ(), AgentEnv), "PATH")
	env = append(env, "PATH=."+string(os.PathListSeparator)+os.Getenv("PATH"))
	wrapped, env, st := WrapAgent(plan, []string{"relative-agent"}, env)
	requireLinuxAgentForReal(t, st)
	cmd := exec.Command(wrapped[0], wrapped[1:]...)
	cmd.Dir = work
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "relative-agent-ok" {
		t.Fatalf("relative PATH agent: %v %s", err, out)
	}
}

// Linux has no probe/profile success cache: a missing bwrap can be installed, a removed bwrap cannot leave a stale
// active result, and every distinct cwd is preflighted rather than adding a permanent full-profile key.
func TestLinuxAgentPreflightAlwaysRuns(t *testing.T) {
	f := newAgentFixture(t)
	dir := t.TempDir()
	fake := filepath.Join(dir, "bwrap")
	count := filepath.Join(dir, "calls")
	writeFake := func() {
		script := "#!/bin/sh\nprintf 'call\\n' >> " + shellQuote(count) + "\nexit 0\n"
		if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	calls := func() int {
		b, err := os.ReadFile(count)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return bytes.Count(b, []byte("\n"))
	}
	plan := f.plan()
	plan.Probe = func() Support { return Support{OS: "linux", Bwrap: fake} }
	argv := []string{"/bin/echo", "ok"}
	if _, _, st := WrapAgent(plan, argv, nil); st.State != AgentUnavailable {
		t.Fatalf("missing fake bwrap: %+v", st)
	}
	writeFake()
	if _, _, st := WrapAgent(plan, argv, nil); st.State != AgentActive {
		t.Fatalf("installed fake bwrap: %+v", st)
	}
	if err := os.Remove(fake); err != nil {
		t.Fatal(err)
	}
	if _, _, st := WrapAgent(plan, argv, nil); st.State != AgentUnavailable {
		t.Fatalf("removed fake bwrap retained a stale success: %+v", st)
	}
	writeFake()
	if _, _, st := WrapAgent(plan, argv, nil); st.State != AgentActive {
		t.Fatalf("reinstalled fake bwrap: %+v", st)
	}
	if got := calls(); got != 2 {
		t.Fatalf("successful preflights before cwd sweep: %d, want 2", got)
	}
	for i := 0; i < 120; i++ {
		cwd := filepath.Join(f.home, "cwd-"+strconv.Itoa(i))
		if err := os.Mkdir(cwd, 0o700); err != nil {
			t.Fatal(err)
		}
		plan.Cwd = cwd
		if _, _, st := WrapAgent(plan, argv, nil); st.State != AgentActive {
			t.Fatalf("cwd %d: %+v", i, st)
		}
	}
	if got := calls(); got != 122 {
		t.Fatalf("120 cwd changes were cached instead of preflighted: total calls %d, want 122", got)
	}
}

// The first Linux slice intentionally has a static view. Once bwrap is ready, replacing node.sock on the host leaves
// the sandbox connected to the old inode and a new top-level public file absent. A file created inside an already-bound
// directory remains visible. This pins why a node restart requires restarting its sandboxed agents.
func TestLinuxAgentSandboxSnapshotForReal(t *testing.T) {
	sup := AgentProbe()
	if st := AgentSupport(sup); st.State != AgentActive {
		t.Skip("no Linux agent sandbox here: " + st.Why)
	}
	requireAliasMount(t, sup.Bwrap)
	f := newAgentFixture(t)
	work := filepath.Join(f.home, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := f.plan()
	plan.Cwd = work
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self := realPath(exe)
	nodeSock := filepath.Join(f.dir, "node-restart.sock")
	publicFile := filepath.Join(f.dir, "public-after")
	nestedFile := filepath.Join(f.dir, "data", "public-after")
	listen := func(path, generation string) net.Listener {
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = conn.Write([]byte(generation))
				_ = conn.Close()
			}
		}()
		return ln
	}
	_ = listen(nodeSock, "old")
	prefix, err := LinuxAgent(sup.Bwrap, plan)
	if err != nil {
		t.Fatal(err)
	}
	preflightLinuxAgentForReal(t, plan)

	script := "set -e; " + shellQuote(self) + " agent-socket-read " + shellQuote(nodeSock) + "; printf '\\nready\\n'; " +
		"IFS= read -r _; " + shellQuote(self) + " agent-socket-read " + shellQuote(nodeSock) + "; printf '\\n'; " +
		"if [ -e " + shellQuote(publicFile) + " ]; then printf 'top=present\\n'; else printf 'top=missing\\n'; fi; " +
		"if [ -e " + shellQuote(nestedFile) + " ]; then printf 'nested=present\\n'; else printf 'nested=missing\\n'; fi"
	command, err := linuxAgentCommand(prefix, []string{"/bin/sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = work
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			lines <- line
		}
	}()
	next := func(want string) string {
		t.Helper()
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("sandbox output ended before %q: %s", want, stderr.String())
			}
			got := strings.TrimSpace(line)
			if got != want {
				t.Fatalf("sandbox output %q, want %q: %s", got, want, stderr.String())
			}
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("waiting for %q: %s", want, stderr.String())
		}
		return ""
	}
	next("old")
	next("ready")
	retired := nodeSock + ".retired"
	if err := os.Rename(nodeSock, retired); err != nil {
		t.Fatal(err)
	}
	_ = listen(nodeSock, "new")
	if err := os.WriteFile(publicFile, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nestedFile, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readAgentSocket(nodeSock); err != nil || got != "new" {
		t.Fatalf("host replacement socket: %q, %v", got, err)
	}
	if _, err := stdin.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	next("old") // the file bind still names the old inode, not the host's new listener
	next("top=missing")
	next("nested=present")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("snapshot shell: %v: %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("snapshot shell did not exit")
	}
	t.Log("after host replacement: sandbox socket=old, top-level file=missing, file in bound data/=present; host socket=new")
}

// Verify two Linux-specific escape hypotheses on the real host: another same-UID process's /proc root and a nested
// user/mount namespace attempting to umount the data-directory mask. Each has an unmasked control that really reads
// the fixture secret, so a disabled tool or an invalid path cannot produce a false pass.
func TestLinuxAgentSandboxNamespaceBoundariesForReal(t *testing.T) {
	sup := AgentProbe()
	if st := AgentSupport(sup); st.State != AgentActive {
		t.Skip("no Linux agent sandbox here: " + st.Why)
	}
	requireAliasMount(t, sup.Bwrap)
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare is not installed")
	}
	if _, err := exec.LookPath("umount"); err != nil {
		t.Skip("umount is not installed")
	}
	f := newAgentFixture(t)
	work := filepath.Join(f.home, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := f.plan()
	plan.Cwd = work
	prefix, err := LinuxAgent(sup.Bwrap, plan)
	if err != nil {
		t.Fatal(err)
	}
	preflightLinuxAgentForReal(t, plan)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	bwrap := func(script string) (string, error) {
		command, err := linuxAgentCommand(prefix, []string{"/bin/sh", "-c", script})
		if err != nil {
			return "", err
		}
		return run(command...)
	}

	t.Run("proc_root", func(t *testing.T) {
		procSecret := filepath.Join("/proc", strconv.Itoa(os.Getpid()), "root"+f.dir, "secret.key")
		if out, err := run("/bin/cat", procSecret); err != nil {
			t.Fatalf("host /proc-root control: %v %s", err, out)
		}
		if out, err := bwrap("cat " + shellQuote(procSecret)); err == nil {
			t.Errorf("secret was readable through /proc/<pid>/root: %s", out)
		} else {
			t.Logf("/proc/<same-UID-pid>/root secret read refused: %s", out)
		}
	})

	t.Run("nested_namespace", func(t *testing.T) {
		if out, err := exec.Command("unshare", "-rm", "/bin/true").CombinedOutput(); err != nil {
			t.Skipf("the host cannot create the nested control namespace: %v: %s", err, strings.TrimSpace(string(out)))
		}
		nestedScript := `printf 'nested-userns=ready\n'; if umount "$1"; then printf 'umount=success\n'; else printf 'umount=failed\n'; fi; if cat "$1/secret.key"; then printf '\nsecret=readable\n'; exit 0; else printf 'secret=closed\n'; exit 1; fi`
		nestedArgs := []string{"unshare", "-rm", "/bin/sh", "-c", nestedScript, "probe", f.dir}
		if out, err := run(nestedArgs...); err != nil || !strings.Contains(out, "secret=readable") {
			t.Fatalf("nested unshare control: %v %s", err, out)
		}
		command, err := linuxAgentCommand(prefix, nestedArgs)
		if err != nil {
			t.Fatal(err)
		}
		out, err := run(command...)
		if err == nil || strings.Contains(out, "secret=readable") {
			t.Errorf("nested unshare/umount exposed the secret: %v %s", err, out)
		}
		if !strings.Contains(out, "nested-userns=ready") || !strings.Contains(out, "secret=closed") {
			t.Fatalf("nested namespace did not exercise the masked path: %v %s", err, out)
		}
		t.Logf("nested unshare/umount left the secret closed: %s", out)
	})
}

// requireAliasMount skips when this host's bwrap leaves the sandboxed process without CAP_SYS_ADMIN in its user
// namespace (some vendored builds drop every capability). The data-directory alias helper's open_tree needs it, so
// there every alias start fails closed (WrapAgent reports the sandbox unavailable) and the contract cannot run. It
// reads the capability bit only and never runs the helper, so a broken alias mount still fails on a capable host.
func requireAliasMount(t *testing.T, bwrap string) {
	t.Helper()
	out, err := exec.Command(bwrap, "--die-with-parent", "--unshare-user", "--bind", "/", "/", "--dev-bind", "/dev", "/dev",
		"--", "cat", "/proc/self/status").CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap capability probe: %v: %s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		hex, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		hex = strings.TrimSpace(hex)
		caps, err := strconv.ParseUint(hex, 16, 64)
		if err != nil {
			t.Fatalf("bwrap capability probe: CapEff %q: %v", hex, err)
		}
		const capSysAdmin = 21
		if caps&(1<<capSysAdmin) == 0 {
			t.Skipf("%s gives the sandbox no CAP_SYS_ADMIN (CapEff %s): the alias helper's open_tree cannot run, and production reports the agent sandbox unavailable here", bwrap, hex)
		}
		return
	}
	t.Fatalf("bwrap capability probe: no CapEff in %s", out)
}
