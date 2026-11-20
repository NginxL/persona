CREATE TABLE IF NOT EXISTS todo_workspace_rules (
    todo_id TEXT PRIMARY KEY REFERENCES todos(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    rules JSONB NOT NULL
);

-- Each immutable snapshot belongs to one preparation operation, never to the
-- lifetime of a Todo. Business associations and overrides remain editable.
CREATE TABLE IF NOT EXISTS workspace_naming_plans (
    operation_id TEXT PRIMARY KEY,
    todo_id TEXT NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    plan JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS workspace_naming_plans_todo ON workspace_naming_plans(todo_id);
