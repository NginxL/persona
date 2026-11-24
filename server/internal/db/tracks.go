package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The assistant's tracks (#13104, migrations/manager/20261123010000_assistant_tracks.sql): the registry the manager
// reads to tell a track's kind, directory, template and default categories. The main space ('') is not a row.

// Track is one row of assistant_tracks.
type Track struct {
	Slug        string
	Kind        string
	Title       string
	Icon        string
	Description string
	// DefaultCategories are the information categories everything produced in the track carries at least.
	DefaultCategories []string
	Template          string
	// ConfigOverride is what the track sets apart from its kind's defaults (a JSON object).
	ConfigOverride  []byte
	DirName         string
	MachineID       string
	DefaultLauncher string
	DefaultModel    string
	Builtin         bool
	CreatedAt       time.Time
	ArchivedAt      *time.Time
	Revision        int64
}

// Tracks are every registered track, archived ones included, by slug.
func (d *DB) Tracks(ctx context.Context) ([]Track, error) {
	rows, err := d.pool.Query(ctx, `SELECT slug, kind, title, icon, description, default_categories, template, config_override, dir_name, machine_id,
		default_launcher, default_model, builtin, created_at, archived_at, revision
		FROM assistant_tracks ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Track
	for rows.Next() {
		var s Track
		if err := rows.Scan(&s.Slug, &s.Kind, &s.Title, &s.Icon, &s.Description, &s.DefaultCategories, &s.Template, &s.ConfigOverride, &s.DirName, &s.MachineID,
			&s.DefaultLauncher, &s.DefaultModel, &s.Builtin, &s.CreatedAt, &s.ArchivedAt, &s.Revision); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Errors of a registry write.
var (
	// ErrTrackExists: a track (or kind) of that slug is registered already.
	ErrTrackExists = errors.New("already registered")
	// ErrTrackNotFound: no track (or kind) of that slug is registered.
	ErrTrackNotFound = errors.New("not registered")
	// ErrTrackRevision: the row changed since the revision the write was made against.
	ErrTrackRevision = errors.New("changed meanwhile")
)

// InsertTrack registers t (its revision 1). ErrTrackExists when its slug is taken.
func (d *DB) InsertTrack(ctx context.Context, t Track) error {
	override := t.ConfigOverride
	if len(override) == 0 {
		override = []byte("{}")
	}
	cats := t.DefaultCategories
	if cats == nil {
		cats = []string{}
	}
	tag, err := d.pool.Exec(ctx, `INSERT INTO assistant_tracks (slug, kind, title, icon, description, default_categories, template, config_override,
		dir_name, machine_id, default_launcher, default_model)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT (slug) DO NOTHING`,
		t.Slug, t.Kind, t.Title, t.Icon, t.Description, cats, t.Template, string(override), t.DirName, t.MachineID, t.DefaultLauncher, t.DefaultModel)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTrackExists
	}
	return nil
}

// UpdateTrack writes t's changeable fields (title, icon, description, default categories, template, config override,
// archived) over the row of its slug, if that row is still at revision; the row's revision goes up by one, and the
// new one is returned.
func (d *DB) UpdateTrack(ctx context.Context, t Track, revision int64) (int64, error) {
	override := t.ConfigOverride
	if len(override) == 0 {
		override = []byte("{}")
	}
	cats := t.DefaultCategories
	if cats == nil {
		cats = []string{}
	}
	var next int64
	err := d.pool.QueryRow(ctx, `UPDATE assistant_tracks SET title = $3, icon = $4, description = $5, default_categories = $6, template = $7,
		config_override = $8, archived_at = $9, revision = revision + 1
		WHERE slug = $1 AND revision = $2 RETURNING revision`,
		t.Slug, revision, t.Title, t.Icon, t.Description, cats, t.Template, string(override), t.ArchivedAt).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM assistant_tracks WHERE slug = $1)`, t.Slug).Scan(&exists); err != nil {
			return 0, err
		}
		if !exists {
			return 0, ErrTrackNotFound
		}
		return 0, ErrTrackRevision
	}
	return next, err
}
