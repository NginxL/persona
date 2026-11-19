package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAssistantEmptyProfileRetriesFailedSeed(t *testing.T) {
	f := cliProfileTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	source := filepath.Join(n.home, ".claude")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"extension hook --persona-extension=agent-bypass-allow"}]}]}}`)
	if err := os.WriteFile(filepath.Join(source, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	originalWrite := assistantProfileWrite
	t.Cleanup(func() { assistantProfileWrite = originalWrite })
	fail := true
	assistantProfileWrite = func(path string, body []byte) error {
		if fail {
			return errors.New("synthetic seed write failure")
		}
		return originalWrite(path, body)
	}
	profile := filepath.Join(n.dir, "profiles", "claude")
	const id = "a5500000-0000-4000-8000-000000000563"
	if _, err := n.spawnClaude(id, t.TempDir(), "claude", false, false, cliExec()); err == nil {
		t.Fatal("seed write failure did not abort the first start")
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("failed first start left a nonempty profile behind: %v", err)
	}

	fail = false
	p, err := n.spawnClaude(id, t.TempDir(), "claude", false, false, cliExec())
	if err != nil {
		t.Fatalf("second start did not retry the seed: %v", err)
	}
	defer p.stop()
	if _, err := os.Stat(filepath.Join(profile, "settings.json")); err != nil {
		t.Fatalf("second start has no seeded settings: %v", err)
	}
}
