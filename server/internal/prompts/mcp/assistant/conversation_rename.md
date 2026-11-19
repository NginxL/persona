Rename: direction name via main-only direction_name (no date/kind); ordinary kind/topic becomes MMDD | kind | topic using its start date. User-chosen names/titles require user_asked. Main has no title.

```json
{
  "type": "object",
  "required": [
    "id"
  ],
  "properties": {
    "id": {
      "type": "string",
      "description": "conversation id or first 8 characters"
    },
    "name": {
      "type": "string",
      "maxLength": 40,
      "description": "direction: plain project-supervisor name"
    },
    "kind": {
      "type": "string",
      "enum": [
        "功能",
        "设计",
        "修复",
        "优化",
        "发布",
        "探索",
        "文档",
        "研究",
        "评审",
        "定时"
      ],
      "description": "ordinary conversation kind"
    },
    "topic": {
      "type": "string",
      "description": "ordinary topic, at most 14 characters"
    },
    "user_asked": {
      "type": "boolean",
      "description": "explicit user request to replace their title/name"
    }
  }
}
```
