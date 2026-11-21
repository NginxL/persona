Search GitHub as the user (their gh login on one of their machines). kind "issues" (default) finds issues and pull requests with GitHub's search syntax, e.g. "is:pr is:open review-requested:@me", "involves:@me is:open updated:>2026-01-01", "repo:owner/name label:bug"; add involves:@me unless the user asked about something wider. kind "code" searches code ("repo:owner/name symbol"). Returns one line per result with its repo#number, state, title, author, last update and URL; read one with github_read.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "GitHub search query"},
    "kind": {"type": "string", "enum": ["issues", "code"], "description": "issues (issues and pull requests, the default) or code"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "How many results (default 20)"}
  },
  "required": ["query"],
  "additionalProperties": false
}
```
