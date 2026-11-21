List the user's iMessage / SMS conversations (the Messages app on their Mac), newest first: each one's name, id, whether it is a group and who is in it, when the last message came, how many are unread and the last message. Only real conversations are listed (someone in the user's contacts, or the user has written there); one-time codes are masked. Use imessage_read with an id to read a conversation, imessage_search to find messages by what they say.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "limit": {"type": "integer", "minimum": 1, "maximum": 100, "description": "How many conversations (default 30)"},
    "before": {"type": "string", "description": "The before value a previous answer gave, for older conversations"}
  },
  "additionalProperties": false
}
```
