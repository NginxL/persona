package node

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// userSetupNode is a node whose home is home and whose own default Claude profile is ~/.claude.
func userSetupNode(t *testing.T, home string) *Node {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	n := assistantNode(t)
	n.home, n.dir = home, filepath.Join(home, ".persona")
	return n
}

// writtenSettings is what assistantCompactionSettings writes for a conversation in cwd and profile on settings.
func writtenSettings(t *testing.T, n *Node, cwd, settings, profile string) map[string]any {
	t.Helper()
	path, err := n.assistantCompactionSettings("test-id", cwd, settings, assistantCompaction{profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	s, err := readLauncherSettings(path, cwd, n.home)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func excludesOf(s map[string]any) []string {
	var out []string
	list, _ := s["claudeMdExcludes"].([]any)
	for _, v := range list {
		f, _ := v.(string)
		out = append(out, f)
	}
	return out
}

// homeSpellings is home as written and, when it differs, as its symlinks resolve (t.TempDir is below one on macOS).
func homeSpellings(t *testing.T, home string) []string {
	t.Helper()
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if real != home {
		return []string{home, real}
	}
	return []string{home}
}

// A conversation in a private profile no longer reads ~/.claude/CLAUDE.md and ~/.claude/rules as the home's project
// instructions, under either spelling of a symlinked home, and keeps the excludes it had; its own instructions and
// its profile's are never excluded. The user's own profile (~/.claude, or the node's CLAUDE_CONFIG_DIR) gets none.
func TestPrivateProfileExcludesTheHomeClaudeInstructions(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{real, link} {
		n := userSetupNode(t, home)
		cwd := filepath.Join(home, ".persona", "assistant")
		profile := filepath.Join(home, ".persona", "profiles", "claude")
		settings := filepath.Join(t.TempDir(), "settings.json")
		writeFile(t, settings, `{"claudeMdExcludes":["/elsewhere/CLAUDE.md"]}`)

		excludes := excludesOf(writtenSettings(t, n, cwd, settings, profile))
		want := []string{"/elsewhere/CLAUDE.md"}
		for _, h := range homeSpellings(t, home) {
			want = append(want, filepath.Join(h, ".claude", "CLAUDE.md"), filepath.Join(h, ".claude", "rules", "**"))
		}
		if !slices.Equal(excludes, want) {
			t.Errorf("home %s: excludes %v, want %v", home, excludes, want)
		}
		for _, own := range []string{filepath.Join(cwd, "CLAUDE.md"), filepath.Join(profile, "CLAUDE.md"), filepath.Join(profile, "rules", "**")} {
			if slices.Contains(excludes, own) {
				t.Errorf("home %s: its own %s is excluded", home, own)
			}
		}

		// The user's own profile, in any spelling: nothing added.
		for _, user := range []string{"", filepath.Join(home, ".claude"), filepath.Join(real, ".claude"), filepath.Join(link, ".claude")} {
			if got := excludesOf(writtenSettings(t, n, cwd, "", user)); len(got) != 0 {
				t.Errorf("home %s, profile %q: excludes %v", home, user, got)
			}
		}
	}

	// A node whose own default is a directory below Persona's profile root: a conversation there is in the user's
	// profile too, also when the machine's plain claude starts in another one.
	n := userSetupNode(t, real)
	own := filepath.Join(real, ".persona", "profiles", "main")
	t.Setenv("CLAUDE_CONFIG_DIR", own)
	for _, start := range []string{"", "~/.claude-main"} {
		n.claudeStartDir.Store(&start)
		if got := excludesOf(writtenSettings(t, n, filepath.Join(real, ".persona", "assistant"), "", own)); len(got) != 0 {
			t.Errorf("the node's own profile, claude_start_dir %q: excludes %v", start, got)
		}
	}
}

// The skills the user turned off in their own settings.json stay off in a private profile; nothing else of that file
// comes along (env, hooks, permissions, plugins, model, other switch values), and a name the conversation's own
// settings set (its profile's settings.json, cwd's .claude/settings.json and settings.local.json, the start's) keeps
// its value. In the user's own profile only the assistant's fixed switches are written.
func TestPrivateProfileCarriesTheUserSkillSwitchesOff(t *testing.T) {
	home := t.TempDir()
	n := userSetupNode(t, home)
	cwd := filepath.Join(home, ".persona", "assistant")
	profile := filepath.Join(home, ".persona", "profiles", "claude")
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
		"skillOverrides": {"lark-doc": "off", "notes": "off", "kept": "on", "named": "name-only", "explicit": "off", "profile-set": "off", "project-set": "off", "shared-set": "off"},
		"env": {"USER_SECRET": "user-token"},
		"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "cat ~/diary.md"}]}]},
		"permissions": {"allow": ["Bash"]},
		"enabledPlugins": {"notes@me": true},
		"model": "user-model"
	}`)
	writeFile(t, filepath.Join(profile, "settings.json"), `{"skillOverrides":{"profile-set":"on"}}`)
	writeFile(t, filepath.Join(cwd, ".claude", "settings.local.json"), `{"skillOverrides":{"project-set":"on"}}`)
	writeFile(t, filepath.Join(cwd, ".claude", "settings.json"), `{"skillOverrides":{"shared-set":"on"}}`)
	settings := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, settings, `{"skillOverrides":{"explicit":"on"}}`)

	s := writtenSettings(t, n, cwd, settings, profile)
	overrides, _ := s["skillOverrides"].(map[string]any)
	want := map[string]any{"lark-doc": "off", "notes": "off", "explicit": "on",
		"design": "off", "doctor": "off", "plugin-authoring": "off", "ui-ux-pro-max": "off"}
	if len(overrides) != len(want) {
		t.Errorf("skill switches %v, want %v", overrides, want)
	}
	for name, value := range want {
		if overrides[name] != value {
			t.Errorf("skill %s: %v, want %v (%v)", name, overrides[name], value, overrides)
		}
	}
	// Not off for the user, or set by the conversation's own settings: the start leaves it alone.
	for _, name := range []string{"kept", "named", "profile-set", "project-set", "shared-set"} {
		if v, set := overrides[name]; set {
			t.Errorf("skill %s: the start sets it to %v", name, v)
		}
	}
	raw, _ := json.Marshal(s)
	for _, bad := range []string{"USER_SECRET", "user-token", "diary.md", "permissions", "notes@me", "user-model"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("the user's %s came along: %s", bad, raw)
		}
	}

	// The user's own profile reads that file itself.
	overrides, _ = writtenSettings(t, n, cwd, settings, filepath.Join(home, ".claude"))["skillOverrides"].(map[string]any)
	if len(overrides) != 5 || overrides["explicit"] != "on" || overrides["lark-doc"] != nil {
		t.Errorf("the user's own profile: %v", overrides)
	}
}

// Only a profile Persona created is private. On a machine whose plain claude starts in claude_start_dir, that profile is
// the user's own: a conversation there (a pre-existing one resolves to it, legacyAssistantProfile) gets neither excludes
// nor switches, and the switches a private profile carries are the ones the user turned off there, not in a stale
// ~/.claude. A directory the user or a wrapper set up elsewhere stays as it is too; one an explicit pick puts below
// Persona's profile root is private.
func TestClaudeStartDirIsTheUserProfile(t *testing.T) {
	home := t.TempDir()
	n := userSetupNode(t, home)
	start := "~/.claude-main"
	n.claudeStartDir.Store(&start)
	cwd := filepath.Join(home, ".persona", "assistant")
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"skillOverrides":{"stale":"off"}}`)
	writeFile(t, filepath.Join(home, ".claude-main", "settings.json"), `{"skillOverrides":{"mine":"off"}}`)
	fixed := map[string]any{"design": "off", "doctor": "off", "plugin-authoring": "off", "ui-ux-pro-max": "off"}

	for _, profile := range []string{filepath.Join(home, ".claude-main"), filepath.Join(home, ".claude-work")} {
		if n.privateProfile(profile) {
			t.Errorf("%s counts as private", profile)
		}
		s := writtenSettings(t, n, cwd, "", profile)
		if got := excludesOf(s); len(got) != 0 {
			t.Errorf("%s: excludes %v", profile, got)
		}
		if overrides, _ := s["skillOverrides"].(map[string]any); !maps.Equal(overrides, fixed) {
			t.Errorf("%s: skill switches %v, want %v", profile, overrides, fixed)
		}
	}

	for _, profile := range []string{filepath.Join(home, ".persona", "profiles", "claude"), filepath.Join(home, ".persona", "profiles", "other")} {
		s := writtenSettings(t, n, cwd, "", profile)
		if !slices.Contains(excludesOf(s), filepath.Join(home, ".claude", "CLAUDE.md")) {
			t.Errorf("%s: excludes %v", profile, excludesOf(s))
		}
		overrides, _ := s["skillOverrides"].(map[string]any)
		if overrides["mine"] != "off" || overrides["stale"] != nil {
			t.Errorf("%s: skill switches %v, want mine off and no stale", profile, overrides)
		}
	}
	// Persona's profile root itself is no conversation's profile.
	if n.privateProfile(filepath.Join(home, ".persona", "profiles")) {
		t.Error("the profile root counts as private")
	}

	// With no claude_start_dir the user's switches come from the node's default.
	none := ""
	n.claudeStartDir.Store(&none)
	overrides, _ := writtenSettings(t, n, cwd, "", filepath.Join(home, ".persona", "profiles", "claude"))["skillOverrides"].(map[string]any)
	if overrides["stale"] != "off" || overrides["mine"] != nil {
		t.Errorf("no claude_start_dir: skill switches %v, want stale off and no mine", overrides)
	}
}

// A guest's settings stay what guestIsolation makes them: the private profile's additions to the start's settings
// reach it as neither excludes of its own nor skill switches.
func TestGuestSettingsIgnoreThePrivateProfileAdditions(t *testing.T) {
	home := t.TempDir()
	n := userSetupNode(t, home)
	cwd := filepath.Join(home, ".persona", "guest")
	profile := filepath.Join(home, ".persona", "profiles", "claude")
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"skillOverrides":{"lark-doc":"off"}}`)
	start, err := n.assistantCompactionSettings("guest-id", cwd, "", assistantCompaction{profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	path, err := n.guestSettingsFile("guest-id", cwd, profile, start, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readLauncherSettings(path, cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	alone := map[string]any{}
	guestIsolation(alone, cwd, home, profile)
	if want, _ := alone["claudeMdExcludes"].([]string); !slices.Equal(excludesOf(got), want) {
		t.Errorf("guest excludes %v, want %v", excludesOf(got), want)
	}
	if got["skillOverrides"] != nil {
		t.Errorf("guest skill switches %v", got["skillOverrides"])
	}
}

// Persona's skill picker lists, for a conversation in a private profile, what its settings let it use: the user's
// switched-off skills are left out, one its own settings switch on is listed.
func TestSkillCatalogMatchesThePrivateProfileSettings(t *testing.T) {
	fakeClaudeManagedSettings(t)
	f := cliTest(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	n := f.n
	n.home = os.Getenv("HOME")
	cwd := filepath.Join(n.home, ".persona", "assistant")
	profile := filepath.Join(n.dir, "profiles", "claude")  // the one a new conversation gets
	for _, name := range []string{"lark-doc", "worklog"} { // cwd's own project skills
		writeFile(t, filepath.Join(cwd, ".claude", "skills", name, "SKILL.md"), "---\nname: "+name+"\n---\nInstructions\n")
	}
	for _, name := range []string{"notes", "explicit"} {
		writeFile(t, filepath.Join(profile, "skills", name, "SKILL.md"), "---\nname: "+name+"\n---\nInstructions\n")
	}
	writeFile(t, filepath.Join(n.home, ".claude", "settings.json"), `{"skillOverrides":{"lark-doc":"off","notes":"off","explicit":"off"}}`)
	writeFile(t, filepath.Join(profile, "settings.json"), `{"skillOverrides":{"explicit":"on"}}`)

	catalog, err := n.skillCatalog(context.Background(), skillCatalogRequest{Owner: "helper", Cwd: cwd, Launcher: "claude", ConfigDir: profile})
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, e := range catalog.Skills {
		listed = append(listed, e.Name)
	}
	slices.Sort(listed)
	if !slices.Equal(listed, []string{"explicit", "worklog"}) {
		t.Errorf("catalog %v", listed)
	}
	overrides, _ := writtenSettings(t, n, cwd, "", profile)["skillOverrides"].(map[string]any)
	for _, name := range []string{"lark-doc", "notes", "explicit", "worklog"} {
		if hidden := overrides[name] == "off"; hidden == slices.Contains(listed, name) {
			t.Errorf("skill %s: listed %v, switched %v", name, slices.Contains(listed, name), overrides[name])
		}
	}
}

// The real start path: a new conversation in its private profile runs on settings with ~/.claude's instructions
// excluded; one in the user's default profile gets no excludes.
func TestAssistantStartInAPrivateProfileExcludesTheHomeInstructions(t *testing.T) {
	fakeClaudeHome(t, "for a in \"$@\"; do printf ' [%s]' \"$a\"; done > \"$HOME/args-$$\"\nread line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	n := codexTestNode(t)
	n.home = os.Getenv("HOME")
	n.dir = filepath.Join(n.home, ".persona")
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	withFakeHub(t, n)
	cwd := filepath.Join(n.dir, "assistant")
	for _, tt := range []struct {
		configDir string
		private   bool
	}{{"", true}, {"default", false}} {
		id, err := n.assistantSend(context.Background(), "", cwd, "claude", "hello", nil, "", "", modelExecution{ConfigDir: tt.configDir})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { n.stopAssistant(id) })
		line := startedArgs(t, "["+id+"]")
		_, after, _ := strings.Cut(line, "[--settings] [")
		path, _, _ := strings.Cut(after, "]")
		s, err := readLauncherSettings(path, cwd, n.home)
		if err != nil {
			t.Fatalf("config_dir %q: %v (%s)", tt.configDir, err, line)
		}
		excluded := slices.Contains(excludesOf(s), filepath.Join(n.home, ".claude", "CLAUDE.md"))
		if excluded != tt.private {
			t.Errorf("config_dir %q: ~/.claude/CLAUDE.md excluded %v, want %v (%v)", tt.configDir, excluded, tt.private, s["claudeMdExcludes"])
		}
	}
}
