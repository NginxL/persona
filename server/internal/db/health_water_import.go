package db

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// HealthIntakeImport commits the entire validated file, with the same writer lock
// as add/undo. Conflicts include tombstones, which are never revived.
func (d *DB) HealthIntakeImport(ctx context.Context, rows []HealthIntakeRow) (int, error) {
	count := 0
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('health_intake', 0))`); err != nil {
			return err
		}
		for _, v := range rows {
			tag, err := tx.Exec(ctx, `INSERT INTO health_intake (id, kind, amount, at, note) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (id) DO NOTHING`, v.ID, v.Kind, v.Amount, v.At, v.Note)
			if err != nil {
				return err
			}
			count += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// HealthIntakeFinishWaterImport publishes settings, reminder timing and the receipt
// together after the extension stopped and its final records committed.
func (d *DB) HealthIntakeFinishWaterImport(ctx context.Context, settings, reminder, receipt any) error {
	values := []any{settings, reminder, receipt}
	keys := []string{"health.intake", "health.intake.water_reminder", "health.intake.water_import"}
	raw := make([][]byte, len(values))
	for i, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw[i] = b
	}
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		for i, key := range keys {
			if _, err := tx.Exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
				ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = now()`, key, raw[i]); err != nil {
				return err
			}
		}
		return nil
	})
}
