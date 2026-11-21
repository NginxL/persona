{{if eq .Lang "en"}}Hand work to the Persona main conversation: the full assistant can use all the user's data and tools (calendar, mail, Lark, documents, computers, scheduled tasks, writing and more). It can do much more than you, but takes tens of seconds to several minutes. Use it when your own tools cannot answer or the user needs something done. The call does not return immediately: tell the user you have handed it to the main conversation, then keep talking normally. When that turn finishes, this call returns its answer, possibly minutes later; tell the user then. Make task clear and self-contained, with the work to do in its first sentence: the main conversation cannot see this voice conversation's context.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "task"
  ],
  "properties": {
    "task": {
      "type": "string",
      "description": "The work for the main conversation, in one or more complete sentences"
    }
  }
}
```{{else if eq .Lang "ja"}}Persona のメイン会話に作業を依頼します。メイン会話は、予定、メール、Lark、文書、各コンピュータ、定期タスク、文章作成など、ユーザーの全データとツールを使える完全なアシスタントです。あなたより多くのことができますが、数十秒から数分かかります。自分のツールでは答えられない場合や、ユーザーが実際の作業を求める場合に使ってください。呼び出しはすぐには戻りません。メイン会話に依頼したことをユーザーに伝え、通常どおり会話を続けてください。そのターンが終わると、数分後になる場合もありますが、この呼び出しに回答が返ります。その時点でユーザーに伝えてください。task は単独で意味が通る明確な指示にし、最初の文で作業内容を述べてください。メイン会話はこの音声会話の文脈を見られません。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "task"
  ],
  "properties": {
    "task": {
      "type": "string",
      "description": "メイン会話に依頼する作業。完全な文を一つ以上で記述します"
    }
  }
}
```{{else}}把一件事交给 Persona 主对话去做：它是能用用户全部数据和工具的完整助手（日程、邮件、飞书、文档、各台电脑、定时任务、写东西……），比你能做的多得多，但要几十秒到几分钟。你自己的工具答不了、或者用户要办一件需要动手的事时用它。它不会马上返回：调用时告诉用户一声已经交给主对话了，然后照常聊；主对话这一轮做完，这次调用的结果就是它的回答（可能要几分钟），那时再告诉用户。task 要写清楚、自成一体，第一句就是要做的事（主对话看不到这次语音的上下文）。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["task"],
  "properties": {
    "task": {"type": "string", "description": "要主对话做的事，完整的一句或几句话"}
  }
}
```{{end}}
