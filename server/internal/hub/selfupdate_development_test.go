package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
)

func TestDevelopmentAppliesOlderProgramBackupThroughSharedInstaller(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			p := newPublisher(t, "unused", 202611021700)
			h, machine, target, execs := updatingHub(t, p, "release")
			u := h.upd
			u.client = newUpdater(h).client
			nodes := t.TempDir()
			t.Setenv("PERSONA_NODE_BINARIES", nodes)
			stale := filepath.Join(nodes, "personad-windows-amd64")
			os.WriteFile(stale, []byte("newer package before rollback"), 0600)
			u.loaded, u.settings.Source, u.settings.DevelopmentAuto = true, "web-dev", true
			h.build.Commit = strings.Repeat("a", 40)
			h.cfg.Update.Development = &update.DevelopmentConfig{Backup: update.DevelopmentBackupConfig{Quiesce: []string{"pause"}, Resume: []string{"resume"}}}
			data := filepath.Join(h.cfg.dir, "current-history")
			os.WriteFile(data, []byte("messages created after the backup"), 0600)
			version := update.DevelopmentVersion{ID: "snapshot-fixture", Version: "web-dev-older", Build: 202611010001, Commit: strings.Repeat("b", 40), UpdatedAt: "2026-11-01T10:00:00Z"}
			prepared := filepath.Join(u.dir, "development", "prepare_backup-fixture", "artifacts")
			phases := make(chan string, 8)
			u.devRun = func(ctx context.Context, cfg *update.DevelopmentConfig, dir, operation string, input map[string]any, result any) error {
				phases <- operation
				var response any
				switch operation {
				case "backups":
					response = map[string]any{"backups": []update.DevelopmentVersion{version}}
				case "prepare_backup":
					if corrupt {
						return errors.New("corrupt snapshot")
					}
					if input["backup_id"] != version.ID {
						return errors.New("wrong backup")
					}
					artifacts := prepared
					if err := os.MkdirAll(artifacts, 0700); err != nil {
						return err
					}
					script := []byte(fmt.Sprintf("#!/bin/sh\necho %s\necho 'channel release, build %d'\n", version.Version, version.Build))
					os.WriteFile(filepath.Join(artifacts, "personad-"+runtime.GOOS+"-"+runtime.GOARCH+".gz"), gz(script), 0600)
					response = map[string]any{"artifacts": artifacts}
				case "backup":
					response = update.DevelopmentBackup{ID: "snapshot-current", Retained: 3}
				default:
					return fmt.Errorf("unexpected Git/build phase: %s", operation)
				}
				encoded, _ := json.Marshal(response)
				return json.Unmarshal(encoded, result)
			}
			setSessions(machine, proto.Session{SessionID: "busy", Status: proto.StatusWorking})
			api := http.NewServeMux()
			u.routes(api)
			request := func(path string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				api.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
				return w
			}
			if w := request("/api/v1/hub/update/backups/missing/apply"); w.Code != 409 {
				t.Fatal(w.Code)
			}
			if !u.view().Auto {
				t.Fatal("missing selection changed automatic policy")
			}
			if w := request("/api/v1/hub/update/backups/" + version.ID + "/apply"); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if u.view().Auto {
				t.Fatal("backup application left automatic upgrades on")
			}
			if corrupt {
				waitFor(t, "preparation failure", func() bool { return u.view().State == "failed" })
				if len(execs()) != 0 {
					t.Fatal("restarted with corrupt backup")
				}
				old, _ := os.ReadFile(target)
				if !strings.Contains(string(old), "echo old") {
					t.Fatal("current program changed")
				}
			} else {
				waitFor(t, "idle wait", func() bool {
					v := u.view()
					if v.State == "failed" {
						t.Fatalf("preparation failed: %s", v.Error)
					}
					return v.State == "waiting_idle"
				})
				if u.view().Latest.Newer || u.view().Latest.Released != nil {
					t.Fatal("historical backup advertised as a new release")
				}
				if _, err := os.Stat(prepared); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("owned build scratch was not removed", err)
				}
				if _, err := os.Stat(filepath.Join(u.stagedDir(version.Build), "personad-"+runtime.GOOS+"-"+runtime.GOARCH)); err != nil {
					t.Fatal("idle wait lost the staged program", err)
				}
				if w := request("/api/v1/hub/update/backups/" + version.ID + "/apply"); w.Code != 409 {
					t.Fatal("duplicate application accepted")
				}
				setSessions(machine)
				waitFor(t, "shared restart", func() bool { return len(execs()) == 1 })
				var pending update.Pending
				if ok, err := update.Load(update.PendingPath(u.dir), &pending); !ok || err != nil {
					t.Fatal(err)
				}
				if pending.ToBuild >= pending.FromBuild || pending.Backup == nil || pending.Development.BackupID != version.ID || pending.Auto {
					t.Fatalf("wrong restoration journal: %+v", pending)
				}
				h.version, h.build.Number, h.build.Commit = pending.ToVersion, pending.ToBuild, version.Commit
				u.confirm(context.Background(), pending)
				if data, _ := os.ReadFile(filepath.Join(nodes, "personad-"+runtime.GOOS+"-"+runtime.GOARCH)); !strings.Contains(string(data), version.Version) {
					t.Fatal("rollback lost the staged historical Node package")
				}
				if u.nodesEnabled() {
					t.Fatal("fixture requires fleet auto-update off")
				}
				if data, err := os.ReadFile(stale); err != nil || string(data) != "newer package before rollback" {
					t.Fatal("backup confirmation removed an unstaged platform package", err)
				}
				if data, err := os.ReadFile(stale + ".version"); err != nil || strings.TrimSpace(string(data)) != pending.FromVersion {
					t.Fatal("retained package must keep its original version identity", err)
				}
				if u.view().State != "updated" || u.view().Development.CurrentUpdatedAt == "" {
					t.Fatal("missing successful installation time")
				}
			}
			if got, _ := os.ReadFile(data); string(got) != "messages created after the backup" {
				t.Fatal("current history overwritten")
			}
			close(phases)
			var got []string
			for phase := range phases {
				got = append(got, phase)
			}
			if slices.Contains(got, "build") || slices.Contains(got, "resolve") {
				t.Fatal(got)
			}
			if !corrupt && !slices.Contains(got, "backup") {
				t.Fatal("current state was not backed up")
			}
		})
	}
}

func TestDevelopmentAvailableWithBuiltInQuiesce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("development updates are unsupported on Windows")
	}
	h := newTestHub(t)
	u := h.upd
	// No hub.json development section: the hub pauses its own writers.
	h.cfg.Update.Development = nil
	if !u.developmentAvailable() {
		t.Fatal("built-in pause did not make development updates available")
	}
	v, _ := getUpdate(t, u)
	if !v.Development.Configured || v.Development.Why != "" {
		t.Fatalf("development view: %+v", v.Development)
	}
	// Hooks are an override and must be a pair.
	h.cfg.Update.Development = &update.DevelopmentConfig{Backup: update.DevelopmentBackupConfig{Quiesce: []string{"pause"}}}
	if u.developmentAvailable() {
		t.Fatal("a quiesce without resume was accepted")
	}
	if v, _ := getUpdate(t, u); v.Development.Configured || v.Development.Why == "" {
		t.Fatalf("half-configured override: %+v", v.Development)
	}
	h.cfg.Update.Development.Backup = update.DevelopmentBackupConfig{Quiesce: []string{"pause"}, Resume: []string{"resume"}}
	if !u.developmentAvailable() {
		t.Fatal("a complete hook pair was refused")
	}
	if v, _ := getUpdate(t, u); v.Development.BuiltinQuiesce {
		t.Fatal("external hooks still reported as the built-in pause")
	}
}

func TestDevelopmentBackupSettingsAreValidatedAndExposed(t *testing.T) {
	h := newTestHub(t)
	u := h.upd
	dir := filepath.Join(t.TempDir(), "snapshots")
	w := nodePolicyRequest(t, u, http.MethodPut, fmt.Sprintf(`{"development_backup_directory":%q,"development_backup_retain":5}`, dir))
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	v, raw := getUpdate(t, u)
	if v.Development.BackupDirectory != dir || v.Development.BackupRetain != 5 || !v.Development.BuiltinQuiesce {
		t.Fatalf("settings not exposed: %+v", v.Development)
	}
	if v.Development.BackupDefaultDirectory != filepath.Join(h.cfg.dir, "update", "development-backups") {
		t.Fatalf("recommended directory = %q", v.Development.BackupDefaultDirectory)
	}
	if raw["development"].(map[string]any)["backup_retain"] != float64(5) {
		t.Fatal("retention missing from the JSON view")
	}
	// Empty restores the recommended folder.
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"development_backup_directory":""}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if v, _ := getUpdate(t, u); v.Development.BackupDirectory != v.Development.BackupDefaultDirectory {
		t.Fatalf("reset directory = %q", v.Development.BackupDirectory)
	}
	// An omitted field keeps the stored folder, including across a retention change.
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"development_backup_retain":4}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if v, _ := getUpdate(t, u); v.Development.BackupDirectory != v.Development.BackupDefaultDirectory || v.Development.BackupRetain != 4 {
		t.Fatalf("omitted folder changed it: %+v", v.Development)
	}
	// Collected data, relative paths and an out-of-range count are refused
	// without changing the stored settings.
	inside := filepath.Join(h.cfg.dir, "data", "snapshots")
	for _, body := range []string{
		fmt.Sprintf(`{"development_backup_directory":%q}`, inside),
		`{"development_backup_directory":"relative/dir"}`,
		`{"development_backup_retain":51}`,
		`{"development_backup_retain":0}`,
		`{"development_backup_retain":-1}`,
	} {
		if w := nodePolicyRequest(t, u, http.MethodPut, body); w.Code != http.StatusConflict {
			t.Fatalf("%s -> %d: %s", body, w.Code, w.Body.String())
		}
	}
	if v, _ := getUpdate(t, u); v.Development.BackupRetain != 4 {
		t.Fatalf("refused request changed the retention: %+v", v.Development)
	}
}

// An explicit Web choice of the recommended folder wins over hub.json's
// directory, while an omitted field keeps hub.json's.
func TestDevelopmentRecommendedFolderOverridesHubConfig(t *testing.T) {
	h := newTestHub(t)
	u := h.upd
	custom := filepath.Join(t.TempDir(), "hub-config-snapshots")
	h.cfg.Update.Development = &update.DevelopmentConfig{Backup: update.DevelopmentBackupConfig{Directory: custom}}
	if v, _ := getUpdate(t, u); v.Development.BackupDirectory != custom {
		t.Fatalf("hub.json directory = %q", v.Development.BackupDirectory)
	}
	if v, _ := getUpdate(t, u); v.Development.BackupDirectory == v.Development.BackupDefaultDirectory {
		t.Fatal("fixture has no hub.json directory")
	}
	// Omitting the field keeps hub.json's directory.
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"development_backup_retain":2}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if v, _ := getUpdate(t, u); v.Development.BackupDirectory != custom {
		t.Fatalf("omitted field lost hub.json's directory: %q", v.Development.BackupDirectory)
	}
	// "Use recommended" (an explicit empty value) selects the recommended one.
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"development_backup_directory":""}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	v, _ := getUpdate(t, u)
	if v.Development.BackupDirectory != v.Development.BackupDefaultDirectory {
		t.Fatalf("explicit recommended folder = %q, want %q", v.Development.BackupDirectory, v.Development.BackupDefaultDirectory)
	}
	u.mu.Lock()
	got := u.developmentBackupConfigLocked().Directory
	u.mu.Unlock()
	if got != v.Development.BackupDefaultDirectory {
		t.Fatalf("snapshot folder = %q, want %q", got, v.Development.BackupDefaultDirectory)
	}
}

// Snapshots that still need recovery keep the folder they are in: changing it
// would pause and dump writers again past the helper's fail-closed gates.
func TestDevelopmentFolderChangeRefusedWhileRecoveryPending(t *testing.T) {
	h := newTestHub(t)
	u := h.upd
	current := filepath.Join(t.TempDir(), "current")
	if w := nodePolicyRequest(t, u, http.MethodPut, fmt.Sprintf(`{"development_backup_directory":%q}`, current)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if err := os.WriteFile(filepath.Join(current, "coordination-pending.json"), []byte(`{"phase":"quiescing"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	w := nodePolicyRequest(t, u, http.MethodPut, fmt.Sprintf(`{"development_backup_directory":%q}`, other))
	if w.Code != http.StatusConflict {
		t.Fatalf("folder changed with a coordination marker: %d %s", w.Code, w.Body.String())
	}
	if v, _ := getUpdate(t, u); v.Development.BackupDirectory != current {
		t.Fatalf("refused change moved the folder: %q", v.Development.BackupDirectory)
	}
	if err := os.Remove(filepath.Join(current, "coordination-pending.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(current, ".incomplete-fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if w := nodePolicyRequest(t, u, http.MethodPut, fmt.Sprintf(`{"development_backup_directory":%q}`, other)); w.Code != http.StatusConflict {
		t.Fatalf("folder changed with an incomplete snapshot: %d %s", w.Code, w.Body.String())
	}
	if err := os.RemoveAll(filepath.Join(current, ".incomplete-fixture")); err != nil {
		t.Fatal(err)
	}
	if w := nodePolicyRequest(t, u, http.MethodPut, fmt.Sprintf(`{"development_backup_directory":%q}`, other)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if v, _ := getUpdate(t, u); v.Development.BackupDirectory != other {
		t.Fatalf("folder after recovery = %q", v.Development.BackupDirectory)
	}
}

func TestDevelopmentBuiltInQuiescePausesWritesUntilResume(t *testing.T) {
	h := newTestHub(t)
	var ran bool
	gated := h.quiesceGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran = true }))
	serve := func(method string) int {
		ran = false
		w := httptest.NewRecorder()
		gated.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/example", nil))
		return w.Code
	}
	resume, err := h.upd.quiesceDevelopmentWriters(context.Background(), update.DevelopmentBackupConfig{Builtin: true})
	if err != nil {
		t.Fatal(err)
	}
	if code := serve(http.MethodPost); code != http.StatusServiceUnavailable || ran {
		t.Fatalf("write during pause: %d ran=%v", code, ran)
	}
	if code := serve(http.MethodGet); code != http.StatusOK || !ran {
		t.Fatalf("read during pause: %d ran=%v", code, ran)
	}
	resume()
	if code := serve(http.MethodPost); code != http.StatusOK || !ran {
		t.Fatalf("write after resume: %d ran=%v", code, ran)
	}
	// A second resume is harmless.
	resume()

	// hub.json hooks are the helper's to run: the hub stays open.
	resume, err = h.upd.quiesceDevelopmentWriters(context.Background(),
		update.DevelopmentBackupConfig{Quiesce: []string{"pause"}, Resume: []string{"resume"}})
	if err != nil {
		t.Fatal(err)
	}
	if code := serve(http.MethodPost); code != http.StatusOK || !ran {
		t.Fatalf("external hooks paused the hub: %d ran=%v", code, ran)
	}
	resume()

	// A half-configured override is never replaced by the built-in pause.
	if _, err := h.upd.quiesceDevelopmentWriters(context.Background(),
		update.DevelopmentBackupConfig{Quiesce: []string{"pause"}}); err == nil {
		t.Fatal("half-configured hooks paused the hub")
	}
	if code := serve(http.MethodPost); code != http.StatusOK {
		t.Fatal("refused quiesce left the hub paused")
	}
}

func TestDevelopmentBackupsAPIDistinguishesFailureFromEmptyList(t *testing.T) {
	h := newTestHub(t)
	u := h.upd
	u.loaded, u.settings.Source = true, "web-dev"
	h.cfg.Update.Development = &update.DevelopmentConfig{Backup: update.DevelopmentBackupConfig{Quiesce: []string{"pause"}, Resume: []string{"resume"}}}
	failed := true
	u.devRun = func(_ context.Context, _ *update.DevelopmentConfig, _, _ string, _ map[string]any, _ any) error {
		if failed {
			return errors.New("fixture unavailable backup directory")
		}
		return nil
	}
	api := http.NewServeMux()
	u.routes(api)
	for _, want := range []int{http.StatusConflict, http.StatusOK} {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/hub/update/backups", nil))
		if w.Code != want {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
		if !failed && strings.TrimSpace(w.Body.String()) != `{"backups":[]}` {
			t.Fatal(w.Body.String())
		}
		failed = false
	}
}

func TestDevelopmentPlatformsFollowRegisteredMachines(t *testing.T) {
	h := newTestHub(t)
	own := runtime.GOOS + "-" + runtime.GOARCH
	if got := h.upd.developmentPlatforms(); !slices.Equal(got, []string{own}) {
		t.Fatalf("Hub-only platforms: %v", got)
	}
	for _, c := range []struct {
		id, os, arch string
		offline      bool
		registered   bool
	}{
		{"mac", "darwin", "arm64", false, true},
		{"same-platform", "darwin", "arm64", false, true},
		{"offline", "linux", "arm64", true, true},
		{"never-seen", "", "", true, true},
		{"partial", "linux", "", true, true},
		{"other-hub", "windows", "amd64", false, false},
	} {
		mc := MachineConfig{ID: c.id, Kind: "ssh"}
		if c.registered {
			h.cfg.Machines = append(h.cfg.Machines, mc)
		}
		m := newMachine(h, mc)
		m.online = !c.offline
		if c.offline {
			m.snap = &proto.Machine{OS: c.os, Arch: c.arch}
		} else {
			m.info = proto.NodeInfo{OS: c.os, Arch: c.arch}
		}
		h.machines, h.byID[c.id] = append(h.machines, m), m
	}
	want := []string{own, "darwin-arm64", "linux-arm64"}
	slices.Sort(want)
	if got := h.upd.developmentPlatforms(); !slices.Equal(got, slices.Compact(want)) {
		t.Fatalf("platforms %v, want %v", got, want)
	}
}

func TestDevelopmentBackupIsNotAStoppedReleaseSource(t *testing.T) {
	p := newPublisher(t, "v0.1.17-177-g563f7fde5b-202611071005", 202611071005)
	h, _ := behindHub(t, p)
	u := h.upd
	if _, err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !u.view().SourceBehind {
		t.Fatal("fixture does not exercise a stopped release source")
	}
	// A saved program version can predate the current release. Selecting it
	// under Web development says nothing about the health of the Git source.
	u.settings.Source = "web-dev"
	u.release.Development = &update.DevelopmentCandidate{Commit: strings.Repeat("b", 40), BackupID: "snapshot-fixture"}
	v, raw := getUpdate(t, u)
	if v.SourceBehind || raw["source_behind"] != nil {
		t.Fatal("historical backup reported as stopped update source")
	}
	if v.Latest == nil || v.Latest.Newer || v.Latest.BackupID != "snapshot-fixture" {
		t.Fatal("backup selection was lost")
	}
}

func TestDevelopmentRejectsUnrebuiltPackagesThatCannotBeSetAside(t *testing.T) {
	h := newTestHub(t)
	nodes := t.TempDir()
	t.Setenv("PERSONA_NODE_BINARIES", nodes)
	stale := filepath.Join(nodes, "personad-windows-amd64")
	if err := os.WriteFile(stale, []byte("old package"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stale+".prev", 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.upd.updateNodes(context.Background(), t.TempDir(), "previous-version", &update.DevelopmentCandidate{}); err == nil {
		t.Fatal("accepted stale packages that still claim the new version")
	}
}

func TestDevelopmentSettingsOptInAndActiveChannelGuard(t *testing.T) {
	u, _, _ := updaterWithStoredSettings(t)
	u.h.cfg.Update.Development = &update.DevelopmentConfig{}
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"source":"web-dev"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if u.view().Auto || u.wantsAuto() {
		t.Fatal("inherited release automatic updates")
	}
	if v := u.view(); !v.Development.Configured || v.Development.Branch != "main" {
		t.Fatalf("unexpected development source: %+v", v.Development)
	}
	u.running = true
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"source":"release"}`); w.Code != 409 {
		t.Fatal("switched during an update")
	}
	u.running = false
	// The hub pauses its own writers, so no hub.json hook is needed.
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"auto":true}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// A half-configured override is refused, not silently replaced by the
	// built-in pause.
	u.h.cfg.Update.Development.Backup = update.DevelopmentBackupConfig{Quiesce: []string{"pause"}}
	if u.developmentAvailable() {
		t.Fatal("availability accepted a quiesce without resume")
	}
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"auto":true}`); w.Code != 409 {
		t.Fatal("enabled automatic updates with a half-configured override")
	}
	u.h.cfg.Update.Development.Backup = update.DevelopmentBackupConfig{Quiesce: []string{"pause"}, Resume: []string{"resume"}}
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"auto":true}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !u.settings.DevelopmentAuto {
		t.Fatal("explicit automatic choice lost")
	}
	for _, source := range []string{"release", "web-dev"} {
		u.settings.DevelopmentBackup = &update.DevelopmentBackup{ID: "previous-channel"}
		if w := nodePolicyRequest(t, u, http.MethodPut, fmt.Sprintf(`{"source":%q}`, source)); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if u.settings.DevelopmentBackup != nil {
			t.Fatal("channel switch retained the previous backup receipt")
		}
	}
	if u.settings.DevelopmentAuto {
		t.Fatal("channel switch retained development opt-in")
	}
	fence := update.Pending{ToBuild: 202611130000, Development: &update.DevelopmentCandidate{Commit: strings.Repeat("b", 40)}}
	if err := update.BeginDevelopmentAdoption(u.dir, fence); err != nil {
		t.Fatal(err)
	}
	if w := nodePolicyRequest(t, u, http.MethodPut, `{"source":"release"}`); w.Code != 409 {
		t.Fatal("changed source across unresolved effects")
	}
}

func TestDevelopmentRepeatedCheckDoesNotMutatePublishedCandidate(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			h := newTestHub(t)
			u := h.upd
			h.cfg.Update.Development = &update.DevelopmentConfig{Backup: update.DevelopmentBackupConfig{Quiesce: []string{"pause"}, Resume: []string{"resume"}}}
			candidate := &update.DevelopmentCandidate{Branch: "main", Commit: strings.Repeat("b", 40)}
			published := &update.Published{Manifest: &update.Manifest{Version: "frozen", Build: 202611130001}, Development: candidate}
			u.release, u.running = published, active
			u.settings.History = []updateRecord{{Source: "web-dev", Commit: candidate.Commit, Result: "rolled_back", ToBuild: 202611120001, ToVersion: "previous-attempt"}}
			u.devRun = func(_ context.Context, _ *update.DevelopmentConfig, _, _ string, _ map[string]any, result any) error {
				*result.(*update.DevelopmentCandidate) = *candidate
				return nil
			}
			r, err := u.checkDevelopment(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if published.Manifest.Version != "frozen" || published.Manifest.Build != 202611130001 {
				t.Fatal("check rewrote the published/installing manifest")
			}
			if r.Manifest.Version != "previous-attempt" {
				t.Fatal("rollback attempt identity was lost")
			}
			if active && u.release != published {
				t.Fatal("active candidate was replaced")
			}
		})
	}
}

func TestDevelopmentHeldHistorySkipsUnbuiltBadAndUnresolvedCandidates(t *testing.T) {
	for _, scenario := range []string{"unbuilt", "known-bad", "unresolved", "held"} {
		t.Run(scenario, func(t *testing.T) {
			h := newTestHub(t)
			u := h.upd
			u.loaded, u.settings.Source, u.settings.DevelopmentAuto = true, "web-dev", true
			h.build.Commit = strings.Repeat("a", 40)
			commit := strings.Repeat("b", 40)
			ancestors, _ := update.EncodeAncestors([]string{commit})
			u.release = &update.Published{Manifest: &update.Manifest{Version: "candidate", Build: 202611130001, Ancestors: ancestors}, Development: &update.DevelopmentCandidate{Commit: commit}}
			switch scenario {
			case "unbuilt":
				u.release.Manifest.Ancestors = ""
			case "known-bad":
				u.settings.DevelopmentBad = []string{commit}
			case "unresolved":
				if err := update.BeginDevelopmentAdoption(u.dir, update.Pending{ToBuild: 202611130001, Development: u.release.Development}); err != nil {
					t.Fatal(err)
				}
			}
			u.noteHeld(context.Background())
			if scenario == "held" {
				if len(u.settings.History) != 1 || u.settings.History[0].Source != "web-dev" || u.settings.History[0].Commit != commit {
					t.Fatal("missing attributed hold", u.settings.History)
				}
			} else if len(u.settings.History) != 0 {
				t.Fatal("false held reason", u.settings.History)
			}
		})
	}
}

func TestDevelopmentUsesSharedInstallAndPreservesRecovery(t *testing.T) {
	for _, failure := range []string{"", "automatic", "build", "backup", "adopt", "preimage", "partial", "restore"} {
		t.Run(failure, func(t *testing.T) {
			p := newPublisher(t, "unused", 202611021700)
			h, machine, target, execs := updatingHub(t, p, "release")
			machine.info.OS, machine.info.Arch = "plan9", "arm"
			h.cfg.Machines = append(h.cfg.Machines, machine.cfg)
			u := h.upd
			u.client = newUpdater(h).client
			nodes := filepath.Join(t.TempDir(), "nodes")
			t.Setenv("PERSONA_NODE_BINARIES", nodes)
			os.MkdirAll(nodes, 0755)
			stale := filepath.Join(nodes, "personad-windows-amd64")
			os.WriteFile(stale, []byte("older unused platform"), 0600)
			u.settings.Source = "web-dev"
			u.loaded = true
			u.h.build.Commit = strings.Repeat("a", 40)
			cfg := &update.DevelopmentConfig{Backup: update.DevelopmentBackupConfig{Quiesce: []string{"fixture-pause"}, Resume: []string{"fixture-resume"}}}
			u.h.cfg.Update.Development = cfg
			web := filepath.Join(h.cfg.dir, "web")
			os.MkdirAll(web, 0700)
			os.WriteFile(filepath.Join(web, "index.html"), []byte("old web"), 0600)
			program := filepath.Join(filepath.Dir(target), "persona-hub")
			os.WriteFile(program, []byte("old companion"), 0700)
			if failure == "preimage" {
				os.Mkdir(program+".prev", 0700)
			}
			if failure == "partial" {
				// A new program cannot be installed, after the hub and existing
				// companion have already been replaced. Full restore must undo both.
				os.Mkdir(filepath.Join(filepath.Dir(target), "persona-manager.new"), 0700)
			}
			var phases []string
			u.devRun = func(ctx context.Context, c *update.DevelopmentConfig, dir, operation string, input map[string]any, result any) error {
				phases = append(phases, operation)
				if operation == failure {
					return errors.New("fixture phase failed")
				}
				var response any
				switch operation {
				case "resolve":
					response = update.DevelopmentCandidate{Branch: "main", Commit: strings.Repeat("b", 40), UpdatedAt: "2026-11-13T10:00:00Z"}
				case "build":
					wantPlatforms := []string{runtime.GOOS + "-" + runtime.GOARCH, "plan9-arm"}
					slices.Sort(wantPlatforms)
					if got := input["platforms"].([]string); !slices.Equal(got, wantPlatforms) {
						t.Fatalf("unexpected build platforms: %v", got)
					}
					artifacts := t.TempDir()
					script := []byte(fmt.Sprintf("#!/bin/sh\necho %s\necho 'channel release, build %d'\n", input["version"], input["build"]))
					os.WriteFile(filepath.Join(artifacts, "personad-"+runtime.GOOS+"-"+runtime.GOARCH+".gz"), gz(script), 0600)
					// The prepared feed can include another registered computer's platform.
					// It must come from this build even when fleet auto-update is off.
					os.WriteFile(filepath.Join(artifacts, "personad-plan9-arm.gz"), gz([]byte("development node")), 0600)
					for _, name := range update.ProgramNames {
						os.WriteFile(filepath.Join(artifacts, name+"-"+runtime.GOOS+"-"+runtime.GOARCH+".gz"), gz(script), 0600)
					}
					response = map[string]any{"artifacts": artifacts, "ancestors": []string{h.build.Commit, strings.Repeat("b", 40)}}
				case "backup":
					response = update.DevelopmentBackup{ID: "snapshot-fixture", Retained: 3, Pruned: []string{"snapshot-old-fixture"}}
				}
				data, _ := json.Marshal(response)
				return json.Unmarshal(data, result)
			}
			if failure == "adopt" {
				u.handover = func(context.Context, string, int64) error { return errors.New("fixture standby failed") }
			}
			if failure == "restore" {
				u.handover = func(context.Context, string, int64) error {
					if err := os.Remove(target + ".prev"); err != nil {
						return err
					}
					return errors.New("fixture preimage lost before standby failed")
				}
			}
			if _, err := u.check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if u.view().Latest.UpdatedAt != "2026-11-13T10:00:00Z" {
				t.Fatal("latest commit date missing from the update view")
			}
			if u.wantsAuto() {
				t.Fatal("automatic adoption before opting in")
			}
			automatic := failure == "automatic"
			if automatic {
				u.settings.DevelopmentAuto = true
				if !u.wantsAuto() {
					t.Fatal("explicit automatic update not scheduled")
				}
			}
			u.install(context.Background(), automatic)
			if failure == "restore" {
				var pending update.Pending
				if ok, err := update.Load(update.PendingPath(u.dir), &pending); !ok || err != nil {
					t.Fatal("lost pending after failed restore", err)
				}
				if !u.adoptionUnresolved() || u.view().State != "failed" {
					t.Fatal("lost recovery fence")
				}
				u.pruneDownloads(0)
				if _, err := os.Stat(pending.Nodes); err != nil {
					t.Fatal("lost staged Nodes", err)
				}
				if _, err := os.Stat(filepath.Join(u.stagedDir(pending.ToBuild), "programs")); err != nil {
					t.Fatal("lost staged programs", err)
				}
				// Restore the unavailable preimage and simulate a fresh old-build
				// startup. Web was already restored by the first attempt.
				os.WriteFile(target+".prev", []byte("#!/bin/sh\necho old\n"), 0700)
				if bootUpdater(t, u) || u.adoptionUnresolved() {
					t.Fatal("recovery could not be retried")
				}
				data, _ := os.ReadFile(target)
				if !strings.Contains(string(data), "echo old") {
					t.Fatal("old hub not restored")
				}
				return
			}
			if failure != "" && !automatic {
				if u.view().State != "failed" {
					t.Fatalf("state %+v", u.view())
				}
				data, _ := os.ReadFile(target)
				if !strings.Contains(string(data), "echo old") {
					t.Fatal("old binary lost")
				}
				data, _ = os.ReadFile(filepath.Join(web, "index.html"))
				if string(data) != "old web" {
					t.Fatal("old Web lost")
				}
				if len(execs()) != 0 {
					t.Fatal("restarted on failed preparation")
				}
				if data, _ := os.ReadFile(program); string(data) != "old companion" {
					t.Fatal("old companion lost")
				}
				if u.adoptionUnresolved() {
					t.Fatal("fully restored attempt kept an unresolved fence")
				}
				if failure == "build" && len(phases) != 2 {
					t.Fatal("backup ran after failed build")
				}
				return
			}
			if len(execs()) != 1 {
				t.Fatal("shared restart not used")
			}
			var pending update.Pending
			if ok, err := update.Load(update.PendingPath(u.dir), &pending); !ok || err != nil {
				t.Fatal(err)
			}
			if pending.Development == nil || pending.Backup == nil || pending.Nodes == "" || pending.WebPrev == "" {
				t.Fatalf("missing recovery state %+v", pending)
			}
			if pending.Auto != automatic {
				t.Fatal("manual/automatic attribution lost")
			}
			// Simulate the next process's confirmation without touching a real service.
			h.build.Number = pending.ToBuild
			h.version = pending.ToVersion
			u.confirm(context.Background(), pending)
			if _, err := os.Stat(filepath.Join(nodes, "personad-"+runtime.GOOS+"-"+runtime.GOARCH)); err != nil {
				t.Fatal("manual node package missing with automatic fleet updates off", err)
			}
			if data, err := os.ReadFile(filepath.Join(nodes, "personad-plan9-arm")); err != nil || string(data) != "development node" {
				t.Fatal("another platform did not use the prepared development feed", err)
			}
			if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unrebuilt package would claim the new Hub version", err)
			}
			if data, err := os.ReadFile(stale + ".prev"); err != nil || string(data) != "older unused platform" {
				t.Fatal("unrebuilt package was not preserved", err)
			}
			if u.view().State != "updated" {
				t.Fatal(u.view().State)
			}
		})
	}
}

func TestDevelopmentBackupKeepsUnstagedPackageIdentity(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			h := newTestHub(t)
			h.version = "saved-hub-version"
			dir, staged := t.TempDir(), t.TempDir()
			t.Setenv("PERSONA_NODE_BINARIES", dir)
			write := func(path, data string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(dir, "personad-linux-arm64"), "retained unlabelled")
			write(filepath.Join(dir, "personad-windows-amd64"), "retained labelled")
			write(filepath.Join(dir, "personad-windows-amd64.version"), "explicit-old-version\n")
			write(filepath.Join(staged, "personad-linux-amd64"), "saved package")
			if broken {
				write(filepath.Join(dir, "personad-linux-arm64.version"), "")
			}
			err := h.upd.updateNodes(context.Background(), staged, "before-rollback", &update.DevelopmentCandidate{BackupID: "snapshot-fixture"})
			if broken {
				if err == nil {
					t.Fatal("accepted an unknown retained version")
				}
				if _, err := os.Stat(filepath.Join(staged, "personad-linux-amd64")); err != nil {
					t.Fatal("lost staging", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{
				"personad-linux-arm64":           "retained unlabelled",
				"personad-linux-arm64.version":   "before-rollback\n",
				"personad-windows-amd64":         "retained labelled",
				"personad-windows-amd64.version": "explicit-old-version\n",
				"personad-linux-amd64":           "saved package",
			} {
				if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
					t.Fatal(name, string(got), err)
				}
			}
		})
	}
}
