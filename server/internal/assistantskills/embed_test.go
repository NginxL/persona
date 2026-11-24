package assistantskills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func skill(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

func quiet(string, ...any) {}

// The user's skills are linked in, follow their space, and a skill gone from it is unlinked; a directory the user put
// in the assistant's skills themselves is never touched.
func TestSyncLinksTheUsersSkills(t *testing.T) {
	user, dir := t.TempDir(), filepath.Join(t.TempDir(), ".claude", "skills")
	skill(t, user, "alpha", "first")
	skill(t, user, "beta", "second")
	if err := os.MkdirAll(filepath.Join(dir, "beta"), 0o755); err != nil { // the user's own, in place
		t.Fatal(err)
	}
	if err := Sync(dir, user, Selection{}, quiet); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "alpha")); err != nil || target != filepath.Join(user, "alpha") {
		t.Fatalf("alpha: %q %v", target, err)
	}
	if !hasSkill(dir, "alpha") {
		t.Fatal("alpha is not usable through the link")
	}
	if st, err := os.Lstat(filepath.Join(dir, "beta")); err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("beta, a directory of the user's own, was replaced: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(user, "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := Sync(dir, user, Selection{}, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "alpha")); !os.IsNotExist(err) {
		t.Fatalf("a link to a skill gone from the user's space stayed: %v", err)
	}
}

// What an older personad copied in (a skill, with its stamp) moves to the user's space once and is linked back.
func TestSyncMovesTheOldCopy(t *testing.T) {
	user, dir := filepath.Join(t.TempDir(), "skills"), t.TempDir()
	skill(t, dir, "lark-gateway", "Feishu through a Lark gateway")
	if err := os.WriteFile(filepath.Join(dir, "lark-gateway", "scripts", "ol.sh"), []byte("#!/bin/bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyStamp), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var said []string
	if err := Sync(dir, user, Selection{}, func(f string, a ...any) { said = append(said, f) }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(user, "lark-gateway", "scripts", "ol.sh")); err != nil {
		t.Fatalf("not moved: %v", err)
	}
	if st, err := os.Lstat(filepath.Join(dir, "lark-gateway")); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("not linked back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lark-gateway", "scripts", "ol.sh")); err != nil {
		t.Fatalf("ol.sh is not reachable where the node looks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, legacyStamp)); !os.IsNotExist(err) {
		t.Fatal("the old stamp stayed: the move would be tried again")
	}
	if len(said) == 0 {
		t.Fatal("the move is not logged")
	}
}

// The user already has the skill in their space: the old copy goes, theirs is linked.
func TestSyncOldCopyGivesWayToTheUsers(t *testing.T) {
	user, dir := t.TempDir(), t.TempDir()
	skill(t, user, "lark-gateway", "mine")
	skill(t, dir, "lark-gateway", "old copy")
	if err := os.WriteFile(filepath.Join(dir, legacyStamp), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Sync(dir, user, Selection{}, quiet); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(filepath.Join(dir, "lark-gateway")); target != filepath.Join(user, "lark-gateway") {
		t.Fatalf("lark-gateway points at %q", target)
	}
}

// An extension's skill copied in next to the old stamp stays the extension's: only what the old personad left moves.
func TestSyncOldCopyLeavesExtensionSkills(t *testing.T) {
	user, dir := t.TempDir(), t.TempDir()
	skill(t, dir, "from-extension", "an extension's")
	if err := os.WriteFile(filepath.Join(dir, "from-extension", pluginStamp), []byte("ext abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyStamp), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	migrate(dir, user, quiet)
	if _, err := os.Stat(filepath.Join(user, "from-extension")); !os.IsNotExist(err) {
		t.Fatal("an extension's skill moved into the user's space")
	}
}

// Without the stamp nothing moves: a directory the user put there is theirs.
func TestNoStampNoMove(t *testing.T) {
	user, dir := t.TempDir(), t.TempDir()
	skill(t, dir, "lark-gateway", "placed by hand")
	if err := Sync(dir, user, Selection{}, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(user, "lark-gateway")); !os.IsNotExist(err) {
		t.Fatal("moved a directory personad never put there")
	}
}

// A built-in skill is one Persona may redistribute: anything else goes in the user's space.
func TestBuiltinsAreRedistributable(t *testing.T) {
	for _, n := range builtinNames() {
		if Redistributable[n] == "" {
			t.Errorf("built-in skill %s is not registered as redistributable (embed.go)", n)
		}
	}
}

func TestListAndDescriptions(t *testing.T) {
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir()) // not this machine's plugins' skills
	user := t.TempDir()
	skill(t, user, "alpha", `"quoted one"`)
	if err := os.MkdirAll(filepath.Join(user, "folded"), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: folded\ndescription: >\n  first line\n  second line\nlicense: MIT\n---\n"
	if err := os.WriteFile(filepath.Join(user, "folded", "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(user, "no-skill-md"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := List(user, Selection{})
	// The user's, then the built-in ones that need no connector (persona-office).
	if len(got) != 3 || got[0] != (Skill{"alpha", "quoted one", "user"}) || got[1] != (Skill{"folded", "first line second line", "user"}) ||
		got[2].Name != "persona-office" || got[2].Source != "builtin" || !strings.HasPrefix(got[2].Description, "生成 Word") {
		t.Fatalf("%+v", got)
	}
}

// A built-in skill that needs a connector (Requires) is in while it is signed in on the hub and out once it is not; the
// user's own skill of the name wins.
func TestConnectorSkillFollowsItsConnector(t *testing.T) {
	old := Requires
	Requires = map[string]string{"persona-office": "example"}
	t.Cleanup(func() { Requires = old })
	user, dir := t.TempDir(), t.TempDir()
	if err := Sync(dir, user, Selection{}, quiet); err != nil {
		t.Fatal(err)
	}
	if hasSkill(dir, "persona-office") || slices.ContainsFunc(List(user, Selection{}), func(s Skill) bool { return s.Name == "persona-office" }) {
		t.Fatal("the skill is in with its connector not signed in")
	}
	if err := Sync(dir, user, Selection{Connectors: []string{"example"}}, quiet); err != nil {
		t.Fatal(err)
	}
	if !hasSkill(dir, "persona-office") {
		t.Fatal("its connector signed in, no skill")
	}
	listed := List(user, Selection{Connectors: []string{"example"}})
	if i := slices.IndexFunc(listed, func(s Skill) bool { return s.Name == "persona-office" }); i < 0 || listed[i].Source != "builtin" {
		t.Fatalf("listed %+v", listed)
	}
	if err := Sync(dir, user, Selection{}, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "persona-office")); !os.IsNotExist(err) {
		t.Fatalf("signed out, the skill stayed: %v", err)
	}
	skill(t, user, "persona-office", "mine")
	if err := Sync(dir, user, Selection{Connectors: []string{"example"}}, quiet); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(filepath.Join(dir, "persona-office")); target != filepath.Join(user, "persona-office") {
		t.Fatalf("the user's skill gave way to the built-in one: %q", target)
	}
}

// Telegram's skill was built in until the connector became an extension (#1704): the copy an older personad put in
// goes, and the extension's skill of the same name comes in, with Telegram still signed in.
func TestABuiltinSkillNoLongerCarriedGivesWayToAPlugins(t *testing.T) {
	user, dir, plugins := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", plugins)
	skill(t, dir, "telegram", "built in")
	if err := os.WriteFile(filepath.Join(dir, builtinStamp), []byte("0123456789ab persona-office,telegram\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	md := []byte("---\nname: telegram\ndescription: the extension's\n---\n")
	if _, err := ApplyPlugins(plugins, []PluginSkill{{Name: "telegram", Plugin: "telegram", Digest: "d1", Files: map[string][]byte{"SKILL.md": md}}}); err != nil {
		t.Fatal(err)
	}
	if err := Sync(dir, user, Selection{Connectors: []string{"telegram"}}, quiet); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "telegram", "SKILL.md")); err != nil || !strings.Contains(string(b), "the extension's") {
		t.Fatalf("telegram: %q %v", b, err)
	}
	if !hasSkill(dir, "persona-office") {
		t.Fatal("the built-in that is still carried went too")
	}
}

// The agents Persona starts get the ForAgents skills as a Claude Code plugin: a manifest, the skills with their
// scripts runnable; written again only when what the binary carries changed.
func TestWritePlugin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent-plugin")
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Name, Version string }
	b, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil || json.Unmarshal(b, &manifest) != nil || manifest.Name != "persona" || manifest.Version != builtinVersion() {
		t.Fatalf("manifest %s %v", b, err)
	}
	for _, name := range ForAgents {
		if _, err := os.Stat(filepath.Join(dir, "skills", name, "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "skills", "persona-office", "scripts", "office.sh")); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("the script is not runnable: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "skills")); len(entries) != len(ForAgents) {
		t.Fatalf("the agents' plugin has %d skills, ForAgents %d: an assistant-only one is in", len(entries), len(ForAgents))
	}
	// The same binary: nothing written; another one: written again.
	marker := filepath.Join(dir, "skills", "persona-office", "marker")
	os.WriteFile(marker, nil, 0o644)
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("written again with nothing changed")
	}
	os.WriteFile(filepath.Join(dir, builtinStamp), []byte("older\n"), 0o644)
	if err := WritePlugin(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("not written again for another binary")
	}
	if _, err := os.Stat(dir + ".old"); !os.IsNotExist(err) {
		t.Fatal("the old copy stayed")
	}
}

// The skill's script runs `personad office …` with the node's personad (PERSONA_BIN), or says there is none.
func TestOfficeScript(t *testing.T) {
	dir := t.TempDir()
	if err := copyBuiltin("persona-office", filepath.Join(dir, "skill")); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "personad")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755)
	script := filepath.Join(dir, "skill", "scripts", "office.sh")
	cmd := exec.Command("sh", script, "docx", "周报.md", "-o", "周报.docx")
	cmd.Env = []string{"PERSONA_BIN=" + bin, "HOME=" + dir, "PATH=/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "office docx 周报.md -o 周报.docx" {
		t.Fatalf("%v %s", err, out)
	}
	cmd = exec.Command("sh", script, "check", "x.docx")
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(string(out), "no personad") {
		t.Fatalf("no personad: %v %s", err, out)
	}
}

// hasSkill says whether the assistant directory's skills (dir) include name.
func hasSkill(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name, "SKILL.md"))
	return err == nil
}

func TestWorkGuideLeavesExistingSkillInPlace(t *testing.T) {
	dir, user := t.TempDir(), t.TempDir()
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	skill(t, dir, "persona-work", "Personal workflow")
	for _, on := range []bool{false, true, false} {
		if err := Sync(dir, user, Selection{WorkGuide: on}, quiet); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "persona-work", "SKILL.md"))
		if err != nil || !strings.Contains(string(b), "Personal workflow") || WorkGuideInstalled(dir) {
			t.Fatalf("changed existing skill: %s, %v", b, err)
		}
	}
}

func TestWorkGuideCatalogBudget(t *testing.T) {
	dir, user := t.TempDir(), t.TempDir()
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	if err := Sync(dir, user, Selection{WorkGuide: true}, t.Logf); err != nil {
		t.Fatal(err)
	}
	entries, err := LocalCatalog([]Root{{Path: dir, Source: "project"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name != workGuideName {
			continue
		}
		// Use a stable fixture path for a reproducible UTF-8 catalog measurement.
		e.Path = "/example/assistant/.claude/skills/persona-work/SKILL.md"
		b, err := json.Marshal(e)
		if err != nil || len(b) > 512 {
			t.Fatalf("catalog entry %d bytes: %v", len(b), err)
		}
		t.Logf("normalized local native catalog entry %d bytes", len(b))
		return
	}
	t.Fatal("missing guide")
}

func TestWorkGuideRoutesToNativeReferences(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	if err := Sync(dir, t.TempDir(), Selection{WorkGuide: true}, t.Logf); err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, workGuideName, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	entry := read("SKILL.md")
	catalog := description(entry)
	for _, area := range []struct{ cue, reference, operation string }{
		{"to-do", "record", "todo_create"},
		{"delegation", "delegate", "task_plan"},
		{"review", "review", "task_review"},
		{"Agent", "agents-and-machines", "agents_list"},
		{"machine", "agents-and-machines", "machines_list"},
		{"conversation", "conversations", "conversation_tags_set"},
		{"repository", "repositories", "todo_repositories_set"},
	} {
		ref := "references/" + area.reference + ".md"
		if !strings.Contains(catalog, area.cue) || !strings.Contains(entry, "]("+ref+")") || !strings.Contains(read(ref), area.operation) {
			t.Errorf("%s must route from the catalog to %s and its %s workflow", area.cue, ref, area.operation)
		}
	}
}

func TestWorkGuidePreservesReplacementAfterInstall(t *testing.T) {
	for _, runtime := range []string{".claude", ".agents"} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/enabled=%t", runtime, enabled), func(t *testing.T) {
				cwd, user := t.TempDir(), t.TempDir()
				sync := func(on bool) {
					t.Helper()
					for _, r := range []string{".claude", ".agents"} {
						if err := Sync(filepath.Join(cwd, r, "skills"), user, Selection{WorkGuide: on}, t.Logf); err != nil {
							t.Fatal(err)
						}
					}
				}
				sync(true)
				at := filepath.Join(cwd, runtime, "skills", workGuideName)
				if err := os.RemoveAll(at); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(at, 0o700); err != nil {
					t.Fatal(err)
				}
				f := filepath.Join(at, "SKILL.md")
				if err := os.WriteFile(f, []byte("local replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
				if WorkGuideReady(cwd) {
					t.Fatal("replacement mistaken for the native guide")
				}
				sync(enabled)
				if b, e := os.ReadFile(f); e != nil || string(b) != "local replacement" || WorkGuideReady(cwd) {
					t.Fatalf("replacement lost or selected: %q %v", b, e)
				}
			})
		}
	}
}

func TestWorkGuideRestoresCodexLinkAfterOlderNode(t *testing.T) {
	cwd, user := t.TempDir(), t.TempDir()
	claude := filepath.Join(cwd, ".claude", "skills")
	codex := filepath.Join(cwd, ".agents", "skills")
	sync := func(on bool) {
		for _, dir := range []string{claude, codex} {
			if err := Sync(dir, user, Selection{WorkGuide: on}, t.Logf); err != nil {
				t.Fatal(err)
			}
		}
	}
	sync(true)
	// A node predating the guide removes its canonical directory and rewrites
	// both inventories, leaving the relative link dangling on the Codex side.
	if err := os.RemoveAll(filepath.Join(claude, workGuideName)); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{claude, codex} {
		if err := os.WriteFile(filepath.Join(dir, builtinStamp), []byte("old persona-office\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	sync(true)
	if !WorkGuideReady(cwd) {
		t.Fatal("upgraded node did not recover both managed skill roots")
	}
	sync(false)
	if _, err := os.Lstat(filepath.Join(codex, workGuideName)); !os.IsNotExist(err) {
		t.Fatal("rollback retained the recovered Codex link", err)
	}
}

func TestMachineOpsSkillIsOptInByDefault(t *testing.T) {
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	user, dir := t.TempDir(), t.TempDir()
	if err := Sync(dir, user, Selection{}, quiet); err != nil {
		t.Fatal(err)
	}
	if hasSkill(dir, MachineOpsSkillName) {
		t.Fatal("persona-machine-ops was installed by default without opt-in")
	}
	if slices.ContainsFunc(List(user, Selection{}), func(s Skill) bool { return s.Name == MachineOpsSkillName }) {
		t.Fatal("persona-machine-ops appeared in default List without opt-in")
	}
	if slices.Contains(ForAgents, MachineOpsSkillName) {
		t.Fatal("persona-machine-ops must not be injected into default ForAgents plugin")
	}
	// Explicit opt-in via Selection{MachineOps: true} installs and rolls back cleanly.
	if err := Sync(dir, user, Selection{MachineOps: true}, quiet); err != nil {
		t.Fatal(err)
	}
	if !hasSkill(dir, MachineOpsSkillName) {
		t.Fatal("persona-machine-ops missing when Selection{MachineOps: true}")
	}
	if err := Sync(dir, user, Selection{MachineOps: false}, quiet); err != nil {
		t.Fatal(err)
	}
	if hasSkill(dir, MachineOpsSkillName) {
		t.Fatal("persona-machine-ops stayed after opting out")
	}
}

func TestMachineOpsSkillCatalogAndReferences(t *testing.T) {
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	dir := t.TempDir()
	if _, err := InstallMachineOps(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := LocalCatalog([]Root{{Path: dir, Source: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Name != MachineOpsSkillName {
			continue
		}
		found = true
		e.Path = "/example/skills/persona-machine-ops/SKILL.md"
		b, err := json.Marshal(e)
		if err != nil || len(b) > 512 {
			t.Fatalf("catalog entry %d bytes (want <= 512): %v", len(b), err)
		}
	}
	if !found {
		t.Fatal("persona-machine-ops missing from LocalCatalog")
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, MachineOpsSkillName, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	entry := read("SKILL.md")
	for _, tc := range []struct{ ref, tool string }{
		{"references/storage.md", "machine_ops_storage_hotspots"},
		{"references/toolchain.md", "machine_ops_toolchain"},
		{"references/network.md", "machine_ops_network"},
		{"references/ports-and-processes.md", "machine_ops_processes"},
		{"references/services.md", "machine_ops_service_logs"},
	} {
		if !strings.Contains(entry, "]("+tc.ref+")") {
			t.Errorf("SKILL.md does not link to %s", tc.ref)
		}
		if !strings.Contains(read(tc.ref), tc.tool) {
			t.Errorf("%s does not document %s", tc.ref, tc.tool)
		}
	}
}

func TestMachineOpsScriptAndConflictProtection(t *testing.T) {
	dir := t.TempDir()
	st, err := InstallMachineOps(dir)
	if err != nil || !st.Installed || !st.Managed || st.Conflict {
		t.Fatalf("InstallMachineOps: %+v %v", st, err)
	}
	script := filepath.Join(dir, MachineOpsSkillName, "scripts", "ops.sh")
	info, err := os.Stat(script)
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("ops.sh is not executable: %v", err)
	}
	bin := filepath.Join(dir, "personad")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script, "ports", "--protocol", "tcp")
	cmd.Env = []string{"PERSONA_BIN=" + bin, "HOME=" + dir, "PATH=/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "ops ports --protocol tcp" {
		t.Fatalf("ops.sh delegation: %v %q", err, out)
	}
	cmd = exec.Command("sh", script, "storage")
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(string(out), "no personad") {
		t.Fatalf("ops.sh without personad: %v %q", err, out)
	}
	// Editing a file marks the skill conflicted; reinstall and uninstall both refuse to clobber user edits.
	if err := os.WriteFile(filepath.Join(dir, MachineOpsSkillName, "SKILL.md"), []byte("user owned"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallMachineOps(dir); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("InstallMachineOps over edited skill: %v, want ErrSkillConflict", err)
	}
	if _, err := UninstallMachineOps(dir); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("UninstallMachineOps over edited skill: %v, want ErrSkillConflict", err)
	}
	// Clean directory installs and uninstalls cleanly.
	dir2 := t.TempDir()
	if _, err := InstallMachineOps(dir2); err != nil {
		t.Fatal(err)
	}
	st2, err := UninstallMachineOps(dir2)
	if err != nil || st2.Installed || st2.Managed {
		t.Fatalf("UninstallMachineOps: %+v %v", st2, err)
	}
}
