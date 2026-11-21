Create a persistent live card in the calling assistant conversation. Use the same View schema as persona-view and extension UI: sections of components, a required plain-text fallback, and optional named pages. The card stays at its feed position; use view_update to change it. The hub validates the View and returns field-level errors for correction. At most 64 KB per card and 200 cards per conversation. This does not send a push notification. Keep the conclusion in your normal reply too. Returns {id, version}; save the id for updates. Do not collect passwords or secrets in a card. An html component can call persona.reply / persona.open only when it has a key, and each call is confirmed by the user; the card's one reply is shared with its reply actions.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["view"],
  "properties": {
    "view": {"type": "object", "description": "A View from schema/extension/view.schema.json with a required plain-text fallback and sections. Only assistant reply/open/push.page/pop actions, not extension ui.action ids."}
  }
}
```
