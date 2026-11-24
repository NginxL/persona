{{if eq .Lang "en"}}Renew this session’s queued or granted lease. Use token 0 while queued and the fencing token once granted. Stop the runner client when fenced.{{else if eq .Lang "ja"}}このセッションの待機中または所有中のリースを更新します。待機中はトークン 0、許可後はフェンシングトークンを使い、fenced の場合はクライアントを停止します。{{else}}续期本会话排队中或持有的租约。排队时使用 token 0，授予后使用 fencing token；收到 fenced 时停止门禁客户端。{{end}}

```json
{
  "type": "object",
  "properties": {
    "resource": {
      "type": "string"
    },
    "lease_id": {
      "type": "string"
    },
    "token": {
      "type": "integer"
    }
  },
  "required": [
    "resource",
    "lease_id",
    "token"
  ]
}
```
