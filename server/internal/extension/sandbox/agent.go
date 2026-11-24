package sandbox

// The agent sandbox (docs/server/agent-sandbox.md, #5357): the coding agents Persona starts on a machine whose data
// directory holds keys (the assistant's Claude, a managed launcher, a Codex started from the phone) run under
// sandbox-exec on macOS or bubblewrap on Linux with everything allowed but the data directory's secrets. Its shape is
// the opposite of the extension sandbox: the network, the home directory, git, ssh and the compilers stay as they are,
// and only the key files, the credential files and the sockets that hand out the hub's power are closed.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
)

// AgentEnv is set in the environment of a process started in the agent sandbox: a launcher it runs (`personad
// launch`) is in it already and must not wrap itself again (nested sandbox-exec and user namespaces can fail).
const AgentEnv = "PERSONA_AGENT_SANDBOXED"

// AgentExecCommand is the node binary's internal entry point for finishing a Linux agent sandbox before exec.
const AgentExecCommand = "agent-sandbox-exec"

// AgentOnFile in the data directory turns the agent sandbox on for this machine (docs/server/agent-sandbox.md "Turning it
// on"). An agent in the sandbox cannot remove it.
const AgentOnFile = "agent-sandbox.on"

// What an agent start found (AgentStatus.State).
const (
	AgentOff         = "off"         // the switch is off
	AgentActive      = "active"      // wrapped (or already inside the sandbox)
	AgentSkipped     = "skipped"     // on, but this start is not wrapped: Why says why
	AgentUnsupported = "unsupported" // this system has no agent sandbox (not macOS or Linux)
	AgentUnavailable = "unavailable" // sandbox-exec or bwrap is missing or will not take the rules
)

// AgentStatus is what happened to one agent start. Reason is additive metadata for newly persisted records; old
// readers ignore it, and records written before it retain their original Why bytes when displayed in any language.
type AgentStatus struct {
	State  string        `json:"state"`
	Why    string        `json:"why,omitempty"`
	Reason *StatusReason `json:"reason,omitempty"`
}

// WhyIn renders a Persona-owned reason in lang. Legacy and external diagnostics have no metadata and stay verbatim.
func (s AgentStatus) WhyIn(lang i18n.Lang) string { return statusWhy(lang, s.Why, s.Reason) }

func agentStatusReason(state, key string, args i18n.Args) AgentStatus {
	reason := NewStatusReason(key, args)
	return AgentStatus{State: state, Why: reason.Error(), Reason: reason}
}

// AgentPlan is one agent start in the sandbox.
type AgentPlan struct {
	// DataDirs lists the configured data directory and canonical default, including explicit custom symlink paths:
	// each is resolved to its real path and written once; one that is not there is left out.
	DataDirs []string
	Home     string
	// Cwd is where the agent runs.
	Cwd string
	// LauncherSettings are the files under launcher-settings/ this start passes to its agent (--settings): readable,
	// the rest of the folder not.
	LauncherSettings []string
	// Inner says why the agent's own sandbox would clash with this one ("" when it has none): nested sandboxes can
	// fail, so such a start is not wrapped. InnerReason is Persona's structured form; external callers may keep Inner
	// as an opaque string. When both are set, InnerReason supplies both the canonical Why and its localized rendering.
	Inner       string
	InnerReason *StatusReason
	// Probe is what this machine can do (nil: AgentProbe); Check tries a profile (nil: the platform's sandbox runs
	// true under it). On Linux the profile is an opaque NUL-separated bwrap command prefix; callers must not parse it.
	Probe func() Support
	Check func(profile string) error
}

// AgentProbe is what this machine can do for the agent sandbox: Probe on macOS, a real default-allow bwrap command on
// Linux; elsewhere only the system.
func AgentProbe() Support {
	switch runtime.GOOS {
	case "darwin":
		return Probe()
	case "linux":
		return linuxAgentProbe()
	default:
		return Support{OS: runtime.GOOS}
	}
}

// AgentSupport says whether this machine can run agents in the sandbox: AgentActive when it can.
func AgentSupport(sup Support) AgentStatus {
	switch sup.OS {
	case "darwin":
		if !sup.SandboxExec {
			return AgentStatus{State: AgentUnavailable, Why: sup.Why, Reason: sup.Reason}
		}
	case "linux":
		if sup.Bwrap == "" {
			return AgentStatus{State: AgentUnavailable, Why: sup.Why, Reason: sup.Reason}
		}
	default:
		return agentStatusReason(AgentUnsupported, "extension-sandbox.agent_unsupported", i18n.Args{"os": sup.OS})
	}
	return AgentStatus{State: AgentActive}
}

// RunAgentExec finishes the Linux alias mounts and execs the command after "--". Other platforms return an error.
func RunAgentExec(args []string) error { return runAgentExec(args) }

// WrapAgent runs argv (argv[0] the program, a path or a name on PATH) in the agent sandbox when it can: the new
// command line starts with sandbox-exec or bwrap, and env gains AgentEnv. When it cannot, argv and env come back as
// they are and the status says why, so the agent starts all the same. Neither platform creates a pid namespace: macOS
// execs the agent at the same pid, while Linux's bwrap supervisor remains the agent's direct parent in the host pid
// namespace, so both pids stay visible to the node.
func WrapAgent(plan AgentPlan, argv, env []string) ([]string, []string, AgentStatus) {
	if envValue(env, AgentEnv) == "1" {
		return argv, env, agentStatusReason(AgentActive, "extension-sandbox.agent_nested", nil)
	}
	if len(argv) == 0 {
		return argv, env, agentStatusReason(AgentSkipped, "extension-sandbox.agent_no_command", nil)
	}
	probe := plan.Probe
	if probe == nil {
		probe = AgentProbe
	}
	sup := probe()
	if st := AgentSupport(sup); st.State != AgentActive {
		return argv, env, st
	}
	if plan.InnerReason != nil {
		return argv, env, AgentStatus{State: AgentSkipped, Why: plan.InnerReason.Error(), Reason: plan.InnerReason}
	}
	if plan.Inner != "" {
		return argv, env, AgentStatus{State: AgentSkipped, Why: plan.Inner}
	}

	var profile string
	var prefix []string
	var wrapped []string
	var err error
	check := plan.Check
	if sup.OS == "linux" {
		prefix, err = LinuxAgent(sup.Bwrap, plan)
		if err == nil {
			wrapped, err = linuxAgentCommand(prefix, argv)
		}
		profile = strings.Join(prefix, "\x00")
		if check == nil {
			check = checkLinuxAgentProfile
		}
	} else {
		profile, err = DarwinAgent(plan)
		prefix = []string{"/usr/bin/sandbox-exec", "-p", profile}
		wrapped = append(prefix, argv...)
		if check == nil {
			check = checkAgentProfile
		}
	}
	if err != nil {
		st := AgentStatus{State: AgentSkipped, Why: err.Error()}
		var reason *StatusReason
		if errors.As(err, &reason) {
			st.Reason = reason
		}
		return argv, env, st
	}
	if err := check(profile); err != nil {
		key := "extension-sandbox.agent_profile_refused"
		if sup.OS == "linux" {
			key = "extension-status.sandbox_bwrap_start"
		}
		return argv, env, agentStatusReason(AgentUnavailable, key, i18n.Args{"err": err.Error()})
	}
	return wrapped, append(withoutKey(env, AgentEnv), AgentEnv+"=1"), AgentStatus{State: AgentActive}
}

// agentClosedDirs are the folders of the data directory the agent sandbox closes whole besides identity's secret
// folders: only Persona's own processes use them. agentSocketDirs hold sockets an agent may not connect to.
var (
	agentClosedDirs = []string{"managers", "extensions/run", "run"}
	agentSocketDirs = []string{"extensions/run", "plugins/run", "run"}
)

// agentWorkspaces are the folders in the data directory Persona's own agents run in (the assistant, the health
// space, the tracks' directories, guests): an agent there is expected. Anywhere else in it, the data directory holds
// the user's projects.
var agentWorkspaces = []string{"assistant", identity.SpaceHealth, "tracks", "guest"}

// DarwinAgent is the sandbox-exec profile for plan: allow everything (version 1, allow default), then
//   - deny reading and writing the data directory's secrets where they are written, at its top (identity's list, as
//     the node's path guard reads it): the credential files by the start of their name, also with a leading dot (the
//     temporary file a rewrite renames into place) and anything in a folder by such a name; the key files and their
//     backups (identity.DataCredentialPattern); the secret folders (identity.DataSecretDirs) whole;
//   - deny the folders only Persona's own processes use, whole: managers/ (a manager's manager.json and key),
//     extensions/run/ and run/ (the extensions' sockets and the hub's edge), and connecting to those sockets and to
//     plugins/run/'s (a hub's from before #1844);
//   - deny writing the data directory itself, every folder above it and its other names, and the folders in it that
//     hold a closed one (extensions/): renaming one would move the secrets out from under the rules; and the switch file;
//   - allow reading this start's own launcher settings.
//
// Every path is the real one (the rules match what the kernel resolves; /tmp is /private/tmp). It refuses (an error
// saying why) a data directory that is the home or above it, or that holds the folder the agent runs in (other than
// Persona's own workspaces), since its rules would then reach the user's own files.
func DarwinAgent(plan AgentPlan) (string, error) {
	home := realPath(plan.Home)
	cwd := ""
	if plan.Cwd != "" {
		cwd = realPath(plan.Cwd)
	}
	var dirs, names []string
	for _, d := range plan.DataDirs {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		d = filepath.Clean(d)
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			continue
		}
		r := realPath(d)
		if !slices.Contains(dirs, r) {
			dirs = append(dirs, r)
		}
		// The name as written (a custom link such as ~/.state-link -> .persona): replacing it would point the next start elsewhere.
		if named := filepath.Join(realPath(filepath.Dir(d)), filepath.Base(d)); named != r && !slices.Contains(names, named) {
			names = append(names, named)
		}
	}
	if len(dirs) == 0 {
		return "", NewStatusReason("extension-sandbox.agent_no_data_dir", nil)
	}
	for _, d := range dirs {
		if strings.ContainsAny(d, "\"\n\\") {
			return "", NewStatusReason("extension-sandbox.agent_bad_path", i18n.Args{"dir": d})
		}
		if home != "" && (d == home || within(home, d)) {
			return "", NewStatusReason("extension-sandbox.agent_home", i18n.Args{"dir": d})
		}
		if cwd != "" && (cwd == d || within(cwd, d)) {
			rel, _ := filepath.Rel(d, cwd)
			first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
			if !slices.Contains(agentWorkspaces, first) {
				return "", NewStatusReason("extension-sandbox.agent_cwd_inside", i18n.Args{"cwd": cwd})
			}
		}
	}
	q := strconv.Quote
	alt := make([]string, len(identity.DataSecrets))
	for i, n := range identity.DataSecrets {
		alt[i] = reQuote(n)
	}
	whole := append(append([]string(nil), identity.DataSecretDirs...), agentClosedDirs...)
	var deny, sockets, noWrite, allow []string
	for _, d := range dirs {
		re := reQuote(d)
		deny = append(deny,
			`(regex #"^`+re+`/\.?(`+strings.Join(alt, "|")+`)[^/]*(/.*)?$")`,
			`(regex #"^`+re+`/[^/]*`+identity.DataCredentialPattern+`")`)
		for _, sub := range whole {
			deny = append(deny, `(subpath `+q(d+"/"+sub)+`)`)
		}
		for _, sub := range agentSocketDirs {
			sockets = append(sockets, `(remote unix-socket (subpath `+q(d+"/"+sub)+`))`)
		}
		for p := d; p != "/" && p != "."; p = filepath.Dir(p) {
			if lit := `(literal ` + q(p) + `)`; !slices.Contains(noWrite, lit) {
				noWrite = append(noWrite, lit)
			}
		}
		noWrite = append(noWrite, `(literal `+q(d+"/"+AgentOnFile)+`)`)
		for _, sub := range whole {
			for up := filepath.Dir(sub); up != "."; up = filepath.Dir(up) {
				if lit := `(literal ` + q(d+"/"+up) + `)`; !slices.Contains(noWrite, lit) {
					noWrite = append(noWrite, lit)
				}
			}
		}
	}
	for _, n := range names {
		noWrite = append(noWrite, `(literal `+q(n)+`)`)
	}
	for _, f := range plan.LauncherSettings {
		if f == "" || !filepath.IsAbs(f) {
			continue
		}
		f = filepath.Join(realPath(filepath.Dir(filepath.Clean(f))), filepath.Base(f))
		for _, d := range dirs {
			if filepath.Dir(f) == d+"/launcher-settings" {
				allow = append(allow, `(allow file-read* (literal `+q(f)+`))`,
					`(allow file-read-metadata (literal `+q(d+"/launcher-settings")+`))`)
			}
		}
	}
	lines := []string{
		"(version 1)",
		"(allow default)",
		"(deny file-read* file-write*\n  " + strings.Join(deny, "\n  ") + ")",
		"(deny network-outbound\n  " + strings.Join(sockets, "\n  ") + ")",
		"(deny file-write*\n  " + strings.Join(noWrite, "\n  ") + ")",
	}
	return strings.Join(append(lines, dedupe(allow)...), "\n"), nil
}

// reQuote escapes s for a sandbox-exec regex (POSIX extended).
func reQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`.^$|?*+()[]{}\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// realPath is p with its links followed (sandbox-exec matches real paths); p itself when it cannot be followed.
func realPath(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// within: p is inside dir (not dir itself).
func within(p, dir string) bool {
	return strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

func dedupe(xs []string) []string {
	var out []string
	for _, x := range xs {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// envValue is key's value in env as getenv sees it: the first.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			return val
		}
	}
	return ""
}

func withoutKey(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != key {
			out = append(out, kv)
		}
	}
	return out
}

// agentProfiles remembers the Darwin profiles sandbox-exec took or refused (a rule it cannot parse would otherwise fail
// the agent after an exec, with nothing left to fall back to). Linux deliberately preflights every start instead.
var agentProfiles sync.Map // profile -> error string ("" took it)

func checkAgentProfile(profile string) error {
	if v, ok := agentProfiles.Load(profile); ok {
		if s := v.(string); s != "" {
			return errors.New(s)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	why := ""
	if out, err := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, "/usr/bin/true").CombinedOutput(); err != nil {
		why = firstLine(out, err)
	}
	agentProfiles.Store(profile, why)
	if why != "" {
		return errors.New(why)
	}
	return nil
}
