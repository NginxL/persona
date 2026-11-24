-- Which answers in a chat the owner opened (#13021): the question rail marks a question unread until its answer was on
-- screen or jumped to. One row per question (its message's entry id), kept so every device agrees; the session row
-- says when marks began there, so answers that ended before it count as read instead of turning the history unread.
CREATE TABLE question_read_sessions (
    machine_id text NOT NULL,
    session_id text NOT NULL,
    since timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (machine_id, session_id)
);

CREATE TABLE question_reads (
    machine_id text NOT NULL,
    session_id text NOT NULL,
    input_id text NOT NULL,
    read_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (machine_id, session_id, input_id)
);
