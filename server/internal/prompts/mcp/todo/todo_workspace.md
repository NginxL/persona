Manage an explicitly requested backend workspace for one to-do. Legacy Engine creation and folder reuse stay available. inspect reads saved and live state. First prepare needs operation_id, machine_id, layout and selected_repo_ids from todo_repositories_get; all selected repos must be ready. Inspect unknown outcomes before retrying the same operation. To correct a branch/path, cancel, edit associations or naming, then prepare the same to-do with a new operation_id. Unchanged successful repos and local work survive; conflicting resources need explicit cleanup. Cleanup removes worktrees and retains branches by default. Use force (discards unsaved work) or delete_branch only for that explicitly requested effect. Prepare after cleanup with a new operation_id. No action starts an Agent or changes the to-do stage.

```json
{
  "type": "object",
  "required": ["id", "action"],
  "properties": {
    "id": {"type": "string"},
    "action": {"type": "string", "enum": ["inspect", "prepare", "cancel", "cleanup"]},
    "operation_id": {"type": "string"},
    "machine_id": {"type": "string"},
    "layout": {"type": "string", "enum": ["root", "container"]},
    "selected_repo_ids": {"type": "array", "items": {"type": "string"}},
    "force": {"type": "boolean"},
    "delete_branch": {"type": "boolean"}
  },
  "additionalProperties": false
}
```
