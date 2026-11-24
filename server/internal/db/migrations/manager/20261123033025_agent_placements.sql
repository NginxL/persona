CREATE TABLE agent_placements (
    id text PRIMARY KEY,
    task_id text NOT NULL,
    machine text NOT NULL,
    session_id text NOT NULL,
    started_at timestamptz NOT NULL,
    decision jsonb NOT NULL
);
CREATE INDEX agent_placements_task ON agent_placements (task_id, started_at) WHERE task_id <> '';
CREATE INDEX agent_placements_session ON agent_placements (machine, session_id) WHERE session_id <> '';
