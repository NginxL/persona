{{if eq .Lang "en"}}Record the exit code and result of this session’s granted lease using its fencing token.{{else if eq .Lang "ja"}}フェンシングトークンで所有リースの終了コードと結果を記録します。{{else}}使用 fencing token 记录本会话持有租约的退出码和结果。{{end}}

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
    },
    "outcome": {
      "type": "object",
      "properties": {
        "exit": {
          "type": "integer"
        },
        "run": {
          "type": "string"
        },
        "output": {
          "type": "string"
        }
      },
      "required": [
        "exit",
        "run"
      ]
    }
  },
  "required": [
    "resource",
    "lease_id",
    "token",
    "outcome"
  ]
}
```
