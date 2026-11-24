package larkcli

import (
	"context"
	"testing"
)

// A restart after Poll has saved its cursor/seen ledger but before the manager
// collects the memory-only things must not permanently lose the new message.
func TestInboxRestartRecoversUncollectedItems(t *testing.T) {
	store := &inboxStateStore{}
	first := inboxTestConnector(t, "inbox")
	first.SetStateStore(store)
	moved, err := first.Poll(context.Background())
	if err != nil || !moved {
		t.Fatalf("first poll: moved=%v err=%v", moved, err)
	}
	const thread = "larkcli:chat:oc_example_dm"
	first.inboxMu.Lock()
	want, ok := first.inboxThings[thread]
	first.inboxMu.Unlock()
	if !ok || want.Last != "om_example_direct" || want.Done != "" {
		t.Fatalf("fixture did not produce the unresolved direct message: %+v", want)
	}

	// Model a process restart here: all connector memory is gone, while the
	// cursor and seen IDs already saved by Poll survive in the shared store.
	second := inboxTestConnector(t, "inbox")
	second.SetStateStore(store)
	if _, err := second.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := inboxThings(t, second)
	got, ok := after[thread]
	if !ok || got.Last != want.Last || got.Done != "" {
		t.Fatalf("new message lost across restart: before=%+v after=%#v", want, after)
	}
}
