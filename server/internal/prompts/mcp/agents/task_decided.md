Record the owner's needs_user choice; main chat or supervisor only. Quote their words and exact option; never guess. told_agent=true means already delivered (also direct Agent chat): record only. Otherwise send and record. Both settle the ask.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["todo_id", "option", "said"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "task id"
    },
    "option": {
      "type": "string",
      "description": "exact option label"
    },
    "said": {
      "type": "string",
      "description": "owner's words"
    },
    "told_agent": {
      "type": "boolean",
      "description": "true: already delivered to the Agent, record only; false: relay and record"
    }
  }
}
```
