Stage the recurring task's state note for this scheduled run. Call near the end of the run with the run key in the scheduled prompt and the full replacement state (up to 4096 UTF-8 bytes). Keep cursors, references, and what was already reported. The hub attempts to save the draft after confirming that the run succeeded. If it cannot track the run or write the note, later runs use the last saved note. An empty string clears the note when saved.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["run_key", "text"],
  "properties": {
    "run_key": { "type": "string" },
    "text": { "type": "string" }
  }
}
```
