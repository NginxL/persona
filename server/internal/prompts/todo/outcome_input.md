{{if eq .Language "日本語"}}タスク：{{.Title}}
{{with .Prompt}}
依頼時の要件：
{{.}}
{{end}}{{range .Replies}}
Agent（{{.Machine}}{{with .Cwd}} · {{.}}{{end}}）の最終返信：
{{.Text}}
{{end}}{{else if .Language}}Task: {{.Title}}
{{with .Prompt}}
Original requirements:
{{.}}
{{end}}{{range .Replies}}
Agent ({{.Machine}}{{with .Cwd}} · {{.}}{{end}})'s final reply:
{{.Text}}
{{end}}{{else}}任务：{{.Title}}
{{with .Prompt}}
派活时的要求：
{{.}}
{{end}}{{range .Replies}}
Agent（{{.Machine}}{{with .Cwd}} · {{.}}{{end}}）最后的回复：
{{.Text}}
{{end}}{{end}}
