{{if eq .Language "English"}}Project: {{.Name}} ({{.Kind}})
Aliases:{{with .Aliases}} {{join . "、"}}{{end}}
Existing tags:{{with .Tags}} {{join . "、"}}{{end}}

# Existing record
{{if blank .Summary}}(none yet){{else}}{{.Summary}}{{end}}
{{with .Pinned}}
# Confirmed facts (read-only, authoritative, do not rewrite)
{{.}}
{{end}}
# Recent timeline
{{join .Log "\n"}}

# Related chats
{{range .Chats}}
## {{.ID}}
{{if .State}}State (recorded by the source): {{if eq .State "merged"}}merged{{else if eq .State "closed"}}closed{{else}}open{{end}}{{with .StateAt}} ({{.}}){{end}}
{{end}}{{.Summary}}
{{end}}{{with .Moments}}
# Related company posts
{{range .}}
- {{.At}} · {{.Author}} · Comments {{.Comments}}{{with .Note}} · {{.}}{{end}}
  {{.Text}}
{{end}}{{end}}{{else if eq .Language "日本語"}}プロジェクト：{{.Name}}（{{.Kind}}）
別名：{{join .Aliases "、"}}
既存のタグ：{{join .Tags "、"}}

# 既存の記録
{{if blank .Summary}}（まだありません）{{else}}{{.Summary}}{{end}}
{{with .Pinned}}
# 確認済みの事実（読み取り専用。最優先で、書き換え禁止）
{{.}}
{{end}}
# 最近のタイムライン
{{join .Log "\n"}}

# 関連する会話
{{range .Chats}}
## {{.ID}}
{{if .State}}状態（出典が記録した事実）：{{if eq .State "merged"}}マージ済み{{else if eq .State "closed"}}クローズ済み{{else}}進行中{{end}}{{with .StateAt}}（{{.}}）{{end}}
{{end}}{{.Summary}}
{{end}}{{with .Moments}}
# 関連する社内投稿
{{range .}}
- {{.At}} · {{.Author}} · コメント {{.Comments}}{{with .Note}} · {{.}}{{end}}
  {{.Text}}
{{end}}{{end}}{{else}}项目：{{.Name}}（{{.Kind}}）
别称：{{join .Aliases "、"}}
已有标签：{{join .Tags "、"}}

# 已有档案
{{if blank .Summary}}（还没有）{{else}}{{.Summary}}{{end}}
{{with .Pinned}}
# 已确认的事实（只读，以此为准，不得改写）
{{.}}
{{end}}
# 最近的时间线
{{join .Log "\n"}}

# 相关会话
{{range .Chats}}
## {{.ID}}
{{if .State}}状态（来源记录的事实）：{{if eq .State "merged"}}已合并{{else if eq .State "closed"}}已关闭{{else}}进行中{{end}}{{with .StateAt}}（{{.}}）{{end}}
{{end}}{{.Summary}}
{{end}}{{with .Moments}}
# 相关公司圈
{{range .}}
- {{.At}} · {{.Author}} · 评论 {{.Comments}}{{with .Note}} · {{.}}{{end}}
  {{.Text}}
{{end}}{{end}}{{end}}
