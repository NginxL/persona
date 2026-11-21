Prepare one exact approval batch for side-effecting connector calls, such as several fully drafted mail_send operations. Use this only when the owner asked to review or approve the batch. Put every complete call in items; the cards, not a prose summary, are the approval authority. The tool waits for the batch decision. After approval, call each approved connector tool exactly once with the returned canonical arguments and no edits. A direct decision on one item approves only that item. Never treat this as an Always permission, never substitute another recipient or body, and never retry after an uncertain external result. Items that are too large for complete card display, read-only calls, unknown tools and non-connector runtime prompts are refused; use their ordinary approval path instead.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["items"],
  "additionalProperties": false,
  "properties": {
    "items": {
      "type": "array",
      "minItems": 1,
      "maxItems": 20,
      "items": {
        "type": "object",
        "required": ["tool", "input"],
        "additionalProperties": false,
        "properties": {
          "tool": {
            "type": "string",
            "description": "exact registered connector tool name, for example mail_send"
          },
          "input": {
            "type": "object",
            "description": "the complete exact arguments for this call, including every recipient, account, body and attachment field"
          }
        }
      }
    }
  }
}
```
