ALTER TABLE todos ADD COLUMN IF NOT EXISTS require_accepted_after boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS deleted_request_gates (id text PRIMARY KEY);
