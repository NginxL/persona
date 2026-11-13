package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSkillCatalogIsolationProfilesDeletionAndFailure(t *testing.T) {
	f := sdkTest(t)
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

func TestSkillCatalogToClaudeSDKDelivery(t *testing.T) {
	f := sdkTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	cwd, profile := t.TempDir(), t.TempDir()
	file := filepath.Join(profile, "skills", "review", "SKILL.md")
	writeFile(t, file, "---\nname: review\ndescription: Review\nallowed-tools: Bash(*)\n---\n!`must-not-execute`\n")
	in := skillCatalogRequest{Owner: "helper-a", Cwd: cwd, Launcher: "claude", Runtime: proto.AssistantRuntimeAgentSDK, ConfigDir: profile}
	catalog, err := n.skillCatalog(context.Background(), in)
	if err != nil || len(catalog.Skills) != 1 {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	text := `<persona-context kind="skill" ref="` + catalog.Skills[0].ID + `">review</persona-context>` + "\nUse this"
	_, err = n.assistantSend(withSkillOwner(context.Background(), in.Owner), "", cwd, "claude", text, nil, "default", "",
		modelExecution{Runtime: in.Runtime, ConfigDir: profile, Fingerprint: "skill-test"})
	if err != nil {
		t.Fatal(err)
	}
	sdkWait(t, "explicit instruction delivery", func() bool { return strings.Contains(f.file("stdin.jsonl"), "Instruction file:") })
	delivered := f.file("stdin.jsonl")
	if !strings.Contains(delivered, file) || !strings.Contains(delivered, catalog.Skills[0].ID) || strings.Contains(delivered, "must-not-execute") || strings.Contains(delivered, "allowed-tools: Bash") {
		t.Fatalf("not exact metadata-safe delivery: %s", delivered)
	}
}

func TestClaudeSkillCatalogDoesNotBypassCustomizationOrSkillPermissions(t *testing.T) {
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
	f := sdkTest(t)
	f.n.home = os.Getenv("HOME")
	profile, cwd := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(profile, "skills", "review", "SKILL.md"), "---\nname: review\n---\nInstructions\n")
	_, err := f.n.skillCatalog(context.Background(), skillCatalogRequest{Owner: "helper", Cwd: cwd, Launcher: "policy-claude", ConfigDir: profile})
	if err == nil {
		t.Fatal("launcher Skill deny policy was bypassed")
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
