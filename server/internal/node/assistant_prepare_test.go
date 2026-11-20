package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantskills"
)

func prepareAssistantForTest(t *testing.T, n *Node, dir string, connectors []string, clis map[string]string) string {
	t.Helper()
	params, err := json.Marshal(map[string]any{"dir": dir, "claude_md": "# Example", "connectors": connectors, "clis": clis})
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.handle(context.Background(), "assistant.prepare", params)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["cwd"].(string)
}

func TestAssistantPreparePerDirectory(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	for _, custom := range []bool{false, true} {
		name := "default data directory"
		if custom {
			name = "custom data directory"
		}
		t.Run(name, func(t *testing.T) {
			n, _ := testNode(t)
			n.dir, n.bin, n.logger = filepath.Join(n.home, ".persona"), "/opt/example/personad", log.New(io.Discard, "", 0)
			if custom {
				n.dir = t.TempDir()
			}
			if err := os.MkdirAll(n.dir, 0o700); err != nil { // a node's state directory is there before anything is prepared in it
				t.Fatal(err)
			}
			const firstDir, secondDir = ".persona/assistant", ".persona/managers/second/assistant"
			first := prepareAssistantForTest(t, n, firstDir, []string{"example"}, map[string]string{"shared": "example"})
			second := prepareAssistantForTest(t, n, secondDir, []string{"other"}, map[string]string{"shared": "other"})
			if n.assistantBin(first) != filepath.Join(n.dir, "assistant-bin") {
				t.Fatal("the default assistant's command directory changed")
			}
			if n.assistantBin(first) == n.assistantBin(second) {
				t.Fatal("the assistants share a command directory")
			}
			check := func(cwd, connector string) {
				t.Helper()
				if got := n.signedInConnectors(cwd); !slices.Equal(got, []string{connector}) {
					t.Fatalf("connectors for %s: %v", cwd, got)
				}
				if got := n.connectorCLIs(cwd); len(got) != 1 || got["shared"] != connector {
					t.Fatalf("command lines for %s: %v", cwd, got)
				}
				b, err := os.ReadFile(filepath.Join(n.assistantBin(cwd), "shared"))
				if err != nil || !strings.Contains(string(b), " tool --cli "+connector+" ") {
					t.Fatalf("wrapper for %s: %q, %v", cwd, b, err)
				}
			}
			// A later sync and process launch must still use the first directory's prepare.
			n.syncAssistantSkills(first)
			for _, cwd := range []string{first, second} {
				env, release := n.assistantLarkEnv(cwd, "")
				release()
				if !slices.Contains(env, "PERSONA_ASSISTANT_BIN="+n.assistantBin(cwd)) ||
					!slices.Contains(env, "PATH="+n.assistantBin(cwd)+string(os.PathListSeparator)+os.Getenv("PATH")) {
					t.Fatalf("the process does not use its own command directory: %v", env)
				}
			}
			check(first, "example")
			check(second, "other")
			// Callers receive snapshots, not the map's mutable slices and maps.
			n.signedInConnectors(first)[0] = "changed"
			n.connectorCLIs(first)["shared"] = "changed"
			check(first, "example")
			prepareAssistantForTest(t, n, firstDir, nil, nil)
			if n.mayCall(first, "example_list") || len(n.connectorCLIs(first)) != 0 {
				t.Fatal("signed-out connectors remain available")
			}
			if _, err := os.Stat(filepath.Join(n.assistantBin(first), "shared")); !os.IsNotExist(err) {
				t.Fatalf("signed-out command remains: %v", err)
			}
			check(second, "other")
		})
	}
}

func TestAssistantPrepareSymlinkDirectory(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = filepath.Join(n.home, "state"), "/opt/example/personad", log.New(io.Discard, "", 0)
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(n.dir, filepath.Join(n.home, ".persona")); err != nil {
		t.Fatal(err)
	}
	cwd := prepareAssistantForTest(t, n, ".persona/assistant", []string{"example"}, map[string]string{"ex": "example"})
	real := filepath.Join(n.dir, "assistant")
	if !n.mayCall(real, "example_list") || n.connectorCLIs(real)["ex"] != "example" || n.assistantBin(real) != n.assistantBin(cwd) {
		t.Fatal("the resolved session directory lost its prepare state or command path")
	}
	prepareAssistantForTest(t, n, ".persona/assistant", nil, nil)
	if n.mayCall(cwd, "example_list") || len(n.connectorCLIs(cwd)) != 0 {
		t.Fatal("preparing through the alias did not replace the same directory's state")
	}
}

func TestAssistantPrepareScopesSkillConsumers(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	old := assistantskills.Requires
	assistantskills.Requires = map[string]string{"persona-office": "example"}
	t.Cleanup(func() { assistantskills.Requires = old })
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	first := prepareAssistantForTest(t, n, ".persona/assistant", []string{"example"}, nil)
	second := prepareAssistantForTest(t, n, ".persona/managers/second/assistant", nil, nil)
	for _, cwd := range []string{first, second} {
		checkSkill := func(root string) {
			t.Helper()
			_, err := os.Stat(filepath.Join(cwd, root, "skills", "persona-office", "SKILL.md"))
			if cwd == first && err != nil || cwd == second && !os.IsNotExist(err) {
				t.Fatalf("skill presence for %s/%s: %v", cwd, root, err)
			}
		}
		n.assistantRuntimeSkills(cwd)
		checkSkill(".agents")
		n.syncAssistantSkills(cwd)
		checkSkill(".agents")
		checkSkill(".claude")
		_, release := n.assistantLarkEnv(cwd, "")
		release()
		checkSkill(".claude")
	}
	for _, cwd := range []string{"", first, second, filepath.Join(n.dir, "unprepared")} {
		params, _ := json.Marshal(map[string]any{"cwd": cwd})
		out, err := n.handle(context.Background(), "skills.list", params)
		if err != nil {
			t.Fatal(err)
		}
		skills := out.(map[string]any)["skills"].([]assistantskills.Skill)
		hasOffice := slices.ContainsFunc(skills, func(s assistantskills.Skill) bool { return s.Name == "persona-office" })
		if hasOffice != (cwd == "" || cwd == first) {
			t.Fatalf("skills.list for %q: %v", cwd, skills)
		}
	}
}

func TestSkillsListPreparedDirectoryFallback(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	old := assistantskills.Requires
	assistantskills.Requires = map[string]string{"persona-office": "example"}
	t.Cleanup(func() { assistantskills.Requires = old })
	const defaultDir = ".persona/assistant"
	const otherDir = ".persona/managers/other/assistant"
	const thirdDir = ".persona/managers/third/assistant"
	for _, tc := range []struct {
		name     string
		prepared map[string]bool // directory → example connector signed in
		cwd      string
		want     bool
	}{
		{name: "nothing prepared"},
		{name: "only non-default prepared", prepared: map[string]bool{otherDir: true}, want: true},
		{name: "only non-default signed out", prepared: map[string]bool{otherDir: false}},
		{name: "only default prepared", prepared: map[string]bool{defaultDir: true}, want: true},
		{name: "default preferred", prepared: map[string]bool{defaultDir: true, otherDir: false}, want: true},
		{name: "prepared empty default preferred", prepared: map[string]bool{defaultDir: false, otherDir: true}},
		{name: "multiple non-default directories", prepared: map[string]bool{otherDir: true, thirdDir: true}},
		{name: "explicit non-default", prepared: map[string]bool{defaultDir: false, otherDir: true}, cwd: otherDir, want: true},
		{name: "explicit unprepared default", prepared: map[string]bool{otherDir: true}, cwd: defaultDir},
		{name: "explicit unprepared directory", prepared: map[string]bool{otherDir: true}, cwd: thirdDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := testNode(t)
			n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
			for dir, signedIn := range tc.prepared {
				var connectors []string
				if signedIn {
					connectors = []string{"example"}
				}
				prepareAssistantForTest(t, n, dir, connectors, nil)
			}
			params := map[string]any{}
			if tc.cwd != "" {
				params["cwd"] = n.assistantDirOf(tc.cwd)
			}
			raw, _ := json.Marshal(params)
			out, err := n.handle(context.Background(), "skills.list", raw)
			if err != nil {
				t.Fatal(err)
			}
			skills := out.(map[string]any)["skills"].([]assistantskills.Skill)
			hasOffice := slices.ContainsFunc(skills, func(s assistantskills.Skill) bool { return s.Name == "persona-office" })
			if hasOffice != tc.want {
				t.Fatalf("skills.list: %v, want connector skill present: %v", skills, tc.want)
			}
		})
	}
}

func TestWorkGuidePrepareAndRollback(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	other := prepareAssistantForTest(t, n, ".persona/assistant", nil, nil)
	for _, guide := range []string{"", "short instructions", ""} {
		params, _ := json.Marshal(map[string]any{"dir": ".persona/managers/second/assistant", "claude_md": "full instructions", "work_guide_md": guide})
		out, err := n.handle(t.Context(), "assistant.prepare", params)
		if err != nil {
			t.Fatal(err)
		}
		cwd := out.(map[string]any)["cwd"].(string)
		n.assistantRuntimeSkills(cwd)
		for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
			want := "full instructions"
			if guide != "" {
				want = guide
			}
			got, err := os.ReadFile(filepath.Join(cwd, name))
			if err != nil || string(got) != want {
				t.Fatalf("%s: %q, %v", name, got, err)
			}
		}
		claude := filepath.Join(cwd, ".claude", "skills", "persona-work")
		codex := filepath.Join(cwd, ".agents", "skills", "persona-work")
		if guide != "" {
			target, err := os.Readlink(codex)
			if err != nil || filepath.Clean(filepath.Join(filepath.Dir(codex), target)) != claude {
				t.Fatalf("expected a link to the canonical guide, got %q, %v", target, err)
			}
		} else if _, err := os.Lstat(codex); !os.IsNotExist(err) {
			t.Fatalf("guide link after rollback: %v", err)
		}
		for _, root := range []string{".claude", ".agents"} {
			for _, dir := range []string{cwd, other} {
				path := filepath.Join(dir, root, "skills", "persona-work", "references", "delegate.md")
				_, err := os.Stat(path)
				if guide != "" && dir == cwd {
					if err != nil {
						t.Fatal(err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("unexpected guide at %s: %v", path, err)
				}
			}
		}
	}
}

// A resumed process can start before the reconnected manager prepares this directory again.
func TestWorkGuideSurvivesNodeRestart(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	for _, guide := range []string{"read the guide", ""} {
		params, _ := json.Marshal(map[string]string{"dir": ".persona/assistant", "claude_md": "full", "work_guide_md": guide})
		out, err := n.handle(t.Context(), "assistant.prepare", params)
		if err != nil {
			t.Fatal(err)
		}
		cwd := out.(map[string]any)["cwd"].(string)
		n.assistantPrepared = nil
		args, _ := n.codexAssistantArgs("test", n.dir, cwd)
		if slices.Contains(args, "features.mcp_tool_list_changed=true") != (guide != "") {
			t.Fatal("wrong Codex discovery mode", args)
		}
		_, release, _ := n.assistantLarkEnvNamed(cwd, "resumed")
		release()
		for _, runtime := range []string{".claude", ".agents"} {
			dir := filepath.Join(cwd, runtime, "skills")
			_, err := os.Stat(filepath.Join(dir, "persona-work", "references", "record.md"))
			if guide != "" && err != nil || guide == "" && !os.IsNotExist(err) {
				t.Fatalf("guide=%q resumed %s: %v", guide, runtime, err)
			}
		}
	}
}

func TestWorkGuidePrepareWithLocalCodexSkill(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	cwd := prepareAssistantForTest(t, n, ".persona/assistant", nil, nil)
	at := filepath.Join(cwd, ".agents", "skills", "persona-work")
	if err := os.MkdirAll(at, 0o700); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(at, "SKILL.md")
	if err := os.WriteFile(custom, []byte("custom"), 0o600); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(map[string]string{"dir": ".persona/assistant", "claude_md": "full instructions", "work_guide_md": "short instructions"})
	if _, err := n.handle(t.Context(), "assistant.prepare", params); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{custom: "custom", filepath.Join(cwd, "AGENTS.md"): "full instructions"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v", path, got, err)
		}
	}
}
