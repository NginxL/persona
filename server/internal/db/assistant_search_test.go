package db

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
	"time"
)

// The chat search index (#6282, 0511): pages and cursors written together, a rewritten transcript starting over, a
// search that wants every word in one message, at most n newest of each chat, newest first, with each chat's count.
func TestAssistantSearchIndex(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, freshDatabase(t), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	t0 := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	row := func(seq int, role, body string) SearchItemRow {
		return SearchItemRow{Seq: seq, ItemID: fmt.Sprintf("i%d#0", seq), Role: role, TS: t0.Add(time.Duration(seq) * time.Minute), Body: body, Folded: body}
	}
	if err := d.PutSearchItems(ctx, "server", "trip", "h1", 4, []SearchItemRow{
		row(1, "user", "我想去青石巷 bluelane 看看"), row(2, "assistant", "紫藤茶馆 在巷口 bluelane"), row(4, "user", "bluelane again"),
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := d.PutSearchItems(ctx, "server", "old", "", 1, []SearchItemRow{row(3, "assistant", "旧的 bluelane")}, false); err != nil {
		t.Fatal(err)
	}
	// written again (a page read twice): the same rows, not two of each
	if err := d.PutSearchItems(ctx, "server", "trip", "h1", 4, []SearchItemRow{row(4, "user", "bluelane again")}, false); err != nil {
		t.Fatal(err)
	}
	cursors, err := d.SearchCursors(ctx)
	if err != nil || len(cursors) != 2 {
		t.Fatalf("cursors %+v, %v", cursors, err)
	}
	for _, c := range cursors {
		if c.SessionID == "trip" && (c.History != "h1" || c.AfterSeq != 4) {
			t.Fatalf("trip cursor %+v", c)
		}
	}
	search := func(words []string, chats ...string) []string {
		t.Helper()
		machines := make([]string, len(chats))
		for i := range chats {
			machines[i] = "server"
		}
		rows, err := d.SearchAssistantItems(ctx, machines, chats, words, 2)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, fmt.Sprintf("%s/%d of %d", r.SessionID, r.Seq, r.Hits))
		}
		return out
	}
	if got := fmt.Sprint(search([]string{"bluelane"}, "trip", "old")); got != "[trip/4 of 3 old/3 of 1 trip/2 of 3]" {
		t.Fatalf("bluelane: %s", got)
	}
	if got := fmt.Sprint(search([]string{"bluelane"}, "old")); got != "[old/3 of 1]" {
		t.Fatalf("only the chats asked for: %s", got)
	}
	if got := fmt.Sprint(search([]string{"紫藤", "bluelane"}, "trip")); got != "[trip/2 of 1]" {
		t.Fatalf("two words in one message: %s", got)
	}
	if got := search([]string{"紫藤", "看看"}, "trip"); got != nil {
		t.Fatalf("words from two messages: %v", got)
	}
	if got := search([]string{"bluelane"}); got != nil {
		t.Fatalf("no chats: %v", got)
	}

	// rewritten: the page starts the chat over
	if err := d.PutSearchItems(ctx, "server", "trip", "h2", 1, []SearchItemRow{row(1, "user", "重新开始")}, true); err != nil {
		t.Fatal(err)
	}
	if got := search([]string{"bluelane"}, "trip"); got != nil {
		t.Fatalf("after a reset: %v", got)
	}
	if err := d.DropSearchConversation(ctx, "server", "old"); err != nil {
		t.Fatal(err)
	}
	if got := search([]string{"bluelane"}, "old"); got != nil {
		t.Fatalf("after dropping: %v", got)
	}
	if err := d.PurgeSearch(ctx); err != nil {
		t.Fatal(err)
	}
	if cursors, _ := d.SearchCursors(ctx); len(cursors) != 0 {
		t.Fatalf("after purging: %+v", cursors)
	}

	// a reset (search-reindex, #6340): emptied like a purge, and counted, so a running manager lets go of its cursors
	if n, err := d.SearchReset(ctx); err != nil || n != 0 {
		t.Fatalf("resets before the first: %d, %v", n, err)
	}
	if err := d.PutSearchItems(ctx, "server", "trip", "h2", 1, []SearchItemRow{row(1, "user", "bluelane once more")}, false); err != nil {
		t.Fatal(err)
	}
	for want := int64(1); want <= 2; want++ {
		if err := d.ResetSearch(ctx); err != nil {
			t.Fatal(err)
		}
		if n, err := d.SearchReset(ctx); err != nil || n != want {
			t.Fatalf("resets %d (%v), want %d", n, err, want)
		}
	}
	if got := search([]string{"bluelane"}, "trip"); got != nil {
		t.Fatalf("after a reset: %v", got)
	}
	if cursors, _ := d.SearchCursors(ctx); len(cursors) != 0 {
		t.Fatalf("cursors after a reset: %+v", cursors)
	}
	if err := d.PurgeSearch(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.SearchReset(ctx); n != 2 {
		t.Fatalf("a purge counted as a reset: %d", n)
	}
}

// 0513 (#7072) empties the index built before replies were kept without Markdown, and counts a reset as
// `assistant search-reindex` does, so a running manager reads every conversation again; over a database with no index
// it writes nothing (a new manager's own database stays empty until the move fills it).
func TestAssistantSearchPlainMigrationEmptiesTheIndex(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, freshDatabase(t), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sql, err := fs.ReadFile(migrations, "migrations/manager/0513_assistant_search_plain.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	if n, err := d.SearchReset(ctx); err != nil || n != 0 {
		t.Fatalf("no index, yet resets %d, %v", n, err)
	}
	row := SearchItemRow{Seq: 1, ItemID: "i1#0", Role: "assistant", TS: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC), Body: "**粗**", Folded: "**粗**"}
	if err := d.PutSearchItems(ctx, "server", "trip", "h1", 1, []SearchItemRow{row}, false); err != nil {
		t.Fatal(err)
	}
	before, err := d.SearchReset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	cursors, err := d.SearchCursors(ctx)
	if err != nil || len(cursors) != 0 {
		t.Fatalf("cursors %+v, %v", cursors, err)
	}
	if hits, err := d.SearchAssistantItems(ctx, []string{"server"}, []string{"trip"}, []string{"粗"}, 5); err != nil || len(hits) != 0 {
		t.Fatalf("hits %+v, %v", hits, err)
	}
	if n, err := d.SearchReset(ctx); err != nil || n != before+1 {
		t.Fatalf("resets %d (before %d), %v", n, before, err)
	}
}
