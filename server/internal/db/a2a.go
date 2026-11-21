package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func (d *DB) A2ACreate(ctx context.Context, r A2ARow) (*A2ARow, error) {
	if existing, err := d.A2ARequest(ctx, r.Owner, r.Request); existing != nil || err != nil {
		return existing, err
	}
	row, err := scanA2A(d.pool.QueryRow(ctx, `INSERT INTO a2a_tasks
		(id,owner_key,request_id,context_id,target_machine,target_session,source_machine,source_session,state,body,next_check)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
		ON CONFLICT (owner_key,request_id) DO NOTHING RETURNING `+a2aColumns,
		r.ID, r.Owner, r.Request, r.Context, r.TargetMachine, r.TargetSession, r.SourceMachine, r.SourceSession, r.State, r.Body))
	if row == nil && err == nil {
		return d.A2ARequest(ctx, r.Owner, r.Request)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.ConstraintName == "a2a_tasks_active_target" {
		if existing, readErr := d.A2ARequest(ctx, r.Owner, r.Request); existing != nil || readErr != nil {
			return existing, readErr
		}
		return nil, ErrA2ATargetBusy
	}
	return row, err
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
		  AND ($4='' OR context_id=$4) AND ($5='' OR state=$5)
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
func (d *DB) A2AUpdate(ctx context.Context, r A2ARow, statusAt time.Time, next *time.Time) (bool, error) {
	result, err := d.pool.Exec(ctx, `UPDATE a2a_tasks SET state=$3, body=$4, revision=revision+1, next_check=$5, status_at=$6, target_session=$7
		WHERE id=$1 AND revision=$2`, r.ID, r.Revision, r.State, r.Body, next, statusAt, r.TargetSession)
	return result.RowsAffected() == 1, err
}

func (d *DB) A2ACount(ctx context.Context, owner, machine, session, contextID, state string, after *time.Time) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `SELECT count(*) FROM a2a_tasks
		WHERE owner_key=$1 AND (target_machine,target_session)=($2,$3)
		AND ($4='' OR context_id=$4) AND ($5='' OR state=$5)
		AND ($6::timestamptz IS NULL OR status_at >= $6)`, owner, machine, session, contextID, state, after).Scan(&n)
	return n, err
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

// A2ADispatches lists recent launches and all active launches for the caller's
// dispatch tools, concurrency limit, and delegation depth check.
func (d *DB) A2ADispatches(ctx context.Context, owner, machine, session string, since time.Time) ([]A2ARow, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+a2aColumns+` FROM a2a_tasks
		WHERE (owner_key=$1 OR (target_machine,target_session)=($2,$3))
		AND body ? 'launch'
		AND ((target_machine,target_session)=($2,$3) OR created_at >= $4 OR state IN ('submitted','working','input_required','auth_required'))
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
