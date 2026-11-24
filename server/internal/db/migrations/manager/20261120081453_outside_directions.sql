-- A conversationless direction keeps its ordinary name, project and progress in
-- settings. This separate row marks that kind and preserves its optional Agent
-- binding when an older manager rewrites the settings JSON it knows.
CREATE TABLE IF NOT EXISTS assistant_outside_directions (
    direction_id TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((machine_id = '') = (session_id = ''))
);
