package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

// createWorkspaceProject is an explicit directory/project operation. It starts no
// conversation and never replaces an existing file. Retrying an interrupted request
// reuses the directory. Native project writes require a verified sidebar adapter.
func (n *Node) createWorkspaceProject(ctx context.Context, path, name, app, key string, createDirectory bool) (proto.Workspace, error) {
	return n.createWorkspaceProjectWithBindings(ctx, path, name, app, key, createDirectory, nil)
}

func (n *Node) createWorkspaceProjectWithBindings(ctx context.Context, path, name, app, key string, createDirectory bool, boundDirectories []string) (proto.Workspace, error) {
	var empty proto.Workspace
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 200 || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		return empty, errors.New("project name must contain 1 to 200 printable characters")
	}
	if app != "" && app != "codex_app" && app != "claude_desktop" {
		return empty, errors.New("unsupported desktop application")
	}
	if app != "" && n.sessionScope != nil {
		return empty, errors.New("desktop projects are unavailable in a scoped node")
	}
	if app == "codex_app" && (key == "" || len(key) > 200 || strings.ContainsFunc(key, unicode.IsControl)) {
		return empty, errors.New("Codex project creation requires an idempotency key")
	}
	path = n.resolvePath(path, "")
	if path == "" || inICloud(path) {
		return empty, errors.New("project path must be a local absolute directory")
	}
	if !n.sessionScopeAllows(path) {
		return empty, errors.New("project directory is outside this node's scope")
	}
	// Resolve aliases and existing ancestors before creating a directory or calling
	// a desktop API. A renamed request for an already bound folder has no effects.
	canonical := folderKey(sessionScopePath(path))
	for _, bound := range boundDirectories {
		if p := n.resolvePath(bound, ""); p != "" && folderKey(sessionScopePath(p)) == canonical {
			return empty, errors.New("project directory is already bound")
		}
	}
	// Neither desktop currently exposes a verified external operation that updates
	// its native sidebar name and folders. A successful metadata database write is
	// not sidebar synchronization; refuse before any directory or project effects.
	if app == "codex_app" || app == "claude_desktop" {
		return empty, refusal("desktop_project_sync_unavailable",
			"Creating native desktop projects is unavailable because this desktop version cannot reliably synchronize the sidebar project name and folders.",
			map[string]any{"desktop_app": app})
	}
	if createDirectory {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			parent, err := n.resolveWorkspace(filepath.Dir(path), false)
			if err != nil {
				return empty, fmt.Errorf("project parent: %w", err)
			}
			path = filepath.Join(parent.Cwd, filepath.Base(path))
			if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return empty, fmt.Errorf("create project directory: %w", err)
			}
		} else if err != nil {
			return empty, err
		}
	}
	w, err := n.resolveWorkspace(path, false)
	if err != nil {
		return empty, err
	}
	w.Title = name
	return w, nil
}
