{{if eq .Language "日本語"}}{{.Owner}}の発言：{{.Text}}

{{.Owner}}の指示に沿って対応を考え直し、inbox_suggest で新しい提案を提出してください。{{else if .Language}}{{.Owner}} says: {{.Text}}

Reconsider the matter as {{.Owner}} directed and call inbox_suggest with a new suggestion.{{else}}{{.Owner}}说：{{.Text}}

照{{.Owner}}说的重新处理，调 inbox_suggest 交一条新的建议。{{end}}
