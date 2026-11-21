What a GitHub pull request changes. Without path: every changed file with its status and lines added / removed. With path: that file's patch (cut when very long; a file too big for GitHub to show has only its counts).{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "owner/name"},
    "number": {"type": "integer", "description": "The pull request number"},
    "path": {"type": "string", "description": "One changed file's path, for its patch"}
  },
  "required": ["repo", "number"],
  "additionalProperties": false
}
```
