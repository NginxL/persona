Read the page this conversation's browser tab (ego lite, task space of its own, tab p1) shows on one of the user's Macs: its address, title, an accessibility snapshot whose rows carry `[ref=N]` (use them in browser_run as `page.click("@N")`), and a screenshot. `full` reads the whole page, not just what is on screen. Changes nothing.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["machine"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "machine id or name"
    },
    "full": {
      "type": "boolean",
      "description": "the whole page, not just the viewport"
    }
  }
}
```
