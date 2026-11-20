{{if eq .Language "日本語"}}{{template "inbox/item" .Item}}{{with .Instruction}}

ユーザーが希望する返信：{{.}}{{end}}{{else if .Language}}{{template "inbox/item" .Item}}{{with .Instruction}}

The user wants to reply this way: {{.}}{{end}}{{else}}{{template "inbox/item" .Item}}{{with .Instruction}}

用户希望这样回：{{.}}{{end}}{{end}}
