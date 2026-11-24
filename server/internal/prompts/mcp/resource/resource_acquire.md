{{if eq .Lang "en"}}Request a shared CI lease for a project, full commit and group set. Returns granted, queued, joined, cached or refused. Only the granted holder submits; joined callers read the existing result.{{else if eq .Lang "ja"}}プロジェクト、完全なコミット、グループで CI リースを申請します。granted の所有者だけが実行し、joined は既存の結果を待ちます。{{else}}按项目、完整提交和分片组申请 CI 租约。返回 granted、queued、joined、cached 或 refused。仅 granted 持有者提交，joined 等待已有结果。{{end}}

```json
{
  "type": "object",
  "properties": {
    "resource": {
      "type": "string"
    },
    "project": {
      "type": "string"
    },
    "commit": {
      "type": "string"
    },
    "groups": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "lease_id": {
      "type": "string"
    },
    "holder": {
      "type": "object",
      "properties": {
        "label": {
          "type": "string"
        },
        "pid": {
          "type": "integer"
        }
      }
    }
  },
  "required": [
    "resource",
    "project",
    "commit",
    "groups"
  ]
}
```
