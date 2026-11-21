Read a to-do's title/state, priority, due/reminder and push status, timestamps, origin/plan, notes, agents, decisions and canonical repository/issue/PR associations. Maintain links with todo_repository_links_add or the revision-checked set tools; associations do not prove completion.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "id"
  ],
  "properties": {
    "id": {
      "type": "string",
      "description": "the to-do's id (todo_…), as todo_list gives it"
    }
  }
}
```
