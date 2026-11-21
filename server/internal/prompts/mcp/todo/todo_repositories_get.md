Read a to-do's saved repository/issue/PR associations and catalog status. No Git/network calls.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "id"
  ],
  "properties": {
    "id": {
      "type": "string"
    }
  },
  "additionalProperties": false
}
```
