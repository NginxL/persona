{{if eq .Language "English"}}{{if .Project}}Project: {{.Name}}{{with .Goal}}
Goal: {{.}}{{end}}

# Timeline (lines {{.From}}–{{.To}} of {{.Total}})
{{join .Lines "\n"}}
{{else}}Chat: {{.Name}}

# Compressed context
{{.Summary}}
{{end}}
# Existing topics
{{range .Topics}}- {{.Name}}{{with .Aliases}}（{{join . "、"}}）{{end}}{{with .Note}}: {{.}}{{end}}
{{else}}(none yet)
{{end}}{{else if eq .Language "日本語"}}{{if .Project}}プロジェクト：{{.Name}}{{with .Goal}}
目標：{{.}}{{end}}

# タイムライン（全 {{.Total}} 行中 {{.From}}–{{.To}} 行）
{{join .Lines "\n"}}
{{else}}会話：{{.Name}}

# 圧縮コンテキスト
{{.Summary}}
{{end}}
# 既存のトピック
{{range .Topics}}- {{.Name}}{{with .Aliases}}（{{join . "、"}}）{{end}}{{with .Note}}：{{.}}{{end}}
{{else}}（まだありません）
{{end}}{{else}}{{if .Project}}项目：{{.Name}}{{with .Goal}}
目标：{{.}}{{end}}

# 时间线（第 {{.From}}–{{.To}} 条，共 {{.Total}} 条）
{{join .Lines "\n"}}
{{else}}会话：{{.Name}}

# 压缩上下文
{{.Summary}}
{{end}}
# 已有主题
{{range .Topics}}- {{.Name}}{{with .Aliases}}（{{join . "、"}}）{{end}}{{with .Note}}：{{.}}{{end}}
{{else}}（还没有）
{{end}}{{end}}
