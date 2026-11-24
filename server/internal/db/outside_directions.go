package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// OutsideDirection is the optional coding-Agent reference of a direction that has no Persona conversation. Empty
// MachineID and SessionID together mean a standalone direction; one without the other is never stored.
type OutsideDirection struct {
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// OutsideDirections reads the outside bindings by direction id. The direction's name, project and progress stay in
// the assistant's ordinary settings record; this table only carries what an older settings writer would drop.
func (d *DB) OutsideDirections(ctx context.Context) (map[string]OutsideDirection, error) {
	rows, err := d.pool.Query(ctx, `SELECT direction_id, machine_id, session_id FROM assistant_outside_directions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]OutsideDirection{}
	for rows.Next() {
		var id string
		var ref OutsideDirection
		if err := rows.Scan(&id, &ref.MachineID, &ref.SessionID); err != nil {
			return nil, err
		}
		out[id] = ref
	}
	return out, rows.Err()
}

// CreateOutsideDirection atomically creates its binding and writes the assistant settings that make the direction
// current. A failure leaves neither a visible direction nor an orphaned binding.
func (d *DB) CreateOutsideDirection(ctx context.Context, id string, ref OutsideDirection, values map[string]any) error {
	keys, raws, err := encodeSettings(values)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO assistant_outside_directions (direction_id, machine_id, session_id)
			VALUES ($1, $2, $3)`, id, ref.MachineID, ref.SessionID); err != nil {
			return err
		}
		return putSettings(ctx, tx, d.settingsTable(), keys, raws)
	})
}
