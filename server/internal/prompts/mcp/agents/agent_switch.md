Move a running Claude Code agent (from agents_list) to another launcher on its computer and go on with it, in one step: the agent is quit and resumed on that launcher in the same terminal, with the same conversation, and `text` is sent to it there. It is the switch the user makes from the agent's chat. Use it only when the user asked for it, for example when a subscription's usage limit stopped agents and the user wants them to go on with another provider now rather than wait for the reset; never on your own. The launcher must be one of that computer's (machines_list and agents_list show the names). The switch happens now or not at all: an agent that is working, waits for the user (a question, a permission, a plan), is starting, has background work its quit would end, or has a draft or a dialog in its input box is refused with nothing typed and nothing left waiting; tell the user why, and try again once it has stopped if they still want it. Other refusals say why too (a launcher that does not run there or is not signed in, a model the launcher's default cannot be read for: give model). Not for stopped agents (agent_send with resume), assistant conversations, read-only agents, or Codex and other agents. An agent its usage limit stopped goes on with what it was doing when `text` is left out; any other agent needs `text`. Its usage-limit continuation, if one was due, is not sent afterwards.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["machine", "session_id", "launcher"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "the machine's id or name, as agents_list shows it"
    },
    "session_id": {
      "type": "string",
      "description": "the agent's session id, or its first 8 characters"
    },
    "launcher": {
      "type": "string",
      "description": "the launcher to move it to, by its name on that computer"
    },
    "model": {
      "type": "string",
      "description": "a model of that launcher; empty: the launcher's default"
    },
    "effort": {
      "type": "string",
      "description": "the reasoning effort on the new launcher; empty: as it is"
    },
    "text": {
      "type": "string",
      "description": "what the agent does on the new launcher, sent as the user would type it; empty only for an agent its usage limit stopped (it goes on with what it was doing)"
    }
  }
}
```
