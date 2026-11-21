Read the screen of one of the user's iPhones (one iphone_status lists): the app in front, a numbering, every element as `[N] role "label" frame`, and a screenshot. Use the numbers in iphone_do as `{"n": N}` with that numbering; they stay put while the screen does, and `+` / `~` mark elements new or moved since the last read. A real iPhone, and some screens, give no elements, only the screenshot: then act on points (`{"x", "y"}` in screen points), and every input is asked on the user's phone first. Changes nothing. A locked iPhone shows only its lock screen: ask the user to unlock it.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "phone": {
      "type": "string",
      "description": "iPhone id or name from iphone_status; omit when there is only one"
    },
    "screenshot": {
      "type": "boolean",
      "description": "default true; false returns the elements only"
    }
  }
}
```
