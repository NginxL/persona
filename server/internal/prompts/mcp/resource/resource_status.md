{{if eq .Lang "en"}}Read registered shared runners, queue tickets, duplicate markers and mirror freshness. An unknown or stale mirror is not a successful result.{{else if eq .Lang "ja"}}登録済みランナー、キュー、重複、同期時刻を読み取ります。不明または古い情報は成功を意味しません。{{else}}读取共享门禁、排队票号、重复标记和镜像时效。未知或过期不代表运行成功。{{end}}

```json
{
  "type": "object",
  "properties": {
    "resource": {
      "type": "string"
    }
  },
  "required": []
}
```
