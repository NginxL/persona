package hub

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/operations/backup"
	"github.com/nwverse/persona/server/internal/update"
)

// developmentBackupRetainDefault is how many verified snapshots are kept when
// the operator did not choose another number.
const developmentBackupRetainDefault = 3

type developmentView struct {
	Configured bool   `json:"configured"`
	Why        string `json:"why,omitempty"` // localized: why it cannot be selected
	Branch     string `json:"branch"`
	// BackupDirectory is the effective snapshot folder, BackupDefaultDirectory
	// the recommended one under the runtime; BackupRetain how many verified
	// snapshots are kept. BuiltinQuiesce: the hub pauses its own writers.
	BackupDirectory        string `json:"backup_directory,omitempty"`
	BackupDefaultDirectory string `json:"backup_default_directory,omitempty"`
	BackupRetain           int    `json:"backup_retain,omitempty"`
	BuiltinQuiesce         bool   `json:"builtin_quiesce,omitempty"`
	CurrentCommit          string `json:"current_commit,omitempty"`
	CurrentUpdatedAt       string `json:"current_updated_at,omitempty"`
}

// Installation time is recorded by confirmation, never inferred from a build or Git date.
// Caller holds u.mu.
func (u *updater) currentUpdatedAtLocked() string {
	for i := len(u.settings.History) - 1; i >= 0; i-- {
		r := u.settings.History[i]
		if r.Result == "updated" && r.ToVersion == u.h.version && r.ToBuild == u.h.build.Number && !r.Ended.IsZero() {
			return r.Ended.Format(time.RFC3339)
		}
	}
	return ""
}

func (u *updater) developmentConfig() *update.DevelopmentConfig {
	if u.h.cfg == nil {
		return nil
	}
	return u.h.cfg.Update.Development
}

// developmentAvailable: the hub can prepare Web development updates. The hub
// pauses its own writers for the snapshot by default (quiesceDevelopmentWriters);
// hub.json's quiesce/resume hooks are an optional advanced override and must be
// configured as a pair. The topology check is part of availability so the Web UI
// does not offer a channel that its first check would refuse.
func (u *updater) developmentAvailable() bool {
	if u.h.cfg == nil || u.h.build.Channel == update.Docker || runtime.GOOS == "windows" {
		return false
	}
	if c := u.developmentConfig(); c != nil && !c.Backup.Coordinated() {
		return false
	}
	return u.developmentTopology() == nil
}

// developmentUnavailableWhy is what the Web UI says when development updates
// cannot be selected, localized; "" for a Docker hub, which has its own view.
func (u *updater) developmentUnavailableWhy(l i18n.Lang) string {
	if u.h.cfg == nil {
		return ""
	}
	if u.h.build.Channel == update.Docker {
		return i18n.Localize(i18n.Err("hub-selfupdate.err_development_docker"), l)
	}
	if runtime.GOOS == "windows" {
		return i18n.Localize(i18n.Err("hub-selfupdate.err_development_platform"), l)
	}
	if c := u.developmentConfig(); c != nil && !c.Backup.Coordinated() {
		return i18n.Localize(i18n.Err("hub-selfupdate.err_development_config"), l)
	}
	return i18n.Localize(i18n.Err("hub-selfupdate.err_development_topology"), l)
}

// developmentBackupConfigLocked is the snapshot configuration in effect: the
// folder chosen in the Web UI (which may be the recommended one an operator
// chose over hub.json's), else hub.json's, else the recommended one under the
// runtime's update/ directory; the chosen retention, else three. Builtin says
// the hub itself pauses the writers. Caller holds u.mu.
func (u *updater) developmentBackupConfigLocked() update.DevelopmentBackupConfig {
	var c update.DevelopmentBackupConfig
	if u.h.cfg != nil && u.h.cfg.Update.Development != nil {
		c = u.h.cfg.Update.Development.Backup
	}
	if u.settings.DevelopmentBackupDirectory != nil {
		c.Directory = *u.settings.DevelopmentBackupDirectory
	} else if c.Directory == "" && u.h.cfg != nil {
		c.Directory = backup.RecommendedDevelopmentBackupDir(u.h.cfg.dir)
	}
	if u.settings.DevelopmentBackupRetain > 0 {
		c.Retain = u.settings.DevelopmentBackupRetain
	}
	if c.Retain <= 0 {
		c.Retain = developmentBackupRetainDefault
	}
	c.Builtin = !c.ExternalQuiesce()
	return c
}

// developmentRequestConfig is what one helper phase receives, with the snapshot
// settings resolved.
func (u *updater) developmentRequestConfig() *update.DevelopmentConfig {
	u.mu.Lock()
	defer u.mu.Unlock()
	return &update.DevelopmentConfig{Backup: u.developmentBackupConfigLocked()}
}

func (u *updater) developmentLocked() bool { return u.settings.Source == "web-dev" }

func (u *updater) adoptionUnresolved() bool {
	_, err := os.Lstat(update.DevelopmentAdoptionPath(u.dir))
	return !errors.Is(err, os.ErrNotExist)
}

func (u *updater) autoLocked() bool {
	if u.developmentLocked() {
		return u.settings.DevelopmentAuto
	}
	return u.settings.auto()
}

func (u *updater) selectedDevelopment() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.developmentLocked()
}

func (u *updater) candidateNewer(r *update.Published) bool {
	if r == nil {
		return false
	}
	if r.Development != nil {
		return r.Development.BackupID == "" && r.Development.Commit != u.h.build.Commit
	}
	return u.h.build.Newer(r.Manifest.Build)
}

func (u *updater) developmentHold(m *update.Manifest) string {
	// A known clean installed commit must be an ancestor for automatic adoption.
	// Unknown/dirty builds can still be updated manually, like release updates.
	return u.h.build.Hold(m)
}

func (u *updater) unreplacedCandidate(r *update.Published) error {
	if r.Development == nil {
		return u.unreplaced(r.Manifest)
	}
	// A rollback can have a lower build number, but must still replace every active split service.
	target, err := u.target()
	if err != nil {
		return err
	}
	self, _ := u.self()
	return unreplaced(r.Manifest, target, self, u.services)
}

func (u *updater) runDevelopment(ctx context.Context, operation string, input map[string]any, result any) error {
	if err := u.devRun(ctx, u.developmentRequestConfig(), filepath.Join(u.dir, "development"), operation, input, result); err != nil {
		return i18n.Err("hub-selfupdate.err_development_phase", i18n.Args{"phase": operation})
	}
	return nil
}

// quiesceDevelopmentWriters pauses the writers a snapshot must not see moving.
// With hub.json's quiesce/resume hooks the helper runs them itself; otherwise
// the hub pauses itself: request writes, the built-in child manager and the
// background writers, then checks both databases with backup.Quiesced rules.
// The returned resume always runs, including after a partial pause.
func (u *updater) quiesceDevelopmentWriters(ctx context.Context, cfg update.DevelopmentBackupConfig) (func(), error) {
	if !cfg.Coordinated() {
		return nil, i18n.Err("hub-selfupdate.err_development_config")
	}
	if cfg.ExternalQuiesce() {
		return func() {}, nil
	}
	if u.h.cfg == nil || !u.h.cfg.Assistant.StartsBuiltin() {
		return nil, i18n.Err("hub-selfupdate.err_development_topology")
	}
	if err := u.h.pauseDevelopmentWriters(ctx); err != nil {
		return nil, i18n.Err("hub-selfupdate.err_development_quiesce", i18n.Args{"error": i18n.Localize(err, u.h.userLang())})
	}
	resumed := false
	resume := func() {
		if resumed {
			return
		}
		resumed = true
		u.h.resumeDevelopmentWriters()
	}
	if err := u.waitDevelopmentQuiesced(ctx); err != nil {
		resume()
		return nil, i18n.Err("hub-selfupdate.err_development_quiesce", i18n.Args{"error": i18n.Localize(err, u.h.userLang())})
	}
	return resume, nil
}

// waitDevelopmentQuiesced checks the databases the snapshot dumps with the
// backup command's own Quiesced rules, after giving the manager's connections a
// moment to close. Only this hub's pool sessions count as its own.
func (u *updater) waitDevelopmentQuiesced(ctx context.Context) error {
	pids, ok := u.h.store.(interface{ BackendPIDs() []int32 })
	if !ok {
		return nil // no real database in this hub (tests): nothing to verify
	}
	if u.h.cfg == nil || u.h.cfg.DatabaseURL == "" {
		return errors.New("the hub has no database connection to verify")
	}
	// The same targets webdev.py's inventory dumps: a built-in manager whose
	// tables are still in the hub database needs only that one checked, and one
	// that moved to its own is checked separately.
	hubTarget, managerTarget, shared, err := backup.DiscoverDevelopment(backup.Options{Home: u.h.cfg.dir, DatabaseURL: u.h.cfg.DatabaseURL})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := backup.QuiescedExcept(ctx, hubTarget, hubTarget.Database, pids.BackendPIDs()...)
		if err == nil && !shared {
			err = backup.QuiescedDatabase(ctx, managerTarget, managerTarget.Database, true)
		}
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (u *updater) checkDevelopment(ctx context.Context) (*update.Published, error) {
	if !u.developmentAvailable() {
		return nil, i18n.Err("hub-selfupdate.err_development_config")
	}
	if err := u.developmentTopology(); err != nil {
		return nil, err
	}
	var candidate update.DevelopmentCandidate
	err := u.runDevelopment(ctx, "resolve", nil, &candidate)
	u.mu.Lock()
	u.lastCheck = time.Now()
	u.mu.Unlock()
	if err != nil {
		return nil, err
	}
	number := update.DevelopmentNumber(u.h.build.Number)
	m := &update.Manifest{Version: fmt.Sprintf("web-dev-%s-%d", candidate.Commit[:12], number), Build: number,
		Commit: candidate.Commit, Released: time.Now()}
	r := &update.Published{Manifest: m, Development: &candidate}
	u.mu.Lock()
	// A repeated check of the same object retains the attempt identity (including
	// its bad-build marker), rather than creating a new build to bypass rollback.
	if u.release != nil && u.release.Development != nil && u.release.Development.BackupID == "" && u.release.Development.Commit == candidate.Commit {
		copy := *u.release
		manifest := *copy.Manifest
		copy.Manifest = &manifest
		r = &copy
	}
	for _, record := range u.settings.History {
		if record.Source == "web-dev" && record.Commit == candidate.Commit && record.Result == "rolled_back" {
			r.Manifest.Build = record.ToBuild
			r.Manifest.Version = record.ToVersion
		}
	}
	if !u.updateActiveLocked() {
		u.release, u.blocked = r, nil
	}
	u.mu.Unlock()
	return r, nil
}

// Build only this Hub and the platforms its registered computers last reported.
// Offline computers keep their platform; never-seen computers supply none yet.
func (u *updater) developmentPlatforms() []string {
	platforms := []string{runtime.GOOS + "-" + runtime.GOARCH}
	for _, mc := range u.h.knownEntries() {
		if m := u.h.machineByID(mc.ID); m != nil {
			s := m.snapshot()
			if s.OS != "" && s.Arch != "" {
				platforms = append(platforms, s.OS+"-"+s.Arch)
			}
		}
	}
	slices.Sort(platforms)
	return slices.Compact(platforms)
}

func (u *updater) buildDevelopment(ctx context.Context, r *update.Published) error {
	operation := "build"
	state := "building"
	if r.Development.BackupID != "" {
		operation, state = "prepare_backup", "applying_backup"
	}
	u.set(state, "web/hub/node", "")
	var built struct {
		Artifacts string   `json:"artifacts"`
		Ancestors []string `json:"ancestors"`
	}
	platforms := u.developmentPlatforms()
	if operation == "prepare_backup" && !u.nodesEnabled() {
		platforms = []string{runtime.GOOS + "-" + runtime.GOARCH}
	}
	err := u.runDevelopment(ctx, operation, map[string]any{
		"candidate": r.Development, "version": r.Manifest.Version, "build": r.Manifest.Build,
		"backup_id": r.Development.BackupID, "runtime": u.h.cfg.dir,
		"platform": runtime.GOOS + "-" + runtime.GOARCH, "platforms": platforms, "release_url": u.h.updateURL()}, &built)
	if err != nil {
		return err
	}
	r.Source = (&url.URL{Scheme: "file", Path: built.Artifacts}).String()
	var ancestors string
	if operation == "build" {
		ancestors, err = update.EncodeAncestors(built.Ancestors)
		if err != nil {
			return err
		}
	}
	m, err := update.BuildManifest(built.Artifacts, *r.Manifest)
	if err != nil {
		return err
	}
	m.Ancestors = ancestors
	r.Manifest = m
	return nil
}

func (u *updater) backupDevelopment(ctx context.Context, r *update.Published, target string) (*update.DevelopmentBackup, error) {
	if err := u.developmentTopology(); err != nil {
		return nil, err
	}
	// Immutable package pointers belong to the release publisher. Do not alter it
	// or silently leave development nodes on the previous release.
	if info, err := os.Lstat(identity.NodeBinaryDir()); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("node package directory is publisher-managed; configure a dedicated development installation")
	}
	u.set("backing_up", "", "")
	programs := map[string]string{"personad": target}
	for _, name := range update.ProgramNames {
		path := filepath.Join(filepath.Dir(target), name)
		if _, err := os.Stat(path); err == nil {
			programs[name] = path
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	var saved update.DevelopmentBackup
	u.mu.Lock()
	updatedAt := u.currentUpdatedAtLocked()
	cfg := u.developmentBackupConfigLocked()
	u.mu.Unlock()
	// The hub's own writers pause around every dump and the file archive; with
	// hub.json's hooks the helper does it instead.
	resume, err := u.quiesceDevelopmentWriters(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer resume()
	err = u.runDevelopment(ctx, "backup", map[string]any{
		"runtime": u.h.cfg.dir, "candidate": r.Development, "hub_database": u.h.cfg.DatabaseURL,
		"from_version": u.h.version, "from_commit": u.h.build.Commit, "from_build": u.h.build.Number,
		"from_updated_at": updatedAt, "platform": runtime.GOOS + "-" + runtime.GOARCH,
		"executables": programs, "node_directory": identity.NodeBinaryDir()}, &saved)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.settings.DevelopmentBackup = &saved
	err = u.saveLocked(ctx)
	u.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

// Only the built-in child manager is known to share this Hub's package directory
// and restart with it. Independent managers keep their original update channel.
func (u *updater) developmentTopology() error {
	if u.h.cfg == nil || !u.h.cfg.Assistant.StartsBuiltin() {
		return i18n.Err("hub-selfupdate.err_development_topology")
	}
	for _, mc := range u.h.knownEntries() {
		if u.h.entryOwner(mc.ID) != devicesOwner {
			return i18n.Err("hub-selfupdate.err_development_topology")
		}
	}
	return nil
}

func pendingSource(p update.Pending) string {
	if p.Development != nil {
		return "web-dev"
	}
	return "release"
}

func pendingCommit(p update.Pending) string {
	if p.Development != nil {
		return p.Development.Commit
	}
	return ""
}

// detachDevelopmentWeb makes the newly embedded Web authoritative. Keep a local
// override with the same transaction so rollback restores the old UI too.
func (u *updater) detachDevelopmentWeb(p *update.Pending) error {
	return update.DetachDevelopmentWeb(u.dir, filepath.Join(u.h.cfg.dir, "web"), p)
}

// Remove only the checkout built by this exact invocation, after the existing
// installer has copied its artifacts. The owned helper directory is not a Git checkout supplied by a user.
func (u *updater) removeDevelopmentBuild(r *update.Published) {
	if r.Source == "" {
		return
	}
	parsed, err := url.Parse(r.Source)
	if err != nil || parsed.Scheme != "file" {
		return
	}
	root := filepath.Dir(parsed.Path)
	if filepath.Dir(root) == filepath.Join(u.dir, "development") && (strings.HasPrefix(filepath.Base(root), "build-") || strings.HasPrefix(filepath.Base(root), "prepare_backup-")) {
		_ = os.RemoveAll(root)
	}
}
