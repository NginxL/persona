package db

import (
	"context"
	"encoding/json"
	"time"
)

func northStarWeeklySchema(ctx context.Context, d *DB, table string, columns ...string) (bool, error) {
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = ANY($2)`, table, columns).Scan(&n); err != nil {
		return false, err
	}
	return n == len(columns), nil
}

// NorthStarWeeklyFinishedTodos lists the non-cancelled to-dos closed in [since, until), excluding failed starts.
// The ids stay inside the command's aggregation; its JSON contains counts only.
func (d *DB) NorthStarWeeklyFinishedTodos(ctx context.Context, since, until time.Time) ([]string, bool, error) {
	ready, err := northStarWeeklySchema(ctx, d, "todos", "id", "done_at", "start_error", "done_by")
	if err != nil || !ready {
		return nil, ready, err
	}
	ready, err = northStarWeeklySchema(ctx, d, "todo_supervision", "todo_id", "state")
	if err != nil || !ready {
		return nil, ready, err
	}
	rows, err := d.pool.Query(ctx, `SELECT t.id FROM todos t
		LEFT JOIN todo_supervision s ON s.todo_id = t.id
		WHERE t.done_at >= $1 AND t.done_at < $2
		AND coalesce(t.start_error, '') = ''
		AND coalesce(t.done_by, '') <> 'cancelled'
		AND coalesce(s.state, '') <> 'cancelled'
		ORDER BY t.done_at, t.id`, since, until)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, true, err
		}
		ids = append(ids, id)
	}
	return ids, true, rows.Err()
}

// NorthStarWeeklyTaskPlans returns started plans for the command's in-memory workspace lookup.
func (d *DB) NorthStarWeeklyTaskPlans(ctx context.Context) ([]TaskPlanRow, bool, error) {
	ready, err := northStarWeeklySchema(ctx, d, "task_plans", "id", "conversation", "tasks", "state", "created_at", "updated_at")
	if err != nil || !ready {
		return nil, ready, err
	}
	rows, err := d.pool.Query(ctx, `SELECT id, conversation, tasks, state, created_at, updated_at
		FROM task_plans WHERE state = 'started' ORDER BY created_at, id`)
	if err != nil {
		return nil, true, err
	}
	plans, err := scanTaskPlans(rows)
	return plans, true, err
}

// NorthStarWeeklyAutoReviews returns the latest stored review per to-do; older runs exist only as reviewer links.
func (d *DB) NorthStarWeeklyAutoReviews(ctx context.Context) ([]json.RawMessage, bool, error) {
	ready, err := northStarWeeklySchema(ctx, d, "todo_supervision", "todo_id", "auto_review")
	if err != nil || !ready {
		return nil, ready, err
	}
	rows, err := d.pool.Query(ctx, `SELECT auto_review FROM todo_supervision WHERE auto_review IS NOT NULL`)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return nil, true, err
		}
		out = append(out, raw)
	}
	return out, true, rows.Err()
}

// NorthStarWeeklyTodoAgents returns links for in-memory review, session-age and worker/leader aggregation.
func (d *DB) NorthStarWeeklyTodoAgents(ctx context.Context) ([]TodoAgentRow, bool, error) {
	ready, err := northStarWeeklySchema(ctx, d, "todo_agents", "todo_id", "machine_id", "session_id", "linked_at", "released_at",
		"title", "cwd", "agent", "start_error", "source", "via", "dismissed_at")
	if err != nil || !ready {
		return nil, ready, err
	}
	links, err := d.ListAllTodoAgents(ctx)
	return links, true, err
}

// NorthStarWeeklyContinuations returns the times of explicit to-do continuation links.
func (d *DB) NorthStarWeeklyContinuations(ctx context.Context) ([]time.Time, bool, error) {
	ready, err := northStarWeeklySchema(ctx, d, "task_brief_todos", "todo_id", "brief_id", "continues", "linked_at")
	if err != nil || !ready {
		return nil, ready, err
	}
	rows, err := d.pool.Query(ctx, `SELECT linked_at FROM task_brief_todos WHERE continues <> '' ORDER BY linked_at, todo_id`)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	out := []time.Time{}
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			return nil, true, err
		}
		out = append(out, at)
	}
	return out, true, rows.Err()
}
