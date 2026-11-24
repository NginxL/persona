package db

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"
)

func intakeRow(n int) HealthIntakeRow {
	return HealthIntakeRow{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", n), Kind: "water", Amount: 250,
		At: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC), Note: "Synthetic entry"}
}

func TestHealthIntakeMigrationOwnsSequence(t *testing.T) {
	files, err := fs.Glob(migrations, "migrations/manager/*_health_intake.sql")
	if err != nil || len(files) != 1 {
		t.Fatalf("migration: %v %v", files, err)
	}
	b, err := migrations.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ALTER SEQUENCE health_intake_seq OWNED BY health_intake.seq;",
		"CREATE UNIQUE INDEX IF NOT EXISTS health_intake_seq_idx ON health_intake (seq);",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("migration lacks %s", want)
		}
	}
	if !slices.Contains(ManagerTables(), "health_intake") || slices.Contains(HubTables(), "health_intake") {
		t.Fatal("health_intake belongs only to the manager's migration set")
	}
}

func TestHealthIntakeIdempotentAddAndUndo(t *testing.T) {
	d, ctx := open(t), context.Background()
	v := intakeRow(1)
	first, changed, err := d.HealthIntakeAdd(ctx, v)
	if err != nil || !changed || first.Seq <= 0 || first.CreatedAt.IsZero() {
		t.Fatalf("add: %+v %v %v", first, changed, err)
	}
	v.Kind, v.Amount, v.Note, v.At = "protein", 30.5, "Changed retry", v.At.Add(time.Hour)
	retry, changed, err := d.HealthIntakeAdd(ctx, v)
	if err != nil || changed || retry.ID != first.ID || retry.Kind != first.Kind || retry.Amount != first.Amount || retry.Note != first.Note || !retry.At.Equal(first.At) || retry.Seq != first.Seq {
		t.Fatalf("retry changed the record: %+v %v %v", retry, changed, err)
	}
	undo, changed, err := d.HealthIntakeUndo(ctx, v.ID)
	if err != nil || !changed || undo == nil || undo.DeletedAt == nil || undo.Seq <= first.Seq || undo.Amount != first.Amount || !undo.At.Equal(first.At) {
		t.Fatalf("undo: %+v %v %v", undo, changed, err)
	}
	again, changed, err := d.HealthIntakeUndo(ctx, v.ID)
	if err != nil || changed || again.Seq != undo.Seq || !again.DeletedAt.Equal(*undo.DeletedAt) {
		t.Fatalf("repeated undo: %+v %v %v", again, changed, err)
	}
	retry, changed, err = d.HealthIntakeAdd(ctx, v)
	if err != nil || changed || retry.DeletedAt == nil || retry.Seq != undo.Seq {
		t.Fatalf("retry revived the record: %+v %v %v", retry, changed, err)
	}
	if missing, changed, err := d.HealthIntakeUndo(ctx, intakeRow(99).ID); err != nil || missing != nil || changed {
		t.Fatalf("missing: %+v %v %v", missing, changed, err)
	}
	rows, err := d.HealthIntakeRange(ctx, first.At.Add(-time.Hour), first.At.Add(time.Hour))
	if err != nil || len(rows) != 0 {
		t.Fatalf("undone entry in totals: %+v %v", rows, err)
	}
}

func TestHealthIntakeChangesPagingAndRange(t *testing.T) {
	d, ctx := open(t), context.Background()
	var saved []HealthIntakeRow
	for i := 1; i <= 4; i++ {
		v := intakeRow(i)
		v.At = v.At.Add(time.Duration(i-1) * time.Hour)
		got, _, err := d.HealthIntakeAdd(ctx, v)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, got)
	}
	rows, err := d.HealthIntakeRange(ctx, saved[0].At, saved[2].At)
	if err != nil || len(rows) != 2 || rows[0].ID != saved[1].ID || rows[1].ID != saved[0].ID {
		t.Fatalf("range must be [from, until), newest first: %+v %v", rows, err)
	}
	page, err := d.HealthIntakeChanges(ctx, 0, 2)
	if err != nil || len(page) != 2 || page[0].ID != saved[0].ID || page[1].ID != saved[1].ID {
		t.Fatalf("first page: %+v %v", page, err)
	}
	undo, _, err := d.HealthIntakeUndo(ctx, saved[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	page2, err := d.HealthIntakeChanges(ctx, page[1].Seq, 2)
	if err != nil || len(page2) != 2 || page2[0].ID != saved[2].ID || page2[1].ID != saved[3].ID {
		t.Fatalf("second page: %+v %v", page2, err)
	}
	page3, err := d.HealthIntakeChanges(ctx, page2[1].Seq, 2)
	if err != nil || len(page3) != 1 || page3[0].Seq != undo.Seq || page3[0].DeletedAt == nil {
		t.Fatalf("third page must carry the undo: %+v %v", page3, err)
	}
	if end, err := d.HealthIntakeChanges(ctx, page3[0].Seq, 2); err != nil || len(end) != 0 {
		t.Fatalf("last cursor: %+v %v", end, err)
	}
}

func TestHealthIntakeRangeIncludesBothKinds(t *testing.T) {
	d, ctx := open(t), context.Background()
	water, protein := intakeRow(1), intakeRow(2)
	protein.Kind, protein.Amount, protein.At = "protein", 0.1, water.At.Add(time.Minute)
	for _, row := range []HealthIntakeRow{water, protein} {
		if _, _, err := d.HealthIntakeAdd(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.HealthIntakeRange(ctx, water.At, protein.At.Add(time.Second))
	if err != nil || len(rows) != 2 || rows[0].Kind != "protein" || rows[1].Kind != "water" {
		t.Fatalf("both kinds in range: %+v %v", rows, err)
	}
	if _, _, err := d.HealthIntakeUndo(ctx, protein.ID); err != nil {
		t.Fatal(err)
	}
	rows, err = d.HealthIntakeRange(ctx, water.At, protein.At.Add(time.Second))
	if err != nil || len(rows) != 1 || rows[0].ID != water.ID {
		t.Fatalf("undone kind leaked into range: %+v %v", rows, err)
	}
}

func TestHealthIntakeTruncateForTest(t *testing.T) {
	d, ctx := open(t), context.Background()
	for i := 1; i <= 2; i++ {
		if _, _, err := d.HealthIntakeAdd(ctx, intakeRow(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := d.HealthIntakeUndo(ctx, intakeRow(1).ID); err != nil {
		t.Fatal(err)
	}
	if err := d.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := d.HealthIntakeChanges(ctx, 0, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("test reset left active rows or tombstones: %+v %v", rows, err)
	}
	if _, changed, err := d.HealthIntakeAdd(ctx, intakeRow(1)); err != nil || !changed {
		t.Fatalf("test reset did not empty intake: changed=%v, %v", changed, err)
	}
}

// Hold the writers' lock before starting both an add and an undo. Neither may consume a sequence
// before acquiring it. After releasing it, a cursor advanced while writes run must see every row.
func TestHealthIntakeConcurrentCommitOrder(t *testing.T) {
	d, ctx := open(t), context.Background()
	seed, _, err := d.HealthIntakeAdd(ctx, intakeRow(1))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('health_intake', 0))`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		seq int64
		err error
	}
	done := make(chan result, 2)
	go func() {
		v, _, err := d.HealthIntakeAdd(ctx, intakeRow(2))
		done <- result{v.Seq, err}
	}()
	go func() {
		v, _, err := d.HealthIntakeUndo(ctx, seed.ID)
		var seq int64
		if v != nil {
			seq = v.Seq
		}
		done <- result{seq, err}
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		var waiting int
		if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event = 'advisory' AND pid <> pg_backend_pid()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("both intake writers did not wait for the transaction lock")
		case <-poll.C:
		}
	}
	var last int64
	if err := d.pool.QueryRow(ctx, `SELECT last_value FROM health_intake_seq`).Scan(&last); err != nil || last != seed.Seq {
		t.Fatalf("a waiting writer allocated a sequence: %d, want %d: %v", last, seed.Seq, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := seed.Seq
	for len(seen) < 2 {
		page, err := d.HealthIntakeChanges(ctx, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 0 {
			if page[0].Seq <= cursor {
				t.Fatal("cursor went backwards")
			}
			cursor, seen[page[0].ID] = page[0].Seq, true
		}
		select {
		case <-deadline.C:
			t.Fatalf("cursor missed a concurrent write: %v", seen)
		case <-poll.C:
		}
	}
	for range 2 {
		if result := <-done; result.err != nil || result.seq <= seed.Seq {
			t.Fatalf("writer: %+v", result)
		}
	}
	if !seen[seed.ID] || !seen[intakeRow(2).ID] {
		t.Fatalf("changes: %v", seen)
	}
}

func TestHealthIntakeSequenceAfterCopyTables(t *testing.T) {
	ctx := context.Background()
	from, err := Open(ctx, migratedDatabase(t, AllSets), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer from.Close()
	to, err := Open(ctx, migratedDatabase(t, ManagerSet), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer to.Close()
	first, _, err := from.HealthIntakeAdd(ctx, intakeRow(1))
	if err != nil {
		t.Fatal(err)
	}
	undo, _, err := from.HealthIntakeUndo(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Gaps from idempotent retries must survive the copy too, not only max(seq).
	if _, _, err := from.HealthIntakeAdd(ctx, intakeRow(1)); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := from.pool.QueryRow(ctx, `SELECT last_value FROM health_intake_seq`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyTables(ctx, from, to, t.Logf); err != nil {
		t.Fatal(err)
	}
	next, changed, err := to.HealthIntakeAdd(ctx, intakeRow(2))
	if err != nil || !changed || next.Seq <= before || next.Seq <= undo.Seq {
		t.Fatalf("sequence went back after the copy: %+v, before %d: %v", next, before, err)
	}
	rows, err := to.HealthIntakeChanges(ctx, 0, 10)
	if err != nil || len(rows) != 2 || rows[0].ID != first.ID || rows[0].DeletedAt == nil {
		t.Fatalf("copy lost the tombstone: %+v %v", rows, err)
	}
}
