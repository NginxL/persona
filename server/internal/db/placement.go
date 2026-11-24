package db

import (
	"context"
	"encoding/json"
	"time"
)

type PlacementRow struct {
	ID, TaskID, Machine, SessionID string
	At                             time.Time
	Decision                       json.RawMessage
}

func (d *DB) PutPlacement(ctx context.Context, p PlacementRow) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO agent_placements (id, task_id, machine, session_id, started_at, decision)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO UPDATE SET
		task_id=EXCLUDED.task_id, machine=EXCLUDED.machine, session_id=EXCLUDED.session_id, decision=EXCLUDED.decision`,
		p.ID, p.TaskID, p.Machine, p.SessionID, p.At, p.Decision)
	return err
}
