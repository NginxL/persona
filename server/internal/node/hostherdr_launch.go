package node

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
)

// CreateSpace opens the workspace on the own server: pane-less work never goes to a named session.
func (h *herdrHost) CreateSpace(ctx context.Context, cwd, label string) (*host.Created, error) {
	return h.CreateSpaceEnv(ctx, cwd, label, nil)
}

func (h *herdrHost) CreateSlot(ctx context.Context, space, cwd, label string) (*host.Created, error) {
	return h.CreateSlotEnv(ctx, space, cwd, label, nil)
}

// CreateSpaceEnv is CreateSpace whose first pane's shell starts with env added (host.EnvLauncher).
func (h *herdrHost) CreateSpaceEnv(ctx context.Context, cwd, label string, env []string) (*host.Created, error) {
	c, err := h.c.WorkspaceCreate(ctx, cwd, label, env...)
	return fromHerdrCreated("", c), err
}

// CreateSlotEnv is CreateSlot whose pane's shell starts with env added (host.EnvLauncher).
func (h *herdrHost) CreateSlotEnv(ctx context.Context, space, cwd, label string, env []string) (*host.Created, error) {
	t, err := h.target(ctx, space)
	if err != nil {
		return nil, err
	}
	c, err := t.c.TabCreate(ctx, t.id, cwd, label, env...)
	return fromHerdrCreated(t.session, c), err
}

func (h *herdrHost) Run(ctx context.Context, pane host.PaneRef, command string) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.PaneRun(ctx, t.id, command)
}

func (h *herdrHost) NameAgent(ctx context.Context, pane host.PaneRef, name string) error {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return err
	}
	return t.c.AgentRename(ctx, t.id, name)
}

// RenameSpace renames the workspace; a name given with the session in front, as the host shows a named session's
// (sessionLabel), is given to herdr without it.
func (h *herdrHost) RenameSpace(ctx context.Context, space, name string) error {
	t, err := h.target(ctx, space)
	if err != nil {
		return err
	}
	if t.session != "" {
		name = strings.TrimPrefix(name, t.session+sessionLabelSep)
	}
	return t.c.WorkspaceRename(ctx, t.id, name)
}

func (h *herdrHost) RenameSlot(ctx context.Context, slot, name string) error {
	t, err := h.target(ctx, slot)
	if err != nil {
		return err
	}
	return t.c.TabRename(ctx, t.id, name)
}

// Close closes the pane when its tab is split, else the whole tab. When the panes cannot be counted it closes only the
// pane: herdr closes the tab itself when that was its last pane, and a split tab keeps its other panes (#4873).
func (h *herdrHost) Close(ctx context.Context, pane host.PaneRef, space, slot string) error {
	t, ids, err := h.targets(ctx, pane.ID, space, slot)
	if err != nil {
		return err
	}
	space, slot = ids[0], ids[1]
	panes, err := t.c.TabPanes(ctx, space, slot)
	if err != nil || panes > 1 {
		return t.c.PaneClose(ctx, t.id)
	}
	return t.c.TabClose(ctx, slot)
}

// CloseSlot closes the whole tab, every pane in it.
func (h *herdrHost) CloseSlot(ctx context.Context, slot string) error {
	t, err := h.target(ctx, slot)
	if err != nil {
		return err
	}
	return t.c.TabClose(ctx, t.id)
}

// Split splits the pane; the new pane keeps the host's kind (herdr panes carry no prefix) and the pane's session.
func (h *herdrHost) Split(ctx context.Context, pane host.PaneRef, direction, cwd string) (host.PaneRef, error) {
	t, err := h.target(ctx, pane.ID)
	if err != nil {
		return host.PaneRef{}, err
	}
	id, err := t.c.PaneSplit(ctx, t.id, direction, cwd)
	if err != nil {
		return host.PaneRef{}, err
	}
	if id == "" {
		return host.PaneRef{}, errors.New("herdr did not return the new pane")
	}
	return host.PaneRef{Kind: pane.Kind, ID: t.qualify(id)}, nil
}

// CreateWorktree has herdr check the branch out at path and open it as a workspace grouped under the source one, in
// the source one's session.
func (h *herdrHost) CreateWorktree(ctx context.Context, space, branch, path string) (*host.Created, error) {
	return h.createWorktree(ctx, space, "", "", branch, path)
}

// CreateWorktreeFromMain starts from a linked checkout's main repository with base as the linked checkout's HEAD. A
// fresh layout of the source session decides whether the main checkout has its own workspace; cached layouts are not used.
func (h *herdrHost) CreateWorktreeFromMain(ctx context.Context, space, mainRoot, base, branch, path string) (*host.Created, error) {
	return h.createWorktree(ctx, space, mainRoot, base, branch, path)
}

// CreateWorktreeFromCwd has herdr make the worktree from the repository root itself, on the own server: no workspace
// holds that repository (worktreeCreate), so none is looked for; herdr's workspace still carries the repository.
func (h *herdrHost) CreateWorktreeFromCwd(ctx context.Context, root, branch, path string) (*host.Created, error) {
	c, err := h.c.WorktreeCreateFromCwd(ctx, root, branch, path)
	if err != nil {
		return nil, err
	}
	out := fromHerdrCreated("", &c.Created)
	if out.SpaceID == "" {
		return nil, errors.New("herdr did not return the new workspace")
	}
	return out, nil
}

func (h *herdrHost) createWorktree(ctx context.Context, space, mainRoot, base, branch, path string) (*host.Created, error) {
	t, err := h.target(ctx, space)
	if err != nil {
		return nil, err
	}
	space = t.id
	if mainRoot != "" {
		if l, layoutErr := layoutOf(ctx, t.c, ""); layoutErr == nil {
			for _, s := range l.Spaces {
				if s.Cwd != "" && realPath(s.Cwd) == realPath(mainRoot) {
					space, mainRoot = s.ID, ""
					break
				}
			}
		}
	}
	var c *herdr.WorktreeCreated
	if mainRoot != "" {
		c, err = t.c.WorktreeCreateFromCwd(ctx, mainRoot, branch, path, base)
	} else {
		c, err = t.c.WorktreeCreate(ctx, space, branch, path, base)
	}
	if err != nil {
		return nil, err
	}
	out := fromHerdrCreated(t.session, &c.Created)
	if out.SpaceID == "" {
		return nil, errors.New("herdr did not return the new workspace")
	}
	return out, nil
}

func (h *herdrHost) WorktreesDir(home string) string { return herdr.WorktreesDir(home) }

// worktreeSupport caches whether this herdr has `worktree` (herdr 0.9 on Unix).
var worktreeSupport struct {
	once sync.Once
	ok   bool
}

func (h *herdrHost) CanWorktree() bool {
	return h.has(&worktreeSupport, "worktree")
}

func fromHerdrCreated(session string, c *herdr.Created) *host.Created {
	if c == nil {
		return nil
	}
	out := &host.Created{SlotID: herdr.QualifyID(session, c.Tab.ID),
		Pane: host.PaneRef{Kind: host.Herdr, ID: herdr.QualifyID(session, c.RootPane.ID)}}
	if c.Workspace != nil {
		out.SpaceID = herdr.QualifyID(session, c.Workspace.ID)
	}
	return out
}
