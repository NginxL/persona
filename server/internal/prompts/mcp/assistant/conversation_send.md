Message another Persona conversation. It waits for a running turn or 2 minutes of user quiet. Marked as from this one; replies use conversation_send. A few requests per user message (not for main; a scheduled wake-up resets), a few messages per pair per ten minutes; a first reply to a request adds no hop. Don't reply just to acknowledge. Policy refusals say why: follow them, don't retry. Owner-verbatim words to main stay allowed: start with them. Handing on a user request: set quote.

```json
{
  "type": "object",
  "required": ["id", "text"],
  "properties": {
    "id": {
      "type": "string",
      "description": "full id or its first 8 characters"
    },
    "text": {
      "type": "string"
    },
    "quote": {
      "type": "string",
      "description": "\"latest\" or a message id: the user's words, carried verbatim"
    }
  }
}
```
