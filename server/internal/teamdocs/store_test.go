package teamdocs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreRevisionLifecycle(t *testing.T) {
	store := newStoreTest(t)
	ctx := t.Context()
	requireStoreError(t, store.Init(ctx), nil)
	reader := grantStoreToken(t, store, "alpha", "reader_1", "reader")
	writer := grantStoreToken(t, store, "alpha", "writer-1", "writer")
	approver := grantStoreToken(t, store, "alpha", "approver_1", "approver")
	secondApprover := grantStoreToken(t, store, "alpha", "approver_2", "approver")

	_, err := store.Publish(ctx, reader, "alpha", "Guide", 0, "draft")
	requireStoreError(t, err, ErrForbidden)
	_, err = store.Approve(ctx, approver, "alpha", "Guide", 1)
	requireStoreError(t, err, ErrNotFound)
	_, err = store.Publish(ctx, writer, "alpha", "Guide", 1, "draft")
	requireStoreError(t, err, ErrConflict)

	content := "# Caf\u00e9\r\n\n\u672c\u6587 \U0001f642  \n"
	first, err := store.Publish(ctx, writer, "alpha", "Guide", 0, content)
	requireStoreError(t, err, nil)
	digest := sha256.Sum256([]byte(content))
	if want := (Revision{Project: "alpha", Document: "Guide", Number: 1, Content: content, SHA256: hex.EncodeToString(digest[:]), Author: "writer-1"}); first != want {
		t.Fatal("first revision did not preserve its metadata or exact content")
	}
	_, err = store.Read(ctx, reader, "alpha", "Guide", 0)
	requireStoreError(t, err, ErrNotFound)
	pinned, err := store.Read(ctx, reader, "alpha", "Guide", 1)
	requireStoreError(t, err, nil)
	if !reflect.DeepEqual(pinned, first) {
		t.Fatal("pinned draft differs from the publication")
	}
	_, err = store.Approve(ctx, reader, "alpha", "Guide", 1)
	requireStoreError(t, err, ErrForbidden)
	_, err = store.Approve(ctx, writer, "alpha", "Guide", 1)
	requireStoreError(t, err, ErrForbidden)
	approved, err := store.Approve(ctx, approver, "alpha", "Guide", 1)
	requireStoreError(t, err, nil)
	if approved.ApprovedAt == nil || approved.ApprovedAt.IsZero() {
		t.Fatal("approval must record its timestamp")
	}
	first.ApprovedAt = approved.ApprovedAt
	first.ApprovedBy = "approver_1"
	if !reflect.DeepEqual(approved, first) {
		t.Fatal("approval did not record the approver or changed the published revision")
	}
	retried, err := store.Approve(ctx, secondApprover, "alpha", "Guide", 1)
	requireStoreError(t, err, nil)
	if !reflect.DeepEqual(retried, approved) {
		t.Fatal("approval retry changed the original approval")
	}

	maxContent := strings.Repeat("\u00e9", 128*1024)
	second, err := store.Publish(ctx, approver, "alpha", "Guide", 1, maxContent)
	requireStoreError(t, err, nil)
	if second.Number != 2 || second.Author != "approver_1" || second.ApprovedBy != "" || second.Content != maxContent {
		t.Fatal("approver publication did not produce the expected draft")
	}
	_, err = store.Publish(ctx, writer, "alpha", "Guide", 1, "stale write")
	requireStoreError(t, err, ErrConflict)
	_, err = store.Approve(ctx, approver, "alpha", "Guide", 1)
	requireStoreError(t, err, ErrConflict)
	latest, err := store.Read(ctx, reader, "alpha", "Guide", 0)
	requireStoreError(t, err, nil)
	if !reflect.DeepEqual(latest, first) {
		t.Fatal("pending head changed the approved result")
	}
	pinned, err = store.Read(ctx, reader, "alpha", "Guide", 2)
	requireStoreError(t, err, nil)
	if pinned != second {
		t.Fatal("pinned head did not return the second draft")
	}
	_, err = store.Read(ctx, reader, "alpha", "Guide", 3)
	requireStoreError(t, err, ErrNotFound)
	approved, err = store.Approve(ctx, secondApprover, "alpha", "Guide", 2)
	requireStoreError(t, err, nil)
	latest, err = store.Read(ctx, writer, "alpha", "Guide", 0)
	requireStoreError(t, err, nil)
	second.ApprovedAt = approved.ApprovedAt
	second.ApprovedBy = "approver_2"
	if !reflect.DeepEqual(latest, second) {
		t.Fatal("latest approved did not advance after approval")
	}
	pinned, err = store.Read(ctx, reader, "alpha", "Guide", 1)
	requireStoreError(t, err, nil)
	if !reflect.DeepEqual(pinned, first) {
		t.Fatal("approving a newer revision changed the pinned approval")
	}
}

func TestStoreRevisionJSON(t *testing.T) {
	revision := Revision{Project: "alpha", Document: "Guide", Number: 1, Content: "draft", SHA256: "digest", Author: "writer"}
	encoded, err := json.Marshal(revision)
	requireStoreError(t, err, nil)
	if string(encoded) != `{"project":"alpha","document":"Guide","revision":1,"content":"draft","sha256":"digest","author":"writer"}` {
		t.Fatalf("unexpected draft JSON: %s", encoded)
	}
	revision.ApprovedBy = "approver"
	approvalTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	revision.ApprovedAt = &approvalTime
	encoded, err = json.Marshal(revision)
	requireStoreError(t, err, nil)
	if string(encoded) != `{"project":"alpha","document":"Guide","revision":1,"content":"draft","sha256":"digest","author":"writer","approved_by":"approver","approved_at":"2026-01-02T03:04:05Z"}` {
		t.Fatalf("unexpected approved JSON: %s", encoded)
	}
}

func TestStoreCredentialRotationAndIsolation(t *testing.T) {
	store := newStoreTest(t)
	ctx := t.Context()
	writer := grantStoreToken(t, store, "alpha", "member", "writer")
	other := grantStoreToken(t, store, "beta", "member", "writer")
	if writer == other {
		t.Fatal("projects share credentials")
	}
	_, err := store.Publish(ctx, writer, "alpha", "Guide", 0, "first project")
	requireStoreError(t, err, nil)
	_, err = store.Publish(ctx, other, "beta", "Guide", 0, "second project")
	requireStoreError(t, err, nil)
	_, err = store.Read(ctx, writer, "beta", "Guide", 1)
	requireStoreError(t, err, ErrUnauthorized)
	_, err = store.Read(ctx, other, "alpha", "Missing", 0)
	requireStoreError(t, err, ErrUnauthorized)

	rotated := grantStoreToken(t, store, "alpha", "member", "reader")
	if rotated == writer || rotated == other {
		t.Fatal("regrant did not generate a separate credential")
	}
	var storedHash []byte
	requireStoreError(t, store.Pool.QueryRow(ctx, `SELECT token_hash FROM teamdocs_members WHERE project = $1 AND member = $2`, "alpha", "member").Scan(&storedHash), nil)
	rotatedHash := sha256.Sum256([]byte(rotated))
	if !bytes.Equal(storedHash, rotatedHash[:]) {
		t.Fatal("the membership does not store the bearer hash")
	}
	_, err = store.Read(ctx, writer, "alpha", "Guide", 1)
	requireStoreError(t, err, ErrUnauthorized)
	_, err = store.Read(ctx, hex.EncodeToString(storedHash), "alpha", "Guide", 1)
	requireStoreError(t, err, ErrUnauthorized)
	_, err = store.Publish(ctx, rotated, "alpha", "Guide", 1, "unauthorized edit")
	requireStoreError(t, err, ErrForbidden)
	first, err := store.Read(ctx, rotated, "alpha", "Guide", 1)
	requireStoreError(t, err, nil)
	if first.Content != "first project" || first.Author != "member" {
		t.Fatal("regrant changed the original authored content")
	}
	requireStoreError(t, store.Revoke(ctx, "alpha", "absent"), ErrNotFound)
	requireStoreError(t, store.Revoke(ctx, "alpha", "member"), nil)
	_, err = store.Read(ctx, rotated, "alpha", "Guide", 1)
	requireStoreError(t, err, ErrUnauthorized)
	_, err = store.Read(ctx, "", "alpha", "Guide", 1)
	requireStoreError(t, err, ErrUnauthorized)
	second, err := store.Read(ctx, other, "beta", "Guide", 1)
	requireStoreError(t, err, nil)
	if second.Content != "second project" || second.Author != "member" {
		t.Fatal("revocation affected a different project")
	}
}

func TestStoreInputValidation(t *testing.T) {
	store := &Store{}
	ctx := t.Context()
	for _, invalid := range []string{"", "one/two", "a\n", "a.b", "\u00e9", strings.Repeat("a", 101)} {
		_, err := store.Grant(ctx, invalid, "member", "reader")
		requireStoreError(t, err, ErrInvalid)
		_, err = store.Grant(ctx, "project", invalid, "reader")
		requireStoreError(t, err, ErrInvalid)
		requireStoreError(t, store.Revoke(ctx, "project", invalid), ErrInvalid)
		requireStoreError(t, store.Revoke(ctx, invalid, "member"), ErrInvalid)
		_, err = store.Publish(ctx, "", "project", invalid, 0, "draft")
		requireStoreError(t, err, ErrInvalid)
		_, err = store.Approve(ctx, "", invalid, "document", 1)
		requireStoreError(t, err, ErrInvalid)
		_, err = store.Read(ctx, "", "project", invalid, 0)
		requireStoreError(t, err, ErrInvalid)
	}
	_, err := store.Grant(ctx, "project", "member", "owner")
	requireStoreError(t, err, ErrInvalid)
	for _, content := range []string{"", "one\x00two", "\xff", strings.Repeat("\u00e9", 128*1024) + "a"} {
		_, err := store.Publish(ctx, "", "project", "document", 0, content)
		requireStoreError(t, err, ErrInvalid)
	}
	_, err = store.Publish(ctx, "", "project", "document", -1, "draft")
	requireStoreError(t, err, ErrInvalid)
	_, err = store.Approve(ctx, "", "project", "document", 0)
	requireStoreError(t, err, ErrInvalid)
	_, err = store.Read(ctx, "", "project", "document", -1)
	requireStoreError(t, err, ErrInvalid)
}

func TestStoreConcurrentPublishChecksHead(t *testing.T) {
	store := newStoreTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	writer := grantStoreToken(t, store, "alpha", "writer", "writer")
	for expected := range int64(2) {
		const contenders = 8
		start := make(chan struct{})
		results := make(chan error, contenders)
		for range contenders {
			go func() {
				<-start
				published, err := store.Publish(ctx, writer, "alpha", "Guide", expected, "draft")
				if err == nil && published.Number != expected+1 {
					err = errors.New("publish skipped a revision number")
				}
				results <- err
			}()
		}
		close(start)
		var winners int
		for range contenders {
			select {
			case err := <-results:
				if err == nil {
					winners++
				} else {
					requireStoreError(t, err, ErrConflict)
				}
			case <-ctx.Done():
				t.Fatal("concurrent publications did not finish")
			}
		}
		if winners != 1 {
			t.Fatalf("head %d: got %d successful publications, want 1", expected, winners)
		}
	}
	_, err := store.Read(ctx, writer, "alpha", "Guide", 3)
	requireStoreError(t, err, ErrNotFound)
}

func TestStoreRevokeWaitsForAuthorizedPublish(t *testing.T) {
	store := newStoreTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	writer := grantStoreToken(t, store, "alpha", "writer", "writer")
	_, err := store.Publish(ctx, writer, "alpha", "Guide", 0, "first")
	requireStoreError(t, err, nil)
	lock, err := store.Pool.Begin(ctx)
	requireStoreError(t, err, nil)
	defer lock.Rollback(context.Background())
	var blocker int32
	requireStoreError(t, lock.QueryRow(ctx, `SELECT pg_backend_pid() FROM teamdocs_heads WHERE project = 'alpha' AND document = 'Guide' FOR UPDATE`).Scan(&blocker), nil)

	published := make(chan error, 1)
	go func() {
		_, err := store.Publish(ctx, writer, "alpha", "Guide", 1, "second")
		published <- err
	}()
	publisher := waitStoreBlockedBy(t, ctx, store, blocker)
	revoked := make(chan error, 1)
	go func() { revoked <- store.Revoke(ctx, "alpha", "writer") }()
	waitStoreBlockedBy(t, ctx, store, publisher)

	requireStoreError(t, lock.Commit(ctx), nil)
	requireStoreError(t, <-published, nil)
	requireStoreError(t, <-revoked, nil)
	_, err = store.Publish(ctx, writer, "alpha", "Guide", 2, "third")
	requireStoreError(t, err, ErrUnauthorized)
	reader := grantStoreToken(t, store, "alpha", "reader", "reader")
	second, err := store.Read(ctx, reader, "alpha", "Guide", 2)
	requireStoreError(t, err, nil)
	if second.Content != "second" || second.Author != "writer" {
		t.Fatal("revocation changed the publication that preceded it")
	}
}

func TestStoreReadWaitsForRevocation(t *testing.T) {
	store := newStoreTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	writer := grantStoreToken(t, store, "alpha", "writer", "writer")
	_, err := store.Publish(ctx, writer, "alpha", "Guide", 0, "draft")
	requireStoreError(t, err, nil)
	revocation, err := store.Pool.Begin(ctx)
	requireStoreError(t, err, nil)
	defer revocation.Rollback(context.Background())
	var blocker int32
	requireStoreError(t, revocation.QueryRow(ctx, `DELETE FROM teamdocs_members WHERE project = 'alpha' AND member = 'writer' RETURNING pg_backend_pid()`).Scan(&blocker), nil)
	read := make(chan error, 1)
	go func() {
		_, err := store.Read(ctx, writer, "alpha", "Guide", 1)
		read <- err
	}()
	waitStoreBlockedBy(t, ctx, store, blocker)
	requireStoreError(t, revocation.Commit(ctx), nil)
	requireStoreError(t, <-read, ErrUnauthorized)
}

func newStoreTest(t *testing.T) *Store {
	t.Helper()
	base := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL must point to local PostgreSQL")
	}
	config, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal("invalid local test database configuration")
	}
	isLocal := func(host string) bool { return filepath.IsAbs(host) || net.ParseIP(host).IsLoopback() }
	if !isLocal(config.ConnConfig.Host) {
		t.Fatal("store tests require a loopback address or local Unix socket")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if !isLocal(fallback.Host) {
			t.Fatal("store tests require every database fallback to be local")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config.ConnConfig.Copy())
	if err != nil {
		t.Fatal("could not connect to local test PostgreSQL")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	schema := "teamdocs_test_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+quoted)
	requireStoreError(t, err, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, `DROP SCHEMA `+quoted+` CASCADE`); err != nil {
			t.Errorf("drop the test-owned schema: %v", err)
		}
	})
	config.ConnConfig.RuntimeParams["search_path"] = quoted
	config.ConnConfig.RuntimeParams["application_name"] = schema
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("could not create isolated test pool")
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal("could not connect the isolated test pool")
	}
	store := &Store{Pool: pool}
	requireStoreError(t, store.Init(ctx), nil)
	return store
}

func waitStoreBlockedBy(t *testing.T, ctx context.Context, store *Store, blocker int32) int32 {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked int32
		err := store.Pool.QueryRow(ctx, `
			SELECT pid FROM pg_stat_activity
			WHERE application_name = current_setting('application_name') AND $1 = ANY(pg_blocking_pids(pid)) LIMIT 1`,
			blocker).Scan(&blocked)
		if err == nil {
			return blocked
		}
		requireStoreError(t, err, pgx.ErrNoRows)
		select {
		case <-ctx.Done():
			t.Fatal("no test transaction waited on the expected row lock")
		case <-ticker.C:
		}
	}
}

func grantStoreToken(t *testing.T, store *Store, project, member, role string) string {
	t.Helper()
	token, err := store.Grant(t.Context(), project, member, role)
	requireStoreError(t, err, nil)
	if token == "" {
		t.Fatal("grant returned no credential")
	}
	return token
}

func requireStoreError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
