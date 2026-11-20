package assistantskills

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The plugins' skills: kept as the hub sends them, copied into the assistant's directory, the user's own of a name
// winning, gone when the plugin is; one the hub thinks this machine has but it does not is asked for again.
func TestPluginSkills(t *testing.T) {
	plugins, user, dir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", plugins)
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", plugins)
	feed := PluginSkill{Name: "example-feed", Plugin: "example", Digest: "d1", Files: map[string][]byte{
		"SKILL.md": []byte("---\nname: example-feed\ndescription: Read the feed.\n---\n"), "scripts/run.sh": []byte("#!/bin/sh\n")}}
	notes := PluginSkill{Name: "example-notes", Plugin: "example", Digest: "d2", Files: map[string][]byte{"SKILL.md": []byte("notes\n")}}
	if need, err := ApplyPlugins(plugins, []PluginSkill{feed, notes}); err != nil || len(need) != 0 {
		t.Fatal(need, err)
	}
	if fi, err := os.Stat(filepath.Join(plugins, "example-feed", "scripts", "run.sh")); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatal("scripts are executable", err)
	}
	// The user's own example-notes wins.
	_ = os.MkdirAll(filepath.Join(user, "example-notes"), 0o755)
	_ = os.WriteFile(filepath.Join(user, "example-notes", "SKILL.md"), []byte("mine\n"), 0o644)
	if err := Sync(dir, user, Selection{}, t.Logf); err != nil {
		t.Fatal(err)
	}
	if stamp, err := os.ReadFile(filepath.Join(dir, ".persona-extension-skills")); err != nil || string(stamp) != "example-feed=example d1\n" {
		t.Fatalf("canonical copied-skill stamp: %q %v", stamp, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "example-feed", "SKILL.md")); len(b) == 0 {
		t.Fatal("the plugin's skill is copied in")
	}
	if target, _ := os.Readlink(filepath.Join(dir, "example-notes")); target != filepath.Join(user, "example-notes") {
		t.Fatal("the user's own wins:", target)
	}
	var sources []string
	for _, s := range List(user, Selection{}) {
		sources = append(sources, s.Name+"/"+s.Source)
	}
	if !slices.Contains(sources, "example-feed/plugin") || !slices.Contains(sources, "example-notes/user") || slices.Contains(sources, "example-notes/plugin") {
		t.Fatal(sources)
	}

	// Without files: kept when the digest is the one here, asked for again when not.
	if need, _ := ApplyPlugins(plugins, []PluginSkill{{Name: "example-feed", Plugin: "example", Digest: "d1"}, {Name: "example-notes", Plugin: "example", Digest: "d9"}}); !slices.Equal(need, []string{"example-notes"}) {
		t.Fatal(need)
	}
	// The plugin removed: its skills go, here and in the assistant's directory.
	if _, err := ApplyPlugins(plugins, []PluginSkill{}); err != nil {
		t.Fatal(err)
	}
	if err := Sync(dir, user, Selection{}, t.Logf); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "example-feed")); err == nil {
		t.Fatal("still in the assistant's directory")
	}
	if stamp, err := os.ReadFile(filepath.Join(dir, ".persona-extension-skills")); err != nil || string(stamp) != "\n" {
		t.Fatalf("removed skill remains in the canonical stamp: %q %v", stamp, err)
	}
	if _, err := ApplyPlugins(plugins, []PluginSkill{{Name: "../x", Plugin: "p", Digest: "d", Files: map[string][]byte{"a": nil}}}); err == nil {
		t.Fatal("a name that leaves the directory")
	}
	if _, err := ApplyPlugins(plugins, []PluginSkill{{Name: "x", Plugin: "p", Digest: "d", Files: map[string][]byte{"../../a": nil}}}); err == nil {
		t.Fatal("a file that leaves the skill")
	}
}

// The user's own skill of a plugin skill's name goes (moved away): the plugin's takes the name at the next sync and
// stays there, whatever was recorded before (#1635).
func TestPluginSkillAfterTheUsersGoes(t *testing.T) {
	plugins, user, dir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", plugins)
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", plugins)
	sk := PluginSkill{Name: "example-feed", Plugin: "example", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("plugin\n")}}
	if _, err := ApplyPlugins(plugins, []PluginSkill{sk}); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(user, "example-feed"), 0o755)
	_ = os.WriteFile(filepath.Join(user, "example-feed", "SKILL.md"), []byte("mine\n"), 0o644)
	if err := Sync(dir, user, Selection{}, t.Logf); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(user, "example-feed")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Sync(dir, user, Selection{}, t.Logf); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "example-feed", "SKILL.md")); string(b) != "plugin\n" {
			t.Fatalf("sync %d: the plugin's skill is not there (%q)", i, b)
		}
	}
	// A stamp that says it was copied while it is not: copied again.
	_ = os.RemoveAll(filepath.Join(dir, "example-feed"))
	if err := Sync(dir, user, Selection{}, t.Logf); err != nil {
		t.Fatal(err)
	}
	if !isCopy(filepath.Join(dir, "example-feed")) {
		t.Fatal("a stamp without its directory stopped the copy")
	}
}

// PluginIssues says what WritePlugin would change, without writing; WritePlugin repairs a plugin whose stamp is
// current but whose files are not.
func TestPluginIssuesAndRepair(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent-plugin")
	if got := PluginIssues(dir); len(got) != 1 || got[0] != "missing" {
		t.Fatalf("no plugin: %v", got)
	}
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	if got := PluginIssues(dir); len(got) != 0 {
		t.Fatalf("fresh plugin: %v", got)
	}
	script := filepath.Join(dir, "skills", "persona-office", "scripts", "office.sh")
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := PluginIssues(dir)
	if len(got) != 2 || got[0] != "differs .claude-plugin/plugin.json" || got[1] != "missing skills/persona-office/scripts/office.sh" {
		t.Fatalf("damaged plugin: %v", got)
	}
	if err := WritePlugin(dir); err != nil { // the stamp is still current: it used to be skipped
		t.Fatal(err)
	}
	if got := PluginIssues(dir); len(got) != 0 {
		t.Fatalf("after WritePlugin: %v", got)
	}
}
