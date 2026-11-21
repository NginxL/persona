package db

import (
	"context"
	"encoding/json"
	"time"
)

// TaskEstimateCohort is the authority's classification of a new plan at
// dispatch. Local repository paths are compared only on the same machine.
type TaskEstimateCohort struct {
	Kind       string
	Readonly   bool
	Role       string
	Tier       string
	Machine    string
	Repository string
}

type TaskEstimateRound struct {
	ID         string
	Todo       string
	Brief      string
	Plan       string
	Dispatched time.Time
	Cohort     TaskEstimateCohort
}

// CreateTaskEstimateRound records the cohort once, after its actual dispatch
// event was stored. Replays cannot replace a round's cohort.
func (d *DB) CreateTaskEstimateRound(ctx context.Context, r TaskEstimateRound) error {
	c := r.Cohort
	_, err := d.pool.Exec(ctx, `
		INSERT INTO task_estimate_rounds (
			round_id, todo_id, brief_id, plan_id, dispatched_at, kind, read_only, role, tier, machine_id, repository
		)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
		FROM task_brief_todos l JOIN task_briefs b ON b.id = l.brief_id
		WHERE l.todo_id = $2 AND b.id = $3
		  AND (b.state -> 'log') @> jsonb_build_array(jsonb_build_object(
			'kind', 'dispatch', 'source', 'task_plan', 'round_id', $1::text, 'plan_id', $4::text, 'todo_id', $2::text, 'at', $12::text))
		ON CONFLICT (round_id) DO NOTHING`,
		r.ID, r.Todo, r.Brief, r.Plan, r.Dispatched, c.Kind, c.Readonly, c.Role, c.Tier, c.Machine, c.Repository,
		r.Dispatched.UTC().Format(time.RFC3339Nano))
	return err
}

// currentTaskEstimateDispatch matches only the latest durable dispatch for
// this to-do. A shared brief can have many rounds or continuation to-dos.
const currentTaskEstimateDispatch = `(
	SELECT item ->> 'round_id'
	FROM jsonb_array_elements(CASE WHEN jsonb_typeof(b.state -> 'log') = 'array'
		THEN b.state -> 'log' ELSE '[]'::jsonb END) WITH ORDINALITY AS e(item, position)
	WHERE item ->> 'kind' = 'dispatch' AND item ->> 'todo_id' = r.todo_id
	ORDER BY position DESC LIMIT 1
) = r.round_id`

// AdmitTaskEstimateRound records successful durable attachment to the original
// cohort. A start rerouted to another machine or repository cannot train that
// cohort. A failed start, missing link or superseded dispatch supplies no row.
func (d *DB) AdmitTaskEstimateRound(ctx context.Context, round, plan, todo, session string, c TaskEstimateCohort) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE task_estimate_rounds r SET admitted_at = a.linked_at, session_id = a.session_id
		FROM todo_agents a, task_briefs b
		WHERE r.round_id = $1 AND r.plan_id = $2 AND r.todo_id = $3
		  AND r.machine_id = $4 AND r.repository = $6 AND r.admitted_at IS NULL
		  AND r.kind = $7 AND r.read_only = $8 AND r.role = $9 AND r.tier = $10
		  AND a.todo_id = r.todo_id AND a.machine_id = $4 AND a.session_id = $5
		  AND a.linked_at >= r.dispatched_at AND a.source <> 'review'
		  AND b.id = r.brief_id AND `+currentTaskEstimateDispatch,
		round, plan, todo, c.Machine, session, c.Repository, c.Kind, c.Readonly, c.Role, c.Tier)
	return err
}

// AcceptTaskEstimateRound learns the first recorded duration exactly once,
// only for an admitted round the supervisor actually closed as accepted.
func (d *DB) AcceptTaskEstimateRound(ctx context.Context, round, todo string, completed time.Time, actual int) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE task_estimate_rounds r SET completed_at = $3, accepted_at = t.done_at, actual_minutes = $4
		FROM todos t, task_brief_todos l, task_briefs b
		WHERE r.round_id = $1 AND r.todo_id = $2 AND r.accepted_at IS NULL
		  AND r.admitted_at IS NOT NULL AND $3 >= r.admitted_at AND $4 > 0
		  AND t.id = r.todo_id AND t.done_at >= r.admitted_at AND t.done_by = $6 AND t.start_error = ''
		  AND (t.reopened_at IS NULL OR t.reopened_at <= r.dispatched_at)
		  AND l.todo_id = r.todo_id AND l.brief_id = r.brief_id AND b.id = r.brief_id
		  AND `+currentTaskEstimateDispatch+`
		  AND (b.state -> 'log') @> jsonb_build_array(jsonb_build_object(
			'kind', 'completion', 'round_id', $1::text, 'todo_id', $2::text,
			'at', $5::text, 'actual_minutes', $4::integer))`,
		round, todo, completed, actual, completed.UTC().Format(time.RFC3339Nano), DoneByReview)
	return err
}

// ReconcileTaskEstimateRounds recovers a bounded set of accepted rounds
// whose durable source committed before the estimate projection did.
func (d *DB) ReconcileTaskEstimateRounds(ctx context.Context, c TaskEstimateCohort, since, now time.Time, limit int) error {
	rows, err := d.pool.Query(ctx, `
		SELECT r.round_id, r.todo_id, completion.event
		FROM task_estimate_rounds r
		JOIN todos t ON t.id = r.todo_id
		JOIN task_brief_todos l ON l.todo_id = r.todo_id AND l.brief_id = r.brief_id
		JOIN task_briefs b ON b.id = r.brief_id
		CROSS JOIN LATERAL (
			SELECT item AS event
			FROM jsonb_array_elements(CASE WHEN jsonb_typeof(b.state -> 'log') = 'array'
				THEN b.state -> 'log' ELSE '[]'::jsonb END) WITH ORDINALITY AS e(item, position)
			WHERE item ->> 'kind' = 'completion' AND item ->> 'todo_id' = r.todo_id
			  AND item ->> 'round_id' = r.round_id
			ORDER BY position LIMIT 1
		) AS completion
		WHERE r.kind = $1 AND r.read_only = $2 AND r.role = $3 AND r.tier = $4
		  AND r.accepted_at IS NULL AND r.admitted_at IS NOT NULL
		  AND t.done_at BETWEEN $5 AND $6 AND t.done_at >= r.admitted_at
		  AND t.done_by = $8 AND t.start_error = ''
		  AND (t.reopened_at IS NULL OR t.reopened_at <= r.dispatched_at)
		  AND `+currentTaskEstimateDispatch+`
		ORDER BY t.done_at DESC, r.round_id DESC LIMIT $7`,
		c.Kind, c.Readonly, c.Role, c.Tier, since, now, limit, DoneByReview)
	if err != nil {
		return err
	}
	defer rows.Close()

	type proof struct {
		round, todo string
		At          time.Time `json:"at"`
		Minutes     int       `json:"actual_minutes"`
	}
	var proofs []proof
	for rows.Next() {
		var row proof
		var event []byte
		if err := rows.Scan(&row.round, &row.todo, &event); err != nil {
			return err
		}
		if json.Unmarshal(event, &row) == nil && !row.At.IsZero() && row.Minutes > 0 {
			proofs = append(proofs, row)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, row := range proofs {
		// Recheck the current durable proof before writing. A concurrent
		// reopen or new dispatch can make a selected row ineligible.
		if err := d.AcceptTaskEstimateRound(ctx, row.round, row.todo, row.At, row.Minutes); err != nil {
			return err
		}
	}
	return nil
}

type TaskEstimateSample struct {
	Machine       string
	Repository    string
	ActualMinutes int
}

// RecentTaskEstimateSamples returns bounded recent samples whose exact
// accepted closure still stands. Reopening removes a sample from learning.
// Manager databases never share calibration history.
func (d *DB) RecentTaskEstimateSamples(ctx context.Context, c TaskEstimateCohort, since, now time.Time, limit int) ([]TaskEstimateSample, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT r.machine_id, r.repository, r.actual_minutes
		FROM task_estimate_rounds r JOIN todos t ON t.id = r.todo_id
		WHERE r.kind = $1 AND r.read_only = $2 AND r.role = $3 AND r.tier = $4
		  AND r.accepted_at >= $5 AND r.accepted_at <= $6 AND r.actual_minutes IS NOT NULL
		  AND t.done_at = r.accepted_at AND t.done_by = $8 AND t.start_error = ''
		  AND (t.reopened_at IS NULL OR t.reopened_at <= r.dispatched_at)
		ORDER BY r.accepted_at DESC, r.round_id DESC LIMIT $7`,
		c.Kind, c.Readonly, c.Role, c.Tier, since, now, limit, DoneByReview)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskEstimateSample
	for rows.Next() {
		var sample TaskEstimateSample
		if err := rows.Scan(&sample.Machine, &sample.Repository, &sample.ActualMinutes); err != nil {
			return nil, err
		}
		out = append(out, sample)
	}
	return out, rows.Err()
}
