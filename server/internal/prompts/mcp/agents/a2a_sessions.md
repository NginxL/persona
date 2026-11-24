List independent Persona peer sessions enrolled for A2A communication, optionally on one machine (subagents are excluded from discovery). Use the exact machine_id and session_id when messaging an existing session, or call a2a_send with machine_id and cwd (and optional launcher) to start a new cross-machine or cross-harness subagent.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "machine_id": {
      "type": "string"
    }
  }
}
```
