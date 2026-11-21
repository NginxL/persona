{{if eq .Lang "en"}}Hand work to the conversation bound to this call (identified in the instructions). Calling sends it immediately, without repeating it or waiting for confirmation, so ask first if you are unsure the user wants it done. The call does not return immediately: tell the user you handed it over, then keep talking normally. When that turn finishes, this call returns its answer, possibly minutes later; tell the user then. Put the work in task's first sentence as a complete instruction: the conversation cannot see this voice context. To ask what it is doing or how far it has got, start task with the exact prefix 状态： (not sent to it; returns its current status). When the user explicitly asks it to stop, use the exact prefix 停止：. When the user is answering its question, use the exact prefix 回答：.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "task"
  ],
  "properties": {
    "task": {
      "type": "string",
      "description": "The work in complete sentences, or a sentence starting with the exact prefix 状态：, 停止： or 回答："
    }
  }
}
```{{else if eq .Lang "ja"}}この通話に紐づく会話に作業を依頼します。対象は instructions の紐づく会話の説明を参照してください。呼び出すと、復唱も確認待ちもせず直ちに送信されます。ユーザーが依頼を意図しているか不明な場合は、先に質問してください。呼び出しはすぐには戻りません。依頼したことをユーザーに伝え、通常どおり会話を続けてください。そのターンが終わると、数分後になる場合もありますが、回答が返ります。その時点でユーザーに伝えてください。task の最初の文に作業内容を完全な指示として書いてください。対象の会話はこの音声会話の文脈を見られません。現在の作業や進捗を尋ねる場合は、task を正確な接頭辞「状态：」で始めてください。これは対象へ送られず、現在の状態が返ります。ユーザーが明示的に停止を求めた場合は「停止：」、対象からの質問に答えている場合は「回答：」で始めてください。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "task"
  ],
  "properties": {
    "task": {
      "type": "string",
      "description": "依頼する作業を完全な文で記述します。または、正確な接頭辞「状态：」「停止：」「回答：」で始める文にします"
    }
  }
}
```{{else}}把一件事交给这次通话绑定的对话去做（是谁见 instructions 里「绑定的对话」）：调用时立刻发出去，不复述、不等确认，所以拿不准用户是不是要它做就先问一句。调用不会马上返回：告诉用户一声交给它了，然后照常聊；它这一轮做完，这次调用的结果就是它的回答（可能要几分钟），那时再告诉用户。task 第一句就是要做的事，写成一句完整的指令（它看不到这次语音的上下文）。问它在干什么、到哪了，task 用「状态：」开头（不会发给它，结果是它现在的状态）；用户明确要它停下，用「停止：」开头；用户在回答它问的问题，用「回答：」开头。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["task"],
  "properties": {
    "task": {"type": "string", "description": "要它做的事，完整的一句或几句话；或者「状态：」「停止：」「回答：」开头的一句"}
  }
}
```{{end}}
