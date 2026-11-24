-- The assistant's tracks (#13104, docs/assistant/health.md): named places the assistant's conversations run in, each of
-- a kind (a domain space is one) and with a directory of its own. The main space ('') is not a row, and neither is the
-- built-in health space: the manager supplies it (health template, default categories {health}, its own health
-- directory), so a new manager database stays empty for the move of a manager's data into its own database
-- (db.CopyTables refuses a target with data). A track organizes; it holds no privacy level.
--   kind: the track's type ('space' for a domain space); the manager knows the kinds, not this table.
--   default_categories: the information categories everything produced in the track carries at least; the manager
--   checks them against its versioned list.
--   template: the instructions its conversations get ('space', 'health'), checked by the manager.
--   config_override: what the track sets apart from its kind's defaults (a JSON object, read by the manager).
--   dir_name: its directory relative to the state directory, kept as data (tracks/<slug> by default, whatever the kind,
--   so a track that changes kind keeps its directory); '' takes that default.
CREATE TABLE IF NOT EXISTS assistant_tracks (
    slug               text PRIMARY KEY CHECK (slug ~ '^[a-z][a-z0-9-]{1,31}$'),
    kind               text NOT NULL DEFAULT 'space',
    title              text NOT NULL DEFAULT '',
    icon               text NOT NULL DEFAULT '',
    description        text NOT NULL DEFAULT '' CHECK (char_length(description) <= 120),
    default_categories text[] NOT NULL DEFAULT '{}',
    template           text NOT NULL DEFAULT 'space',
    config_override    jsonb NOT NULL DEFAULT '{}',
    dir_name           text NOT NULL DEFAULT '',
    machine_id         text NOT NULL DEFAULT '',
    default_launcher   text NOT NULL DEFAULT '',
    default_model      text NOT NULL DEFAULT '',
    builtin            boolean NOT NULL DEFAULT false,
    created_at         timestamptz NOT NULL DEFAULT now(),
    archived_at        timestamptz,
    revision           bigint NOT NULL DEFAULT 1
);
