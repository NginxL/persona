-- The user's track kinds (#13472, docs/assistant/health.md "Tracks and kinds"): a kind is what its tracks do, a config
-- of options the manager defines (its template, context, default categories, rhythms, attention, supervision, usage,
-- sidebar...). The built-in kinds (orchestration, space) are the manager's and no rows, so a new manager database stays
-- empty (db.CopyTables refuses a target with data); this table holds only the kinds the user adds.
--   config: the kind's config (a JSON object with its schema version), checked by the manager; one with a schema newer
--   than the manager knows is not enabled.
CREATE TABLE IF NOT EXISTS assistant_track_kinds (
    slug       text PRIMARY KEY CHECK (slug ~ '^[a-z][a-z0-9-]{1,31}$'),
    title      text NOT NULL DEFAULT '',
    config     jsonb NOT NULL DEFAULT '{}',
    builtin    boolean NOT NULL DEFAULT false,
    revision   bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now()
);
