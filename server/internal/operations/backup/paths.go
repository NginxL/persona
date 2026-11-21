package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RecommendedDevelopmentBackupDir is where a Web development update keeps its
// snapshots unless the operator chose another directory: under the runtime's
// update/ directory, which a snapshot never collects, so a snapshot cannot
// contain an earlier one.
func RecommendedDevelopmentBackupDir(home string) string {
	return filepath.Join(home, "update", "development-backups")
}

// ValidateOutputDirectory checks and prepares a snapshot directory chosen in
// the Web UI and returns the canonical absolute path. It applies the same rules
// as Create's --output: the directory may not be inside a tree a snapshot
// collects (the knowledge base, managers/assistant, extensions, skills,
// hub.machines_kept or tls), nor contain the runtime directory or the
// knowledge base (the archive leaves the store out, so such a store would drop
// what the snapshot is for). Snapshots hold database dumps, configuration and encryption
// keys, so the directory is created with 0700 permissions and must already be
// private when it exists; it may not be a symbolic link.
func ValidateOutputDirectory(home, dir string) (string, error) {
	name, err := expandHome(dir)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(name) {
		return "", errors.New("choose an absolute path, or one starting with ~")
	}
	clean := filepath.Clean(name)
	if clean == string(filepath.Separator) {
		return "", errors.New("the filesystem root is not a backup directory")
	}
	// Validate before creating anything, in both the path as given and as its
	// symlinked parents resolve it: a refused path leaves no directory behind
	// inside the data a snapshot collects.
	resolved := resolveExisting(clean)
	for _, candidate := range []string{clean, resolved} {
		if err := refuseOutputInsideCollectedTree(home, filepath.Join(candidate, ".persona-snapshot")); err != nil {
			return "", err
		}
		if err := refuseOutputAroundCollectedRoots(home, candidate); err != nil {
			return "", err
		}
	}
	info, err := os.Lstat(clean)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(clean, 0o700); err != nil {
			return "", err
		}
		info, err = os.Lstat(clean)
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("the backup directory is a symbolic link; choose a real directory")
	}
	if !info.IsDir() {
		return "", errors.New("the backup path is not a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("the backup directory is readable by other users (%04o); run chmod 700 on it", info.Mode().Perm())
	}
	// The directory now exists: resolve it again and repeat the checks, so a
	// link made between the checks and the creation is caught too.
	resolved, err = filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	if err := refuseOutputInsideCollectedTree(home, filepath.Join(resolved, ".persona-snapshot")); err != nil {
		return "", err
	}
	if err := refuseOutputAroundCollectedRoots(home, resolved); err != nil {
		return "", err
	}
	if info, err := os.Stat(resolved); err != nil {
		return "", err
	} else if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("the backup directory is readable by other users (%04o); run chmod 700 on it", info.Mode().Perm())
	}
	return resolved, nil
}

// refuseOutputAroundCollectedRoots refuses a directory that is the runtime
// home itself or contains it, or that contains an external knowledge base: a
// snapshot leaves its own store out of the archive, which would also leave out
// everything below it.
func refuseOutputAroundCollectedRoots(home, output string) error {
	homeAbs, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	_, kbDir, err := knowledgeBaseLocator(homeAbs)
	if err != nil {
		return err
	}
	for _, root := range []string{homeAbs, kbDir} {
		for _, base := range pathForms(root) {
			if pathWithin(output, base) {
				return fmt.Errorf("the backup directory may not contain %s: choose a folder outside the runtime directory and the knowledge base", root)
			}
		}
	}
	return nil
}

// UnrecoveredSnapshot explains why a snapshot folder may not be left: the
// helper's coordination marker (a resume hook that did not finish) and its
// incomplete snapshots both stop a retry until someone looked at them, so a
// new folder would be paused and dumped again past those gates.
func UnrecoveredSnapshot(dir string) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(dir, "coordination-pending.json")); err == nil {
		return errors.New("the current snapshot folder has a coordination marker from a resume that did not finish; inspect it first")
	}
	incomplete, err := filepath.Glob(filepath.Join(dir, ".incomplete-*"))
	if err != nil {
		return err
	}
	if len(incomplete) > 0 {
		return fmt.Errorf("the current snapshot folder has an incomplete snapshot (%s); inspect it first", filepath.Base(incomplete[0]))
	}
	return nil
}

// expandHome expands a leading ~ or ~/ to the user's home directory. Other
// forms (~user) are left alone, so they fail the absolute-path check.
func expandHome(name string) (string, error) {
	if name == "" {
		return "", errors.New("choose a backup directory")
	}
	if name != "~" && !strings.HasPrefix(name, "~/") {
		return name, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(name, "~")), nil
}
