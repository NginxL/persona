package assistant

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestInboxDoneChainBy(t *testing.T) {
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for _, by := range []string{"user", "dismissed", "read", doneByManager} {
		t.Run(by, func(t *testing.T) {
			b := newInbox()
			it := &InboxItem{ID: "item"}
			b.file(it, at, by)
			if it.DoneBy != by || len(it.Chain) != 1 || it.Chain[0].Kind != ChainDone || it.Chain[0].By != by {
				t.Fatalf("done_by=%q chain=%+v, want by %q", it.DoneBy, it.Chain, by)
			}
			raw, err := json.Marshal(it.Chain[0])
			if err != nil || !bytes.Contains(raw, []byte(`"by":"`+by+`"`)) {
				t.Fatalf("done entry JSON=%s, err=%v", raw, err)
			}
		})
	}
}

func TestInboxMutedDoneChainBy(t *testing.T) {
	b := newInbox()
	it := &InboxItem{ID: "item"}
	b.fileMuted(it, time.Now().UTC(), "Repository")
	if it.DoneBy != "read" || len(it.Chain) != 1 || it.Chain[0].Kind != ChainDone || it.Chain[0].By != DoneByMuted {
		t.Fatalf("done_by=%q chain=%+v, want read and by %q", it.DoneBy, it.Chain, DoneByMuted)
	}
	raw, err := json.Marshal(it.Chain[0])
	if err != nil || !bytes.Contains(raw, []byte(`"by":"muted"`)) {
		t.Fatalf("muted done entry JSON=%s, err=%v", raw, err)
	}
}

func TestInboxOldDoneChainOmitsBy(t *testing.T) {
	raw, err := json.Marshal(InboxChainEntry{Kind: ChainDone})
	if err != nil || bytes.Contains(raw, []byte(`"by"`)) {
		t.Fatalf("old done entry JSON=%s, err=%v", raw, err)
	}
}
