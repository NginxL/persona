On the user's request, add a reference for this assistant using Settings > Repositories URLs/metadata and existence checks through its provider. Refuse duplicates, concurrent changes and URL credentials. A permission card shows normalized details; only the user's own approval saves. Returns the saved binding with id and verified_at. Reference only, not authorization or an allowlist: no clone, source fetch, permission grant, machine or local checkout. If saving is uncertain or its response lost, inspect repositories_list/repository_get before retrying. Undo in Settings > Repositories.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["url"],
  "properties": {
    "url": {"type": "string", "minLength": 1, "maxLength": 2048, "description": "Repository URL, repository page, or git SSH remote without credentials."},
    "provider": {"type": "string", "enum": ["github", "gitlab"], "description": "Required for a self-hosted GitHub/GitLab; other providers are detected from the host."},
    "name": {"type": "string", "maxLength": 80},
    "aliases": {"type": "array", "maxItems": 10, "items": {"type": "string", "minLength": 1, "maxLength": 80}},
    "purpose": {"type": "string", "maxLength": 500}
  }
}
```
