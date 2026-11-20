{{if eq .Language "日本語"}}作業内容：
{{.Prompt}}{{else if .Language}}Task to do:
{{.Prompt}}{{else}}要做的事：
{{.Prompt}}{{end}}
