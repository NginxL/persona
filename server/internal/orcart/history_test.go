package orcart_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
)

// The real history page shape (agentSession.history): stable journal items and an opaque cursor for the next older page.
func TestHistoryReadsJournalItemsAndOlderCursor(t *testing.T) {
	f := orcarttest.New(t)
	f.Handle("agentSession.history", func(p map[string]any) any {
		page := map[string]any{
			"sessionId": chat, "epoch": "epoch-1", "fence": 7, "direction": p["direction"],
			"working": true, "hasOlder": true, "hasNewer": false,
			"items": []any{
				map[string]any{"itemId": "assistant-2", "revision": 3, "sequence": 12, "sequenceIndex": 1, "observedAt": 1793850002000,
					"body": map[string]any{"kind": "message", "role": "assistant", "blocks": []any{map[string]any{"type": "text", "text": "Bot reply"}}}},
				map[string]any{"itemId": "user-1", "revision": 1, "sequence": 11, "observedAt": 1793850001000,
					"body": map[string]any{"kind": "message", "role": "user", "blocks": []any{map[string]any{"type": "text", "text": "Assign a task"}}}},
			},
			"window": map[string]any{"nextCursor": map[string]any{"epoch": "epoch-1", "sequence": 11}},
		}
		return map[string]any{"ok": true, "page": page, "providerSession": map[string]any{"id": "provider-1"}}
	})
	c := &orcart.Client{MetaPath: f.MetaPath}
	page, err := c.History(context.Background(), chat, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.Fence != 7 || !page.Working || !page.HasOlder || len(page.Items) != 2 {
		t.Fatalf("page %+v", page)
	}
	first := page.Items[0]
	if first.ItemID != "assistant-2" || first.Sequence != 12 || first.SequenceIndex != 1 || first.ObservedAt != 1793850002000 ||
		first.Body.Kind != "message" || first.Body.Role != "assistant" || len(first.Body.Blocks) != 1 || first.Body.Blocks[0].Text != "Bot reply" {
		t.Fatalf("first item %+v", first)
	}
	if page.NextCursor == "" {
		t.Fatal("an older page has a cursor")
	}
	if _, err := c.History(context.Background(), chat, page.NextCursor, 2); err != nil {
		t.Fatal(err)
	}
	calls := f.Params("agentSession.history")
	if len(calls) != 2 || calls[0]["direction"] != "tail" || calls[1]["direction"] != "before" || calls[0]["limit"] != float64(2) {
		t.Fatalf("history calls %#v", calls)
	}
	cursor, ok := calls[1]["cursor"].(map[string]any)
	if !ok || cursor["epoch"] != "epoch-1" || cursor["sequence"] != float64(11) {
		t.Fatalf("older cursor %#v", calls[1]["cursor"])
	}
	if got := f.Invalid(); len(got) != 0 {
		t.Fatalf("schema rejected history: %v", got)
	}
}

func TestHistoryRejectsBadCursorWithoutCallingOrca(t *testing.T) {
	f := orcarttest.New(t)
	c := &orcart.Client{MetaPath: f.MetaPath}
	if _, err := c.History(context.Background(), chat, "not-a-cursor", 40); err == nil {
		t.Fatal("bad cursor accepted")
	}
	if n := f.Count("agentSession.history"); n != 0 {
		t.Fatalf("%d upstream calls for a bad cursor", n)
	}
}

// The fake validates the same strict history params used by Orca, not a Persona-invented transport.
func TestHistoryRequestMatchesOrcaSchema(t *testing.T) {
	f := orcarttest.New(t)
	f.Handle("agentSession.history", func(map[string]any) any {
		return map[string]any{"ok": true, "page": map[string]any{"sessionId": chat, "epoch": "e", "items": []any{}, "window": map[string]any{}}}
	})
	c := &orcart.Client{MetaPath: f.MetaPath}
	if _, err := c.History(context.Background(), chat, "", 40); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f.Params("agentSession.history")[0])
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	if decoded["sessionId"] != chat || decoded["direction"] != "tail" || decoded["limit"] != float64(40) {
		t.Fatalf("params %s", raw)
	}
}
