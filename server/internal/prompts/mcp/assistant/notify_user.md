Send the user a push notification right now (works when the app is closed). Tapping it opens this conversation. Use for results of scheduled runs and anything time-sensitive; don't notify for ordinary replies (they get one automatically).

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
      "description": "How urgent this is. P1 (default): worth knowing; in the hub's low-distraction mode it is held for the briefing instead of alerting. P0: the user must act now, so it alerts even in low-distraction mode. Use P0 only when waiting would cause real harm."
    },
    "related_key": {
      "type": "string",
      "description": "Optional: what this is about, such as a PR link or a to-do id. In low-distraction mode the same key interrupts at most once in 12 hours, and held notices about it are listed together."
    }
  }
}
```
