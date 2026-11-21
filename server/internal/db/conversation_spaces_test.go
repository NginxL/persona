package db

import (
	"context"
	"encoding/json"
	"maps"
	"testing"
)

// The conversations' spaces (#10941): each noted once a computer, the health space's for good; read by session id on
// whichever computer noted it, the health space's when any did; and the records of deleted conversations read many
// at once.
func TestConversationSpaces(t *testing.T) {
	d, ctx := open(t), context.Background()
	note := func(rows ...ConversationSpace) {
		t.Helper()
		if err := d.NoteConversationSpaces(ctx, rows); err != nil {
			t.Fatal(err)
		}
	}
	note(ConversationSpace{"server", "chat", ""}, ConversationSpace{"server", "h0", "health"},
		ConversationSpace{"server", "twice", ""}, ConversationSpace{"server", "twice", "health"},
		ConversationSpace{"laptop", "moved", ""}, ConversationSpace{"server", "moved", "health"})
	// Noted again: what is noted stays, and only a move into the health space is taken.
	note(ConversationSpace{"server", "chat", "health"}, ConversationSpace{"server", "h0", ""})
	note(ConversationSpace{"server", "chat", ""})
	got, err := d.ConversationSpaces(ctx, []string{"chat", "h0", "twice", "moved", "never"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"chat": "health", "h0": "health", "twice": "health", "moved": "health"}
	if !maps.Equal(got, want) {
		t.Fatalf("spaces = %v, want %v", got, want)
	}
	note(ConversationSpace{"server", "plain", ""})
	if got, err := d.ConversationSpaces(ctx, []string{"plain"}); err != nil || !maps.Equal(got, map[string]string{"plain": ""}) {
		t.Fatalf("plain = %v, %v", got, err)
	}
	if got, err := d.ConversationSpaces(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("none asked = %v, %v", got, err)
	}

	if got, err := d.ConversationDeletions(ctx, "server", []string{"a"}); err != nil || len(got) != 0 {
		t.Fatalf("no ledger yet: %v, %v", got, err)
	}
	for _, id := range []string{"a", "b"} {
		raw, _ := json.Marshal(map[string]string{"machine_id": "server", "session_id": id, "space": "health"})
		if err := d.PutConversationDeletion(ctx, "server", id, raw); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]string{"machine_id": "laptop", "session_id": "c"})
	if err := d.PutConversationDeletion(ctx, "laptop", "c", raw); err != nil {
		t.Fatal(err)
	}
	dels, err := d.ConversationDeletions(ctx, "server", []string{"a", "b", "c", "none"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dels) != 2 || dels["a"] == nil || dels["b"] == nil {
		t.Fatalf("deletions = %v", dels)
	}
	for id, raw := range dels {
		one, found, err := d.ConversationDeletion(ctx, "server", id)
		if err != nil || !found || string(one) != string(raw) {
			t.Errorf("%s: batch %s, one by one %s (%v, %v)", id, raw, one, found, err)
		}
	}
}
