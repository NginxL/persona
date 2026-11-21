Ring the owner's iPhone like a phone call about a matter that can't wait; answering starts a voice conversation with you about it. Only for the two cases in the section of your instructions about urgent_call: the owner asked for a call about this (why owner_asked, owner_quote: their exact words), or it matches one of the urgent rules and is happening now (why rule, rule: its id, happening: one factual line). Stop a misbehaving agent first. Pass the card's interaction_id when there is one; otherwise say in body what the owner must do. The manager checks all of this; when the call may not ring it sends the matter as a P0 notification instead and says why, so don't send it again.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["title", "why"],
  "properties": {
    "title": {
      "type": "string",
      "description": "A few words for the lock screen, at most 30 characters."
    },
    "why": {
      "type": "string",
      "enum": ["owner_asked", "rule"]
    },
    "owner_quote": {
      "type": "string",
      "description": "why owner_asked: the owner's own words asking for the call, copied exactly."
    },
    "rule": {
      "type": "string",
      "description": "why rule: the id of the rule it matches."
    },
    "happening": {
      "type": "string",
      "description": "why rule: what is happening now, one factual line of 8 to 120 characters (numbers, what is affected)."
    },
    "interaction_id": {
      "type": "string",
      "description": "The pending card the call is about."
    },
    "body": {
      "type": "string",
      "description": "Without a card: what the owner needs to do or decide."
    }
  }
}
```
