List your Persona conversations, newest activity first: id, title, tags, last activity, main/running state and last-message gist. Archived ones need `include_archived`; this conversation is marked. Use it to identify the user's conversation before reading, renaming, archiving, tagging or messaging it.

```json
{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "case-insensitive match on title or last message"
    },
    "tag": {
      "type": "string",
      "description": "exact tag to match"
    },
    "include_archived": {
      "type": "boolean",
      "description": "include archived conversations (marked as such)"
    },
    "limit": {
      "type": "integer",
      "description": "default 30, max 100"
    }
  }
}
```
