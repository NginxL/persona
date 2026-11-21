package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/proto"
)

var ErrConversationHistoryChanged = errors.New("conversation history changed during read")

// ConversationHistory is durable source coverage, separate from payload completeness.
// TailSeq is nil until a runtime page proves the current end (including an empty history).
type ConversationHistory struct {
	MachineID, SessionID, History string
	Revision                      int64
	Coverage                      []conversationlog.Range
	TailSeq                       *int
	Summary                       string
	SummaryKnown                  bool
}

// ConversationPage is an unfolded source page. ExpectedHistory is a compare-and-swap
// fence read before fetching: a late fetch cannot resurrect an abandoned generation.
type ConversationPage struct {
	MachineID, SessionID, History, ExpectedHistory string
	Revision                                       int64
	FromSeq, ThroughSeq                            int
	Tail                                           bool
	Items                                          []proto.Item
}

func (d *DB) ConversationHistory(ctx context.Context, machine, session string) (*ConversationHistory, error) {
	var h ConversationHistory
	var coverage []byte
	var summary []byte
	err := d.pool.QueryRow(ctx, `SELECT machine_id,session_id,history,revision,coverage,tail_seq,summary,summary_known
		FROM conversation_histories WHERE machine_id=$1 AND session_id=$2`, machine, session).
		Scan(&h.MachineID, &h.SessionID, &h.History, &h.Revision, &coverage, &h.TailSeq, &summary, &h.SummaryKnown)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.Summary = string(summary)
	err = json.Unmarshal(coverage, &h.Coverage)
	return &h, err
}

// PutConversationPage atomically stores bounded items and coverage, fencing both
// concurrent deletion and generation replacement across manager processes.
func (d *DB) PutConversationPage(ctx context.Context, p ConversationPage) error {
	if p.MachineID == "" || p.SessionID == "" || len(p.History) > 512 || p.FromSeq < 0 || p.ThroughSeq < p.FromSeq {
		return errors.New("invalid conversation page")
	}
	bounded := make([]conversationlog.Item, 0, len(p.Items))
	for _, it := range p.Items {
		if it.Seq <= p.FromSeq || it.Seq > p.ThroughSeq || it.Fold != nil {
			return errors.New("conversation page contains an out-of-range or folded item")
		}
		row, err := conversationlog.Bound(it)
		if err != nil {
			return err
		}
		bounded = append(bounded, row)
	}
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if err := conversationAvailableTx(ctx, tx, p.MachineID, p.SessionID); err != nil {
			return err
		}
		inserted, err := tx.Exec(ctx, `INSERT INTO conversation_histories(machine_id,session_id)
			VALUES($1,$2) ON CONFLICT DO NOTHING`, p.MachineID, p.SessionID)
		if err != nil {
			return err
		}
		var old string
		var revision int64
		var coverage []byte
		var tail *int
		if err := tx.QueryRow(ctx, `SELECT history,revision,coverage,tail_seq FROM conversation_histories
			WHERE machine_id=$1 AND session_id=$2 FOR UPDATE`, p.MachineID, p.SessionID).
			Scan(&old, &revision, &coverage, &tail); err != nil {
			return err
		}
		if old != p.ExpectedHistory {
			return ErrConversationHistoryChanged
		}
		var ranges []conversationlog.Range
		if err := json.Unmarshal(coverage, &ranges); err != nil {
			return err
		}
		if old != p.History {
			if p.FromSeq != 0 && inserted.RowsAffected() == 0 {
				return ErrConversationHistoryChanged // fetch the replacement from its beginning
			}
			if _, err := tx.Exec(ctx, `DELETE FROM conversation_items WHERE machine_id=$1 AND session_id=$2`, p.MachineID, p.SessionID); err != nil {
				return err
			}
			ranges, tail = nil, nil
			if _, err := tx.Exec(ctx, `UPDATE conversation_histories SET summary='',summary_known=false WHERE machine_id=$1 AND session_id=$2`, p.MachineID, p.SessionID); err != nil {
				return err
			}
		} else if p.Revision != 0 && revision != 0 && p.Revision < revision {
			return ErrConversationHistoryChanged
		}
		if p.Revision != 0 && revision != p.Revision {
			tail = nil // the old end is no proof of the new end
		}
		ranges = conversationlog.MergeRanges(ranges, conversationlog.Range{From: p.FromSeq, Through: p.ThroughSeq})
		if len(ranges) > 128 {
			return errors.New("too many disjoint conversation ranges; synchronize the missing prefix")
		}
		if p.Tail {
			end := p.ThroughSeq
			tail = &end
		}
		b := &pgx.Batch{}
		for _, row := range bounded {
			if row.Item.Compact != nil {
				b.Queue(`UPDATE conversation_histories SET summary='',summary_known=false WHERE machine_id=$1 AND session_id=$2`, p.MachineID, p.SessionID)
				break
			}
		}
		positions := map[int]int{}
		for _, row := range bounded {
			raw, err := json.Marshal(row.Item)
			if err != nil {
				return err
			}
			b.Queue(`INSERT INTO conversation_items(machine_id,session_id,item_id,seq,payload,complete,source_bytes,position)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(machine_id,session_id,item_id) DO UPDATE
				SET seq=EXCLUDED.seq,payload=EXCLUDED.payload,complete=EXCLUDED.complete,source_bytes=EXCLUDED.source_bytes,position=EXCLUDED.position
				WHERE NOT conversation_items.complete OR EXCLUDED.complete`,
				p.MachineID, p.SessionID, row.Item.ID, row.Item.Seq, raw, row.Complete, row.SourceBytes, positions[row.Item.Seq])
			positions[row.Item.Seq]++
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		raw, err := json.Marshal(ranges)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE conversation_histories SET history=$3,revision=$4,coverage=$5,tail_seq=$6,updated_at=now()
			WHERE machine_id=$1 AND session_id=$2`, p.MachineID, p.SessionID, p.History, p.Revision, raw, tail)
		return err
	})
}

// ConversationHistoryStats exposes aggregate capacity evidence, never message text or session IDs.
type ConversationHistoryStats struct {
	Conversations int64 `json:"conversations"`
	Items         int64 `json:"items"`
	PayloadBytes  int64 `json:"payload_bytes"`
	Incomplete    int64 `json:"incomplete"`
	SummaryBytes  int64 `json:"summary_bytes"`
}

func (d *DB) ConversationHistoryStats(ctx context.Context) (ConversationHistoryStats, error) {
	var out ConversationHistoryStats
	err := d.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM conversation_histories),count(*),
		coalesce(sum(octet_length(payload)),0),count(*) FILTER (WHERE NOT complete),
		(SELECT coalesce(sum(octet_length(summary)),0) FROM conversation_histories) FROM conversation_items`).
		Scan(&out.Conversations, &out.Items, &out.PayloadBytes, &out.Incomplete, &out.SummaryBytes)
	return out, err
}

// ConversationItems returns bounded ordered rows of the expected generation.
// A changed generation is an error, never a mixture of old and new items.
func (d *DB) ConversationItems(ctx context.Context, machine, session, history string, after, through, limit int, newest bool) ([]conversationlog.Item, error) {
	if limit <= 0 || limit > 10000 {
		return nil, errors.New("conversation item limit must be between 1 and 10000")
	}
	order := "ASC"
	if newest {
		order = "DESC"
	}
	// Include the entire boundary sequence group: one runtime line can contain many blocks.
	query := fmt.Sprintf(`WITH selected AS (
		SELECT i.seq FROM conversation_items i JOIN conversation_histories h USING(machine_id,session_id)
		WHERE i.machine_id=$1 AND i.session_id=$2 AND h.history=$3 AND i.seq>$4 AND i.seq<=$5
		ORDER BY i.seq %s,i.position %s,i.item_id LIMIT $6
	) SELECT payload,complete,source_bytes FROM conversation_items i
		WHERE i.machine_id=$1 AND i.session_id=$2 AND i.seq>$4 AND i.seq<=$5
		AND i.seq IN (SELECT seq FROM selected) ORDER BY i.seq,i.position,i.item_id`, order, order)
	var out []conversationlog.Item
	err := pgx.BeginTxFunc(ctx, d.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var current string
		if err := tx.QueryRow(ctx, `SELECT history FROM conversation_histories WHERE machine_id=$1 AND session_id=$2`, machine, session).Scan(&current); err != nil {
			return err
		}
		if current != history {
			return ErrConversationHistoryChanged
		}
		rows, err := tx.Query(ctx, query, machine, session, history, after, through, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row conversationlog.Item
			var raw []byte
			if err := rows.Scan(&raw, &row.Complete, &row.SourceBytes); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &row.Item); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

func dropConversationHistoryTx(ctx context.Context, tx pgx.Tx, machine, session string) error {
	_, err := tx.Exec(ctx, `DELETE FROM conversation_histories WHERE machine_id=$1 AND session_id=$2`, machine, session)
	return err
}

// PutConversationSummary checkpoints the source engine's compaction result, without
// calling a model. Both generation and revision must still match the source read.
func (d *DB) PutConversationSummary(ctx context.Context, machine, session, history string, revision int64, summary string) error {
	if len(summary) > conversationlog.MaxItemBytes {
		return errors.New("conversation summary exceeds item limit")
	}
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if err := conversationAvailableTx(ctx, tx, machine, session); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE conversation_histories SET summary=$5,summary_known=true
			WHERE machine_id=$1 AND session_id=$2 AND history=$3 AND revision=$4`, machine, session, history, revision, []byte(summary))
		if err == nil && tag.RowsAffected() != 1 {
			return ErrConversationHistoryChanged
		}
		return err
	})
}

func (d *DB) ConversationItem(ctx context.Context, machine, session, history, id string) (*conversationlog.Item, error) {
	var row conversationlog.Item
	var raw []byte
	err := d.pool.QueryRow(ctx, `SELECT i.payload,i.complete,i.source_bytes FROM conversation_items i
		JOIN conversation_histories h USING(machine_id,session_id)
		WHERE i.machine_id=$1 AND i.session_id=$2 AND h.history=$3 AND i.item_id=$4`, machine, session, history, id).
		Scan(&raw, &row.Complete, &row.SourceBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, &row.Item)
	return &row, err
}
