package node

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// desktopWorkspaces lists saved desktop projects independently of running terminals and conversations. A scoped
// assistant node must not discover the user's desktop projects, even if its process inherited that user's home.
func (n *Node) desktopWorkspaces() ([]proto.Workspace, int, []proto.WorkspaceSourceError) {
	if n.sessionScope != nil || !filepath.IsAbs(n.home) {
		return nil, 0, nil
	}
	var out []proto.Workspace
	var failures []proto.WorkspaceSourceError
	sources := 0
	homes := append([]string{n.codexHome()}, n.codexInventory().Homes...)
	seenHomes := map[string]bool{}
	for _, home := range homes {
		home = canonicalCodexHome(home, n.home)
		if home == "" || seenHomes[folderKey(home)] {
			continue
		}
		seenHomes[folderKey(home)] = true
		projects, order, err := codexDesktopProjects(home)
		if err != nil {
			failures = append(failures, proto.WorkspaceSourceError{Source: "codex_app", Error: err.Error()})
		}
		if projects == nil {
			continue // No saved desktop project source, rather than a successful empty terminal inventory.
		}
		sources++
		ids := slices.Clone(order)
		seenIDs := map[string]bool{}
		ids = slices.DeleteFunc(ids, func(id string) bool {
			_, exists := projects[id]
			if !exists || seenIDs[id] {
				return true
			}
			seenIDs[id] = true
			return false
		})
		var rest []string
		for id := range projects {
			if !seenIDs[id] {
				rest = append(rest, id)
			}
		}
		sort.Strings(rest)
		ids = append(ids, rest...)
		for i, id := range ids {
			p := projects[id]
			projectID := codexDesktopProjectID(home, id)
			roots := p.RootPaths
			if len(roots) == 0 {
				out = append(out, proto.Workspace{ID: projectID, Source: "codex_app", SourceID: projectID,
					Title: p.Name, ProjectID: projectID, ProjectName: p.Name, Index: i + 1, Online: true})
				continue
			}
			seenRoots := map[string]bool{}
			for _, root := range roots {
				if !filepath.IsAbs(root) || inICloud(root) || n.cwdBlocked(root) {
					continue
				}
				cwd := filepath.Clean(root)
				real := n.sessionWorkspaceCwd(cwd)
				if real != "" {
					cwd = real
				}
				if seenRoots[folderKey(cwd)] {
					continue
				}
				seenRoots[folderKey(cwd)] = true
				// Saved project identity distinguishes projects sharing a directory. Root-specific row IDs keep
				// every directory of one project independently selectable.
				out = append(out, proto.Workspace{ID: projectID + ":" + folderKey(cwd), Source: "codex_app", SourceID: projectID,
					Title: firstNonEmpty(strings.TrimSpace(p.Name), filepath.Base(cwd)), Cwd: cwd,
					ProjectID: projectID, ProjectName: p.Name, ProjectPath: cwd, Index: i + 1, Online: true, Available: real != ""})
			}
		}
	}

	for _, dir := range claudeDesktopDirs(n.home) {
		environments, err := readClaudeDesktopEnvironments(dir)
		if err != nil {
			failures = append(failures, proto.WorkspaceSourceError{Source: proto.SourceClaudeDesktop, Error: err.Error()})
			continue
		}
		if environments == nil {
			continue
		}
		sources++
		for i, environment := range environments {
			id := claudeDesktopEnvironmentID(dir, environment.AccountID, environment.ID)
			seenRoots := map[string]bool{}
			for _, root := range append([]string{environment.Cwd}, environment.AdditionalFolders...) {
				if !filepath.IsAbs(root) || inICloud(root) || n.cwdBlocked(root) {
					continue
				}
				cwd := filepath.Clean(root)
				real := n.sessionWorkspaceCwd(cwd)
				if real != "" {
					cwd = real
				}
				key := folderKey(cwd)
				if seenRoots[key] {
					continue
				}
				seenRoots[key] = true
				out = append(out, proto.Workspace{ID: id + ":" + key, Source: proto.SourceClaudeDesktop, SourceID: id,
					Title: environment.Name, Cwd: cwd, ProjectID: id, ProjectName: environment.Name, ProjectPath: cwd,
					Index: i + 1, Online: true, Available: real != ""})
			}
		}
	}
	claudeSource := false
	for _, dir := range claudeDesktopDirs(n.home) {
		if st, err := os.Stat(filepath.Join(dir, "claude-code-sessions")); err == nil && st.IsDir() {
			claudeSource = true
			break
		}
	}
	if claudeSource {
		sources++
		index := readClaudeDesktopIndex(n.home)
		projects := map[string]string{}
		roots := map[string]string{}
		for _, about := range index.about {
			if _, saved := index.environments[about.environment]; saved {
				continue
			}
			root := claudeDesktopProjectRoot(firstNonEmpty(about.originCwd, about.cwd), projects)
			if root == "" || inICloud(root) || n.cwdBlocked(root) {
				continue
			}
			roots[folderKey(root)] = root
		}
		keys := make([]string, 0, len(roots))
		for key := range roots {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for i, key := range keys {
			root := roots[key]
			id, name := claudeDesktopDirectoryProject(root, n.home)
			if id == "" {
				continue
			}
			out = append(out, proto.Workspace{ID: id, Source: proto.SourceClaudeDesktop, SourceID: id,
				Title: name, Cwd: root, ProjectID: id, ProjectName: name, ProjectPath: root,
				Index: i + 1, Online: true, Available: n.sessionWorkspaceCwd(root) != ""})
		}
	}
	return out, sources, failures
}

func codexDesktopProjectID(home, id string) string {
	return "codex-project:" + codexHomeTag(home) + ":" + id
}

func claudeDesktopProjectRoot(cwd string, projects map[string]string) string {
	if !filepath.IsAbs(cwd) || inICloud(cwd) {
		return ""
	}
	return codexProjectRoot(cwd, projects)
}

// Directory-only projects share Codex's space and the session list's home/ancestor exclusions. Native saved
// environments keep their own identities even when several of them use the same directory.
func claudeDesktopDirectoryProject(root, home string) (id, name string) {
	rows := []proto.Session{{Agent: "claude", SourceKind: proto.SourceClaudeDesktop, RepoRoot: root}}
	placeClaudeDesktop(rows, nil, home)
	return rows[0].SpaceID, rows[0].SpaceName
}

func claudeDesktopEnvironmentID(profile, account, id string) string {
	return "claude-environment:" + codexHomeTag(folderKey(filepath.Clean(profile))+"\x00"+account) + ":" + id
}

// decorateClaudeDesktop uses the same project identity for live and offline chats. The original folder survives a
// desktop-created worktree or a later cwd change; the session's own working directory and transcript identity stay.
func (n *Node) decorateClaudeDesktop(s *proto.Session, index claudeDesktopIndex, projects map[string]string) {
	if n.sessionScope != nil || s.PaneID != "" || s.SpaceID != "" || s.Assistant || s.Agent != "claude" ||
		(s.SourceKind != "" && s.SourceKind != proto.SourceClaudeDesktop) {
		return
	}
	about, indexed := index.about[s.SessionID]
	if !indexed && s.SourceKind != proto.SourceClaudeDesktop && s.AgentSurface != transcript.SurfaceDesktop {
		return
	}
	s.AgentName, s.AgentSurface, s.SourceKind = "Claude Desktop", transcript.SurfaceDesktop, proto.SourceClaudeDesktop
	root := claudeDesktopProjectRoot(firstNonEmpty(about.originCwd, about.cwd, s.Cwd), projects)
	if environment, saved := index.environments[about.environment]; saved {
		if root == "" {
			root = claudeDesktopProjectRoot(environment.Cwd, projects)
		}
		s.RepoRoot = root
		s.SpaceKind, s.SpaceID, s.SpaceName = proto.SourceClaudeDesktop, about.environment, environment.Name
		return
	}
	if root == "" {
		return
	}
	s.RepoRoot = root
	if id, name := claudeDesktopDirectoryProject(root, n.home); id != "" {
		s.SpaceKind, s.SpaceID, s.SpaceName = proto.SourceClaudeDesktop, id, name
	}
}
