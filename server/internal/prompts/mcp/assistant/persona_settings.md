Read and change Persona's settings: inbox sorting and task links, 关于我, default launcher, mail accounts, machine Claude Code exclusions and extensions. List keys and inputs; get reads one. set/add/remove show before → after on the phone and wait for approval; secrets are entered there, never requested in chat. Change only what was asked.

Model groups `model_config.kb`, `model_config.inbox.triage`, `model_config.agent.delegate` and `model_config.translate`: `get` returns their effective binding, revision and compatible `purpose.provider_ids`. `set` and `test` take one listed provider ID as value. A changed provider clears the old model, effort and permission mode. `test` only checks the connection, without approval or saving; an unverified or offline provider may still be saved.

`assistant.standing_authorizations`: only the main conversation may replace the list at its current revision with the user’s phone approval. Each grant names `machine_id`, absolute `directory`, `actions` (merge, push, deploy-test, deploy-production, send, delete, spend), `conditions`, exact user `quote`, `source` and `reversible`. Keep unchanged grants by ID; replace one by removing it and adding it without an ID. Never invent a quote or expand scope. Only independently verifiable conditions may permit automatic answers. Revoke in Settings. Tool permissions are never automatically approved.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["list", "get", "set", "test", "add", "remove"],
      "description": "default list"
    },
    "key": {
      "type": "string",
      "description": "the setting, from list (inbox.rules, mail.accounts, extension.<id>…)"
    },
    "value": {
      "type": ["string", "number", "integer", "boolean", "object", "array", "null"],
      "description": "set: the new value; test: the provider ID to check; add: the item (each setting's list entry says what)"
    },
    "id": {
      "type": "string",
      "description": "remove: which item (from get)"
    }
  },
  "additionalProperties": false
}
```
