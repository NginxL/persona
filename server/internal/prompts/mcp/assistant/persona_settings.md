Read and change Persona's own settings for the user: their inbox sorting rules, how their inbox items link to their tasks, 关于我 (what you know about them), your default launcher (model), their mail accounts, which Claude Code config directories each machine keeps out of Agents, and each extension's settings (extension.<id>: what Settings › Extensions shows, like turning one of its features on). Start with action list (what there is, what each takes); get reads one. set / add / remove put a card on the user's phone that says what changes (before → after) and nothing changes until they approve; what the card needs filled in, like a mail password, the user types there, so never ask for passwords in the conversation. Change only what the user asked for.

`assistant.standing_authorizations`: get returns `{revision, authorizations}`. Only the main conversation may set the whole list, with its current revision and the user’s own approval on the phone. Each grant names one `machine_id`, a specific absolute `directory`, `actions` (merge, push, deploy-test, deploy-production, send, delete, spend), `conditions`, the user’s exact `quote`, `source`, and `reversible`. Keep existing grants unchanged by ID; to change a grant, remove it and add a new entry without an ID. The server assigns the new ID and grant time. Never invent a user quote or expand the scope. Conditions may retain the user’s words; only independently verifiable conditions can permit automatic answers. Revocation is available in Settings through the authorization list. Tool permissions are never automatically approved.

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["list", "get", "set", "add", "remove"],
      "description": "default list"
    },
    "key": {
      "type": "string",
      "description": "the setting, from list (inbox.rules, mail.accounts, extension.<id>…)"
    },
    "value": {
      "type": ["string", "number", "integer", "boolean", "object", "array", "null"],
      "description": "set: the new value; add: the item (each setting's list entry says what)"
    },
    "id": {
      "type": "string",
      "description": "remove: which item (from get)"
    }
  },
  "additionalProperties": false
}
```
