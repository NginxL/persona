Submit a review on a GitHub pull request as the user: approve it, request changes, or comment. The user approves it on their phone first (an approval says so in bold), unless their rules allow it. Only approve when the user has said to.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "owner/name"},
    "number": {"type": "integer", "description": "The pull request number"},
    "event": {"type": "string", "enum": ["APPROVE", "REQUEST_CHANGES", "COMMENT"], "description": "APPROVE, REQUEST_CHANGES (body required) or COMMENT (body required)"},
    "body": {"type": "string", "description": "The review's text, Markdown"}
  },
  "required": ["repo", "number", "event"],
  "additionalProperties": false
}
```
