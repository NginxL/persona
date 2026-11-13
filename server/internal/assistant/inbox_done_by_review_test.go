package assistant

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
)

// newestDoneEntry is the last done entry of an item's chain (state keys read the newest one, an older muted entry
// says nothing once the item was filed again).
func newestDoneEntry(t *testing.T, it *InboxItem) InboxChainEntry {
	t.Helper()
	for i := len(it.Chain) - 1; i >= 0; i-- {
		if it.Chain[i].Kind == ChainDone {
			return it.Chain[i]
		}
	}
	t.Fatalf("no done entry in the chain: %+v", it.Chain)
	return InboxChainEntry{}
}

// The user's own filing over the API records how it filed the item on the chain, and the listed JSON carries it:
// done_by stays as it was (user / dismissed), so the entry's by is what tells the two apart.
func TestInboxDoneChainByOverHTTP(t *testing.T) {
	for _, tc := range []struct{ body, by, key string }{
		{`{"done":true}`, "user", "assistant-inbox_chain.done_user"},
		{`{"done":true,"dismiss":true}`, "dismissed", "assistant-inbox_chain.done_dismissed"},
	} {
		t.Run(tc.by, func(t *testing.T) {
			text := i18n.T(i18n.Default, tc.key)
			h := newInboxHub(t)
			it := &InboxItem{ID: "lark:f1:1", Source: "lark", Kind: KindDM, Last: "1", Title: "Alice",
				StartedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
			h.inbox.mu.Lock()
			h.inbox.items[it.ID] = it
			h.inbox.mu.Unlock()
			if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/done", tc.body); code != 200 {
				t.Fatalf("filing: %d %v", code, out)
			}
			h.inbox.mu.Lock()
			e, doneBy := newestDoneEntry(t, it), it.DoneBy
			h.inbox.mu.Unlock()
			if doneBy != tc.by || e.By != tc.by || e.Text != text {
				t.Fatalf("done_by=%q entry=%+v, want by %q", doneBy, e, tc.by)
			}
			// The listed detail carries the entry as stored, by included.
			_, out := inboxCall(t, h.routes(), "GET", "/api/v1/inbox/"+it.ID, "")
			raw, _ := json.Marshal(out)
			if !bytes.Contains(raw, []byte(`"by":"`+tc.by+`"`)) {
				t.Fatalf("listed detail has no by: %s", raw)
			}
		})
	}
}

// A mute files the item read, says on the chain that a mute did it, and a later filing by the user replaces that:
// the newest done entry is what a state key reads.
func TestInboxMutedThenFiledAgainByTheUser(t *testing.T) {
	h := newInboxHub(t)
	now := time.Now().UTC()
	mute := InboxMute{Source: "example", Tag: connector.TagRepo, Value: "example/repository", Name: "Repository"}
	h.inbox.mu.Lock()
	h.inbox.mutes = []InboxMute{mute}
	it := &InboxItem{ID: "example:1", Source: mute.Source, Last: "1", Title: "Example", Tier: TierAction, UpdatedAt: now,
		Connector: &InboxConnector{Tags: []connector.Tag{{Key: mute.Tag, Value: mute.Value, Name: mute.Name}}}}
	h.inbox.items[it.ID] = it
	writes := h.inbox.applyMutes(now)
	h.inbox.mu.Unlock()
	if len(writes) != 1 || it.DoneBy != "read" || newestDoneEntry(t, it).By != DoneByMuted {
		t.Fatalf("muted filing: done_by=%q chain=%+v", it.DoneBy, it.Chain)
	}
	// Move back and file by hand: done_by user, and the newest done entry says user, not muted.
	if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/done", `{"done":false}`); code != 200 {
		t.Fatalf("move back: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	open := it.open()
	h.inbox.mu.Unlock()
	if !open {
		t.Fatal("the item did not come back open")
	}
	if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/done", `{"done":true}`); code != 200 {
		t.Fatalf("filing: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	if e, doneBy := newestDoneEntry(t, it), it.DoneBy; doneBy != "user" || e.By != "user" {
		t.Fatalf("shifted: done_by=%q entry=%+v", doneBy, e)
	}
	h.inbox.mu.Unlock()
}

// A connector saying a thread is over files it read through the same path, so its done entry carries by read;
// a replied thread is recorded as self_replied and adds no done entry.
func TestInboxConnectorDoneBy(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		done      string
		entry     string
		by        string
		wantEntry bool
	}{
		{connector.DoneRead, ChainDone, "read", true},
		{connector.DoneReplied, ChainSelfReplied, "", false},
	} {
		t.Run(tc.done, func(t *testing.T) {
			h := newInboxHub(t)
			it := &InboxItem{ID: "example:1", Source: "example", Last: "1", Title: "Example", Tier: TierAction, UpdatedAt: now,
				Connector: &InboxConnector{Thread: "t1"}}
			h.inbox.mu.Lock()
			h.inbox.items[it.ID] = it
			changed := h.inbox.settleThing(it, connector.InboxThing{Thread: "t1", Kind: "review_requested", Last: "1", Done: tc.done}, now)
			h.inbox.mu.Unlock()
			if !changed || it.DoneBy != tc.done {
				t.Fatalf("settled=%v done_by=%q chain=%+v", changed, it.DoneBy, it.Chain)
			}
			last := it.Chain[len(it.Chain)-1]
			if last.Kind != tc.entry {
				t.Fatalf("last entry = %+v, want kind %q", last, tc.entry)
			}
			if tc.wantEntry && last.By != tc.by {
				t.Fatalf("done entry by = %q, want %q", last.By, tc.by)
			}
			for _, e := range it.Chain {
				if e.Kind == ChainDone && !tc.wantEntry {
					t.Fatalf("a replied thread added a done entry: %+v", e)
				}
			}
		})
	}
}

// A chain saved before by existed loads and lists without the field; one filed now keeps it through storage, so a
// restart does not turn done_muted back into done_read.
func TestInboxDoneChainBySurvivesStorage(t *testing.T) {
	h := newInboxHub(t)
	at := time.Now().UTC()
	old := &InboxItem{ID: "old", Source: "lark", Kind: KindDM, Last: "1", StartedAt: at, UpdatedAt: at, DoneAt: &at, DoneBy: "read",
		Chain: []InboxChainEntry{{ID: "c1", At: at, Kind: ChainDone, Text: i18n.T(i18n.En, "assistant-inbox_chain.done_read")}}}
	fresh := &InboxItem{ID: "new", Source: "lark", Kind: KindDM, Last: "2", StartedAt: at, UpdatedAt: at}
	h.inbox.mu.Lock()
	h.inbox.items[old.ID], h.inbox.items[fresh.ID] = old, fresh
	h.inbox.fileMuted(fresh, at, "Repository")
	h.inbox.mu.Unlock()

	// Both as the store keeps them, read back the way loadInbox does.
	for _, it := range []*InboxItem{old, fresh} {
		raw, err := json.Marshal(it)
		if err != nil {
			t.Fatal(err)
		}
		back := &InboxItem{}
		if err := json.Unmarshal(raw, back); err != nil {
			t.Fatal(err)
		}
		h.inbox.mu.Lock()
		listed, _ := json.Marshal(it.listed())
		h.inbox.mu.Unlock()
		switch it.ID {
		case "old":
			if strings.Contains(string(raw), `"by"`) || strings.Contains(string(listed), `"by"`) {
				t.Fatalf("an old done entry grew a by: %s / %s", raw, listed)
			}
		case "new":
			if newestDoneEntry(t, back).By != DoneByMuted || !bytes.Contains(listed, []byte(`"by":"muted"`)) {
				t.Fatalf("stored chain lost by: %s / %s", raw, listed)
			}
		}
	}
}
