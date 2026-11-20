package node

import (
	"context"
	"maps"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
)

// Snapshot is the own server's layout with every running named session's (each one's last when it does not answer
// in time). The own server failing is the host failing, whatever the named sessions say.
func (h *herdrHost) Snapshot(ctx context.Context) (*host.Layout, error) {
	if h.named == nil {
		return layoutOf(ctx, h.c, "")
	}
	others := fromNamedSessions(ctx, h.named, func(ctx context.Context, ns *herdrSession) (*host.Layout, error) {
		l, err := layoutOf(ctx, ns.c, ns.name)
		if err == nil {
			ns.mu.Lock()
			ns.layout, ns.spaces = l, nil
			ns.mu.Unlock()
		}
		return l, err
	}, (*herdrSession).lastLayout)
	l, err := layoutOf(ctx, h.c, "")
	named := others()
	if err != nil {
		return nil, err
	}
	for _, o := range named {
		mergeLayout(l, o)
	}
	return l, nil
}

// fromNamedSessions asks s's running sessions alongside the own server: it starts at once (fromNamed) and the
// function it returns waits for what they answered by namedWait from now.
func fromNamedSessions[T any](ctx context.Context, s *herdrSessions, get func(context.Context, *herdrSession) (T, error), last func(*herdrSession) (T, bool)) func() []T {
	var out []T
	done := make(chan struct{})
	go func() {
		defer close(done)
		until := time.Now().Add(namedWait)
		out = fromNamed(ctx, until, s.running(ctx, until), get, last)
	}()
	return func() []T {
		<-done
		return out
	}
}

// layoutOf is one server's snapshot as a layout, its ids qualified with session ("" the own server: as they are).
func layoutOf(ctx context.Context, c *herdr.Client, session string) (*host.Layout, error) {
	snap, err := c.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	l := &host.Layout{Agents: fromHerdrAgents(session, snap.Agents), Panes: fromHerdrAgents(session, snap.Panes)}
	if len(snap.Layouts) > 0 {
		l.PaneIndex = herdr.PaneIndexes(snap.Layouts)
		if session != "" {
			idx := make(map[string]int, len(l.PaneIndex))
			for pane, i := range l.PaneIndex {
				idx[herdr.QualifyID(session, pane)] = i
			}
			l.PaneIndex = idx
		}
	}
	for _, w := range snap.Workspaces {
		l.Spaces = append(l.Spaces, fromHerdrWorkspace(session, w))
	}
	for _, t := range snap.Tabs {
		l.Slots = append(l.Slots, host.Slot{ID: herdr.QualifyID(session, t.ID), SpaceID: herdr.QualifyID(session, t.WorkspaceID),
			Label: t.Label, Number: t.Number})
	}
	fillSpaceDirectories(l.Spaces, l.Panes)
	if len(l.Panes) == 0 {
		fillSpaceDirectories(l.Spaces, l.Agents)
	}
	return l, nil
}

// mergeLayout adds o's spaces, slots, agents and panes to l (their ids never collide: o's are qualified), leaving o as
// it is (a session's last layout is used again).
func mergeLayout(l, o *host.Layout) {
	if o == nil {
		return
	}
	l.Spaces = append(l.Spaces, o.Spaces...)
	l.Slots = append(l.Slots, o.Slots...)
	l.Agents = append(l.Agents, o.Agents...)
	l.Panes = append(l.Panes, o.Panes...)
	if len(o.PaneIndex) > 0 {
		if l.PaneIndex == nil {
			l.PaneIndex = make(map[string]int, len(o.PaneIndex))
		}
		maps.Copy(l.PaneIndex, o.PaneIndex)
	}
}

func (h *herdrHost) Agents(ctx context.Context) ([]host.Agent, error) {
	if h.named == nil {
		agents, err := h.c.Agents(ctx)
		return fromHerdrAgents("", agents), err
	}
	others := fromNamedSessions(ctx, h.named, func(ctx context.Context, ns *herdrSession) ([]host.Agent, error) {
		agents, err := ns.c.Agents(ctx)
		return fromHerdrAgents(ns.name, agents), err
	}, func(ns *herdrSession) ([]host.Agent, bool) {
		l, ok := ns.lastLayout()
		if !ok {
			return nil, false
		}
		return l.Agents, true
	})
	agents, err := h.c.Agents(ctx)
	out := fromHerdrAgents("", agents)
	for _, o := range others() {
		out = append(out, o...)
	}
	return out, err
}

// Spaces are the own server's workspaces and every running named session's (each one's last when it does not answer
// in time).
func (h *herdrHost) Spaces(ctx context.Context) ([]host.Space, error) {
	if h.named == nil {
		wss, err := h.c.Workspaces(ctx)
		return spacesOf("", wss), err
	}
	others := fromNamedSessions(ctx, h.named, func(ctx context.Context, ns *herdrSession) ([]host.Space, error) {
		wss, err := ns.c.Workspaces(ctx)
		if err != nil {
			return nil, err
		}
		out := spacesOf(ns.name, wss)
		ns.mu.Lock()
		ns.spaces = out
		ns.mu.Unlock()
		return out, nil
	}, (*herdrSession).lastSpaces)
	wss, err := h.c.Workspaces(ctx)
	out := spacesOf("", wss)
	for _, o := range others() {
		out = append(out, o...)
	}
	return out, err
}

// spacesOf are the workspaces of session ("" the own server) as spaces.
func spacesOf(session string, wss []herdr.Workspace) []host.Space {
	out := make([]host.Space, 0, len(wss))
	for _, w := range wss {
		out = append(out, fromHerdrWorkspace(session, w))
	}
	return out
}

// fromHerdrWorkspace is a workspace of session ("" the own server) as a space: a named session's id qualified and its
// label with the session in front (sessionLabel).
func fromHerdrWorkspace(session string, w herdr.Workspace) host.Space {
	return host.Space{ID: herdr.QualifyID(session, w.ID), Label: sessionLabel(session, w.Label), Number: w.Number,
		Cwd: w.Cwd, Branch: w.Branch}
}

func fromHerdrAgents(session string, agents []herdr.Agent) []host.Agent {
	if agents == nil {
		return nil
	}
	out := make([]host.Agent, len(agents))
	for i, a := range agents {
		out[i] = fromHerdrAgent(session, a)
	}
	return out
}

// fromHerdrAgent: herdr's pane ids are herdr PaneRefs as they are (no prefix), qualified with session for a named
// session's (herdr.QualifyID), as are its tab and workspace ids.
func fromHerdrAgent(session string, a herdr.Agent) host.Agent {
	q := func(id string) string { return herdr.QualifyID(session, id) }
	out := host.Agent{Agent: a.Agent, Name: a.Name, AgentStatus: a.AgentStatus, Cwd: a.Cwd, ForegroundCwd: a.ForegroundCwd,
		PaneID: q(a.PaneID), TabID: q(a.TabID), WorkspaceID: q(a.WorkspaceID), Title: a.Title, RawTitle: a.RawTitle, Tokens: a.Tokens}
	if a.AgentSession != nil {
		out.AgentSession = &host.AgentSession{Source: a.AgentSession.Source, Agent: a.AgentSession.Agent,
			Kind: a.AgentSession.Kind, Value: a.AgentSession.Value}
	}
	return out
}

// directorySpaces only attributes pane directories to a workspace when they agree: herdr creates from that
// workspace's repository, not from whichever pane happened to match the requested directory.
// Each server is queried independently so equal workspace ids in named sessions cannot collide.
func (h *herdrHost) directorySpaces(ctx context.Context) ([]spaceDirectory, error) {
	read := func(ctx context.Context, c *herdr.Client, session string) ([]spaceDirectory, error) {
		wss, err := c.Workspaces(ctx)
		if err != nil {
			return nil, err
		}
		var out []spaceDirectory
		for _, w := range wss {
			add := func(dir string) {
				if dir != "" {
					out = append(out, spaceDirectory{workspace: herdr.QualifyID(session, w.ID), cwd: dir})
				}
			}
			if w.Cwd != "" {
				add(w.Cwd)
				continue
			}
			panes, err := c.Panes(ctx, w.ID)
			if err != nil {
				return nil, err
			}
			var cwd, foreground string
			var conflict, foregroundConflict bool
			for _, p := range panes {
				if p.WorkspaceID != "" && p.WorkspaceID != w.ID {
					continue
				}
				if p.Cwd != "" {
					dir := realPath(filepath.Clean(p.Cwd))
					if cwd != "" && cwd != dir {
						conflict = true
					}
					cwd = dir
				}
				if p.ForegroundCwd != "" {
					dir := realPath(filepath.Clean(p.ForegroundCwd))
					if foreground != "" && foreground != dir {
						foregroundConflict = true
					}
					foreground = dir
				}
			}
			if conflict {
				continue
			}
			if w.Worktree != nil {
				add(w.Worktree.CheckoutPath)
				if !w.Worktree.IsLinkedWorktree {
					add(w.Worktree.RepoRoot)
				}
				if w.Worktree.IsLinkedWorktree || w.Worktree.CheckoutPath != "" || w.Worktree.RepoRoot != "" {
					continue
				}
			}
			if cwd != "" {
				add(cwd)
			} else if foreground != "" && !foregroundConflict {
				out = append(out, spaceDirectory{workspace: herdr.QualifyID(session, w.ID), cwd: foreground, fallback: true})
			}
		}
		return out, nil
	}
	if h.named == nil {
		return read(ctx, h.c, "")
	}
	others := fromNamedSessions(ctx, h.named, func(ctx context.Context, ns *herdrSession) ([]spaceDirectory, error) {
		return read(ctx, ns.c, ns.name)
	}, func(*herdrSession) ([]spaceDirectory, bool) { return nil, false })
	out, err := read(ctx, h.c, "")
	for _, spaces := range others() {
		out = append(out, spaces...)
	}
	return out, err
}
