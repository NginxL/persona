package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

type orcaLaunch struct {
	terminal orcart.Terminal
	cwd      string
}

// Orca owns the project's worktrees: opening a space means opening a new terminal in the registered directory,
// without making another git worktree or changing an existing terminal.
func (h *orcaHost) CreateSpace(ctx context.Context, cwd, label string) (*host.Created, error) {
	return h.createTerminal(ctx, "", cwd, label)
}

func (h *orcaHost) CreateSlot(ctx context.Context, space, cwd, label string) (*host.Created, error) {
	return h.createTerminal(ctx, space, cwd, label)
}

func (h *orcaHost) createTerminal(ctx context.Context, space, cwd, label string) (*host.Created, error) {
	// Run types `cd -- <cwd>` into the shell's line editor, which acts on a control character even inside the quotes
	// (a tab completes and closes the quoted word, a line break runs the line): such a directory gets no terminal.
	if strings.ContainsFunc(cwd, unicode.IsControl) {
		return nil, host.Mark(refusal(proto.CodeDirectoryUntypable, i18n.T(i18n.Default, "rpc-errors.directory_untypable"), nil), host.ErrNotDelivered)
	}
	trees, _, _, err := h.c.Worktrees(ctx, "")
	if err != nil {
		return nil, orcaErr(err)
	}

	wt, err := h.resolveWorktree(ctx, trees, space, cwd)
	if err != nil {
		return nil, err
	}

	shell, typed := h.launchShell(ctx)
	t, err := h.c.CreateTerminal(ctx, "id:"+wt.WorktreeID, label, newUUID(), shell)
	if err != nil {
		return nil, orcaErr(err)
	}

	h.mu.Lock()
	if h.launches == nil {
		h.launches = map[string]orcaLaunch{}
	}
	h.launches[t.PaneKey()] = orcaLaunch{terminal: *t, cwd: cwd}
	h.track(*t)
	h.mu.Unlock()

	return &host.Created{
		SpaceID: t.WorktreeID,
		SlotID:  t.TabID,
		Pane:    host.PaneRef{Kind: host.Orca, ID: t.PaneKey()},
		Shell:   typed,
	}, nil
}

// orcaOnWindows: the node and its Orca run on Windows. A var for tests.
var orcaOnWindows = func() bool { return runtime.GOOS == "windows" }

// launchShell is the shell a terminal the node types its starts into should run (terminal.create's shell), and the
// one it then reports to them (host.Created's Shell): "" for both, Orca's default, except on Windows. There Orca
// opens Windows PowerShell by default, which reads neither the `cd -- <dir> &&` Run types nor the POSIX start
// (#9810), so the terminal runs Git Bash when Orca can be told so (orcart.CapShellSelection). An Orca that cannot keeps
// its default: a user who made that Git Bash still starts as before.
func (h *orcaHost) launchShell(ctx context.Context) (shell, typed string) {
	if !orcaOnWindows() {
		return "", ""
	}
	st, err := h.c.Probe(ctx)
	if err != nil || !slices.Contains(st.Capabilities, orcart.CapShellSelection) {
		return "", ""
	}
	return orcart.GitBash, "bash"
}

// resolveWorktree locates the best-matching local Orca worktree for cwd.
// A non-empty space is an explicit target and must contain cwd. Without one, if cwd is inside a worktree (including subdirectories),
// it chooses the deepest enclosing worktree.
// If not found in any existing worktree, it checks whether cwd can be safely registered via repo.add (if git repo).
func (h *orcaHost) resolveWorktree(ctx context.Context, trees []orcart.Worktree, spaceHint, cwd string) (*orcart.Worktree, error) {
	path := orcaLaunchPath(cwd)

	// 1. An explicit selection never silently falls back to a different workspace.
	if spaceHint != "" {
		for i := range trees {
			w := &trees[i]
			if w.HostID != "" && w.HostID != "local" {
				continue
			}
			if w.WorktreeID == spaceHint {
				wPath := orcaLaunchPath(w.Path)
				if isInsideOrEqual(path, wPath) {
					return w, nil
				}
			}
		}
		return nil, host.Mark(refusal("orca_workspace_unavailable", "The selected Orca workspace is unavailable or does not contain this directory", nil), host.ErrNotDelivered)
	}

	// 2. Search all local worktrees for the deepest enclosing worktree.
	var best *orcart.Worktree
	for i := range trees {
		w := &trees[i]
		if w.HostID != "" && w.HostID != "local" {
			continue
		}
		wPath := orcaLaunchPath(w.Path)
		if isInsideOrEqual(path, wPath) {
			if best == nil || len(wPath) > len(orcaLaunchPath(best.Path)) {
				best = w
			}
		}
	}
	if best != nil {
		return best, nil
	}

	// 3. Not in any registered worktree: try safe auto-registration if it is a Git repo
	// and not a forbidden directory (~, /, /tmp).
	if backendLaunchFrom(ctx) == nil && h.canAutoRegister(path) {
		if gitRoot, err := findGitRoot(path); err == nil && gitRoot != "" {
			if addErr := h.c.RepoAdd(ctx, gitRoot, "git"); addErr == nil {
				// Re-fetch worktrees after repo.add.
				newTrees, _, _, listErr := h.c.Worktrees(ctx, "")
				if listErr == nil {
					for i := range newTrees {
						w := &newTrees[i]
						if w.HostID != "" && w.HostID != "local" {
							continue
						}
						wPath := orcaLaunchPath(w.Path)
						if isInsideOrEqual(path, wPath) {
							if best == nil || len(wPath) > len(orcaLaunchPath(best.Path)) {
								best = w
							}
						}
					}
					if best != nil {
						return best, nil
					}
				}
			}
		}
	}

	// 4. Fallback: if cwd is not in any worktree (e.g. user home ~ or an external directory), the terminal goes into a
	// local worktree as its container and Run cds into cwd: the one Orca's window shows, else the first. Orca's UI
	// does not adopt a terminal made in a worktree it is not showing: it becomes an orphaned background terminal under
	// a new pane key (#5547).
	var first *orcart.Worktree
	for i := range trees {
		w := &trees[i]
		if w.HostID != "" && w.HostID != "local" {
			continue
		}
		if w.Active {
			return w, nil
		}
		if first == nil {
			first = w
		}
	}
	if first != nil {
		return first, nil
	}

	return nil, host.Mark(
		refusal("orca_not_registered", "Orca 当前没有任何打开的项目，请先在电脑上打开一个项目再新建 Agent。", nil),
		host.ErrNotDelivered,
	)
}

func (h *orcaHost) canAutoRegister(path string) bool {
	clean := filepath.Clean(path)
	if clean == "/" {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil && clean == filepath.Clean(home) {
		return false
	}
	temp := filepath.Clean(os.TempDir())
	if strings.HasPrefix(clean, temp) || strings.HasPrefix(clean, "/tmp") || strings.HasPrefix(clean, "/private/tmp") {
		return false
	}
	return true
}

func findGitRoot(path string) (string, error) {
	curr := path
	for {
		gitDir := filepath.Join(curr, ".git")
		if st, err := os.Stat(gitDir); err == nil && (st.IsDir() || !st.IsDir()) {
			return curr, nil
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	return "", os.ErrNotExist
}

// isInsideOrEqual returns true if sub is equal to base or is a descendant of base.
func isInsideOrEqual(sub, base string) bool {
	if sub == base {
		return true
	}
	rel, err := filepath.Rel(base, sub)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func orcaLaunchPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// Run may write shell commands only to a pane created by this host instance, once. Consuming the grant before the
// send prevents a timeout or duplicate call from launching twice. Unlike normal sends it never refreshes a stale
// handle: replacing that shell revokes the grant rather than redirecting the command to a different process.
func (h *orcaHost) Run(ctx context.Context, pane host.PaneRef, command string) error {
	h.mu.Lock()
	launch, ok := h.launches[pane.ID]
	delete(h.launches, pane.ID)
	h.mu.Unlock()
	if !ok || pane.Kind != host.Orca {
		return host.Mark(fmt.Errorf("orca: shell launch is only allowed once in a newly created terminal"), host.ErrNotDelivered)
	}
	terms, err := h.c.Terminals(ctx, false)
	if err != nil {
		return orcaInputErr(err)
	}
	// By its handle, not its pane key: Orca re-keys a terminal its UI did not adopt, orphaned, with the same handle
	// and process (#5547), and a background terminal still runs what it is sent. Orphaned is accepted only so: the
	// terminal this host made, re-keyed. A handle that changed is not ours (#972).
	for _, t := range terms.Terminals {
		if t.Handle != launch.terminal.Handle {
			continue
		}
		if t.IncarnationID != launch.terminal.IncarnationID || !t.IsLocal() || !t.Writable || t.AgentIdentity != "" ||
			t.Orphaned && t.PaneKey() == launch.terminal.PaneKey() {
			break
		}
		return orcaInputErr(h.c.Send(ctx, launch.terminal.Target(), "cd -- "+shellQuote(launch.cwd)+" && "+command+"\r"))
	}
	return host.Mark(fmt.Errorf("orca: the newly created shell was replaced or is no longer writable"), host.ErrNotDelivered)
}

// launchable: Run would still take a command in pane, a terminal this host made whose one launch is unused.
func (h *orcaHost) launchable(pane host.PaneRef) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.launches[pane.ID]
	return ok
}

func (h *orcaHost) NameAgent(ctx context.Context, pane host.PaneRef, name string) error {
	t, err := h.terminal(ctx, pane.ID, true)
	if err != nil {
		return orcaErr(err)
	}
	return orcaErr(h.onTerminal(ctx, t, true, func(target orcart.Target) error {
		return h.c.RenameTerminal(ctx, target, name)
	}))
}
