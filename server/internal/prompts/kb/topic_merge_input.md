{{if eq .Language "English"}}Topic: {{.Name}}{{with .Aliases}}（{{join . "、"}}）{{end}}
{{with .Note}}Description: {{.}}
{{end}}{{with .Pinned}}
# Confirmed facts (read-only, authoritative)
{{.}}
{{end}}
# Existing knowledge points ({{len .Points}})
{{range .Points}}- {{.ID}} [{{join .Path " / "}}] {{.Kind}} · {{.Text}}{{with .Why}}（{{.}}）{{end}}{{with .When}} · {{.}}{{end}}
{{else}}(none yet)
{{end}}
# Candidates ({{len .Candidates}})
{{range .Candidates}}- {{.ID}} [{{join .Path " / "}}] {{.Kind}} · {{.Text}}{{with .Why}}（{{.}}）{{end}}{{with .When}} · {{.}}{{end}}
{{end}}{{else if eq .Language "日本語"}}トピック：{{.Name}}{{with .Aliases}}（{{join . "、"}}）{{end}}
{{with .Note}}説明：{{.}}
{{end}}{{with .Pinned}}
# 確認済みの事実（読み取り専用、最優先）
{{.}}
{{end}}
# 既存の知識（{{len .Points}} 件）
{{range .Points}}- {{.ID}} [{{join .Path " / "}}] {{.Kind}} · {{.Text}}{{with .Why}}（{{.}}）{{end}}{{with .When}} · {{.}}{{end}}
{{else}}（まだありません）
{{end}}
# 候補（{{len .Candidates}} 件）
{{range .Candidates}}- {{.ID}} [{{join .Path " / "}}] {{.Kind}} · {{.Text}}{{with .Why}}（{{.}}）{{end}}{{with .When}} · {{.}}{{end}}
{{end}}{{else}}主题：{{.Name}}{{with .Aliases}}（{{join . "、"}}）{{end}}
{{with .Note}}说明：{{.}}
{{end}}{{with .Pinned}}
# 已确认的事实（只读，以此为准）
{{.}}
{{end}}
# 已有知识点（{{len .Points}} 条）
{{range .Points}}- {{.ID}} [{{join .Path " / "}}] {{.Kind}} · {{.Text}}{{with .Why}}（{{.}}）{{end}}{{with .When}} · {{.}}{{end}}
{{else}}（还没有）
{{end}}
# 候选（{{len .Candidates}} 条）
{{range .Candidates}}- {{.ID}} [{{join .Path " / "}}] {{.Kind}} · {{.Text}}{{with .Why}}（{{.}}）{{end}}{{with .When}} · {{.}}{{end}}
{{end}}{{end}}
