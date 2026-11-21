package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/secretkey"
)

// pendingKeyName is a secret.key.next-* file as a key rotation names it.
func pendingKeyName(t *testing.T, key []byte) string {
	t.Helper()
	return "secret.key.next-" + secretkey.Fingerprint(key)
}

// previousFilesHolding lists the entries of the previous-files directories of
// home that keep name (as NNN-<name>).
func previousFilesHolding(t *testing.T, home, name string) []string {
	t.Helper()
	var found []string
	roots, _ := filepath.Glob(filepath.Join(home, ".persona-backup-previous-*"))
	for _, root := range roots {
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if _, kept, ok := strings.Cut(entry.Name(), "-"); ok && kept == name {
				found = append(found, filepath.Join(root, entry.Name()))
			}
		}
	}
	return found
}

func assertFile(t *testing.T, name string, want []byte) {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(raw, want) {
		errorf(t, "%s = %q, %v; want %q", name, raw, err, want)
	}
}

// A file or directory the bundle has no copy of is not the bundle's to
// replace: restore leaves it where it is instead of moving it out of the live
// install with nothing in its place. That holds for an ordinary optional file,
// a runtime directory, the knowledge base, and above all a pending key
// rotation's secret.key.next-* (the only copy of a key).
func TestRestoreLeavesWhatTheBundleLacksInPlace(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	if err := os.RemoveAll(filepath.Join(fixture.home, "data")); err != nil {
		fatal(t, err)
	}
	operator := fixture.operator()
	if _, err := operator.Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	pending := bytes.Repeat([]byte{7}, secretkey.Size)
	kept := map[string][]byte{
		"apns.json":                                []byte(`{"team":"later"}`),
		"tailcat-hub.json":                         []byte("hub tailcat identity made after the backup"),
		pendingKeyName(t, pending):                 pending,
		filepath.Join("skills", "s", "SKILL.md"):   []byte("a skill installed after the backup"),
		filepath.Join("tls", "hub.key"):            []byte("TLS identity made after the backup"),
		filepath.Join("data", "facts", "later.md"): []byte("knowledge written after the backup"),
	}
	for name, content := range kept {
		writeFixtureFile(t, filepath.Join(fixture.home, name), content)
	}
	fixture.mutate(t) // also rewrites data/facts/note.md
	result, err := operator.Restore(context.Background(), Options{Home: fixture.home, Bundle: fixture.output, WritesQuiesced: true})
	if err != nil {
		fatal(t, err)
	}
	for name, content := range kept {
		assertFile(t, filepath.Join(fixture.home, name), content)
		if moved := previousFilesHolding(t, fixture.home, filepath.Base(strings.SplitN(name, string(filepath.Separator), 2)[0])); len(moved) != 0 {
			errorf(t, "%s was also moved to the previous files: %v", name, moved)
		}
	}
	// What the bundle has is still replaced, its previous copy kept and named.
	assertFile(t, filepath.Join(fixture.home, "hub.json"), []byte(`{"database_url":"`+fixture.hub.URL+`","token":"hub-token-secret"}`))
	if result.PreviousFiles == "" || len(previousFilesHolding(t, fixture.home, "hub.json")) != 1 {
		errorf(t, "the replaced hub.json is not in the named previous files %q: %v", result.PreviousFiles, previousFilesHolding(t, fixture.home, "hub.json"))
	}
}

// A pending rotation key is never moved away: one the install has stays, even
// when the bundle has a file of the same name; one only the bundle has is
// installed.
func TestRestoreNeverMovesAPendingRotationKey(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	shared := bytes.Repeat([]byte{8}, secretkey.Size)
	bundled := bytes.Repeat([]byte{9}, secretkey.Size)
	writeFixtureFile(t, filepath.Join(fixture.home, pendingKeyName(t, shared)), shared)
	writeFixtureFile(t, filepath.Join(fixture.home, pendingKeyName(t, bundled)), bundled)
	operator := fixture.operator()
	if _, err := operator.Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	if err := os.Remove(filepath.Join(fixture.home, pendingKeyName(t, bundled))); err != nil {
		fatal(t, err)
	}
	live, err := os.Stat(filepath.Join(fixture.home, pendingKeyName(t, shared)))
	if err != nil {
		fatal(t, err)
	}
	fixture.mutate(t)
	if _, err := operator.Restore(context.Background(), Options{Home: fixture.home, Bundle: fixture.output, WritesQuiesced: true}); err != nil {
		fatal(t, err)
	}
	after, err := os.Stat(filepath.Join(fixture.home, pendingKeyName(t, shared)))
	if err != nil || !os.SameFile(live, after) {
		errorf(t, "the install's pending key %s was replaced or moved: %v", pendingKeyName(t, shared), err)
	}
	if moved := previousFilesHolding(t, fixture.home, pendingKeyName(t, shared)); len(moved) != 0 {
		errorf(t, "the install's pending key was moved to the previous files: %v", moved)
	}
	assertFile(t, filepath.Join(fixture.home, pendingKeyName(t, bundled)), bundled)
}

// A failed restore leaves what the bundle lacks untouched as well, and removes
// the previous-files directory once everything is back.
func TestFailedRestoreLeavesWhatTheBundleLacksAndNoPreviousFiles(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	operator := fixture.operator()
	if _, err := operator.Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	pending := bytes.Repeat([]byte{7}, secretkey.Size)
	writeFixtureFile(t, filepath.Join(fixture.home, "link.json"), []byte("link"))
	writeFixtureFile(t, filepath.Join(fixture.home, pendingKeyName(t, pending)), pending)
	fixture.mutate(t)
	fixture.backend.failInstallPart = "manager"
	if _, err := operator.Restore(context.Background(), Options{Home: fixture.home, Bundle: fixture.output, WritesQuiesced: true}); err == nil {
		fatal(t, "restore succeeded")
	}
	assertFile(t, filepath.Join(fixture.home, "link.json"), []byte("link"))
	assertFile(t, filepath.Join(fixture.home, pendingKeyName(t, pending)), pending)
	if got := readNote(t, fixture); got != "changed knowledge" {
		errorf(t, "files were not rolled back: %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(fixture.home, ".persona-backup-previous-*")); len(left) != 0 {
		errorf(t, "previous files remain after the rollback: %v", left)
	}
}

// The built-in manager's own pending key goes with its directory: it belongs to
// the previous manager database (kept as persona_previous_m_*), and a restored
// manager would delete a key file its database does not name at its start
// (keyRotation.removeOtherNexts). So it is in the named previous files.
func TestRestoreKeepsThePreviousManagersPendingKeyWithItsDirectory(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	operator := fixture.operator()
	if _, err := operator.Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	pending := bytes.Repeat([]byte{6}, secretkey.Size)
	writeFixtureFile(t, filepath.Join(fixture.home, "managers", "assistant", pendingKeyName(t, pending)), pending)
	fixture.mutate(t)
	result, err := operator.Restore(context.Background(), Options{Home: fixture.home, Bundle: fixture.output, WritesQuiesced: true})
	if err != nil {
		fatal(t, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.home, "managers", "assistant", pendingKeyName(t, pending))); !errors.Is(err, os.ErrNotExist) {
		errorf(t, "the previous manager's pending key is beside the restored manager: %v", err)
	}
	dirs := previousFilesHolding(t, fixture.home, "assistant")
	if len(dirs) != 1 || !strings.HasPrefix(dirs[0], result.PreviousFiles) {
		fatalf(t, "previous manager directory %v not under the named previous files %q", dirs, result.PreviousFiles)
	}
	assertFile(t, filepath.Join(dirs[0], pendingKeyName(t, pending)), pending)
}
