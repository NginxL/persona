package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/proto"
)

// FinishStandingInteraction commits the authorization's answer and its task decision together before the hook hears it.
// Locking the registry row orders this transaction against revocation, including writers in another manager process.
func (d *DB) FinishStandingInteraction(ctx context.Context, setting, grant string, ix proto.Interaction, resolution proto.Resolution, decision TodoDecisionRow, taskUpdatedAt time.Time) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1 FOR SHARE`, setting).Scan(&raw); err != nil {
			return err
		}
		var registry struct {
			Authorizations []struct {
				ID string `json:"id"`
			} `json:"authorizations"`
		}
		if err := json.Unmarshal(raw, &registry); err != nil {
			return err
		}
		active := false
		for _, a := range registry.Authorizations {
			if a.ID == grant {
				active = true
			}
		}
		if !active {
			return errors.New("standing authorization was revoked")
		}
		var open bool
		var updated time.Time
		if err := tx.QueryRow(ctx, `SELECT done_at IS NULL,updated_at FROM todos WHERE id = $1 FOR UPDATE`, decision.TodoID).Scan(&open, &updated); err != nil {
			return err
		}
		if !open || !updated.Equal(taskUpdatedAt) {
			return errors.New("standing authorization task changed or closed")
		}
		payload, err := json.Marshal(ix)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO interactions (id,machine_id,session_id,kind,payload,created_at)
   VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`, ix.ID, ix.MachineID, ix.SessionID, ix.Kind, payload, ix.CreatedAt); err != nil {
			return err
		}
		answer, err := json.Marshal(resolution)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE interactions SET status='resolved',resolution=$2,resolved_at=now(),ended_by='authorization',
   auto_authorized_at=now(),authorization_id=$3,todo_id=$4 WHERE id=$1 AND status='pending' AND created_at=$5`, ix.ID, answer, grant, decision.TodoID, ix.CreatedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("interaction is no longer pending")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO todo_decisions (id,todo_id,question,choice,reason,source,source_machine,source_session,source_message,created_at)
   VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, decision.ID, decision.TodoID, decision.Question, decision.Choice, decision.Reason, decision.Source, decision.SourceMachine, decision.SourceSession, decision.SourceMessage, decision.CreatedAt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at=$2 WHERE id=$1`, decision.TodoID, decision.CreatedAt)
		return err
	})
}
