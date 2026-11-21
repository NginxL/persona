Read this assistant's saved reference by id from repositories_list: provider, instance, full path, canonical URL, optional name/aliases/purpose, verified_at. Retrieve details only when useful; the user's explicit repository URL or path takes precedence. A reference is not authorization or an allowlist; never infer a machine or local checkout path. Saved metadata only: no remote check, source read, clone, credentials or permission grant.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["id"],
  "properties": {
    "id": {"type": "string", "minLength": 1, "maxLength": 80, "description": "Stable saved binding id returned by this assistant's repositories_list, not a URL, machine, path or another assistant's id."}
  }
}
```
