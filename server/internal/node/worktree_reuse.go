package node

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// Branch lookup is inspection only. General worktree creation still refuses duplicate checkouts. A checkout it hands
// back is named as a terminal here opens it (visiblePath: git's worktree list gives the home's real path), says
// whether it lies in the checkouts folder, and has its git links made portable (worktree_links.go); a refusal carries
// its code (proto.CodeCheckout*) and the branch and path, so the hub tells them apart (#10365). A refused checkout of
// this repository's branch gets portable links too: the refusal names its path for a workspace {existing}, where an
// Agent's terminal then needs git to work.
func (n *Node) worktreePrepareBranch(ctx context.Context, in worktreePrepareParams) (proto.WorktreePrepare, error) {
	prep, err := n.worktreePrepare(ctx, in.Kind, in.WorkspaceID, in.Cwd)
	branch := in.Branch
	if err != nil || branch == "" {
		return prep, err
	}
	if err := checkBranch(ctx, prep.Root, branch); err != nil {
		return prep, err
	}
	at, err := branchCheckedOutAt(ctx, prep.Root, branch)
	if err != nil {
		return prep, err
	}
	if at == "" {
		return prep, nil
	}
	// The path as the checkouts folder names it when it is that one (the hub's own preview of a task's checkout),
	// else as a terminal here opens it.
	shown := n.visiblePath(at)
	if want := filepath.Join(prep.Path, herdr.WorktreeDirName(branch)); prep.Path != "" && realPath(want) == realPath(at) {
		shown = want
	}
	unverified := func(err error) error {
		return checkoutRefusal(ctx, proto.CodeCheckoutUnverified, i18n.Args{"branch": branch, "path": shown, "reason": gitFailure(err)})
	}
	actual, err := repoAt(ctx, at)
	if err != nil {
		return prep, unverified(err)
	}
	common, sourceErr := absGit(ctx, prep.Root, "--git-common-dir")
	other, targetErr := absGit(ctx, at, "--git-common-dir")
	if sourceErr != nil || targetErr != nil || actual.branch != branch ||
		realPath(actual.root) != realPath(at) || realPath(common) != realPath(other) {
		return prep, checkoutRefusal(ctx, proto.CodeCheckoutChanged, i18n.Args{"branch": branch, "path": shown})
	}
	n.portableWorktreeLinks(at)
	if err := n.refreshHostInventory(ctx, true); err != nil {
		return prep, unverified(err)
	}
	n.mu.Lock()
	owner := ""
	for id, a := range n.agents {
		if a.AgentStatus == proto.StatusOffline {
			continue
		}
		for _, dir := range []string{a.Cwd, a.ForegroundCwd} {
			if dir != "" && within(realPath(dir), realPath(at)) {
				owner = id
				if name := firstOf(a.Name, a.Title); name != "" {
					owner = name + " (" + id + ")"
				}
				break
			}
		}
		if owner != "" {
			break
		}
	}
	n.mu.Unlock()
	if owner != "" {
		return prep, checkoutRefusal(ctx, proto.CodeCheckoutInUse, i18n.Args{"branch": branch, "path": shown, "owner": owner})
	}
	inCheckouts := prep.Path != "" && within(realPath(at), realPath(prep.Path))
	untracked, submodules := "normal", "none"
	if in.AllowUntracked && inCheckouts && strings.HasPrefix(branch, "task/") {
		untracked, submodules = "no", "untracked"
	}
	out, err := git(ctx, at, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files="+untracked, "--ignore-submodules="+submodules)
	if err != nil {
		return prep, unverified(err)
	}
	if out != "" {
		return prep, checkoutRefusal(ctx, proto.CodeCheckoutDirty, i18n.Args{"branch": branch, "path": shown, "files": checkoutStatusFiles(out)})
	}
	prep.Existing = &proto.WorktreeCheckout{Path: shown, Branch: branch, InCheckouts: inCheckouts}
	return prep, nil
}

// Porcelain v1 with -z and no rename detection gives one "XY path" per NUL-delimited entry.
func checkoutStatusFiles(status string) string {
	var files []string
	for entry := range strings.SplitSeq(status, "\x00") {
		if len(entry) < 4 {
			continue
		}
		if len(files) == 3 {
			break
		}
		name := strconv.Quote(entry[3:])
		files = append(files, textfmt.Clip(name[:len(name)-1], 72)+`"`)
	}
	return strings.Join(files, ", ")
}

// checkoutRefusal is worktree.prepare's refusal of a branch's checkout: code, in the request's language
// (worktree-composer.<code>), with args' words as its data.
func checkoutRefusal(ctx context.Context, code string, args i18n.Args) error {
	data := map[string]string{}
	for k, v := range args {
		if s, ok := v.(string); ok {
			data[k] = s
		}
	}
	b, _ := json.Marshal(data)
	return &rpc.Error{Code: code, Message: i18n.T(i18n.From(ctx), "worktree-composer."+code, args), Data: b}
}
