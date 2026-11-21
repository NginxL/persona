The user's Lark chats read through lark-cli. The Open Platform does not expose unread counts, so the default and `recent` filter list chats by recent activity; `at_me` searches messages that mention the user. Each result includes a chat id and name for `larkcli_messages`.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "filter": {
      "type": "string",
      "enum": ["unread", "at_me", "recent"],
      "description": "recent by default; unread falls back to recent because the Open Platform has no unread state"
    },
    "limit": {
      "type": "integer",
      "description": "default 30"
    }
  }
}
```
