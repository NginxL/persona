Search a user-configured knowledge folder on another computer. Call with no arguments to list source names, then pass a source and a case-insensitive literal query. Results contain relative paths for kb_folder_read. A truncated result covers only part of the folder; an unreachable folder is an error.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "source": {"type": "string"},
    "query": {"type": "string"},
    "limit": {"type": "integer", "description": "Default 40, maximum 200"}
  }
}
```
