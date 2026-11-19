{{if eq .Language "English"}}# Project {{.Key}}: {{.Name}}
Aliases: {{with .Aliases}}{{join . "、"}}{{else}}None{{end}}
One-line summary: {{with .OneLine}}{{.}}{{else}}None{{end}}
Goal: {{with .Goal}}{{.}}{{else}}None{{end}}
From: {{with .Chats}}{{join . "、"}}{{else}}None{{end}} ({{.People}} people)

{{if blank .Summary}}(no record yet){{else}}{{.Summary}}{{end}}{{else if eq .Language "日本語"}}# プロジェクト {{.Key}}：{{.Name}}
別名：{{with .Aliases}}{{join . "、"}}{{else}}なし{{end}}
一文の要約：{{with .OneLine}}{{.}}{{else}}なし{{end}}
目標：{{with .Goal}}{{.}}{{else}}なし{{end}}
出典：{{with .Chats}}{{join . "、"}}{{else}}なし{{end}}（{{.People}}人）

{{if blank .Summary}}（記録はまだありません）{{else}}{{.Summary}}{{end}}{{else}}# 项目 {{.Key}}：{{.Name}}
别称：{{with .Aliases}}{{join . "、"}}{{else}}无{{end}}
一句话：{{with .OneLine}}{{.}}{{else}}无{{end}}
目标：{{with .Goal}}{{.}}{{else}}无{{end}}
来自：{{with .Chats}}{{join . "、"}}{{else}}无{{end}}（{{.People}} 个人）

{{if blank .Summary}}（还没有档案）{{else}}{{.Summary}}{{end}}{{end}}
