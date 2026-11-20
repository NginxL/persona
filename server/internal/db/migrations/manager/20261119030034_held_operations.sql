-- Operations the owner sent to a computer while it was away, held by its manager and sent once its node is back
-- (#10661, internal/devices held.go). One row per operation; kind says what it is ('message': a session.send to an
-- existing Agent). call is the devicewire.NodeCall as the hub sent it, its caller and submission with it.
CREATE TABLE IF NOT EXISTS held_operations (
    seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind text NOT NULL,
    op_id text NOT NULL,
    machine_id text NOT NULL,
    session_id text NOT NULL DEFAULT '',
    call jsonb NOT NULL,
    lang text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'sending', 'failed')),
    failure text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    sent_at timestamptz NOT NULL DEFAULT now(),
    held_at timestamptz NOT NULL DEFAULT now(),
    tried_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    UNIQUE (kind, machine_id, session_id, op_id)
);
CREATE INDEX IF NOT EXISTS held_operations_machine ON held_operations (machine_id, seq);
