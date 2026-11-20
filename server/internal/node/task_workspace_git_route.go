package node

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

// backendGit uses the real registered worktree and the standard Git RPC under
// the same workspace lock as cleanup. A bare container is never a default repo.
func (n *Node) backendGit(ctx context.Context, verb string, raw json.RawMessage) (any, error) {
	var in struct {
		Workspace string `json:"backend_workspace_id"`
		Repo      string `json:"repo_id"`
		Cwd       string `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if !workspacenaming.ValidOperationID(in.Workspace) || in.Repo == "" || strings.Contains(verb, ".") {
		return nil, taskworkspace.Failure("invalid_input", "git", "", "", "")
	}
	e, lock, err := n.lockBackendReceipt(in.Workspace, "git", in.Repo)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	j := &e.journal
	if j.WorkspaceID != in.Workspace || j.State != "ready" || j.Cleanup != nil {
		return nil, taskworkspace.Failure("not_ready", "git", j.OperationID, in.Repo, "")
	}
	for i := range j.Repositories {
		repo := &j.Repositories[i]
		if repo.RepoID != in.Repo {
			continue
		}
		if in.Cwd != "" && filepath.Clean(in.Cwd) != repo.Path {
			return nil, taskworkspace.Failure("identity_mismatch", "git", j.OperationID, in.Repo, in.Cwd)
		}
		if !e.owned(j.Plan.RootPath) || !e.owned(repo.Path) || repo.Removed || repo.State != "ready" {
			return nil, taskworkspace.Failure("identity_mismatch", "git", j.OperationID, in.Repo, repo.Path)
		}
		if code := e.checkRegistration(ctx, repo); code != "" {
			return nil, taskworkspace.Failure(code, "git", j.OperationID, in.Repo, repo.Path)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		fields["cwd"], _ = json.Marshal(repo.Path)
		raw, err = json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		return n.handleMethod(ctx, "git."+verb, raw)
	}
	return nil, taskworkspace.Failure("not_found", "git", j.OperationID, in.Repo, "")
}
