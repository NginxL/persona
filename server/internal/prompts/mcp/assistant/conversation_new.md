Open a chat the owner requested. Returns its ID and preset configuration. It reports via conversation_send; failure or silence leaves one notice. Respect read-only refusals.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "text"
  ],
  "properties": {
    "text": {
      "type": "string",
      "description": "Self-contained first message"
    },
    "tags": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "Only if asked; up to 10 tags, 32 characters each"
    },
    "tier": {
      "type": "string",
      "description": "Active profile preset; unknown or assistant-incompatible presets are refused"
    },
    "pin": {
      "type": "boolean",
      "description": "Ask for approval to pin after the first turn"
    },
    "quote": {
      "type": "string",
      "description": "“latest”, a message ID or the owner’s exact words"
    }
  }
}
```
