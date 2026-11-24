CREATE SEQUENCE IF NOT EXISTS health_intake_seq;
CREATE TABLE IF NOT EXISTS health_intake (
    id         uuid PRIMARY KEY,
    kind       text NOT NULL CHECK (kind IN ('water', 'protein')),
    amount     numeric(7,1) NOT NULL CHECK (amount > 0),
    at         timestamptz NOT NULL,
    note       text NOT NULL DEFAULT '',
    seq        bigint NOT NULL DEFAULT nextval('health_intake_seq'),
    created_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
ALTER SEQUENCE health_intake_seq OWNED BY health_intake.seq;
CREATE INDEX IF NOT EXISTS health_intake_day ON health_intake (kind, at) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS health_intake_seq_idx ON health_intake (seq);
