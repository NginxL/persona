这件事：{{.Title}}{{with .Summary}}
总结：{{.}}{{end}}

它的消息：
{{.Context}}

之后的消息（旧的在前）：
{{range .Later}}{{with .At}}[{{.}}] {{end}}{{if .Mine}}我{{else}}{{.Sender}}{{end}}：{{.Text}}{{if .ReplyTo}} (回复 {{.ReplyTo}}){{end}}{{if .Thread}} (话题 {{.Thread}}){{end}}
{{end}}
