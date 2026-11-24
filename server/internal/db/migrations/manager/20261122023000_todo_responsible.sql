-- An external responsible party on a to-do that is not a Persona-started agent (#12712, docs/assistant/todos.md):
-- {kind: "session" | "person", name, machine_id?, session_id?}, NULL while none is set. One value per to-do, read
-- and written as a unit, hence a jsonb column like todos.thread and todos.source.
ALTER TABLE todos ADD COLUMN IF NOT EXISTS responsible jsonb
    CONSTRAINT todos_responsible_valid CHECK (
        responsible IS NULL OR (
            jsonb_typeof(responsible) = 'object'
            AND coalesce(jsonb_typeof(responsible -> 'kind'), '') = 'string'
            AND coalesce(responsible ->> 'kind', '') IN ('session', 'person')
            AND coalesce(jsonb_typeof(responsible -> 'name'), '') = 'string'
            AND length(btrim(coalesce(responsible ->> 'name', ''))) > 0
            AND (NOT (responsible ? 'machine_id') OR coalesce(jsonb_typeof(responsible -> 'machine_id'), '') = 'string')
            AND (NOT (responsible ? 'session_id') OR coalesce(jsonb_typeof(responsible -> 'session_id'), '') = 'string')
        )
    );
