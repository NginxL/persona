{{if eq .Lang "en"}}Main or a direction’s lead can adopt an Agent on no open task for supervision and task_review. Omit role for a worker. A leader needs done_when and a non-work-trust computer. It gets instructions to plan and review direct workers, who cannot delegate. On your open task, use task_scope for done_when/expect_minutes. Use agent_transfer to move another conversation's task.{{else if eq .Lang "ja"}}メイン会話または担当分野のリーダーは、進行中のタスクがない Agent を監督できます。role を省略すると worker になります。leader には done_when が必要で、仕事用の信頼設定のコンピュータでは指定できません。leader は task_plan と task_review で直属の worker に作業を割り当てて確認できます。worker は再委任できません。自分のタスクの完了条件や見積もりの変更には task_scope、別の会話への移管には agent_transfer を使います。{{else}}主对话或方向主管可收编没有进行中任务的 Agent。省略 role 即为 worker；leader 需要 done_when，不能使用工作信任电脑。它可用 task_plan 分派直属 worker，用 task_review 验收；worker 不能继续派活。已有任务用 task_scope 调整验收条件或预计用时，用 agent_transfer 转交其他对话的任务。{{end}}{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "machine",
    "session_id"
  ],
  "properties": {
    "machine": {
      "type": "string",
      "description": "agents_list machine id or name"
    },
    "session_id": {
      "type": "string",
      "description": "Agent session id or first 8 characters"
    },
    "title": {
      "type": "string",
      "description": "{{if eq .Lang "en"}}the task in a short line (English, under 20 characters); empty: the agent's title{{else if eq .Lang "ja"}}短いタスク名（日本語、20文字未満）。空の場合は Agent のタイトルを使います{{else}}Chinese task title, under 20 characters; empty: Agent title{{end}}"
    },
    "role": {
      "type": "string",
      "enum": ["leader"]
    },
    "done_when": {
      "type": "array",
      "maxItems": 6,
      "items": {
        "type": "string"
      },
      "description": "checkable completion criteria"
    },
    "expect_minutes": {
      "type": "integer",
      "description": "estimated minutes from now"
    }
  }
}
```
