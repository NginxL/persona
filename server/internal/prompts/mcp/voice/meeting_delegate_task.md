{{if eq .Lang "en"}}Hand the user's question to this meeting's conversation, which is bound to this call. Calling sends it immediately, without repeating it or waiting for confirmation. The call does not return immediately: tell the user you are asking. When the meeting conversation answers, this call returns its answer, possibly after a while; then convey it briefly in your own words without adding content of your own. task is the user's question or requested work in complete sentences: the meeting conversation cannot see this voice context. Preserve the user's meaning and add nothing else.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "task"
  ],
  "properties": {
    "task": {
      "type": "string",
      "description": "The question or work for the meeting conversation, in one or more complete sentences"
    }
  }
}
```{{else if eq .Lang "ja"}}ユーザーの質問を、この通話に紐づく会議の会話に渡します。呼び出すと、復唱も確認待ちもせず直ちに送信されます。呼び出しはすぐには戻りません。問い合わせていることをユーザーに伝えてください。会議の会話が答えると、しばらくかかる場合もありますが、この呼び出しに回答が返ります。その時点で、自分の内容を付け加えず、自分の言葉で簡潔に伝えてください。task はユーザーの質問または依頼を完全な文で記述します。会議の会話はこの音声会話の文脈を見られません。ユーザーの意図を保ち、別の内容を加えないでください。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "task"
  ],
  "properties": {
    "task": {
      "type": "string",
      "description": "会議の会話に尋ねる質問や依頼する作業。完全な文を一つ以上で記述します"
    }
  }
}
```{{else}}把用户的问题交给这场会议的会议对话（这次通话绑定的就是它）：调用时立刻发出去，不复述、不等确认。调用不会马上返回：告诉用户一声在问了；会议对话答完，这次调用的结果就是它的回答（可能要一会儿），那时用自己的话简短说出来，不加自己的内容。task 就是用户问的事，写成一个完整的问题或要办的事（会议对话看不到这次语音的上下文），照用户的意思写，不加别的。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["task"],
  "properties": {
    "task": {"type": "string", "description": "要问会议对话的问题或要它办的事，完整的一句或几句话"}
  }
}
```{{end}}
