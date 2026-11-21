package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestPositionalItemRemovalIsNotFoundAfterRefresh(t *testing.T) {
	n, home := testNode(t)
	const session = "11111111-2222-3333-4444-555555555555"
	const kept = `{"type":"user","uuid":"kept","timestamp":"2026-11-20T00:00:00Z","message":{"role":"user","content":"Kept question"}}` + "\n"
	const saved = `{"type":"assistant","timestamp":"2026-11-20T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"Saved reply"}]}}` + "\n"
	path := filepath.Join(home, "fixture.jsonl")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(kept + saved)
	n.setPath(session, path)
	read := func(strict bool) (any, error) {
		params, err := json.Marshal(map[string]any{"session_id": session, "item_id": "line2#0", "strict_read": strict})
		if err != nil {
			t.Fatal(err)
		}
		return n.handle(context.Background(), "session.item", params)
	}
	result, err := read(false)
	if item, ok := result.(proto.Item); err != nil || !ok || item.Text != "Saved reply" || item.SourceFingerprint == "" {
		t.Fatalf("expected a readable positional item: %+v, %v", result, err)
	}
	write(kept)
	for _, strict := range []bool{false, true} {
		if _, err := read(strict); refusalCode(err) != codeNotFound {
			t.Errorf("removed item with strict=%t: got %v, want %s", strict, err, codeNotFound)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := read(false); refusalCode(err) != codeNotFound {
		t.Errorf("removed transcript: got %v, want %s", err, codeNotFound)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := read(false); err == nil || refusalCode(err) == codeNotFound {
		t.Fatalf("failed refresh should stay retryable: %v", err)
	}
}
