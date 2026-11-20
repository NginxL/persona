package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/proto"
)

// newWorktree makes a worktree on branch of the repository cwd is in, through the machine's terminal engine ("New
// worktree…", docs/integrations/terminal.md): herdr's or Orca's own, so it is listed there and in the apps, never a bare
// `git worktree add` by the agent. The checkout goes where the engine puts them, named for the branch the way the
// dialog names it.
func (s *Service) newWorktree(ctx context.Context, mid, cwd, branch string) (proto.WorktreeCreateResult, error) {
	return s.newWorktreeOpen(ctx, mid, cwd, branch, nil)
}

// newWorktreeForTask creates a task checkout. On a herdr leader task checkoutOnly keeps the checkout out of a new
// herdr workspace: the leader and its workers open as tabs of the recorded task workspace instead (#4786).
func (s *Service) newWorktreeForTask(ctx context.Context, mid, cwd, branch string, checkoutOnly bool) (proto.WorktreeCreateResult, error) {
	if !checkoutOnly {
		return s.newWorktree(ctx, mid, cwd, branch)
	}
	openWorkspace := false
	return s.newWorktreeOpen(ctx, mid, cwd, branch, &openWorkspace)
}

func (s *Service) newWorktreeOpen(ctx context.Context, mid, cwd, branch string, openWorkspace *bool) (proto.WorktreeCreateResult, error) {
	var prep proto.WorktreePrepare
	if err := s.call(ctx, mid, DeviceWorktreePrepare, map[string]any{"cwd": cwd}, &prep); err != nil {
		return proto.WorktreeCreateResult{}, err
	}
	var res proto.WorktreeCreateResult
	params := map[string]any{"cwd": cwd, "branch": branch,
		"path": proto.NodeJoin(prep.Path, herdr.WorktreeDirName(branch)), "for_agent": true}
	if openWorkspace != nil {
		params["open_workspace"] = *openWorkspace
	}
	// for_agent: every caller starts an Agent in it, so the node marks the workspace's first shell as Persona's and
	// closes it with that Agent's finished terminal (#10624); an older node ignores it.
	err := s.call(ctx, mid, DeviceWorktreeCreate, params, &res)
	return res, err
}
