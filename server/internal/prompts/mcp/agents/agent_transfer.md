Move an open Agent task to a current direction or main: main moves any; a lead hands off only its own. Keep task, brief, criteria, estimate, origin and Agent; no message, interruption or restart. Refuses user-owned to-dos, assistant sessions and untracked work (adopt with agent_supervise).{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "machine",
    "session_id",
    "supervisor"
  ],
  "properties": {
    "machine": {
      "type": "string",
      "description": "agents_list machine id or name"
    },
    "session_id": {
      "type": "string",
      "description": "Agent session id or first 8 characters"
    },
    "supervisor": {
      "type": "string",
      "description": "exact direction or main id from conversations_list"
    }
  }
}
```
