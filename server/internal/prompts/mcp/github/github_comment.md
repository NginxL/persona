Comment on a GitHub pull request or issue as the user. Everyone who can see the repository sees it (for a public repository, everyone), so the user approves each comment on their phone before it is posted, unless their rules allow it. Write what the user would write; never post on your own initiative what they have not asked for.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "owner/name"},
    "number": {"type": "integer", "description": "The issue or pull request number"},
    "body": {"type": "string", "description": "The comment, Markdown"}
  },
  "required": ["repo", "number", "body"],
  "additionalProperties": false
}
```
