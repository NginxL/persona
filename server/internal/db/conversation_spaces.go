package db

import "context"

// The space each of the assistant's conversations is in (#10941, migrations/manager/
// 20261120232858_assistant_conversation_spaces.sql): what the manager noted when it started one or a computer listed
// one, read when no computer lists it any more.

// ConversationSpace is one conversation's space as noted: Space "" for the assistant's own, "health" for the health
// space's.
type ConversationSpace struct {
	MachineID string
	SessionID string
	Space     string
}

// healthSpace is the health space's name, as the assistant notes it (identity.SpaceHealth).
const healthSpace = "health"

// NoteConversationSpaces keeps the space of each of rows. A conversation noted already keeps its space, unless it is
// noted in the health space now: one noted there stays there whatever is noted after.
func (d *DB) NoteConversationSpaces(ctx context.Context, rows []ConversationSpace) error {
	if len(rows) == 0 {
		return nil
	}
	machines, sessions, spaces := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
	for i, r := range rows {
		machines[i], sessions[i], spaces[i] = r.MachineID, r.SessionID, r.Space
	}
	// One row each: a conversation named twice in one call is noted in the health space when either says so.
	_, err := d.pool.Exec(ctx, `INSERT INTO assistant_conversation_spaces (machine_id, session_id, space)
		SELECT m, s, max(sp) FROM unnest($1::text[], $2::text[], $3::text[]) AS r(m, s, sp) GROUP BY m, s
		ON CONFLICT (machine_id, session_id) DO UPDATE SET space = EXCLUDED.space, noted_at = now()
			WHERE EXCLUDED.space = $4 AND assistant_conversation_spaces.space <> $4`,
		machines, sessions, spaces, healthSpace)
	return err
}

// ConversationSpaces are the spaces noted of the conversations sessions, by session id, on whichever computer each was
// noted: the health space's when any computer's note says so. A conversation never noted is left out.
func (d *DB) ConversationSpaces(ctx context.Context, sessions []string) (map[string]string, error) {
	out := map[string]string{}
	if len(sessions) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT session_id, max(space) FROM assistant_conversation_spaces
		WHERE session_id = ANY($1) GROUP BY session_id`, sessions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, space string
		if err := rows.Scan(&id, &space); err != nil {
			return nil, err
		}
		out[id] = space
	}
	return out, rows.Err()
}
