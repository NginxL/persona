package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateOutputDirectoryCreatesPrivateDirectory(t *testing.T) {
	home := t.TempDir()
	want := RecommendedDevelopmentBackupDir(home)
	got, err := ValidateOutputDirectory(home, want)
	if err != nil {
		fatal(t, err)
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		fatal(t, err)
	}
	if got != filepath.Join(resolvedHome, "update", "development-backups") {
		fatalf(t, "resolved directory = %q", got)
	}
	info, err := os.Stat(got)
	if err != nil {
		fatal(t, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		fatalf(t, "directory mode %04o is not private", info.Mode().Perm())
	}
}

func TestValidateOutputDirectoryExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := ValidateOutputDirectory(home, "~/snapshots")
	if err != nil {
		fatal(t, err)
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		fatal(t, err)
	}
	if got != filepath.Join(resolvedHome, "snapshots") {
		fatalf(t, "resolved directory = %q", got)
	}
}

func TestValidateOutputDirectoryRefusals(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "data"), 0o700); err != nil {
		fatal(t, err)
	}
	symlink := filepath.Join(home, "linked")
	if err := os.Symlink(filepath.Join(home, "elsewhere"), symlink); err != nil {
		fatal(t, err)
	}
	public := filepath.Join(home, "public")
	if err := os.Mkdir(public, 0o700); err != nil {
		fatal(t, err)
	}
	if err := os.Chmod(public, 0o755); err != nil {
		fatal(t, err)
	}
	file := filepath.Join(home, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		fatal(t, err)
	}
	for name, c := range map[string]struct {
		dir  string
		want string
	}{
		"relative":         {"relative/dir", "absolute"},
		"root":             {"/", "filesystem root"},
		"knowledge base":   {filepath.Join(home, "data", "snapshots"), "which the backup collects"},
		"manager files":    {filepath.Join(home, "managers", "assistant", "snapshots"), "which the backup collects"},
		"symlink":          {symlink, "symbolic link"},
		"public directory": {public, "readable by other users"},
		"not a directory":  {file, "not a directory"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateOutputDirectory(home, c.dir); err == nil || !strings.Contains(err.Error(), c.want) {
				fatalf(t, "ValidateOutputDirectory(%q) = %v, want %q", c.dir, err, c.want)
			}
		})
	}
	if _, err := ValidateOutputDirectory(home, ""); err == nil {
		fatal(t, "accepted an empty directory")
	}
	if filepath.Separator != '\\' {
		backslash := filepath.Join(t.TempDir(), `snap\shots`)
		if _, err := ValidateOutputDirectory(home, backslash); err == nil || !strings.Contains(err.Error(), "backslash") {
			fatalf(t, "ValidateOutputDirectory(%q) = %v, want backslash refusal", backslash, err)
		}
		if _, err := os.Lstat(backslash); !errors.Is(err, os.ErrNotExist) {
			fatalf(t, "refused backslash path was created: %v", err)
		}
		nul := filepath.Join(t.TempDir(), "snap\x00shots")
		if _, err := ValidateOutputDirectory(home, nul); err == nil || !strings.Contains(err.Error(), "NUL") {
			fatalf(t, "ValidateOutputDirectory(%q) = %v, want NUL refusal", nul, err)
		}
	}
}

func TestValidateOutputDirectoryAllowsExistingBackslashPrefixes(t *testing.T) {
	if filepath.Separator == '\\' {
		skip(t, "POSIX backslash semantics")
	}
	base := t.TempDir()
	home := filepath.Join(base, `persona\prod`)
	if err := os.Mkdir(home, 0o700); err != nil {
		fatal(t, err)
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		fatal(t, err)
	}

	// Recommended backup directory under a runtime home containing a backslash.
	recommended := RecommendedDevelopmentBackupDir(home)
	got, err := ValidateOutputDirectory(home, recommended)
	if err != nil {
		fatalf(t, "ValidateOutputDirectory(recommended under backslash home) = %v", err)
	}
	wantRecommended := filepath.Join(resolvedHome, "update", "development-backups")
	if got != wantRecommended {
		fatalf(t, "resolved recommended = %q, want %q", got, wantRecommended)
	}

	// Existing private directory whose own name contains a backslash.
	existing := filepath.Join(base, `existing\backups`)
	if err := os.Mkdir(existing, 0o700); err != nil {
		fatal(t, err)
	}
	resolvedExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		fatal(t, err)
	}
	got, err = ValidateOutputDirectory(home, existing)
	if err != nil {
		fatalf(t, "ValidateOutputDirectory(existing backslash dir) = %v", err)
	}
	if got != resolvedExisting {
		fatalf(t, "resolved existing = %q, want %q", got, resolvedExisting)
	}

	// Symlink parent resolving to a directory containing a backslash.
	link := filepath.Join(base, "link-to-backslash")
	if err := os.Symlink(existing, link); err != nil {
		fatal(t, err)
	}
	got, err = ValidateOutputDirectory(home, filepath.Join(link, "snapshots"))
	if err != nil {
		fatalf(t, "ValidateOutputDirectory(through symlink to backslash dir) = %v", err)
	}
	if got != filepath.Join(resolvedExisting, "snapshots") {
		fatalf(t, "resolved symlink child = %q, want %q", got, filepath.Join(resolvedExisting, "snapshots"))
	}

	// Newly created components containing a backslash remain refused and create nothing.
	newBad := filepath.Join(home, `new\dir`, "snapshots")
	if _, err := ValidateOutputDirectory(home, newBad); err == nil || !strings.Contains(err.Error(), "backslash") {
		fatalf(t, "ValidateOutputDirectory(%q) = %v, want backslash refusal", newBad, err)
	}
	if _, err := os.Lstat(filepath.Join(home, `new\dir`)); !errors.Is(err, os.ErrNotExist) {
		fatalf(t, "refused new backslash component was created: %v", err)
	}
}

func TestRecommendedDevelopmentBackupDirCanonicalizesSymlinks(t *testing.T) {
	realHome := filepath.Join(t.TempDir(), "real-home")
	if err := os.Mkdir(realHome, 0o700); err != nil {
		fatal(t, err)
	}
	linkHome := filepath.Join(t.TempDir(), "link-home")
	if err := os.Symlink(realHome, linkHome); err != nil {
		fatal(t, err)
	}
	resolvedHome, err := filepath.EvalSymlinks(realHome)
	if err != nil {
		fatal(t, err)
	}
	got := RecommendedDevelopmentBackupDir(linkHome)
	want := filepath.Join(resolvedHome, "update", "development-backups")
	if got != want {
		fatalf(t, "RecommendedDevelopmentBackupDir(symlink) = %q, want %q", got, want)
	}
	if !SamePath(filepath.Join(linkHome, "update", "development-backups"), want) {
		fatal(t, "SamePath did not treat symlinked and resolved recommended paths as equal")
	}
	if SamePath(filepath.Join(linkHome, "other"), want) {
		fatal(t, "SamePath matched distinct paths")
	}
}

func TestRecommendedDevelopmentBackupDirPreservesFinalSymlinkForRefusal(t *testing.T) {
	realHome := filepath.Join(t.TempDir(), "real-home")
	if err := os.MkdirAll(filepath.Join(realHome, "update"), 0o700); err != nil {
		fatal(t, err)
	}
	target := filepath.Join(t.TempDir(), "symlink-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		fatal(t, err)
	}
	if err := os.Symlink(target, filepath.Join(realHome, "update", "development-backups")); err != nil {
		fatal(t, err)
	}
	linkHome := filepath.Join(t.TempDir(), "link-home")
	if err := os.Symlink(realHome, linkHome); err != nil {
		fatal(t, err)
	}
	resolvedHome, err := filepath.EvalSymlinks(realHome)
	if err != nil {
		fatal(t, err)
	}
	got := RecommendedDevelopmentBackupDir(linkHome)
	want := filepath.Join(resolvedHome, "update", "development-backups")
	if got != want {
		fatalf(t, "RecommendedDevelopmentBackupDir(final symlink) = %q, want %q", got, want)
	}
	if _, err := ValidateOutputDirectory(linkHome, got); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		fatalf(t, "ValidateOutputDirectory(%q) = %v, want symbolic link refusal", got, err)
	}
	if !SamePath(got, target) {
		fatal(t, "SamePath did not resolve final symlink for equivalence comparison")
	}
}

// A symlinked parent is resolved, and a path that reaches a collected tree
// through one is still refused.
func TestValidateOutputDirectoryFollowsParentsButRefusesCollectedTargets(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "data"), 0o700); err != nil {
		fatal(t, err)
	}
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(elsewhere, 0o700); err != nil {
		fatal(t, err)
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(elsewhere, link); err != nil {
		fatal(t, err)
	}
	got, err := ValidateOutputDirectory(home, filepath.Join(link, "snapshots"))
	if err != nil {
		fatal(t, err)
	}
	resolvedElsewhere, err := filepath.EvalSymlinks(elsewhere)
	if err != nil {
		fatal(t, err)
	}
	if got != filepath.Join(resolvedElsewhere, "snapshots") {
		fatalf(t, "resolved directory = %q", got)
	}

	intoKB := filepath.Join(home, "intolink")
	if err := os.Symlink(filepath.Join(home, "data"), intoKB); err != nil {
		fatal(t, err)
	}
	if _, err := ValidateOutputDirectory(home, intoKB); err == nil || !strings.Contains(err.Error(), "which the backup collects") {
		fatalf(t, "accepted a symlink into the knowledge base: %v", err)
	}
	if _, err := ValidateOutputDirectory(home, filepath.Join(intoKB, "snapshots")); err == nil {
		fatal(t, "accepted a directory reached through a link into the knowledge base")
	}
}

// A store that is the runtime or contains it cannot be left out of the
// archive without dropping the runtime: refused, for the path as given and
// through a symlink.
func TestValidateOutputDirectoryRefusesTheRuntimeAndItsAncestors(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "data"), 0o700); err != nil {
		fatal(t, err)
	}
	for name, c := range map[string]struct{ dir, want string }{
		"the runtime":        {home, "may not contain"},
		"an ancestor":        {filepath.Dir(home), "may not contain"},
		"the knowledge base": {filepath.Join(home, "data"), "which the backup collects"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateOutputDirectory(home, c.dir); err == nil || !strings.Contains(err.Error(), c.want) {
				fatalf(t, "ValidateOutputDirectory(%q) = %v, want %q", c.dir, err, c.want)
			}
		})
	}
	// A symlink that only makes the chosen folder reach the runtime is caught
	// before anything is created.
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(filepath.Dir(home), link); err != nil {
		fatal(t, err)
	}
	if _, err := ValidateOutputDirectory(home, link); err == nil || !strings.Contains(err.Error(), "may not contain") {
		fatalf(t, "accepted an ancestor reached through a link: %v", err)
	}
}

// A refused path through a symlinked parent leaves no directory behind.
func TestValidateOutputDirectoryCreatesNothingWhenRefused(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "data"), 0o700); err != nil {
		fatal(t, err)
	}
	intoKB := filepath.Join(home, "intolink")
	if err := os.Symlink(filepath.Join(home, "data"), intoKB); err != nil {
		fatal(t, err)
	}
	if _, err := ValidateOutputDirectory(home, filepath.Join(intoKB, "snapshots")); err == nil {
		fatal(t, "accepted a directory reached through a link into the knowledge base")
	}
	if _, err := os.Lstat(filepath.Join(home, "data", "snapshots")); !errors.Is(err, os.ErrNotExist) {
		fatalf(t, "the refused path was created: %v", err)
	}
}

func TestUnrecoveredSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := UnrecoveredSnapshot(dir); err != nil {
		fatalf(t, "an empty folder needs recovery: %v", err)
	}
	if err := UnrecoveredSnapshot(""); err != nil {
		fatalf(t, "no folder needs recovery: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coordination-pending.json"), []byte("{}"), 0o600); err != nil {
		fatal(t, err)
	}
	if err := UnrecoveredSnapshot(dir); err == nil || !strings.Contains(err.Error(), "coordination") {
		fatalf(t, "coordination marker = %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "coordination-pending.json")); err != nil {
		fatal(t, err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".incomplete-fixture"), 0o700); err != nil {
		fatal(t, err)
	}
	if err := UnrecoveredSnapshot(dir); err == nil || !strings.Contains(err.Error(), "incomplete") {
		fatalf(t, "incomplete snapshot = %v", err)
	}
}
