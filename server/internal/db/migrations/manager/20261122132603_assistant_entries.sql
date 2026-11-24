-- Payloads never change. Lifecycle changes append a successor and are projected on read.
CREATE TABLE assistant_entries (
 id text PRIMARY KEY,
 at timestamptz NOT NULL,
 supersedes text REFERENCES assistant_entries(id),
 body jsonb NOT NULL,
 CHECK (jsonb_typeof(body) = 'object'),
 CHECK (body->>'id' = id),
 CHECK (body->>'type' IN ('fact', 'preference', 'rule', 'decision')),
 CHECK (body->>'by' IN ('user', 'agent', 'enkeep', 'import')),
 CHECK (body->>'trust' IN ('user_verbatim', 'user_relayed', 'repo', 'agent', 'inferred')),
 CHECK (body->>'status' IN ('active', 'superseded', 'wrong')),
 CHECK (supersedes IS DISTINCT FROM id)
);
CREATE INDEX assistant_entries_time ON assistant_entries(at, id);
CREATE UNIQUE INDEX assistant_entries_successor ON assistant_entries(supersedes) WHERE supersedes IS NOT NULL;
CREATE INDEX assistant_entries_fields ON assistant_entries USING gin(body jsonb_path_ops);
CREATE FUNCTION assistant_entries_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'assistant entries are append-only' USING ERRCODE = '55000';
END;
$$;
CREATE TRIGGER assistant_entries_no_update BEFORE UPDATE OR DELETE ON assistant_entries
 FOR EACH ROW EXECUTE FUNCTION assistant_entries_immutable();
