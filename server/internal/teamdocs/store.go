// Package teamdocs stores project-scoped document revisions and approvals.
package teamdocs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nwverse/persona/server/internal/lazyre"
)

var (
	ErrInvalid      = errors.New("invalid team document input")
	ErrUnauthorized = errors.New("invalid project credential")
	ErrForbidden    = errors.New("project role does not permit this operation")
	ErrNotFound     = errors.New("team document resource not found")
	ErrConflict     = errors.New("document head does not match")
)

const (
	roleReader       = "reader"
	roleWriter       = "writer"
	roleApprover     = "approver"
	credentialBytes  = 32
	maxRevisionBytes = 256 * 1024
)

var storeID = lazyre.New(`^[a-zA-Z0-9_-]{1,100}$`)

//go:embed store.sql
var storeSchema string

// Store uses a database configured independently of the hub.
type Store struct {
	Pool *pgxpool.Pool
}

type Revision struct {
	Project    string     `json:"project"`
	Document   string     `json:"document"`
	Number     int64      `json:"revision"`
	Content    string     `json:"content"`
	SHA256     string     `json:"sha256"`
	Author     string     `json:"author"`
	ApprovedBy string     `json:"approved_by,omitempty"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
}

func (store *Store) Init(ctx context.Context) error {
	return pgx.BeginFunc(ctx, store.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('persona-teamdocs-bootstrap'))`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, storeSchema)
		return err
	})
}

// Grant creates or rotates the credential for one project member.
func (store *Store) Grant(ctx context.Context, project, member, role string) (string, error) {
	if !storeID.MatchString(project) || !storeID.MatchString(member) ||
		(role != roleReader && role != roleWriter && role != roleApprover) {
		return "", ErrInvalid
	}
	var secret [credentialBytes]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	hash := sha256.Sum256([]byte(token))
	_, err := store.Pool.Exec(ctx, `
		INSERT INTO teamdocs_members (project, member, role, token_hash) VALUES ($1, $2, $3, $4)
		ON CONFLICT (project, member) DO UPDATE SET role = EXCLUDED.role, token_hash = EXCLUDED.token_hash`,
		project, member, role, hash[:])
	if err != nil {
		return "", err
	}
	return token, nil
}

func (store *Store) Revoke(ctx context.Context, project, member string) error {
	if !storeID.MatchString(project) || !storeID.MatchString(member) {
		return ErrInvalid
	}
	result, err := store.Pool.Exec(ctx, `DELETE FROM teamdocs_members WHERE project = $1 AND member = $2`, project, member)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// withMember holds the credential row lock through the document operation and commit.
// A concurrent rotation or revocation either precedes authorization or waits for the operation.
func (store *Store) withMember(ctx context.Context, token, project, requiredRole string, action func(pgx.Tx, string) error) error {
	if len(token) != base64.RawURLEncoding.EncodedLen(credentialBytes) {
		return ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	return pgx.BeginTxFunc(ctx, store.Pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var member, role string
		err := tx.QueryRow(ctx, `
			SELECT member, role FROM teamdocs_members WHERE project = $1 AND token_hash = $2 FOR SHARE`,
			project, hash[:]).Scan(&member, &role)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if requiredRole != roleReader && role != requiredRole && role != roleApprover {
			return ErrForbidden
		}
		return action(tx, member)
	})
}

func (store *Store) Publish(ctx context.Context, token, project, document string, expected int64, content string) (Revision, error) {
	if !storeID.MatchString(project) || !storeID.MatchString(document) || expected < 0 ||
		len(content) == 0 || len(content) > maxRevisionBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return Revision{}, ErrInvalid
	}
	var revision Revision
	err := store.withMember(ctx, token, project, roleWriter, func(tx pgx.Tx, member string) error {
		if expected == 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO teamdocs_heads (project, document, head) VALUES ($1, $2, 0) ON CONFLICT DO NOTHING`,
				project, document); err != nil {
				return err
			}
		}
		if expected == math.MaxInt64 {
			return ErrConflict
		}
		var number int64
		err := tx.QueryRow(ctx, `
			UPDATE teamdocs_heads SET head = head + 1 WHERE project = $1 AND document = $2 AND head = $3 RETURNING head`,
			project, document, expected).Scan(&number)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		body := []byte(content)
		hash := sha256.Sum256(body)
		digest := hex.EncodeToString(hash[:])
		if _, err := tx.Exec(ctx, `
			INSERT INTO teamdocs_revisions (project, document, revision, content, sha256, author)
			VALUES ($1, $2, $3, $4, $5, $6)`, project, document, number, body, digest, member); err != nil {
			return err
		}
		revision = Revision{Project: project, Document: document, Number: number, Content: content, SHA256: digest, Author: member}
		return nil
	})
	if err != nil {
		return Revision{}, err
	}
	return revision, nil
}

func (store *Store) Approve(ctx context.Context, token, project, document string, number int64) (Revision, error) {
	if !storeID.MatchString(project) || !storeID.MatchString(document) || number <= 0 {
		return Revision{}, ErrInvalid
	}
	var revision Revision
	err := store.withMember(ctx, token, project, roleApprover, func(tx pgx.Tx, member string) error {
		var head int64
		err := tx.QueryRow(ctx, `SELECT head FROM teamdocs_heads WHERE project = $1 AND document = $2 FOR UPDATE`,
			project, document).Scan(&head)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if head != number {
			return ErrConflict
		}
		revision, err = scanRevision(tx.QueryRow(ctx, `
			UPDATE teamdocs_revisions SET approved_by = COALESCE(approved_by, $4), approved_at = COALESCE(approved_at, now())
			WHERE project = $1 AND document = $2 AND revision = $3 RETURNING `+revisionColumns,
			project, document, number, member))
		return err
	})
	if err != nil {
		return Revision{}, err
	}
	return revision, nil
}

func (store *Store) Read(ctx context.Context, token, project, document string, number int64) (Revision, error) {
	if !storeID.MatchString(project) || !storeID.MatchString(document) || number < 0 {
		return Revision{}, ErrInvalid
	}
	var revision Revision
	err := store.withMember(ctx, token, project, roleReader, func(tx pgx.Tx, _ string) error {
		var err error
		if number == 0 {
			revision, err = scanRevision(tx.QueryRow(ctx, `
				SELECT `+revisionColumns+` FROM teamdocs_revisions
				WHERE project = $1 AND document = $2 AND approved_by IS NOT NULL ORDER BY revision DESC LIMIT 1`,
				project, document))
		} else {
			revision, err = scanRevision(tx.QueryRow(ctx, `
				SELECT `+revisionColumns+` FROM teamdocs_revisions WHERE project = $1 AND document = $2 AND revision = $3`,
				project, document, number))
		}
		return err
	})
	if err != nil {
		return Revision{}, err
	}
	return revision, nil
}

const revisionColumns = `project, document, revision, content, sha256, author, COALESCE(approved_by, ''), approved_at`

func scanRevision(row pgx.Row) (Revision, error) {
	var revision Revision
	var body []byte
	err := row.Scan(&revision.Project, &revision.Document, &revision.Number, &body, &revision.SHA256, &revision.Author, &revision.ApprovedBy, &revision.ApprovedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	if err != nil {
		return Revision{}, err
	}
	revision.Content = string(body)
	return revision, nil
}
