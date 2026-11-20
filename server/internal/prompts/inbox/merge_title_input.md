{{if eq .Language "日本語"}}統合した案件（新しい順）：
{{range .Items}}
種類：{{.Kind}}{{with .Source}}（{{.}}）{{end}}
タイトル：{{.Title}}{{with .Sender}}
送信者：{{.}}{{end}}{{with .Summary}}
要約：{{.}}{{end}}{{with .Preview}}
内容：{{.}}{{end}}
{{end}}{{else if .Language}}Merged items (newest first):
{{range .Items}}
Type: {{.Kind}}{{with .Source}} ({{.}}){{end}}
Title: {{.Title}}{{with .Sender}}
From: {{.}}{{end}}{{with .Summary}}
Summary: {{.}}{{end}}{{with .Preview}}
Content: {{.}}{{end}}
{{end}}{{else}}合并在一起的几件事（最新的在前）：
{{range .Items}}
类型：{{.Kind}}{{with .Source}}（{{.}}）{{end}}
标题：{{.Title}}{{with .Sender}}
来自：{{.}}{{end}}{{with .Summary}}
摘要：{{.}}{{end}}{{with .Preview}}
内容：{{.}}{{end}}
{{end}}{{end}}
