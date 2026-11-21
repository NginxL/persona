List the user's iPhones the assistant may operate: those paired with one of the user's computers, with that computer turned on for it and the iPhone's own consent in its Persona app. For each: id, name, model, iOS version, the computer it is reached through, whether it is connected, locked and ready, what is missing (note), and what the iPhone says of itself now (detail: the app in front, the screen…). A real iPhone gives no element tree (`ax_tree` false in detail's capabilities; only a simulator has one) and is still ready: work from iphone_look's screenshot by points, and every input is asked on the user's phone first. Changes nothing. `not_allowed` counts the iPhones that are paired but not allowed: only the user can allow them, on that iPhone.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {}
}
```
