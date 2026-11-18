-- Permission mode changes of running sessions (#10297, docs/protocol/protocol.md「Session mode changes」): each change made
-- through the hub's /modes route (who and on which device, from which mode to which, at once or by a queued restart
-- into Full access), and what became of such a restart (done, failed, dropped and why). Append-only; the apps read
-- one session's rows (GET /api/v1/sessions/{mid}/{sid}/mode-changes) to show a line in its chat.
CREATE TABLE IF NOT EXISTS session_mode_changes (
	id bigserial PRIMARY KEY,
	machine_id text NOT NULL,
	session_id text NOT NULL,
	kind text NOT NULL,
	from_mode text NOT NULL DEFAULT '',
	to_mode text NOT NULL DEFAULT '',
	applied text NOT NULL DEFAULT '',
	actor_kind text NOT NULL DEFAULT '',
	device_id text NOT NULL DEFAULT '',
	device_name text NOT NULL DEFAULT '',
	reason text NOT NULL DEFAULT '',
	at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS session_mode_changes_session ON session_mode_changes (machine_id, session_id, at);
