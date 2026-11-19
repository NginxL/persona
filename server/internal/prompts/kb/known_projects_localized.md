{{if eq .Language "English"}}{{range .Projects}}
- {{.Name}}{{with .Aliases}}（{{join . "、"}}）{{end}}{{with .OneLine}}: {{.}}{{end}}{{with .Pinned}}; Confirmed facts (read-only, authoritative): {{.}}{{end}}{{else}} (none yet){{end}}{{else if eq .Language "日本語"}}{{range .Projects}}
- {{.Name}}{{with .Aliases}}（{{join . "、"}}）{{end}}{{with .OneLine}}：{{.}}{{end}}{{with .Pinned}}；確認済みの事実（読み取り専用、最優先）：{{.}}{{end}}{{else}}（まだありません）{{end}}{{else}}{{range .Projects}}
- {{.Name}}{{with .Aliases}}（{{join . "、"}}）{{end}}{{with .OneLine}}：{{.}}{{end}}{{with .Pinned}}；已确认事实（只读，以此为准）：{{.}}{{end}}{{else}}（还没有）{{end}}{{end}}
