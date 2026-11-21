Archive a Persona conversation: it leaves the sidebar's list (it is kept, and listed under 已归档), nothing is deleted. Only when the user asks to tidy up or archive. The main conversation can't be archived. Archiving this conversation itself is allowed: the current turn finishes, and a new message from the user brings it back.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["id"],
  "properties": {
    "id": {
      "type": "string",
      "description": "full id or its first 8 characters"
    }
  }
}
```
