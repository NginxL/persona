package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Quick capture (#13502, migrations/manager/20261123053622_assistant_captures.sql): the manager checks what a capture
// is and what its status may become; the store keeps what it is given, one capture per client id.

// Capture is one row of assistant_captures. Sort and FiledTo are JSON objects ("{}" for none).
type Capture struct {
	ID        string
	ClientID  string
	CreatedAt time.Time
	Source    string
	Kind      string
	Text      string
	URL       string
	SpaceHint string
	Status    string
	Sort      []byte
	FiledTo   []byte
	Attempts  int
	Error     string
	UpdatedAt time.Time
}

const captureColumns = `id, client_id, created_at, source, kind, text, url, space_hint, status, sort, filed_to, attempts, error, updated_at`

func scanCapture(row pgx.Row) (Capture, error) {
	var c Capture
	err := row.Scan(&c.ID, &c.ClientID, &c.CreatedAt, &c.Source, &c.Kind, &c.Text, &c.URL, &c.SpaceHint, &c.Status, &c.Sort,
		&c.FiledTo, &c.Attempts, &c.Error, &c.UpdatedAt)
	return c, err
}

// InsertCapture keeps c as a new capture (status new) and answers it as kept, created true. A capture of the same
// client id kept before is answered instead, as it is now, created false: a device's retry is one capture.
func (d *DB) InsertCapture(ctx context.Context, c Capture) (Capture, bool, error) {
	got, err := scanCapture(d.pool.QueryRow(ctx, `INSERT INTO assistant_captures (id, client_id, source, kind, text, url, space_hint)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (client_id) DO NOTHING RETURNING `+captureColumns,
		c.ID, c.ClientID, c.Source, c.Kind, c.Text, c.URL, c.SpaceHint))
	if err == nil {
		return got, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Capture{}, false, err
	}
	// Kept before (by this statement's start, or by a writer it waited for: a statement of its own sees that one).
	got, err = scanCapture(d.pool.QueryRow(ctx, `SELECT `+captureColumns+` FROM assistant_captures WHERE client_id = $1`, c.ClientID))
	return got, false, err
}

// CaptureFilter selects captures: of these statuses (none: any), changed at or after UpdatedSince (zero: any time), at
// most Limit (0: 200), the newest first.
type CaptureFilter struct {
	Statuses     []string
	UpdatedSince time.Time
	Limit        int
}

// Captures are the captures f selects.
func (d *DB) Captures(ctx context.Context, f CaptureFilter) ([]Capture, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	statuses := f.Statuses
	if statuses == nil {
		statuses = []string{}
	}
	rows, err := d.pool.Query(ctx, `SELECT `+captureColumns+` FROM assistant_captures
		WHERE (cardinality($1::text[]) = 0 OR status = ANY($1)) AND updated_at >= $2
		ORDER BY created_at DESC, id DESC LIMIT $3`, statuses, f.UpdatedSince, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Capture
	for rows.Next() {
		c, err := scanCapture(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CapturesToSort are the captures the manager's work loop takes up, at most limit: new ones, and sorted ones meant to
// be filed on their own (sort.auto_file) that were not yet, each tried fewer than maxAttempts times. New ones come
// first, then the oldest: captures waiting for a computer that can file them count no try, so they must never fill a
// batch ahead of a new one.
func (d *DB) CapturesToSort(ctx context.Context, maxAttempts, limit int) ([]Capture, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+captureColumns+` FROM assistant_captures
		WHERE attempts < $1 AND (status = 'new' OR (status = 'sorted' AND (sort->>'auto_file')::boolean IS TRUE))
		ORDER BY (status = 'new') DESC, created_at, id LIMIT $2`, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Capture
	for rows.Next() {
		c, err := scanCapture(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetCaptureResult records what the work loop made of capture id: its status, sort and filed_to (nil keeps each as it
// is), the error of a try that failed ("" clears it), and one more try when tried. Only a capture still new or sorted
// changes, so the loop never overwrites what the user did meanwhile; changed says whether it did.
func (d *DB) SetCaptureResult(ctx context.Context, id, status string, sort, filedTo []byte, errText string, tried bool) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE assistant_captures SET status = $2, sort = coalesce($3::jsonb, sort),
		filed_to = coalesce($4::jsonb, filed_to), error = $5, attempts = attempts + CASE WHEN $6 THEN 1 ELSE 0 END, updated_at = now()
		WHERE id = $1 AND status IN ('new', 'sorted')`, id, status, nullJSON(sort), nullJSON(filedTo), errText, tried)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// nullJSON is b as a parameter: NULL for nil, so coalesce keeps the column.
func nullJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}
