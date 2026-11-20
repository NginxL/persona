{{with .KB}}{{with .Summary}}<persona-context kind="context" title="{{if eq $.Language "日本語"}}会話の記録{{else if $.Language}}Conversation profile{{else}}会话档案{{end}} · {{attr $.KB.ChatName}}">
{{ctx .}}
</persona-context>
{{end}}{{range .Projects}}<persona-context kind="context" title="{{if eq $.Language "日本語"}}プロジェクト{{else if $.Language}}Project{{else}}项目{{end}} · {{attr .Name}}">
{{ctx .Summary}}
</persona-context>
{{end}}{{end}}
