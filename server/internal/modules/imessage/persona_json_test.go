package imessage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The read tools' JSON answers for subscriptions (CallJSON), pinned in testdata/readjson (UPDATE_GOLDEN=1 writes
// them): codes masked as in the text, nothing while "Hand to Persona" is off. server/internal/subscriptions
// dry-runs the same files item by item.
func TestReadToolsAnswerJSON(t *testing.T) {
	ctx := context.Background()
	c, n := personaRemote(t, map[string]string{"connector.chats": chatsPage(time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)),
		"connector.older": olderPage,
		"connector.search": `{"hits":[{"chat":{"id":"imessage-000000000001","name":"张三","members":[],"services":[],"last":"2026-10-30T08:00:00Z"},
		  "message":{"id":"21","at":"2026-10-30T07:59:00Z","kind":"message","sender":` + zhangJSON + `,"text":"示例：周五见"}}]}`})
	for _, tc := range []struct{ file, name, args string }{
		{"chats", "imessage_chats", `{}`},
		{"read", "imessage_read", `{"chat":"imessage-000000000001","limit":5}`},
		{"search", "imessage_search", `{"query":"周五"}`},
	} {
		out, ok, err := c.CallJSON(ctx, tc.name, json.RawMessage(tc.args))
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", tc.file, ok, err)
		}
		if strings.Contains(string(out), "123456") {
			t.Errorf("%s: a code was not masked: %s", tc.file, out)
		}
		golden(t, filepath.Join("testdata", "readjson", tc.file+".json"), out)
	}
	// The same arguments go to the Mac as for the text.
	if got := n.sent("connector.older"); len(got) != 1 || !strings.Contains(got[0], `"limit":5`) || !strings.Contains(got[0], `"keep":"contacts"`) {
		t.Errorf("older %v", got)
	}
	if _, ok, err := c.CallJSON(ctx, "imessage_read", json.RawMessage(`{"chat":"+15550100"}`)); !ok || err == nil {
		t.Errorf("a bad chat id: %v %v", ok, err)
	}
	if out, ok, err := c.CallJSON(ctx, "imessage_send", json.RawMessage(`{}`)); ok || out != nil || err != nil {
		t.Errorf("not a read tool: %s %v %v", out, ok, err)
	}

	c.SetAI(false)
	calls := len(n.sent("connector.chats"))
	if _, ok, err := c.CallJSON(ctx, "imessage_chats", nil); !ok || err == nil || len(n.sent("connector.chats")) != calls {
		t.Errorf("read while off: %v %v", ok, err)
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
