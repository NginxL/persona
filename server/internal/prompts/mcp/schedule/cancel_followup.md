Cancel a scheduled follow-up or a repeating task by id (from list_followups).{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["id"],
  "properties": {
    "id": {
      "type": "string"
    }
  }
}
```
