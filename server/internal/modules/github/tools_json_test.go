package github

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The read tools' JSON answers for subscriptions (CallJSON) on the made-up API, pinned in testdata/readjson
// (UPDATE_GOLDEN=1 writes them). server/internal/subscriptions dry-runs the same files item by item.
func TestReadToolsAnswerJSON(t *testing.T) {
	ctx := context.Background()
	c, _ := fakeAPI(t)
	for _, tc := range []struct{ file, name, args string }{
		{"search", "github_search", `{"query":"is:pr repo:example-org/example-repo"}`},
		{"search_code", "github_search", `{"query":"example","kind":"code"}`},
		{"read", "github_read", `{"repo":"example-org/example-repo","number":12,"comments":2}`},
		{"diff", "github_diff", `{"repo":"example-org/example-repo","number":12}`},
		{"diff_path", "github_diff", `{"repo":"example-org/example-repo","number":12,"path":"src/example.go"}`},
		{"checks", "github_checks", `{"repo":"example-org/example-repo","number":12}`},
	} {
		out, ok, err := c.CallJSON(ctx, tc.name, json.RawMessage(tc.args))
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", tc.file, ok, err)
		}
		golden(t, filepath.Join("testdata", "readjson", tc.file+".json"), out)
	}

	// The same errors as the text, and no JSON answer for anything but a read tool.
	if _, ok, err := c.CallJSON(ctx, "github_search", json.RawMessage(`{"query":" "}`)); !ok || err == nil {
		t.Errorf("empty query: %v %v", ok, err)
	}
	if _, ok, err := c.CallJSON(ctx, "github_read", json.RawMessage(`{"repo":"../x","number":1}`)); !ok || err == nil {
		t.Errorf("bad repo: %v %v", ok, err)
	}
	if out, ok, err := c.CallJSON(ctx, "github_comment", json.RawMessage(`{}`)); ok || out != nil || err != nil {
		t.Errorf("an action answered JSON: %s %v %v", out, ok, err)
	}
}

// golden compares got, indented, with the file at path; UPDATE_GOLDEN=1 writes it.
func golden(t *testing.T, path string, got json.RawMessage) {
	t.Helper()
	var b bytes.Buffer
	if err := json.Indent(&b, got, "", "  "); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	b.WriteString("\n")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil || b.String() != string(want) {
		t.Errorf("%s: got\n%s\nwant (%v)\n%s", path, b.String(), err, want)
	}
}
