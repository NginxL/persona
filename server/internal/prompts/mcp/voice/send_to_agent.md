{{if eq .Lang "en"}}Send a message to an Agent, as if the user typed it into its input and pressed Enter. Use only when the user explicitly asks. If the recipient and content are clear, send directly without repeating them or waiting for confirmation; ask first if you are unsure which Agent. machine_id and session_id come from list_agents.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "machine_id",
    "session_id",
    "text"
  ],
  "properties": {
    "machine_id": {
      "type": "string"
    },
    "session_id": {
      "type": "string"
    },
    "text": {
      "type": "string",
      "description": "The message to send"
    }
  }
}
```{{else if eq .Lang "ja"}}ユーザーが入力欄に入力して Enter を押すのと同様に、Agent にメッセージを送信します。ユーザーが明示的に求めた場合にのみ使ってください。宛先と内容が明確なら、復唱も確認待ちもせず直接送信します。どの Agent か不明な場合は先に質問してください。machine_id と session_id は list_agents から取得します。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "machine_id",
    "session_id",
    "text"
  ],
  "properties": {
    "machine_id": {
      "type": "string"
    },
    "session_id": {
      "type": "string"
    },
    "text": {
      "type": "string",
      "description": "送信する内容"
    }
  }
}
```{{else}}给某个 Agent 发一条消息（相当于用户在它的输入框里打字并回车）。只在用户明确要求时用；说清楚了发给谁、发什么就直接发，不复述、不等确认，拿不准发给哪个 Agent 就先问一句。machine_id 和 session_id 来自 list_agents。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["machine_id", "session_id", "text"],
  "properties": {
    "machine_id": {"type": "string"},
    "session_id": {"type": "string"},
    "text": {"type": "string", "description": "要发的内容"}
  }
}
```{{end}}
