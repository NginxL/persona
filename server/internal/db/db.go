// Package db is the hub's Postgres store (docker container persona-postgres on the hub machine, see docs/server/server.md).
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The migrations are two sets (docs/server/server.md "Migrations"): the hub's tables in migrations/hub, the manager's in
// migrations/manager. A hub's database runs the hub set, a manager's the manager set; while the default manager
// shares the hub's database, both run there, each recording its own part.
//
//go:embed migrations/hub/*.sql migrations/manager/*.sql
var migrations embed.FS

// Sets says which migrations a database gets (and so which side's tables it has).
type Sets uint8

const (
	// HubSet is the hub's own tables: connections, devices and their tokens, pairing, push delivery, peers and the
	// mesh, the admin page, the managers' roster and bus identities.
	HubSet Sets = 1 << iota
	// ManagerSet is a manager's: the assistant, inbox, knowledge base, todos, boards, extensions, the computers'
	// sessions and metrics.
	ManagerSet
	// AllSets is both, the hub's first: a test that uses either side's tables.
	AllSets = HubSet | ManagerSet
)

// setDirs are the sets' directories under migrations/, in the order they run.
var setDirs = []struct {
	set  Sets
	part string // what schema_migrations.part records
}{{HubSet, "hub"}, {ManagerSet, "manager"}}

type DB struct {
	pool *pgxpool.Pool
	sets Sets
	// olderHub: the hub's database is shared with the default manager's settings table, which a hub build from before
	// hub_settings reads (followOlderHub): the hub's keys are kept there too.
	olderHub  atomic.Bool
	olderMu   sync.Mutex // one syncOlderHub at a time
	closeOnce sync.Once
	closeDone chan struct{}
	// pids are the server processes of this pool's connections (OtherManagers tells them from another process's).
	pids *pidSet
}

// QueryRow runs one ordinary read on the existing pool. It carries no manager-owned types.
func (d *DB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return d.pool.QueryRow(ctx, query, args...)
}

// Transaction runs fn in one transaction on the existing pool.
func (d *DB) Transaction(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.pool, fn)
}

// pidSet is the backend process ids of a pool's open connections.
type pidSet struct {
	mu sync.Mutex
	m  map[uint32]bool
}

func (p *pidSet) add(pid uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[uint32]bool{}
	}
	p.m[pid] = true
}

func (p *pidSet) remove(pid uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, pid)
}

func (p *pidSet) list() []int32 {
	if p == nil {
		return []int32{} // not NULL: pid = ANY(NULL) would match nothing and hide every connection
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int32, 0, len(p.m))
	for pid := range p.m {
		out = append(out, int32(pid))
	}
	return out
}

// Open connects and applies the pending migrations of sets. It retries for a while so the hub can start alongside
// the container.
func Open(ctx context.Context, url string, sets Sets) (*DB, error) {
	if sets == 0 || sets&^AllSets != 0 {
		return nil, fmt.Errorf("db.Open: bad migration sets %d", sets)
	}
	pids := &pidSet{}
	pool, err := connectTracked(ctx, url, appName(sets), pids)
	if err != nil {
		return nil, err
	}
	d := &DB{pool: pool, sets: sets, pids: pids}
	if err := d.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if sets == HubSet {
		if err := d.followOlderHub(ctx); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return d, nil
}

// Attach connects the way Open does for sets, migrating nothing and taking up nothing (a tool that looks at a database
// another build owns: assistant drop-shared-tables).
func Attach(ctx context.Context, url string, sets Sets) (*DB, error) {
	pids := &pidSet{}
	pool, err := connectTracked(ctx, url, appName(sets), pids)
	if err != nil {
		return nil, err
	}
	return &DB{pool: pool, sets: sets, pids: pids}, nil
}

// Check connects the way Open does and lets go again, migrating nothing: a new hub binary waiting to take over
// (hubedge, a standby slot) checks it can reach the database while the one serving still owns the schema.
func Check(ctx context.Context, url string) error {
	pool, err := connect(ctx, url, appName(HubSet))
	if err != nil {
		return err
	}
	pool.Close()
	return nil
}

// appName is the application_name a part's connections give Postgres, so a part can tell the other's connections
// (and ones of a build from before, which give none) on a database they share (managerdb.go).
func appName(sets Sets) string {
	switch sets {
	case HubSet:
		return AppHub
	case ManagerSet:
		return AppManager
	}
	return "persona"
}

// AppHub and AppManager are the application_name of the hub's connections and a manager's.
const (
	AppHub     = "persona-hub"
	AppManager = "persona-manager"
)

func connect(ctx context.Context, url, app string) (*pgxpool.Pool, error) {
	return connectTracked(ctx, url, app, nil)
}

// connectTracked is connect, keeping the server process id of each connection in pids while it is open (nil: none).
func connectTracked(ctx context.Context, url, app string, pids *pidSet) (*pgxpool.Pool, error) {
	if url == "" {
		return nil, errors.New("hub 的数据库还没装好。运行 personad hub setup，它会帮你建好。")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database_url: %w", err)
	}
	if _, set := cfg.ConnConfig.RuntimeParams["application_name"]; !set && app != "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = app
	}
	cfg.MaxConns = 8
	cfg.AfterConnect = func(_ context.Context, c *pgx.Conn) error {
		utcTimes(c.TypeMap())
		if pids != nil {
			pids.add(c.PgConn().PID())
		}
		return nil
	}
	if pids != nil {
		cfg.BeforeClose = func(c *pgx.Conn) { pids.remove(c.PgConn().PID()) }
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = pool.Ping(pctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
		time.Sleep(time.Second)
	}
	return pool, nil
}

// utcTimes makes m read timestamptz values in UTC. pgx reads them in time.Local by default, which puts the hub
// machine's offset ("+08:00") into every time the protocol sends, where docs/protocol/protocol.md has UTC.
func utcTimes(m *pgtype.Map) {
	m.RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
}

func (d *DB) Close() { _ = d.CloseContext(context.Background()) }

// CloseContext stops new acquisitions and bounds waiting for borrowed connections.
// pgxpool has no forced close: outstanding borrowers may finish after ctx expires.
func (d *DB) CloseContext(ctx context.Context) error {
	d.closeOnce.Do(func() {
		d.closeDone = make(chan struct{})
		go func() {
			d.pool.Close()
			close(d.closeDone)
		}()
	})
	select {
	case <-d.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// migrate runs the pending migrations of the database's sets, in order, each in its own transaction, recording them
// in schema_migrations.
func (d *DB) migrate(ctx context.Context) error {
	return d.migrateFrom(ctx, migrations)
}

// renumbered are migrations a database may have recorded under the number they had before a merge gave them another
// one (two lines of work picked the same number). Such a record would make migrate skip the migration that has the old
// number now, and run the renumbered one again; so it is moved to the new number first, or dropped when the new number
// is recorded too. The renumbered migrations are idempotent; the ones that took over the old numbers never ran there.
var renumbered = []struct{ from, to string }{
	{"0071_pair_tokens.sql", "0111_pair_tokens.sql"},             // 0071 is sent_messages
	{"0081_device_push_relay.sql", "0141_device_push_relay.sql"}, // 0081 is device_sessions
	{"0428_todo_source.sql", "0429_todo_source.sql"},             // 0428 is manager_moved
	{"0492_client_password.sql", "0505_client_password.sql"},     // 0492 is the manager's agent_shares
}

// lastUnified is the last migration from before the split: a NULL-part row up to it stands for both sets.
const lastUnified = 419

// migrateFrom is migrate over the migrations/hub and migrations/manager directories of fsys (tests start from a
// database at an older state).
//
// schema_migrations has a row per migration a part applied: part is "hub" or "manager", NULL for the rows from before
// the split, when one set ran everything (such a row stands for both parts). A version is done for a set when it has a
// row of that part, or a NULL one from up to lastUnified (or, after it, of the same file: a number both sets use for
// different files, migrations_test.go sharedNumbers, is not taken as done by the other's row from a build before the
// split). So a database migrated before the split runs nothing again, and the few early migrations that are in both
// sets (they made tables for both sides; migrations_test.go bothSets lists them) run once per part on a database the
// hub and the default manager share.
//
// The rows are read once, under the lock: no migration writes schema_migrations, so they change only by what this
// records itself. A database that is up to date costs a few reads and no write (docs/server/server.md "Migrations").
func (d *DB) migrateFrom(ctx context.Context, fsys fs.FS) error {
	// Serialize concurrent hubs, managers (and tests) on one advisory lock.
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(424242)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(424242)`) //nolint:errcheck
	if err := splitSchemaMigrations(ctx, conn); err != nil {
		return err
	}
	recs, err := readSchemaRecords(ctx, conn)
	if err != nil {
		return err
	}
	for _, r := range renumbered {
		if !recs.named(r.from) {
			continue // renumberMigration would change no row
		}
		if err := renumberMigration(ctx, conn, r.from, r.to); err != nil {
			return fmt.Errorf("renumber migration %s: %w", r.from, err)
		}
		if recs, err = readSchemaRecords(ctx, conn); err != nil {
			return err
		}
	}
	for _, sd := range setDirs {
		if d.sets&sd.set == 0 {
			continue
		}
		if err := migrateSet(ctx, conn, fsys, sd.part, recs); err != nil {
			return err
		}
	}
	return nil
}

// schemaMigrationsSplit says whether schema_migrations is already as splitSchemaMigrations' statements leave it: a
// table in the schema CREATE TABLE makes it in, with a part column, without schema_migrations_pkey, and with a
// relation named schema_migrations_version_part next to it. Each condition is what one statement's IF [NOT] EXISTS
// looks at.
const schemaMigrationsSplit = `SELECT coalesce((
	SELECT c.relkind = 'r' AND n.nspname = current_schema()
		AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'part' AND a.attnum > 0 AND NOT a.attisdropped)
		AND NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conrelid = c.oid AND k.conname = 'schema_migrations_pkey')
		AND EXISTS (SELECT 1 FROM pg_class i WHERE i.relnamespace = c.relnamespace AND i.relname = 'schema_migrations_version_part')
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass('schema_migrations')), false)`

// splitSchemaMigrations makes schema_migrations, or gives one from before the split its part column: the rows it has
// keep part NULL, and a version may have a row for each part. A table that is so already is left alone: an ALTER TABLE
// that changes nothing still takes an ACCESS EXCLUSIVE lock and a transaction id, on every start.
func splitSchemaMigrations(ctx context.Context, conn *pgxpool.Conn) error {
	var split bool
	if err := conn.QueryRow(ctx, schemaMigrationsSplit).Scan(&split); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	if split {
		return nil
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version int NOT NULL, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now(), part text)`,
		`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS part text`,
		`ALTER TABLE schema_migrations DROP CONSTRAINT IF EXISTS schema_migrations_pkey`,
		`CREATE UNIQUE INDEX IF NOT EXISTS schema_migrations_version_part ON schema_migrations (version, (coalesce(part, '')))`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("schema_migrations: %w", err)
		}
	}
	return nil
}

// schemaRecord is a schema_migrations row of some version: part is NULL on the rows from before the split.
type schemaRecord struct {
	name string
	part pgtype.Text
}

// schemaRecords are schema_migrations' rows by version.
type schemaRecords map[int][]schemaRecord

func readSchemaRecords(ctx context.Context, conn *pgxpool.Conn) (schemaRecords, error) {
	rows, err := conn.Query(ctx, `SELECT version, name, part FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	recs := schemaRecords{}
	var version int
	var r schemaRecord
	_, err = pgx.ForEachRow(rows, []any{&version, &r.name, &r.part}, func() error {
		recs[version] = append(recs[version], r)
		return nil
	})
	return recs, err
}

// named says whether a row records the file name.
func (recs schemaRecords) named(name string) bool {
	for _, rs := range recs {
		for _, r := range rs {
			if r.name == name {
				return true
			}
		}
	}
	return false
}

// done says whether part has migration name (numbered version) applied: a row of that part, or a NULL-part one from up
// to lastUnified or of the same file (migrateFrom).
func (recs schemaRecords) done(version int, name, part string) bool {
	for _, r := range recs[version] {
		if r.part.Valid && r.part.String == part || !r.part.Valid && (version <= lastUnified || r.name == name) {
			return true
		}
	}
	return false
}

// migrateSet applies the migrations in migrations/<part> that part has not (recs), adding each to recs as it records it.
func migrateSet(ctx context.Context, conn *pgxpool.Conn, fsys fs.FS, part string, recs schemaRecords) error {
	dir := "migrations/" + part
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		name := e.Name()
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if recs.done(version, name, part) {
			continue
		}
		sql, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, part) VALUES ($1, $2, $3)`, version, name, part)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s/%s: %w", part, name, err)
		}
		recs[version] = append(recs[version], schemaRecord{name: name, part: pgtype.Text{String: part, Valid: true}})
	}
	return nil
}

// migrationVersion is the number a migration file name starts with.
func migrationVersion(name string) (int, error) {
	version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
	if err != nil {
		return 0, fmt.Errorf("bad migration name %s", name)
	}
	return version, nil
}

// renumberMigration moves a database's record of migration from (applied under its old number) to to's name and
// number; when to is recorded already, the old record only goes away.
func renumberMigration(ctx context.Context, conn *pgxpool.Conn, from, to string) error {
	version, err := migrationVersion(to)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE name = $1
			AND EXISTS (SELECT 1 FROM schema_migrations WHERE version = $2)`, from, version); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE schema_migrations SET version = $2, name = $3 WHERE name = $1`, from, version, to)
		return err
	})
}

// TruncateForTest clears tables in tests: those of them the database's sets have, with what refers to them.
func (d *DB) TruncateForTest(ctx context.Context) error {
	cascade, err := d.truncateCascade(ctx, []string{"workspace_board", "workspace_snapshots", "push_device_retirements", "devices",
		"device_tokens", "live_activities", "phone_tools", "phone_write_queue", "node_sessions", "mail_accounts", "interactions",
		"settings", "inbox_items", "kb_entities", "connector_secrets", "machine_systems", "machine_metrics", "sent_messages",
		"todos", "todo_agents", "assistant_always", "machine_trust", "hub_settings", "manager_outbox", "handed_over",
		"machine_audit", "sent_as_user", "session_work_events", "hub_push_sent", "urgent_calls", "model_usage"})
	if err != nil {
		return err
	}
	return d.truncateNonEmpty(ctx, cascade)
}

// truncateCascade is what TRUNCATE tables CASCADE empties: those of tables the database has, and each table whose
// foreign key refers to one of these, and so on.
func (d *DB) truncateCascade(ctx context.Context, tables []string) ([]string, error) {
	var out []string
	err := d.pool.QueryRow(ctx, `WITH RECURSIVE c(rel) AS (
			SELECT to_regclass(t) FROM unnest($1::text[]) AS t WHERE to_regclass(t) IS NOT NULL
			UNION SELECT k.conrelid FROM pg_constraint k JOIN c ON k.confrelid = c.rel WHERE k.contype = 'f')
		SELECT coalesce(array_agg(rel::regclass::text ORDER BY rel::regclass::text), '{}') FROM c`, tables).Scan(&out)
	return out, err
}

// truncateNonEmpty empties tables (truncateCascade's) with one TRUNCATE ... CASCADE of those that have a row. TRUNCATE
// gives every table it names new files, a row in it or not: clearing two dozen tables, mostly empty, cost a test
// more than most of what it then did. The tables left empty are the same.
//
// The probe and the TRUNCATE are two statements: a row not yet committed when the probe looks, or written to a table it
// found empty after it, stays, where one TRUNCATE of every table would wait for the writer's lock and clear it. So it is
// for the start of a test, before anything it starts writes there.
func (d *DB) truncateNonEmpty(ctx context.Context, tables []string) error {
	if len(tables) == 0 {
		return nil
	}
	probe := make([]string, len(tables))
	for i, t := range tables {
		probe[i] = `SELECT ` + strconv.Itoa(i) + ` WHERE EXISTS (SELECT FROM ` + t + `)`
	}
	var rowful []int
	if err := d.pool.QueryRow(ctx, `SELECT coalesce(array_agg(i), '{}') FROM (`+strings.Join(probe, " UNION ALL ")+`) AS p(i)`).Scan(&rowful); err != nil {
		return err
	}
	if len(rowful) == 0 {
		return nil
	}
	names := make([]string, len(rowful))
	for i, n := range rowful {
		names[i] = tables[n]
	}
	_, err := d.pool.Exec(ctx, `TRUNCATE `+strings.Join(names, ", ")+` CASCADE`)
	return err
}
