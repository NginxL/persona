Search the knowledge base: summaries and metadata of chats, people, projects, docs, meetings, events, recordings (with their minutes and any transcript still retained), topics (curated knowledge points: how something works, what was decided and why, the steps and pitfalls), project timelines, the notes directories the user added (hits with an absolute path) and, with messages=true, the full message archive (newest days first). All words must match. Hits come in the order the files are read, not by relevance, so a broad search can fill the limit with chats: give kind to search one kind only, the entries whose name matches first, then as kb_list ranks them. A past meeting (its decisions, what is still open, who does what) is kind=recordings: found by its title, the calendar event's title, its minutes or any transcript still retained; then kb_get it. Transcripts expire before minutes: never promise a trimmed recording's full transcript.{{template "shared/mcp_language" .}}

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
      "enum": ["projects", "chats", "people", "docs", "meetings", "events", "recordings", "topics"],
      "description": "only this kind's entries (no notes directories; the message archive only with chats)"
    },
    "messages": {
      "type": "boolean",
      "description": "also search the message archive (default true)"
    },
    "limit": {
      "type": "integer",
      "description": "default 40"
    }
  }
}
```
