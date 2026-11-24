-- Opt-in per-(machine, repo_id) managed bare repository mirrors (#13197).
-- Defaults to disabled (enabled = false, auto_sync = false); zero background traffic unless explicitly enabled.
CREATE TABLE IF NOT EXISTS repo_mirrors (
    machine_id TEXT NOT NULL,
    repo_id TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    auto_sync BOOLEAN NOT NULL DEFAULT FALSE,
    sync_interval_minutes INTEGER NOT NULL DEFAULT 30,
    last_synced_at TIMESTAMPTZ,
    last_head_commit TEXT NOT NULL DEFAULT '',
    last_default_ref TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    disk_bytes BIGINT NOT NULL DEFAULT 0,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    next_sync_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (machine_id, repo_id)
);

CREATE INDEX IF NOT EXISTS repo_mirrors_auto_sync
    ON repo_mirrors (next_sync_at)
    WHERE enabled = TRUE AND auto_sync = TRUE;
