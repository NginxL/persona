Send a text request to another Persona session, start a new Agent, or supply input on an existing A2A task. Use a new message_id for each message and reuse it on retries. Set machine_id and session_id for an existing session. Set machine_id and cwd to start an Agent, with an optional launcher. For a reply set task_id. With wake enabled (the default), questions and results return to your session after your current turn ends. You can stop your turn while waiting.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "message_id",
    "text"
  ],
  "properties": {
    "machine_id": {
      "type": "string"
    },
    "session_id": {
      "type": "string"
    },
    "task_id": {
      "type": "string"
    },
    "context_id": {
      "type": "string"
    },
    "message_id": {
      "type": "string"
    },
    "text": {
      "type": "string",
      "maxLength": 12000
    },
    "wake": {
      "type": "boolean"
    },
    "cwd": {
      "type": "string"
    },
    "launcher": {
      "type": "string"
    }
  }
}
```
