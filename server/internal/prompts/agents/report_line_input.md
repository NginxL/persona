{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}タスク：{{.Task}}
報告の種類：{{.Kind}}
報告の原文：
{{.Text}}{{else}}Task: {{.Task}}
Report kind: {{.Kind}}
Original report:
{{.Text}}{{end}}{{else}}任务：{{.Task}}
汇报类型：{{.Kind}}
汇报原文：
{{.Text}}{{end}}
