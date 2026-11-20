package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

type herdrWorkspaceDeletion struct {
	host      *herdrHost
	route     herdrRoute
	workspace host.Space
	panes     string
	socket    os.FileInfo
	closeOnly bool
	branch    string
	common    string
}

func (n *Node) inspectHerdrDeletion(ctx context.Context, id string, closeOnly bool) (*composerDeletion, int, error) {
	h, err := n.hostOfKind(host.Herdr)
	if err != nil {
		return nil, 0, err
	}
	hh, ok := h.(*herdrHost)
	if !ok {
		return nil, 0, worktreeUnsupported(host.Herdr)
	}
	route, err := hh.target(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	layout, err := hh.Snapshot(ctx)
	if err != nil {
		return nil, 0, err
	}
	var spaces []host.Space
	var panes []string
	if layout != nil {
		for _, space := range layout.Spaces {
			if space.ID == id {
				spaces = append(spaces, space)
			}
		}
		for _, pane := range layout.Panes {
			if pane.WorkspaceID == id {
				panes = append(panes, pane.PaneID+"\x00"+pane.SessionID()+"\x00"+pane.Cwd)
			}
		}
	}
	if len(spaces) != 1 {
		return nil, 0, composerError(ctx, "preview_stale")
	}
	slices.Sort(panes)
	if real := n.sessionWorkspaceCwd(spaces[0].Cwd); real != "" {
		spaces[0].Cwd = real
	}
	target := &herdrWorkspaceDeletion{host: hh, route: route, workspace: spaces[0], panes: strings.Join(panes, "\n"), closeOnly: closeOnly}
	if route.c.Socket != "" {
		// The CLI can reach servers whose socket is unavailable to this client.
		target.socket, _ = os.Stat(route.c.Socket)
	}
	entry := &composerDeletion{node: n, herdr: target, created: time.Now()}
	if closeOnly {
		return entry, 0, nil
	}
	path := target.workspace.Cwd
	if !filepath.IsAbs(path) {
		return nil, 0, composerError(ctx, "identity_unavailable")
	}
	if err := n.guardBackendDeletion(path); err != nil {
		return nil, 0, err
	}
	top, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || realPath(strings.TrimSpace(top)) != realPath(path) {
		return nil, 0, composerError(ctx, "main_checkout")
	}
	inspected, dirty, err := inspectWorktreeDeletion(ctx, path, false)
	if err != nil {
		return nil, 0, err
	}
	branch, err := git(ctx, path, "branch", "--show-current")
	if err != nil {
		return nil, 0, err
	}
	target.branch = strings.TrimSpace(branch)
	target.common, err = absGit(ctx, path, "--git-common-dir")
	if err != nil {
		return nil, 0, err
	}
	inspected.node, inspected.herdr = n, target
	return inspected, dirty, nil
}

func sameHerdrDeletion(a, b *composerDeletion) bool {
	x, y := a.herdr, b.herdr
	if x == nil || y == nil || x.workspace.ID != y.workspace.ID || x.workspace.Cwd != y.workspace.Cwd ||
		x.route.c.Socket != y.route.c.Socket || x.route.session != y.route.session || x.panes != y.panes ||
		x.closeOnly != y.closeOnly || x.branch != y.branch || x.common != y.common {
		return false
	}
	if x.socket != nil && (y.socket == nil || !os.SameFile(x.socket, y.socket)) {
		return false
	}
	if !x.closeOnly && (!os.SameFile(a.pathInfo, b.pathInfo) || !os.SameFile(a.gitInfo, b.gitInfo) || a.dirtyState != b.dirtyState) {
		return false
	}
	return true
}

func (n *Node) herdrWorktreeDelete(ctx context.Context, id string, in workspaceDeletionInput) (any, error) {
	if in.CloseOnly && (in.Force || in.DeleteBranch) {
		return nil, composerError(ctx, "folder_branch")
	}
	var prior *composerDeletion
	if !in.Preview {
		composerDeletions.Lock()
		prior = composerDeletions.previews[in.Token]
		dispatched := prior != nil && prior.dispatched
		composerDeletions.Unlock()
		if prior == nil || prior.node != n || prior.herdr == nil || prior.herdr.workspace.ID != id ||
			prior.herdr.closeOnly != in.CloseOnly || time.Since(prior.created) > 24*time.Hour {
			return nil, composerError(ctx, "preview_required")
		}
		if in.Reconcile {
			return n.reconcileHerdrDeletion(ctx, prior), nil
		}
		if dispatched {
			return nil, composerError(ctx, "delete_dispatched")
		}
	}
	current, dirty, err := n.inspectHerdrDeletion(ctx, id, in.CloseOnly)
	if err != nil {
		return nil, err
	}
	if in.Preview {
		token, err := rememberWorkspaceDeletion(current)
		if err != nil {
			return nil, err
		}
		return proto.WorktreeDeletePreview{WorkspaceID: id, Kind: host.Herdr, HostID: "local", Path: current.herdr.workspace.Cwd,
			Branch: current.herdr.branch, Dirty: dirty, Folder: in.CloseOnly, CloseOnly: in.CloseOnly, PreviewToken: token}, nil
	}
	if time.Since(prior.created) > 10*time.Minute || !sameHerdrDeletion(prior, current) {
		return nil, composerError(ctx, "preview_stale")
	}
	if dirty > 0 && !in.Force {
		return nil, composerError(ctx, "dirty")
	}
	composerDeletions.Lock()
	if prior.dispatched {
		composerDeletions.Unlock()
		return nil, composerError(ctx, "delete_dispatched")
	}
	prior.dispatched = true
	composerDeletions.Unlock()

	target := current.herdr
	if in.CloseOnly {
		err = target.route.c.WorkspaceClose(ctx, target.route.id)
	} else {
		err = target.route.c.WorktreeRemove(ctx, target.route.id, in.Force)
	}
	result := n.reconcileHerdrDeletion(ctx, prior)
	if err != nil {
		if result.Removed {
			return result, nil
		}
		var native *herdr.Error
		if errors.As(err, &native) && native.Code == "workspace_group_close_required" {
			return nil, refusal("worktree_herdr_children", composerError(ctx, "herdr_children").Error(), nil)
		}
		return nil, err
	}
	if result.Removed && !in.CloseOnly && in.DeleteBranch && target.branch != "" {
		_, err = git(ctx, target.common, "branch", "-d", target.branch)
		result.BranchDeleted = err == nil
	}
	_ = n.refreshHosts(ctx, target.host)
	return result, nil
}

func (n *Node) reconcileHerdrDeletion(ctx context.Context, prior *composerDeletion) proto.WorktreeDeleteResult {
	target := prior.herdr
	result := proto.WorktreeDeleteResult{WorkspaceID: target.workspace.ID, State: "unknown"}
	layout, err := target.host.Snapshot(ctx)
	if err != nil || layout == nil {
		return result
	}
	for _, space := range layout.Spaces {
		if space.ID == target.workspace.ID {
			result.State = "present"
			return result
		}
	}
	if !target.closeOnly {
		if _, err := os.Lstat(target.workspace.Cwd); !os.IsNotExist(err) {
			return result
		}
	}
	result.Removed, result.State = true, "removed"
	return result
}
