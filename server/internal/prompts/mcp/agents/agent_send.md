Send a message to an agent as the user would type it; queued while it works. Only send what the user asked for. A stopped agent takes no plain message. Only a recorded non-user stop (idle, task completion, restart) permits resume; user and unknown stops need the user's Resume. If the next work needs its context (investigation followed by a fix, rework, the user asked for this agent), send with resume and resume_why: the same session and permission mode, your text first, the reason shown to the user. It must be supervised (give task unless it is on an open task of yours). Unrelated work, a nearly full context or an agent gone off track: agent_start a new one (agent_read the old). Unsure: ask_user. A failed resume says why. A user's branch that never ran is started by its first message. Only /exit or /quit stops a running agent (tab kept); finished tasks' agents stop automatically. /exit to another conversation's task is refused (the user can close it, or agent_archive with close).

A dialog holding its terminal refuses the send (dialog_open) with its first line and whether Enter alone closes it; then send with dismiss_notice true. Confirmations, permissions and pickers are never closed for you: tell the user or use interaction_answer.

To hand new work to a running agent (one the user opened, a long-lived one that passes work on), give task from the main conversation: once sent, the agent is your supervised task as agent_supervise makes one (a todo with the text as notes, done_when, a card; 督办 messages, task_review). Put the criteria in the text too: the agent reads only the text. An agent on one of your tasks takes more under the same title (done_when updated); another title, the user's own todo or another conversation's task is refused, with nothing sent. A failed send makes no task. Leave task out or null for a reply, nudge or decision; a task with only blank fields counts as left out.

```json
{
  "type": "object",
  "required": ["machine", "session_id", "text"],
  "properties": {
    "machine": {
      "type": "string"
    },
    "session_id": {
      "type": "string"
    },
    "text": {
      "type": "string"
    },
    "resume": {
      "type": "boolean",
      "description": "resume this stopped agent with its context, text as its first message"
    },
    "resume_why": {
      "type": "string",
      "description": "why its context is needed (shown to the user)"
    },
    "dismiss_notice": {
      "type": "boolean",
      "description": "Press Enter first to close a notice that only says \"Enter to continue\" (no options). Any other dialog still refuses the send."
    },
    "attachments": {
      "type": "array",
      "maxItems": 8,
      "items": { "type": "string" },
      "description": "pictures and files the user gave you for it, by their absolute paths on your computer: a picture they sent (the path Claude Code gives it), a file the app put in their message. They are copied to the agent's computer and listed at the end of its message, and the text's mentions of them name the copies. Only those the user gave you in this conversation are accepted"
    },
    "task": {
      "type": ["object", "null"],
      "description": "new work: make it your supervised task first (main conversation only); null for a reply",
      "additionalProperties": false,
      "properties": {
        "title": {
          "type": "string",
          "description": "the task in a short line (Chinese, under 20 characters)"
        },
        "done_when": {
          "type": "array",
          "maxItems": 6,
          "items": { "type": "string" },
          "description": "results you can check when it says it is done (a PR merged, a sheet's link, what the live system answers)"
        },
        "expect_minutes": {
          "type": "integer",
          "description": "how long it should take from now; longer without progress counts as stuck"
        }
      }
    }
  }
}
```
