package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantskills"
)

func TestSkillCatalogIsolationProfilesDeletionAndFailure(t *testing.T) {
	managed, _ := fakeClaudeManagedSettings(t)
	writeFile(t, managed, `{"hooks":{}}`) // an administrator's file that restricts no skill (#9862)
	f := cliTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	cwd, profile := t.TempDir(), t.TempDir()
	file := filepath.Join(profile, "skills", "review", "SKILL.md")
	writeFile(t, file, "---\nname: review\ndescription: Inspect boundaries\n---\nPrivate instructions\n")
	request := skillCatalogRequest{Owner: "helper-a", Machine: "computer-a", Cwd: cwd, Launcher: "claude", ConfigDir: profile}
	ctx := context.Background()
	catalog, err := n.skillCatalog(ctx, request)
	if err != nil || len(catalog.Skills) != 1 || catalog.Complete {
		t.Fatalf("bounded effective profile: %+v %v", catalog, err)
	}
	text := `<persona-context kind="skill" title="review" ref="` + catalog.Skills[0].ID + `">user</persona-context>` + "\nReview this"
	for _, field := range []string{"assistant", "machine", "profile"} {
		other := request
		switch field {
		case "assistant":
			other.Owner = "helper-b"
		case "machine":
			other.Machine = "computer-b"
		case "profile":
			other.ConfigDir = t.TempDir()
		}
		changed, err := n.skillCatalog(ctx, other)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := assistantskills.Resolve(text, changed.Skills); err == nil {
			t.Fatalf("selection crossed %s boundary", field)
		}
	}
	writeFile(t, filepath.Join(profile, "settings.json"), `{"skillOverrides":{"review":"off"}}`)
	disabled, err := n.skillCatalog(ctx, request)
	if err != nil || len(disabled.Skills) != 0 {
		t.Fatalf("disabled skills visible: %+v %v", disabled, err)
	}
	writeFile(t, filepath.Join(profile, "settings.json"), `{}`)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	missing, err := n.skillCatalog(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assistantskills.Resolve(text, missing.Skills); err == nil {
		t.Fatal("removed skill silently resolved")
	}
	writeFile(t, filepath.Join(profile, "settings.json"), `{broken`)
	if _, err := n.skillCatalog(ctx, request); err == nil {
		t.Fatal("broken settings reported an authoritative inventory")
	}
	if selected, err := n.selectedSkills(ctx, "", cwd, "claude", "Literal @review me@example.com", modelExecution{}); err != nil || len(selected) != 0 {
		t.Fatalf("ordinary chat depends on catalog: %v", err)
	}
}

func TestSkillCatalogToClaudeDelivery(t *testing.T) {
	fakeClaudeManagedSettings(t)
	f := cliTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	cwd, profile := t.TempDir(), t.TempDir()
	file := filepath.Join(profile, "skills", "review", "SKILL.md")
	writeFile(t, file, "---\nname: review\ndescription: Review\nallowed-tools: Bash(*)\n---\n!`must-not-execute`\n")
	in := skillCatalogRequest{Owner: "helper-a", Cwd: cwd, Launcher: "claude", ConfigDir: profile}
	catalog, err := n.skillCatalog(context.Background(), in)
	if err != nil || len(catalog.Skills) != 1 {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	text := `<persona-context kind="skill" ref="` + catalog.Skills[0].ID + `">review</persona-context>` + "\nUse this"
	_, err = n.assistantSend(withSkillOwner(context.Background(), in.Owner), "", cwd, "claude", text, nil, "default", "",
		modelExecution{ConfigDir: profile, Fingerprint: "skill-test"})
	if err != nil {
		t.Fatal(err)
	}
	cliWait(t, "explicit instruction delivery", func() bool { return strings.Contains(f.file("stdin.jsonl"), "Instruction file:") })
	delivered := f.file("stdin.jsonl")
	if !strings.Contains(delivered, resolved(t, file)) || !strings.Contains(delivered, catalog.Skills[0].ID) || strings.Contains(delivered, "must-not-execute") || strings.Contains(delivered, "allowed-tools: Bash") {
		t.Fatalf("not exact metadata-safe delivery: %s", delivered)
	}
}

func TestClaudeSkillCatalogDoesNotBypassCustomizationOrSkillPermissions(t *testing.T) {
	fakeClaudeManagedSettings(t)
	profile, cwd := t.TempDir(), t.TempDir()
	entries := []assistantskills.Entry{{Name: "review", Path: "/fixture/SKILL.md"}}
	for _, settings := range []string{
		`{"strictPluginOnlyCustomization":true}`,
		`{"strictPluginOnlyCustomization":["skills"]}`,
		`{"permissions":{"deny":["Skill(review)"]}}`,
		`{"permissions":{"ask":["Skill"]}}`,
	} {
		writeFile(t, filepath.Join(profile, "settings.json"), settings)
		if _, err := visibleClaudeSkills(entries, profile, cwd); err == nil {
			t.Fatalf("exact-file adapter must not bypass policy: %s", settings)
		}
	}
	writeFile(t, filepath.Join(profile, "settings.json"), `{}`)
	writeFile(t, filepath.Join(profile, "remote-settings.json"), `{"strictPluginOnlyCustomization":["skills"]}`)
	if _, err := visibleClaudeSkills(entries, profile, cwd); err == nil {
		t.Fatal("managed policy cache must not be bypassed by exact-file instructions")
	}
}

func TestClaudeSkillCatalogRespectsLauncherSettings(t *testing.T) {
	probedLaunchers(t, map[string]string{"policy-claude": "claude"})
	withLauncherSettings(t, map[string]string{"policy-claude": `{"permissions":{"deny":["Skill(review)"]}}`})
	fakeClaudeManagedSettings(t)
	f := cliTest(t)
	f.n.home = os.Getenv("HOME")
	profile, cwd := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(profile, "skills", "review", "SKILL.md"), "---\nname: review\n---\nInstructions\n")
	_, err := f.n.skillCatalog(context.Background(), skillCatalogRequest{Owner: "helper", Cwd: cwd, Launcher: "policy-claude", ConfigDir: profile})
	if err == nil {
		t.Fatal("launcher Skill deny policy was bypassed")
	}
}

// fakeClaudeManagedSettings points every system's managed settings at a new directory, so a test never reads this
// computer's, and returns that directory's managed-settings.json and managed-settings.d.
func fakeClaudeManagedSettings(t *testing.T) (file, dir string) {
	t.Helper()
	mac, macDirs, linux, linuxDirs := claudeManagedSettings, claudeManagedSettingsDirs, claudeLinuxManagedSettings, claudeLinuxManagedSettingsDirs
	t.Cleanup(func() {
		claudeManagedSettings, claudeManagedSettingsDirs, claudeLinuxManagedSettings, claudeLinuxManagedSettingsDirs = mac, macDirs, linux, linuxDirs
	})
	root := t.TempDir()
	file, dir = filepath.Join(root, "managed-settings.json"), filepath.Join(root, "managed-settings.d")
	claudeManagedSettings, claudeManagedSettingsDirs = []string{file}, []string{dir}
	claudeLinuxManagedSettings, claudeLinuxManagedSettingsDirs = nil, nil
	t.Setenv("ProgramFiles", "")
	return file, dir
}

// An administrator's managed-settings.json and managed-settings.d go through the same checks as the other layers, so
// one that only sets hooks leaves every skill listed (#9862).
func TestClaudeSkillCatalogFollowsManagedSettings(t *testing.T) {
	file, dir := fakeClaudeManagedSettings(t)
	profile, cwd := t.TempDir(), t.TempDir()
	visible := func() ([]string, error) {
		entries := []assistantskills.Entry{{Name: "review", Path: "/fixture/review/SKILL.md"}, {Name: "deploy", Path: "/fixture/deploy/SKILL.md"}}
		// The launcher's settings, a lower layer than the administrator's, turn review on.
		out, err := visibleClaudeSkills(entries, profile, cwd, map[string]any{"skillOverrides": map[string]any{"review": "on"}})
		names := []string{}
		for _, e := range out {
			names = append(names, e.Name)
		}
		return names, err
	}
	reset := func() {
		for _, path := range []string{file, dir, filepath.Join(profile, "remote-settings.json")} {
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
		}
	}
	listed := func(what string, want ...string) {
		t.Helper()
		if names, err := visible(); err != nil || strings.Join(names, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: got %v %v, want %v", what, names, err, want)
		}
	}

	// A file that is not there holds no policy: readSettingsFile gives it as an empty map, not as the nil of a file that
	// holds null (review of #9870).
	if m, err := readSettingsFile(file); err != nil || m == nil || len(m) != 0 {
		t.Fatalf("a missing managed-settings.json: %v %v, want an empty map", m, err)
	}
	listed("no managed settings", "review", "deploy")
	writeFile(t, file, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/opt/example/hook"}]}]}}`)
	writeFile(t, filepath.Join(dir, "10-hooks.json"), `{"hooks":{}}`)
	listed("hooks only", "review", "deploy")

	deny := `{"permissions":{"deny":["Skill(review)"]}}`
	for _, c := range []struct{ what, path, body string }{
		{"deny rule in managed-settings.json", file, deny},
		{"deny rule in a drop-in", filepath.Join(dir, "20-skills.json"), `{"permissions":{"deny":["Skill"]}}`},
		{"ask rule in a drop-in", filepath.Join(dir, "20-skills.json"), `{"permissions":{"ask":["Skill(deploy *)"]}}`},
		{"strict plugin-only list", file, `{"strictPluginOnlyCustomization":["skills"]}`},
		{"strict plugin-only true", filepath.Join(dir, "20-strict.json"), `{"strictPluginOnlyCustomization":true}`},
		{"strict plugin-only it cannot read", file, `{"strictPluginOnlyCustomization":"skills"}`},
		{"policy helper", file, `{"policyHelper":{"path":"/opt/example/policy-helper"}}`},
		{"malformed managed-settings.json", file, `{broken`},
		{"malformed drop-in", filepath.Join(dir, "20-broken.json"), `{"permissions":`},
		{"null", file, `null`},
		{"not an object", filepath.Join(dir, "20-list.json"), `[]`},
		{"drop-in directory that cannot be listed", dir, `{}`},
		{"server-policy cache", filepath.Join(profile, "remote-settings.json"), `{}`},
	} {
		reset()
		writeFile(t, c.path, c.body)
		if names, err := visible(); err == nil {
			t.Fatalf("%s: listed %v", c.what, names)
		}
	}
	reset()
	if err := os.MkdirAll(file, 0o700); err != nil { // there, but cannot be read as a file
		t.Fatal(err)
	}
	if names, err := visible(); err == nil {
		t.Fatalf("unreadable managed-settings.json: listed %v", names)
	}
	reset()
	writeFile(t, filepath.Join(dir, ".20-skills.json"), deny) // Claude Code skips hidden files
	writeFile(t, filepath.Join(dir, "notes.txt"), `{broken`)  // and files that do not end in .json
	listed("hidden and non-JSON drop-ins", "review", "deploy")

	reset()
	writeFile(t, file, `{"skillOverrides":{"review":"off"}}`)
	for _, lower := range []string{filepath.Join(profile, "settings.json"), filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json")} {
		writeFile(t, lower, `{"skillOverrides":{"review":"on"}}`)
	}
	listed("managed off over every lower on", "deploy")
	// Drop-ins merge after managed-settings.json, alphabetically; an entry Claude Code cannot read changes nothing.
	writeFile(t, filepath.Join(dir, "20-on.json"), `{"skillOverrides":{"review":"on"}}`)
	writeFile(t, filepath.Join(dir, "10-off.json"), `{"skillOverrides":{"review":"off","deploy":"off"}}`)
	writeFile(t, filepath.Join(dir, "30-unreadable.json"), `{"skillOverrides":{"deploy":true}}`)
	listed("drop-ins in order", "review")

	// Every system's place is read.
	linux, programFiles := t.TempDir(), t.TempDir()
	claudeLinuxManagedSettings = []string{filepath.Join(linux, "managed-settings.json")}
	claudeLinuxManagedSettingsDirs = []string{filepath.Join(linux, "managed-settings.d")}
	t.Setenv("ProgramFiles", programFiles)
	for _, path := range []string{filepath.Join(linux, "managed-settings.json"), filepath.Join(linux, "managed-settings.d", "x.json"),
		filepath.Join(programFiles, "ClaudeCode", "managed-settings.json"), filepath.Join(programFiles, "ClaudeCode", "managed-settings.d", "x.json")} {
		writeFile(t, path, deny)
		if names, err := visible(); err == nil {
			t.Fatalf("%s not read: listed %v", path, names)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodexSkillCatalogToTypedTurnAndSteer(t *testing.T) {
	probedLaunchers(t, map[string]string{"cx_test": "codex"})
	n := codexTestNode(t)
	withFakeHub(t, n)
	// The catalog reports resolved paths; on macOS t.TempDir() is under a symlink.
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, c, f := newFakeCodex(t, n, cwd)
	p.id, c.threadID = testThread, testThread
	n.procs[testThread] = p
	file := filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md")
	writeFile(t, file, "---\nname: review\ndescription: Review\n---\nInstructions\n")
	f.results["skills/list"] = map[string]any{"data": []any{map[string]any{"cwd": cwd, "skills": []any{
		map[string]any{"name": "review", "description": "Review", "path": file, "scope": "repo", "enabled": true},
		map[string]any{"name": "hidden", "path": "/hidden/SKILL.md", "enabled": false},
	}, "errors": []any{}}}}
	request := skillCatalogRequest{Owner: "helper-a", Machine: n.machineID, SessionID: testThread, Cwd: cwd, Launcher: p.launcher}
	catalog, err := n.skillCatalog(context.Background(), request)
	if err != nil || !catalog.Complete || len(catalog.Skills) != 1 {
		t.Fatalf("native catalog: %+v %v", catalog, err)
	}
	text := `<persona-context kind="skill" ref="` + catalog.Skills[0].ID + `">review</persona-context>` + "\nReview"
	entries, err := n.selectedSkills(withSkillOwner(context.Background(), request.Owner), testThread, cwd, p.launcher, text, modelExecution{})
	if err != nil || len(entries) != 1 {
		t.Fatalf("catalog-to-send resolution: %+v %v", entries, err)
	}
	if _, err := n.selectedSkills(withSkillOwner(context.Background(), "helper-b"), testThread, cwd, p.launcher, text, modelExecution{}); err == nil {
		t.Fatal("Codex selection crossed assistant ownership")
	}
	f.results["turn/start"] = map[string]any{"turn": map[string]any{"id": "turn-1"}}
	if err := c.deliver(context.Background(), "", "Review", nil, entries...); err != nil {
		t.Fatal(err)
	}
	if err := c.deliver(context.Background(), "", "And this", nil, entries...); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"turn/start", "turn/steer"} {
		var sent struct{ Input []map[string]any }
		_ = json.Unmarshal(f.sent(method)[0].Params, &sent)
		if sent.Input[0]["type"] != "skill" || sent.Input[0]["name"] != "review" || sent.Input[0]["path"] != file {
			t.Fatalf("%s lost typed invocation: %+v", method, sent)
		}
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := n.selectedSkills(withSkillOwner(context.Background(), request.Owner), testThread, cwd, p.launcher, text, modelExecution{}); err == nil {
		t.Fatal("Codex selected a deleted instruction file")
	}
}
