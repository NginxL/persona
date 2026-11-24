// Package assistantskills puts the assistant's skills in its directory (<assistant dir>/.claude/skills,
// docs/operations/hub-update.md "Skills move out of the binary"): the user's own, kept in ~/.persona/skills/<name> on the machine that runs
// the assistant and linked in, and the built-in ones this binary carries, copied in. Built-in skills are only those
// Persona may redistribute (persona-office, personad office's): a copied one, like an extension's skill a user put
// there by hand, lives in the user's space. Updates do not overwrite it. Explicit Web ZIP imports may add a new
// name there; their narrow Undo moves only an unchanged new installation to hidden recovery.
package assistantskills

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/identity"
)

//go:embed all:builtin
var embedded embed.FS

// builtin is the skills this binary carries: one directory each under builtin/ (its README is not one).
var builtin, _ = fs.Sub(embedded, "builtin")

// Redistributable is every built-in skill and why Persona may ship it (TestBuiltinsAreRedistributable).
var Redistributable = map[string]string{
	workGuideName:       "Persona’s own Assistant work management guide",
	MachineOpsSkillName: "Persona's opt-in read-only machine diagnostics guide (docs/server/machines.md)",
	"persona-office":    "Persona's own (personad office's skill, docs/assistant/office.md)",
}

// ForAgents are the built-in skills the agents Persona starts get too, as the plugin WritePlugin makes (the
// assistant gets the selected built-ins in its directory). Only for the agents Persona starts: nothing goes into a
// machine's own Claude or Codex skills, so sessions the user starts themselves are left as they are (#1300).
var ForAgents = []string{"persona-office"}

// Requires are the built-in skills that need a connector of the hub signed in (its id): put in while it is, taken
// out once it is not (the hub says which are with assistant.prepare). None now: Telegram's skill, the one that did,
// comes with its extension (#1704), as a plugin skill (plugins.go).
var Requires = map[string]string{}

// Selection is the manager's provisioning state for one Assistant directory.
type Selection struct {
	Connectors []string
	WorkGuide  bool
	MachineOps bool
}

// wanted says whether the built-in skill name goes in with the connectors signed in.
func (s Selection) wanted(name string) bool {
	if name == workGuideName {
		return s.WorkGuide && !slices.Contains(pluginNames(PluginDir()), name)
	}
	if name == MachineOpsSkillName {
		return s.MachineOps && !slices.Contains(pluginNames(PluginDir()), name)
	}
	need, ok := Requires[name]
	return !ok || slices.Contains(s.Connectors, need)
}

const (
	// Codex can discover both native skill roots. One canonical file keeps a single catalog entry.
	runtimeGuideLink = "../../.claude/skills/" + workGuideName
	workGuideName    = "persona-work"
	// MachineOpsSkillName is the built-in opt-in read-only machine diagnostics skill (#13053).
	MachineOpsSkillName  = "persona-machine-ops"
	machineOpsDigestFile = ".persona-machine-ops-digest"
	builtinStamp         = ".persona-builtin" // the built-in skills last copied in
	legacyStamp          = ".persona-skills"  // what older personad binaries left next to their copies
)

// ErrSkillConflict is returned when an opt-in skill directory already exists and is not managed by Persona.
var ErrSkillConflict = errors.New("a local persona-machine-ops skill already exists and is not managed by Persona")

// UserDir is the user's skill space: ~/.persona/skills (PERSONA_SKILLS_DIR elsewhere, for tests).
func UserDir() string {
	if d := identity.Getenv("PERSONA_SKILLS_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.DataDir(home), "skills")
}

// Skill is one skill the assistant gets.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"` // user | builtin | plugin
}

// builtinNames lists the built-in skills.
func builtinNames() []string {
	var out []string
	entries, _ := fs.ReadDir(builtin, ".")
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// userNames lists the skills in the user's space: directories with a SKILL.md, not hidden.
func userNames(user string) []string {
	var out []string
	entries, _ := os.ReadDir(user)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if st, err := os.Stat(filepath.Join(user, e.Name(), "SKILL.md")); err == nil && !st.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// Sync makes dir (<assistant dir>/.claude/skills) hold the user's skills, as links into user, and the built-in ones
// the user has none of, as copies: those that need a connector (Requires) only while it is among connectors, the hub's
// signed in. A link into user whose skill is gone is removed; a directory the user put in dir themselves is left
// alone. logf reports what changed.
func Sync(dir, user string, selection Selection, logf func(string, ...any)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	migrate(dir, user, logf)
	mine := userNames(user)
	if err := installBuiltins(dir, mine, selection); err != nil {
		return err
	}
	if err := installPlugins(dir, PluginDir(), mine); err != nil {
		return err
	}
	for _, name := range mine {
		target := filepath.Join(user, name)
		at := filepath.Join(dir, name)
		if cur, err := os.Readlink(at); err == nil {
			if cur == target || name == workGuideName || name == MachineOpsSkillName {
				continue
			}
			_ = os.Remove(at)
		} else if st, err := os.Lstat(at); err == nil && st.IsDir() {
			if name == workGuideName || name == MachineOpsSkillName || !slices.Contains(builtinNames(), name) {
				logf("assistant skills: %s is a directory of its own in %s; %s is not linked over it", name, dir, target)
				continue
			}
			_ = os.RemoveAll(at) // a built-in copy: the user's skill of that name wins
		}
		if err := os.Symlink(target, at); err != nil {
			return err
		}
	}
	// Links into the user's space whose skill is gone.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		at := filepath.Join(dir, e.Name())
		target, err := os.Readlink(at)
		if err != nil || filepath.Dir(target) != filepath.Clean(user) || slices.Contains(mine, e.Name()) {
			continue
		}
		_ = os.Remove(at)
	}
	return nil
}

// migrate moves the skills an older personad copied into dir (the stamp says it did: every real directory there but
// the built-in ones) into the user's space, once: the user keeps them, and Sync links them back.
func migrate(dir, user string, logf func(string, ...any)) {
	if _, err := os.Stat(filepath.Join(dir, legacyStamp)); err != nil {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || (name != workGuideName && name != MachineOpsSkillName && slices.Contains(builtinNames(), name)) {
			continue // a link (the user's), a stamp, or what installBuiltins puts in
		}
		if _, err := os.Stat(filepath.Join(dir, name, pluginStamp)); err == nil {
			continue // an extension's skill (installPlugins): it stays the extension's
		}
		from, to := filepath.Join(dir, name), filepath.Join(user, name)
		if _, err := os.Stat(to); err == nil {
			_ = os.RemoveAll(from) // the user's space has its own: the old copy goes, Sync links theirs
			continue
		}
		if err := os.MkdirAll(user, 0o755); err != nil {
			logf("assistant skills: move %s to %s: %v", name, user, err)
			return
		}
		if err := os.Rename(from, to); err != nil {
			logf("assistant skills: move %s to %s: %v", name, user, err)
			return
		}
		logf("assistant skills: moved %s to %s (skills are no longer in personad)", name, to)
	}
	_ = os.Remove(filepath.Join(dir, legacyStamp))
}

// builtinVersion identifies the built-in skills (a hash of every file).
func builtinVersion() string {
	h := sha256.New()
	_ = fs.WalkDir(builtin, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(builtin, p)
			h.Write([]byte(p))
			h.Write(b)
		}
		return err
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func installedNames(stamp []byte) []string {
	if fields := strings.Fields(string(stamp)); len(fields) == 2 {
		return strings.Split(fields[1], ",")
	}
	return nil
}

// WorkGuideInstalled distinguishes our managed guide from a user's or an extension's same-name skill.
func WorkGuideInstalled(dir string) bool {
	stamp, _ := os.ReadFile(filepath.Join(dir, builtinStamp))
	return slices.Contains(installedNames(stamp), workGuideName) && ownsWorkGuide(dir, stamp) && workGuideFilePresent(dir)
}

// WorkGuideReady reports whether the assistant workspace can use compact instructions in both runtimes.
func WorkGuideReady(cwd string) bool {
	return cwd != "" && WorkGuideInstalled(filepath.Join(cwd, ".claude", "skills")) &&
		WorkGuideInstalled(filepath.Join(cwd, ".agents", "skills"))
}

const guideDigestFile = ".persona-work-digest"

func filePresent(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func codexSkillDir(dir string) bool {
	return filepath.Base(dir) == "skills" && filepath.Base(filepath.Dir(dir)) == ".agents"
}

// A directory-wide stamp outlives a user's replacement of one of its entries. Check
// the exact Codex link or the canonical copy's recorded tree before touching either.
func ownsWorkGuide(dir string, stamp []byte) bool {
	at := filepath.Join(dir, workGuideName)
	if codexSkillDir(dir) {
		link, err := os.Readlink(at)
		return err == nil && link == filepath.FromSlash(runtimeGuideLink)
	}
	st, err := os.Lstat(at)
	if err != nil || !st.IsDir() {
		return false
	}
	if saved, err := os.ReadFile(filepath.Join(dir, guideDigestFile)); err == nil {
		hash, err := importTree(context.Background(), at)
		return err == nil && hash == strings.TrimSpace(string(saved))
	}
	// Earlier installations only recorded the combined embedded version. Rebuild
	// it with this on-disk guide, including extra files, before adopting the copy.
	version, _, _ := strings.Cut(string(stamp), " ")
	h := sha256.New()
	write := func(source fs.FS, path string, entry fs.DirEntry) error {
		if !entry.Type().IsRegular() {
			return fs.ErrInvalid
		}
		info, err := entry.Info()
		if err != nil || info.Size() > MaxImportFile {
			return fs.ErrInvalid
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(data)
		return nil
	}
	err = fs.WalkDir(builtin, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == workGuideName {
			src := os.DirFS(dir)
			err := fs.WalkDir(src, path, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				return write(src, p, d)
			})
			if err != nil {
				return err
			}
			return fs.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		return write(builtin, path, entry)
	})
	return err == nil && version == hex.EncodeToString(h.Sum(nil))[:12]
}

func workGuideFilePresent(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, workGuideName, "SKILL.md"))
	return err == nil && st.Mode().IsRegular()
}

// installBuiltins copies the built-in skills into dir, except those the user has their own of and those whose
// connector is not signed in; the stamp (version and what was copied) skips an identical install and lets a copy no
// longer carried, or no longer wanted, be removed.
func installBuiltins(dir string, mine []string, selection Selection) error {
	stamp := filepath.Join(dir, builtinStamp)
	old, _ := os.ReadFile(stamp)
	installed := installedNames(old)
	ownsGuide := slices.Contains(installed, workGuideName) && ownsWorkGuide(dir, old)
	ownsOps := slices.Contains(installed, MachineOpsSkillName) && ownsManagedMachineOps(dir)
	var guideLink string
	if codexSkillDir(dir) {
		guideLink = filepath.FromSlash(runtimeGuideLink)
		if selection.WorkGuide {
			selection.WorkGuide = WorkGuideInstalled(filepath.Dir(filepath.Join(dir, guideLink)))
			// Older nodes drop unknown names from their inventory but leave symlinks.
			// Reclaim only the exact alias to a verified, managed canonical guide.
			if selection.WorkGuide && !ownsGuide {
				ownsGuide = ownsWorkGuide(dir, old)
			}
		}
	}
	var names []string
	for _, n := range builtinNames() {
		if n == workGuideName && !ownsGuide {
			// An upgrade may find a handwritten skill here. Leave it in place.
			if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if n == MachineOpsSkillName && !ownsOps {
			// Leave a handwritten or user-managed persona-machine-ops directory untouched.
			if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if !slices.Contains(mine, n) && selection.wanted(n) {
			names = append(names, n)
		}
	}
	want := builtinVersion() + " " + strings.Join(names, ",")
	if strings.TrimSpace(string(old)) == want {
		workOK := !slices.Contains(names, workGuideName)
		if !workOK {
			target, _ := os.Readlink(filepath.Join(dir, workGuideName))
			workOK = target == guideLink && workGuideFilePresent(dir) && (guideLink != "" || filePresent(filepath.Join(dir, guideDigestFile)))
		}
		opsOK := !slices.Contains(names, MachineOpsSkillName) || (ownsManagedMachineOps(dir) && filePresent(filepath.Join(dir, MachineOpsSkillName, "SKILL.md")))
		if workOK && opsOK {
			return nil
		}
	}
	for _, n := range installed {
		if fs.ValidPath(n) && n != "." && filepath.Base(n) == n && !slices.Contains(names, n) {
			if n == workGuideName && !ownsGuide {
				continue
			}
			if n == MachineOpsSkillName && !ownsOps {
				continue
			}
			at := filepath.Join(dir, n)
			target, _ := os.Readlink(at)
			managedLink := n == workGuideName && guideLink != "" && target == guideLink
			if st, err := os.Lstat(at); err == nil && (st.IsDir() || managedLink) {
				if err := os.RemoveAll(at); err != nil {
					return err
				}
			}
		}
	}
	for _, name := range names {
		tmp := filepath.Join(dir, "."+name+".new")
		_ = os.RemoveAll(tmp)
		if name == workGuideName && guideLink != "" {
			if err := os.Symlink(guideLink, tmp); err != nil {
				return err
			}
		} else if err := copyBuiltin(name, tmp); err != nil {
			return err
		}
		final := filepath.Join(dir, name)
		_ = os.RemoveAll(final)
		if err := os.Rename(tmp, final); err != nil {
			return err
		}
	}
	if guideLink == "" && slices.Contains(names, workGuideName) {
		digest, err := importTree(context.Background(), filepath.Join(dir, workGuideName))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, guideDigestFile), []byte(digest+"\n"), 0o600); err != nil {
			return err
		}
	} else if err := os.Remove(filepath.Join(dir, guideDigestFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if slices.Contains(names, MachineOpsSkillName) {
		digest, err := importTree(context.Background(), filepath.Join(dir, MachineOpsSkillName))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, machineOpsDigestFile), []byte(digest+"\n"), 0o600); err != nil {
			return err
		}
	} else if ownsOps {
		if err := os.Remove(filepath.Join(dir, machineOpsDigestFile)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.WriteFile(stamp, []byte(want+"\n"), 0o644)
}

// copyBuiltin writes the built-in skill name into dir, its scripts (*.sh) runnable.
func copyBuiltin(name, dir string) error {
	return fs.WalkDir(builtin, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dir, strings.TrimPrefix(p, name))
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := fs.ReadFile(builtin, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(out, b, mode)
	})
}

// pluginManifest is the plugin's .claude-plugin/plugin.json.
const pluginManifest = `{
  "name": "persona",
  "description": "Persona's skills for the agents it starts (docs/assistant/office.md)",
  "version": "%s"
}
`

// PluginIssues says what differs between dir and the plugin WritePlugin would make there, as slash-separated paths
// under dir ("missing", alone, when dir is not there): nil when it is current. It reads, never writes.
func PluginIssues(dir string) []string {
	if _, err := os.Stat(dir); err != nil {
		return []string{"missing"}
	}
	want := builtinVersion()
	expected := map[string][]byte{
		builtinStamp:                 []byte(want + "\n"),
		".claude-plugin/plugin.json": []byte(fmt.Sprintf(pluginManifest, want)),
	}
	for _, name := range ForAgents {
		_ = fs.WalkDir(builtin, name, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				b, _ := fs.ReadFile(builtin, p)
				expected["skills/"+p] = b
			}
			return err
		})
	}
	var issues []string
	for rel, b := range expected {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		switch {
		case err != nil:
			issues = append(issues, "missing "+rel)
		case !bytes.Equal(got, b):
			issues = append(issues, "differs "+rel)
		}
	}
	slices.Sort(issues)
	return issues
}

// WritePlugin makes dir the Claude Code plugin the agents Persona starts load (claude --plugin-dir dir): the
// ForAgents built-in skills under skills/. Written again only when what this binary carries changed (the stamp), and
// swapped in whole, so a session reading it never sees half of one.
func WritePlugin(dir string) error {
	want := builtinVersion()
	if len(PluginIssues(dir)) == 0 {
		return nil
	}
	tmp := dir + ".new"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, ".claude-plugin"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude-plugin", "plugin.json"), []byte(fmt.Sprintf(pluginManifest, want)), 0o644); err != nil {
		return err
	}
	for _, name := range ForAgents {
		if err := copyBuiltin(name, filepath.Join(tmp, "skills", name)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, builtinStamp), []byte(want+"\n"), 0o644); err != nil {
		return err
	}
	old := dir + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	return os.RemoveAll(old)
}

// List is the skills the assistant gets on this machine: the user's, then the built-in ones they have none of whose
// connector, if they need one, is signed in.
func List(user string, selection Selection) []Skill {
	out := []Skill{}
	mine := userNames(user)
	for _, n := range mine {
		b, _ := os.ReadFile(filepath.Join(user, n, "SKILL.md"))
		out = append(out, Skill{Name: n, Description: description(string(b)), Source: "user"})
	}
	for _, n := range builtinNames() {
		if !slices.Contains(mine, n) && selection.wanted(n) {
			b, _ := fs.ReadFile(builtin, n+"/SKILL.md")
			out = append(out, Skill{Name: n, Description: description(string(b)), Source: "builtin"})
		}
	}
	for _, n := range pluginNames(PluginDir()) {
		if !slices.Contains(mine, n) && (n == workGuideName || n == MachineOpsSkillName || !slices.Contains(builtinNames(), n)) {
			b, _ := os.ReadFile(filepath.Join(PluginDir(), n, "SKILL.md"))
			out = append(out, Skill{Name: n, Description: description(string(b)), Source: "plugin"})
		}
	}
	return out
}

// MachineOpsSkillStatus reports whether the opt-in persona-machine-ops skill is installed in Dir.
type MachineOpsSkillStatus struct {
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Managed   bool   `json:"managed"`
	Conflict  bool   `json:"conflict,omitempty"`
	Version   string `json:"version"`
}

func resolveMachineOpsDir(baseDir string) string {
	if strings.TrimSpace(baseDir) == "" {
		return UserDir()
	}
	return filepath.Clean(baseDir)
}

// ownsManagedMachineOps reports whether baseDir/persona-machine-ops is an unmodified Persona-managed copy.
func ownsManagedMachineOps(baseDir string) bool {
	at := filepath.Join(baseDir, MachineOpsSkillName)
	st, err := os.Lstat(at)
	if err != nil || !st.IsDir() {
		return false
	}
	saved, err := os.ReadFile(filepath.Join(baseDir, machineOpsDigestFile))
	if err != nil {
		return false
	}
	hash, err := importTree(context.Background(), at)
	return err == nil && hash == strings.TrimSpace(string(saved))
}

// MachineOpsStatus inspects whether persona-machine-ops is installed in baseDir (default UserDir).
func MachineOpsStatus(baseDir string) (MachineOpsSkillStatus, error) {
	dir := resolveMachineOpsDir(baseDir)
	at := filepath.Join(dir, MachineOpsSkillName)
	status := MachineOpsSkillStatus{
		Name:    MachineOpsSkillName,
		Dir:     dir,
		Path:    at,
		Version: builtinVersion(),
	}
	_, err := os.Lstat(at)
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	status.Installed = true
	if ownsManagedMachineOps(dir) && filePresent(filepath.Join(at, "SKILL.md")) {
		status.Managed = true
	} else {
		status.Conflict = true
	}
	return status, nil
}

// InstallMachineOps explicitly installs or refreshes the opt-in persona-machine-ops skill in baseDir
// (default UserDir). It refuses to overwrite an existing user-authored or modified skill of the same name.
func InstallMachineOps(baseDir string) (MachineOpsSkillStatus, error) {
	dir := resolveMachineOpsDir(baseDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return MachineOpsSkillStatus{}, err
	}
	status, err := MachineOpsStatus(dir)
	if err != nil {
		return status, err
	}
	if status.Conflict {
		return status, ErrSkillConflict
	}
	tmp := filepath.Join(dir, "."+MachineOpsSkillName+".new")
	_ = os.RemoveAll(tmp)
	if err := copyBuiltin(MachineOpsSkillName, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return status, err
	}
	digest, err := importTree(context.Background(), tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return status, err
	}
	final := filepath.Join(dir, MachineOpsSkillName)
	if status.Installed {
		if err := os.RemoveAll(final); err != nil {
			_ = os.RemoveAll(tmp)
			return status, err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.RemoveAll(tmp)
		return status, err
	}
	if err := os.WriteFile(filepath.Join(dir, machineOpsDigestFile), []byte(digest+"\n"), 0o600); err != nil {
		return status, err
	}
	return MachineOpsStatus(dir)
}

// UninstallMachineOps removes a Persona-managed persona-machine-ops installation from baseDir
// (default UserDir). It refuses to delete a user-authored or locally modified skill of the same name.
func UninstallMachineOps(baseDir string) (MachineOpsSkillStatus, error) {
	dir := resolveMachineOpsDir(baseDir)
	status, err := MachineOpsStatus(dir)
	if err != nil {
		return status, err
	}
	if !status.Installed {
		_ = os.Remove(filepath.Join(dir, machineOpsDigestFile))
		return status, nil
	}
	if status.Conflict {
		return status, ErrSkillConflict
	}
	if err := os.RemoveAll(status.Path); err != nil {
		return status, err
	}
	if err := os.Remove(filepath.Join(dir, machineOpsDigestFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return status, err
	}
	return MachineOpsStatus(dir)
}

// description is the `description:` of a SKILL.md's front matter (a folded `>` or `|` one joined on one line).
func description(md string) string {
	// ZIP imports accept real YAML (including quoted keys/escapes and block scalars).
	// Preserve the historical permissive reader for older hand-written manifests.
	if _, desc, err := importMetadata([]byte(md)); err == nil {
		return strings.Join(strings.Fields(desc), " ")
	}
	sc := bufio.NewScanner(strings.NewReader(md))
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return ""
	}
	var folded []string
	inDesc := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		if inDesc {
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				folded = append(folded, strings.TrimSpace(line))
				continue
			}
			break
		}
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			v = strings.TrimSpace(v)
			if v == ">" || v == "|" || v == ">-" || v == "|-" || v == "" {
				inDesc = true
				continue
			}
			return strings.Trim(v, `"'`)
		}
	}
	return strings.Join(folded, " ")
}
