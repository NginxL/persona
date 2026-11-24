package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The user's track kinds (#13472, migrations/manager/20261123045348_assistant_track_kinds.sql): the built-in kinds
// are the manager's, never rows.

// TrackKind is one row of assistant_track_kinds.
type TrackKind struct {
	Slug  string
	Title string
	// Config is the kind's config (a JSON object), checked by the manager.
	Config    []byte
	Builtin   bool
	Revision  int64
	CreatedAt time.Time
}

// TrackKinds are the user's kinds, by slug.
func (d *DB) TrackKinds(ctx context.Context) ([]TrackKind, error) {
	rows, err := d.pool.Query(ctx, `SELECT slug, title, config, builtin, revision, created_at FROM assistant_track_kinds ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackKind
	for rows.Next() {
		var k TrackKind
		if err := rows.Scan(&k.Slug, &k.Title, &k.Config, &k.Builtin, &k.Revision, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// InsertTrackKind adds kind k (its revision 1). ErrTrackExists when its slug is taken.
func (d *DB) InsertTrackKind(ctx context.Context, k TrackKind) error {
	tag, err := d.pool.Exec(ctx, `INSERT INTO assistant_track_kinds (slug, title, config) VALUES ($1, $2, $3) ON CONFLICT (slug) DO NOTHING`,
		k.Slug, k.Title, string(k.Config))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTrackExists
	}
	return nil
}

// UpdateTrackKind writes k's title and config over the row of its slug, if that row is still at revision; the new
// revision is returned.
func (d *DB) UpdateTrackKind(ctx context.Context, k TrackKind, revision int64) (int64, error) {
	var next int64
	err := d.pool.QueryRow(ctx, `UPDATE assistant_track_kinds SET title = $3, config = $4, revision = revision + 1
		WHERE slug = $1 AND revision = $2 RETURNING revision`, k.Slug, revision, k.Title, string(k.Config)).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM assistant_track_kinds WHERE slug = $1)`, k.Slug).Scan(&exists); err != nil {
			return 0, err
		}
		if !exists {
			return 0, ErrTrackNotFound
		}
		return 0, ErrTrackRevision
	}
	return next, err
}
