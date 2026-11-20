package assistantskills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/identity"
)

// Plugin skills (docs/extensions/extensions.md "Assistant"): skills from the hub's plugins, kept on this machine in PluginDir as the hub
// sends them (assistant.prepare) and copied into the assistant's directory like the built-in ones. The user's own
// skill of the same name wins.

// PluginSkill is a plugin's skill as the hub sends it; Files only when this machine does not have its Digest yet.
type PluginSkill struct {
	Name   string            `json:"name"`
	Plugin string            `json:"plugin"`
	Digest string            `json:"digest"`
	Files  map[string][]byte `json:"files,omitempty"`
}

const pluginStamp = ".persona-plugin" // in each plugin skill: "<plugin> <digest>"

// PluginDir is where this machine keeps the plugins' skills (PERSONA_PLUGIN_SKILLS_DIR elsewhere, for tests).
func PluginDir() string {
	if d := identity.Getenv("PERSONA_PLUGIN_SKILLS_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.DataDir(home), "plugin-skills")
}

// ApplyPlugins makes PluginDir hold exactly the skills listed: those with files are written, those without are kept
// when their digest is the one here, and any other is removed. need are those listed without files that this machine
// does not have (the hub sends them again).
func ApplyPlugins(dir string, skills []PluginSkill) (need []string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var names []string
	for _, s := range skills {
		if s.Name == "" || s.Name != filepath.Base(s.Name) || strings.HasPrefix(s.Name, ".") {
			return nil, fmt.Errorf("plugin skill %q: not a name", s.Name)
		}
		names = append(names, s.Name)
		at := filepath.Join(dir, s.Name)
		if s.Files == nil {
			if stamp, _ := os.ReadFile(filepath.Join(at, pluginStamp)); strings.TrimSpace(string(stamp)) != s.Plugin+" "+s.Digest {
				need = append(need, s.Name)
			}
			continue
		}
		tmp := filepath.Join(dir, "."+s.Name+".new")
		_ = os.RemoveAll(tmp)
		for rel, b := range s.Files {
			clean := filepath.Clean(filepath.FromSlash(rel))
			if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
				return nil, fmt.Errorf("plugin skill %s: %q leaves the skill", s.Name, rel)
			}
			out := filepath.Join(tmp, clean)
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return nil, err
			}
			mode := os.FileMode(0o644)
			if strings.HasSuffix(rel, ".sh") || strings.HasPrefix(rel, "scripts/") || strings.HasPrefix(rel, "bin/") {
				mode = 0o755
			}
			if err := os.WriteFile(out, b, mode); err != nil {
				return nil, err
			}
		}
		if err := os.WriteFile(filepath.Join(tmp, pluginStamp), []byte(s.Plugin+" "+s.Digest+"\n"), 0o644); err != nil {
			return nil, err
		}
		_ = os.RemoveAll(at)
		if err := os.Rename(tmp, at); err != nil {
			return nil, err
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !slices.Contains(names, e.Name()) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	return need, nil
}

// pluginNames lists the plugin skills kept here.
func pluginNames(dir string) []string {
	var out []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	return out
}

const pluginsCopied = ".persona-extension-skills" // in the assistant's skills: the plugin skills last copied in, with digests

// installPlugins copies the plugin skills from plugins into dir, except those the user has their own of; one copied
// before and no longer there is removed. The stamp (pluginsCopied) lists only skills that really are copies in dir:
// one a user's link holds is left out, so it is copied once the user's goes (#1635).
func installPlugins(dir, plugins string, mine []string) error {
	var want []string
	for _, n := range pluginNames(plugins) {
		if slices.Contains(mine, n) || (n != workGuideName && slices.Contains(builtinNames(), n)) {
			continue
		}
		stamp, _ := os.ReadFile(filepath.Join(plugins, n, pluginStamp))
		want = append(want, n+"="+strings.TrimSpace(string(stamp)))
	}
	line := strings.Join(want, ",")
	stampFile := filepath.Join(dir, pluginsCopied)
	old, _ := os.ReadFile(stampFile)
	if strings.TrimSpace(string(old)) == line && slices.IndexFunc(want, func(w string) bool {
		n, _, _ := strings.Cut(w, "=")
		return !isCopy(filepath.Join(dir, n))
	}) < 0 {
		return nil
	}
	for _, entry := range strings.Split(strings.TrimSpace(string(old)), ",") {
		n, _, _ := strings.Cut(entry, "=")
		if n == "" || slices.ContainsFunc(want, func(w string) bool { return strings.HasPrefix(w, n+"=") }) {
			continue
		}
		if isCopy(filepath.Join(dir, n)) {
			_ = os.RemoveAll(filepath.Join(dir, n))
		}
	}
	var copied []string
	for _, w := range want {
		n, _, _ := strings.Cut(w, "=")
		if err := copyDir(filepath.Join(plugins, n), filepath.Join(dir, n)); err != nil {
			return err
		}
		if isCopy(filepath.Join(dir, n)) { // not when a user's link holds the name
			copied = append(copied, w)
		}
	}
	return os.WriteFile(stampFile, []byte(strings.Join(copied, ",")+"\n"), 0o644)
}

// isCopy: p is a directory of its own, not a link.
func isCopy(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.IsDir()
}

// copyDir replaces to with a copy of from.
func copyDir(from, to string) error {
	tmp := filepath.Join(filepath.Dir(to), "."+filepath.Base(to)+".new")
	_ = os.RemoveAll(tmp)
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		out := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return errors.New(p + ": not a regular file")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, info.Mode().Perm())
	})
	if err != nil {
		return err
	}
	if st, err := os.Lstat(to); err == nil && st.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(to); err == nil {
			_ = os.RemoveAll(tmp)
			return nil // the user's link: theirs wins
		}
		_ = os.Remove(to) // a link to a user's skill that is gone: the plugin's takes the name
	}
	_ = os.RemoveAll(to)
	return os.Rename(tmp, to)
}
