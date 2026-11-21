package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// CommitTodoAnswer accepts one complete manual answer: its decisions, durable delivery settings and to-do completion
// commit together, before any message is sent. A to-do deleted after the answer's claim stays deleted; its answer
// still enters the delivery settings, without recreating its ledger. Returned decisions are only the newly inserted ones.
func (d *DB) CommitTodoAnswer(ctx context.Context, id string, decisions []TodoDecisionRow, settings map[string]any, at time.Time) (*TodoRow, []TodoDecisionRow, error) {
	if len(decisions) == 0 {
		return nil, nil, errors.New("an answer must contain decisions")
	}
	for _, r := range decisions {
		if r.TodoID != id || r.Source != DecisionManual || r.SupersedesID != "" {
			return nil, nil, errors.New("an answer must contain manual decisions of the same to-do")
		}
	}
	keys := slices.Sorted(maps.Keys(settings))
	raws := make([][]byte, len(keys))
	for i, key := range keys {
		if isHubKey(key) {
			return nil, nil, fmt.Errorf("answer settings: %s is a hub key", key)
		}
		raw, err := json.Marshal(settings[key])
		if err != nil {
			return nil, nil, err
		}
		raws[i] = raw
	}
	var row *TodoRow
	var inserted []TodoDecisionRow
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		var cur TodoRow
		err := tx.QueryRow(ctx, `SELECT `+todoColumns+` FROM todos WHERE id = $1 FOR UPDATE`, id).Scan(todoDest(&cur)...)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if cur.Kind == KindConcern && cur.DoneAt == nil {
				return ErrConcernUnreported
			}
			for _, r := range decisions {
				tag, err := tx.Exec(ctx, `INSERT INTO todo_decisions
						(id, todo_id, question, choice, reason, source, source_machine, source_session, source_message, created_at)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
					ON CONFLICT (todo_id, source_message, question) WHERE source_message <> '' DO NOTHING`,
					r.ID, r.TodoID, r.Question, r.Choice, r.Reason, r.Source, r.SourceMachine, r.SourceSession, r.SourceMessage, r.CreatedAt)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 0 {
					inserted = append(inserted, r)
				}
			}
			done := true
			TodoEdit{Done: &done}.Apply(&cur, at)
			_, err = tx.Exec(ctx, `UPDATE todos SET done_at = $2, updated_at = $3, start_error = $4,
				done_by = $5, done_reason = $6 WHERE id = $1`,
				id, cur.DoneAt, cur.UpdatedAt, cur.StartError, cur.DoneBy, cur.DoneReason)
			if err != nil {
				return err
			}
			row = &cur
		}
		for i, key := range keys {
			if _, err := tx.Exec(ctx, `INSERT INTO `+d.settingsTable()+` (key, value, updated_at) VALUES ($1, $2, now())
				ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = now()`, key, raws[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return row, inserted, nil
}
