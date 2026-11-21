package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
)

type postgresBackend struct {
	lookPath func(string) (string, error)
}

func newPostgresBackend() *postgresBackend {
	return &postgresBackend{lookPath: exec.LookPath}
}

func (b *postgresBackend) connect(ctx context.Context, target Target, database string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(target.URL)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	if database != "" {
		cfg.Database = database
	}
	cfg.RuntimeParams["application_name"] = "personad-backup"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, targetError(target, "connect to the database", err)
	}
	return conn, nil
}

func (b *postgresBackend) admin(ctx context.Context, target Target, avoid ...string) (*pgx.Conn, error) {
	var last error
	for _, database := range []string{"postgres", "template1"} {
		var skip bool
		for _, name := range avoid {
			if database == name {
				skip = true
			}
		}
		if skip {
			continue
		}
		conn, err := b.connect(ctx, target, database)
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no maintenance database is available")
	}
	return nil, last
}

func closeConn(ctx context.Context, conn *pgx.Conn) {
	if conn != nil {
		_ = conn.Close(context.WithoutCancel(ctx))
	}
}

func (b *postgresBackend) Identity(ctx context.Context, target Target, part, database string) (DatabaseIdentity, error) {
	if database == "" {
		database = target.Database
	}
	conn, err := b.connect(ctx, target, database)
	if err != nil {
		return DatabaseIdentity{}, fmt.Errorf("%s database is missing or unreachable: %w", part, err)
	}
	defer closeConn(ctx, conn)
	var identity DatabaseIdentity
	if err := conn.QueryRow(ctx, `SHOW server_version`).Scan(&identity.ServerVersion); err != nil {
		return DatabaseIdentity{}, targetError(target, "read the server version", err)
	}
	rows, err := conn.Query(ctx, `SELECT version, name, coalesce(part, '') FROM schema_migrations ORDER BY version, name, coalesce(part, '')`)
	if err != nil {
		return DatabaseIdentity{}, targetError(target, "read schema_migrations", err)
	}
	defer rows.Close()
	for rows.Next() {
		var migration Migration
		if err := rows.Scan(&migration.Version, &migration.Name, &migration.Part); err != nil {
			return DatabaseIdentity{}, targetError(target, "read schema_migrations", err)
		}
		identity.Schema = append(identity.Schema, migration)
	}
	if err := rows.Err(); err != nil {
		return DatabaseIdentity{}, targetError(target, "read schema_migrations", err)
	}
	if len(identity.Schema) == 0 {
		return DatabaseIdentity{}, fmt.Errorf("%s schema_migrations is empty; refusing an uninitialized database", part)
	}
	if part == "manager" {
		var hasTable bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass('public.sealing_key') IS NOT NULL`).Scan(&hasTable); err != nil {
			return DatabaseIdentity{}, targetError(target, "check the encryption-key identity", err)
		}
		if hasTable {
			if err := conn.QueryRow(ctx, `SELECT fingerprint FROM sealing_key`).Scan(&identity.SealingKey); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return DatabaseIdentity{}, targetError(target, "read the encryption-key identity", err)
			}
		}
		if identity.SealingKey == "" {
			if identity.SealedValues, err = sealedValues(ctx, conn); err != nil {
				return DatabaseIdentity{}, targetError(target, "read the sealed secrets", err)
			}
		}
	}
	return identity, nil
}

// sealedValues mirrors db.SealedValues over one connection (db.DB would open a
// pool of its own), skipping tables and columns an older schema lacks.
func sealedValues(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	var out []string
	collect := func(query string, args ...any) error {
		rows, err := conn.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		values, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, value := range values {
			if value != "" {
				out = append(out, value)
			}
		}
		return nil
	}
	hasColumn := func(table, column string) (bool, error) {
		var ok bool
		err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = to_regclass($1) AND attname = $2 AND attnum > 0 AND NOT attisdropped)`, table, column).Scan(&ok)
		return ok, err
	}
	for _, sealed := range db.SealedColumns() {
		table, column, _ := strings.Cut(sealed, ".")
		if ok, err := hasColumn(table, column); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			continue
		}
		if err := collect(`SELECT coalesce(` + pgx.Identifier{column}.Sanitize() + `::text, '') FROM ` + pgx.Identifier{table}.Sanitize()); err != nil {
			return nil, err
		}
	}
	if ok, err := hasColumn("settings", "value"); err != nil || !ok {
		return out, err
	}
	for _, setting := range db.SealedSettings() {
		for _, field := range setting.Fields {
			query := `SELECT coalesce(value->>$2, '') FROM settings WHERE key = $1 AND jsonb_typeof(value) = 'object'`
			if setting.List {
				query = `SELECT coalesce(e.elem->>$2, '') FROM settings s, jsonb_array_elements(s.value) AS e(elem)
					WHERE s.key = $1 AND jsonb_typeof(s.value) = 'array' AND jsonb_typeof(e.elem) = 'object'`
			}
			if err := collect(query, setting.Key, field); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (b *postgresBackend) Quiesced(ctx context.Context, target Target, database string, missingOK bool) error {
	return b.quiesced(ctx, target, database, missingOK, nil)
}

// QuiescedDatabase is the backup command's quiesce check for a process that has
// no pool of its own on the database (the hub checking the built-in manager's
// own database while the manager is stopped). missingOK: a database that does
// not exist yet (the manager has not moved there) passes.
func QuiescedDatabase(ctx context.Context, target Target, database string, missingOK bool) error {
	return newPostgresBackend().Quiesced(ctx, target, database, missingOK)
}

// QuiescedExcept is Quiesced for a process that keeps its own pool open while
// it reads the database (the hub, pausing its own writers for a built-in
// snapshot): the named server processes are its connections and do not count
// as other writers. Everything else, including sessions a role may not see,
// still fails closed.
func QuiescedExcept(ctx context.Context, target Target, database string, own ...int32) error {
	return newPostgresBackend().quiesced(ctx, target, database, false, own)
}

func (b *postgresBackend) quiesced(ctx context.Context, target Target, database string, missingOK bool, own []int32) error {
	if database == "" {
		database = target.Database
	}
	conn, err := b.connect(ctx, target, database)
	if err != nil {
		if missingOK {
			exists, existsErr := b.Exists(ctx, target, database)
			if existsErr == nil && !exists {
				return nil
			}
		}
		return fmt.Errorf("%s database is missing or unreachable: %w", target.Part, err)
	}
	defer closeConn(ctx, conn)
	var sessions, hidden int
	if len(own) == 0 {
		err = conn.QueryRow(ctx, quiescedQuery).Scan(&sessions, &hidden)
	} else {
		err = conn.QueryRow(ctx, quiescedExceptQuery, own).Scan(&sessions, &hidden)
	}
	if err != nil {
		return targetError(target, "check that writes are stopped", err)
	}
	if sessions != 0 {
		return fmt.Errorf("%s database still has %d other connection(s); stop the hub and built-in manager before using --writes-quiesced", target.Part, sessions)
	}
	if hidden != 0 {
		return fmt.Errorf("%s database has %d session(s) of other roles whose kind this role may not see (pg_stat_activity hides it from a role without pg_read_all_stats), so it cannot be shown that writes are stopped. "+
			"Stop them, or run the backup as a role that has pg_read_all_stats (GRANT pg_read_all_stats TO <role>) or is a superuser, then retry", target.Part, hidden)
	}
	return nil
}

// quiescedQuery counts the other sessions on the current database that may
// write. A client backend is one. pg_stat_activity shows a role without
// pg_read_all_stats only the database, pid, role and application name of
// another role's session (backend_type and the rest are NULL), so such a row
// cannot be told apart from a client and counts as hidden: the check fails
// closed instead of seeing nothing. A row with no role either is not a client
// (every client backend logs in as a role; autovacuum workers and the like
// have none), so it is ignored, as visible non-client processes such as a
// subscription's apply worker are.
const quiescedQuery = `SELECT
	count(*) FILTER (WHERE backend_type = 'client backend'),
	count(*) FILTER (WHERE backend_type IS NULL AND usesysid IS NOT NULL)
	FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`

// quiescedExceptQuery is quiescedQuery for the caller's own connections: the
// hub's pool keeps its server processes open while it reads the database.
const quiescedExceptQuery = `SELECT
	count(*) FILTER (WHERE backend_type = 'client backend' AND NOT (pid = ANY($1::int4[]))),
	count(*) FILTER (WHERE backend_type IS NULL AND usesysid IS NOT NULL AND NOT (pid = ANY($1::int4[])))
	FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`

func (b *postgresBackend) Create(ctx context.Context, target Target, name string) error {
	conn, err := b.admin(ctx, target, name)
	if err != nil {
		return err
	}
	defer closeConn(ctx, conn)
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err != nil {
		return targetError(target, "create a temporary database", err)
	}
	return nil
}

func (b *postgresBackend) Drop(ctx context.Context, target Target, name string) error {
	conn, err := b.admin(ctx, target, name)
	if err != nil {
		return err
	}
	defer closeConn(ctx, conn)
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+identifier+` WITH (FORCE)`); err != nil {
		if _, fallbackErr := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+identifier); fallbackErr != nil {
			return targetError(target, "drop a temporary database", errors.Join(err, fallbackErr))
		}
	}
	return nil
}

func (b *postgresBackend) Rename(ctx context.Context, target Target, from, to string) error {
	conn, err := b.admin(ctx, target, from)
	if err != nil {
		return err
	}
	defer closeConn(ctx, conn)
	query := `ALTER DATABASE ` + pgx.Identifier{from}.Sanitize() + ` RENAME TO ` + pgx.Identifier{to}.Sanitize()
	if _, err := conn.Exec(ctx, query); err != nil {
		return targetError(target, "rename a staged database", err)
	}
	return nil
}

func (b *postgresBackend) Exists(ctx context.Context, target Target, name string) (bool, error) {
	conn, err := b.admin(ctx, target, name)
	if err != nil {
		return false, err
	}
	defer closeConn(ctx, conn)
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return false, targetError(target, "check a database name", err)
	}
	return exists, nil
}

func (b *postgresBackend) Dump(ctx context.Context, target Target, destination string) error {
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	runErr := b.runClient(ctx, target, target.Database, nil, out, "pg_dump", "--format=custom", "--no-owner", "--no-acl")
	if syncErr := out.Sync(); runErr == nil {
		runErr = syncErr
	}
	if closeErr := out.Close(); runErr == nil {
		runErr = closeErr
	}
	if runErr != nil {
		return runErr
	}
	info, err := os.Stat(destination)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return errors.New("pg_dump produced an empty dump")
	}
	return nil
}

func (b *postgresBackend) Restore(ctx context.Context, target Target, database, dumpPath string) error {
	// pg_restore connects only with -d (otherwise it prints a script). libpq
	// expands a dbname with "=" or a URI prefix as a connection string, so
	// only a plain name is passed; the rest stays in the environment.
	if database == "" || strings.Contains(database, "=") || strings.Contains(database, "://") {
		return errors.New("refusing to restore into a database name libpq would read as a connection string")
	}
	in, err := os.Open(dumpPath)
	if err != nil {
		return err
	}
	defer in.Close()
	return b.runClient(ctx, target, database, in, io.Discard, "pg_restore", "--format=custom", "--exit-on-error", "--no-owner", "--no-acl", "--dbname="+database)
}

var postgresURLPattern = lazyre.New(`postgres(?:ql)?://[^[:space:]"']+`)
var passwordPattern = lazyre.New(`(?i)(password|sslpassword)=[^[:space:]]+`)

func redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "<redacted>")
		}
	}
	text = postgresURLPattern.ReplaceAllString(text, "<redacted-database-url>")
	return passwordPattern.ReplaceAllString(text, "$1=<redacted>")
}

func targetSecrets(target Target) []string {
	secrets := []string{target.URL}
	if u, err := url.Parse(target.URL); err == nil {
		if password, ok := u.User.Password(); ok {
			secrets = append(secrets, password)
		}
	}
	// Without the TLS file parameters: unreadable files here must not hide a
	// query-string password from the redaction.
	if cfg, err := parseAddressing(target.URL); err == nil {
		secrets = append(secrets, cfg.Password)
	}
	return append(secrets, tlsKeyPasswords(target.URL)...)
}

// tlsKeyPasswords are the URL's sslpassword values (the passphrase of an
// encrypted TLS client key).
func tlsKeyPasswords(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return u.Query()["sslpassword"]
}

func targetError(target Target, action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %s", action, redact(err.Error(), targetSecrets(target)...))
}

type limitedBuffer struct {
	bytes.Buffer
	remaining int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.remaining <= 0 {
		return len(p), nil
	}
	if len(p) > b.remaining {
		_, _ = b.Buffer.Write(p[:b.remaining])
		b.remaining = 0
		return len(p), nil
	}
	n, _ := b.Buffer.Write(p)
	b.remaining -= n
	return len(p), nil
}

// libpqQueryEnvironment maps connection-URL query parameters to the libpq
// environment variables the command-line clients read. Host, port, user,
// database and password are taken from pgx's parsed configuration instead.
var libpqQueryEnvironment = map[string]string{
	"sslmode":                  "PGSSLMODE",
	"sslrootcert":              "PGSSLROOTCERT",
	"sslcert":                  "PGSSLCERT",
	"sslkey":                   "PGSSLKEY",
	"sslcrl":                   "PGSSLCRL",
	"sslcrldir":                "PGSSLCRLDIR",
	"sslsni":                   "PGSSLSNI",
	"sslnegotiation":           "PGSSLNEGOTIATION",
	"sslcertmode":              "PGSSLCERTMODE",
	"ssl_min_protocol_version": "PGSSLMINPROTOCOLVERSION",
	"ssl_max_protocol_version": "PGSSLMAXPROTOCOLVERSION",
	"requirepeer":              "PGREQUIREPEER",
	"gssencmode":               "PGGSSENCMODE",
	"channel_binding":          "PGCHANNELBINDING",
	"connect_timeout":          "PGCONNECT_TIMEOUT",
	"target_session_attrs":     "PGTARGETSESSIONATTRS",
	"passfile":                 "PGPASSFILE",
	// sslpassword has no libpq environment variable: clientServiceFile.
}

// parseAddressing parses a database URL for where and as whom to connect. pgx
// loads TLS files while parsing, so those parameters are stripped (and TLS
// turned off, which also keeps pgx from doubling every host into TLS and plain
// fallbacks): host, port, user, password and database must not depend on
// whether the files are readable where personad runs. It is for addressing
// only, never for connecting.
func parseAddressing(rawURL string) (*pgx.ConnConfig, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	query := u.Query()
	for _, key := range []string{"sslrootcert", "sslcert", "sslkey", "sslpassword", "sslcrl", "sslcrldir", "sslmode", "sslsni", "sslnegotiation"} {
		query.Del(key)
	}
	query.Set("sslmode", "disable")
	addressing := *u
	addressing.RawQuery = query.Encode()
	return pgx.ParseConfig(addressing.String())
}

// clientEnvironment is the libpq environment for pg_dump/pg_restore. libpq
// expands a URI only as an explicit dbname argument, never in PGDATABASE, so
// every part is passed as its own variable; the password only in PGPASSWORD.
// Every host pgx would try goes into PGHOST, in pgx's order, and its port at the
// same place in PGPORT. Inside a Docker container the server is the
// container's own loopback.
func clientEnvironment(target Target, database string, docker bool) []string {
	values := map[string]string{"PGDATABASE": database}
	u, err := url.Parse(target.URL)
	if err != nil {
		return []string{"PGDATABASE=" + database, "PGPASSWORD="}
	}
	// pgx loads TLS files while parsing; host, port, user and password must
	// not depend on whether those files are readable here.
	if cfg, err := parseAddressing(target.URL); err == nil {
		hosts, ports := []string{cfg.Host}, []string{strconv.Itoa(int(cfg.Port))}
		for _, fallback := range cfg.Fallbacks {
			hosts, ports = append(hosts, fallback.Host), append(ports, strconv.Itoa(int(fallback.Port)))
		}
		values["PGHOST"], values["PGPORT"] = strings.Join(hosts, ","), strings.Join(ports, ",")
		values["PGUSER"], values["PGPASSWORD"] = cfg.User, cfg.Password
	}
	if docker {
		values["PGHOST"], values["PGPORT"], values["PGSSLMODE"] = "127.0.0.1", "5432", "disable"
	} else {
		for key, list := range u.Query() {
			// pgx takes the first of repeated values; so do the clients.
			if name, ok := libpqQueryEnvironment[key]; ok && len(list) > 0 {
				values[name] = list[0]
			}
		}
	}
	environment := make([]string, 0, len(values))
	for name, value := range values {
		if value != "" || name == "PGPASSWORD" {
			environment = append(environment, name+"="+value)
		}
	}
	sort.Strings(environment)
	return environment
}

// clientServiceFile gives the clients the URL's sslpassword, the passphrase of
// an encrypted TLS client key. libpq reads it from no environment variable and
// the argument list is visible to every user, so it goes into a libpq service
// file of its own: mode 0600 in a private directory, named by PGSERVICEFILE and
// PGSERVICE, and deleted once the client exits. Its other settings come from
// the environment as before.
func clientServiceFile(target Target) (environment []string, cleanup func(), err error) {
	cleanup = func() {}
	// The first value, as pgx takes it (an empty one is no password for pgx
	// either).
	passwords := tlsKeyPasswords(target.URL)
	if len(passwords) == 0 || passwords[0] == "" {
		return nil, cleanup, nil
	}
	password := passwords[0]
	// A service file holds one value per line, trailing white space dropped.
	if strings.ContainsAny(password, "\r\n\x00") || strings.TrimRight(password, " \t\v\f") != password {
		return nil, cleanup, errors.New("the database URL's sslpassword has a line break or trailing white space, which the PostgreSQL client tools cannot be given")
	}
	directory, err := os.MkdirTemp("", "persona-backup-libpq-*")
	if err != nil {
		return nil, cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(directory) }
	name := filepath.Join(directory, "pg_service.conf")
	if err := os.WriteFile(name, []byte("[persona-backup]\nsslpassword="+password+"\n"), 0o600); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return []string{"PGSERVICEFILE=" + name, "PGSERVICE=persona-backup"}, cleanup, nil
}

func (b *postgresBackend) runClient(ctx context.Context, target Target, database string, stdin io.Reader, stdout io.Writer, program string, args ...string) error {
	container := target.Container
	if container == "" {
		if _, err := b.lookPath(program); err != nil {
			container = target.DefaultContainer
		}
	}
	environment := clientEnvironment(target, database, container != "")
	if container == "" {
		service, cleanup, err := clientServiceFile(target)
		if err != nil {
			return fmt.Errorf("%s: %w", program, err)
		}
		defer cleanup()
		environment = append(environment, service...)
	}
	var cmd *exec.Cmd
	if container != "" {
		dockerArgs := []string{"exec", "--interactive"}
		for _, variable := range environment {
			name, _, _ := strings.Cut(variable, "=")
			dockerArgs = append(dockerArgs, "--env", name)
		}
		dockerArgs = append(dockerArgs, container, program)
		cmd = exec.CommandContext(ctx, "docker", append(dockerArgs, args...)...)
	} else {
		cmd = exec.CommandContext(ctx, program, args...)
	}
	cmd.Env = append(os.Environ(), environment...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	stderr := &limitedBuffer{remaining: 32 << 10}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			detail = ": " + detail
		}
		return fmt.Errorf("%s failed%s: %w", program, redact(detail, targetSecrets(target)...), err)
	}
	return nil
}

// sortedIdentities makes fake and real backends produce the same canonical
// comparison even if a database driver relaxes row ordering in the future.
func sortedIdentities(identity DatabaseIdentity) DatabaseIdentity {
	identity.Schema = append([]Migration(nil), identity.Schema...)
	sort.Slice(identity.Schema, func(i, j int) bool {
		if identity.Schema[i].Version != identity.Schema[j].Version {
			return identity.Schema[i].Version < identity.Schema[j].Version
		}
		if identity.Schema[i].Name != identity.Schema[j].Name {
			return identity.Schema[i].Name < identity.Schema[j].Name
		}
		return identity.Schema[i].Part < identity.Schema[j].Part
	})
	return identity
}

func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// CheckClients fails before any work when the programs a command needs cannot
// run: not on PATH here, and no Docker container to run them in (an explicit
// one, or the local persona-postgres default for a loopback server).
func (b *postgresBackend) CheckClients(target Target, programs ...string) error {
	container := target.Container
	if container == "" {
		var missing []string
		for _, program := range programs {
			if _, err := b.lookPath(program); err != nil {
				missing = append(missing, program)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		container = target.DefaultContainer
		if container == "" {
			return fmt.Errorf("the %s database needs %s, which is not installed here, and its server is not on this machine's loopback address, so no local container is assumed. "+
				"Backing up from inside the hub container of the shipped Docker Compose deployment (database host \"postgres\") is not supported yet. "+
				"Run personad backup where PostgreSQL client tools of the server's major version are installed, or pass --%s-docker-container naming a container that has them",
				target.Part, strings.Join(missing, " and "), target.Part)
		}
		if _, err := b.lookPath("docker"); err != nil {
			return fmt.Errorf("the %s database needs %s, which is not installed here, and the docker command that would run them in container %q is not available either; install PostgreSQL client tools of the server's major version", target.Part, strings.Join(missing, " and "), container)
		}
		return nil
	}
	if _, err := b.lookPath("docker"); err != nil {
		return fmt.Errorf("the %s database is set to use pg_dump/pg_restore inside container %q, but the docker command is not available here", target.Part, container)
	}
	return nil
}
