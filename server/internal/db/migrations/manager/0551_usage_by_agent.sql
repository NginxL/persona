-- Token usage by agent (docs/server/machines.md「Token usage」, #9984): each row also says which agent ran the replies
-- (proto.Session.Agent's names: claude, codex, traex, …), so the token usage page can show them apart. Rows from
-- before stay '' ("not split"); the nodes' next whole windows send their last 26 hours by agent.
ALTER TABLE machine_usage ADD COLUMN IF NOT EXISTS agent text NOT NULL DEFAULT '';
ALTER TABLE machine_usage DROP CONSTRAINT IF EXISTS machine_usage_pkey;
ALTER TABLE machine_usage ADD PRIMARY KEY (machine_id, ts, step, model, launcher, agent);

-- The backfill keeps a record per agent: an agent a node starts reading later has its history filled in on its own.
-- The existing records ('') stand for the agents the backfill read before (Claude Code, Codex, traex).
ALTER TABLE usage_backfill ADD COLUMN IF NOT EXISTS agent text NOT NULL DEFAULT '';
ALTER TABLE usage_backfill DROP CONSTRAINT IF EXISTS usage_backfill_pkey;
ALTER TABLE usage_backfill ADD PRIMARY KEY (machine_id, agent);
