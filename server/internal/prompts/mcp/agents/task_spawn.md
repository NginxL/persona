Start a separate coding Agent for part of your current supervised task, on your current computer and directory. It joins the SAME to-do with the SAME supervisor; this does not make you a task leader or create a new task. Use this instead of a runtime's standalone new-conversation tool (for example Codex create_thread). Title and prompt must describe work within the existing assignment; ask your supervisor for an unrelated task. The server chooses the task, parent, launcher and permission from your verified assignment, never from arguments. Unknown permission stays read-only. The result names the child session, to-do and supervisor. Do not retry a start with an uncertain result.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "title": {
      "type": "string",
      "description": "Short title for this part of the existing task"
    },
    "prompt": {
      "type": "string",
      "description": "Self-contained assignment within the current task; include what evidence to return"
    }
  },
  "required": ["title", "prompt"],
  "additionalProperties": false
}
```
