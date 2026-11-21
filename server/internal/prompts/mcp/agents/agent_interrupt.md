Stop what an agent (from agents_list) is doing right now, and optionally redirect it: its current turn is cut off (Esc in its terminal, or its runtime's cancel), the agent and its session stay, and `then` is sent to it at once as the user would type it. Use it when waiting is worse than stopping: it works in the wrong direction, waits in a loop (sleep, polling), repeats a failing step, collides with another agent, or the user said stop. To add a word to an agent that works fine, use agent_send instead (queued behind its turn). What it did already is not undone (files it changed, commands it ran): tell it in `then` what to put back. Not for assistant conversations, and not for agents that can only be read. A `then` of only /exit or /quit to an agent on a task another conversation supervises is refused, as agent_send refuses it: nothing is stopped or sent.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["machine", "session_id"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "the machine's id or name, as agents_list shows it"
    },
    "session_id": {
      "type": "string",
      "description": "the agent's session id, or its first 8 characters"
    },
    "then": {
      "type": "string",
      "description": "the new instruction, sent right after it stopped; empty: just stop it"
    }
  }
}
```
