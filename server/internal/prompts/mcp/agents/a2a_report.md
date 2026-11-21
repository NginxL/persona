Report progress, a question, or the result of an A2A task received by your current session. input_required asks the sender for more information. completed and failed finish the task and return the text. Use a distinct message_id and reuse it on retries.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "task_id",
    "message_id",
    "state",
    "text"
  ],
  "properties": {
    "task_id": {
      "type": "string"
    },
    "message_id": {
      "type": "string"
    },
    "state": {
      "type": "string",
      "enum": [
        "progress",
        "input_required",
        "completed",
        "failed"
      ]
    },
    "text": {
      "type": "string",
      "maxLength": 12000
    }
  }
}
```
