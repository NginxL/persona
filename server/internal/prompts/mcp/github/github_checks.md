The checks (CI) of a GitHub pull request's head commit, or of a commit / branch: each check's name, status and conclusion. For failed GitHub Actions jobs it adds the last part of the job's log, where the error usually is.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "owner/name"},
    "number": {"type": "integer", "description": "A pull request number (its head commit is checked)"},
    "ref": {"type": "string", "description": "A commit SHA or branch name, instead of number"}
  },
  "required": ["repo"],
  "additionalProperties": false
}
```
