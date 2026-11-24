package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// A2ARow stores a task's bounded event history and delivery state as one atomic revision.
type A2ARow struct {
	ID, Owner, Request, Context  string
	TargetMachine, TargetSession string
	SourceMachine, SourceSession string
	State                        string
	Revision                     int64
	Body                         json.RawMessage
	Created                      time.Time
}

var ErrA2ATargetBusy = errors.New("target already has an active A2A task")

const a2aColumns = `id, owner_key, request_id, context_id, target_machine, target_session, source_machine, source_session, state, revision, body, created_at`

func scanA2A(row pgx.Row) (*A2ARow, error) {
	r := new(A2ARow)
	err := row.Scan(&r.ID, &r.Owner, &r.Request, &r.Context, &r.TargetMachine, &r.TargetSession,
		&r.SourceMachine, &r.SourceSession, &r.State, &r.Revision, &r.Body, &r.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func a2aTerminalState(state string) bool {
	return state == "completed" || state == "failed" || state == "canceled"
}

func lockA2ATarget(ctx context.Context, tx pgx.Tx, machine, session string) error {
	key := fmt.Sprintf("a2a_target:%d:%s:%s", len(machine), machine, session)
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key)
	return err
}

func (d *DB) A2ACreate(ctx context.Context, r A2ARow) (*A2ARow, error) {
	if existing, err := d.A2ARequest(ctx, r.Owner, r.Request); existing != nil || err != nil {
		return existing, err
	}
	var out *A2ARow
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if err := lockA2ATarget(ctx, tx, r.TargetMachine, r.TargetSession); err != nil {
			return err
		}
		existing, err := scanA2A(tx.QueryRow(ctx, `SELECT `+a2aColumns+` FROM a2a_tasks WHERE owner_key=$1 AND request_id=$2`, r.Owner, r.Request))
		if err != nil {
			return err
		}
		if existing != nil {
			out = existing
			return nil
		}
		state := r.State
		if state == "submitted" {
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM a2a_tasks
				WHERE target_machine=$1 AND target_session=$2
				  AND state IN ('submitted','working','input_required','auth_required','queued')
			)`, r.TargetMachine, r.TargetSession).Scan(&busy); err != nil {
				return err
			}
			if busy {
				state = "queued"
			}
		}
		row, err := scanA2A(tx.QueryRow(ctx, `INSERT INTO a2a_tasks
			(id,owner_key,request_id,context_id,target_machine,target_session,source_machine,source_session,state,body,created_at,status_at,next_check)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
				GREATEST(clock_timestamp(), COALESCE((SELECT max(created_at) + interval '1 microsecond' FROM a2a_tasks WHERE target_machine=$5 AND target_session=$6), clock_timestamp())),
				now(),
				CASE WHEN $9 = 'queued' THEN NULL ELSE now() END)
			ON CONFLICT (owner_key,request_id) DO NOTHING RETURNING `+a2aColumns,
			r.ID, r.Owner, r.Request, r.Context, r.TargetMachine, r.TargetSession, r.SourceMachine, r.SourceSession, state, r.Body))
		if err != nil {
			return err
		}
		if row == nil {
			row, err = scanA2A(tx.QueryRow(ctx, `SELECT `+a2aColumns+` FROM a2a_tasks WHERE owner_key=$1 AND request_id=$2`, r.Owner, r.Request))
			if err != nil {
				return err
			}
		}
		out = row
		return nil
	})
	return out, err
}

func (d *DB) A2ARequest(ctx context.Context, owner, request string) (*A2ARow, error) {
	return scanA2A(d.pool.QueryRow(ctx, `SELECT `+a2aColumns+` FROM a2a_tasks WHERE owner_key=$1 AND request_id=$2`, owner, request))
}

func (d *DB) A2AGet(ctx context.Context, id string) (*A2ARow, error) {
	return scanA2A(d.pool.QueryRow(ctx, `SELECT `+a2aColumns+` FROM a2a_tasks WHERE id=$1`, id))
}

// A2AList scopes the cursor and every returned row to the caller and target (when given).
func (d *DB) A2AList(ctx context.Context, owner, machine, session, contextID, state, before string, participant bool, after *time.Time, limit int) ([]A2ARow, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+a2aColumns+` FROM a2a_tasks
		WHERE ((owner_key=$1 AND ($7 OR $2='' OR (target_machine,target_session)=($2,$3)))
		       OR ($7 AND (target_machine,target_session)=($2,$3)))
		  AND ($4='' OR context_id=$4) AND ($5='' OR state=$5 OR ($5='submitted' AND state='queued'))
		  AND ($9::timestamptz IS NULL OR status_at >= $9)
		  AND ($6='' OR (created_at,id)<(SELECT created_at,id FROM a2a_tasks WHERE id=$6
			AND ((owner_key=$1 AND ($7 OR $2='' OR (target_machine,target_session)=($2,$3)))
			     OR ($7 AND (target_machine,target_session)=($2,$3)))))
		ORDER BY created_at DESC,id DESC LIMIT $8`, owner, machine, session, contextID, state, before, participant, limit, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]A2ARow, 0)
	for rows.Next() {
		r, err := scanA2A(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// A2AUpdate compares the revision observed by the caller. Claims and receipts share
// that revision with task messages, so a cancel or report cannot erase a send in flight.
// When a task reaches a terminal state and frees its target, the oldest queued task for
// that target is promoted to submitted in the same transaction.
func (d *DB) A2AUpdate(ctx context.Context, r A2ARow, statusAt time.Time, next *time.Time) (bool, error) {
	if !a2aTerminalState(r.State) {
		result, err := d.pool.Exec(ctx, `UPDATE a2a_tasks SET state=$3, body=$4, revision=revision+1, next_check=$5, status_at=$6, target_session=$7
			WHERE id=$1 AND revision=$2`, r.ID, r.Revision, r.State, r.Body, next, statusAt, r.TargetSession)
		return result.RowsAffected() == 1, err
	}
	var updated bool
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if err := lockA2ATarget(ctx, tx, r.TargetMachine, r.TargetSession); err != nil {
			return err
		}
		var targetMachine, targetSession string
		err := tx.QueryRow(ctx, `UPDATE a2a_tasks SET state=$3, body=$4, revision=revision+1, next_check=$5, status_at=$6, target_session=$7
			WHERE id=$1 AND revision=$2
			RETURNING target_machine, target_session`,
			r.ID, r.Revision, r.State, r.Body, next, statusAt, r.TargetSession).Scan(&targetMachine, &targetSession)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		updated = true
		_, err = tx.Exec(ctx, `UPDATE a2a_tasks
			SET state='submitted', revision=revision+1, next_check=now(), status_at=now()
			WHERE id = (
				SELECT id FROM a2a_tasks
				WHERE target_machine=$1 AND target_session=$2 AND state='queued'
				  AND NOT EXISTS (
					SELECT 1 FROM a2a_tasks
					WHERE target_machine=$1 AND target_session=$2
					  AND state IN ('submitted','working','input_required','auth_required')
				  )
				ORDER BY created_at ASC, id ASC
				LIMIT 1
				FOR UPDATE
			)`, targetMachine, targetSession)
		return err
	})
	return updated, err
}

func (d *DB) A2ACount(ctx context.Context, owner, machine, session, contextID, state string, after *time.Time) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `SELECT count(*) FROM a2a_tasks
		WHERE owner_key=$1 AND (target_machine,target_session)=($2,$3)
		AND ($4='' OR context_id=$4) AND ($5='' OR state=$5 OR ($5='submitted' AND state='queued'))
		AND ($6::timestamptz IS NULL OR status_at >= $6)`, owner, machine, session, contextID, state, after).Scan(&n)
	return n, err
}

// A2AReady makes an undelivered active request due at the recipient's idle
// edge. Merely waking the poller would leave its previous eight-second lease
// in place. Sending/uncertain inputs and FIFO tails keep their leases/state.
func (d *DB) A2AReady(ctx context.Context, machine, session string) error {
	_, err := d.pool.Exec(ctx, `UPDATE a2a_tasks SET next_check=now()
		WHERE target_machine=$1 AND target_session=$2 AND state='submitted'
		  AND next_check > now()
		  AND body->'events' @> '[{"kind":"message","delivery":"queued"}]'::jsonb`, machine, session)
	return err
}

func (d *DB) A2AClaim(ctx context.Context, limit int) ([]A2ARow, error) {
	rows, err := d.pool.Query(ctx, `UPDATE a2a_tasks SET next_check=now()+interval '8 seconds'
		WHERE id IN (SELECT id FROM a2a_tasks WHERE next_check<=now() ORDER BY next_check LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING `+a2aColumns, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []A2ARow
	for rows.Next() {
		r, err := scanA2A(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// A2ALaunchesForTargets resolves durable A2A/dispatch launch rows for the exact set of
// visible target sessions on machine, with no age or global row-count truncation.
func (d *DB) A2ALaunchesForTargets(ctx context.Context, machine string, sessions []string) ([]A2ARow, error) {
	if machine == "" || len(sessions) == 0 {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT `+a2aColumns+` FROM a2a_tasks
		WHERE target_machine = $1
		  AND target_session = ANY($2::text[])
		  AND body ? 'launch'
		  AND source_session <> ''
		ORDER BY created_at DESC,id DESC`, machine, sessions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []A2ARow
	for rows.Next() {
		r, err := scanA2A(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// A2ADispatches lists recent launches and all active launches for the caller's
// dispatch tools, concurrency limit, and delegation depth check.
func (d *DB) A2ADispatches(ctx context.Context, owner, machine, session string, since time.Time) ([]A2ARow, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+a2aColumns+` FROM a2a_tasks
		WHERE (owner_key=$1 OR (target_machine,target_session)=($2,$3))
		AND body ? 'launch'
		AND ((target_machine,target_session)=($2,$3) OR created_at >= $4 OR state IN ('queued','submitted','working','input_required','auth_required'))
		ORDER BY created_at DESC,id DESC`, owner, machine, session, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []A2ARow
	for rows.Next() {
		r, err := scanA2A(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}
