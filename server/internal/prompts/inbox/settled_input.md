{{if eq .Language "日本語"}}案件：{{.Title}}{{with .Summary}}
要約：{{.}}{{end}}

案件のメッセージ：
{{.Context}}

その後のメッセージ（古い順）：
{{range .Later}}{{with .At}}[{{.}}] {{end}}{{if .Mine}}自分{{else}}{{.Sender}}{{end}}：{{.Text}}{{if .ReplyTo}} (返信 {{.ReplyTo}}){{end}}{{if .Thread}} (スレッド {{.Thread}}){{end}}
{{end}}{{else if .Language}}Event: {{.Title}}{{with .Summary}}
Summary: {{.}}{{end}}

Its messages:
{{.Context}}

Later messages (oldest first):
{{range .Later}}{{with .At}}[{{.}}] {{end}}{{if .Mine}}Me{{else}}{{.Sender}}{{end}}: {{.Text}}{{if .ReplyTo}} (reply {{.ReplyTo}}){{end}}{{if .Thread}} (thread {{.Thread}}){{end}}
{{end}}{{else}}这件事：{{.Title}}{{with .Summary}}
总结：{{.}}{{end}}

它的消息：
{{.Context}}

之后的消息（旧的在前）：
{{range .Later}}{{with .At}}[{{.}}] {{end}}{{if .Mine}}我{{else}}{{.Sender}}{{end}}：{{.Text}}{{if .ReplyTo}} (回复 {{.ReplyTo}}){{end}}{{if .Thread}} (话题 {{.Thread}}){{end}}
{{end}}{{end}}
