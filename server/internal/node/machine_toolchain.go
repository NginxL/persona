package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	toolchainCacheTTL       = 60 * time.Second
	toolchainOverallTimeout = 8 * time.Second
	toolchainCmdTimeout     = 2 * time.Second
	toolchainSocketTimeout  = 500 * time.Millisecond
	toolchainMaxConcurrency = 6
	toolchainMaxOutputBytes = 8 << 10
	toolchainMaxPathEntries = 32
	toolchainMaxNoteLen     = 160
)

type toolchainSpec struct {
	id       string
	category string // "core" | "agent"
	cmds     []string
	args     []string
	version  *lazyre.Regexp
}

var (
	toolchainDottedRe = lazyre.New(`v?(\d+(?:\.\d+)+)`)
	toolchainGoRe     = lazyre.New(`go(\d+(?:\.\d+)+)`)
	toolchainHostRe   = lazyre.New(`^[a-zA-Z0-9][a-zA-Z0-9.-]*\.[a-zA-Z]{2,}$`)
	toolchainPctRe    = lazyre.New(`(\d{1,3})%`)
	toolchainSecretRe = lazyre.New(
		`(?i)(?:` +
			`gh[pousr]_[A-Za-z0-9_]{8,}|` +
			`github_pat_[A-Za-z0-9_]{8,}|` +
			`sk-[A-Za-z0-9_-]{8,}|` +
			`xox[baprs]-[A-Za-z0-9-]{8,}|` +
			`AIza[0-9A-Za-z_-]{16,}|` +
			`AKIA[0-9A-Z]{16}|` +
			`bearer\s+[A-Za-z0-9._~+/=-]{8,}|` +
			`basic\s+[A-Za-z0-9+/=]{8,}|` +
			`[a-z][a-z0-9+.-]*://[^\s:@/]+:[^\s@/]+@|` +
			`(?:token|secret|password|passwd|api[_-]?key|auth|credential)\s*[:=]\s*\S+` +
			`)`,
	)
)

var coreToolchainSpecs = []toolchainSpec{
	{id: "git", category: "core", cmds: []string{"git"}, args: []string{"--version"}},
	{id: "go", category: "core", cmds: []string{"go"}, args: []string{"version"}, version: toolchainGoRe},
	{id: "node", category: "core", cmds: []string{"node"}, args: []string{"--version"}},
	{id: "npm", category: "core", cmds: []string{"npm"}, args: []string{"--version"}},
	{id: "pnpm", category: "core", cmds: []string{"pnpm"}, args: []string{"--version"}},
	{id: "python3", category: "core", cmds: []string{"python3"}, args: []string{"--version"}},
	{id: "gh", category: "core", cmds: []string{"gh"}, args: []string{"--version"}},
	{id: "docker", category: "core", cmds: []string{"docker"}, args: []string{"--version"}},
	{id: "pg_dump", category: "core", cmds: []string{"pg_dump"}, args: []string{"--version"}},
	{id: "psql", category: "core", cmds: []string{"psql"}, args: []string{"--version"}},
}

var agentToolchainSpecs = []toolchainSpec{
	{id: "claude", category: "agent", cmds: []string{"claude"}, args: []string{"--version"}},
	{id: "codex", category: "agent", cmds: []string{"codex"}, args: []string{"--version"}},
	{id: "agy", category: "agent", cmds: []string{"agy"}, args: []string{"--version"}},
	{id: "omp", category: "agent", cmds: []string{"omp"}, args: []string{"--version"}},
}

var toolchainMacShims = map[string]bool{
	"git":     true,
	"python3": true,
}

type toolchainDeps struct {
	goos           string
	goarch         string
	home           string
	overallTimeout time.Duration
	getenv         func(string) string
	now            func() time.Time
	stat           func(string) (os.FileInfo, error)
	readDir        func(string) ([]os.DirEntry, error)
	readFile       func(string) ([]byte, error)
	evalSymlinks   func(string) (string, error)
	lookPathIn     func(name string, dirs []string) string
	run            func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	dialUnix       func(ctx context.Context, sock string) error
}

// toolchainState holds the node's on-demand toolchain cache and single-flight
// state. Zero-value safe so Node instances constructed without New still work.
type toolchainState struct {
	mu       sync.Mutex
	cached   *proto.MachineToolchainReport
	cachedAt time.Time
	inFlight *toolchainFlight
	deps     *toolchainDeps // test hook; nil uses real host dependencies
}

type toolchainFlight struct {
	done     chan struct{}
	report   proto.MachineToolchainReport
	complete bool
}

func (n *Node) machineToolchain(ctx context.Context, params json.RawMessage) (any, error) {
	var p proto.MachineToolchainParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, errors.New("bad machine.toolchain params")
		}
	}
	deps := n.toolchainDeps()
	return n.toolchain.get(ctx, deps, p.Refresh)
}

func (n *Node) toolchainDeps() toolchainDeps {
	n.toolchain.mu.Lock()
	custom := n.toolchain.deps
	n.toolchain.mu.Unlock()
	if custom != nil {
		d := *custom
		if d.goos == "" {
			d.goos = runtime.GOOS
		}
		if d.goarch == "" {
			d.goarch = runtime.GOARCH
		}
		if d.now == nil {
			d.now = time.Now
		}
		if d.getenv == nil {
			d.getenv = func(string) string { return "" }
		}
		if d.stat == nil {
			d.stat = os.Stat
		}
		if d.readDir == nil {
			d.readDir = os.ReadDir
		}
		if d.readFile == nil {
			d.readFile = os.ReadFile
		}
		if d.evalSymlinks == nil {
			d.evalSymlinks = func(p string) (string, error) { return p, nil }
		}
		if d.lookPathIn == nil {
			d.lookPathIn = lookupInDirs
		}
		if d.run == nil {
			d.run = runToolchainCmd
		}
		if d.dialUnix == nil {
			d.dialUnix = dialToolchainUnix
		}
		return d
	}
	home := n.home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return toolchainDeps{
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
		home:         home,
		getenv:       os.Getenv,
		now:          time.Now,
		stat:         os.Stat,
		readDir:      os.ReadDir,
		readFile:     os.ReadFile,
		evalSymlinks: filepath.EvalSymlinks,
		lookPathIn:   lookupInDirs,
		run:          runToolchainCmd,
		dialUnix:     dialToolchainUnix,
	}
}

func (s *toolchainState) get(ctx context.Context, deps toolchainDeps, refresh bool) (proto.MachineToolchainReport, error) {
	if !refresh {
		s.mu.Lock()
		now := deps.now()
		if s.cached != nil && now.Sub(s.cachedAt) < toolchainCacheTTL {
			rep := cloneToolchainReport(*s.cached)
			rep.Cached = true
			s.mu.Unlock()
			return rep, nil
		}
		if s.inFlight != nil {
			flight := s.inFlight
			s.mu.Unlock()
			select {
			case <-flight.done:
				rep := cloneToolchainReport(flight.report)
				rep.Cached = flight.complete
				return rep, nil
			case <-ctx.Done():
				return proto.MachineToolchainReport{}, ctx.Err()
			}
		}
		flight := &toolchainFlight{done: make(chan struct{})}
		s.inFlight = flight
		s.mu.Unlock()

		// Run the shared probe detached from the first caller's cancellation so
		// one caller disconnecting cannot poison waiters or cache a partial result.
		probeCtx := context.WithoutCancel(ctx)
		go func() {
			rep, complete := probeMachineToolchain(probeCtx, deps)
			s.mu.Lock()
			cachedCopy := cloneToolchainReport(rep)
			if complete {
				s.cached = &cachedCopy
				s.cachedAt = deps.now()
			}
			flight.report = cachedCopy
			flight.complete = complete
			if s.inFlight == flight {
				s.inFlight = nil
			}
			close(flight.done)
			s.mu.Unlock()
		}()

		select {
		case <-flight.done:
			return cloneToolchainReport(flight.report), nil
		case <-ctx.Done():
			return proto.MachineToolchainReport{}, ctx.Err()
		}
	}

	// Manual refresh runs on the caller's context and only replaces the cache if
	// it completes without cancellation or overall timeout.
	rep, complete := probeMachineToolchain(ctx, deps)
	if ctx.Err() != nil {
		return proto.MachineToolchainReport{}, ctx.Err()
	}
	if complete {
		s.mu.Lock()
		cachedCopy := cloneToolchainReport(rep)
		s.cached = &cachedCopy
		s.cachedAt = deps.now()
		s.mu.Unlock()
	}
	return rep, nil
}

func cloneToolchainReport(r proto.MachineToolchainReport) proto.MachineToolchainReport {
	out := r
	out.Tools = slices.Clone(r.Tools)
	for i := range out.Tools {
		out.Tools[i].Capabilities = slices.Clone(out.Tools[i].Capabilities)
	}
	out.AgentCLIs = slices.Clone(r.AgentCLIs)
	for i := range out.AgentCLIs {
		out.AgentCLIs[i].Capabilities = slices.Clone(out.AgentCLIs[i].Capabilities)
	}
	if r.Docker != nil {
		d := *r.Docker
		out.Docker = &d
	}
	if r.GitHubCLI != nil {
		g := *r.GitHubCLI
		g.Hosts = slices.Clone(g.Hosts)
		out.GitHubCLI = &g
	}
	if r.Hardware != nil {
		h := *r.Hardware
		h.GPUs = slices.Clone(h.GPUs)
		if h.Power != nil {
			p := *h.Power
			if p.BatteryPercent != nil {
				v := *p.BatteryPercent
				p.BatteryPercent = &v
			}
			if p.Charging != nil {
				v := *p.Charging
				p.Charging = &v
			}
			h.Power = &p
		}
		out.Hardware = &h
	}
	out.Environment.PathEntries = slices.Clone(r.Environment.PathEntries)
	out.Environment.VersionManagers = slices.Clone(r.Environment.VersionManagers)
	return out
}

func probeMachineToolchain(parent context.Context, deps toolchainDeps) (proto.MachineToolchainReport, bool) {
	started := deps.now()
	overall := deps.overallTimeout
	if overall <= 0 {
		overall = toolchainOverallTimeout
	}
	ctx, cancel := context.WithTimeout(parent, overall)
	defer cancel()

	searchDirs, versionManagers := buildToolchainSearchDirs(deps)
	probeEnv := buildProbeEnv(deps, searchDirs)

	hasMacDevDir := sync.OnceValue(func() bool {
		if deps.goos != "darwin" {
			return true
		}
		cctx, ccancel := context.WithTimeout(ctx, toolchainCmdTimeout)
		defer ccancel()
		out, err := deps.run(cctx, probeEnv, "/usr/bin/xcode-select", "-p")
		if err != nil {
			return false
		}
		dir := strings.TrimSpace(string(out))
		if dir == "" {
			return false
		}
		_, err = deps.stat(dir)
		return err == nil
	})

	// Pre-populate every slot with its ID, category, and timeout fallback so
	// entries skipped when the overall context expires never appear as empty rows.
	coreEntries := make([]proto.ToolchainEntry, len(coreToolchainSpecs))
	for i, spec := range coreToolchainSpecs {
		coreEntries[i] = proto.ToolchainEntry{
			ID:       spec.id,
			Category: spec.category,
			Found:    false,
			Status:   "error",
			Note:     "probe timed out",
		}
	}
	agentEntries := make([]proto.ToolchainEntry, len(agentToolchainSpecs))
	for i, spec := range agentToolchainSpecs {
		agentEntries[i] = proto.ToolchainEntry{
			ID:       spec.id,
			Category: spec.category,
			Found:    false,
			Status:   "error",
			Note:     "probe timed out",
		}
	}

	var (
		dockerInfo *proto.ToolchainDockerInfo
		ghInfo     *proto.ToolchainGHAuth
		hwInfo     *proto.ToolchainHardware
		limiter    = make(chan struct{}, toolchainMaxConcurrency)
		wg         sync.WaitGroup
	)

	runLimited := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case limiter <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-limiter }()
			if ctx.Err() != nil {
				return
			}
			fn()
		}()
	}

	var dockerPath, ghPath string
	var pathMu sync.Mutex

	for i, spec := range coreToolchainSpecs {
		i, spec := i, spec
		runLimited(func() {
			entry := probeToolchainEntry(ctx, deps, probeEnv, searchDirs, spec, hasMacDevDir)
			coreEntries[i] = entry
			if entry.Found && (spec.id == "docker" || spec.id == "gh") {
				pathMu.Lock()
				if spec.id == "docker" {
					dockerPath = entry.Path
				} else if spec.id == "gh" {
					ghPath = entry.Path
				}
				pathMu.Unlock()
			}
		})
	}

	for i, spec := range agentToolchainSpecs {
		i, spec := i, spec
		runLimited(func() {
			agentEntries[i] = probeToolchainEntry(ctx, deps, probeEnv, searchDirs, spec, hasMacDevDir)
		})
	}

	runLimited(func() {
		hwInfo = probeToolchainHardware(ctx, deps, probeEnv, searchDirs)
	})

	wg.Wait()

	// Follow-up capability checks for gh and docker (bounded by remaining overall timeout).
	if ctx.Err() == nil {
		var followWG sync.WaitGroup
		followWG.Add(2)
		go func() {
			defer followWG.Done()
			ghEntry := findEntryByID(coreEntries, "gh")
			ghInfo = probeGitHubCLI(ctx, deps, probeEnv, ghPath, ghEntry)
		}()
		go func() {
			defer followWG.Done()
			dockerEntry := findEntryByID(coreEntries, "docker")
			dockerInfo = probeDockerRuntime(ctx, deps, probeEnv, dockerPath, dockerEntry)
		}()
		followWG.Wait()
	}

	// Enrich gh and docker entries with capability flags and refined status.
	for i := range coreEntries {
		switch coreEntries[i].ID {
		case "gh":
			if ghInfo != nil && ghInfo.Installed {
				if ghInfo.Authenticated {
					coreEntries[i].Status = "ready"
					coreEntries[i].Capabilities = appendUnique(coreEntries[i].Capabilities, "authenticated")
				} else {
					coreEntries[i].Status = "unauthenticated"
				}
				if ghInfo.Note != "" && coreEntries[i].Note == "" {
					coreEntries[i].Note = ghInfo.Note
				}
			}
		case "docker":
			if dockerInfo != nil && dockerInfo.Installed {
				coreEntries[i].Status = dockerInfo.Status
				if dockerInfo.SocketAccessible {
					coreEntries[i].Capabilities = appendUnique(coreEntries[i].Capabilities, "daemon_accessible")
				}
				if dockerInfo.Note != "" && coreEntries[i].Note == "" {
					coreEntries[i].Note = dockerInfo.Note
				}
			}
		}
	}

	pathSummary := slices.Clone(searchDirs)
	if len(pathSummary) > toolchainMaxPathEntries {
		pathSummary = pathSummary[:toolchainMaxPathEntries]
	}

	shellName := ""
	if sh := strings.TrimSpace(deps.getenv("SHELL")); sh != "" {
		shellName = filepath.Base(sh)
	}

	proxyConfigured := hasAnyEnv(deps.getenv, "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy")
	customGoPath := hasAnyEnv(deps.getenv, "GOPATH", "GOROOT", "GOBIN")

	dur := deps.now().Sub(started).Milliseconds()
	if dur < 0 {
		dur = 0
	}

	complete := ctx.Err() == nil
	return proto.MachineToolchainReport{
		CollectedAt: deps.now().UTC(),
		Cached:      false,
		CacheTTLSec: int(toolchainCacheTTL / time.Second),
		DurationMS:  dur,
		OS:          deps.goos,
		Arch:        deps.goarch,
		Tools:       coreEntries,
		AgentCLIs:   agentEntries,
		Docker:      dockerInfo,
		GitHubCLI:   ghInfo,
		Hardware:    hwInfo,
		Environment: proto.ToolchainEnvSummary{
			Shell:           shellName,
			PathEntries:     pathSummary,
			VersionManagers: versionManagers,
			HasCustomGoPath: customGoPath,
			ProxyConfigured: proxyConfigured,
		},
	}, complete
}

func probeToolchainEntry(
	ctx context.Context,
	deps toolchainDeps,
	probeEnv []string,
	searchDirs []string,
	spec toolchainSpec,
	hasMacDevDir func() bool,
) proto.ToolchainEntry {
	if ctx.Err() != nil {
		return proto.ToolchainEntry{
			ID:       spec.id,
			Category: spec.category,
			Found:    false,
			Status:   "error",
			Note:     "probe timed out",
		}
	}
	entry := proto.ToolchainEntry{
		ID:       spec.id,
		Category: spec.category,
		Found:    false,
		Status:   "missing",
	}
	var binPath string
	for _, cmd := range spec.cmds {
		if p := deps.lookPathIn(cmd, searchDirs); p != "" {
			binPath = p
			break
		}
	}
	if binPath == "" {
		return entry
	}
	if deps.goos == "darwin" && filepath.Dir(binPath) == "/usr/bin" && toolchainMacShims[filepath.Base(binPath)] {
		if !hasMacDevDir() {
			return entry
		}
	}

	entry.Found = true
	entry.Path = binPath
	entry.Status = "ready"

	if resolved, err := deps.evalSymlinks(binPath); err == nil && resolved != "" {
		entry.ResolvedPath = resolved
		if filepath.Clean(resolved) != filepath.Clean(binPath) {
			entry.Capabilities = appendUnique(entry.Capabilities, "symlink")
		}
	} else {
		entry.ResolvedPath = binPath
	}

	cctx, cancel := context.WithTimeout(ctx, toolchainCmdTimeout)
	defer cancel()
	out, err := deps.run(cctx, probeEnv, binPath, spec.args...)
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			entry.Status = "error"
			entry.Note = "version check timed out"
			return entry
		}
		// Some CLIs print version information even on non-zero exit; parse safely if present.
		if v := extractToolchainVersion(spec, out); v != "" {
			entry.Version = v
			return entry
		}
		entry.Status = "error"
		entry.Note = redactToolchainText(firstNonEmptyLine(string(out)))
		if entry.Note == "" {
			entry.Note = "version check failed"
		}
		return entry
	}
	entry.Version = extractToolchainVersion(spec, out)
	return entry
}

func probeGitHubCLI(
	ctx context.Context,
	deps toolchainDeps,
	probeEnv []string,
	ghPath string,
	entry *proto.ToolchainEntry,
) *proto.ToolchainGHAuth {
	info := &proto.ToolchainGHAuth{
		Installed: entry != nil && entry.Found,
		Status:    "missing",
	}
	if entry != nil {
		info.Version = entry.Version
	}
	if !info.Installed || ghPath == "" {
		return info
	}

	cctx, cancel := context.WithTimeout(ctx, toolchainCmdTimeout)
	defer cancel()
	// Pass GitHub CLI auth environment variables only to the local `gh auth status`
	// child process so headless/CI token-authenticated machines are detected accurately.
	// Output is parsed only for status and hostnames; tokens are never returned.
	ghEnv := append(slices.Clone(probeEnv), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		if v := deps.getenv(k); v != "" {
			ghEnv = append(ghEnv, k+"="+v)
		}
	}
	out, err := deps.run(cctx, ghEnv, ghPath, "auth", "status")
	text := string(out)
	hosts := parseGHAuthHosts(text)
	if len(hosts) > 0 {
		info.Hosts = hosts
	}
	if err == nil {
		info.Authenticated = true
		info.Status = "authenticated"
		return info
	}
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		info.Status = "error"
		info.Note = "auth check timed out"
		return info
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "not logged into") || strings.Contains(lower, "not authenticated") || strings.Contains(lower, "no oauth token") {
		info.Authenticated = false
		info.Status = "unauthenticated"
		return info
	}
	if len(hosts) > 0 && strings.Contains(lower, "logged in to") {
		// Partial multi-host auth: at least one host is logged in.
		info.Authenticated = true
		info.Status = "authenticated"
		info.Note = "partially authenticated"
		return info
	}
	info.Authenticated = false
	info.Status = "unauthenticated"
	return info
}

func parseGHAuthHosts(out string) []string {
	var hosts []string
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		// Never inspect lines that could hold tokens.
		if strings.Contains(lower, "token:") || strings.Contains(lower, "token=") {
			continue
		}
		// Match lines like "github.com" or "✓ Logged in to github.com account ..."
		if toolchainHostRe.MatchString(line) {
			hosts = appendUnique(hosts, strings.ToLower(line))
			continue
		}
		const prefix = "logged in to "
		if idx := strings.Index(lower, prefix); idx >= 0 {
			rest := strings.TrimSpace(line[idx+len(prefix):])
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				candidate := strings.Trim(fields[0], ":.,;()[]")
				if toolchainHostRe.MatchString(candidate) {
					hosts = appendUnique(hosts, strings.ToLower(candidate))
				}
			}
		}
	}
	return hosts
}

func probeDockerRuntime(
	ctx context.Context,
	deps toolchainDeps,
	probeEnv []string,
	dockerPath string,
	entry *proto.ToolchainEntry,
) *proto.ToolchainDockerInfo {
	info := &proto.ToolchainDockerInfo{
		Installed: entry != nil && entry.Found,
		Status:    "missing",
	}
	if entry != nil {
		info.ClientVersion = entry.Version
	}

	dockerHost := strings.TrimSpace(deps.getenv("DOCKER_HOST"))
	dockerContext := strings.TrimSpace(deps.getenv("DOCKER_CONTEXT"))
	hasNonUnixDockerHost := dockerHost != "" && !strings.HasPrefix(dockerHost, "unix://")
	hasCustomDockerContext := dockerContext != "" && dockerContext != "default"
	skipUnixSockets := deps.goos == "windows" || hasNonUnixDockerHost || hasCustomDockerContext

	var foundSock string
	var permDenied bool

	if !skipUnixSockets {
		sockCandidates := dockerSocketCandidates(deps)
		for _, sock := range sockCandidates {
			if _, err := deps.stat(sock); err != nil {
				if errors.Is(err, os.ErrPermission) {
					foundSock = sock
					permDenied = true
				}
				continue
			}
			foundSock = sock
			dctx, dcancel := context.WithTimeout(ctx, toolchainSocketTimeout)
			err := deps.dialUnix(dctx, sock)
			dcancel()
			if err == nil {
				info.SocketPath = sock
				info.SocketAccessible = true
				break
			}
			if errors.Is(err, os.ErrPermission) || strings.Contains(strings.ToLower(err.Error()), "permission denied") {
				permDenied = true
			}
		}
	}
	if info.SocketPath == "" && foundSock != "" {
		info.SocketPath = foundSock
	}

	if !info.Installed {
		if info.SocketAccessible {
			info.Note = "socket present without docker CLI"
		}
		return info
	}

	if info.SocketAccessible {
		info.Status = "ready"
		if dockerPath != "" {
			cctx, cancel := context.WithTimeout(ctx, toolchainCmdTimeout)
			defer cancel()
			if out, err := deps.run(cctx, probeEnv, dockerPath, "version", "--format", "{{.Server.Version}}"); err == nil {
				if v := extractDottedVersion(out); v != "" {
					info.ServerVersion = v
				}
			}
		}
		return info
	}

	if permDenied {
		info.Status = "permission_denied"
		info.Note = "docker socket permission denied"
		return info
	}

	// Fall back to a bounded CLI daemon check when no local Unix socket was accessible:
	// this covers Windows (named pipe / Docker Desktop), non-unix DOCKER_HOST schemes
	// (tcp://, ssh://, npipe://), and non-default Docker contexts.
	if dockerPath != "" && ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, toolchainCmdTimeout)
		out, err := deps.run(cctx, probeEnv, dockerPath, "version", "--format", "{{.Server.Version}}")
		cancel()
		if err == nil {
			trimmed := strings.TrimSpace(string(out))
			lower := strings.ToLower(trimmed)
			if trimmed != "" && !strings.Contains(lower, "cannot connect") && !strings.Contains(lower, "error during connect") {
				info.SocketAccessible = true
				info.Status = "ready"
				if v := extractDottedVersion(out); v != "" {
					info.ServerVersion = v
				}
				return info
			}
		}
		if err != nil && strings.Contains(strings.ToLower(string(out)), "permission denied") {
			info.Status = "permission_denied"
			info.Note = "docker socket permission denied"
			return info
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			info.Status = "daemon_unreachable"
			info.Note = "docker daemon check timed out"
			return info
		}
	}

	info.Status = "daemon_unreachable"
	if deps.goos == "windows" || hasNonUnixDockerHost || hasCustomDockerContext || info.SocketPath != "" {
		info.Note = "docker daemon is not reachable"
	} else {
		info.Note = "docker daemon socket not found"
	}
	return info
}

func dockerSocketCandidates(deps toolchainDeps) []string {
	var out []string
	if dh := strings.TrimSpace(deps.getenv("DOCKER_HOST")); strings.HasPrefix(dh, "unix://") {
		sock := strings.TrimPrefix(dh, "unix://")
		if filepath.IsAbs(sock) {
			return []string{filepath.Clean(sock)}
		}
		return nil
	}
	if deps.goos != "windows" {
		out = appendUnique(out, "/var/run/docker.sock")
		if xdg := strings.TrimSpace(deps.getenv("XDG_RUNTIME_DIR")); xdg != "" && filepath.IsAbs(xdg) {
			out = appendUnique(out, filepath.Join(xdg, "docker.sock"))
		}
		if deps.home != "" {
			out = appendUnique(out,
				filepath.Join(deps.home, ".docker", "run", "docker.sock"),
				filepath.Join(deps.home, ".orbstack", "run", "docker.sock"),
				filepath.Join(deps.home, ".colima", "default", "docker.sock"),
			)
		}
	}
	return out
}

func probeToolchainHardware(
	ctx context.Context,
	deps toolchainDeps,
	probeEnv []string,
	searchDirs []string,
) *proto.ToolchainHardware {
	hw := &proto.ToolchainHardware{}

	// 1. NVIDIA GPU via nvidia-smi (if present on PATH or standard locations).
	if smi := deps.lookPathIn("nvidia-smi", searchDirs); smi != "" {
		cctx, cancel := context.WithTimeout(ctx, toolchainCmdTimeout)
		out, err := deps.run(cctx, probeEnv, smi, "--query-gpu=name,memory.total,driver_version", "--format=csv,noheader,nounits")
		cancel()
		if err == nil {
			hw.GPUs = append(hw.GPUs, parseNvidiaSMI(string(out))...)
		}
	}

	// 2. macOS Apple Silicon / chip info.
	if deps.goos == "darwin" {
		cctx, cancel := context.WithTimeout(ctx, toolchainCmdTimeout)
		out, err := deps.run(cctx, probeEnv, "sysctl", "-n", "machdep.cpu.brand_string")
		cancel()
		if err == nil {
			chip := strings.TrimSpace(string(out))
			if chip != "" {
				hw.Chip = redactToolchainText(chip)
				if strings.HasPrefix(chip, "Apple ") && len(hw.GPUs) == 0 {
					hw.GPUs = append(hw.GPUs, proto.ToolchainGPU{
						Name:    chip + " GPU",
						Vendor:  "apple",
						Unified: true,
					})
				}
			}
		}
	}

	// 3. Optional laptop power / battery / sleep state.
	hw.Power = probeToolchainPower(ctx, deps, probeEnv)

	if hw.Chip == "" && len(hw.GPUs) == 0 && hw.Power == nil {
		return nil
	}
	return hw
}

func parseNvidiaSMI(out string) []proto.ToolchainGPU {
	var gpus []proto.ToolchainGPU
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 1 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		gpu := proto.ToolchainGPU{
			Name:   redactToolchainText(name),
			Vendor: "nvidia",
		}
		if len(parts) >= 2 {
			vramStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), "MiB"))
			if v, err := strconv.ParseInt(vramStr, 10, 64); err == nil && v > 0 {
				gpu.VRAMMB = v
			}
		}
		if len(parts) >= 3 {
			drv := strings.TrimSpace(parts[2])
			if drv != "" && !strings.EqualFold(drv, "[N/A]") {
				gpu.Driver = redactToolchainText(drv)
			}
		}
		gpus = append(gpus, gpu)
	}
	return gpus
}

func probeToolchainPower(ctx context.Context, deps toolchainDeps, probeEnv []string) *proto.ToolchainPower {
	switch deps.goos {
	case "linux":
		return probeLinuxPower(deps)
	case "darwin":
		return probeDarwinPower(ctx, deps, probeEnv)
	default:
		return nil
	}
}

func probeLinuxPower(deps toolchainDeps) *proto.ToolchainPower {
	const base = "/sys/class/power_supply"
	entries, err := deps.readDir(base)
	if err != nil {
		return nil
	}
	var (
		hasBattery bool
		pct        *int
		charging   *bool
		acOnline   *bool
	)
	for _, e := range entries {
		name := e.Name()
		dir := filepath.Join(base, name)
		typBytes, _ := deps.readFile(filepath.Join(dir, "type"))
		typ := strings.TrimSpace(string(typBytes))
		if typ == "Battery" || strings.HasPrefix(name, "BAT") {
			capBytes, capErr := deps.readFile(filepath.Join(dir, "capacity"))
			if capErr == nil {
				if v, convErr := strconv.Atoi(strings.TrimSpace(string(capBytes))); convErr == nil && v >= 0 && v <= 100 {
					hasBattery = true
					val := v
					pct = &val
				}
			}
			stBytes, stErr := deps.readFile(filepath.Join(dir, "status"))
			if stErr == nil {
				hasBattery = true
				st := strings.TrimSpace(string(stBytes))
				c := strings.EqualFold(st, "Charging") || strings.EqualFold(st, "Full")
				charging = &c
			}
		} else if typ == "Mains" || strings.HasPrefix(name, "AC") || strings.HasPrefix(name, "ADP") {
			if onlineBytes, oErr := deps.readFile(filepath.Join(dir, "online")); oErr == nil {
				on := strings.TrimSpace(string(onlineBytes)) == "1"
				acOnline = &on
			}
		}
	}
	if !hasBattery {
		return nil
	}
	source := "unknown"
	if acOnline != nil {
		if *acOnline {
			source = "ac"
		} else {
			source = "battery"
		}
	} else if charging != nil && *charging {
		source = "ac"
	} else {
		source = "battery"
	}

	sleepState := ""
	if memSleep, err := deps.readFile("/sys/power/mem_sleep"); err == nil {
		for _, tok := range strings.Fields(string(memSleep)) {
			if strings.HasPrefix(tok, "[") && strings.HasSuffix(tok, "]") {
				sleepState = strings.Trim(tok, "[]")
				break
			}
		}
	}

	return &proto.ToolchainPower{
		HasBattery:     true,
		BatteryPercent: pct,
		Charging:       charging,
		Source:         source,
		SleepState:     sleepState,
	}
}

func probeDarwinPower(ctx context.Context, deps toolchainDeps, probeEnv []string) *proto.ToolchainPower {
	cctx, cancel := context.WithTimeout(ctx, toolchainCmdTimeout)
	out, err := deps.run(cctx, probeEnv, "pmset", "-g", "batt")
	cancel()
	if err != nil {
		return nil
	}
	text := string(out)
	if !strings.Contains(text, "InternalBattery") {
		return nil
	}
	p := &proto.ToolchainPower{
		HasBattery: true,
		Source:     "unknown",
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "'ac power'") {
		p.Source = "ac"
	} else if strings.Contains(lower, "'battery power'") {
		p.Source = "battery"
	}
	if m := toolchainPctRe.FindStringSubmatch(text); len(m) > 1 {
		if v, convErr := strconv.Atoi(m[1]); convErr == nil && v >= 0 && v <= 100 {
			p.BatteryPercent = &v
		}
	}
	if strings.Contains(lower, "discharging") {
		c := false
		p.Charging = &c
	} else if strings.Contains(lower, "charging;") || strings.Contains(lower, "charged;") || strings.Contains(lower, "finishing charge") {
		c := true
		p.Charging = &c
	}

	// Best-effort sleep assertion check.
	actx, acancel := context.WithTimeout(ctx, toolchainCmdTimeout)
	aout, aerr := deps.run(actx, probeEnv, "pmset", "-g", "assertions")
	acancel()
	if aerr == nil {
		atext := string(aout)
		if strings.Contains(atext, "PreventUserIdleSystemSleep 1") || strings.Contains(atext, "PreventSystemSleep         1") {
			p.SleepState = "prevent_sleep"
		} else {
			p.SleepState = "normal"
		}
	}
	return p
}

func buildToolchainSearchDirs(deps toolchainDeps) (dirs []string, managers []string) {
	seen := map[string]bool{}
	addDir := func(d string, mustExist bool) {
		d = strings.TrimSpace(d)
		if d == "" || !filepath.IsAbs(d) {
			return
		}
		d = filepath.Clean(d)
		if seen[d] {
			return
		}
		if mustExist {
			st, err := deps.stat(d)
			if err != nil || !st.IsDir() {
				return
			}
		}
		seen[d] = true
		dirs = append(dirs, d)
	}

	for _, d := range filepath.SplitList(deps.getenv("PATH")) {
		addDir(d, false)
	}

	home := deps.home
	if home != "" {
		// Common user-local and language tool directories.
		for _, d := range []string{
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".local", "share", "pnpm"),
		} {
			addDir(d, true)
		}

		// NVM (active NVM_BIN or newest installed node version).
		nvmFound := false
		if nvmBin := strings.TrimSpace(deps.getenv("NVM_BIN")); nvmBin != "" {
			addDir(nvmBin, true)
			nvmFound = true
		}
		nvmDir := strings.TrimSpace(deps.getenv("NVM_DIR"))
		if nvmDir == "" {
			nvmDir = filepath.Join(home, ".nvm")
		}
		if newest := newestSubdirBin(deps, filepath.Join(nvmDir, "versions", "node")); newest != "" {
			addDir(newest, true)
			nvmFound = true
		}
		if nvmFound {
			managers = appendUnique(managers, "nvm")
		}

		// Custom Go installations under ~/.local/go*/bin or ~/sdk/go*/bin.
		if customGo := newestPrefixedSubdirBin(deps, filepath.Join(home, ".local"), "go"); customGo != "" {
			addDir(customGo, true)
		}
		if sdkGo := newestPrefixedSubdirBin(deps, filepath.Join(home, "sdk"), "go"); sdkGo != "" {
			addDir(sdkGo, true)
		}

		// Volta
		voltaBin := filepath.Join(home, ".volta", "bin")
		if toolchainDirExists(deps, voltaBin) {
			addDir(voltaBin, true)
			managers = appendUnique(managers, "volta")
		}

		// fnm
		fnmBin := filepath.Join(home, ".fnm", "current", "bin")
		if toolchainDirExists(deps, fnmBin) {
			addDir(fnmBin, true)
			managers = appendUnique(managers, "fnm")
		}

		// mise
		miseDir := strings.TrimSpace(deps.getenv("MISE_DATA_DIR"))
		if miseDir == "" {
			if xdg := strings.TrimSpace(deps.getenv("XDG_DATA_HOME")); xdg != "" {
				miseDir = filepath.Join(xdg, "mise")
			} else {
				miseDir = filepath.Join(home, ".local", "share", "mise")
			}
		}
		if miseShims := filepath.Join(miseDir, "shims"); toolchainDirExists(deps, miseShims) {
			addDir(miseShims, true)
			managers = appendUnique(managers, "mise")
		}

		// asdf
		asdfDir := strings.TrimSpace(deps.getenv("ASDF_DATA_DIR"))
		if asdfDir == "" {
			asdfDir = filepath.Join(home, ".asdf")
		}
		if asdfShims := filepath.Join(asdfDir, "shims"); toolchainDirExists(deps, asdfShims) {
			addDir(asdfShims, true)
			managers = appendUnique(managers, "asdf")
		}

		// pyenv
		pyenvDir := strings.TrimSpace(deps.getenv("PYENV_ROOT"))
		if pyenvDir == "" {
			pyenvDir = filepath.Join(home, ".pyenv")
		}
		if pyenvShims := filepath.Join(pyenvDir, "shims"); toolchainDirExists(deps, pyenvShims) {
			addDir(pyenvShims, true)
			managers = appendUnique(managers, "pyenv")
		}
	}

	if deps.goos != "windows" {
		if toolchainDirExists(deps, "/opt/homebrew/bin") {
			managers = appendUnique(managers, "homebrew")
		}
		for _, d := range []string{
			"/opt/homebrew/bin",
			"/opt/homebrew/opt/libpq/bin",
			"/usr/local/opt/libpq/bin",
			"/usr/local/go/bin",
			"/usr/local/bin",
			"/usr/bin",
			"/bin",
		} {
			addDir(d, true)
		}
		if pgBin := newestSubdirBin(deps, "/usr/lib/postgresql"); pgBin != "" {
			addDir(pgBin, true)
		}
	}

	return dirs, managers
}

func toolchainDirExists(deps toolchainDeps, p string) bool {
	st, err := deps.stat(p)
	return err == nil && st.IsDir()
}

func newestPrefixedSubdirBin(deps toolchainDeps, parent, prefix string) string {
	entries, err := deps.readDir(parent)
	if err != nil || len(entries) == 0 {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Slice(names, func(i, j int) bool {
		return compareVersionNames(strings.TrimPrefix(names[i], prefix), strings.TrimPrefix(names[j], prefix)) > 0
	})
	for _, name := range names {
		cand := filepath.Join(parent, name, "bin")
		if toolchainDirExists(deps, cand) {
			return cand
		}
	}
	return ""
}

func newestSubdirBin(deps toolchainDeps, parent string) string {
	entries, err := deps.readDir(parent)
	if err != nil || len(entries) == 0 {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Slice(names, func(i, j int) bool {
		return compareVersionNames(names[i], names[j]) > 0
	})
	for _, name := range names {
		cand := filepath.Join(parent, name, "bin")
		if toolchainDirExists(deps, cand) {
			return cand
		}
	}
	return ""
}

func compareVersionNames(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	n := max(len(pa), len(pb))
	for i := 0; i < n; i++ {
		var va, vb int
		if i < len(pa) {
			va, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			vb, _ = strconv.Atoi(pb[i])
		}
		if va != vb {
			if va > vb {
				return 1
			}
			return -1
		}
	}
	return strings.Compare(a, b)
}

func buildProbeEnv(deps toolchainDeps, searchDirs []string) []string {
	env := []string{
		"PATH=" + strings.Join(searchDirs, string(os.PathListSeparator)),
	}
	if deps.home != "" {
		env = append(env, "HOME="+deps.home)
	}
	for _, k := range []string{"USER", "LOGNAME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "GH_CONFIG_DIR", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "SYSTEMROOT", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		if v := deps.getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func lookupInDirs(name string, dirs []string) string {
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		if toolchainIsExecutable(name) {
			return name
		}
		return ""
	}
	exts := []string{""}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		exts = []string{".exe", ".cmd", ".bat", ""}
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			cand := filepath.Join(dir, name+ext)
			if toolchainIsExecutable(cand) {
				return cand
			}
		}
	}
	return ""
}

func toolchainIsExecutable(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return st.Mode().Perm()&0o111 != 0
}

func runToolchainCmd(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if len(out) > toolchainMaxOutputBytes {
		out = out[:toolchainMaxOutputBytes]
	}
	return out, err
}

func dialToolchainUnix(ctx context.Context, sock string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func extractToolchainVersion(spec toolchainSpec, out []byte) string {
	re := spec.version
	if re == nil {
		re = toolchainDottedRe
	}
	if m := re.FindStringSubmatch(string(out)); len(m) > 1 {
		return m[1]
	}
	return ""
}

func extractDottedVersion(out []byte) string {
	if m := toolchainDottedRe.FindStringSubmatch(string(out)); len(m) > 1 {
		return m[1]
	}
	return ""
}

// redactToolchainText removes tokens, credentials, and secret-like key/value
// pairs from diagnostic notes and bounds the result length.
func redactToolchainText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = toolchainSecretRe.ReplaceAllString(s, "[REDACTED]")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > toolchainMaxNoteLen {
		s = s[:toolchainMaxNoteLen-1] + "…"
	}
	return s
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func findEntryByID(list []proto.ToolchainEntry, id string) *proto.ToolchainEntry {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

func appendUnique(list []string, items ...string) []string {
	for _, it := range items {
		if it != "" && !slices.Contains(list, it) {
			list = append(list, it)
		}
	}
	return list
}

func hasAnyEnv(getenv func(string) string, keys ...string) bool {
	for _, k := range keys {
		if strings.TrimSpace(getenv(k)) != "" {
			return true
		}
	}
	return false
}
