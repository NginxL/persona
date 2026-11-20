package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---- held_operations (#10661, internal/devices held.go) ----

// HeldKindMessage is a held_operations row's kind for the owner's message to an existing Agent (a session.send).
const HeldKindMessage = "message"

// A HeldOperation's states.
const (
	HeldWaiting = "waiting" // sent by itself once its computer is back
	HeldSending = "sending" // being sent now; a manager that stopped meanwhile left it so (RecoverHeldOperations)
	HeldFailed  = "failed"  // given up: the owner retries or deletes it
)

// Why a HeldOperation failed.
const (
	HeldUnclear = "unclear" // a try whose outcome is unknown: it may have arrived
	HeldRefused = "refused" // the computer refused it, nothing typed (Error says why)
	HeldExpired = "expired" // its computer was away for a whole day
	// HeldUnconfirmed: one that may have arrived was looked for in its transcript and not found, or the transcript
	// could not be read: not proof it did not arrive (the node's tie of a line to its id does not always last), so it
	// waits for the owner; a Retry of it sends it as it is.
	HeldUnconfirmed = "unconfirmed"

	// HeldSignedOut means the device credential that sent the message was revoked.
	HeldSignedOut = "signed_out"
)

// HeldOperation is a row of held_operations: an operation the owner sent to a computer while it was away, held by its
// manager. Call is the devicewire.NodeCall as the hub sent it, caller and submission included (HeldOperations leaves it
// out: a held message's pictures are read only when it goes); Text and Images are what its row shows of it (its
// params' text, how many pictures). SentAt is when the owner sent it, HeldAt when it began to wait (a Retry starts it
// again), TriedAt when its latest try began (zero: never tried). A waiting one whose Failure is HeldUnclear was retried
// by the owner after a try that may have arrived: its session's transcript is looked at before it goes.
type HeldOperation struct {
	Seq          int64
	Kind         string
	OpID         string
	MachineID    string
	SessionID    string
	Call         json.RawMessage
	Lang         string
	State        string
	Failure      string
	Error        string
	SentAt       time.Time
	HeldAt       time.Time
	TriedAt      time.Time
	Attempts     int
	Text         string
	Images       int
	CredentialID string
}

// heldShown is what a row shows of its call, read in the database (a held message's pictures never leave it for that).
const heldShown = `coalesce(call->'params'->>'text', ''),
	CASE WHEN jsonb_typeof(call->'params'->'images') = 'array' THEN jsonb_array_length(call->'params'->'images') ELSE 0 END,
	coalesce(call->'caller'->>'credential_id', '')`

const heldColumns = `seq, kind, op_id, machine_id, session_id, call, lang, state, failure, error, sent_at, held_at, tried_at, attempts, ` + heldShown

func scanHeld(row pgx.Row) (HeldOperation, error) {
	var o HeldOperation
	var tried *time.Time
	err := row.Scan(&o.Seq, &o.Kind, &o.OpID, &o.MachineID, &o.SessionID, &o.Call, &o.Lang, &o.State, &o.Failure, &o.Error,
		&o.SentAt, &o.HeldAt, &tried, &o.Attempts, &o.Text, &o.Images, &o.CredentialID)
	if tried != nil {
		o.TriedAt = *tried
	}
	return o, err
}

// HoldOperation keeps op, waiting. The same operation held already (kind, machine, session and op id) is not held
// again: that row comes back, with fresh false. A zero SentAt is now, a zero HeldAt its SentAt.
func (d *DB) HoldOperation(ctx context.Context, op HeldOperation) (HeldOperation, bool, error) {
	if op.SentAt.IsZero() {
		op.SentAt = time.Now()
	}
	if op.HeldAt.IsZero() {
		op.HeldAt = op.SentAt
	}
	held, err := scanHeld(d.pool.QueryRow(ctx, `INSERT INTO held_operations
		(kind, op_id, machine_id, session_id, call, lang, sent_at, held_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (kind, machine_id, session_id, op_id) DO NOTHING RETURNING `+heldColumns,
		op.Kind, op.OpID, op.MachineID, op.SessionID, string(op.Call), op.Lang, op.SentAt.UTC(), op.HeldAt.UTC()))
	if err == nil {
		return held, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return HeldOperation{}, false, err
	}
	held, found, err := d.HeldOperationOf(ctx, op.Kind, op.MachineID, op.SessionID, op.OpID)
	if err == nil && !found {
		err = pgx.ErrNoRows // dropped between the two statements
	}
	return held, false, err
}

// HeldOperationOf is the held operation of kind for machine, session and op id; false when there is none.
func (d *DB) HeldOperationOf(ctx context.Context, kind, machine, session, opID string) (HeldOperation, bool, error) {
	held, err := scanHeld(d.pool.QueryRow(ctx, `SELECT `+heldColumns+` FROM held_operations
		WHERE kind = $1 AND machine_id = $2 AND session_id = $3 AND op_id = $4`, kind, machine, session, opID))
	if errors.Is(err, pgx.ErrNoRows) {
		return HeldOperation{}, false, nil
	}
	return held, err == nil, err
}

// HeldOperations are every held operation of kind, in the order they were held, without their Call (Text and Images
// say what their rows show).
func (d *DB) HeldOperations(ctx context.Context, kind string) ([]HeldOperation, error) {
	rows, err := d.pool.Query(ctx, `SELECT seq, kind, op_id, machine_id, session_id, NULL::jsonb, lang, state, failure,
		error, sent_at, held_at, tried_at, attempts, `+heldShown+` FROM held_operations WHERE kind = $1 ORDER BY seq`, kind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (HeldOperation, error) { return scanHeld(r) })
}

// RecoverHeldOperations fails, as unclear, every operation of kind a manager left being sent: it stopped during the
// try, so whether it arrived is not known, and it is never sent again by itself. It says how many there were.
func (d *DB) RecoverHeldOperations(ctx context.Context, kind string) (int64, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE held_operations SET state = 'failed', failure = 'unclear', error = ''
		WHERE kind = $1 AND state = 'sending'`, kind)
	return tag.RowsAffected(), err
}

// ClaimHeldOperation marks waiting operation seq as being sent from at, before its try, and gives back the row as it
// is now (its Call to send, the attempts that name this try): false when it is not waiting (gone, or taken already),
// and then it must not be sent.
func (d *DB) ClaimHeldOperation(ctx context.Context, seq int64, at time.Time) (HeldOperation, bool, error) {
	held, err := scanHeld(d.pool.QueryRow(ctx, `UPDATE held_operations SET state = 'sending', tried_at = $2,
		attempts = attempts + 1 WHERE seq = $1 AND state = 'waiting' RETURNING `+heldColumns, seq, at.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return HeldOperation{}, false, nil
	}
	return held, err == nil, err
}

// SettleHeldOperation moves operation seq from state from to state (with why it failed and the computer's words):
// false when it was not in from.
func (d *DB) SettleHeldOperation(ctx context.Context, seq int64, from, state, failure, reason string) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE held_operations SET state = $3, failure = $4, error = $5
		WHERE seq = $1 AND state = $2`, seq, from, state, failure, reason)
	return tag.RowsAffected() == 1, err
}

// SettleHeldTry moves operation seq from being sent to state, only while the try it was judged on (its attempts then)
// is still its latest: a repair, or the owner's change over a try that is over, never overwrites a newer claim (this
// manager's after a Retry, or another's in a switch-over). False when it is not that try being sent.
func (d *DB) SettleHeldTry(ctx context.Context, seq int64, attempts int, state, failure, reason string) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE held_operations SET state = $3, failure = $4, error = $5
		WHERE seq = $1 AND state = 'sending' AND attempts = $2`, seq, attempts, state, failure, reason)
	return tag.RowsAffected() == 1, err
}

// DeleteHeldTry drops operation seq while that try (its attempts then) is the one being sent. False otherwise.
func (d *DB) DeleteHeldTry(ctx context.Context, seq int64, attempts int) (bool, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM held_operations WHERE seq = $1 AND state = 'sending' AND attempts = $2`, seq, attempts)
	return tag.RowsAffected() == 1, err
}

// RetryHeldOperation is the owner's Retry on failed operation seq: waiting again, a new wait from at. One whose try
// may have arrived (HeldUnclear) keeps that mark while it waits (check true): its transcript is looked at before it
// goes, so it is never sent twice. ok false when it is not failed (gone, waiting, or being sent: another manager on
// the same database in a switch-over may be sending it, and it must not be made to wait again and go twice).
func (d *DB) RetryHeldOperation(ctx context.Context, seq int64, at time.Time) (ok, check bool, err error) {
	var failure string
	err = d.pool.QueryRow(ctx, `UPDATE held_operations SET state = 'waiting', error = '', held_at = $2,
		failure = CASE WHEN failure = 'unclear' THEN 'unclear' ELSE '' END
		WHERE seq = $1 AND state = 'failed' AND failure <> 'signed_out' RETURNING failure`, seq, at.UTC()).Scan(&failure)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, failure == HeldUnclear, err
}

// DeleteUnsureHeld drops an operation of kind for machine, session and op id whose hold the manager could not confirm
// (the insert's answer and the look-up after it both failed): only while it waits untried, so a hold confirmed since
// (a retry of the same id) or a try begun is never touched. False when there is no such row.
func (d *DB) DeleteUnsureHeld(ctx context.Context, kind, machine, session, opID string) (bool, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM held_operations WHERE kind = $1 AND machine_id = $2 AND session_id = $3
		AND op_id = $4 AND state = 'waiting' AND attempts = 0`, kind, machine, session, opID)
	return tag.RowsAffected() == 1, err
}

// RenewHeldOperation starts waiting operation seq's wait again from at: one held behind a message that may have
// arrived waits for the owner, not for its computer. False when it is not waiting.
func (d *DB) RenewHeldOperation(ctx context.Context, seq int64, at time.Time) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE held_operations SET held_at = $2 WHERE seq = $1 AND state = 'waiting'`, seq, at.UTC())
	return tag.RowsAffected() == 1, err
}

// DeleteHeldOperation drops operation seq, only while in one of states when any are given, else only while it is not
// being sent (the owner's Delete: another manager on the same database in a switch-over may be sending it). False when
// no such row was there.
func (d *DB) DeleteHeldOperation(ctx context.Context, seq int64, states ...string) (bool, error) {
	var tag pgconn.CommandTag
	var err error
	if len(states) == 0 {
		tag, err = d.pool.Exec(ctx, `DELETE FROM held_operations WHERE seq = $1 AND state <> 'sending'`, seq)
	} else {
		tag, err = d.pool.Exec(ctx, `DELETE FROM held_operations WHERE seq = $1 AND state = ANY($2)`, seq, states)
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
