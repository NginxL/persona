Take down a plan card this conversation put up (task_plan) while nothing of it has started: its work was done elsewhere, or the user decided not to do it. The card stops waiting for the user's 开始, as when they dismiss it themselves, and leaves the to-handle lists (the snapshot's plans waiting to start, the briefings). Only for a plan of this conversation that still waits for 开始; a started or dismissed plan, or another conversation's, is refused and nothing changes. Your reason is kept on record; tell the user in a line which card you took down and why.

```json
{
  "type": "object",
  "required": ["plan_id", "reason"],
  "properties": {
    "plan_id": {
      "type": "string",
      "description": "the plan's id (plan_…), as task_plan's answer or the snapshot's plans waiting to start give it"
    },
    "reason": {
      "type": "string",
      "description": "why it is no longer needed, in the user's language, one sentence"
    }
  }
}
```
