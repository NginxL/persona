-- Calibration starts with new supervised task_plan rounds. Old brief events
-- record durations but have no immutable cohort, so there is no backfill.
CREATE TABLE task_estimate_rounds (
    round_id text PRIMARY KEY,
    todo_id text NOT NULL REFERENCES todos (id) ON DELETE CASCADE,
    brief_id text NOT NULL REFERENCES task_briefs (id) ON DELETE CASCADE,
    plan_id text NOT NULL,
    dispatched_at timestamptz NOT NULL,
    kind text NOT NULL,
    read_only boolean NOT NULL,
    role text NOT NULL,
    tier text NOT NULL,
    machine_id text NOT NULL,
    repository text NOT NULL,
    admitted_at timestamptz,
    session_id text NOT NULL DEFAULT '',
    completed_at timestamptz,
    accepted_at timestamptz,
    actual_minutes integer CHECK (actual_minutes > 0),
    CHECK ((admitted_at IS NULL) = (session_id = '')),
    CHECK ((accepted_at IS NULL) = (completed_at IS NULL)),
    CHECK ((accepted_at IS NULL) = (actual_minutes IS NULL))
);

CREATE INDEX task_estimate_rounds_samples
    ON task_estimate_rounds (kind, read_only, role, tier, accepted_at DESC, round_id DESC)
    WHERE accepted_at IS NOT NULL;

CREATE INDEX task_estimate_rounds_pending
    ON task_estimate_rounds (kind, read_only, role, tier)
    WHERE accepted_at IS NULL AND admitted_at IS NOT NULL;
