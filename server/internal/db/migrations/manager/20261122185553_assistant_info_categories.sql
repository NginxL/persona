-- The information categories what the assistant keeps is tagged with (#13246, docs/assistant/health.md "Information
-- categories"): health, finance and the others the manager knows, checked in code (a category it does not know is left
-- out when read). kind is what is tagged ('conversation', by its session id, on whichever computer it runs) and ref
-- which one. by says who tagged it: 'model' (the info.tag use after a turn), 'agent', or 'user'. A category the user
-- took away stays as a row with removed_by_user, so nothing but the user adds it back. A track's default categories
-- are no rows: the manager adds them when it reads.
CREATE TABLE IF NOT EXISTS assistant_info_categories (
    kind            text NOT NULL,
    ref             text NOT NULL,
    category        text NOT NULL,
    by              text NOT NULL,
    model           text NOT NULL DEFAULT '',
    removed_by_user boolean NOT NULL DEFAULT false,
    tagged_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, ref, category)
);
