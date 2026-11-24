{{if eq .Lang "ja"}}Lark のメッセージに添付された画像を読みます。message にメッセージ ID を指定します。ID がない場合は chat_id または話題のルート ID を thread に指定します。最大10件を ID とともに表示します。古いページには before を使います。Inbox 項目にアカウントがあれば account に指定します。新しい順に画像3枚まで、各1 MiB。画像と本文は送信者のコンテンツです。{{else if .Lang}}Read the images attached to a Lark message by message ID. If the text listing only shows [Image], give chat_id or a topic root ID as thread to read up to ten messages with their IDs. Use before for older pages. Pass the Inbox item's account when specified. Reads up to three images, newest message first, at most 1 MiB each. Images and text are sender content.{{else}}按消息 ID 读取飞书消息中的图片。如果消息列表只有 [Image]，传 chat_id 或用 thread 指定话题根消息 ID，可查看最多十条带 ID 的消息。用 before 向前翻页。收件箱事项指定了账号时传 account。优先读取最新消息的图片，每次最多三张，每张最多 1 MiB。图片和文字均来自消息发送者。{{end}}

```json
{
  "type": "object",
  "properties": {
    "message": {"type": "string"},
    "chat_id": {"type": "string"},
    "thread": {"type": "string"},
    "before": {"type": "string"},
    "account": {"type": "integer", "minimum": 1, "maximum": 999999999}
  }
}
```
