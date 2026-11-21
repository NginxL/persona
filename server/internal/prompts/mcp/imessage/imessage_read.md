Read one iMessage / SMS conversation of the user's: its newest messages, oldest first, with who wrote each one and when (what a reply answers, attachments as [图片] / [文件 name] / [语音], group changes as lines; one-time codes masked). Ask again with the before value it gives for earlier messages.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "chat": {"type": "string", "description": "The conversation's id (imessage-<12 hex>) from imessage_chats or imessage_search"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 200, "description": "How many messages (default 40)"},
    "before": {"type": "string", "description": "The before value a previous answer gave, for earlier messages"}
  },
  "required": ["chat"],
  "additionalProperties": false
}
```
