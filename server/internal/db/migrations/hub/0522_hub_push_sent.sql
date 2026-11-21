-- The hub's record of delivered alert pushes (hub/push_grading.go, #8257): one row per push, appended, swept by age and count.
-- It replaces the hub.push_sent setting, which every push rewrote whole; the hub adds that setting's entries here at
-- start (AdoptPushSentSetting) and keeps the setting for a build from before this table, so this migration only makes
-- the table. push_id names one logical push, chosen before
-- its first write: a retry after a write that landed but reported an error inserts nothing (ON CONFLICT DO NOTHING).
CREATE TABLE IF NOT EXISTS hub_push_sent (
    id          bigserial PRIMARY KEY,
    push_id     text NOT NULL UNIQUE,
    at          timestamptz NOT NULL,
    level       text NOT NULL DEFAULT '',
    related_key text NOT NULL DEFAULT '',
    manager     text NOT NULL DEFAULT ''
);
