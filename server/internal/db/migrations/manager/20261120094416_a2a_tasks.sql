CREATE TABLE a2a_tasks (
    id text PRIMARY KEY,
    owner_key text NOT NULL,
    request_id text NOT NULL,
    target_machine text NOT NULL,
    target_session text NOT NULL,
    source_machine text NOT NULL DEFAULT '',
    source_session text NOT NULL DEFAULT '',
    context_id text NOT NULL,
    state text NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    body jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    status_at timestamptz NOT NULL DEFAULT now(),
    next_check timestamptz,
    UNIQUE (owner_key, request_id)
);

CREATE INDEX a2a_tasks_delivery ON a2a_tasks (next_check) WHERE next_check IS NOT NULL;
CREATE INDEX a2a_tasks_owner ON a2a_tasks (owner_key, created_at DESC, id DESC);
CREATE INDEX a2a_tasks_target ON a2a_tasks (target_machine, target_session, created_at DESC, id DESC);
CREATE UNIQUE INDEX a2a_tasks_active_target ON a2a_tasks (target_machine, target_session)
    WHERE state IN ('submitted', 'working', 'input_required', 'auth_required');
