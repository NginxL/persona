package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistantProfileUsesValidSourceAfterReadFailure(t *testing.T) {
	f := cliProfileTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	source := filepath.Join(n.home, ".claude")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	alternate := filepath.Join(n.home, "channel")
	if err := os.MkdirAll(alternate, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"extension hook --persona-extension=agent-bypass-allow"}]}]}}`)
	if err := os.WriteFile(filepath.Join(alternate, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	n.claudeStartDir.Store(&alternate)
	profile := filepath.Join(n.dir, "profiles", "other")
	if err := n.prepareAssistantProfile(profile); err != nil {
		t.Fatalf("a later valid source was blocked by an earlier read failure: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(profile, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if groups := taggedAssistantBypassGroups(got); len(groups) != 1 {
		t.Fatalf("later valid source was not seeded: %s", raw)
	}
}

func TestAssistantProfileIgnoresUnrelatedSourceReadFailure(t *testing.T) {
	f := cliProfileTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	source := filepath.Join(n.home, ".claude")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(n.home, "some-project-claude")
	if err := os.MkdirAll(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelated, "settings.json"), []byte(`{"hooks": {,}`), 0o600); err != nil {
		t.Fatal(err)
	}
	n.claudeStartDir.Store(&unrelated)
	const id = "a5500000-0000-4000-8000-000000000590"
	p, err := n.spawnClaude(id, t.TempDir(), "claude", false, false, cliExec())
	if err != nil {
		t.Fatalf("an unrelated broken source without any opt-in blocked a new profile: %v", err)
	}
	defer p.stop()
	if _, err := os.Stat(filepath.Join(n.dir, "profiles", "claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("a source without an opt-in produced settings: %v", err)
	}
	if got := f.log.String(); !strings.Contains(got, unrelated) {
		t.Fatalf("the ignored source error was not observable in the node log: %q", got)
	}
}

func TestAssistantProfileRetriesSourceReadFailure(t *testing.T) {
	f := cliProfileTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	source := filepath.Join(n.home, ".claude")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "settings.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(n.dir, "profiles", "claude")
	const id = "a5500000-0000-4000-8000-000000000564"
	if _, err := n.spawnClaude(id, t.TempDir(), "claude", false, false, cliExec()); err == nil {
		t.Fatal("an unreadable source policy did not abort the first start")
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("failed source read left a nonempty profile: %v", err)
	}
	settings := []byte(`{"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"extension hook --persona-extension=agent-bypass-allow"}]}]}}`)
	if err := os.WriteFile(path, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := n.spawnClaude(id, t.TempDir(), "claude", false, false, cliExec())
	if err != nil {
		t.Fatalf("second start did not retry after the source became readable: %v", err)
	}
	defer p.stop()
	if _, err := os.Stat(filepath.Join(profile, "settings.json")); err != nil {
		t.Fatalf("second start has no seeded settings: %v", err)
	}
}
