package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// The kinds of a SessionModeChange (#10297).
const (
	ModeChangeSet            = "set"             // a mode picked through the hub's /modes route
	ModeChangeRestartDone    = "restart_done"    // a queued restart into Full access ran: the agent runs in it now
	ModeChangeRestartFailed  = "restart_failed"  // it ran and did not come up in the mode (the agent is as before, or stopped)
	ModeChangeRestartDropped = "restart_dropped" // it never ran: another mode picked, its process gone, too old
)

// How a ModeChangeSet took effect.
const (
	ModeAppliedNow     = "now"     // the agent runs in it already (shift+tab, or the next message of an assistant chat)
	ModeAppliedRestart = "restart" // a restart into it waits for the turn to end (node/mode_restart.go)
)

// SessionModeChange is a row of session_mode_changes: a permission mode change of a running session, or what became
// of a queued restart into Full access (#10297). Applied is "" where an older node did not say; who asked is the
// actor's kind ("device", "hub" for the hub's own token, "extension") with the device's id and name as the hub knew them.
type SessionModeChange struct {
	MachineID  string    `json:"-"`
	SessionID  string    `json:"-"`
	Kind       string    `json:"kind"`
	From       string    `json:"from,omitempty"`
	To         string    `json:"to,omitempty"`
	Applied    string    `json:"applied,omitempty"`
	ActorKind  string    `json:"actor_kind,omitempty"`
	DeviceID   string    `json:"device_id,omitempty"`
	DeviceName string    `json:"device_name,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	At         time.Time `json:"at"`
}

// AddSessionModeChange records c; a zero At is now.
func (d *DB) AddSessionModeChange(ctx context.Context, c SessionModeChange) error {
	if c.At.IsZero() {
		c.At = time.Now()
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO session_mode_changes
		(machine_id, session_id, kind, from_mode, to_mode, applied, actor_kind, device_id, device_name, reason, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		c.MachineID, c.SessionID, c.Kind, c.From, c.To, c.Applied, c.ActorKind, c.DeviceID, c.DeviceName, c.Reason, c.At.UTC())
	return err
}

// SessionModeChanges are a session's latest changes, at most limit, oldest first.
func (d *DB) SessionModeChanges(ctx context.Context, machine, session string, limit int) ([]SessionModeChange, error) {
	rows, err := d.pool.Query(ctx, `SELECT kind, from_mode, to_mode, applied, actor_kind, device_id, device_name, reason, at
		FROM (SELECT * FROM session_mode_changes WHERE machine_id = $1 AND session_id = $2 ORDER BY at DESC, id DESC LIMIT $3) latest
		ORDER BY at, id`, machine, session, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SessionModeChange, error) {
		c := SessionModeChange{MachineID: machine, SessionID: session}
		err := r.Scan(&c.Kind, &c.From, &c.To, &c.Applied, &c.ActorKind, &c.DeviceID, &c.DeviceName, &c.Reason, &c.At)
		return c, err
	})
}
