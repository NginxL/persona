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
	var prep proto.WorktreePrepare
	if err := s.call(ctx, mid, DeviceWorktreePrepare, map[string]any{"cwd": cwd}, &prep); err != nil {
		return proto.WorktreeCreateResult{}, err
	}
	var res proto.WorktreeCreateResult
	err := s.call(ctx, mid, DeviceWorktreeCreate, map[string]any{"cwd": cwd, "branch": branch,
		"path": proto.NodeJoin(prep.Path, herdr.WorktreeDirName(branch))}, &res)
	return res, err
}
