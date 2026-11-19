Replace a conversation's tags; [] clears them. These user-defined labels group conversations in apps and lists. They are trimmed and de-duplicated, at most 10 of 32 characters each. Tag only conversations in your own space when the user asks for tags or grouping; never invent them. This conversation is a valid id.

```json
{
  "type": "object",
  "required": ["id", "tags"],
  "properties": {
    "id": {
      "type": "string",
      "description": "full id or first 8 characters"
    },
    "tags": {
      "type": "array",
      "items": { "type": "string" },
      "description": "complete replacement list; [] clears all"
    }
  }
}
```
