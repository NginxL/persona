package node

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/sysmon"
)

const (
	storageCodeBadRequest        = "bad_request"
	storageCodeConfirmNeeded     = "confirmation_required"
	storageDefaultTimeout        = 10 * time.Second
	storageMaxTimeout            = 20 * time.Second
	storageMinTimeout            = 500 * time.Millisecond
	storageDefaultMaxDepth       = 4
	storageMaxDepthLimit         = 8
	storageMaxWalkEntries        = 40_000
	storageDockerCmdTimeout      = 5 * time.Second
	storageDockerPruneTimeout    = 45 * time.Second
	storageGitCmdTimeout         = 5 * time.Second
	storageProcCmdTimeout        = 5 * time.Second
	storageCleanupDefaultTimeout = 90 * time.Second // strictly shorter than hub's 120s cleanup RPC timeout
	storageStaleWorktreeAge      = 7 * 24 * time.Hour
	storageMaxCategoryItems      = 12
	storageMaxWorktreeScans      = 48
)

var errStorageOutsideRoot = errors.New("path is outside allowed root")

type storageProcEntry struct {
	comm         string
	args         []string
	rawCmdline   string // unsplit raw command line for OSes whose ps joins argv with spaces (macOS)
	cmdlineKnown bool
}

type storageProcList struct {
	procs         []storageProcEntry
	cmdlinesKnown bool
}

type storageProcSnapshot struct {
	procs         []storageProcEntry
	known         bool
	cmdlinesKnown bool
	err           error
}

type storageDeps struct {
	goos           string
	home           string
	dataDir        string
	getenv         func(string) string
	now            func() time.Time
	mounts         func() ([]proto.MountStat, bool)
	available      func(string) (int64, bool)
	runCmd         func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath       func(string) (string, error)
	runningProcs   func(ctx context.Context) (storageProcList, error)
	activeLogFiles func(ctx context.Context, dirs []string) map[string]bool
	cleanupTimeout time.Duration
}

func (n *Node) defaultStorageDeps() storageDeps {
	home := n.home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	dir := n.dir
	if dir == "" && home != "" {
		dir = filepath.Join(home, ".persona")
	}
	runCmd := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	goos := runtime.GOOS
	return storageDeps{
		goos:      goos,
		home:      home,
		dataDir:   dir,
		getenv:    os.Getenv,
		now:       time.Now,
		mounts:    sysmon.StorageMounts,
		available: sysmon.AvailableBytes,
		runCmd:    runCmd,
		lookPath:  exec.LookPath,
		runningProcs: func(ctx context.Context) (storageProcList, error) {
			return readRunningProcsForOS(ctx, goos, runCmd)
		},
		activeLogFiles: func(ctx context.Context, dirs []string) map[string]bool {
			return n.collectActiveStorageLogFiles(ctx, goos, dirs)
		},
		cleanupTimeout: storageCleanupDefaultTimeout,
	}
}

// machineStorage handles machine.storage: fast read-only per-mount capacity and inode stats (statfs only, no du).
func (n *Node) machineStorage(ctx context.Context, _ json.RawMessage) (any, error) {
	return collectMachineStorage(n.defaultStorageDeps()), nil
}

// machineStorageHotspots handles machine.storage.hotspots: explicit on-demand bounded hotspot storage scan.
func (n *Node) machineStorageHotspots(ctx context.Context, params json.RawMessage) (any, error) {
	var p proto.StorageHotspotsParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpc.Error{Code: storageCodeBadRequest, Message: err.Error()}
		}
	}
	return collectMachineStorageHotspots(ctx, n.defaultStorageDeps(), p), nil
}

// machineStorageCleanup handles machine.storage.cleanup: preview (dry_run) or confirmation-gated selective cleanup.
func (n *Node) machineStorageCleanup(ctx context.Context, params json.RawMessage) (any, error) {
	var p proto.StorageCleanupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpc.Error{Code: storageCodeBadRequest, Message: err.Error()}
	}
	return runMachineStorageCleanup(ctx, n.defaultStorageDeps(), p)
}

func collectMachineStorage(deps storageDeps) proto.MachineStorageReport {
	start := deps.now()
	mounts, supported := deps.mounts()
	if mounts == nil {
		mounts = []proto.MountStat{}
	}
	return proto.MachineStorageReport{
		At:          start.Unix(),
		OS:          deps.goos,
		Supported:   supported,
		Mounts:      mounts,
		CollectedMS: max(deps.now().Sub(start).Milliseconds(), 0),
	}
}

func clampStorageHotspotsLimits(p proto.StorageHotspotsParams) (time.Duration, int) {
	timeout := storageDefaultTimeout
	if p.TimeoutMS > 0 {
		timeout = time.Duration(p.TimeoutMS) * time.Millisecond
		if timeout < storageMinTimeout {
			timeout = storageMinTimeout
		}
		if timeout > storageMaxTimeout {
			timeout = storageMaxTimeout
		}
	}
	maxDepth := storageDefaultMaxDepth
	if p.MaxDepth > 0 {
		maxDepth = min(p.MaxDepth, storageMaxDepthLimit)
	}
	return timeout, maxDepth
}

func collectMachineStorageHotspots(ctx context.Context, deps storageDeps, params proto.StorageHotspotsParams) proto.MachineStorageHotspotsReport {
	start := deps.now()
	timeout, maxDepth := clampStorageHotspotsLimits(params)
	scanCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	procs := readStorageProcs(scanCtx, deps)
	partial := false

	categories := make([]proto.StorageHotspotCategory, 0, 9)

	// 1. Stale Git worktrees
	wtCat, wtPartial := scanGitWorktreesHotspot(scanCtx, deps, maxDepth)
	partial = partial || wtPartial
	categories = append(categories, wtCat)

	// 2. Docker system df
	dockerCat := scanDockerHotspot(scanCtx, deps)
	categories = append(categories, dockerCat)

	// 3. ~/.cache (or ~/Library/Caches)
	ucCat, ucPartial := scanUserCacheHotspot(scanCtx, deps, maxDepth)
	partial = partial || ucPartial
	categories = append(categories, ucCat)

	// 4. Toolchain caches (Go build, Go mod, npm, pip) and Persona logs/backups
	for _, spec := range storageDirTargets(deps) {
		cat, p := scanDirTargetHotspot(scanCtx, deps, spec, maxDepth, procs)
		partial = partial || p
		categories = append(categories, cat)
	}

	slices.SortFunc(categories, func(a, b proto.StorageHotspotCategory) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.ID, b.ID))
	})

	return proto.MachineStorageHotspotsReport{
		At:         start.Unix(),
		OS:         deps.goos,
		Partial:    partial || scanCtx.Err() != nil,
		DurationMS: max(deps.now().Sub(start).Milliseconds(), 0),
		Categories: categories,
	}
}

type storageDirTarget struct {
	categoryID      string
	targetID        string
	roots           []string // allowed parent roots for guard check
	paths           []string // one or more target directories
	users           []string // toolchain executables that mark the cache in_use
	action          string
	rotatedLogsOnly bool // preserve active/current & open log files; only reclaim rotated/archived logs
}

func storageDirTargets(deps storageDeps) []storageDirTarget {
	home := deps.home
	dataDir := deps.dataDir
	env := deps.getenv
	cacheBase := storageUserCacheDir(deps.goos, home, env)

	goBuild := env("GOCACHE")
	if goBuild == "" && cacheBase != "" {
		goBuild = filepath.Join(cacheBase, "go-build")
	}
	goMod := env("GOMODCACHE")
	if goMod == "" {
		if gopath := env("GOPATH"); gopath != "" {
			goMod = filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod")
		} else if home != "" {
			goMod = filepath.Join(home, "go", "pkg", "mod")
		}
	}
	npmCache := resolveNpmCacheDir(home, env("npm_config_cache"))
	pipCache := env("PIP_CACHE_DIR")
	if pipCache == "" && cacheBase != "" {
		pipCache = filepath.Join(cacheBase, "pip")
	}

	var logPaths, backupPaths []string
	var personaRoots []string
	if dataDir != "" {
		personaRoots = append(personaRoots, dataDir)
		logPaths = append(logPaths, filepath.Join(dataDir, "logs"))
		backupPaths = append(backupPaths,
			filepath.Join(dataDir, "backups"),
			filepath.Join(dataDir, "update", "development-backups"),
		)
	}
	if home != "" {
		personaRoots = append(personaRoots, home)
	}

	homeRoots := []string{}
	if home != "" {
		homeRoots = append(homeRoots, home)
	}

	return []storageDirTarget{
		{
			categoryID: proto.StorageCategoryGoBuild,
			targetID:   proto.StorageTargetGoBuild,
			roots:      homeRoots,
			paths:      nonEmptyPaths(goBuild),
			users:      []string{"go", "gopls", "compile"},
			action:     "empty_dir_contents",
		},
		{
			categoryID: proto.StorageCategoryGoMod,
			targetID:   proto.StorageTargetGoMod,
			roots:      homeRoots,
			paths:      nonEmptyPaths(goMod),
			users:      []string{"go", "gopls"},
			action:     "empty_dir_contents",
		},
		{
			categoryID: proto.StorageCategoryNpmCache,
			targetID:   proto.StorageTargetNpmCache,
			roots:      homeRoots,
			paths:      nonEmptyPaths(npmCache),
			users:      []string{"npm", "npx"},
			action:     "empty_dir_contents",
		},
		{
			categoryID: proto.StorageCategoryPipCache,
			targetID:   proto.StorageTargetPipCache,
			roots:      homeRoots,
			paths:      nonEmptyPaths(pipCache),
			users:      []string{"pip", "python"},
			action:     "empty_dir_contents",
		},
		{
			categoryID:      proto.StorageCategoryPersonaLogs,
			targetID:        proto.StorageTargetPersonaLogs,
			roots:           personaRoots,
			paths:           logPaths,
			action:          "remove_rotated_logs",
			rotatedLogsOnly: true,
		},
		{
			categoryID: proto.StorageCategoryPersonaBackups,
			targetID:   proto.StorageTargetPersonaBackups,
			roots:      personaRoots,
			paths:      backupPaths,
			action:     "empty_dir_contents",
		},
	}
}

func resolveNpmCacheDir(home, envCache string) string {
	if v := strings.TrimSpace(envCache); v != "" {
		clean := filepath.Clean(v)
		if !filepath.IsAbs(clean) {
			return ""
		}
		if filepath.Base(clean) == "_cacache" {
			return clean
		}
		cacache := filepath.Join(clean, "_cacache")
		if _, err := os.Lstat(cacache); err == nil {
			// Return `_cacache` whenever it exists (even if symlink or non-directory) so
			// `guardStorageDir` accepts real directories and rejects symlinks/files instead
			// of widening cleanup to the parent directory.
			return cacache
		} else if !errors.Is(err, fs.ErrNotExist) {
			// Do not fall back to the parent directory on non-NotExist stat errors.
			return cacache
		}
		return clean
	}
	if home != "" {
		return filepath.Join(home, ".npm", "_cacache")
	}
	return ""
}

func nonEmptyPaths(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if p != "" && filepath.IsAbs(p) {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}

func storageUserCacheDir(goos, home string, env func(string) string) string {
	if home == "" {
		return ""
	}
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Caches")
	}
	if v := env("XDG_CACHE_HOME"); filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	return filepath.Join(home, ".cache")
}

func scanUserCacheHotspot(ctx context.Context, deps storageDeps, maxDepth int) (proto.StorageHotspotCategory, bool) {
	dir := storageUserCacheDir(deps.goos, deps.home, deps.getenv)
	cat := proto.StorageHotspotCategory{
		ID:        proto.StorageCategoryUserCache,
		Path:      storageTilde(deps.home, dir),
		Status:    proto.StorageStatusEmpty,
		Cleanable: false,
		Note:      "advisory",
	}
	if dir == "" {
		cat.Status = proto.StorageStatusUnavailable
		return cat, false
	}
	realDir, err := guardStorageDir(deps.home, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cat, false
		}
		cat.Status = proto.StorageStatusUnavailable
		return cat, false
	}
	entries, err := os.ReadDir(realDir)
	if err != nil {
		cat.Status = proto.StorageStatusUnavailable
		return cat, false
	}
	var total int64
	partial := false
	items := make([]proto.StorageHotspotItem, 0, len(entries))
	budget := storageMaxWalkEntries
	for _, e := range entries {
		if ctx.Err() != nil {
			partial = true
			break
		}
		childPath := filepath.Join(realDir, e.Name())
		sz, _, done := boundedDirSize(ctx, childPath, max(maxDepth-1, 1), &budget)
		if !done {
			partial = true
		}
		total += sz
		if sz > 0 {
			items = append(items, proto.StorageHotspotItem{
				Label: e.Name(),
				Path:  storageTilde(deps.home, filepath.Join(dir, e.Name())),
				Bytes: sz,
			})
		}
	}
	slices.SortFunc(items, func(a, b proto.StorageHotspotItem) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Label, b.Label))
	})
	cat.ItemCount = len(items)
	if len(items) > storageMaxCategoryItems {
		items = items[:storageMaxCategoryItems]
	}
	cat.Items = items
	cat.Bytes = total
	if partial {
		cat.Status = proto.StorageStatusPartial
	} else if total > 0 {
		cat.Status = proto.StorageStatusOK
	}
	return cat, partial
}

func scanDirTargetHotspot(ctx context.Context, deps storageDeps, spec storageDirTarget, maxDepth int, procs storageProcSnapshot) (proto.StorageHotspotCategory, bool) {
	inUse, procsUnknown := storageToolchainInUse(spec.users, procs)
	cat := proto.StorageHotspotCategory{
		ID:              spec.categoryID,
		Status:          proto.StorageStatusEmpty,
		Cleanable:       true,
		CleanupTargetID: spec.targetID,
		InUse:           inUse,
	}
	if procsUnknown {
		cat.Note = "process_list_unavailable"
	}
	if len(spec.paths) > 0 {
		cat.Path = storageTilde(deps.home, spec.paths[0])
	}
	var guarded []string
	var guardedOrig []string
	for _, p := range spec.paths {
		realDir, err := guardStorageDirAnyRoot(spec.roots, p)
		if err != nil {
			continue
		}
		guarded = append(guarded, realDir)
		guardedOrig = append(guardedOrig, p)
	}
	var activeLogs map[string]bool
	if spec.rotatedLogsOnly && len(guarded) > 0 && deps.activeLogFiles != nil {
		activeLogs = deps.activeLogFiles(ctx, guarded)
	}
	var total int64
	var reclaimable int64
	var protectedCount int
	var items []proto.StorageHotspotItem
	partial := false
	budget := storageMaxWalkEntries
	for i, realDir := range guarded {
		p := guardedOrig[i]
		var sz, rec int64
		var count int
		var done bool
		if spec.rotatedLogsOnly {
			var prot int
			sz, rec, count, _, prot, done = collectRotatedLogFiles(ctx, realDir, maxDepth, &budget, activeLogs)
			protectedCount += prot
		} else {
			sz, count, done = boundedDirSize(ctx, realDir, maxDepth, &budget)
			rec = sz
		}
		if !done {
			partial = true
		}
		total += sz
		reclaimable += rec
		cat.ItemCount += count
		if sz > 0 {
			items = append(items, proto.StorageHotspotItem{
				Label:            filepath.Base(p),
				Path:             storageTilde(deps.home, p),
				Bytes:            sz,
				ReclaimableBytes: rec,
			})
		}
	}
	cat.Bytes = total
	cat.ReclaimableBytes = reclaimable
	if len(items) > 1 {
		cat.Items = items
	}
	if partial {
		cat.Status = proto.StorageStatusPartial
	} else if len(guarded) > 0 && total > 0 {
		cat.Status = proto.StorageStatusOK
	} else {
		cat.Status = proto.StorageStatusEmpty
		cat.Cleanable = false
	}
	if spec.rotatedLogsOnly && total > 0 && reclaimable == 0 && protectedCount > 0 {
		cat.Cleanable = false
		cat.InUse = true
		cat.Note = "active_logs_preserved"
	}
	return cat, partial
}

// scanGitWorktreesHotspot inspects Persona/herdr worktree roots for large or stale Git worktrees.
func scanGitWorktreesHotspot(ctx context.Context, deps storageDeps, maxDepth int) (proto.StorageHotspotCategory, bool) {
	roots := worktreeScanRoots(deps)
	cat := proto.StorageHotspotCategory{
		ID:     proto.StorageCategoryGitWorktrees,
		Status: proto.StorageStatusEmpty,
	}
	if len(roots) > 0 {
		cat.Path = storageTilde(deps.home, roots[0])
	}
	var items []proto.StorageHotspotItem
	var totalBytes int64
	var staleBytes int64
	prunableRepos := map[string]bool{}
	partial := false
	budget := storageMaxWalkEntries
	scanned := 0
	cutoff := deps.now().Add(-storageStaleWorktreeAge)

	for _, root := range roots {
		realRoot, err := guardStorageDirAnyRoot([]string{deps.home, deps.dataDir}, root)
		if err != nil {
			continue
		}
		candidates := discoverWorktreeDirs(realRoot, storageMaxWorktreeScans-scanned)
		for _, wt := range candidates {
			if ctx.Err() != nil {
				partial = true
				break
			}
			scanned++
			sz, _, done := boundedDirSize(ctx, wt.path, maxDepth, &budget)
			if !done {
				partial = true
			}
			totalBytes += sz
			stale := wt.broken || wt.modTime.Before(cutoff)
			if stale {
				staleBytes += sz
			}
			if wt.repoRoot != "" {
				prunableRepos[wt.repoRoot] = true
			}
			detail := "active"
			if wt.broken {
				detail = "broken"
			} else if stale {
				detail = "stale"
			}
			items = append(items, proto.StorageHotspotItem{
				Label:  wt.label,
				Path:   storageTilde(deps.home, wt.path),
				Bytes:  sz,
				Stale:  stale,
				Detail: detail,
			})
		}
	}

	slices.SortFunc(items, func(a, b proto.StorageHotspotItem) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Path, b.Path))
	})
	cat.ItemCount = len(items)
	if len(items) > storageMaxCategoryItems {
		items = items[:storageMaxCategoryItems]
	}
	cat.Items = items
	cat.Bytes = totalBytes
	cat.ReclaimableBytes = staleBytes
	if len(prunableRepos) > 0 {
		cat.Cleanable = true
		cat.CleanupTargetID = proto.StorageTargetGitWorktreesPrune
		cat.Note = "prune_metadata_only"
	}
	if partial {
		cat.Status = proto.StorageStatusPartial
	} else if len(items) > 0 {
		cat.Status = proto.StorageStatusOK
	}
	return cat, partial
}

type discoveredWorktree struct {
	label    string
	path     string
	repoRoot string
	broken   bool
	modTime  time.Time
}

func worktreeScanRoots(deps storageDeps) []string {
	var candidates []string
	if deps.home != "" {
		candidates = append(candidates,
			filepath.Join(deps.home, ".herdr", "worktrees"),
			filepath.Join(deps.home, "worktrees"),
		)
	}
	if deps.dataDir != "" {
		candidates = append(candidates, filepath.Join(deps.dataDir, "worktrees"))
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		clean := filepath.Clean(c)
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	return out
}

// discoverWorktreeDirs looks 1 or 2 levels deep under a worktrees root (<root>/<wt> or <root>/<repo>/<wt>).
func discoverWorktreeDirs(root string, limit int) []discoveredWorktree {
	if limit <= 0 {
		return nil
	}
	topEntries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []discoveredWorktree
	inspect := func(dir, label string) bool {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return false
		}
		gitPath := filepath.Join(dir, ".git")
		gitInfo, err := os.Lstat(gitPath)
		if err != nil {
			return false
		}
		repoRoot, broken := parseWorktreeGitPointer(dir, gitPath, gitInfo)
		out = append(out, discoveredWorktree{
			label:    label,
			path:     dir,
			repoRoot: repoRoot,
			broken:   broken,
			modTime:  info.ModTime(),
		})
		return true
	}
	for _, e := range topEntries {
		if len(out) >= limit {
			break
		}
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		p := filepath.Join(root, e.Name())
		if inspect(p, e.Name()) {
			continue
		}
		subEntries, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if len(out) >= limit {
				break
			}
			if !sub.IsDir() || sub.Type()&fs.ModeSymlink != 0 {
				continue
			}
			inspect(filepath.Join(p, sub.Name()), e.Name()+"/"+sub.Name())
		}
	}
	return out
}

func parseWorktreeGitPointer(wtDir, gitPath string, info fs.FileInfo) (repoRoot string, broken bool) {
	if info.IsDir() {
		return "", false
	}
	b, err := os.ReadFile(gitPath)
	if err != nil {
		return "", true
	}
	line := strings.TrimSpace(string(b))
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", true
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Clean(filepath.Join(wtDir, target))
	}
	if _, err := os.Stat(target); err != nil {
		broken = true
	}
	// Resolve owning repo from .../.git/worktrees/<name>
	cleanTarget := filepath.ToSlash(filepath.Clean(target))
	if idx := strings.LastIndex(cleanTarget, "/.git/worktrees/"); idx > 0 {
		repoRoot = filepath.FromSlash(cleanTarget[:idx])
	}
	return repoRoot, broken
}

// scanDockerHotspot runs `docker system df --format json` with a strict timeout.
func scanDockerHotspot(ctx context.Context, deps storageDeps) proto.StorageHotspotCategory {
	cat := proto.StorageHotspotCategory{
		ID:     proto.StorageCategoryDocker,
		Status: proto.StorageStatusUnavailable,
	}
	if _, err := deps.lookPath("docker"); err != nil {
		cat.Note = "docker_not_installed"
		return cat
	}
	cmdCtx, cancel := context.WithTimeout(ctx, storageDockerCmdTimeout)
	defer cancel()
	out, err := deps.runCmd(cmdCtx, "docker", "system", "df", "--format", "json")
	if err != nil {
		cat.Note = "docker_unavailable"
		return cat
	}
	items, total, reclaimable, buildCacheReclaimable, ok := parseDockerSystemDF(out)
	if !ok {
		cat.Note = "docker_parse_failed"
		return cat
	}
	cat.Items = items
	cat.ItemCount = len(items)
	cat.Bytes = total
	cat.ReclaimableBytes = reclaimable
	if total > 0 {
		cat.Status = proto.StorageStatusOK
	} else {
		cat.Status = proto.StorageStatusEmpty
	}
	if buildCacheReclaimable > 0 {
		cat.Cleanable = true
		cat.CleanupTargetID = proto.StorageTargetDockerBuildCache
	}
	return cat
}

type dockerDFRow struct {
	Type        string `json:"Type"`
	TotalCount  string `json:"TotalCount"`
	Active      string `json:"Active"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

func parseDockerSystemDF(raw []byte) (items []proto.StorageHotspotItem, total, reclaimable, buildCacheReclaimable int64, ok bool) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var row dockerDFRow
		if err := json.Unmarshal([]byte(line), &row); err != nil || row.Type == "" {
			continue
		}
		ok = true
		sz := parseHumanBytes(row.Size)
		rec := parseHumanBytes(row.Reclaimable)
		total += sz
		reclaimable += rec
		if strings.EqualFold(row.Type, "Build Cache") {
			buildCacheReclaimable += rec
		}
		detail := ""
		if row.TotalCount != "" {
			detail = fmt.Sprintf("%s total, %s active", row.TotalCount, cmp.Or(row.Active, "0"))
		}
		items = append(items, proto.StorageHotspotItem{
			Label:            row.Type,
			Bytes:            sz,
			ReclaimableBytes: rec,
			Detail:           detail,
		})
	}
	return items, total, reclaimable, buildCacheReclaimable, ok
}

// parseHumanBytes parses Docker-style sizes like "1.23GB", "12.5MB (100%)", "512kB", "4.2GiB", "0B".
func parseHumanBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '('); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	if s == "" || s == "0" || s == "0B" {
		return 0
	}
	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0
	}
	val, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || math.IsNaN(val) || math.IsInf(val, 0) || val < 0 {
		return 0
	}
	unit := strings.TrimSpace(s[i:])
	var mult float64
	switch strings.ToLower(unit) {
	case "", "b":
		mult = 1
	case "kb":
		mult = 1e3
	case "mb":
		mult = 1e6
	case "gb":
		mult = 1e9
	case "tb":
		mult = 1e12
	case "kib":
		mult = 1 << 10
	case "mib":
		mult = 1 << 20
	case "gib":
		mult = 1 << 30
	case "tib":
		mult = 1 << 40
	default:
		return 0
	}
	return int64(math.Round(val * mult))
}

func runMachineStorageCleanup(ctx context.Context, deps storageDeps, params proto.StorageCleanupParams) (proto.StorageCleanupResult, error) {
	if len(params.Targets) == 0 {
		return proto.StorageCleanupResult{}, &rpc.Error{Code: storageCodeBadRequest, Message: "no cleanup targets specified"}
	}
	for _, id := range params.Targets {
		if !slices.Contains(proto.StorageCleanupTargetIDs, id) {
			return proto.StorageCleanupResult{}, &rpc.Error{Code: storageCodeBadRequest, Message: fmt.Sprintf("unknown cleanup target %q", id)}
		}
	}
	if !params.DryRun && !params.Confirm {
		return proto.StorageCleanupResult{}, &rpc.Error{Code: storageCodeConfirmNeeded, Message: "selective cleanup requires dry_run=true or confirm=true"}
	}

	overallTimeout := deps.cleanupTimeout
	if overallTimeout <= 0 {
		overallTimeout = storageCleanupDefaultTimeout
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(ctx, overallTimeout)
	defer cancelCleanup()

	dirTargets := map[string]storageDirTarget{}
	for _, spec := range storageDirTargets(deps) {
		dirTargets[spec.targetID] = spec
	}
	procs := readStorageProcs(cleanupCtx, deps)

	var before int64
	if !params.DryRun && deps.home != "" {
		before, _ = deps.available(deps.home)
	}

	out := proto.StorageCleanupResult{
		DryRun:          params.DryRun,
		AvailableBefore: before,
		Items:           make([]proto.StorageCleanupItem, 0, len(params.Targets)),
	}

	seen := map[string]bool{}
	var estimated int64
	var reportedCleaned int64

	for _, id := range params.Targets {
		if seen[id] {
			continue
		}
		seen[id] = true
		if cleanupCtx.Err() != nil {
			out.Items = append(out.Items, proto.StorageCleanupItem{
				TargetID: id,
				Status:   proto.StorageCleanupSkipped,
				Error:    "cleanup deadline exceeded",
			})
			continue
		}
		var item proto.StorageCleanupItem
		switch id {
		case proto.StorageTargetDockerBuildCache:
			item = cleanupDockerBuildCache(cleanupCtx, deps, params.DryRun)
		case proto.StorageTargetGitWorktreesPrune:
			item = cleanupGitWorktreesPrune(cleanupCtx, deps, params.DryRun)
		default:
			spec, ok := dirTargets[id]
			if !ok {
				item = proto.StorageCleanupItem{TargetID: id, Status: proto.StorageCleanupMissing}
			} else {
				item = cleanupDirTarget(cleanupCtx, deps, spec, procs, params.DryRun)
			}
		}
		if item.Status == proto.StorageCleanupPreview || item.Status == proto.StorageCleanupCleaned {
			estimated += item.Bytes
		}
		if item.Status == proto.StorageCleanupCleaned {
			reportedCleaned += item.Bytes
		}
		out.Items = append(out.Items, item)
	}

	out.EstimatedBytes = estimated
	if !params.DryRun {
		if deps.home != "" {
			after, ok := deps.available(deps.home)
			if ok {
				out.AvailableAfter = after
				out.FreedBytes = max(after-before, 0)
			}
		}
		if out.FreedBytes == 0 && reportedCleaned > 0 {
			out.FreedBytes = reportedCleaned
		}
	}
	return out, nil
}

func cleanupDirTarget(ctx context.Context, deps storageDeps, spec storageDirTarget, procs storageProcSnapshot, dryRun bool) proto.StorageCleanupItem {
	item := proto.StorageCleanupItem{
		TargetID: spec.targetID,
		Action:   spec.action,
		Status:   proto.StorageCleanupMissing,
	}
	if len(spec.paths) > 0 {
		item.Path = storageTilde(deps.home, spec.paths[0])
	}
	var guarded []string
	for _, p := range spec.paths {
		realDir, err := guardStorageDirAnyRoot(spec.roots, p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			item.Status = proto.StorageCleanupFailed
			item.Error = sanitizeStorageErr(deps.home, err)
			return item
		default:
			guarded = append(guarded, realDir)
		}
	}
	if len(guarded) == 0 {
		return item
	}
	if spec.rotatedLogsOnly {
		return cleanupRotatedLogDirs(ctx, deps, guarded, item, dryRun)
	}
	sizeCtx, cancel := context.WithTimeout(ctx, storageDefaultTimeout)
	var totalBytes int64
	budget := storageMaxWalkEntries
	nonEmpty := false
	for _, d := range guarded {
		if !isStorageDirEmpty(d) {
			nonEmpty = true
		}
		sz, _, _ := boundedDirSize(sizeCtx, d, storageMaxDepthLimit, &budget)
		totalBytes += sz
	}
	cancel()
	if !nonEmpty && totalBytes == 0 {
		return item
	}
	item.Bytes = totalBytes
	if inUse, procsUnknown := storageToolchainInUse(spec.users, procs); inUse {
		item.Status = proto.StorageCleanupInUse
		if procsUnknown {
			item.Error = "process list unavailable"
		}
		return item
	}
	if dryRun {
		item.Status = proto.StorageCleanupPreview
		return item
	}
	for _, d := range guarded {
		if err := emptyStorageDirContents(ctx, d); err != nil {
			item.Status = proto.StorageCleanupFailed
			item.Error = sanitizeStorageErr(deps.home, err)
			return item
		}
	}
	item.Status = proto.StorageCleanupCleaned
	return item
}

func cleanupRotatedLogDirs(ctx context.Context, deps storageDeps, guarded []string, item proto.StorageCleanupItem, dryRun bool) proto.StorageCleanupItem {
	var activeLogs map[string]bool
	if deps.activeLogFiles != nil {
		activeLogs = deps.activeLogFiles(ctx, guarded)
	}
	sizeCtx, cancel := context.WithTimeout(ctx, storageDefaultTimeout)
	var totalBytes, reclaimableBytes int64
	var filesToRemove []string
	protectedCount := 0
	budget := storageMaxWalkEntries
	for _, d := range guarded {
		sz, rec, _, cleanable, prot, _ := collectRotatedLogFiles(sizeCtx, d, storageMaxDepthLimit, &budget, activeLogs)
		totalBytes += sz
		reclaimableBytes += rec
		filesToRemove = append(filesToRemove, cleanable...)
		protectedCount += prot
	}
	cancel()
	if len(filesToRemove) == 0 {
		if protectedCount > 0 || totalBytes > 0 {
			item.Status = proto.StorageCleanupInUse
			item.Error = "only active log files present"
		}
		return item
	}
	item.Bytes = reclaimableBytes
	if dryRun {
		item.Status = proto.StorageCleanupPreview
		return item
	}
	var firstErr error
	failed := 0
	for _, p := range filesToRemove {
		if err := ctx.Err(); err != nil {
			item.Status = proto.StorageCleanupFailed
			item.Error = sanitizeStorageErr(deps.home, err)
			return item
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			firstErr = cmp.Or(firstErr, err)
			failed++
		}
	}
	if failed > 0 {
		if failed > 1 {
			firstErr = fmt.Errorf("%w (and %d more)", firstErr, failed-1)
		}
		item.Status = proto.StorageCleanupFailed
		item.Error = sanitizeStorageErr(deps.home, firstErr)
		return item
	}
	item.Status = proto.StorageCleanupCleaned
	return item
}

func cleanupDockerBuildCache(ctx context.Context, deps storageDeps, dryRun bool) proto.StorageCleanupItem {
	item := proto.StorageCleanupItem{
		TargetID: proto.StorageTargetDockerBuildCache,
		Action:   "docker builder prune -f",
		Status:   proto.StorageCleanupMissing,
	}
	if _, err := deps.lookPath("docker"); err != nil {
		item.Status = proto.StorageCleanupSkipped
		item.Error = "docker not installed"
		return item
	}
	dfCtx, cancelDF := context.WithTimeout(ctx, storageDockerCmdTimeout)
	raw, err := deps.runCmd(dfCtx, "docker", "system", "df", "--format", "json")
	cancelDF()
	if err != nil {
		item.Status = proto.StorageCleanupSkipped
		item.Error = "docker daemon unavailable"
		return item
	}
	_, _, _, buildReclaimable, ok := parseDockerSystemDF(raw)
	if !ok || buildReclaimable == 0 {
		return item
	}
	item.Bytes = buildReclaimable
	if dryRun {
		item.Status = proto.StorageCleanupPreview
		return item
	}
	pruneCtx, cancelPrune := context.WithTimeout(ctx, storageDockerPruneTimeout)
	out, err := deps.runCmd(pruneCtx, "docker", "builder", "prune", "-f")
	cancelPrune()
	if err != nil {
		item.Status = proto.StorageCleanupFailed
		item.Error = "docker builder prune failed"
		return item
	}
	if reclaimed := parseDockerReclaimedLine(out); reclaimed > 0 {
		item.Bytes = reclaimed
	}
	item.Status = proto.StorageCleanupCleaned
	return item
}

func parseDockerReclaimedLine(out []byte) int64 {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "Total reclaimed space:"); ok {
			return parseHumanBytes(strings.TrimSpace(rest))
		}
	}
	return 0
}

func cleanupGitWorktreesPrune(ctx context.Context, deps storageDeps, dryRun bool) proto.StorageCleanupItem {
	item := proto.StorageCleanupItem{
		TargetID: proto.StorageTargetGitWorktreesPrune,
		Action:   "git worktree prune",
		Status:   proto.StorageCleanupMissing,
	}
	if _, err := deps.lookPath("git"); err != nil {
		item.Status = proto.StorageCleanupSkipped
		item.Error = "git not installed"
		return item
	}
	repoSet := map[string]bool{}
	for _, root := range worktreeScanRoots(deps) {
		realRoot, err := guardStorageDirAnyRoot([]string{deps.home, deps.dataDir}, root)
		if err != nil {
			continue
		}
		for _, wt := range discoverWorktreeDirs(realRoot, storageMaxWorktreeScans) {
			if wt.repoRoot != "" {
				repoSet[wt.repoRoot] = true
			}
		}
	}
	if len(repoSet) == 0 {
		return item
	}
	repos := make([]string, 0, len(repoSet))
	for r := range repoSet {
		repos = append(repos, r)
	}
	slices.Sort(repos)
	item.Path = storageTilde(deps.home, repos[0])
	if dryRun {
		item.Status = proto.StorageCleanupPreview
		return item
	}
	for _, repo := range repos {
		cmdCtx, cancel := context.WithTimeout(ctx, storageGitCmdTimeout)
		_, err := deps.runCmd(cmdCtx, "git", "-C", repo, "worktree", "prune")
		cancel()
		if err != nil {
			item.Status = proto.StorageCleanupFailed
			item.Error = sanitizeStorageErr(deps.home, err)
			return item
		}
	}
	item.Status = proto.StorageCleanupCleaned
	return item
}

// boundedDirSize walks root up to maxDepth and entry budget, never following symlinks.
func boundedDirSize(ctx context.Context, root string, maxDepth int, budget *int) (bytes int64, entries int, complete bool) {
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&fs.ModeSymlink != 0 {
		return 0, 0, true
	}
	if !info.IsDir() {
		return info.Size(), 1, true
	}
	type frame struct {
		path  string
		depth int
	}
	stack := []frame{{path: root, depth: 0}}
	complete = true
	for len(stack) > 0 {
		if ctx.Err() != nil {
			return bytes, entries, false
		}
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		dirEntries, err := os.ReadDir(cur.path)
		if err != nil {
			continue
		}
		for _, e := range dirEntries {
			if ctx.Err() != nil {
				return bytes, entries, false
			}
			if *budget <= 0 {
				return bytes, entries, false
			}
			*budget--
			if e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			entries++
			if fi.IsDir() {
				if cur.depth < maxDepth {
					stack = append(stack, frame{path: filepath.Join(cur.path, e.Name()), depth: cur.depth + 1})
				} else {
					complete = false
				}
			} else {
				bytes += fi.Size()
			}
		}
	}
	return bytes, entries, complete
}

func guardStorageDirAnyRoot(roots []string, p string) (string, error) {
	var notExistErr error
	var specificErr error
	for _, r := range roots {
		if r == "" {
			continue
		}
		real, err := guardStorageDir(r, p)
		if err == nil {
			return real, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			if notExistErr == nil {
				notExistErr = err
			}
			continue
		}
		if specificErr == nil || errors.Is(specificErr, errStorageOutsideRoot) {
			specificErr = err
		}
	}
	if notExistErr != nil {
		if specificErr != nil && !errors.Is(specificErr, errStorageOutsideRoot) {
			return "", specificErr
		}
		return "", notExistErr
	}
	if specificErr != nil {
		return "", specificErr
	}
	return "", errStorageOutsideRoot
}

// guardStorageDir resolves p and ensures it is a real directory strictly inside root with no symlink traversal.
func guardStorageDir(root, p string) (string, error) {
	if root == "" || p == "" || !filepath.IsAbs(root) || !filepath.IsAbs(p) {
		return "", errStorageOutsideRoot
	}
	rel, ok := storageInsideRoot(root, p)
	if !ok {
		return "", errStorageOutsideRoot
	}
	info, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return "", errStorageOutsideRoot
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if realPath != filepath.Join(realRoot, rel) {
		return "", errStorageOutsideRoot
	}
	if _, ok := storageInsideRoot(realRoot, realPath); !ok {
		return "", errStorageOutsideRoot
	}
	return realPath, nil
}

func storageInsideRoot(root, p string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func storageTilde(home, p string) string {
	if home == "" || p == "" {
		return p
	}
	if rel, ok := storageInsideRoot(home, p); ok {
		return "~/" + filepath.ToSlash(rel)
	}
	if filepath.Clean(p) == filepath.Clean(home) {
		return "~"
	}
	return p
}

func sanitizeStorageErr(home string, err error) string {
	if err == nil {
		return ""
	}
	if home == "" {
		return err.Error()
	}
	return strings.ReplaceAll(err.Error(), filepath.Clean(home), "~")
}

func isStorageDirEmpty(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return true
	}
	defer f.Close()
	names, _ := f.Readdirnames(1)
	return len(names) == 0
}

func emptyStorageDirContents(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var firstErr error
	failed := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := filepath.Join(dir, e.Name())
		err := os.RemoveAll(p)
		if err != nil && e.IsDir() {
			makeStorageDirWritable(p)
			err = os.RemoveAll(p)
		}
		if err != nil {
			firstErr = cmp.Or(firstErr, err)
			failed++
		}
	}
	if failed > 1 {
		return fmt.Errorf("%w (and %d more)", firstErr, failed-1)
	}
	return firstErr
}

func makeStorageDirWritable(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if d == nil || !d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			_ = os.Chmod(p, info.Mode().Perm()|0o700)
		}
		return nil
	})
}

func readStorageProcs(ctx context.Context, deps storageDeps) storageProcSnapshot {
	if deps.runningProcs == nil {
		return storageProcSnapshot{err: errors.New("process enumeration unavailable")}
	}
	list, err := deps.runningProcs(ctx)
	if err != nil || list.procs == nil {
		return storageProcSnapshot{err: cmp.Or(err, errors.New("process enumeration unavailable"))}
	}
	return storageProcSnapshot{
		procs:         list.procs,
		known:         true,
		cmdlinesKnown: list.cmdlinesKnown,
	}
}

// storageToolchainInUse reports whether any toolchain executable in users is running.
// It inspects full process command lines (not just truncated comm names) so npm/npx
// launched via node are detected accurately without unrelated node servers blocking cleanup.
// When users is non-empty and process enumeration is unavailable (procs.known == false),
// or when npm/npx is checked without full command-line visibility (e.g. image-name-only
// fallback or a node process with an unreadable command line), it fails closed
// (inUse=true, unknown=true) so destructive cleanup never proceeds blindly.
func storageToolchainInUse(users []string, procs storageProcSnapshot) (inUse bool, unknown bool) {
	if len(users) == 0 {
		return false, false
	}
	if !procs.known {
		return true, true
	}
	for _, proc := range procs.procs {
		if storageProcMatchesAnyUser(proc, users) {
			return true, false
		}
	}
	if storageUsersNeedCmdline(users) {
		if !procs.cmdlinesKnown {
			return true, true
		}
		for _, proc := range procs.procs {
			if proc.cmdlineKnown || len(proc.args) > 0 {
				continue
			}
			commExe := storageCmdTokenExe(proc.comm)
			if storageExeMatchesTool(commExe, "node") || storageExeMatchesTool(commExe, "nodejs") {
				return true, true
			}
		}
	}
	return false, false
}

func storageUsersNeedCmdline(users []string) bool {
	for _, u := range users {
		lu := strings.ToLower(strings.TrimSpace(u))
		if lu == "npm" || lu == "npx" {
			return true
		}
	}
	return false
}

func storageProcMatchesAnyUser(proc storageProcEntry, users []string) bool {
	if len(proc.args) > 0 {
		exe, rest := unwrapStorageProcCmd(proc.args)
		for _, u := range users {
			if storageExeMatchesTool(exe, u) {
				return true
			}
			lu := strings.ToLower(u)
			if (lu == "npm" || lu == "npx") && (storageExeMatchesTool(exe, "node") || storageExeMatchesTool(exe, "nodejs")) {
				if storageNodeArgsInvokeNpmTool(rest, lu) {
					return true
				}
			}
			if lu == "pip" && storageExeMatchesTool(exe, "python") {
				if storagePythonArgsInvokePip(rest) {
					return true
				}
			}
		}
	}
	if proc.rawCmdline != "" {
		if storageRawCmdlineMatchesAnyUser(proc.rawCmdline, users) {
			return true
		}
	}
	if proc.comm != "" {
		commExe := storageCmdTokenExe(proc.comm)
		for _, u := range users {
			if storageExeMatchesTool(commExe, u) {
				return true
			}
		}
	}
	return false
}

// storageRawCmdlineMatchesAnyUser handles space-joined command lines (macOS `ps -axo args=`)
// where executable or script paths may contain unquoted spaces (e.g. `/Users/a/My Tools/bin/node`).
func storageRawCmdlineMatchesAnyUser(raw string, users []string) bool {
	pieces := strings.Fields(raw)
	if len(pieces) == 0 {
		return false
	}
	if storageCmdTokenExe(pieces[0]) == "env" {
		i := 1
		for i < len(pieces) {
			tok := strings.Trim(pieces[i], `"'`)
			if tok == "--" {
				i++
				break
			}
			if tok == "-u" || tok == "--unset" || tok == "-C" || tok == "--chdir" {
				i += 2
				continue
			}
			if strings.HasPrefix(tok, "-") {
				i++
				continue
			}
			if strings.Contains(tok, "=") && !strings.ContainsAny(tok, `/\`) {
				i++
				continue
			}
			break
		}
		if i >= len(pieces) {
			return false
		}
		pieces = pieces[i:]
	}

	type exeCandidate struct {
		exe  string
		rest []string
	}
	var cands []exeCandidate
	firstClean := strings.Trim(pieces[0], `"'`)
	if exe := storageCmdTokenExe(firstClean); exe != "" {
		cands = append(cands, exeCandidate{exe: exe, rest: pieces[1:]})
	}
	// If the command starts with a path (e.g. `/Users/a/My Tools/bin/node ...`), also consider
	// space-separated continuations of that first path up to the next `-` flag or `/`-rooted arg.
	if strings.HasPrefix(firstClean, "/") || strings.HasPrefix(firstClean, "~") {
		for k := 1; k < len(pieces); k++ {
			tok := strings.Trim(pieces[k], `"'`)
			if tok == "" || strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "~") {
				break
			}
			if strings.Contains(tok, "/") {
				if exe := storageCmdTokenExe(tok); exe != "" {
					cands = append(cands, exeCandidate{exe: exe, rest: pieces[k+1:]})
				}
			}
		}
	}

	for _, c := range cands {
		for _, u := range users {
			if storageExeMatchesTool(c.exe, u) {
				return true
			}
			lu := strings.ToLower(u)
			if (lu == "npm" || lu == "npx") && (storageExeMatchesTool(c.exe, "node") || storageExeMatchesTool(c.exe, "nodejs")) {
				if storageRawNodeRestInvokesNpmTool(c.rest, lu) {
					return true
				}
			}
			if lu == "pip" && storageExeMatchesTool(c.exe, "python") {
				if storagePythonArgsInvokePip(c.rest) {
					return true
				}
			}
		}
	}
	return false
}

func storageRawNodeRestInvokesNpmTool(rest []string, tool string) bool {
	for i := 0; i < len(rest); i++ {
		tok := strings.Trim(rest[i], `"'`)
		if tok == "" {
			continue
		}
		if tok == "-e" || tok == "--eval" || tok == "-p" || tok == "--print" {
			return false
		}
		if tok == "--" {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			if !strings.Contains(tok, "=") && storageNodeFlagTakesArg(tok) {
				i++
			}
			continue
		}
		norm := strings.ToLower(strings.ReplaceAll(tok, "\\", "/"))
		base := path.Base(norm)
		switch tool {
		case "npm":
			if base == "npm.js" || base == "npm-cli.js" || base == "npm-cli.cjs" || base == "npm-cli.mjs" ||
				strings.Contains(norm, "/node_modules/npm/bin/npm") ||
				(strings.Contains(norm, "/") && base == "npm") {
				return true
			}
		case "npx":
			if base == "npx.js" || base == "npx-cli.js" || base == "npx-cli.cjs" || base == "npx-cli.mjs" ||
				strings.Contains(norm, "/node_modules/npm/bin/npx") ||
				(strings.Contains(norm, "/") && base == "npx") {
				return true
			}
		}
	}
	return false
}

func unwrapStorageProcCmd(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	// Handle processes that overwrite argv[0] via process.title (e.g. "npm install").
	if len(args) == 1 && !strings.ContainsAny(args[0], `/\`) {
		if fields := strings.Fields(args[0]); len(fields) > 0 {
			args = fields
		}
	}
	i := 0
	exe := storageCmdTokenExe(args[0])
	if exe == "env" {
		i = 1
		for i < len(args) {
			tok := args[i]
			if tok == "--" {
				i++
				break
			}
			if tok == "-u" || tok == "--unset" || tok == "-C" || tok == "--chdir" {
				i += 2
				continue
			}
			if strings.HasPrefix(tok, "-") {
				i++
				continue
			}
			if strings.Contains(tok, "=") && !strings.ContainsAny(tok, `/\`) {
				i++
				continue
			}
			break
		}
		if i < len(args) {
			return storageCmdTokenExe(args[i]), args[i+1:]
		}
		return "", nil
	}
	return exe, args[1:]
}

func storageCmdTokenExe(tok string) string {
	tok = strings.Trim(strings.TrimSpace(tok), `"'`)
	if tok == "" {
		return ""
	}
	// If there is no path separator, a space-separated token (e.g. comm/process.title "npm install")
	// starts with the executable name.
	if !strings.ContainsAny(tok, `/\`) {
		if fields := strings.Fields(tok); len(fields) > 0 {
			tok = fields[0]
		}
	}
	tok = strings.TrimPrefix(tok, "-")
	norm := strings.ReplaceAll(tok, "\\", "/")
	base := path.Base(norm)
	base = strings.TrimPrefix(base, "-")
	base = strings.ToLower(strings.TrimSpace(base))
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".cmd")
	base = strings.TrimSuffix(base, ".bat")
	if base == "." || base == "/" {
		return ""
	}
	return base
}

func storageExeMatchesTool(exe, tool string) bool {
	exe = strings.ToLower(strings.TrimSpace(exe))
	tool = strings.ToLower(strings.TrimSpace(tool))
	if exe == "" || tool == "" {
		return false
	}
	rest, ok := strings.CutPrefix(exe, tool)
	return ok && strings.Trim(rest, "0123456789.") == ""
}

func storageNodeFlagTakesArg(flag string) bool {
	switch flag {
	case "-r", "--require", "--import", "--loader", "--experimental-loader",
		"-C", "--conditions", "--env-file", "--env-file-if-exists",
		"--diagnostic-dir", "--redirect-warnings", "--title",
		"--cpu-prof-dir", "--cpu-prof-name", "--heap-prof-dir", "--heap-prof-name",
		"--inspect-port", "--heapsnapshot-signal", "--input-type",
		"--test-name-pattern", "--test-reporter", "--test-reporter-destination",
		"--watch-path":
		return true
	default:
		return false
	}
}

func storageNodeArgsInvokeNpmTool(args []string, tool string) bool {
	var script string
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "" {
			continue
		}
		if tok == "--" {
			if i+1 < len(args) {
				script = args[i+1]
			}
			break
		}
		if tok == "-e" || tok == "--eval" || tok == "-p" || tok == "--print" {
			return false
		}
		if strings.HasPrefix(tok, "-") {
			if !strings.Contains(tok, "=") && storageNodeFlagTakesArg(tok) {
				i++
			}
			continue
		}
		script = tok
		break
	}
	if script == "" {
		return false
	}
	norm := strings.ToLower(strings.ReplaceAll(script, "\\", "/"))
	base := path.Base(norm)
	switch tool {
	case "npm":
		return base == "npm" || base == "npm.js" || base == "npm-cli.js" || base == "npm-cli.cjs" || base == "npm-cli.mjs" ||
			strings.Contains(norm, "/node_modules/npm/bin/npm")
	case "npx":
		return base == "npx" || base == "npx.js" || base == "npx-cli.js" || base == "npx-cli.cjs" || base == "npx-cli.mjs" ||
			strings.Contains(norm, "/node_modules/npm/bin/npx")
	default:
		return false
	}
}

func storagePythonArgsInvokePip(args []string) bool {
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "-m" && i+1 < len(args) {
			return storageExeMatchesTool(args[i+1], "pip")
		}
		if tok == "-c" {
			return false
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		return storageExeMatchesTool(storageCmdTokenExe(tok), "pip")
	}
	return false
}

// readRunningProcsForOS enumerates running processes with full command lines where supported:
// Linux (/proc/<pid>/cmdline + comm), macOS (/bin/ps -axo args=), and Windows
// (PowerShell Win32_Process Name+CommandLine CSV, falling back to image-name-only tasklist).
// It returns an error whenever process enumeration is unavailable or unsupported so callers fail closed.
func readRunningProcsForOS(ctx context.Context, goos string, runCmd func(context.Context, string, ...string) ([]byte, error)) (storageProcList, error) {
	switch goos {
	case "linux":
		return readLinuxProcs("/proc")
	case "darwin":
		if runCmd == nil {
			return storageProcList{}, errors.New("ps command runner unavailable")
		}
		cmdCtx, cancel := context.WithTimeout(ctx, storageProcCmdTimeout)
		defer cancel()
		out, err := runCmd(cmdCtx, "/bin/ps", "-axo", "args=")
		if err != nil {
			return storageProcList{}, fmt.Errorf("ps: %w", err)
		}
		procs, parseErr := parsePSArgsLines(bytes.NewReader(out))
		if parseErr != nil {
			return storageProcList{}, fmt.Errorf("parse ps output: %w", parseErr)
		}
		if len(procs) == 0 {
			return storageProcList{}, errors.New("ps returned no processes")
		}
		return storageProcList{procs: procs, cmdlinesKnown: true}, nil
	case "windows":
		if runCmd == nil {
			return storageProcList{}, errors.New("windows process command runner unavailable")
		}
		cmdCtx, cancel := context.WithTimeout(ctx, storageProcCmdTimeout)
		out, psErr := runCmd(cmdCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command",
			"Get-CimInstance Win32_Process | Select-Object Name,CommandLine | ConvertTo-Csv -NoTypeInformation")
		cancel()
		if psErr == nil {
			if procs, cmdlinesKnown := parseWindowsCIMCSV(out); len(procs) > 0 {
				return storageProcList{procs: procs, cmdlinesKnown: cmdlinesKnown}, nil
			}
		}
		tlCtx, tlCancel := context.WithTimeout(ctx, storageProcCmdTimeout)
		defer tlCancel()
		tlOut, tlErr := runCmd(tlCtx, "tasklist", "/FO", "CSV", "/NH")
		if tlErr != nil {
			if psErr != nil {
				return storageProcList{}, fmt.Errorf("powershell: %w; tasklist: %v", psErr, tlErr)
			}
			return storageProcList{}, fmt.Errorf("tasklist: %w", tlErr)
		}
		procs := parseTasklistCSV(tlOut)
		if len(procs) == 0 {
			return storageProcList{}, errors.New("tasklist returned no processes")
		}
		// tasklist only exposes image names (no command lines), so cmdlinesKnown stays false
		// and cmdline-dependent targets (npm/npx) fail closed.
		return storageProcList{procs: procs, cmdlinesKnown: false}, nil
	default:
		return storageProcList{}, fmt.Errorf("process enumeration unsupported on %s", goos)
	}
}

func readLinuxProcs(procRoot string) (storageProcList, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return storageProcList{}, fmt.Errorf("read %s: %w", procRoot, err)
	}
	var out []storageProcEntry
	anyCmdline := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) == 0 || name[0] < '0' || name[0] > '9' {
			continue
		}
		pidDir := filepath.Join(procRoot, name)
		var entry storageProcEntry
		if b, err := os.ReadFile(filepath.Join(pidDir, "comm")); err == nil {
			entry.comm = strings.TrimSpace(string(b))
		}
		if b, err := os.ReadFile(filepath.Join(pidDir, "cmdline")); err == nil && len(b) > 0 {
			entry.args = parseLinuxCmdline(b)
			if len(entry.args) > 0 {
				entry.cmdlineKnown = true
				anyCmdline = true
			}
		}
		if entry.comm != "" || len(entry.args) > 0 {
			out = append(out, entry)
		}
	}
	if len(out) == 0 {
		return storageProcList{}, fmt.Errorf("no readable processes in %s", procRoot)
	}
	return storageProcList{procs: out, cmdlinesKnown: anyCmdline}, nil
}

func parseLinuxCmdline(raw []byte) []string {
	parts := bytes.Split(raw, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(string(p)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parsePSArgsLines parses `ps -axo args=` output (macOS/BSD) into full command-line entries.
// It uses an unbounded bufio.Reader (no 64 KiB Scanner cap) and propagates read errors
// so callers never accept a truncated process list.
func parsePSArgsLines(r io.Reader) ([]storageProcEntry, error) {
	br := bufio.NewReader(r)
	var out []storageProcEntry
	for {
		line, err := br.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			args := splitPOSIXLikeCmdline(trimmed)
			if len(args) > 0 {
				out = append(out, storageProcEntry{
					comm:         storageCmdTokenExe(args[0]),
					args:         args,
					rawCmdline:   trimmed,
					cmdlineKnown: true,
				})
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return out, nil
}

// splitPOSIXLikeCmdline splits a command line on whitespace outside single/double quotes.
func splitPOSIXLikeCmdline(s string) []string {
	var args []string
	var cur strings.Builder
	var quote byte
	hasToken := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
				continue
			}
			cur.WriteByte(c)
			hasToken = true
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			hasToken = true
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
			continue
		}
		cur.WriteByte(c)
		hasToken = true
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args
}

// parseWindowsCIMCSV parses PowerShell `Get-CimInstance Win32_Process | Select-Object Name,CommandLine | ConvertTo-Csv -NoTypeInformation`.
func parseWindowsCIMCSV(raw []byte) ([]storageProcEntry, bool) {
	cleaned := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	r := csv.NewReader(bytes.NewReader(cleaned))
	r.FieldsPerRecord = -1
	nameIdx, cmdIdx := -1, -1
	var out []storageProcEntry
	anyCmdline := false
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) == 0 {
			continue
		}
		first := strings.TrimSpace(rec[0])
		if strings.HasPrefix(first, "#TYPE") {
			continue
		}
		if nameIdx < 0 {
			for i, h := range rec {
				switch strings.ToLower(strings.TrimSpace(h)) {
				case "name":
					nameIdx = i
				case "commandline":
					cmdIdx = i
				}
			}
			if nameIdx >= 0 {
				continue
			}
			return nil, false
		}
		var nameVal, cmdVal string
		if nameIdx < len(rec) {
			nameVal = strings.TrimSpace(rec[nameIdx])
		}
		if cmdIdx >= 0 && cmdIdx < len(rec) {
			cmdVal = strings.TrimSpace(rec[cmdIdx])
		}
		var args []string
		if cmdVal != "" {
			args = splitWindowsCmdline(cmdVal)
		}
		comm := storageCmdTokenExe(nameVal)
		if comm == "" && len(args) > 0 {
			comm = storageCmdTokenExe(args[0])
		}
		if comm == "" && len(args) == 0 {
			continue
		}
		hasCmd := len(args) > 0
		if hasCmd {
			anyCmdline = true
		}
		out = append(out, storageProcEntry{
			comm:         comm,
			args:         args,
			cmdlineKnown: hasCmd,
		})
	}
	return out, anyCmdline
}

// splitWindowsCmdline splits a Windows Win32_Process.CommandLine string into argv tokens,
// preserving spaces inside double-quoted executable or script paths.
func splitWindowsCmdline(s string) []string {
	var args []string
	var cur strings.Builder
	inQuote := false
	hasToken := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQuote = !inQuote
			hasToken = true
			continue
		}
		if !inQuote && (c == ' ' || c == '\t' || c == '\r' || c == '\n') {
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
			continue
		}
		cur.WriteByte(c)
		hasToken = true
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args
}

// parseTasklistCSV parses `tasklist /FO CSV /NH` output on Windows, stripping `.exe` suffixes.
func parseTasklistCSV(raw []byte) []storageProcEntry {
	var out []storageProcEntry
	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) == 0 {
			continue
		}
		name := storageCmdTokenExe(rec[0])
		if name != "" {
			out = append(out, storageProcEntry{comm: name})
		}
	}
	return out
}

// isRotatedStorageLogName reports whether a file under Persona's logs directory is a rotated or archived log.
// Active/current logs (e.g., node.log, hub.log, link.log, node-attach.log) return false and are never deleted.
func isRotatedStorageLogName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	lower := strings.ToLower(name)
	if idx := strings.Index(lower, ".log."); idx > 0 && idx+len(".log.") < len(lower) {
		return true
	}
	if idx := strings.Index(lower, ".log-"); idx > 0 && idx+len(".log-") < len(lower) {
		return true
	}
	for _, ext := range []string{".gz", ".zst", ".bz2", ".xz", ".zip", ".old", ".bak"} {
		if strings.HasSuffix(lower, ext) && len(lower) > len(ext) {
			return true
		}
	}
	return false
}

// collectRotatedLogFiles walks realDir (never following symlinks) and partitions files into
// reclaimable rotated/closed logs vs protected active/current or open log files.
func collectRotatedLogFiles(ctx context.Context, realDir string, maxDepth int, budget *int, activePaths map[string]bool) (totalBytes, reclaimableBytes int64, totalCount int, cleanable []string, protectedCount int, complete bool) {
	info, err := os.Lstat(realDir)
	if err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return 0, 0, 0, nil, 0, true
	}
	type frame struct {
		path  string
		depth int
	}
	stack := []frame{{path: realDir, depth: 0}}
	complete = true
	for len(stack) > 0 {
		if ctx.Err() != nil {
			return totalBytes, reclaimableBytes, totalCount, cleanable, protectedCount, false
		}
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		dirEntries, err := os.ReadDir(cur.path)
		if err != nil {
			continue
		}
		for _, e := range dirEntries {
			if ctx.Err() != nil {
				return totalBytes, reclaimableBytes, totalCount, cleanable, protectedCount, false
			}
			if *budget <= 0 {
				return totalBytes, reclaimableBytes, totalCount, cleanable, protectedCount, false
			}
			*budget--
			if e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			child := filepath.Join(cur.path, e.Name())
			totalCount++
			if fi.IsDir() {
				if cur.depth < maxDepth {
					stack = append(stack, frame{path: child, depth: cur.depth + 1})
				} else {
					complete = false
				}
				continue
			}
			totalBytes += fi.Size()
			if activePaths[child] || !isRotatedStorageLogName(e.Name()) {
				protectedCount++
				continue
			}
			reclaimableBytes += fi.Size()
			cleanable = append(cleanable, child)
		}
	}
	return totalBytes, reclaimableBytes, totalCount, cleanable, protectedCount, complete
}

// collectActiveStorageLogFiles resolves log files currently held open by this node's logger,
// stdout/stderr redirects, or readable local process file descriptors under dirs.
func (n *Node) collectActiveStorageLogFiles(ctx context.Context, goos string, dirs []string) map[string]bool {
	active := map[string]bool{}
	addPath := func(p string) {
		p = strings.TrimSuffix(strings.TrimSpace(p), " (deleted)")
		if p == "" || !filepath.IsAbs(p) {
			return
		}
		resolved := filepath.Clean(p)
		if realPath, err := filepath.EvalSymlinks(resolved); err == nil {
			resolved = realPath
		}
		for _, d := range dirs {
			if _, ok := storageInsideRoot(d, resolved); ok {
				active[resolved] = true
				return
			}
		}
	}
	type namedWriter interface {
		Name() string
	}
	if n != nil && n.logger != nil {
		if nw, ok := n.logger.Writer().(namedWriter); ok {
			addPath(nw.Name())
		}
	}
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if f != nil {
			addPath(f.Name())
		}
	}
	if goos == "linux" {
		collectLinuxOpenFilesUnder(ctx, "/proc", dirs, addPath)
	}
	return active
}

func collectLinuxOpenFilesUnder(ctx context.Context, procRoot string, dirs []string, addPath func(string)) {
	if len(dirs) == 0 {
		return
	}
	scanFDDir := func(fdDir string) {
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			return
		}
		for _, fd := range fds {
			if ctx.Err() != nil {
				return
			}
			if target, err := os.Readlink(filepath.Join(fdDir, fd.Name())); err == nil {
				addPath(target)
			}
		}
	}
	scanFDDir(filepath.Join(procRoot, "self", "fd"))
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) == 0 || name[0] < '0' || name[0] > '9' {
			continue
		}
		scanFDDir(filepath.Join(procRoot, name, "fd"))
	}
}
