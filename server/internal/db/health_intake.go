package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// HealthIntakeRow is an immutable measurement, with a new sequence number when undone.
// Undone rows stay indefinitely so an offline client can remove its own health samples.
type HealthIntakeRow struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Amount    float64    `json:"amount"`
	At        time.Time  `json:"at"`
	Note      string     `json:"note"`
	Seq       int64      `json:"seq"`
	CreatedAt time.Time  `json:"created_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

const healthIntakeColumns = `id::text, kind, amount::double precision, at, note, seq, created_at, deleted_at`

func scanHealthIntake(row pgx.Row) (v HealthIntakeRow, err error) {
	err = row.Scan(&v.ID, &v.Kind, &v.Amount, &v.At, &v.Note, &v.Seq, &v.CreatedAt, &v.DeletedAt)
	return
}

// HealthIntakeAdd records an id once and returns the original row on a retry, even after undo.
// Every writer takes the same transaction lock before nextval: sequence order is commit order,
// including across manager processes sharing this database. A sync cursor cannot skip a late commit.
func (d *DB) HealthIntakeAdd(ctx context.Context, v HealthIntakeRow) (saved HealthIntakeRow, changed bool, err error) {
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('health_intake', 0))`); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO health_intake (id, kind, amount, at, note) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (id) DO NOTHING`, v.ID, v.Kind, v.Amount, v.At, v.Note)
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() == 1
		saved, err = scanHealthIntake(tx.QueryRow(ctx, `SELECT `+healthIntakeColumns+` FROM health_intake WHERE id = $1`, v.ID))
		return err
	})
	return
}

// HealthIntakeUndo marks a row once. A repeated undo succeeds without moving its sequence;
// a missing id returns nil. It shares HealthIntakeAdd's lock until the transaction commits.
func (d *DB) HealthIntakeUndo(ctx context.Context, id string) (saved *HealthIntakeRow, changed bool, err error) {
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('health_intake', 0))`); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE health_intake SET deleted_at = now(), seq = nextval('health_intake_seq')
			WHERE id = $1 AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() == 1
		v, err := scanHealthIntake(tx.QueryRow(ctx, `SELECT `+healthIntakeColumns+` FROM health_intake WHERE id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			saved = &v
		}
		return err
	})
	return
}

// HealthIntakeRange lists active rows in [from, until), newest first. Calendar boundaries belong
// to the manager, which uses the user's time zone and calendar days rather than 24-hour durations.
func (d *DB) HealthIntakeRange(ctx context.Context, from, until time.Time) ([]HealthIntakeRow, error) {
	return d.healthIntakeRows(ctx, `SELECT `+healthIntakeColumns+` FROM health_intake
		WHERE deleted_at IS NULL AND kind = ANY($3::text[]) AND at >= $1 AND at < $2
		ORDER BY at DESC, id`, from, until, []string{"water", "protein"})
}

// HealthIntakeChanges lists the latest state of each changed row after a cursor, including tombstones.
// An add followed by an undo before reading is returned as one tombstone, at its newer sequence.
func (d *DB) HealthIntakeChanges(ctx context.Context, after int64, limit int) ([]HealthIntakeRow, error) {
	return d.healthIntakeRows(ctx, `SELECT `+healthIntakeColumns+` FROM health_intake WHERE seq > $1 ORDER BY seq LIMIT $2`, after, limit)
}

func (d *DB) healthIntakeRows(ctx context.Context, query string, args ...any) ([]HealthIntakeRow, error) {
	rows, err := d.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HealthIntakeRow{}
	for rows.Next() {
		v, err := scanHealthIntake(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
