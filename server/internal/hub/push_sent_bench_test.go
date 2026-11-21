package hub

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
)

// BenchmarkPushRecordSent is one delivered push recorded with the full 2,000-entry record, on Postgres, saved before
// the next (#8257). WAL-B/op is the write-ahead log the push costs the database: run it alone, since the WAL is the
// whole server's. It reads the database only through SQL, so the same file measures a build from before the table.
func BenchmarkPushRecordSent(b *testing.B) {
	url := os.Getenv(dbtest.Env)
	if url == "" {
		b.Skip(dbtest.Env + " not set")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, url, db.HubSet)
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `DELETE FROM hub_settings WHERE key = 'hub.push_sent';
		DO $$ BEGIN IF to_regclass('hub_push_sent') IS NOT NULL THEN DELETE FROM hub_push_sent; END IF; END $$`); err != nil {
		b.Fatal(err)
	}
	now := time.Now().UTC()
	seed := make([]pushSent, pushSentMax)
	for i := range seed {
		seed[i] = pushSent{At: now.Add(-time.Duration(pushSentMax-i) * time.Minute), Level: "p1",
			RelatedKey: fmt.Sprintf("session:0e000000-0000-4000-8000-%012d", i), Manager: "manager-1"}
	}
	if err := database.PutSetting(ctx, pushSentSettingKey, seed); err != nil {
		b.Fatal(err)
	}
	p := newPusher(b.TempDir(), database, b.Logf)
	p.gradingMu.Lock()
	loaded := len(p.sent)
	p.gradingMu.Unlock()
	if loaded != pushSentMax {
		b.Fatalf("loaded %d delivered pushes, want %d", loaded, pushSentMax)
	}
	lsn := func() (n int64) {
		if err := conn.QueryRow(ctx, `SELECT pg_wal_lsn_diff(pg_current_wal_insert_lsn(), '0/0')::bigint`).Scan(&n); err != nil {
			b.Fatal(err)
		}
		return n
	}
	b.ReportAllocs()
	b.ResetTimer()
	start := lsn()
	for i := range b.N {
		p.recordSent(pushPolicy{relatedKey: fmt.Sprintf("session:%d", i), manager: "manager-1"}, "p1", time.Now())
		if !p.waitInterceptsSaved(10 * time.Second) {
			b.Fatal("the delivered push was not saved")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(lsn()-start)/float64(b.N), "WAL-B/op")
}
