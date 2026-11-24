-- CI arbitration is manager-owned; the external runner retains its own hard locks.
CREATE SEQUENCE resource_fence_tokens;
CREATE TABLE resource_leases (
 id text PRIMARY KEY,
 resource text NOT NULL,
 seq bigserial UNIQUE NOT NULL,
 state text NOT NULL CHECK (state IN ('queued','granted','released','expired','timeout','cancelled')),
 dedupe_key text NOT NULL,
 body jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX resource_leases_active_key ON resource_leases(resource,dedupe_key)
 WHERE state IN ('queued','granted');
CREATE INDEX resource_leases_queue ON resource_leases(resource,seq);
