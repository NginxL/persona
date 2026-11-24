package db

import (
	"context"
	"time"
)

// QuestionReadsMax bounds the marks one session's read returns (the newest): far more questions than a chat's rail
// shows at once.
const QuestionReadsMax = 5000

// QuestionReads returns when read marks began in a session (its first read, recorded now when there is none) and the
// questions whose answers the owner opened there, newest first (#13021).
func (d *DB) QuestionReads(ctx context.Context, machine, session string) (time.Time, []string, error) {
	var since time.Time
	err := d.pool.QueryRow(ctx, `WITH ins AS (
		INSERT INTO question_read_sessions (machine_id, session_id) VALUES ($1, $2) ON CONFLICT DO NOTHING RETURNING since)
		SELECT since FROM ins UNION ALL SELECT since FROM question_read_sessions WHERE machine_id = $1 AND session_id = $2 LIMIT 1`,
		machine, session).Scan(&since)
	if err != nil {
		return time.Time{}, nil, err
	}
	rows, err := d.pool.Query(ctx, `SELECT input_id FROM question_reads WHERE machine_id = $1 AND session_id = $2
		ORDER BY read_at DESC, input_id LIMIT $3`, machine, session, QuestionReadsMax)
	if err != nil {
		return time.Time{}, nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return time.Time{}, nil, err
		}
		ids = append(ids, id)
	}
	return since, ids, rows.Err()
}

// MarkQuestionsRead records that the owner opened these questions' answers; one marked already keeps its first time.
func (d *DB) MarkQuestionsRead(ctx context.Context, machine, session string, ids []string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO question_reads (machine_id, session_id, input_id, read_at)
		SELECT $1, $2, unnest($3::text[]), $4 ON CONFLICT DO NOTHING`, machine, session, ids, at)
	return err
}
