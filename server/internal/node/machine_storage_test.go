package node

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func testStorageDeps(t *testing.T) (storageDeps, string, string) {
	t.Helper()
	home := t.TempDir()
	dataDir := filepath.Join(home, ".persona")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	pct := 45.0
	deps := storageDeps{
		goos:    "linux",
		home:    home,
		dataDir: dataDir,
		getenv:  func(string) string { return "" },
		now:     func() time.Time { return now },
		mounts: func() ([]proto.MountStat, bool) {
			return []proto.MountStat{
				{Mount: "/", Device: "/dev/vda2", FSType: "ext4", UsedBytes: 50 << 30, AvailBytes: 50 << 30, TotalBytes: 100 << 30, UsedPercent: 50, InodesTotal: 1000, InodesUsed: 450, InodesFree: 550, InodesPercent: &pct},
			}, true
		},
		available: func(string) (int64, bool) { return 50 << 30, true },
		lookPath:  func(string) (string, error) { return "", errors.New("not found") },
		runCmd:    func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not called") },
		runningProcs: func(context.Context) (storageProcList, error) {
			return storageProcList{procs: []storageProcEntry{}, cmdlinesKnown: true}, nil
		},
		activeLogFiles: func(context.Context, []string) map[string]bool {
			return map[string]bool{}
		},
	}
	return deps, home, dataDir
}

func writeStorageFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectMachineStorage(t *testing.T) {
	deps, _, _ := testStorageDeps(t)
	rep := collectMachineStorage(deps)
	if !rep.Supported || rep.OS != "linux" || len(rep.Mounts) != 1 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if rep.Mounts[0].Mount != "/" || rep.Mounts[0].InodesPercent == nil || *rep.Mounts[0].InodesPercent != 45 {
		t.Fatalf("unexpected mount: %+v", rep.Mounts[0])
	}
}

func TestCollectMachineStorageHotspotsCoversCategories(t *testing.T) {
	deps, home, dataDir := testStorageDeps(t)
	writeStorageFile(t, filepath.Join(home, ".cache", "go-build", "a.obj"), 2048)
	writeStorageFile(t, filepath.Join(home, ".cache", "pip", "wheel.whl"), 1024)
	writeStorageFile(t, filepath.Join(home, "go", "pkg", "mod", "cache.zip"), 4096)
	writeStorageFile(t, filepath.Join(home, ".npm", "_cacache", "tar.tgz"), 1536)
	writeStorageFile(t, filepath.Join(dataDir, "logs", "node.log"), 512)
	writeStorageFile(t, filepath.Join(dataDir, "logs", "node.log.1"), 1024)
	writeStorageFile(t, filepath.Join(dataDir, "backups", "snap-1.tar"), 3072)

	// Add a stale broken worktree under ~/.herdr/worktrees/repo/old-branch
	wtDir := filepath.Join(home, ".herdr", "worktrees", "repo", "old-branch")
	writeStorageFile(t, filepath.Join(wtDir, "payload.bin"), 2560)
	if err := os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: /nonexistent/repo/.git/worktrees/old-branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Mock docker system df --format json
	deps.lookPath = func(bin string) (string, error) {
		if bin == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errors.New("missing")
	}
	deps.runCmd = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "docker" && len(args) >= 2 && args[0] == "system" && args[1] == "df" {
			return []byte(strings.Join([]string{
				`{"Type":"Images","TotalCount":"3","Active":"1","Size":"2GB","Reclaimable":"1GB (50%)"}`,
				`{"Type":"Build Cache","TotalCount":"10","Active":"0","Size":"500MB","Reclaimable":"500MB"}`,
			}, "\n")), nil
		}
		return nil, errors.New("unexpected cmd")
	}

	rep := collectMachineStorageHotspots(context.Background(), deps, proto.StorageHotspotsParams{TimeoutMS: 5000, MaxDepth: 4})
	if rep.Partial {
		t.Fatalf("expected non-partial scan, got partial")
	}
	byID := map[string]proto.StorageHotspotCategory{}
	for _, c := range rep.Categories {
		byID[c.ID] = c
	}
	if byID[proto.StorageCategoryGoBuild].Bytes != 2048 || !byID[proto.StorageCategoryGoBuild].Cleanable {
		t.Errorf("go-build = %+v", byID[proto.StorageCategoryGoBuild])
	}
	if byID[proto.StorageCategoryGoMod].Bytes != 4096 || !byID[proto.StorageCategoryGoMod].Cleanable {
		t.Errorf("go-mod = %+v", byID[proto.StorageCategoryGoMod])
	}
	if byID[proto.StorageCategoryNpmCache].Bytes != 1536 {
		t.Errorf("npm-cache = %+v", byID[proto.StorageCategoryNpmCache])
	}
	if byID[proto.StorageCategoryPipCache].Bytes != 1024 {
		t.Errorf("pip-cache = %+v", byID[proto.StorageCategoryPipCache])
	}
	if byID[proto.StorageCategoryPersonaLogs].Bytes != 1536 || byID[proto.StorageCategoryPersonaLogs].ReclaimableBytes != 1024 || !byID[proto.StorageCategoryPersonaLogs].Cleanable {
		t.Errorf("persona-logs = %+v", byID[proto.StorageCategoryPersonaLogs])
	}
	if byID[proto.StorageCategoryPersonaBackups].Bytes != 3072 || !byID[proto.StorageCategoryPersonaBackups].Cleanable {
		t.Errorf("persona-backups = %+v", byID[proto.StorageCategoryPersonaBackups])
	}
	if byID[proto.StorageCategoryUserCache].Cleanable {
		t.Errorf("user-cache must be advisory (cleanable=false)")
	}
	if byID[proto.StorageCategoryDocker].Bytes != 2_500_000_000 || !byID[proto.StorageCategoryDocker].Cleanable || byID[proto.StorageCategoryDocker].CleanupTargetID != proto.StorageTargetDockerBuildCache {
		t.Errorf("docker = %+v", byID[proto.StorageCategoryDocker])
	}
	wt := byID[proto.StorageCategoryGitWorktrees]
	if wt.ItemCount != 1 || wt.CleanupTargetID != proto.StorageTargetGitWorktreesPrune || !wt.Items[0].Stale {
		t.Errorf("git-worktrees = %+v", wt)
	}
}

func TestMachineStorageCleanupValidationAndPreviewAndExecute(t *testing.T) {
	deps, home, dataDir := testStorageDeps(t)
	writeStorageFile(t, filepath.Join(home, ".cache", "pip", "a.whl"), 2048)
	activeLogPath := filepath.Join(dataDir, "logs", "node.log")
	rotatedLogPath := filepath.Join(dataDir, "logs", "node.log.1")
	writeStorageFile(t, activeLogPath, 512)
	writeStorageFile(t, rotatedLogPath, 1024)

	// Reject empty targets
	if _, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{DryRun: true}); err == nil {
		t.Fatal("expected error for empty targets")
	}
	// Reject arbitrary path / non-allowlisted target
	if _, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{Targets: []string{"/etc"}, Confirm: true}); err == nil {
		t.Fatal("expected error for unknown target")
	} else {
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != storageCodeBadRequest {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	// Reject unconfirmed live cleanup
	if _, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{Targets: []string{proto.StorageTargetPipCache}}); err == nil {
		t.Fatal("expected confirmation_required error")
	} else {
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != storageCodeConfirmNeeded {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// Dry-run preview must NOT delete files and must only count rotated logs (not active node.log)
	prev, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetPipCache, proto.StorageTargetPersonaLogs},
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if !prev.DryRun || prev.EstimatedBytes != 3072 || prev.FreedBytes != 0 || len(prev.Items) != 2 {
		t.Fatalf("unexpected preview result: %+v", prev)
	}
	if _, err := os.Stat(filepath.Join(home, ".cache", "pip", "a.whl")); err != nil {
		t.Fatalf("dry run deleted pip cache file: %v", err)
	}
	if _, err := os.Stat(rotatedLogPath); err != nil {
		t.Fatalf("dry run deleted rotated log file: %v", err)
	}

	// Confirmed execution deletes pip cache & rotated log while keeping active node.log intact
	res, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetPipCache, proto.StorageTargetPersonaLogs},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if res.DryRun || res.FreedBytes != 3072 {
		t.Fatalf("unexpected cleanup result: %+v", res)
	}
	for _, it := range res.Items {
		if it.Status != proto.StorageCleanupCleaned {
			t.Errorf("item %+v not cleaned", it)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".cache", "pip", "a.whl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pip file still exists after cleanup")
	}
	if st, err := os.Stat(filepath.Join(home, ".cache", "pip")); err != nil || !st.IsDir() {
		t.Fatalf("pip dir itself should be preserved: %v", err)
	}
	if _, err := os.Stat(rotatedLogPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotated log file still exists after cleanup")
	}
	if st, err := os.Stat(activeLogPath); err != nil || st.Size() != 512 {
		t.Fatalf("active node.log must remain intact after cleanup: %v", err)
	}
}

func TestMachineStorageCleanupRefusesSymlinkEscapeAndRespectsInUse(t *testing.T) {
	deps, home, _ := testStorageDeps(t)
	outside := t.TempDir()
	writeStorageFile(t, filepath.Join(outside, "secret.txt"), 1024)
	if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".cache", "pip")); err != nil {
		t.Fatal(err)
	}
	writeStorageFile(t, filepath.Join(home, ".cache", "go-build", "build.o"), 1024)
	deps.runningProcs = func(context.Context) (storageProcList, error) {
		return storageProcList{
			procs:         []storageProcEntry{{comm: "go", args: []string{"/usr/local/go/bin/go", "test", "./..."}, cmdlineKnown: true}},
			cmdlinesKnown: true,
		}, nil
	}

	res, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetPipCache, proto.StorageTargetGoBuild},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("cleanup returned err: %v", err)
	}
	if res.Items[0].Status != proto.StorageCleanupFailed {
		t.Errorf("symlink target status = %q, want failed", res.Items[0].Status)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
		t.Fatalf("symlink escape removed outside file!")
	}
	if res.Items[1].Status != proto.StorageCleanupInUse {
		t.Errorf("in-use target status = %q, want in_use", res.Items[1].Status)
	}
}

func TestMachineStorageCleanupFailsClosedWhenProcessListUnavailable(t *testing.T) {
	deps, home, dataDir := testStorageDeps(t)
	goFile := filepath.Join(home, ".cache", "go-build", "build.o")
	npmFile := filepath.Join(home, ".npm", "_cacache", "tar.tgz")
	pipFile := filepath.Join(home, ".cache", "pip", "wheel.whl")
	backupFile := filepath.Join(dataDir, "backups", "old.tar")
	writeStorageFile(t, goFile, 2048)
	writeStorageFile(t, npmFile, 1024)
	writeStorageFile(t, pipFile, 512)
	writeStorageFile(t, backupFile, 4096)

	deps.runningProcs = func(context.Context) (storageProcList, error) {
		return storageProcList{}, errors.New("procfs unavailable")
	}

	// Hotspot scan must mark toolchain caches in_use when process state is unknown
	hotspots := collectMachineStorageHotspots(context.Background(), deps, proto.StorageHotspotsParams{TimeoutMS: 5000, MaxDepth: 4})
	for _, c := range hotspots.Categories {
		switch c.ID {
		case proto.StorageCategoryGoBuild, proto.StorageCategoryNpmCache, proto.StorageCategoryPipCache:
			if !c.InUse {
				t.Errorf("category %s should be marked in_use when process list is unavailable: %+v", c.ID, c)
			}
		case proto.StorageCategoryPersonaBackups:
			if c.InUse {
				t.Errorf("persona-backups should not depend on toolchain process list: %+v", c)
			}
		}
	}

	// Confirmed cleanup must refuse toolchain caches (in_use) and leave their files intact,
	// while non-toolchain persona-backups can still be cleaned.
	res, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{
			proto.StorageTargetGoBuild,
			proto.StorageTargetNpmCache,
			proto.StorageTargetPipCache,
			proto.StorageTargetPersonaBackups,
		},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("cleanup returned err: %v", err)
	}
	for i, wantID := range []string{proto.StorageTargetGoBuild, proto.StorageTargetNpmCache, proto.StorageTargetPipCache} {
		if res.Items[i].TargetID != wantID || res.Items[i].Status != proto.StorageCleanupInUse {
			t.Errorf("item[%d] = %+v, want %s with status in_use", i, res.Items[i], wantID)
		}
	}
	if res.Items[3].Status != proto.StorageCleanupCleaned {
		t.Errorf("persona-backups status = %q, want cleaned", res.Items[3].Status)
	}
	for _, keep := range []string{goFile, npmFile, pipFile} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("cache file %s was deleted despite unavailable process list: %v", keep, err)
		}
	}
	if _, err := os.Stat(backupFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup file should have been cleaned")
	}
}

type errAfterFirstLineReader struct {
	first string
	sent  bool
	err   error
}

func (r *errAfterFirstLineReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.first), nil
	}
	return 0, r.err
}

func TestReadRunningProcsCrossPlatformAndUnsupportedFailClosed(t *testing.T) {
	// Darwin ps -axo args= parsing (full paths, login shell dash, mixed case Python, node + npm-cli.js vs unrelated node)
	darwinRun := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/bin/ps" || len(args) != 2 || args[0] != "-axo" || args[1] != "args=" {
			t.Fatalf("unexpected darwin cmd: %s %v", name, args)
		}
		return []byte(strings.Join([]string{
			"/opt/homebrew/bin/go build ./...",
			"-zsh",
			"/Library/Frameworks/Python.framework/Versions/3.12/Resources/Python.app/Contents/MacOS/Python -m pip install foo",
			"/usr/local/bin/node /app/dist/server.js --port 3000",
		}, "\n") + "\n"), nil
	}
	darwinList, err := readRunningProcsForOS(context.Background(), "darwin", darwinRun)
	if err != nil {
		t.Fatalf("darwin process enum failed: %v", err)
	}
	snap := storageProcSnapshot{procs: darwinList.procs, known: true, cmdlinesKnown: darwinList.cmdlinesKnown}
	if inUse, unknown := storageToolchainInUse([]string{"go", "gopls"}, snap); !inUse || unknown {
		t.Errorf("expected go in use on darwin, got inUse=%v unknown=%v", inUse, unknown)
	}
	if inUse, unknown := storageToolchainInUse([]string{"pip", "python"}, snap); !inUse || unknown {
		t.Errorf("expected python/pip in use on darwin, got inUse=%v unknown=%v", inUse, unknown)
	}
	// An unrelated node server must NOT block npm/npx cleanup.
	if inUse, unknown := storageToolchainInUse([]string{"npm", "npx"}, snap); inUse || unknown {
		t.Errorf("unrelated node server should not mark npm/npx in use, got inUse=%v unknown=%v", inUse, unknown)
	}

	// Darwin ps with node invoking npm-cli.js MUST mark npm in use.
	darwinNpmRun := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("/usr/local/bin/node --max-old-space-size=4096 /opt/homebrew/lib/node_modules/npm/bin/npm-cli.js install\n"), nil
	}
	darwinNpmList, err := readRunningProcsForOS(context.Background(), "darwin", darwinNpmRun)
	if err != nil {
		t.Fatalf("darwin npm process enum failed: %v", err)
	}
	if inUse, _ := storageToolchainInUse([]string{"npm", "npx"}, storageProcSnapshot{procs: darwinNpmList.procs, known: true, cmdlinesKnown: darwinNpmList.cmdlinesKnown}); !inUse {
		t.Errorf("expected node running npm-cli.js to mark npm in use on darwin")
	}

	// Darwin ps with spaces in node executable and npm-cli.js / npx-cli.js paths (unquoted & quoted)
	// MUST still detect npm/npx in use, while an unrelated node server with spaces in its path does NOT.
	darwinSpaceNpmRun := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(strings.Join([]string{
			"/Users/alice/My Tools/bin/node --max-old-space-size=4096 /Users/alice/My Tools/lib/node_modules/npm/bin/npm-cli.js install",
		}, "\n") + "\n"), nil
	}
	darwinSpaceNpmList, err := readRunningProcsForOS(context.Background(), "darwin", darwinSpaceNpmRun)
	if err != nil {
		t.Fatalf("darwin space-path npm enum failed: %v", err)
	}
	if inUse, unknown := storageToolchainInUse([]string{"npm", "npx"}, storageProcSnapshot{procs: darwinSpaceNpmList.procs, known: true, cmdlinesKnown: darwinSpaceNpmList.cmdlinesKnown}); !inUse || unknown {
		t.Errorf("expected darwin space-path node+npm-cli.js to mark npm in use, got inUse=%v unknown=%v", inUse, unknown)
	}

	darwinQuotedSpaceNpxRun := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("\"/Users/alice/My Tools/bin/node\" \"/Users/alice/My Tools/lib/node_modules/npm/bin/npx-cli.js\" vitest\n"), nil
	}
	darwinQuotedNpxList, err := readRunningProcsForOS(context.Background(), "darwin", darwinQuotedSpaceNpxRun)
	if err != nil {
		t.Fatalf("darwin quoted space-path npx enum failed: %v", err)
	}
	if inUse, unknown := storageToolchainInUse([]string{"npm", "npx"}, storageProcSnapshot{procs: darwinQuotedNpxList.procs, known: true, cmdlinesKnown: darwinQuotedNpxList.cmdlinesKnown}); !inUse || unknown {
		t.Errorf("expected darwin quoted space-path node+npx-cli.js to mark npx in use, got inUse=%v unknown=%v", inUse, unknown)
	}

	darwinSpaceServerRun := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(strings.Join([]string{
			"/Applications/My Node App/Contents/MacOS/node /Applications/My Node App/Contents/Resources/server.js --port 3000",
			"/usr/bin/vim /Users/alice/My Tools/lib/node_modules/npm/bin/npm-cli.js",
		}, "\n") + "\n"), nil
	}
	darwinSpaceServerList, err := readRunningProcsForOS(context.Background(), "darwin", darwinSpaceServerRun)
	if err != nil {
		t.Fatalf("darwin space-path server enum failed: %v", err)
	}
	if inUse, unknown := storageToolchainInUse([]string{"npm", "npx"}, storageProcSnapshot{procs: darwinSpaceServerList.procs, known: true, cmdlinesKnown: darwinSpaceServerList.cmdlinesKnown}); inUse || unknown {
		t.Errorf("unrelated darwin node server / vim with space paths should not mark npm/npx in use, got inUse=%v unknown=%v", inUse, unknown)
	}

	// Darwin ps output containing a >64 KiB command line BEFORE an active npm process
	// MUST parse completely without truncating or omitting the trailing npm process.
	hugeLine := "/usr/bin/python3 " + strings.Repeat("a", 80*1024)
	darwinHugeRun := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(hugeLine + "\n/usr/local/bin/node /opt/homebrew/lib/node_modules/npm/bin/npm-cli.js ci\n"), nil
	}
	darwinHugeList, err := readRunningProcsForOS(context.Background(), "darwin", darwinHugeRun)
	if err != nil {
		t.Fatalf("darwin >64KiB ps output parse failed: %v", err)
	}
	if len(darwinHugeList.procs) != 2 {
		t.Fatalf("expected 2 processes after >64KiB line, got %d", len(darwinHugeList.procs))
	}
	if inUse, unknown := storageToolchainInUse([]string{"npm", "npx"}, storageProcSnapshot{procs: darwinHugeList.procs, known: true, cmdlinesKnown: darwinHugeList.cmdlinesKnown}); !inUse || unknown {
		t.Errorf("expected npm after >64KiB line to be detected, got inUse=%v unknown=%v", inUse, unknown)
	}

	// parsePSArgsLines must propagate reader errors rather than returning partial output.
	if _, err := parsePSArgsLines(&errAfterFirstLineReader{first: "/opt/homebrew/bin/go version\n", err: errors.New("read failed")}); err == nil {
		t.Fatal("expected parsePSArgsLines to propagate reader error")
	}

	// Darwin ps failure must fail closed
	if _, err := readRunningProcsForOS(context.Background(), "darwin", func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("ps failed")
	}); err == nil {
		t.Fatal("expected error when darwin ps fails")
	}

	// Windows PowerShell Win32_Process CSV with full command lines:
	// - Detects node.exe running npm-cli.js (including quoted paths with spaces)
	// - Allows cleanup when only an unrelated node.exe server is running with a known CommandLine
	winCimNpmRun := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "powershell" {
			t.Fatalf("unexpected windows cmd: %s", name)
		}
		return []byte(strings.Join([]string{
			`"Name","CommandLine"`,
			`"go.exe","""C:\Program Files\Go\bin\go.exe"" test ./..."`,
			`"python3.12.exe","""C:\Python312\python3.12.exe"" -m pip install wheel"`,
			`"node.exe","""C:\Program Files\nodejs\node.exe"" ""C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js"" install"`,
		}, "\r\n") + "\r\n"), nil
	}
	winNpmList, err := readRunningProcsForOS(context.Background(), "windows", winCimNpmRun)
	if err != nil {
		t.Fatalf("windows CIM process enum failed: %v", err)
	}
	winNpmSnap := storageProcSnapshot{procs: winNpmList.procs, known: true, cmdlinesKnown: winNpmList.cmdlinesKnown}
	if inUse, unknown := storageToolchainInUse([]string{"go"}, winNpmSnap); !inUse || unknown {
		t.Errorf("expected go in use on windows CIM, got inUse=%v unknown=%v", inUse, unknown)
	}
	if inUse, unknown := storageToolchainInUse([]string{"python"}, winNpmSnap); !inUse || unknown {
		t.Errorf("expected python3.12 in use on windows CIM, got inUse=%v unknown=%v", inUse, unknown)
	}
	if inUse, unknown := storageToolchainInUse([]string{"npm", "npx"}, winNpmSnap); !inUse || unknown {
		t.Errorf("expected Windows node.exe running npm-cli.js to mark npm in use, got inUse=%v unknown=%v", inUse, unknown)
	}

	// Windows PowerShell Win32_Process with only an unrelated node.exe server (readable CommandLine):
	// npm/npx is NOT in use.
	winCimServerRun := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "powershell" {
			t.Fatalf("unexpected windows cmd: %s", name)
		}
		return []byte(strings.Join([]string{
			`"Name","CommandLine"`,
			`"node.exe","""C:\Program Files\nodejs\node.exe"" ""C:\srv\my app\server.js"" --port 3000"`,
		}, "\r\n") + "\r\n"), nil
	}
	winSrvList, err := readRunningProcsForOS(context.Background(), "windows", winCimServerRun)
	if err != nil {
		t.Fatalf("windows CIM unrelated node enum failed: %v", err)
	}
	winSrvSnap := storageProcSnapshot{procs: winSrvList.procs, known: true, cmdlinesKnown: winSrvList.cmdlinesKnown}
	if inUse, unknown := storageToolchainInUse([]string{"npm", "npx"}, winSrvSnap); inUse || unknown {
		t.Errorf("unrelated Windows node.exe server should not block npm cleanup, got inUse=%v unknown=%v", inUse, unknown)
	}

	// Windows PowerShell Win32_Process where a node.exe process has an empty/unreadable CommandLine
	// (e.g. elevated process): MUST fail closed for npm/npx.
	winCimEmptyNodeCmd := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(strings.Join([]string{
			`"Name","CommandLine"`,
			`"go.exe","""C:\Go\bin\go.exe"" version"`,
			`"node.exe",""`,
		}, "\r\n") + "\r\n"), nil
	}
	winElevatedList, err := readRunningProcsForOS(context.Background(), "windows", winCimEmptyNodeCmd)
	if err != nil {
		t.Fatalf("windows CIM elevated node enum failed: %v", err)
	}
	winElevatedSnap := storageProcSnapshot{procs: winElevatedList.procs, known: true, cmdlinesKnown: winElevatedList.cmdlinesKnown}
	if inUse, unknown := storageToolchainInUse([]string{"npm", "npx"}, winElevatedSnap); !inUse || !unknown {
		t.Errorf("Windows node.exe with unreadable CommandLine must fail closed for npm/npx, got inUse=%v unknown=%v", inUse, unknown)
	}

	// Windows fallback to image-name-only tasklist CSV when PowerShell fails:
	// - go/python still detected accurately by image name
	// - npm/npx MUST fail closed (inUse=true, unknown=true) because command lines are unavailable
	winTasklistFallback := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "powershell" {
			return nil, errors.New("powershell unavailable")
		}
		if name != "tasklist" {
			t.Fatalf("unexpected windows fallback cmd: %s", name)
		}
		return []byte("\"go.exe\",\"1234\",\"Console\",\"1\",\"10,000 K\"\n\"python3.12.exe\",\"5678\",\"Console\",\"1\",\"20,000 K\"\n"), nil
	}
	winFallbackList, err := readRunningProcsForOS(context.Background(), "windows", winTasklistFallback)
	if err != nil {
		t.Fatalf("windows tasklist fallback failed: %v", err)
	}
	if winFallbackList.cmdlinesKnown {
		t.Fatalf("expected tasklist fallback to report cmdlinesKnown=false")
	}
	winFallbackSnap := storageProcSnapshot{procs: winFallbackList.procs, known: true, cmdlinesKnown: winFallbackList.cmdlinesKnown}
	if inUse, unknown := storageToolchainInUse([]string{"go"}, winFallbackSnap); !inUse || unknown {
		t.Errorf("expected go in use on windows tasklist fallback, got inUse=%v unknown=%v", inUse, unknown)
	}
	if inUse, unknown := storageToolchainInUse([]string{"python"}, winFallbackSnap); !inUse || unknown {
		t.Errorf("expected python3.12 in use on windows tasklist fallback, got inUse=%v unknown=%v", inUse, unknown)
	}
	if inUse, unknown := storageToolchainInUse([]string{"npm", "npx"}, winFallbackSnap); !inUse || !unknown {
		t.Errorf("image-name-only tasklist fallback MUST fail closed for npm/npx, got inUse=%v unknown=%v", inUse, unknown)
	}

	// Unsupported OS must return an error so cleanup fails closed
	if _, err := readRunningProcsForOS(context.Background(), "plan9", nil); err == nil {
		t.Fatal("expected unsupported OS to return an error")
	}
}

func TestGuardStorageDirAnyRootMultiRootBehavior(t *testing.T) {
	base := t.TempDir()
	missingRoot := filepath.Join(base, "missing-root")
	realRoot := filepath.Join(base, "real-root")
	target := filepath.Join(realRoot, "backups")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Earlier candidate root missing (or broken symlink) must NOT stop searching later roots.
	danglingRoot := filepath.Join(base, "dangling-root")
	if err := os.Symlink(filepath.Join(base, "no-such-dir"), danglingRoot); err != nil {
		t.Fatal(err)
	}
	got, err := guardStorageDirAnyRoot([]string{missingRoot, danglingRoot, realRoot}, target)
	if err != nil {
		t.Fatalf("guardStorageDirAnyRoot failed when later root matched: %v", err)
	}
	wantReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantReal {
		t.Fatalf("got %q, want %q", got, wantReal)
	}

	// 2. If an earlier root shares a prefix and returns fs.ErrNotExist (e.g. root itself missing)
	// while a later root matches via symlinked/real parent, the later root still succeeds;
	// and when a target inside dataDir (outside home) does not exist, ErrNotExist is preserved
	// instead of being overwritten by home's errStorageOutsideRoot.
	outsideDataDir := t.TempDir()
	home := t.TempDir()
	missingDevBackups := filepath.Join(outsideDataDir, "update", "development-backups")
	_, err = guardStorageDirAnyRoot([]string{outsideDataDir, home}, missingDevBackups)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist for missing subdir under dataDir with separate home root, got: %v", err)
	}

	// End-to-end persona-backups cleanup with dataDir outside home and missing update/development-backups:
	// existing backups dir must be cleaned without failing on missing sibling path.
	deps, _, _ := testStorageDeps(t)
	deps.home = home
	deps.dataDir = outsideDataDir
	backupFile := filepath.Join(outsideDataDir, "backups", "snap-2.tar")
	writeStorageFile(t, backupFile, 2048)
	res, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetPersonaBackups},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("persona-backups cleanup returned err: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Status != proto.StorageCleanupCleaned || res.Items[0].Bytes != 2048 {
		t.Fatalf("expected persona-backups cleaned with 2048 bytes, got %+v", res.Items)
	}
	if _, err := os.Stat(backupFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected backupFile to be removed")
	}

	// 3. Symlink escape across multiple roots must still return errStorageOutsideRoot (not ErrNotExist).
	outside := t.TempDir()
	symTarget := filepath.Join(realRoot, "escape")
	if err := os.Symlink(outside, symTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := guardStorageDirAnyRoot([]string{missingRoot, realRoot}, symTarget); !errors.Is(err, errStorageOutsideRoot) {
		t.Fatalf("expected errStorageOutsideRoot for symlink escape, got %v", err)
	}
}

func TestNpmCacheResolutionAndCmdlineInUseDetection(t *testing.T) {
	// 1. npm_config_cache pointing directly to a flat cache root (no _cacache child) is honored directly.
	deps, home, _ := testStorageDeps(t)
	flatNpmCache := filepath.Join(home, "custom-npm-cache")
	flatFile := filepath.Join(flatNpmCache, "content-v2.bin")
	writeStorageFile(t, flatFile, 4096)
	deps.getenv = func(k string) string {
		if k == "npm_config_cache" {
			return flatNpmCache
		}
		return ""
	}
	// Unrelated long-running node server must NOT block npm cache cleanup.
	deps.runningProcs = func(context.Context) (storageProcList, error) {
		return storageProcList{
			procs: []storageProcEntry{
				{comm: "node", args: []string{"/usr/bin/node", "/srv/app/server.js", "--port", "8080"}, cmdlineKnown: true},
			},
			cmdlinesKnown: true,
		}, nil
	}

	rep := collectMachineStorageHotspots(context.Background(), deps, proto.StorageHotspotsParams{TimeoutMS: 5000, MaxDepth: 4})
	var npmCat proto.StorageHotspotCategory
	for _, c := range rep.Categories {
		if c.ID == proto.StorageCategoryNpmCache {
			npmCat = c
			break
		}
	}
	if npmCat.Bytes != 4096 || npmCat.InUse || !npmCat.Cleanable {
		t.Fatalf("expected flat npm_config_cache cleanable & not in_use, got %+v", npmCat)
	}

	res, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetNpmCache},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("npm cleanup failed: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Status != proto.StorageCleanupCleaned || res.Items[0].Bytes != 4096 {
		t.Fatalf("expected flat npm cache cleaned, got %+v", res.Items)
	}
	if _, err := os.Stat(flatFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected flat npm cache file to be deleted")
	}

	// 2. When npm_config_cache has a _cacache subdirectory, target _cacache without wiping sibling files.
	nestedRoot := filepath.Join(home, "nested-npm")
	siblingConfig := filepath.Join(nestedRoot, "keep-me.txt")
	nestedCacheFile := filepath.Join(nestedRoot, "_cacache", "index-v5.json")
	writeStorageFile(t, siblingConfig, 128)
	writeStorageFile(t, nestedCacheFile, 2048)
	deps.getenv = func(k string) string {
		if k == "npm_config_cache" {
			return nestedRoot
		}
		return ""
	}
	res2, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetNpmCache},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("nested npm cleanup failed: %v", err)
	}
	if len(res2.Items) != 1 || res2.Items[0].Status != proto.StorageCleanupCleaned || res2.Items[0].Bytes != 2048 {
		t.Fatalf("expected nested _cacache cleaned, got %+v", res2.Items)
	}
	if _, err := os.Stat(siblingConfig); err != nil {
		t.Fatalf("sibling file outside _cacache should be preserved: %v", err)
	}
	if _, err := os.Stat(nestedCacheFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nested _cacache file should be deleted")
	}

	// 3. npm_config_cache already ending in _cacache must not double-append _cacache.
	directCacache := filepath.Join(home, "explicit-npm", "_cacache")
	if got := resolveNpmCacheDir(home, directCacache); got != directCacache {
		t.Fatalf("resolveNpmCacheDir(%q) = %q, want %q", directCacache, got, directCacache)
	}

	// 3b. If `_cacache` is a symlink, resolveNpmCacheDir must return `_cacache` so guardStorageDir
	// rejects it instead of falling back to and wiping the parent cache root and sibling files.
	symRoot := filepath.Join(home, "symlinked-npm")
	symSibling := filepath.Join(symRoot, "npmrc-sibling.txt")
	writeStorageFile(t, symSibling, 256)
	realTarget := filepath.Join(home, "real-npm-store")
	realTargetFile := filepath.Join(realTarget, "blob.bin")
	writeStorageFile(t, realTargetFile, 1024)
	if err := os.Symlink(realTarget, filepath.Join(symRoot, "_cacache")); err != nil {
		t.Fatal(err)
	}
	if got := resolveNpmCacheDir(home, symRoot); got != filepath.Join(symRoot, "_cacache") {
		t.Fatalf("symlinked _cacache resolved to %q, want %q", got, filepath.Join(symRoot, "_cacache"))
	}
	deps.getenv = func(k string) string {
		if k == "npm_config_cache" {
			return symRoot
		}
		return ""
	}
	symRes, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetNpmCache},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("symlinked npm cleanup returned rpc err: %v", err)
	}
	if len(symRes.Items) != 1 || symRes.Items[0].Status != proto.StorageCleanupFailed {
		t.Fatalf("expected symlinked _cacache cleanup to fail closed, got %+v", symRes.Items)
	}
	if _, err := os.Stat(symSibling); err != nil {
		t.Fatalf("sibling file in parent npm cache root must NOT be deleted: %v", err)
	}
	if _, err := os.Stat(realTargetFile); err != nil {
		t.Fatalf("symlink target file must NOT be deleted: %v", err)
	}

	// 3c. If `_cacache` is a regular file (non-directory), resolveNpmCacheDir must also return
	// `_cacache` so guardStorageDir rejects it without touching parent sibling files.
	fileRoot := filepath.Join(home, "file-cacache-npm")
	fileSibling := filepath.Join(fileRoot, "sibling.txt")
	cacacheFile := filepath.Join(fileRoot, "_cacache")
	writeStorageFile(t, fileSibling, 256)
	writeStorageFile(t, cacacheFile, 64)
	if got := resolveNpmCacheDir(home, fileRoot); got != cacacheFile {
		t.Fatalf("regular-file _cacache resolved to %q, want %q", got, cacacheFile)
	}
	deps.getenv = func(k string) string {
		if k == "npm_config_cache" {
			return fileRoot
		}
		return ""
	}
	fileRes, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetNpmCache},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("regular-file _cacache cleanup returned rpc err: %v", err)
	}
	if len(fileRes.Items) != 1 || fileRes.Items[0].Status != proto.StorageCleanupFailed {
		t.Fatalf("expected regular-file _cacache cleanup to fail closed, got %+v", fileRes.Items)
	}
	if _, err := os.Stat(fileSibling); err != nil {
		t.Fatalf("sibling file in parent npm cache root must NOT be deleted when _cacache is a file: %v", err)
	}
	if _, err := os.Stat(cacacheFile); err != nil {
		t.Fatalf("regular file _cacache must NOT be deleted: %v", err)
	}

	// 4. Linux /proc cmdline parsing: detects node running npm-cli.js / npx-cli.js / env npm / process.title "npm install"
	// while ignoring unrelated node processes.
	procDir := t.TempDir()
	writeProc := func(pid, comm string, cmdline []byte) {
		t.Helper()
		dir := filepath.Join(procDir, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if cmdline != nil {
			if err := os.WriteFile(filepath.Join(dir, "cmdline"), cmdline, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Unrelated node process only -> NOT in use
	writeProc("100", "node", []byte("/usr/bin/node\x00/home/dev/server.js\x00--npm-like-flag\x00"))
	procs, err := readLinuxProcs(procDir)
	if err != nil {
		t.Fatalf("readLinuxProcs failed: %v", err)
	}
	if inUse, _ := storageToolchainInUse([]string{"npm", "npx"}, storageProcSnapshot{procs: procs.procs, known: true, cmdlinesKnown: procs.cmdlinesKnown}); inUse {
		t.Fatalf("unrelated node process should not mark npm/npx in use")
	}

	// Add node running npm-cli.js with comm="node" -> MUST be in use
	writeProc("101", "node", []byte("/usr/bin/node20\x00--max-old-space-size=4096\x00/usr/lib/node_modules/npm/bin/npm-cli.js\x00install\x00"))
	procs, err = readLinuxProcs(procDir)
	if err != nil {
		t.Fatalf("readLinuxProcs failed: %v", err)
	}
	if inUse, _ := storageToolchainInUse([]string{"npm", "npx"}, storageProcSnapshot{procs: procs.procs, known: true, cmdlinesKnown: procs.cmdlinesKnown}); !inUse {
		t.Fatalf("expected node20 running npm-cli.js to mark npm in use")
	}

	// Test npx-cli.js via /usr/bin/env node
	procDir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir2, "201"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(procDir2, "201", "comm"), []byte("env\n"), 0o644)
	_ = os.WriteFile(filepath.Join(procDir2, "201", "cmdline"), []byte("/usr/bin/env\x00NODE_ENV=dev\x00node\x00/opt/node/lib/node_modules/npm/bin/npx-cli.js\x00tsc\x00"), 0o644)
	procs2, err := readLinuxProcs(procDir2)
	if err != nil {
		t.Fatalf("readLinuxProcs failed: %v", err)
	}
	if inUse, _ := storageToolchainInUse([]string{"npm", "npx"}, storageProcSnapshot{procs: procs2.procs, known: true, cmdlinesKnown: procs2.cmdlinesKnown}); !inUse {
		t.Fatalf("expected env node npx-cli.js to mark npx in use")
	}

	// Test process.title overwriting argv[0] & truncated 15-char comm ("npm install --n")
	procDir3 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir3, "301"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(procDir3, "301", "comm"), []byte("npm install --n\n"), 0o644)
	_ = os.WriteFile(filepath.Join(procDir3, "301", "cmdline"), []byte("npm install --no-audit\x00"), 0o644)
	procs3, err := readLinuxProcs(procDir3)
	if err != nil {
		t.Fatalf("readLinuxProcs failed: %v", err)
	}
	if inUse, _ := storageToolchainInUse([]string{"npm", "npx"}, storageProcSnapshot{procs: procs3.procs, known: true, cmdlinesKnown: procs3.cmdlinesKnown}); !inUse {
		t.Fatalf("expected npm process.title to mark npm in use")
	}
}

func TestMachineStorageCleanupPreservesActiveAndOpenPersonaLogs(t *testing.T) {
	deps, _, dataDir := testStorageDeps(t)
	logsDir := filepath.Join(dataDir, "logs")
	activeNodeLog := filepath.Join(logsDir, "node.log")
	activeHubLog := filepath.Join(logsDir, "hub.log")
	rotatedLog1 := filepath.Join(logsDir, "node.log.1")
	rotatedLogGz := filepath.Join(logsDir, "node.log.2.gz")
	openRotatedLog := filepath.Join(logsDir, "link.log.1")

	writeStorageFile(t, activeNodeLog, 512)
	writeStorageFile(t, activeHubLog, 256)
	writeStorageFile(t, rotatedLog1, 1024)
	writeStorageFile(t, rotatedLogGz, 2048)
	writeStorageFile(t, openRotatedLog, 4096)

	realOpenRotated, err := filepath.EvalSymlinks(openRotatedLog)
	if err != nil {
		t.Fatal(err)
	}
	deps.activeLogFiles = func(context.Context, []string) map[string]bool {
		return map[string]bool{realOpenRotated: true}
	}

	// Confirmed cleanup deletes ONLY closed rotated logs; active *.log and open rotated log stay intact.
	res, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetPersonaLogs},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Status != proto.StorageCleanupCleaned || res.Items[0].Bytes != 3072 {
		t.Fatalf("unexpected persona-logs cleanup result: %+v", res.Items)
	}
	for _, removed := range []string{rotatedLog1, rotatedLogGz} {
		if _, err := os.Stat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected rotated log %s to be removed", removed)
		}
	}
	for _, kept := range []string{activeNodeLog, activeHubLog, openRotatedLog} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected active/open log %s to remain intact: %v", kept, err)
		}
	}

	// Second confirmed run with only active/open logs remaining must report in_use and delete nothing.
	res2, err := runMachineStorageCleanup(context.Background(), deps, proto.StorageCleanupParams{
		Targets: []string{proto.StorageTargetPersonaLogs},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("second cleanup failed: %v", err)
	}
	if len(res2.Items) != 1 || res2.Items[0].Status != proto.StorageCleanupInUse || res2.Items[0].Bytes != 0 {
		t.Fatalf("expected in_use when only active/open logs remain, got: %+v", res2.Items)
	}
	for _, kept := range []string{activeNodeLog, activeHubLog, openRotatedLog} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("second cleanup removed protected log %s: %v", kept, err)
		}
	}

	// Real open file handle via node.logger + /proc/self/fd must also protect an open rotated log file.
	liveLogFile, err := os.OpenFile(filepath.Join(logsDir, "live.log.1"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer liveLogFile.Close()
	if _, err := liveLogFile.WriteString("live log line\n"); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: filepath.Dir(dataDir), dir: dataDir, logger: log.New(liveLogFile, "", 0)}
	realLogsDir, err := filepath.EvalSymlinks(logsDir)
	if err != nil {
		t.Fatal(err)
	}
	active := n.collectActiveStorageLogFiles(context.Background(), runtime.GOOS, []string{realLogsDir})
	realLivePath, err := filepath.EvalSymlinks(liveLogFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !active[realLivePath] {
		t.Fatalf("expected open logger file %s in active set %v", realLivePath, active)
	}
}

func TestMachineStorageCleanupOverallDeadlineTruncatesRemainingTargets(t *testing.T) {
	deps, home, dataDir := testStorageDeps(t)
	pipFile := filepath.Join(home, ".cache", "pip", "wheel.whl")
	backupFile := filepath.Join(dataDir, "backups", "snap.tar")
	writeStorageFile(t, pipFile, 2048)
	writeStorageFile(t, backupFile, 4096)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps.lookPath = func(bin string) (string, error) {
		if bin == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errors.New("missing")
	}
	deps.runCmd = func(cmdCtx context.Context, name string, args ...string) ([]byte, error) {
		if name != "docker" {
			return nil, errors.New("unexpected command")
		}
		if _, hasDeadline := cmdCtx.Deadline(); !hasDeadline {
			t.Errorf("expected cleanup command context to carry bounded deadline")
		}
		if len(args) >= 2 && args[0] == "system" && args[1] == "df" {
			return []byte(`{"Type":"Build Cache","TotalCount":"2","Active":"0","Size":"100MB","Reclaimable":"100MB"}` + "\n"), nil
		}
		if len(args) >= 2 && args[0] == "builder" && args[1] == "prune" {
			// Simulate the overall cleanup deadline expiring just as target 1 completes.
			cancel()
			return []byte("Total reclaimed space: 100MB\n"), nil
		}
		return nil, errors.New("unexpected docker args")
	}

	res, err := runMachineStorageCleanup(ctx, deps, proto.StorageCleanupParams{
		Targets: []string{
			proto.StorageTargetDockerBuildCache,
			proto.StorageTargetPipCache,
			proto.StorageTargetPersonaBackups,
		},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("cleanup returned unexpected rpc err: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("expected 3 items, got %+v", res.Items)
	}
	if res.Items[0].TargetID != proto.StorageTargetDockerBuildCache || res.Items[0].Status != proto.StorageCleanupCleaned {
		t.Errorf("item[0] = %+v, want docker-build-cache cleaned", res.Items[0])
	}
	for i, wantID := range []string{proto.StorageTargetPipCache, proto.StorageTargetPersonaBackups} {
		it := res.Items[i+1]
		if it.TargetID != wantID || it.Status != proto.StorageCleanupSkipped || !strings.Contains(it.Error, "deadline") {
			t.Errorf("item[%d] = %+v, want %s skipped with deadline reason", i+1, it, wantID)
		}
	}
	for _, keep := range []string{pipFile, backupFile} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("skipped target file %s should remain intact: %v", keep, err)
		}
	}
	if res.FreedBytes != 100_000_000 {
		t.Errorf("FreedBytes = %d, want 100000000 from completed target", res.FreedBytes)
	}
}

func TestParseHumanBytes(t *testing.T) {
	cases := map[string]int64{
		"0B":            0,
		"512kB":         512_000,
		"12.5MB (100%)": 12_500_000,
		"2GB":           2_000_000_000,
		"1.5GiB (50%)":  1610612736,
		"bad":           0,
	}
	for in, want := range cases {
		if got := parseHumanBytes(in); got != want {
			t.Errorf("parseHumanBytes(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestNodeHandleMachineStorageDispatch(t *testing.T) {
	n := &Node{home: t.TempDir()}
	out, err := n.handle(context.Background(), proto.MethodMachineStorage, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("machine.storage failed: %v", err)
	}
	if _, ok := out.(proto.MachineStorageReport); !ok {
		t.Fatalf("unexpected type %T", out)
	}
	if _, err := n.handle(context.Background(), proto.MethodMachineStorageCleanup, json.RawMessage(`{"targets":["/"]}`)); err == nil {
		t.Fatal("expected bad_request for non-allowlisted target via handle")
	}
}
