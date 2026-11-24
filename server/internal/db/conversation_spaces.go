package db

import "context"

// The space each of the assistant's conversations is in (#10941, migrations/manager/
// 20261120232858_assistant_conversation_spaces.sql): what the manager noted when it started one or a computer listed
// one, read when no computer lists it any more.

// ConversationSpace is one conversation's space as noted: Space "" for the assistant's own, else a space's slug
// ("health" for the health space's).
type ConversationSpace struct {
	MachineID string
	SessionID string
	Space     string
}

// NoteConversationSpaces keeps the space of each of rows. A conversation noted in a space other than the assistant's
// own stays there whatever is noted after; one noted in the assistant's own moves to a space noted later. A
// conversation named twice in one call takes the first space other than the assistant's own it is named in: the
// manager puts the strictest first (noteConversationSpaces).
func (d *DB) NoteConversationSpaces(ctx context.Context, rows []ConversationSpace) error {
	if len(rows) == 0 {
		return nil
	}
	machines, sessions, spaces := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
	for i, r := range rows {
		machines[i], sessions[i], spaces[i] = r.MachineID, r.SessionID, r.Space
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO assistant_conversation_spaces (machine_id, session_id, space)
		SELECT DISTINCT ON (m, s) m, s, sp FROM unnest($1::text[], $2::text[], $3::text[]) WITH ORDINALITY AS r(m, s, sp, i)
		ORDER BY m, s, sp = '', i
		ON CONFLICT (machine_id, session_id) DO UPDATE SET space = EXCLUDED.space, noted_at = now()
			WHERE EXCLUDED.space <> '' AND assistant_conversation_spaces.space = ''`,
		machines, sessions, spaces)
	return err
}

// ConversationSpaces are the spaces noted of the conversations sessions, by session id, on whichever computer each was
// noted: a space other than the assistant's own when any computer's note says so (the first by name when two do). A
// conversation never noted is left out.
func (d *DB) ConversationSpaces(ctx context.Context, sessions []string) (map[string]string, error) {
	out := map[string]string{}
	if len(sessions) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT session_id, coalesce(min(nullif(space, '')), '') FROM assistant_conversation_spaces
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
