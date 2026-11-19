package node

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// git links a linked worktree and its repository both ways with absolute paths it resolves first: <worktree>/.git says
// "gitdir: <repo>/.git/worktrees/<id>", and that folder's gitdir file names <worktree>/.git. On a computer whose home is
// a link (/home/me -> /data/home/me) both name /data/home/me/…, which a terminal that sees the home only as /home/me
// (a container that mounts it there) does not have: git there says "not a git repository" inside the worktree, and
// from the main checkout lists it as prunable, so a `git worktree prune` would drop its record (#10365). The node
// rewrites both links to the home's own spelling, which names the same files on either side. It does not use git's
// relative links (worktree.useRelativePaths, git 2.48): they set extensions.relativeWorktrees in the repository's
// shared config, and an older git, libgit2 or go-git then refuses the whole repository. On Windows it leaves git's
// links alone: no terminal there sees the home only by its own spelling, and Git for Windows misreads a link written
// with backslashes (its worktree list strips only "/.git", so the checkout reads as its .git file).

// worktreeLinksGOOS is the system portableWorktreeLinks runs on; tests replace it.
var worktreeLinksGOOS = runtime.GOOS

// addWorktree runs `git worktree add` (args, from "worktree") in root for the checkout at dest, then makes its links
// portable. The node's three ways of adding one use it: a task or "New worktree…" checkout (worktreeByGit), an
// Agent's own worktree (worktreeAdd) and a review copy (worktreeSnapshot).
func (n *Node) addWorktree(ctx context.Context, root, dest string, args ...string) error {
	if _, err := gitTimed(ctx, root, 2*time.Minute, args...); err != nil {
		return err
	}
	n.portableWorktreeLinks(dest)
	return nil
}

// portableWorktreeLinks rewrites the links between the linked worktree at dest and its repository to the home's own
// spelling when git wrote them under the home's real path. A link that is relative, outside the home, or that the
// home's spelling would not reach the same file through stays as git wrote it; so does everything on a computer whose
// home is no link, and on Windows. A failure is only logged: the checkout works on this computer either way.
func (n *Node) portableWorktreeLinks(dest string) {
	if worktreeLinksGOOS == "windows" || realHome(n.home) == "" {
		return
	}
	dotGit := filepath.Join(dest, ".git")
	b, err := os.ReadFile(dotGit)
	if err != nil {
		return // not a linked worktree's file (a main checkout's .git is a folder)
	}
	line := strings.TrimSpace(string(b))
	admin, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok || strings.ContainsAny(line, "\n") {
		return
	}
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(dest, admin)
	} else if p := n.visiblePath(admin); p != admin {
		if err := replaceLink(dotGit, "gitdir: "+p+"\n"); err != nil {
			n.logf("worktree %s: could not rewrite its .git link: %v", dest, err)
		}
	}
	back := filepath.Join(admin, "gitdir")
	if b, err = os.ReadFile(back); err != nil {
		return
	}
	target := strings.TrimSpace(string(b))
	if !filepath.IsAbs(target) || strings.ContainsAny(target, "\n") {
		return
	}
	if p := n.visiblePath(target); p != target {
		if err := replaceLink(back, p+"\n"); err != nil {
			n.logf("worktree %s: could not rewrite its repository's gitdir link: %v", dest, err)
		}
	}
}

// worktreeTop is the linked worktree dir lies in: the nearest folder at or above it whose .git is a file; "" when
// that .git is a folder (a main checkout) or there is none.
func worktreeTop(dir string) string {
	for d := filepath.Clean(dir); ; {
		if st, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			if st.Mode().IsRegular() {
				return d
			}
			return ""
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// replaceLink replaces the small link file path whole (writeFileAtomic), keeping its permissions.
func replaceLink(path, text string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, []byte(text)); err != nil {
		return err
	}
	return os.Chmod(path, st.Mode().Perm())
}
