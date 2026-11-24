-- Quick capture (#13502, docs/assistant/health.md "Quick capture"): what the user jots down from the share sheet, the
-- watch, Siri or the web, kept apart from every conversation. client_id is the device's own id for one capture, sent
-- again with each retry, so an offline device's queue delivers it once. status is new, sorted, filed, dismissed or
-- failed, checked by the manager. sort is what sorting made of it (the track, the action, a title, the confidence,
-- whether it is filed on its own), filed_to where it was filed (a file in a track's inbox/). Its information
-- categories are rows of assistant_info_categories (kind 'capture', ref its id).
CREATE TABLE IF NOT EXISTS assistant_captures (
    id         text PRIMARY KEY,
    client_id  text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    source     text NOT NULL DEFAULT '',
    kind       text NOT NULL DEFAULT 'text',
    text       text NOT NULL DEFAULT '',
    url        text NOT NULL DEFAULT '',
    space_hint text NOT NULL DEFAULT '',
    status     text NOT NULL DEFAULT 'new',
    sort       jsonb NOT NULL DEFAULT '{}',
    filed_to   jsonb NOT NULL DEFAULT '{}',
    attempts   integer NOT NULL DEFAULT 0,
    error      text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS assistant_captures_status ON assistant_captures (status, created_at);
