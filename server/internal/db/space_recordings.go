package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The recordings saved into a track (#13501, migrations/manager/20261123054045_assistant_space_recordings.sql): the
// transcript is a file in the track's recordings/ on the assistant's machine; a row is its sync state and tombstone.

// SpaceRecording is one row of assistant_space_recordings.
type SpaceRecording struct {
	ID          string
	Slug        string
	FileName    string
	Revision    int64
	StartedAt   time.Time
	Complete    bool
	UpdatedAt   time.Time
	DeletedAt   *time.Time
	FileRemoved bool
}

const spaceRecordingColumns = `id, slug, file_name, revision, started_at, complete, updated_at, deleted_at, file_removed`

func scanSpaceRecording(row pgx.Row) (SpaceRecording, error) {
	var r SpaceRecording
	err := row.Scan(&r.ID, &r.Slug, &r.FileName, &r.Revision, &r.StartedAt, &r.Complete, &r.UpdatedAt, &r.DeletedAt, &r.FileRemoved)
	return r, err
}

// SpaceRecording is the row of recording id; ok false when there is none.
func (d *DB) SpaceRecording(ctx context.Context, id string) (SpaceRecording, bool, error) {
	r, err := scanSpaceRecording(d.pool.QueryRow(ctx, `SELECT `+spaceRecordingColumns+` FROM assistant_space_recordings WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SpaceRecording{}, false, nil
	}
	return r, err == nil, err
}

// SpaceRecordings are every row, tombstones included, oldest start first.
func (d *DB) SpaceRecordings(ctx context.Context) ([]SpaceRecording, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+spaceRecordingColumns+` FROM assistant_space_recordings ORDER BY started_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpaceRecording
	for rows.Next() {
		r, err := scanSpaceRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutSpaceRecording writes r over the row of its id unless that row is a tombstone or holds a higher revision; false
// when nothing was written.
func (d *DB) PutSpaceRecording(ctx context.Context, r SpaceRecording) (bool, error) {
	tag, err := d.pool.Exec(ctx, `INSERT INTO assistant_space_recordings (id, slug, file_name, revision, started_at, complete, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET slug = EXCLUDED.slug, file_name = EXCLUDED.file_name, revision = EXCLUDED.revision,
			started_at = EXCLUDED.started_at, complete = EXCLUDED.complete, updated_at = EXCLUDED.updated_at
		WHERE assistant_space_recordings.deleted_at IS NULL AND assistant_space_recordings.revision <= EXCLUDED.revision`,
		r.ID, r.Slug, r.FileName, r.Revision, r.StartedAt, r.Complete, r.UpdatedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteSpaceRecording turns the row of id into a tombstone at now (removed: the file is gone already); false when
// there is no row, or it was one already.
func (d *DB) DeleteSpaceRecording(ctx context.Context, id string, now time.Time, removed bool) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE assistant_space_recordings SET deleted_at = $2, file_removed = $3
		WHERE id = $1 AND deleted_at IS NULL`, id, now, removed)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SpaceRecordingRemoved notes that the file of tombstone id is gone.
func (d *DB) SpaceRecordingRemoved(ctx context.Context, id string) error {
	_, err := d.pool.Exec(ctx, `UPDATE assistant_space_recordings SET file_removed = true WHERE id = $1 AND deleted_at IS NOT NULL`, id)
	return err
}

// ForgetSpaceRecordings drops the tombstones whose file is gone and whose recording started before cutoff: past the
// retention window an upload of one is expired anyway.
func (d *DB) ForgetSpaceRecordings(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM assistant_space_recordings WHERE deleted_at IS NOT NULL AND file_removed AND started_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
