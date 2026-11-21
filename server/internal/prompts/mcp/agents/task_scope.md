Change what a task you supervise is to do, or what done means for it, after it started (its todo id). The task's brief (the owner's request and the criteria it started with) is never rewritten: this adds an entry to the brief's log with what changed and why, applies the new done_when and expect_minutes, restarts overdue, estimate_due only on a new estimate, and the owner reads the change in their next digest. It is the only way done_when changes after a task started (agent_supervise and agent_send with a task on your own task go through the same log). Give done_when as the whole list after the change: a criterion you leave out is recorded as dropped by this change, and the completion report says so. A change that adds a step only the owner may allow (deploying, merging, releasing, sending or commenting where other people see it, deleting, spending, changing production) and was not part of the task is refused unless the owner's own message started this turn: ask them first (ask_user, or task_review needs_user), then call it again when they answered. Only the task's supervisor may call it. The agent is not told: send it what changed with agent_send. Use it also for estimate_due/overdue: explain and set expect_minutes.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["todo_id", "change", "why"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the task's todo id (todo_…)"
    },
    "change": {
      "type": "string",
      "description": "{{if eq .Lang "en"}}what changes, in one sentence the owner can read (English), e.g. require verification in the test environment only, instead of a staged production rollout{{else if eq .Lang "ja"}}変更内容をユーザーが読める日本語の一文で記述します。例：段階的な本番検証を求めず、テスト環境でのみ検証する{{else}}what changes, in one sentence the owner can read (Chinese), e.g. 「不再要求灰度验证，改为只在测试环境验证」{{end}}"
    },
    "why": {
      "type": "string",
      "description": "{{if eq .Lang "en"}}why, in one sentence (English){{else if eq .Lang "ja"}}理由を日本語の一文で記述します{{else}}why, in one sentence (Chinese){{end}}"
    },
    "done_when": {
      "type": "array",
      "minItems": 1,
      "maxItems": 6,
      "items": { "type": "string" },
      "description": "the criteria after the change, the whole list; leave it out to keep them as they are"
    },
    "expect_minutes": {
      "type": "integer",
      "minimum": 0,
      "description": "how long the task should take from now; leave it out (or 0) to keep it"
    }
  }
}
```
