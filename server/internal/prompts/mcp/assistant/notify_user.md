Send the user a push notification right now (works when the app is closed). Tapping it opens this conversation. Use for results of scheduled runs and anything time-sensitive; ordinary replies already notify. In the hub's push grading, a notice from a conversation other than the main one is held for the briefing instead of alerting unless it is P0; the tool answer then says it was moved to the briefing, so treat the user as not alerted.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["title", "body"],
  "properties": {
    "title": {
      "type": "string"
    },
    "body": {
      "type": "string"
    },
    "push_level": {
      "type": "string",
      "enum": ["P0", "P1"],
      "description": "How urgent this is. P1 (default): worth knowing; in the hub's push grading it is held for the briefing instead of alerting, and from a non-main conversation it is held even without an explicit level. P0: something the user must do or decide themselves (an action only they can take, a decision only they can make), so it alerts even in push grading and from any conversation. Use P1 for results, progress and conclusions."
    },
    "related_key": {
      "type": "string",
      "description": "Optional: what this is about, such as a PR link or a to-do id. In push grading the same key interrupts at most once in 12 hours, and held notices about it are listed together."
    }
  }
}
```
