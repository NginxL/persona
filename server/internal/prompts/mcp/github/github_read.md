Read one GitHub pull request or issue: title, state, author, labels, the description, and for a pull request its branches, merge state, requested reviewers and reviews; then its comments, newest first (long texts are cut). Use github_diff for what a pull request changes and github_checks for its CI.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "owner/name"},
    "number": {"type": "integer", "description": "The issue or pull request number"},
    "comments": {"type": "integer", "minimum": 0, "maximum": 50, "description": "How many of the newest comments and reviews to include (default 15)"}
  },
  "required": ["repo", "number"],
  "additionalProperties": false
}
```
