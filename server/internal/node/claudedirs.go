package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// Claude Code keeps everything (projects/<cwd>/<session>.jsonl, sessions/<pid>.json, settings.json) under its config
// directory: $CLAUDE_CONFIG_DIR, else ~/.claude. Launchers for several accounts or profiles set CLAUDE_CONFIG_DIR, so one
// machine can have many. The node looks in all of them:
//   - the node's own default ($CLAUDE_CONFIG_DIR of the node, else ~/.claude);
//   - the machine's Agent directory, private assistant profiles, and explicitly selected assistant directories;
//   - every running claude process's CLAUDE_CONFIG_DIR (scanned every 30 s; also tells which directory a pid uses);
//   - the hub's list: hub.json machines[].claude_config_dirs plus directories scanned earlier (node.configure), so a
//     profile's past sessions stay listed when none of its claudes runs.
// Directories are compared by their real path: ~/.claude/projects symlinked into a profile is the same directory.
// Directories the user blocked for this machine (node.configure claude_dirs_blocked, proto.ClaudeDirBlocked) are left
// out of all of it: not looked in, not reported, their sessions not listed. The node's default directory is never
// blocked. Launch selection is separate from discovery below.

// claudeScanEvery is how often running claude processes are scanned for their config directory.
const claudeScanEvery = 30 * time.Second

type claudeDirs struct {
	mu        sync.Mutex
	known     []string           // from the hub: configured and remembered
	blocked   []string           // from the hub: patterns of directories kept out (proto.ClaudeDirBlocked)
	found     []string           // seen in running claude processes, in order of discovery
	byPID     map[int]string     // live claude pid -> its config directory
	bySession map[string]procEnv // session a live claude runs (its sessions/<pid>.json) -> that claude's environment
	scanned   time.Time
	report    func(dirs []string) // tells the hub about newly found directories
	scan      func() map[int]procEnv
}

// procEnv is what the scan keeps from a running claude's environment: its config directory, and the API endpoint
// and model a launcher function exported (used to tell which launcher started a claude Persona did not start).
type procEnv struct {
	ConfigDir string // CLAUDE_CONFIG_DIR
	BaseURL   string // ANTHROPIC_BASE_URL
	Model     string // ANTHROPIC_MODEL
}

func newClaudeDirs() *claudeDirs {
	return &claudeDirs{byPID: map[int]string{}, bySession: map[string]procEnv{}, scan: scanClaudeProcesses}
}

// defaultClaudeDir is the inherited legacy default, also kept for old-session discovery.
func (n *Node) defaultClaudeDir() string {
	if n.sessionScope != nil {
		return n.sessionScope.claude
	}
	return claudeDirOf(n.home)
}

// claudeDirOf is the node's own default Claude Code directory for home: $CLAUDE_CONFIG_DIR, else ~/.claude.
func claudeDirOf(home string) string { return nodeclient.ClaudeDir(home) }

// claudeConfigDirs lists every config directory in use, the default first, each once (by real path), existing only.
func (n *Node) claudeConfigDirs() []string {
	if n.sessionScope != nil {
		return []string{n.sessionScope.claude}
	}
	d := n.cdirs
	n.refreshClaudeDirs()
	cands := []string{n.defaultClaudeDir()}
	// Include private assistant profiles even before a process scan, including
	// after a node restart when only their transcripts remain.
	profiles, _ := filepath.Glob(filepath.Join(n.dir, "profiles", "*"))
	cands = append(cands, profiles...)
	cands = append(cands, filepath.Join(n.home, ".claude"))
	if dir := n.claudeStartDir.Load(); dir != nil && *dir != "" {
		cands = append(cands, *dir)
	}
	if n.state != nil {
		n.state.mu.Lock()
		for _, st := range n.state.Sessions {
			if st.ConfigDir != "" && st.ConfigDir != "default" {
				cands = append(cands, st.ConfigDir)
			}
		}
		n.state.mu.Unlock()
	}
	if d != nil {
		d.mu.Lock()
		cands = append(append(cands, d.found...), d.known...)
		d.mu.Unlock()
	}
	var out, seen []string
	for i, c := range cands {
		c = proto.ExpandHome(c, n.home)
		real, err := filepath.EvalSymlinks(c)
		if err != nil {
			continue // missing
		}
		if i > 0 && n.claudeDirBlocked(c, real) {
			continue
		}
		if !slices.Contains(seen, real) {
			seen = append(seen, real)
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		out = []string{n.defaultClaudeDir()}
	}
	return out
}

// projectDirs: the projects directory of each config directory. Two config directories may share one through a
// symlink; that one is listed once.
func (n *Node) projectDirs() []string {
	var out, seen []string
	for _, d := range n.claudeConfigDirs() {
		p := filepath.Join(d, "projects")
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if !slices.Contains(seen, real) {
			seen = append(seen, real)
			out = append(out, p)
		}
	}
	return out
}

// globProjects globs pattern (relative to a projects directory) in every projects directory.
func (n *Node) globProjects(pattern ...string) []string {
	n.projectGlobs.Add(1)
	var out []string
	for _, p := range n.projectDirs() {
		if n.sessionScope != nil && !sessionScopeWithin(n.sessionScope.claude, p) {
			continue
		}
		m, _ := filepath.Glob(filepath.Join(append([]string{p}, pattern...)...))
		for _, path := range m {
			if n.scopedTranscript(path) {
				out = append(out, path)
			}
		}
	}
	return out
}

// claudeSessionFile is sessions/<pid>.json of that claude: in the directory its environment names when known, else
// the newest one among the config directories (a stale file of a reused pid may sit in another).
func (n *Node) claudeSessionFile(pid int) string {
	name := filepath.Join("sessions", strconv.Itoa(pid)+".json")
	if d := n.cdirs; d != nil {
		n.refreshClaudeDirs()
		d.mu.Lock()
		dir, ok := d.byPID[pid]
		d.mu.Unlock()
		if ok {
			return filepath.Join(proto.ExpandHome(dir, n.home), name)
		}
	}
	best, bestT := filepath.Join(n.defaultClaudeDir(), name), time.Time{}
	for _, dir := range n.claudeConfigDirs() {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.ModTime().After(bestT) {
			best, bestT = p, st.ModTime()
		}
	}
	return best
}

// primaryClaudeDir is the config directory most running claudes use (the default when none runs): the one whose
// settings the machine settings page shows.
func (n *Node) primaryClaudeDir() string {
	dirs := n.claudeConfigDirs()
	d := n.cdirs
	if d == nil {
		return dirs[0]
	}
	count := map[string]int{}
	d.mu.Lock()
	for _, dir := range d.byPID {
		if real, err := filepath.EvalSymlinks(proto.ExpandHome(dir, n.home)); err == nil {
			count[real]++
		}
	}
	d.mu.Unlock()
	best, bestN := dirs[0], -1
	for _, dir := range dirs {
		real, _ := filepath.EvalSymlinks(dir)
		if count[real] > bestN {
			best, bestN = dir, count[real]
		}
	}
	return best
}

// claudeDirBlocked: dir (or its real path) matches a blocked pattern. The default directory never does.
func (n *Node) claudeDirBlocked(dir, real string) bool {
	d := n.cdirs
	if d == nil {
		return false
	}
	d.mu.Lock()
	blocked := d.blocked
	d.mu.Unlock()
	return n.blockedBy(dir, real, blocked)
}

// cwdBlocked: true if a session's working directory matches any of the machine's blocked patterns.
func (n *Node) cwdBlocked(cwd string) bool {
	if cwd == "" {
		return false
	}
	d := n.cdirs
	if d == nil {
		return false
	}
	d.mu.Lock()
	blocked := d.blocked
	d.mu.Unlock()
	if len(blocked) == 0 {
		return false
	}
	real, _ := filepath.EvalSymlinks(proto.ExpandHome(cwd, n.home))
	return proto.ClaudeDirBlocked(cwd, n.home, blocked) || (real != "" && proto.ClaudeDirBlocked(real, n.home, blocked))
}

func (n *Node) blockedBy(dir, real string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	def := proto.ExpandHome(n.defaultClaudeDir(), n.home)
	if dir = proto.ExpandHome(dir, n.home); filepath.Clean(dir) == filepath.Clean(def) {
		return false
	}
	if real != "" {
		if defReal, err := filepath.EvalSymlinks(def); err == nil && real == defReal {
			return false
		}
	}
	return proto.ClaudeDirBlocked(dir, n.home, patterns) || (real != "" && proto.ClaudeDirBlocked(real, n.home, patterns))
}

// reportableDirs: the found directories the hub may remember, those patterns do not block.
func (n *Node) reportableDirs(found, patterns []string) []string {
	var out []string
	for _, dir := range found {
		real, _ := filepath.EvalSymlinks(proto.ExpandHome(dir, n.home))
		if !n.blockedBy(dir, real, patterns) {
			out = append(out, dir)
		}
	}
	return out
}

// setBlockedClaudeDirs takes the hub's blocked patterns (node.configure). The session list is rebuilt on its next
// read; directories found while blocked are reported once unblocked, so the hub remembers them again.
func (n *Node) setBlockedClaudeDirs(patterns []string) {
	d := n.cdirs
	if d == nil {
		return
	}
	patterns = proto.CleanClaudeDirPatterns(patterns)
	d.mu.Lock()
	before := d.blocked
	d.blocked = patterns
	found, report := slices.Clone(d.found), d.report
	d.mu.Unlock()
	if slices.Equal(before, patterns) {
		return
	}
	offlineCache.Lock()
	offlineCache.at = time.Time{}
	offlineCache.Unlock()
	if report == nil {
		return
	}
	if now := n.reportableDirs(found, patterns); len(now) > len(n.reportableDirs(found, before)) {
		report(now)
	}
}

// claudeScanGOOS is the platform refreshClaudeDirs reads running claudes' directories on (tests set it).
var claudeScanGOOS = runtime.GOOS

// scannedConfigDir is a running claude's CLAUDE_CONFIG_DIR as the node names its directories. On Windows the
// assistant's claude gets it with forward slashes (scriptPath, #5487) and every other start with backslashes: cleaned
// to backslashes, one profile is listed once. filepath.Clean does that on Windows; the replacement makes it so where
// tests run.
func scannedConfigDir(goos, dir string) string {
	if goos != "windows" {
		return dir
	}
	return filepath.Clean(strings.ReplaceAll(dir, "/", `\`))
}

// setKnownClaudeDirs takes the hub's list (node.configure).
func (n *Node) setKnownClaudeDirs(dirs []string) {
	if n.cdirs == nil {
		return
	}
	n.cdirs.mu.Lock()
	n.cdirs.known = dirs
	n.cdirs.mu.Unlock()
}

// refreshClaudeDirs rescans running claudes when the last scan is old; new directories are reported to the hub.
func (n *Node) refreshClaudeDirs() {
	if n.sessionScope != nil {
		return
	}
	d := n.cdirs
	if d == nil {
		return
	}
	d.mu.Lock()
	if time.Since(d.scanned) < claudeScanEvery {
		d.mu.Unlock()
		return
	}
	d.scanned = time.Now()
	scan := d.scan
	d.mu.Unlock()
	envs := scan()
	byPID := map[int]string{}
	for pid, e := range envs {
		if e.ConfigDir != "" {
			byPID[pid] = scannedConfigDir(claudeScanGOOS, e.ConfigDir)
		}
	}
	d.mu.Lock()
	d.byPID = byPID
	d.mu.Unlock()
	// Which session each claude runs: its sessions/<pid>.json (found through the directory just scanned).
	bySession := map[string]procEnv{}
	for pid, e := range envs {
		if e.BaseURL == "" && e.Model == "" {
			continue
		}
		if id := readClaudeSessionID(n.claudeSessionFile(pid)); id != "" {
			bySession[id] = e
		}
	}
	d.mu.Lock()
	d.bySession = bySession
	var added []string
	for _, dir := range byPID {
		if !slices.Contains(d.found, dir) {
			d.found = append(d.found, dir)
			added = append(added, dir)
		}
	}
	found, report := slices.Clone(d.found), d.report
	d.mu.Unlock()
	// A blocked directory is kept in found (unblocking it lists it again at once) but never reported.
	if report != nil {
		d.mu.Lock()
		blocked := d.blocked
		d.mu.Unlock()
		if len(n.reportableDirs(added, blocked)) > 0 {
			report(n.reportableDirs(found, blocked))
		}
	}
}

// sessionEnv is the scanned environment of the live claude running session id (false when none was seen or it set
// neither ANTHROPIC_BASE_URL nor ANTHROPIC_MODEL).
func (n *Node) sessionEnv(id string) (procEnv, bool) {
	d := n.cdirs
	if d == nil {
		return procEnv{}, false
	}
	n.refreshClaudeDirs()
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.bySession[id]
	return e, ok
}

var (
	claudeConfigEnvRe = envVarRe("CLAUDE_CONFIG_DIR")
	anthropicBaseRe   = envVarRe("ANTHROPIC_BASE_URL")
	anthropicModelRe  = envVarRe("ANTHROPIC_MODEL")
)

// envVarRe matches NAME=value in a NUL- (/proc/<pid>/environ) or space-separated (ps -E) environment.
func envVarRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\x00\s])` + name + `=([^\x00\s]+)`)
}

// parseProcEnv picks the variables procEnv keeps out of an environment listing.
func parseProcEnv(env string) procEnv {
	get := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(env); m != nil {
			return m[1]
		}
		return ""
	}
	return procEnv{ConfigDir: get(claudeConfigEnvRe), BaseURL: get(anthropicBaseRe), Model: get(anthropicModelRe)}
}

// scanClaudeProcesses maps this user's running claude processes to the variables procEnv keeps from their
// environment (processes with none of them are left out: default directory, default endpoint). Linux reads the
// environ of the claudes in the shared walk of this user's /proc entries (userProcesses); macOS asks ps once for all;
// Windows reads the process table (scanClaudeProcessesOS).
func scanClaudeProcesses() map[int]procEnv {
	out := map[int]procEnv{}
	if runtime.GOOS == "windows" {
		return scanClaudeProcessesOS()
	}
	if runtime.GOOS == "linux" {
		for pid, argv := range userProcesses() {
			if !isClaudeCmd(argv) {
				continue
			}
			env, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
			if pe := parseProcEnv(string(env)); pe != (procEnv{}) {
				out[pid] = pe
			}
		}
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "pid=,command=", "-U", strconv.Itoa(os.Getuid())).Output()
	if err != nil {
		return out
	}
	return parsePSEnv(string(b))
}

// parsePSEnv reads `ps -E -o pid=,command=` output: the command line is followed by the environment.
func parsePSEnv(s string) map[int]procEnv {
	out := map[int]procEnv{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || !isClaudeCmd(f[1:]) {
			continue
		}
		if pe := parseProcEnv(line); pe != (procEnv{}) {
			out[pid] = pe
		}
	}
	return out
}

// isClaudeCmd: the command line runs Claude Code — its native binary, or its script under node / bun.
func isClaudeCmd(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch progName(argv[0]) {
	case "claude":
		return true
	case "node", "bun":
		return len(argv) > 1 && strings.Contains(argv[1], "claude")
	}
	return false
}

// listClaudeDirs: every config directory the node knows of (default, found in running claudes, from the hub),
// existing, each once, blocked ones marked: what the settings page offers to block and unblock.
func (n *Node) listClaudeDirs() []proto.ClaudeDir {
	n.refreshClaudeDirs()
	type cand struct{ dir, source string }
	cands := []cand{{n.defaultClaudeDir(), "default"}}
	running := map[string]int{}
	if d := n.cdirs; d != nil {
		d.mu.Lock()
		for _, f := range d.found {
			cands = append(cands, cand{f, "found"})
		}
		for _, k := range d.known {
			cands = append(cands, cand{k, "found"}) // the hub tells config ones apart
		}
		for _, dir := range d.byPID {
			if real, err := filepath.EvalSymlinks(proto.ExpandHome(dir, n.home)); err == nil {
				running[real]++
			}
		}
		d.mu.Unlock()
	}
	var out []proto.ClaudeDir
	var seen []string
	for i, c := range cands {
		real, err := filepath.EvalSymlinks(proto.ExpandHome(c.dir, n.home))
		if err != nil || slices.Contains(seen, real) {
			continue
		}
		seen = append(seen, real)
		files, _ := filepath.Glob(filepath.Join(real, "projects", "*", "*.jsonl"))
		r := running[real]
		if i == 0 {
			r = 0 // claudes without CLAUDE_CONFIG_DIR are not scanned: unknown
		}
		out = append(out, proto.ClaudeDir{Path: c.dir, Default: i == 0, Blocked: i > 0 && n.claudeDirBlocked(c.dir, real),
			Source: c.source, Running: r, Sessions: len(files)})
	}
	return out
}
