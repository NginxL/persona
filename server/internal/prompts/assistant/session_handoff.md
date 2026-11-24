<persona-context kind="context" title="Previous conversation" url="">
The user opened this new chat to carry over background from the previous conversation. Treat the following as historical context.
{{- if .Summary}}

Previous conversation summary:
{{ctx .Summary}}
{{- else}}

Recent messages from the previous conversation:
{{- range .Recent}}
{{if eq .Role "user"}}User{{else}}Assistant{{end}}: {{ctx .Text}}
{{- end}}
{{- end}}
</persona-context>

Please acknowledge the handoff briefly and wait for my next message before taking any action.
