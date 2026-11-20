{{if eq .Language "English"}}# Project: {{.Name}}
Aliases: {{with .Aliases}}{{join . "、"}}{{else}}None{{end}}
Keywords: {{join .Keywords "、"}}
One-line summary: {{with .OneLine}}{{.}}{{else}}None{{end}}
Goal: {{with .Goal}}{{.}}{{else}}None{{end}}

{{if blank .Summary}}(no record yet){{else}}{{.Summary}}{{end}}

# Posts
{{range .Posts}}
## {{.ID}}
{{.At}} · {{.Author}} · Comments {{.Comments}}
{{.Text}}
{{end}}{{else if eq .Language "日本語"}}# プロジェクト：{{.Name}}
別名：{{with .Aliases}}{{join . "、"}}{{else}}なし{{end}}
キーワード：{{join .Keywords "、"}}
一文の要約：{{with .OneLine}}{{.}}{{else}}なし{{end}}
目標：{{with .Goal}}{{.}}{{else}}なし{{end}}

{{if blank .Summary}}（記録はまだありません）{{else}}{{.Summary}}{{end}}

# 投稿
{{range .Posts}}
## {{.ID}}
{{.At}} · {{.Author}} · コメント {{.Comments}}
{{.Text}}
{{end}}{{else}}# 项目：{{.Name}}
别称：{{with .Aliases}}{{join . "、"}}{{else}}无{{end}}
关键词：{{join .Keywords "、"}}
一句话：{{with .OneLine}}{{.}}{{else}}无{{end}}
目标：{{with .Goal}}{{.}}{{else}}无{{end}}

{{if blank .Summary}}（还没有档案）{{else}}{{.Summary}}{{end}}

# 帖子
{{range .Posts}}
## {{.ID}}
{{.At}} · {{.Author}} · 评论 {{.Comments}}
{{.Text}}
{{end}}{{end}}
