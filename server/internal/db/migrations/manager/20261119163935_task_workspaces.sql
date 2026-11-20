-- Backend preparation is an additional path. No legacy Todo or engine workspace
-- fields change meaning, and old clients need not echo any of these records.
CREATE TABLE IF NOT EXISTS task_workspace_operations (
    operation_id TEXT PRIMARY KEY,
    todo_id TEXT NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL,
    revision BIGINT NOT NULL,
    record JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS task_workspace_operations_todo ON task_workspace_operations(todo_id);
CREATE TABLE IF NOT EXISTS task_workspace_heads (
    todo_id TEXT PRIMARY KEY REFERENCES todos(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL REFERENCES task_workspace_operations(operation_id)
);
