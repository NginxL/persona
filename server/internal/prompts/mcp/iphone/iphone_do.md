Act on one of the user's iPhones, in one call: `steps` in order, stopping at the first that fails. A step is `{"action": …}`; a target is an element `{"n": N}` from iphone_look (with its `numbering`), `{"label": "…", "role"?: "…"}`, or a point `{"x": …, "y": …}`. Actions: `tap` {target, count?: 2 for a double tap}, `long_press` {target, ms?}, `drag` {target, to, ms?, hold_ms?}, `swipe` {target?, to} or {direction: up|down|left|right, distance?}, `type` {text, target?: tapped first}, `key` {key: return|backspace|delete|tab|escape|space|up|down|left|right, repeat?}, `button` {button: home|lock|volume_up|volume_down}, `launch` / `terminate` {app: bundle id}, `wait` {ms}. `screenshot: true` adds one after the steps; `timeout_ms` is the time for all of it (default 30000, at most 120000). Read first with iphone_look when unsure what is on screen. Steps that pay, delete, send a message or type a password, and every input on a screen without elements, are asked on the user's phone first (every input, if the user set that); a refusal is the user's, so do not retry it and do not try to approve it yourself. A failure says whether anything reached the iPhone: when it may have, look with iphone_look before doing anything else.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["steps"],
  "properties": {
    "phone": {
      "type": "string",
      "description": "iPhone id or name from iphone_status; omit when there is only one"
    },
    "steps": {
      "type": "array",
      "items": {
        "type": "object"
      },
      "description": "actions in order: {\"action\": \"tap\", \"target\": {\"n\": 3}}, {\"action\": \"type\", \"text\": \"…\"}"
    },
    "numbering": {
      "type": "integer",
      "description": "the numbering of the iphone_look the element numbers come from; default the latest"
    },
    "screenshot": {
      "type": "boolean",
      "description": "a screenshot after the steps"
    },
    "timeout_ms": {
      "type": "integer",
      "description": "time for the steps, default 30000, max 120000"
    }
  }
}
```
