Search the user's iMessage / SMS messages for a word or phrase (plain substring, any case; not a query language): the newest matching messages, each with its conversation's name and id, who wrote it and when. Covers the last year by default. Only real conversations are searched (someone in the user's contacts, or the user has written there); one-time codes are masked. Read the conversation around a hit with imessage_read.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Text the message contains"},
    "days": {"type": "integer", "minimum": 1, "maximum": 3650, "description": "How far back to look, in days (default 365)"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "How many messages (default 20)"}
  },
  "required": ["query"],
  "additionalProperties": false
}
```
