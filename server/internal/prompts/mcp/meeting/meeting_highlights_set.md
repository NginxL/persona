Replaces this meeting's highlights with the whole list given: the points that matter most in the meeting so far (decisions, conclusions, commitments, open questions), 3 to 4 once the meeting has that many, fewer before. Each is one short sentence in the meeting's language that states the point itself rather than that it was discussed, with the clock time of the line where it came to a head. Call it after a batch of lines only when the batch changed what matters most; otherwise leave the highlights as they are. Send the ones you keep again word for word with their said_at, never clear them, and name a person only when the words name them. The highlights are shown on the owner's phone and read with the meeting's material. At most 4 highlights, a text of at most 80 characters; a list over a limit, or a said_at that is not the minute of a line of the meeting so far, is refused, saying which, and nothing changes.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["highlights"],
  "properties": {
    "highlights": {
      "type": "array",
      "maxItems": 4,
      "description": "Every highlight: the list replaces the one before. They are kept in the order of their times.",
      "items": {
        "type": "object",
        "required": ["text", "said_at"],
        "properties": {
          "text": {
            "type": "string",
            "description": "The point in one short sentence: at most 80 characters."
          },
          "said_at": {
            "type": "string",
            "description": "The HH:MM of the line where the point came to a head, copied as shown: a batch's [HH:MM], a highlight's in the highlights so far, or the HH:MM after the · in meeting_lines. A clock time, never an offset from the meeting's start (the m:ss before the · in meeting_lines, or a topic's at); never work one out from an offset."
          }
        }
      }
    }
  }
}
```
