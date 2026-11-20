package node

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssistantExistingProfileKeepsSettings(t *testing.T) {
	f := cliProfileTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	source := filepath.Join(n.home, ".claude")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceSettings := `{"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"extension hook --persona-extension=agent-bypass-allow"}]}]}}`
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(sourceSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(n.dir, "profiles", "claude")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profile, "settings.json")
	original := []byte("{\n  \"permissions\": {\"ask\": [\"Bash(example:*)\"]},\n  \"custom\": \"keep me\"\n}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := n.spawnClaude("a5500000-0000-4000-8000-000000000559", t.TempDir(), "claude", false, false, cliExec())
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("existing profile was rewritten:\nbefore %s\nafter  %s", original, got)
	}
}

func TestAssistantNewProfileWithoutSourceCreatesNoSettings(t *testing.T) {
	f := cliProfileTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	p, err := n.spawnClaude("a5500000-0000-4000-8000-000000000560", t.TempDir(), "claude", false, false, cliExec())
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	path := filepath.Join(n.dir, "profiles", "claude", "settings.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a profile without a source hook gained settings: %v", err)
	}
}

func TestAssistantBypassHookTagMatching(t *testing.T) {
	for _, tag := range assistantBypassHookTags {
		if !isAssistantBypassHookCommand("extension hook --config-dir '/tmp/a b' " + tag) {
			t.Errorf("existing tag not recognized: %s", tag)
		}
		if isAssistantBypassHookCommand("extension hook " + tag + ".other") {
			t.Errorf("a suffix is not the ownership marker: %s.other", tag)
		}
	}
	for _, command := range []string{"", "extension hook", "extension hook --persona-extension=agent-bypass-allow extra"} {
		if isAssistantBypassHookCommand(command) {
			t.Errorf("unrelated command accepted: %q", command)
		}
	}
}

func TestTaggedAssistantBypassGroupsIgnoresMalformedSettings(t *testing.T) {
	for _, settings := range []map[string]any{
		{},
		{"hooks": "not an object"},
		{"hooks": map[string]any{"PermissionRequest": "not a list"}},
		{"hooks": map[string]any{"PermissionRequest": []any{nil, "not a group", map[string]any{"hooks": "not a list"}}}},
	} {
		if got := taggedAssistantBypassGroups(settings); len(got) != 0 {
			t.Errorf("malformed settings produced hooks: %+v", got)
		}
	}
}
