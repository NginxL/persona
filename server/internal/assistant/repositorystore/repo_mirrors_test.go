package repositorystore

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

func TestRepoMirrorsStoreCRUDAndQueries(t *testing.T) {
	d := open(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	// Missing row reads as nil (implicitly disabled).
	got, err := d.RepoMirror(ctx, "dev1", "repo_web")
	if err != nil || got != nil {
		t.Fatalf("expected nil for unconfigured mirror: %+v %v", got, err)
	}

	// Reject auto_sync=true with enabled=false.
	if _, err := d.UpsertRepoMirrorConfig(ctx, repository.RepoMirrorRecord{
		MachineID:           "dev1",
		RepoID:              "repo_web",
		Enabled:             false,
		AutoSync:            true,
		SyncIntervalMinutes: 30,
		UpdatedAt:           now,
	}); err == nil {
		t.Fatal("expected error for auto_sync without enabled")
	}

	due := now
	saved, err := d.UpsertRepoMirrorConfig(ctx, repository.RepoMirrorRecord{
		MachineID:           "dev1",
		RepoID:              "repo_web",
		Enabled:             true,
		AutoSync:            true,
		SyncIntervalMinutes: 30,
		NextSyncAt:          &due,
		UpdatedAt:           now,
	})
	if err != nil || !saved.Enabled || !saved.AutoSync || saved.SyncIntervalMinutes != 30 {
		t.Fatalf("unexpected upsert result: %+v %v", saved, err)
	}

	enabled, err := d.ListEnabledRepoMirrors(ctx, "dev1", []string{"repo_web", "repo_api"})
	if err != nil || len(enabled) != 1 || enabled[0].RepoID != "repo_web" {
		t.Fatalf("unexpected enabled list: %+v %v", enabled, err)
	}

	dueList, err := d.ListDueAutoSyncRepoMirrors(ctx, now.Add(time.Second))
	if err != nil || len(dueList) != 1 || dueList[0].RepoID != "repo_web" {
		t.Fatalf("unexpected due list: %+v %v", dueList, err)
	}

	syncedAt := now.Add(time.Minute)
	nextSync := now.Add(31 * time.Minute)
	if err := d.SaveRepoMirrorSyncResult(ctx, "dev1", "repo_web", repository.RepoMirrorSyncUpdate{
		LastSyncedAt:        &syncedAt,
		LastHeadCommit:      "0123456789abcdef0123456789abcdef01234567",
		LastDefaultRef:      "refs/heads/main",
		DiskBytes:           8192,
		HasDiskBytes:        true,
		ConsecutiveFailures: 0,
		NextSyncAt:          &nextSync,
		UpdatedAt:           syncedAt,
	}); err != nil {
		t.Fatal(err)
	}

	// No longer due before nextSync.
	dueList, err = d.ListDueAutoSyncRepoMirrors(ctx, now.Add(10*time.Minute))
	if err != nil || len(dueList) != 0 {
		t.Fatalf("expected empty due list before nextSync: %+v %v", dueList, err)
	}

	// Updating config preserves prior sync status.
	updated, err := d.UpsertRepoMirrorConfig(ctx, repository.RepoMirrorRecord{
		MachineID:           "dev1",
		RepoID:              "repo_web",
		Enabled:             true,
		AutoSync:            false,
		SyncIntervalMinutes: 60,
		UpdatedAt:           now.Add(2 * time.Minute),
	})
	if err != nil || updated.AutoSync || updated.DiskBytes != 8192 || updated.LastHeadCommit == "" {
		t.Fatalf("expected config update to preserve sync observations: %+v %v", updated, err)
	}

	if err := d.DeleteRepoMirror(ctx, "dev1", "repo_web"); err != nil {
		t.Fatal(err)
	}
	got, err = d.RepoMirror(ctx, "dev1", "repo_web")
	if err != nil || got != nil {
		t.Fatalf("expected nil after delete: %+v %v", got, err)
	}
}
