-- Assistant history belongs to the manager, independent of the disposable search index.
CREATE TABLE conversation_histories (
    machine_id text NOT NULL,
    session_id text NOT NULL,
    history text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 0,
    coverage jsonb NOT NULL DEFAULT '[]',
    tail_seq integer,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (machine_id, session_id)
);

CREATE TABLE conversation_items (
    machine_id text NOT NULL,
    session_id text NOT NULL,
    item_id text NOT NULL CHECK (octet_length(item_id) BETWEEN 1 AND 512),
    seq integer NOT NULL CHECK (seq > 0),
    position integer NOT NULL CHECK (position >= 0),
    payload bytea NOT NULL CHECK (octet_length(payload) <= 65536),
    complete boolean NOT NULL,
    source_bytes integer NOT NULL CHECK (source_bytes >= 0),
    PRIMARY KEY (machine_id, session_id, item_id),
    FOREIGN KEY (machine_id, session_id) REFERENCES conversation_histories ON DELETE CASCADE
);
CREATE INDEX conversation_items_sequence ON conversation_items (machine_id, session_id, seq, position, item_id);
