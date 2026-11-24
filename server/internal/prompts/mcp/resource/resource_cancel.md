{{if eq .Lang "en"}}Cancel a queued lease owned by this session. Joined callers cannot cancel the holder’s work.{{else if eq .Lang "ja"}}このセッションの待機リースを取り消します。参加者は所有者の作業を取り消せません。{{else}}取消本会话持有的排队租约，加入者不能取消持有者的作业。{{end}}

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
