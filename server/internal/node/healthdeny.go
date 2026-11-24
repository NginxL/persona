package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
)

// healthDenyRules are the permission rules that keep an assistant conversation off the files of the spaces it is not
// in (docs/assistant/health.md "How directories and memory are isolated", #3282): every manager's health space, found
// here (the assistant directory's sibling, and the data directory's in case they differ), and the spaces the manager
// names (assistant.prepare deny_dirs), each with its Claude project directories (auto memory, transcripts). Deny
// rules hold over any allow rule: "Always allow in this session" and bypassPermissions included. A conversation in a
// space is never denied its own (a health conversation gets the other managers' health spaces only). Bash is not
// covered: a command can name a path any way it likes (the issue says what would).
func (n *Node) healthDenyRules(cwd string) []string {
	if cwd == "" {
		return nil
	}
	// A health conversation has its own space: every other manager's is denied to it (its own is not).
	own := ""
	if identity.SpaceOf(cwd) == identity.SpaceHealth {
		own = filepath.Base(filepath.Clean(cwd))
	}
	var dirs []string
	add := func(d string) {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	// Every manager's health space: the built-in's health, another's health-<id> (identity.HealthDirName), next to
	// the conversation's directory and in the data directory.
	roots := []string{filepath.Dir(cwd)}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Dir(identity.HealthDir(home)))
	}
	var candidates []string
	for _, root := range roots {
		if own != identity.SpaceHealth {
			candidates = append(candidates, filepath.Join(root, identity.SpaceHealth))
		}
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.IsDir() && e.Name() != identity.SpaceHealth && e.Name() != own && identity.IsManagerDirName(e.Name(), identity.SpaceHealth) {
				candidates = append(candidates, filepath.Join(root, e.Name()))
			}
		}
	}
	// The tracks the managers name (assistant.prepare deny_dirs): every one but the conversation's own.
	for _, d := range namedDenyDirs() {
		if filepath.Clean(d) != filepath.Clean(cwd) && !sameDir(d, cwd) {
			candidates = append(candidates, d)
		}
	}
	if own != "" { // the same space reached another way (a symlinked home) stays its own
		candidates = slices.DeleteFunc(candidates, func(d string) bool { return sameDir(d, cwd) })
	}
	for _, d := range candidates {
		add(filepath.Clean(d))
		if real, err := filepath.EvalSymlinks(d); err == nil {
			add(real) // Claude names a project after the directory as it resolved it
		}
	}
	paths := slices.Clone(dirs)
	for _, root := range n.projectDirs() {
		for _, d := range dirs {
			paths = append(paths, filepath.Join(root, nonAlnum.ReplaceAllString(d, "-")))
		}
	}
	var rules []string
	for _, p := range paths {
		p = filepath.ToSlash(p)
		if p[0] != '/' {
			p = "/" + p // a Windows drive path
		}
		for _, tool := range []string{"Read", "Edit"} { // Read covers the reading tools, Edit the writing ones
			rules = append(rules, tool+"(/"+p+"/**)")
		}
	}
	return rules
}

// denyDir is one track directory a manager denies its other conversations (assistant.prepare deny_dirs): relative to
// HOME, as assistant.prepare's dir, with its track's slug.
type denyDir struct {
	Dir   string `json:"dir"`
	Track string `json:"track"`
}

// setDenyDirs keeps the track directories the manager names on preparing its directory source, in place of what it
// named there before: the node then denies them to the manager's other conversations (healthDenyRules) and tells a
// conversation in one as that track's (identity.SpaceAt: a track's directory is data, never told from its path).
// One that names no directory below the state directory (an absolute path, "..", the state directory itself) or no
// valid track is dropped.
func (n *Node) setDenyDirs(source string, list []denyDir) {
	dirs := map[string]string{}
	for _, d := range list {
		sub, ok := compat.StateSubpath(filepath.ToSlash(d.Dir))
		if !ok || sub == "" || filepath.IsAbs(d.Dir) || strings.Contains(d.Dir, "..") || !identity.ValidTrackSlug(d.Track) {
			continue
		}
		dirs[filepath.Clean(n.assistantDirOf(d.Dir))] = d.Track
	}
	identity.SetTrackDirs(source, dirs)
}

// namedDenyDirs are the track directories the managers named (setDenyDirs), as absolute paths.
func namedDenyDirs() []string {
	var out []string
	for dir := range identity.TrackDirs() {
		out = append(out, dir)
	}
	slices.Sort(out)
	return out
}

// healthReportRules let a health conversation write the records of the reports uploaded to it (reports/, docs/assistant/health.md
// "checkup reports") without the phone asking each time; nil outside the health space, and for a path the comma-joined
// --allowedTools could not carry.
func healthReportRules(cwd string) []string {
	if cwd == "" || identity.SpaceOf(cwd) != identity.SpaceHealth || strings.Contains(cwd, ",") {
		return nil
	}
	// Edit(path) covers Write too: Claude Code matches path rules on Read and Edit only. Claude Code names a file by
	// the directory as it resolved it (<target>/health/... for a ~/.persona that is a link): both spellings.
	rules := []string{"Edit(/" + filepath.Join(cwd, "reports") + "/**)"}
	if real := realPath(filepath.Clean(cwd)); real != filepath.Clean(cwd) && !strings.Contains(real, ",") {
		rules = append(rules, "Edit(/"+filepath.Join(real, "reports")+"/**)")
	}
	return rules
}

// sameDir: a and b are the same directory (as they resolve), or the same path when either cannot be read.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}
