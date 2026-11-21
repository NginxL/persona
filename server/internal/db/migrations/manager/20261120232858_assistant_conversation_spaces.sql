-- The space each of the assistant's conversations is in (#10941, docs/extensions/extensions.md "Never shown to
-- extensions"): noted when the manager starts one and whenever a computer lists one, so the rule that keeps the health
-- space's words from an extension still tells a quiet conversation (its computer lists a quiet one for a week, and only
-- its 100 newest) and one its computer has not reported since a restart. space is '' for the assistant's own and
-- 'health' for the health space's; a conversation once noted in the health space stays there.
CREATE TABLE IF NOT EXISTS assistant_conversation_spaces (
    machine_id text NOT NULL,
    session_id text NOT NULL,
    space      text NOT NULL,
    noted_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (machine_id, session_id)
);

-- A to-do names the conversation it came from by its session id alone.
CREATE INDEX IF NOT EXISTS assistant_conversation_spaces_session ON assistant_conversation_spaces (session_id);
