Replaces this meeting's topics with the whole list given: the agenda as it has gone so far, in the order the topics came up or are to come, each done, current (the one being discussed now; at most one) or open (still to come). Call it after a batch of lines only when the meeting moved on: a new topic began, one was finished, or one still to come was named; otherwise leave the topics as they are. Short titles in the meeting's language. The topics are shown on the owner's phone, its Lock Screen and the web, and read with the meeting's material. At most 20 topics, a title of at most 80 characters, a note of at most 200; a list over a limit is refused, saying which, and nothing changes.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["topics"],
  "properties": {
    "topics": {
      "type": "array",
      "maxItems": 20,
      "description": "Every topic, the earliest first: the list replaces the one before.",
      "items": {
        "type": "object",
        "required": ["title", "state"],
        "properties": {
          "title": {
            "type": "string",
            "description": "The topic in a few words: at most 80 characters."
          },
          "state": {
            "type": "string",
            "enum": ["done", "current", "open"],
            "description": "done: finished; current: being discussed now (at most one); open: still to come."
          },
          "at": {
            "type": "number",
            "description": "When the topic began, in seconds from the meeting's start: the offset meeting_lines shows first in each line (the m:ss before the ·, or h:mm:ss past an hour). A batch's [HH:MM], and the HH:MM after the · in meeting_lines, are clock times, not offsets. Absent for one that has not begun."
          },
          "note": {
            "type": "string",
            "description": "Optional: what was decided or is still open on it, at most 200 characters."
          }
        }
      }
    }
  }
}
```
