package node

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Native project metadata is read to place new desktop chats in an existing
// project. Persona does not write the desktop project's database or sidebar state.
type codexDesktopProject struct {
	ID       string                    `json:"id"`
	Name     string                    `json:"name"`
	Roots    []codexDesktopProjectRoot `json:"roots"`
	Position int64                     `json:"position"`
}

type codexDesktopProjectRoot struct {
	Path string `json:"path"`
}

// listCodexDesktopProjects reads every page in the app-server's position order.
// A broken/repeated cursor is an error rather than an infinite startup loop.
func listCodexDesktopProjects(call codexAppCall) ([]codexDesktopProject, error) {
	var projects []codexDesktopProject
	var cursor *string
	seenCursors := map[string]bool{}
	seenIDs := map[string]bool{}
	for {
		var response struct {
			Data       []codexDesktopProject `json:"data"`
			NextCursor *string               `json:"nextCursor"`
		}
		if err := call("project/list", map[string]any{"cursor": cursor, "limit": 100, "sortKey": "position"}, &response); err != nil {
			return nil, err
		}
		for _, project := range response.Data {
			if strings.TrimSpace(project.ID) == "" {
				return nil, errors.New("project/list: project has no id")
			}
			if seenIDs[project.ID] {
				continue
			}
			seenIDs[project.ID] = true
			projects = append(projects, project)
		}
		if response.NextCursor == nil {
			return projects, nil
		}
		if len(seenCursors) >= 100 || *response.NextCursor == "" || seenCursors[*response.NextCursor] {
			return nil, errors.New("project/list: invalid or repeated pagination cursor")
		}
		seenCursors[*response.NextCursor] = true
		cursor = response.NextCursor
	}
}

// codexDesktopProjectIDForDir selects the most specific registered root. Windows
// spelling differences and symlinks are normalized as for the desktop catalog;
// sibling names with a shared prefix do not match. No project is created here.
func codexDesktopProjectIDForDir(projects []codexDesktopProject, cwd string) string {
	if !filepath.IsAbs(cwd) {
		return ""
	}
	cwd = folderKey(realPath(filepath.Clean(cwd)))
	var best codexDesktopProject
	bestRoot := ""
	for _, project := range projects {
		if project.ID == "" {
			continue
		}
		for _, root := range project.Roots {
			if !filepath.IsAbs(root.Path) {
				continue
			}
			path := folderKey(realPath(filepath.Clean(root.Path)))
			if !pathWithin(cwd, path) {
				continue
			}
			if len(path) > len(bestRoot) || len(path) == len(bestRoot) &&
				(project.Position < best.Position || project.Position == best.Position && project.ID < best.ID) {
				best, bestRoot = project, path
			}
		}
	}
	return best.ID
}

// readCodexProjectSelection is a read-only snapshot seam for selection tests.
var readCodexProjectSelection = codexDesktopProjectSnapshot

// parseCodexDesktopProjectID resolves a catalog source id to the current home's
// native API id. Migrated projects keep their legacy catalog identity, so a
// verified native-to-legacy alias must be reversed before using projectId.
func parseCodexDesktopProjectID(home, sourceID string) (string, error) {
	if sourceID == "" {
		return "", nil
	}
	canonicalHome := canonicalCodexHome(home, "")
	if canonicalHome == "" {
		return "", errors.New("invalid Codex home for project selection")
	}
	id, ok := strings.CutPrefix(sourceID, codexDesktopProjectID(canonicalHome, ""))
	if !ok && home != canonicalHome {
		id, ok = strings.CutPrefix(sourceID, codexDesktopProjectID(home, ""))
	}
	if !ok || strings.TrimSpace(id) == "" {
		return "", errors.New("the selected Codex project belongs to a different profile or has an invalid identifier")
	}
	snapshot, err := readCodexProjectSelection(canonicalHome)
	if err != nil {
		return "", fmt.Errorf("read selected Codex project: %w", err)
	}
	// A card created by Persona may still hold the native id after the desktop
	// migrates its catalog row to the equivalent legacy id.
	if _, exists := snapshot.NativeProjectIDs[id]; exists {
		return id, nil
	}
	if _, exists := snapshot.Projects[id]; !exists {
		return "", errors.New("the selected Codex project no longer exists")
	}
	nativeID := ""
	for native, canonical := range snapshot.NativeProjectIDs {
		if canonical != id {
			continue
		}
		if nativeID != "" && nativeID != native {
			return "", errors.New("the selected Codex project has ambiguous native project mappings")
		}
		nativeID = native
	}
	if nativeID == "" {
		return "", errors.New("the selected legacy Codex project is not available through the desktop project API yet; open or update it in Codex first")
	}
	return nativeID, nil
}

// selectCodexDesktopProject never substitutes another project for an explicit
// selection, even if multiple projects share a directory. Live API roots are
// checked immediately before thread/start so stale cards cannot misassign chats.
func selectCodexDesktopProject(projects []codexDesktopProject, cwd, projectID string) (string, error) {
	if projectID == "" {
		return codexDesktopProjectIDForDir(projects, cwd), nil
	}
	for _, project := range projects {
		if project.ID != projectID {
			continue
		}
		if codexDesktopProjectIDForDir([]codexDesktopProject{project}, cwd) == projectID {
			return projectID, nil
		}
		return "", errors.New("the selected directory is outside the selected Codex project's roots")
	}
	return "", errors.New("the selected Codex project is no longer available from the desktop project API")
}
