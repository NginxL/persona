package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

const ConversationDeletionsKey = "assistant.deleted_conversations"

var ErrConversationDeleted = errors.New("conversation deleted")

func conversationDeletionKey(machine, session string) string {
	raw, _ := json.Marshal([2]string{machine, session})
	return string(raw)
}

// PutConversationDeletion merges one entry against the current database value,
// never an instance's cached whole map. The row lock also orders search commits
// from overlapping manager processes before deletion and index cleanup.
func (d *DB) PutConversationDeletion(ctx context.Context, machine, session string, row json.RawMessage) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO settings (key,value,updated_at) VALUES ($1,'{}',now()) ON CONFLICT (key) DO NOTHING`, ConversationDeletionsKey); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE settings SET value=value || jsonb_build_object($2::text,$3::jsonb), updated_at=now() WHERE key=$1`, ConversationDeletionsKey, conversationDeletionKey(machine, session), row); err != nil {
			return err
		}
		// Fence execution before deleting every physical segment. Keep only closure
		// identities for nodes that are offline; no messages or prompts survive.
		c, err := readLogicalConversation(ctx, tx, machine, session)
		if err != nil {
			return err
		}
		if c != nil {
			var epoch int64
			if err = tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 FOR UPDATE NOWAIT`, c.MachineID, c.SessionID).Scan(&epoch); err != nil {
				return err
			}
			for _, seg := range c.Segments {
				if _, err = tx.Exec(ctx, `INSERT INTO conversation_segment_closures(machine_id,session_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, seg.MachineID, seg.SessionID); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `UPDATE settings SET value=value || jsonb_build_object($2::text,$3::jsonb || jsonb_build_object('machine_id',$4::text,'session_id',$5::text,'hidden_segment',$6::boolean)) WHERE key=$1`, ConversationDeletionsKey, conversationDeletionKey(seg.MachineID, seg.SessionID), row, seg.MachineID, seg.SessionID, seg.Ordinal > 1); err != nil {
					return err
				}
				if err = dropSearchConversationTx(ctx, tx, seg.MachineID, seg.SessionID); err != nil {
					return err
				}
				if err = dropConversationHistoryTx(ctx, tx, seg.MachineID, seg.SessionID); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, `DELETE FROM logical_conversations WHERE machine_id=$1 AND session_id=$2`, c.MachineID, c.SessionID); err != nil {
				return err
			}
		}
		if err := dropSearchConversationTx(ctx, tx, machine, session); err != nil {
			return err
		}
		return dropConversationHistoryTx(ctx, tx, machine, session)
	})
}

// ConversationDeletion is an authoritative read, including when this process
// previously cached an empty ledger before another process deleted the chat.
func (d *DB) ConversationDeletion(ctx context.Context, machine, session string) (json.RawMessage, bool, error) {
	var raw []byte
	err := d.pool.QueryRow(ctx, `SELECT value -> $2 FROM settings WHERE key=$1`, ConversationDeletionsKey, conversationDeletionKey(machine, session)).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return raw, len(raw) > 0 && string(raw) != "null", err
}

// ConversationDeletions are the records of machine's deleted conversations among sessions, by session id, in one read
// (ConversationDeletion's for each).
func (d *DB) ConversationDeletions(ctx context.Context, machine string, sessions []string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(sessions) == 0 {
		return out, nil
	}
	keys := make([]string, len(sessions))
	byKey := make(map[string]string, len(sessions))
	for i, s := range sessions {
		keys[i] = conversationDeletionKey(machine, s)
		byKey[keys[i]] = s
	}
	rows, err := d.pool.Query(ctx, `SELECT k, s.value -> k FROM settings s CROSS JOIN unnest($2::text[]) AS k
		WHERE s.key = $1 AND s.value ? k`, ConversationDeletionsKey, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		if len(raw) > 0 && string(raw) != "null" {
			out[byKey[key]] = raw
		}
	}
	return out, rows.Err()
}

func (d *DB) ConversationDeletedID(ctx context.Context, session string) (bool, error) {
	var deleted bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM settings s CROSS JOIN LATERAL jsonb_each(s.value) entry WHERE s.key=$1 AND entry.value->>'session_id'=$2)`, ConversationDeletionsKey, session).Scan(&deleted)
	return deleted, err
}

// This shared row lock lasts through a conversation-derived write. Deletion's
// UPDATE waits for earlier commits; later writes observe the marker.
func conversationAvailableTx(ctx context.Context, tx pgx.Tx, machine, session string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO settings (key,value,updated_at) VALUES ($1,'{}',now()) ON CONFLICT (key) DO NOTHING`, ConversationDeletionsKey); err != nil {
		return err
	}
	var deleted bool
	if err := tx.QueryRow(ctx, `SELECT value ? $2 FROM settings WHERE key=$1 FOR SHARE`, ConversationDeletionsKey, conversationDeletionKey(machine, session)).Scan(&deleted); err != nil {
		return err
	}
	if deleted {
		return ErrConversationDeleted
	}
	return nil
}
