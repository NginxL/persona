{{if eq .Lang "en"}}Read a lease owned by or joined by this session; never submit again while waiting.{{else if eq .Lang "ja"}}このセッションが所有または参加したリースを読み取ります。待機中に再送信しないでください。{{else}}读取本会话持有或加入的租约；等待时不要重新提交。{{end}}

```json
{
  "type": "object",
  "properties": {
    "resource": {
      "type": "string"
    },
    "lease_id": {
      "type": "string"
    }
  },
  "required": [
    "resource",
    "lease_id"
  ]
}
```
