Read a Lark chat through lark-cli (the id comes from `larkcli_chats` or `larkcli_search`). Results are JSON messages in oldest-first order. Pass `before` as the previous page token to continue. Pass `thread` to read replies in a topic. Reading does not mark anything as read.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["chat_id"],
  "properties": {
    "chat_id": {
      "type": "string"
    },
    "thread": {
      "type": "string",
      "description": "a topic's message id; read that topic's replies instead of the chat"
    },
    "limit": {
      "type": "integer",
      "description": "default 40, max 50"
    },
    "before": {
      "type": "string",
      "description": "the page token returned by an earlier read"
    },
    "account": {
      "type": "integer",
      "description": "retained for gateway-compatible calls; lark-cli uses its configured profile"
    }
  }
}
```
