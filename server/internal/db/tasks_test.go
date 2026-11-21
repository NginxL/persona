package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// A message card's sent mark: set once on the cards about the item put in by then, kept, and listed again to a
// client that asks for what came since (its time does not change, its body does).
func TestFeedMarkSent(t *testing.T) {
	d, ctx := open(t), context.Background()
	t0 := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	add := func(id, kind, ref string, at time.Time) {
		t.Helper()
		if err := d.AppendFeed(ctx, FeedRow{ID: id, Conversation: "c1", At: at, Kind: kind, Ref: ref, Body: json.RawMessage(`{"draft":"在修了"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	add("f1", "message", "inbox:1", t0)
	add("f2", "task", "inbox:1", t0.Add(time.Minute))
	add("f3", "message", "inbox:2", t0.Add(2*time.Minute))
	add("f4", "message", "inbox:1", t0.Add(30*time.Minute)) // a later draft

	sent := t0.Add(10 * time.Minute)
	patch := json.RawMessage(`{"sent_at":"` + sent.Format(time.RFC3339Nano) + `","sent":"修好了"}`)
	rows, err := d.MarkFeedSent(ctx, "message", "inbox:1", sent, patch)
	if err != nil || len(rows) != 1 || rows[0].ID != "f1" {
		t.Fatalf("MarkFeedSent = %+v, %v", rows, err)
	}
	var b map[string]string
	_ = json.Unmarshal(rows[0].Body, &b)
	if b["draft"] != "在修了" || b["sent"] != "修好了" || b["sent_at"] == "" {
		t.Fatalf("body = %s", rows[0].Body)
	}
	// Marked once: a second send does not move it.
	again := json.RawMessage(`{"sent_at":"` + t0.Add(40*time.Minute).Format(time.RFC3339Nano) + `","sent":"别的"}`)
	if rows, err := d.MarkFeedSent(ctx, "message", "inbox:1", t0.Add(20*time.Minute), again); err != nil || len(rows) != 0 {
		t.Fatalf("marked again: %+v, %v", rows, err)
	}
	if r, err := d.FeedItem(ctx, "f1"); err != nil || r == nil || r.Kind != "message" || !json.Valid(r.Body) ||
		!strings.Contains(string(r.Body), `"sent": "修好了"`) {
		t.Fatalf("FeedItem = %+v (%s), %v", r, r.Body, err)
	}
	if r, err := d.FeedItem(ctx, "nope"); err != nil || r != nil {
		t.Fatalf("FeedItem(nope) = %+v, %v", r, err)
	}

	// A client that has everything up to f3 gets f4 and the card sent after f3 came.
	list, err := d.Feed(ctx, "c1", t0.Add(2*time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range list {
		ids = append(ids, r.ID)
	}
	if len(ids) != 2 || ids[0] != "f1" || ids[1] != "f4" {
		t.Fatalf("since f3: %v", ids)
	}
	if list, _ := d.Feed(ctx, "c1", t0.Add(30*time.Minute), 100); len(list) != 0 {
		t.Fatalf("since f4: %+v", list)
	}
}

// MarkFeedSent finds the unsent cards through assistant_feed_unsent rather than scanning the whole feed (#2617).
// Which index the planner takes is only worth checking on a feed shaped like a real one: on the few rows other tests
// leave behind, a full scan of the small assistant_feed_sent costs about as little and wins once their leftovers tip
// it. So this runs in a database of its own, on a few thousand synthetic cards, analyzed, at the planner's default
// costs, and checks the plan a statement starts with (custom) and the one a cached statement can switch to (generic).
func TestMarkFeedSentUsesUnsentIndex(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, migratedDatabase(t, AllSets), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	// 8000 cards about 2000 items, one of each kind per item; every other message card is sent.
	if _, err := d.pool.Exec(ctx, `INSERT INTO assistant_feed (id, conversation, at, kind, ref, body, sent_at)
		SELECT 'f' || n, 'c' || n % 20, $1::timestamptz + n * interval '1 second',
			(ARRAY['message', 'task', 'plan', 'receipt'])[1 + n % 4], 'inbox:' || n / 4,
			CASE WHEN n % 8 = 0 THEN jsonb_build_object('sent_at', $1::timestamptz) ELSE '{}' END,
			CASE WHEN n % 8 = 0 THEN $1::timestamptz END
		FROM generate_series(0, 7999) n`, time.Now().UTC().Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `ANALYZE assistant_feed`); err != nil {
		t.Fatal(err)
	}
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `PREPARE mark_feed_sent (text, text, timestamptz, jsonb) AS `+markFeedSentSQL); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := conn.Exec(ctx, `SET plan_cache_mode = `+mode); err != nil {
			t.Fatal(err)
		}
		rows, err := conn.Query(ctx, `EXPLAIN EXECUTE mark_feed_sent ('message', 'inbox:1', now(), '{"sent_at": "x"}')`)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(plan, "\n"), "assistant_feed_unsent") {
			t.Fatalf("%s: MarkFeedSent does not use assistant_feed_unsent:\n%s", mode, strings.Join(plan, "\n"))
		}
	}
}

// FeedByRef (#10465, a to-do's history): the items of the kinds asked about the ref, in any conversation, newest first
// (equal times by id), at most limit; never message cards, which it refuses.
func TestFeedByRef(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, err := d.pool.Exec(ctx, `TRUNCATE assistant_feed`); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	add := func(id, conversation, kind, ref string, at time.Time) {
		t.Helper()
		if err := d.AppendFeed(ctx, FeedRow{ID: id, Conversation: conversation, At: at, Kind: kind, Ref: ref,
			Body: json.RawMessage(`{"text":"` + id + `"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	add("r1", "c1", "routed", "todo_1", t0)
	add("p1", "c1", "receipt", "todo_1", t0.Add(time.Minute))
	add("p2", "c2", "receipt", "todo_1", t0.Add(2*time.Minute)) // another conversation's (a supervisor's)
	add("p3", "", "receipt", "todo_1", t0.Add(2*time.Minute))   // equal time: by id, newest first
	add("s1", "c1", "start_remedy", "todo_1", t0.Add(3*time.Minute))
	add("o1", "c1", "receipt", "todo_2", t0.Add(4*time.Minute))
	add("m1", "c1", "message", "todo_1", t0.Add(5*time.Minute))
	ids := func(kinds []string, limit int) string {
		t.Helper()
		rows, err := d.FeedByRef(ctx, "todo_1", kinds, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			if r.Ref != "todo_1" || !json.Valid(r.Body) || r.At.IsZero() {
				t.Fatalf("row %+v", r)
			}
			out = append(out, r.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids([]string{"routed", "receipt"}, 100); got != "p3,p2,p1,r1" {
		t.Fatalf("routed and receipts: %s", got)
	}
	if got := ids([]string{"routed", "receipt"}, 2); got != "p3,p2" {
		t.Fatalf("limit 2: %s", got)
	}
	if got := ids([]string{"start_remedy"}, 100); got != "s1" {
		t.Fatalf("start_remedy: %s", got)
	}
	if got := ids(nil, 100); got != "" {
		t.Fatalf("no kinds: %s", got)
	}
	if rows, err := d.FeedByRef(ctx, "todo_1", []string{"receipt", "message"}, 100); !errors.Is(err, ErrFeedByRefMessage) || rows != nil {
		t.Fatalf("message cards: %+v, %v", rows, err)
	}
}

// FeedByRef reads a to-do's items through assistant_feed_unsent, as MarkFeedSent does (its predicate is the index's):
// no migration, and no scan of the whole feed on a to-do's read. Checked as TestMarkFeedSentUsesUnsentIndex checks
// MarkFeedSent, on a feed shaped like a real one, for the plan a statement starts with and the one it can switch to.
func TestFeedByRefUsesUnsentIndex(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, migratedDatabase(t, AllSets), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	// 8000 items about 2000 to-dos and inbox items, one of each kind per ref; every other message card is sent.
	if _, err := d.pool.Exec(ctx, `INSERT INTO assistant_feed (id, conversation, at, kind, ref, body, sent_at)
		SELECT 'f' || n, 'c' || n % 20, $1::timestamptz + n * interval '1 second',
			(ARRAY['message', 'task', 'routed', 'receipt'])[1 + n % 4], 'todo_' || n / 4,
			CASE WHEN n % 8 = 0 THEN jsonb_build_object('sent_at', $1::timestamptz) ELSE '{}' END,
			CASE WHEN n % 8 = 0 THEN $1::timestamptz END
		FROM generate_series(0, 7999) n`, time.Now().UTC().Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `ANALYZE assistant_feed`); err != nil {
		t.Fatal(err)
	}
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `PREPARE feed_by_ref (text[], text, int) AS `+feedByRefSQL); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := conn.Exec(ctx, `SET plan_cache_mode = `+mode); err != nil {
			t.Fatal(err)
		}
		rows, err := conn.Query(ctx, `EXPLAIN EXECUTE feed_by_ref ('{routed,receipt,task}', 'todo_1', 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(plan, "\n"), "assistant_feed_unsent") {
			t.Fatalf("%s: FeedByRef does not use assistant_feed_unsent:\n%s", mode, strings.Join(plan, "\n"))
		}
	}
}

// FeedConversations (#10941): the conversations each ref's items of the kinds asked are in, each once and in order,
// however many items; a ref with none left out; never message cards, which it refuses.
func TestFeedConversations(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, err := d.pool.Exec(ctx, `TRUNCATE assistant_feed`); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for i, f := range []struct{ conversation, kind, ref string }{
		{"c2", "routed", "todo_1"}, {"c1", "receipt", "todo_1"}, {"c1", "routed", "todo_1"}, {"", "receipt", "todo_1"},
		{"c3", "start_remedy", "todo_1"}, {"c4", "message", "todo_1"}, {"c1", "receipt", "todo_2"}, {"c5", "receipt", "todo_3"},
	} {
		if err := d.AppendFeed(ctx, FeedRow{ID: fmt.Sprint("f", i), Conversation: f.conversation, At: t0.Add(time.Duration(i) * time.Minute),
			Kind: f.kind, Ref: f.ref, Body: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.FeedConversations(ctx, []string{"todo_1", "todo_2", "todo_9"}, []string{"routed", "receipt"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string][]string{"todo_1": {"", "c1", "c2"}, "todo_2": {"c1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("conversations %v, want %v", got, want)
	}
	if got, err := d.FeedConversations(ctx, nil, []string{"routed"}); err != nil || len(got) != 0 {
		t.Fatalf("no refs: %v, %v", got, err)
	}
	if got, err := d.FeedConversations(ctx, []string{"todo_1"}, []string{"receipt", "message"}); !errors.Is(err, ErrFeedByRefMessage) || got != nil {
		t.Fatalf("message cards: %v, %v", got, err)
	}
}

// FeedConversations reads through assistant_feed_unsent as FeedByRef does, for the to-dos of a list read.
func TestFeedConversationsUsesUnsentIndex(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, migratedDatabase(t, AllSets), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if _, err := d.pool.Exec(ctx, `INSERT INTO assistant_feed (id, conversation, at, kind, ref, body, sent_at)
		SELECT 'f' || n, 'c' || n % 20, $1::timestamptz + n * interval '1 second',
			(ARRAY['message', 'task', 'routed', 'receipt'])[1 + n % 4], 'todo_' || n / 4,
			CASE WHEN n % 8 = 0 THEN jsonb_build_object('sent_at', $1::timestamptz) ELSE '{}' END,
			CASE WHEN n % 8 = 0 THEN $1::timestamptz END
		FROM generate_series(0, 7999) n`, time.Now().UTC().Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `ANALYZE assistant_feed`); err != nil {
		t.Fatal(err)
	}
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `PREPARE feed_conversations (text[], text[]) AS `+feedConversationsSQL); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := conn.Exec(ctx, `SET plan_cache_mode = `+mode); err != nil {
			t.Fatal(err)
		}
		rows, err := conn.Query(ctx, `EXPLAIN EXECUTE feed_conversations ('{routed,receipt,task}', '{todo_1,todo_7,todo_40,todo_300}')`)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(plan, "\n"), "assistant_feed_unsent") {
			t.Fatalf("%s: FeedConversations does not use assistant_feed_unsent:\n%s", mode, strings.Join(plan, "\n"))
		}
	}
}

// feedIDs is the ids Feed lists for c1 since since, at most limit.
func feedIDs(t *testing.T, d *DB, since time.Time, limit int) []string {
	t.Helper()
	list, err := d.Feed(context.Background(), "c1", since, limit)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range list {
		ids = append(ids, r.ID)
	}
	return ids
}

// The cards sent after since and the items put in after it come in one list by time, each once, cut at limit; a card
// put in with sent_at already in its body counts as sent too.
func TestFeedSentAndNewInOrder(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, err := d.pool.Exec(ctx, `TRUNCATE assistant_feed`); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	add := func(id, kind string, at time.Time, body string) {
		t.Helper()
		if err := d.AppendFeed(ctx, FeedRow{ID: id, Conversation: "c1", At: at, Kind: kind, Ref: "inbox:" + id, Body: json.RawMessage(body)}); err != nil {
			t.Fatal(err)
		}
	}
	sentAt := func(at time.Time) string { return `{"sent_at":"` + at.Format(time.RFC3339Nano) + `"}` }
	since := t0.Add(10 * time.Minute)
	add("a", "message", t0, `{}`)                                             // sent after since below
	add("b", "message", t0.Add(time.Minute), sentAt(since.Add(time.Minute)))  // put in already sent, after since
	add("c", "message", t0.Add(2*time.Minute), sentAt(t0.Add(3*time.Minute))) // sent before since
	add("d", "task", t0.Add(3*time.Minute), sentAt(since.Add(time.Minute)))   // not a card
	add("e", "task", since.Add(time.Minute), `{}`)
	add("f", "message", since.Add(2*time.Minute), `{}`)
	add("g", "task", since.Add(3*time.Minute), `{}`)
	if _, err := d.MarkFeedSent(ctx, "message", "inbox:a", since.Add(5*time.Minute), json.RawMessage(sentAt(since.Add(5*time.Minute)))); err != nil {
		t.Fatal(err)
	}
	// f is sent after since and put in after it: listed once.
	if _, err := d.MarkFeedSent(ctx, "message", "inbox:f", since.Add(6*time.Minute), json.RawMessage(sentAt(since.Add(6*time.Minute)))); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(feedIDs(t, d, since, 100), ","); got != "a,b,e,f,g" {
		t.Fatalf("since: %s", got)
	}
	if got := strings.Join(feedIDs(t, d, since, 3), ","); got != "a,b,e" {
		t.Fatalf("since, limit 3: %s", got)
	}
	if got := strings.Join(feedIDs(t, d, time.Time{}, 4), ","); got != "a,b,c,d" {
		t.Fatalf("from the start, limit 4: %s", got)
	}
}

// A database from before 0390 keeps sent_at only in the bodies: the migration copies it to the column, so its sent
// cards are still listed.
func TestFeedSentAtBackfill(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, err := d.pool.Exec(ctx, `TRUNCATE assistant_feed`); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	since := t0.Add(10 * time.Minute)
	for _, q := range []string{
		`DROP INDEX assistant_feed_sent`,
		`ALTER TABLE assistant_feed DROP COLUMN sent_at`,
		`DELETE FROM schema_migrations WHERE version = 390`,
	} {
		if _, err := d.pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, r := range []struct {
		id, kind string
		at       time.Time
		body     string
	}{
		{"old-sent", "message", t0, `{"sent_at":"` + since.Add(time.Minute).Format(time.RFC3339Nano) + `"}`},
		{"old-early", "message", t0, `{"sent_at":"` + t0.Add(time.Minute).Format(time.RFC3339Nano) + `"}`},
		{"old-draft", "message", t0, `{"draft":"x"}`},
		{"old-task", "task", t0, `{"sent_at":"` + since.Add(time.Minute).Format(time.RFC3339Nano) + `"}`},
	} {
		if _, err := d.pool.Exec(ctx, `INSERT INTO assistant_feed (id, conversation, at, kind, ref, body) VALUES ($1, 'c1', $2, $3, '', $4)`,
			r.id, r.at, r.kind, []byte(r.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(feedIDs(t, d, since, 100), ","); got != "old-sent" {
		t.Fatalf("after backfill: %s", got)
	}
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM assistant_feed WHERE sent_at IS NOT NULL`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("backfilled %d, %v", n, err)
	}
}

// TaskPlansByID reads several plans in one query: no ids ask nothing (a DB without a pool would panic on a query), an
// unknown id is left out and a repeated one gives one row.
func TestTaskPlansByID(t *testing.T) {
	ctx := context.Background()
	if rows, err := (&DB{}).TaskPlansByID(ctx, nil); err != nil || rows != nil {
		t.Fatalf("no ids: %+v, %v", rows, err)
	}
	d := open(t)
	prefix := fmt.Sprintf("plan_byid_%d", time.Now().UnixNano()) // task_plans is not truncated between tests
	at := time.Now().UTC().Truncate(time.Microsecond)
	want := map[string]TaskPlanRow{}
	for i := range 3 {
		p := TaskPlanRow{ID: fmt.Sprintf("%s_%d", prefix, i), Conversation: prefix, Tasks: json.RawMessage(fmt.Sprintf(`[{"title": "Task %d"}]`, i)),
			State: "open", CreatedAt: at, UpdatedAt: at.Add(time.Duration(i) * time.Second)}
		if err := d.CreateTaskPlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		want[p.ID] = p
	}
	ids := []string{prefix + "_2", prefix + "_nope", prefix + "_0", prefix + "_2"}
	rows, err := d.TaskPlansByID(ctx, ids)
	if err != nil || len(rows) != 2 {
		t.Fatalf("TaskPlansByID = %+v, %v", rows, err)
	}
	for _, r := range rows {
		w, ok := want[r.ID]
		one, err := d.TaskPlan(ctx, r.ID)
		if !ok || r.ID == prefix+"_1" || err != nil || r.Conversation != w.Conversation || string(r.Tasks) != string(w.Tasks) ||
			r.State != w.State || !r.CreatedAt.Equal(w.CreatedAt) || !r.UpdatedAt.Equal(w.UpdatedAt) ||
			string(one.Tasks) != string(r.Tasks) || !one.UpdatedAt.Equal(r.UpdatedAt) {
			t.Fatalf("row %+v, want %+v (TaskPlan %+v, %v)", r, w, one, err)
		}
	}
	if rows[0].ID == rows[1].ID {
		t.Fatalf("a repeated id read twice: %+v", rows)
	}
	if rows, err := d.TaskPlansByID(ctx, []string{prefix + "_nope"}); err != nil || len(rows) != 0 {
		t.Fatalf("unknown ids: %+v, %v", rows, err)
	}
}

// TaskPlansMadeSince is the plans made inside the window that were started or dismissed, oldest first; an open plan
// is no answer yet.
func TestTaskPlansMadeSince(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	prefix := fmt.Sprintf("plan_since_%d", time.Now().UnixNano()) // task_plans is not truncated between tests
	// Far ahead of every other test's plans, so only these are in the window.
	base := time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)
	for i, state := range []string{"started", "started", "open", "dismissed", "started"} {
		at := base.Add(time.Duration(i-1) * time.Minute) // the first is a minute before the window
		p := TaskPlanRow{ID: fmt.Sprintf("%s_%d", prefix, i), Conversation: prefix, State: state, CreatedAt: at, UpdatedAt: at.Add(time.Hour),
			Tasks: json.RawMessage(fmt.Sprintf(`[{"title": "Task %d", "owner_started": true}]`, i))}
		if err := d.CreateTaskPlan(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.TaskPlansMadeSince(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		if r.Conversation == prefix {
			got = append(got, strings.TrimPrefix(r.ID, prefix+"_")+":"+r.State)
		}
	}
	if strings.Join(got, ",") != "1:started,3:dismissed,4:started" {
		t.Fatalf("since = %v", got)
	}
	for _, r := range rows {
		if r.ID == prefix+"_1" && (!r.CreatedAt.Equal(base) || !strings.Contains(string(r.Tasks), `"owner_started"`)) {
			t.Fatalf("row = %+v", r)
		}
	}

	// The queue uses the update window plus durable queue records: the first plan was created before base but updated
	// inside it. A stale serialized queue survives a long outage; a stale plan without one stays excluded. The open plan
	// is still excluded, and MadeSince above retains its independent creation-window result.
	for _, suffix := range []string{"stale_queued", "stale_plain"} {
		tasks := json.RawMessage(`[{"title": "Stale"}]`)
		if suffix == "stale_queued" {
			tasks = json.RawMessage(`[{"title": "Stale", "queue": {}}]`)
		}
		p := TaskPlanRow{ID: prefix + "_" + suffix, Conversation: prefix, State: "started", CreatedAt: base.Add(-2 * time.Hour),
			UpdatedAt: base.Add(-2 * time.Hour), Tasks: tasks}
		if err := d.CreateTaskPlan(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = d.TaskPlansQueued(ctx, base.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, r := range rows {
		if r.Conversation == prefix {
			got = append(got, strings.TrimPrefix(r.ID, prefix+"_")+":"+r.State)
		}
	}
	if strings.Join(got, ",") != "stale_queued:started,0:started,1:started,3:dismissed,4:started" {
		t.Fatalf("queue candidates = %v", got)
	}
}

func TestOpenTaskPlans(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("pending_%d", time.Now().UnixNano())
	at := time.Now().UTC().Truncate(time.Microsecond)
	for i, state := range []string{"open", "started", "dismissed", "open"} {
		p := TaskPlanRow{ID: fmt.Sprintf("%s_%d", prefix, i), Conversation: fmt.Sprintf("%s_chat_%d", prefix, i),
			Tasks: json.RawMessage(`[]`), State: state, CreatedAt: at, UpdatedAt: at}
		if err := d.CreateTaskPlan(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.OpenTaskPlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range rows {
		if strings.HasPrefix(row.ID, prefix) {
			ids = append(ids, row.ID)
		}
		if row.State != "open" {
			t.Fatalf("closed plan returned: %+v", row)
		}
	}
	if !reflect.DeepEqual(ids, []string{prefix + "_0", prefix + "_3"}) {
		t.Fatalf("open plans: %v", ids)
	}
}
