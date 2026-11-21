Cancel an A2A task you sent before it can reach the recipient. Returns the current task record.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "task_id"
  ],
  "properties": {
    "task_id": {
      "type": "string"
    }
  }
}
```
