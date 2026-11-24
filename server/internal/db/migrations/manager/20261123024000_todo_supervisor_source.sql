-- Explicit supervisor moves must survive legacy plan rehoming (#13344).
ALTER TABLE todo_supervision ADD COLUMN IF NOT EXISTS supervisor_source text NOT NULL DEFAULT '';
