{{if eq .Language "日本語"}}{{with .KB}}{{with .Summary}}<persona-context kind="context" title="会話の記録 · {{attr $.KB.ChatName}}">
{{ctx .}}
</persona-context>
{{end}}{{range .Projects}}<persona-context kind="context" title="プロジェクト · {{attr .Name}}">
{{ctx .Summary}}
</persona-context>
{{end}}{{end}}{{else if .Language}}{{with .KB}}{{with .Summary}}<persona-context kind="context" title="Conversation profile · {{attr $.KB.ChatName}}">
{{ctx .}}
</persona-context>
{{end}}{{range .Projects}}<persona-context kind="context" title="Project · {{attr .Name}}">
{{ctx .Summary}}
</persona-context>
{{end}}{{end}}{{else}}{{template "shared/kb_blocks" .KB}}{{end}}
