Move Claude Code agents to another launcher only when the user asks. Name `agents`, or select all running and Persona-started stopped agents, filtered by `machine` and `from_launcher` (default `claude`). Preview and explain first unless the user named agents and target. Usage-limited agents switch and continue; idle agents switch without sending; working/waiting/starting/background-work agents queue until their turn ends, retaining any due limit continuation. Stopped agents change the launcher used by Resume or a resuming message. Unsupported sessions, unavailable launchers, or unreadable default models (supply `model`) are refused unchanged. Failures roll back without retry; explain why. Results are per agent: switched/continued, switched, switch-on-resume, queued, or refused. Queues expire after a day or independent launcher change; stopped agents' Resume uses the target. `agents_list` marks queues `switch_queued=`. Conversation and terminal stay unchanged.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["launcher"],
  "properties": {
    "launcher": {
      "type": "string",
      "description": "target launcher name on each agent's computer"
    },
    "model": {
      "type": "string",
      "description": "target model; empty uses the launcher default"
    },
    "effort": {
      "type": "string",
      "description": "target effort; empty preserves each agent's setting"
    },
    "agents": {
      "type": "array",
      "description": "agents to move; empty selects eligible agents after machine/from_launcher filters",
      "items": {
        "type": "object",
        "required": ["session_id"],
        "properties": {
          "machine": {
            "type": "string",
            "description": "machine ID or name from agents_list"
          },
          "session_id": {
            "type": "string",
            "description": "session ID or its first 8 characters"
          }
        }
      }
    },
    "machine": {
      "type": "string",
      "description": "without agents, limit to this computer"
    },
    "from_launcher": {
      "type": "string",
      "description": "without agents, source launcher (default claude)"
    },
    "preview": {
      "type": "boolean",
      "description": "read-only; switches and queues nothing"
    }
  }
}
```
