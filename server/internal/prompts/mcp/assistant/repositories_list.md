Search this assistant's saved names, aliases, full paths and purposes to resolve repository identity; use repository_get for useful details. The user's explicit repository URL or path takes precedence. References are not authorization or an allowlist; never infer a machine or local checkout path. Returns saved metadata and verified_at only: no remote, source, clone or permission checks. Non-repository tasks need no lookup; this is not a mandatory dispatch preflight.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "query": {"type": "string", "maxLength": 200, "description": "Optional case-insensitive substring of a saved name, alias, full path or purpose; omit to browse. Preserve explicit user repository choices."},
    "cursor": {"type": "string", "maxLength": 1024, "description": "Opaque next_cursor from this assistant's previous page with the same query. Restart without it if the saved references changed."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Results per page, default 20; at most 50."}
  }
}
```
