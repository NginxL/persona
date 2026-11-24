package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// mail_accounts and mail_search answer subscriptions with JSON (CallJSON), pinned in testdata/readjson
// (UPDATE_GOLDEN=1 writes them); mail_read and mail_send have none. server/internal/subscriptions dry-runs the
// same files item by item.
func TestReadToolsAnswerJSON(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	ctx := context.Background()
	srv.appendMessage(t, "INBOX", rawMessage("Quarterly report", "Example Boss <boss@example.com>", "Numbers are attached"))
	srv.appendMessage(t, "INBOX", rawMessage("Lunch", "friend@example.com", "See you at noon"))

	for _, tc := range []struct{ file, name, args string }{
		{"accounts", ToolAccounts, `{}`},
		{"search", ToolSearch, `{"account_id":"acc_test","limit":5}`},
	} {
		out, ok, err := s.CallJSON(ctx, tc.name, json.RawMessage(tc.args))
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", tc.file, ok, err)
		}
		golden(t, filepath.Join("testdata", "readjson", tc.file+".json"), out)
	}

	if _, ok, err := s.CallJSON(ctx, ToolSearch, json.RawMessage(`not json`)); !ok || err == nil {
		t.Errorf("malformed arguments: %v %v", ok, err)
	}
	for _, name := range []string{ToolRead, ToolSend, ToolSaveAttachment} {
		if out, ok, err := s.CallJSON(ctx, name, json.RawMessage(`{}`)); ok || out != nil || err != nil {
			t.Errorf("%s answered JSON: %s %v %v", name, out, ok, err)
		}
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
