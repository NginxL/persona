Switch atomically to a model profile by id when the user asks (for example economy or performance). Saves a before-image for model_profile_rollback and records actor/time. Applies on next model call or Agent start; does not interrupt running work. Inspect profiles first; do not assume economy needs any particular model.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "Profile id from model_profiles_list"
    }
  },
  "additionalProperties": false,
  "required": [
    "id"
  ]
}
```
