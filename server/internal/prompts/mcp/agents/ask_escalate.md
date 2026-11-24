Hand the cards a task's agents wait on to the user, when a 督办 message gave them to you first and only the user can decide: a form or secret; an approval that sends or replies in their name, spends money or deletes; a question that is irreversible, seen by others, changes the goal or contradicts a decision made on the task. Other permissions and plans are yours (interaction_answer); no card reaches the user on a timeout. Every card of the task the user has not been told of goes to them at once, as one reminder (with push grading on it pushes only for why_owner or p0), with your reason and recommendation; their answers go to the agents and are kept as the task's decisions. Only the task's supervisor and main can; main is told of a leader's escalation, so don't ask the user again.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["todo_id", "reason"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's todo id (todo_…), from the 督办 message"
    },
    "reason": {
      "type": "string",
      "description": "in a sentence, in the user's language: what they are to decide and why it is theirs"
    },
    "recommend": {
      "type": "string",
      "description": "the option you recommend, in a line (an option's label when there is one)"
    },
    "why_owner": {
      "type": "string",
      "enum": ["irreversible", "visible_to_others", "priority", "acceptance", "only_owner_knows"],
      "description": "why only the user can decide it. In the user's push grading only irreversible, visible_to_others and only_owner_knows push to their phone; the rest wait in their Needs you list and briefing"
    },
    "p0": {
      "type": "boolean",
      "description": "true: the user must do or decide this themselves now, so it pushes even in push grading whatever why_owner says"
    }
  }
}
```
