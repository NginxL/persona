package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/proto"
)

func historyDB(t *testing.T) (*DB, string) {
	t.Helper()
	url := freshDatabase(t)
	d, err := Open(context.Background(), url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d, url
}

func historyItem(id string, seq int, text string) proto.Item {
	return proto.Item{ID: id, Seq: seq, Role: "user", Kind: proto.KindText, Text: text}
}

func TestConversationHistoryDurabilityReplayOrderAndGeneration(t *testing.T) {
	d, url := historyDB(t)
	ctx := context.Background()
	p := ConversationPage{MachineID: "box", SessionID: "chat", History: "h1", Revision: 10,
		ThroughSeq: 10, Tail: true, Items: []proto.Item{
			historyItem("z", 2, "same words"), historyItem("a", 2, "second block"), historyItem("b", 8, "same words"),
		}}
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.ExpectedHistory = "h1"
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	// A new connection/process sees the actual committed data, not an in-memory cache.
	restarted, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	h, err := restarted.ConversationHistory(ctx, "box", "chat")
	if err != nil || h == nil || h.TailSeq == nil || *h.TailSeq != 10 || !conversationlog.Covers(h.Coverage, 0, 10) {
		t.Fatalf("durable coverage %+v, %v", h, err)
	}
	rows, err := restarted.ConversationItems(ctx, "box", "chat", "h1", 0, 10, 1, false)
	if err != nil || len(rows) != 2 || rows[0].Item.ID != "z" || rows[1].Item.ID != "a" {
		t.Fatalf("source line split or reordered: %+v %v", rows, err)
	}
	rows, err = restarted.ConversationItems(ctx, "box", "chat", "h1", 0, 10, 100, true)
	if err != nil || len(rows) != 3 {
		t.Fatalf("replay duplicated rows or identical text lost: %d %v", len(rows), err)
	}
	// Replacement deletes the old generation and fences an earlier in-flight fetch.
	newPage := ConversationPage{MachineID: "box", SessionID: "chat", History: "h2", ExpectedHistory: "h1", ThroughSeq: 1, Tail: true,
		Items: []proto.Item{historyItem("new", 1, "replacement")}}
	if err := d.PutConversationPage(ctx, newPage); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationPage(ctx, p); !errors.Is(err, ErrConversationHistoryChanged) {
		t.Fatalf("late generation accepted: %v", err)
	}
	if _, err := d.ConversationItems(ctx, "box", "chat", "h1", 0, 10, 100, false); !errors.Is(err, ErrConversationHistoryChanged) {
		t.Fatalf("old reader accepted: %v", err)
	}
	rows, err = d.ConversationItems(ctx, "box", "chat", "h2", 0, 10, 100, false)
	if err != nil || len(rows) != 1 || rows[0].Item.ID != "new" {
		t.Fatalf("mixed generations: %+v %v", rows, err)
	}
}

func TestConversationHistoryPartialEmptyAndBoundedUpgrade(t *testing.T) {
	d, _ := historyDB(t)
	ctx := context.Background()
	p := ConversationPage{MachineID: "box", SessionID: "chat", History: "h1", ThroughSeq: 20, Tail: true,
		Items: []proto.Item{historyItem("big", 18, strings.Repeat("x", 100000))}}
	p.FromSeq = 10
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	h, err := d.ConversationHistory(ctx, "box", "chat")
	if err != nil || conversationlog.Covers(h.Coverage, 0, 20) {
		t.Fatalf("tail called complete: %+v %v", h, err)
	}
	rows, err := d.ConversationItems(ctx, "box", "chat", "h1", 0, 20, 100, false)
	if err != nil || len(rows) != 1 || rows[0].Complete {
		t.Fatalf("overflow not referenced: %+v %v", rows, err)
	}
	raw, _ := json.Marshal(rows[0].Item)
	if len(raw) > conversationlog.MaxItemBytes || rows[0].SourceBytes < 100000 {
		t.Fatal("unbounded stored payload or lost source size")
	}
	p.FromSeq, p.ThroughSeq, p.Tail = 0, 10, false
	p.ExpectedHistory = "h1"
	p.Items = []proto.Item{historyItem("small", 3, "full")}
	p.Items[0].FullSize = 100
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	h, err = d.ConversationHistory(ctx, "box", "chat")
	if err != nil || !conversationlog.Covers(h.Coverage, 0, 20) {
		t.Fatalf("prefix did not reconcile: %+v %v", h, err)
	}
	// A complete item upgrades a truncated page, and replay cannot downgrade it.
	rows, err = d.ConversationItems(ctx, "box", "chat", "h1", 0, 10, 100, false)
	if err != nil || len(rows) != 1 || rows[0].Complete {
		t.Fatalf("truncated row called complete: %+v %v", rows, err)
	}
	p.Items[0].FullSize = 0
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.FromSeq, p.ThroughSeq = 0, 10
	p.Items[0].FullSize = 100
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	rows, _ = d.ConversationItems(ctx, "box", "chat", "h1", 0, 10, 100, false)
	if len(rows) != 1 || !rows[0].Complete || rows[0].Item.FullSize != 0 {
		t.Fatal("degraded a complete item")
	}
	p.SessionID, p.Items, p.ThroughSeq, p.Tail = "empty", nil, 0, true
	p.History, p.ExpectedHistory = "", ""
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	h, err = d.ConversationHistory(ctx, "box", "empty")
	if err != nil || h == nil || h.TailSeq == nil || *h.TailSeq != 0 {
		t.Fatalf("empty versus missing lost: %+v %v", h, err)
	}
	stats, err := d.ConversationHistoryStats(ctx)
	if err != nil || stats.Conversations != 2 || stats.Items != 2 || stats.Incomplete != 1 || stats.PayloadBytes <= 0 {
		t.Fatalf("aggregate growth evidence %+v %v", stats, err)
	}
}

func TestConversationHistoryTailInvalidationAndStaleRevision(t *testing.T) {
	d, _ := historyDB(t)
	ctx := context.Background()
	p := ConversationPage{MachineID: "box", SessionID: "chat", History: "h1", Revision: 100, ThroughSeq: 1, Tail: true,
		Items: []proto.Item{historyItem("first", 1, "first")}}
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.ExpectedHistory, p.Revision, p.FromSeq, p.ThroughSeq, p.Tail = "h1", 200, 1, 2, false
	p.Items = []proto.Item{historyItem("next", 2, "next")}
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	h, err := d.ConversationHistory(ctx, "box", "chat")
	if err != nil || h.TailSeq != nil {
		t.Fatalf("old tail survived new revision: %+v %v", h, err)
	}
	p.Revision, p.Tail = 100, true
	if err := d.PutConversationPage(ctx, p); !errors.Is(err, ErrConversationHistoryChanged) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	p.Revision, p.History = 200, "h2"
	if err := d.PutConversationPage(ctx, p); !errors.Is(err, ErrConversationHistoryChanged) {
		t.Fatalf("partial replacement accepted: %v", err)
	}
}

func TestConversationHistoryKeepsJSONToolBytesIncludingNUL(t *testing.T) {
	d, _ := historyDB(t)
	ctx := context.Background()
	it := historyItem("tool-result", 1, "literal \\u0000 and NUL \x00 stay distinct")
	it.Kind = proto.KindToolResult
	it.ToolInput = json.RawMessage(`{"literal":"\\u0000","nul":"\u0000","number":9007199254740993}`)
	p := ConversationPage{MachineID: "box", SessionID: "chat", History: "h1", ThroughSeq: 1, Tail: true, Items: []proto.Item{it}}
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ConversationItems(ctx, "box", "chat", "h1", 0, 1, 100, false)
	if err != nil || len(rows) != 1 || rows[0].Item.Text != it.Text || string(rows[0].Item.ToolInput) != string(it.ToolInput) {
		t.Fatalf("tool bytes changed or JSONB rejected an escaped NUL: %+v %v", rows, err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE conversation_items SET payload=$1`, []byte(strings.Repeat("x", conversationlog.MaxItemBytes+1))); err == nil {
		t.Fatal("database did not enforce the payload ceiling")
	}
}

func TestConversationSummaryIsBoundedAndGenerationFenced(t *testing.T) {
	d, _ := historyDB(t)
	ctx := t.Context()
	p := ConversationPage{MachineID: "box", SessionID: "chat", History: "h1", Revision: 10, ThroughSeq: 0, Tail: true}
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationSummary(ctx, "box", "chat", "h1", 10, "source compaction"); err != nil {
		t.Fatal(err)
	}
	h, err := d.ConversationHistory(ctx, "box", "chat")
	if err != nil || h.Summary != "source compaction" || !h.SummaryKnown {
		t.Fatalf("summary %+v %v", h, err)
	}
	if err := d.PutConversationSummary(ctx, "box", "chat", "h1", 9, "stale"); !errors.Is(err, ErrConversationHistoryChanged) {
		t.Fatalf("stale summary accepted: %v", err)
	}
	if err := d.PutConversationSummary(ctx, "box", "chat", "h1", 10, strings.Repeat("x", conversationlog.MaxItemBytes+1)); err == nil {
		t.Fatal("unbounded summary")
	}
	p.ExpectedHistory, p.History = "h1", "h2"
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	h, err = d.ConversationHistory(ctx, "box", "chat")
	if err != nil || h.Summary != "" || h.SummaryKnown {
		t.Fatalf("old summary survived replacement %+v %v", h, err)
	}
	if err := d.PutConversationSummary(ctx, "box", "chat", "h1", 10, "late"); !errors.Is(err, ErrConversationHistoryChanged) {
		t.Fatalf("old generation summary accepted: %v", err)
	}
}

func TestConversationDeletionCascadesAndFencesConcurrentHistory(t *testing.T) {
	d, _ := historyDB(t)
	ctx := context.Background()
	p := ConversationPage{MachineID: "box", SessionID: "chat", History: "h1", ThroughSeq: 1, Tail: true,
		Items: []proto.Item{historyItem("i1", 1, "private content")}}
	if err := d.PutConversationPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.ExpectedHistory = "h1"
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := d.PutConversationPage(ctx, p)
			if err != nil && !errors.Is(err, ErrConversationDeleted) {
				t.Errorf("concurrent ingestion: %v", err)
			}
		}()
	}
	if err := d.PutConversationDeletion(ctx, "box", "chat", json.RawMessage(`{"session_id":"chat"}`)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := d.PutConversationPage(ctx, p); !errors.Is(err, ErrConversationDeleted) {
		t.Fatalf("recreated deleted content: %v", err)
	}
	var remaining int
	if err := d.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM conversation_histories)+(SELECT count(*) FROM conversation_items)`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("deleted rows/references remain: %d %v", remaining, err)
	}
}
