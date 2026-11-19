-- The owner's restore of an inactive to-do and their Someday, kept on the hub so every device agrees (#10226,
-- docs/assistant/todos.md "Restores and Someday"). restored_at: when the owner last brought the to-do back from Inactive,
-- activity for its status only. someday_until: when a to-do the owner put off to Someday comes back; NULL while it is
-- not put off, the year 9999 for Someday with no date. Neither is an edit: updated_at stays.
ALTER TABLE todos ADD COLUMN IF NOT EXISTS restored_at timestamptz;
ALTER TABLE todos ADD COLUMN IF NOT EXISTS someday_until timestamptz;
