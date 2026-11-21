Legacy inbox_suggest `reply`. Prefer inbox_suggest. Draft a reply for this conversation’s inbox item. It waits in the user's inbox; they read it and send it with one tap (you never send it yourself). `text` is the reply in the user's voice, ready to go out as it is: no blanks to fill in (【…】) — leave out what you could not find and say so in `note`; `note` is one or two sentences on what you found and where, for the user to check the draft against. For a Feishu sheet / Base item, `changes` proposes moving rows' status (ref, field and one of the values the item lists); they are made when the user sends. Call it once, at the end.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["text"],
  "properties": {
    "processing_turn": {
      "type": "string"
    },
    "text": {
      "type": "string"
    },
    "note": {
      "type": "string"
    },
    "changes": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["ref", "field", "value"],
        "properties": {
          "ref": {
            "type": "string"
          },
          "field": {
            "type": "string"
          },
          "value": {
            "type": "string"
          }
        }
      }
    }
  }
}
```
