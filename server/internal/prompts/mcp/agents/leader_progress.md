Report your direction's progress to the user's work log: leader conversations only (assistant.leaders). Write at the goal level, not the process: what was reached, what is being done now and where it is stuck, what the user has to do, with links to the evidence (merged PR, deployed and checked page, document). Send one when a task of yours was reached and at each report cycle (your scheduled check-in, or the end of a day's work on the direction); not after every step, and never only to say you are still working. It does not message the user: it becomes a progress line of your direction in the work-log extension's card, and the latest one is kept on the direction. What needs the user's decision now still goes by task_review needs_user.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {
    "reached": {
      "type": "string",
      "description": "{{if eq .Lang "en"}}what was reached since the last report, one sentence (English unless the user writes otherwise); empty when nothing was{{else if eq .Lang "ja"}}前回の報告から達成したことを一文で記述します。ユーザーが別の言語を使わない限り日本語で書き、進展がない場合は空にします{{else}}what was reached since the last report, one sentence (Chinese unless the user writes otherwise); empty when nothing was{{end}}"
    },
    "doing": {
      "type": "string",
      "description": "what is being done now and, if stuck, where and on what; one sentence"
    },
    "needs_you": {
      "type": "string",
      "description": "what the user has to do (decide, verify, give access), with where; empty when nothing"
    },
    "links": {
      "type": "array",
      "maxItems": 5,
      "items": {"type": "string"},
      "description": "evidence links: PRs, deployed pages, documents; the first is shown first"
    }
  }
}
```
