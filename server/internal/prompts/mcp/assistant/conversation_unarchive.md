Bring an archived Persona conversation back into the sidebar's list (find it with conversations_list and include_archived).{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["id"],
  "properties": {
    "id": {
      "type": "string",
      "description": "full id or its first 8 characters"
    }
  }
}
```
