Put this conversation into the user's Inbox as a row that opens it, so a result is not missed behind a quiet unread dot. Call it only when the user explicitly asked for it (for example "tell me if there is anything worth a look", "send me the briefing every time") or when the scheduled task you are running is set to put findings in the Inbox. Never decide on your own to interrupt the user: an ordinary reply, a run with nothing new, or something merely interesting does not go there. `title` is a short name for the row (the task's name or what was found), `summary` one or two sentences saying what to look at, `tier` is `action` when the user has to do something and `fyi` (the default) when it is worth knowing, and `reason` names the request or task setting that makes this post (the user can read it on the row). Calling it again for this conversation soon after updates the same row instead of adding another. It does not push to the phone: use notify_user for that when the user asked for a push.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["title", "summary", "reason"],
  "properties": {
    "title": {
      "type": "string"
    },
    "summary": {
      "type": "string"
    },
    "tier": {
      "type": "string",
      "enum": ["action", "fyi"]
    },
    "reason": {
      "type": "string"
    }
  }
}
```
