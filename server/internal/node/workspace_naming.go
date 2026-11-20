package node

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func (n *Node) resolveWorkspaceNaming(ctx context.Context, raw json.RawMessage) (any, error) {
	var source workspacenaming.Source
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, err
	}
	preview := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: n.home, PersonaHome: n.dir})
	if preview.Plan == nil {
		return preview, nil
	}
	denied := func(repoID string) workspacenaming.Preview {
		preview.Plan = nil
		preview.Status = "invalid"
		preview.Diagnostics = append(preview.Diagnostics, workspacenaming.Problem{Code: "base_scope_denied", Field: "base_directory", RepoID: repoID, Severity: "error"})
		return preview
	}
	state := n.dir
	if state == "" {
		state = filepath.Join(n.home, ".persona")
	}
	state, err := n.realPath(state)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return denied(""), nil
	}
	allowed := func(path string) bool {
		// realPath checks each existing ancestor before following its links.
		// Only a missing tail is valid for metadata about a future workspace.
		real, err := n.realPath(path)
		return (err == nil || errors.Is(err, fs.ErrNotExist)) &&
			n.offLimitsResolved(real) == nil && !workspacenaming.InPrivateState(real, state)
	}
	if !allowed(preview.Plan.BasePath) || !allowed(preview.Plan.RootPath) {
		return denied(""), nil
	}
	for _, repo := range preview.Plan.Repositories {
		if !allowed(repo.AbsolutePath) {
			return denied(repo.RepoID), nil
		}
	}
	for _, repo := range preview.Plan.Repositories {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := exec.CommandContext(checkCtx, "git", "check-ref-format", "--branch", repo.TaskBranch).Run()
		cancel()
		if err != nil {
			code := "git_validation_unavailable"
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = "branch_invalid"
			}
			preview.Plan = nil
			preview.Status = "invalid"
			preview.Diagnostics = append(preview.Diagnostics, workspacenaming.Problem{Code: code, Field: "branch_template", RepoID: repo.RepoID, Severity: "error"})
			break
		}
	}
	return preview, nil
}
