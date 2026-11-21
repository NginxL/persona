Open a done Persona to-do again, when the user says it is not done after all. The user's own to-do is reopened at once; a task's, or one agents worked on, is confirmed on the phone first.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["id"],
  "properties": {
    "id": {
      "type": "string",
      "description": "the to-do's id (todo_…), as todo_list gives it"
    }
  }
}
```
