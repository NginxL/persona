package mail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The mail MCP tools as they go over the wire (indented for reading). docs/assistant/prompts.md: regenerate with UPDATE_GOLDEN=1.
func TestGoldenMailTools(t *testing.T) {
	b, err := json.MarshalIndent((*Service)(nil).Tools(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := string(b) + "\n"
	path := filepath.Join("testdata", "prompts", "mail_tools.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil || got != string(want) {
		t.Fatalf("got\n%s\nwant (%v)\n%s", got, err, want)
	}
}
