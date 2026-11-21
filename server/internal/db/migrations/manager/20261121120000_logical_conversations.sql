-- Public identity is independent of the machine and runtime of its active segment.
CREATE TABLE logical_conversations (
    machine_id text NOT NULL,
    session_id text NOT NULL,
    epoch bigint NOT NULL DEFAULT 1 CHECK (epoch > 0),
    history text NOT NULL,
    projection_version bigint NOT NULL DEFAULT 0,
    revision bigint NOT NULL DEFAULT 0,
    deleted_at timestamptz,
    PRIMARY KEY (machine_id, session_id)
);

CREATE TABLE conversation_segments (
    id text PRIMARY KEY,
    logical_machine text NOT NULL,
    logical_session text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal > 0),
    machine_id text NOT NULL,
    session_id text NOT NULL,
    engine text NOT NULL CHECK (engine <> ''),
    launcher text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    effort text NOT NULL DEFAULT '',
    permission_mode text NOT NULL DEFAULT '',
    source_history text NOT NULL,
    source_revision bigint NOT NULL DEFAULT 0,
    seq_offset integer NOT NULL CHECK (seq_offset BETWEEN 0 AND 2147483646),
    frozen_end integer CHECK (frozen_end >= 0 AND frozen_end <= 2147483646 - seq_offset),
    state text NOT NULL CHECK (state IN ('prepared', 'active', 'retired', 'abandoned')),
    close_pending boolean NOT NULL DEFAULT false,
    FOREIGN KEY (logical_machine, logical_session) REFERENCES logical_conversations ON DELETE CASCADE,
    UNIQUE (logical_machine, logical_session, ordinal),
    UNIQUE (machine_id, session_id)
);
CREATE UNIQUE INDEX conversation_one_active_segment ON conversation_segments (logical_machine, logical_session) WHERE state = 'active';

CREATE TABLE conversation_switches (
    logical_machine text NOT NULL,
    logical_session text NOT NULL,
    operation_id text NOT NULL,
    expected_epoch bigint NOT NULL,
    source_segment text NOT NULL,
    target_segment text,
    request bytea NOT NULL CHECK (octet_length(request) <= 65536),
    phase text NOT NULL CHECK (phase IN ('preparing', 'prepared', 'active', 'accepted', 'rejected', 'in_doubt')),
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (logical_machine, logical_session, operation_id),
    FOREIGN KEY (logical_machine, logical_session) REFERENCES logical_conversations ON DELETE CASCADE
);
CREATE UNIQUE INDEX conversation_one_unfinished_switch ON conversation_switches (logical_machine, logical_session)
    WHERE phase IN ('preparing', 'prepared', 'active', 'in_doubt');

CREATE TABLE conversation_inputs (
    logical_machine text NOT NULL,
    logical_session text NOT NULL,
    client_msg_id text NOT NULL,
    position bigint GENERATED ALWAYS AS IDENTITY,
    epoch bigint NOT NULL,
    payload bytea NOT NULL CHECK (octet_length(payload) <= 1048576),
    payload_hash bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'sending', 'accepted', 'in_doubt', 'rejected')),
    FOREIGN KEY (logical_machine, logical_session) REFERENCES logical_conversations ON DELETE CASCADE,
    PRIMARY KEY (logical_machine, logical_session, client_msg_id)
);
CREATE INDEX conversation_inputs_queue ON conversation_inputs (logical_machine, logical_session, position) WHERE state = 'queued';

CREATE TABLE conversation_interaction_routes (
    interaction_id text PRIMARY KEY,
    pending boolean NOT NULL DEFAULT true,
    logical_machine text NOT NULL,
    logical_session text NOT NULL,
    machine_id text NOT NULL,
    session_id text NOT NULL,
    epoch bigint NOT NULL,
    FOREIGN KEY (logical_machine, logical_session) REFERENCES logical_conversations ON DELETE CASCADE
);

-- Preparation and its context survive manager restarts. A claimed start is never
-- repeated without a positive node receipt, even when its result was lost.
ALTER TABLE conversation_switches ADD COLUMN handoff bytea CHECK (octet_length(handoff) <= 262144);
ALTER TABLE conversation_switches ADD COLUMN start_claimed boolean NOT NULL DEFAULT false;

-- Minimal closure intents outlive deletion of transcript content and mappings.
CREATE TABLE conversation_segment_closures (
 machine_id text NOT NULL,
 session_id text NOT NULL,
 PRIMARY KEY(machine_id, session_id)
);

-- A start may finish just before deletion or a manager restart. Keep only its
-- physical identity until it is adopted into a segment or closed; no prompt.
CREATE TABLE conversation_prepared_candidates (
 machine_id text NOT NULL,
 session_id text NOT NULL,
 logical_machine text NOT NULL,
 logical_session text NOT NULL,
 operation_id text NOT NULL,
 PRIMARY KEY(machine_id,session_id)
);
