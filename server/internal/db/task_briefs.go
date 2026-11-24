package db

import (
	"context"
	"encoding/json"
	"time"
)

// TaskBriefRow is a task's brief (#3822): Brief is what was asked when the work was handed out, written once and never
// again; State is what happened to it since (scope changes, the overdue mark, the result), JSON the assistant owns.
type TaskBriefRow struct {
	ID        string
	Brief     json.RawMessage
	State     json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TaskBriefTodoRow says which brief a to-do works for, and the to-do it continues (with why) when it took over.
type TaskBriefTodoRow struct {
	TodoID    string
	BriefID   string
	Continues string
	Reason    string
	LinkedAt  time.Time
}

// ListTaskBriefs returns every brief and every to-do's link to one (links oldest first).
func (d *DB) ListTaskBriefs(ctx context.Context) ([]TaskBriefRow, []TaskBriefTodoRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT id, brief, state, created_at, updated_at FROM task_briefs`)
	if err != nil {
		return nil, nil, err
	}
	var briefs []TaskBriefRow
	for rows.Next() {
		var r TaskBriefRow
		var brief, state []byte
		if err := rows.Scan(&r.ID, &brief, &state, &r.CreatedAt, &r.UpdatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		r.Brief, r.State = brief, state
		briefs = append(briefs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = d.pool.Query(ctx, `SELECT todo_id, brief_id, continues, reason, linked_at FROM task_brief_todos ORDER BY linked_at, todo_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var links []TaskBriefTodoRow
	for rows.Next() {
		var l TaskBriefTodoRow
		if err := rows.Scan(&l.TodoID, &l.BriefID, &l.Continues, &l.Reason, &l.LinkedAt); err != nil {
			return nil, nil, err
		}
		links = append(links, l)
	}
	return briefs, links, rows.Err()
}

// CreateTaskBrief writes a brief once: one already there under that id stays as it is (false), since a brief is never
// rewritten.
func (d *DB) CreateTaskBrief(ctx context.Context, r TaskBriefRow) (bool, error) {
	state := r.State
	if len(state) == 0 {
		state = json.RawMessage(`{}`)
	}
	tag, err := d.pool.Exec(ctx, `INSERT INTO task_briefs (id, brief, state, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING`, r.ID, []byte(r.Brief), []byte(state), r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// PutTaskBriefState writes what happened to brief id since it was made (its log, overdue mark and result), never the
// brief itself; false when there is no such brief.
func (d *DB) PutTaskBriefState(ctx context.Context, id string, state json.RawMessage, at time.Time) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE task_briefs SET state = $2, updated_at = $3 WHERE id = $1`, id, []byte(state), at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// LinkTaskBriefTodo puts to-do l.TodoID on brief l.BriefID (continuing l.Continues for l.Reason); a to-do already on a
// brief stays on it (false). ErrTodoNotFound when the to-do is gone.
func (d *DB) LinkTaskBriefTodo(ctx context.Context, l TaskBriefTodoRow) (bool, error) {
	tag, err := d.pool.Exec(ctx, `INSERT INTO task_brief_todos (todo_id, brief_id, continues, reason, linked_at)
		SELECT $1, $2, $3, $4, $5 WHERE EXISTS (SELECT 1 FROM todos WHERE id = $1)
		ON CONFLICT (todo_id) DO NOTHING`, l.TodoID, l.BriefID, l.Continues, l.Reason, l.LinkedAt)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := d.GetTodo(ctx, l.TodoID); gerr != nil {
			return false, gerr
		}
		return false, nil
	}
	return true, nil
}

// UnlinkTaskBriefTodo removes todoID's brief link when a continuation is aborted before its Agent can take over.
func (d *DB) UnlinkTaskBriefTodo(ctx context.Context, todoID string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM task_brief_todos WHERE todo_id = $1`, todoID)
	return err
}
