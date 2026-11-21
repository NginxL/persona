Search Lark through lark-cli. The `messages` kind searches messages; `chats`, `docs`, and `people` use the corresponding read-only CLI shortcuts.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": {
      "type": "string"
    },
    "kind": {
      "type": "string",
      "enum": ["messages", "docs", "people", "chats"],
      "description": "messages by default"
    }
  }
}
```
