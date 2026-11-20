{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}現在：{{.Now}}
Agent：{{.Agent}}（{{.Machine}}{{with .Cwd}}、ディレクトリ {{.}}{{end}}）{{with .Project}}
プロジェクト：{{.}}{{end}}{{with .Task}}
取り組んでいるタスク：{{.}}{{end}}{{with .Decisions}}

このタスクについてユーザーが決めたこと：
{{.}}{{end}}{{with .Asks}}

ユーザーからの最近の依頼（古い順）：
{{range .}}- {{.}}
{{end}}{{end}}{{with .Reply}}
最近の返答：
{{.}}
{{end}}
質問：
{{range .Questions}}- {{.Question}}{{if .MultiSelect}}（複数選択可）{{end}}
{{range .Options}}  - {{.Label}}{{with .Description}}：{{.}}{{end}}
{{end}}{{end}}{{else}}Now: {{.Now}}
Agent: {{.Agent}} ({{.Machine}}{{with .Cwd}}, directory {{.}}{{end}}){{with .Project}}
Project: {{.}}{{end}}{{with .Task}}
Its task: {{.}}{{end}}{{with .Decisions}}

The user's decisions on this task:
{{.}}{{end}}{{with .Asks}}

The user's recent requests, oldest first:
{{range .}}- {{.}}
{{end}}{{end}}{{with .Reply}}
Its recent reply:
{{.}}
{{end}}
Its questions:
{{range .Questions}}- {{.Question}}{{if .MultiSelect}} (multiple selections allowed){{end}}
{{range .Options}}  - {{.Label}}{{with .Description}}: {{.}}{{end}}
{{end}}{{end}}{{end}}{{else}}现在：{{.Now}}
Agent：{{.Agent}}（{{.Machine}}{{with .Cwd}}，目录 {{.}}{{end}}）{{with .Project}}
项目：{{.}}{{end}}{{with .Task}}
它在做的任务：{{.}}{{end}}{{with .Decisions}}

这件事上用户拍过的板：
{{.}}{{end}}{{with .Asks}}

用户最近给它的要求（从早到晚）：
{{range .}}- {{.}}
{{end}}{{end}}{{with .Reply}}
它最近说的：
{{.}}
{{end}}
它在问：
{{range .Questions}}- {{.Question}}{{if .MultiSelect}}（可多选）{{end}}
{{range .Options}}  - {{.Label}}{{with .Description}}：{{.}}{{end}}
{{end}}{{end}}{{end}}
