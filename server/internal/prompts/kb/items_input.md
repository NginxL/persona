{{if eq .Language "English"}}Known projects:{{template "kb/known_projects_localized" .}}
Existing tags:{{with .Tags}} {{join . "、"}}{{end}}

{{range .Items}}{{.Key}} | {{if eq .Kind "docs"}}Document{{else if eq .Kind "meetings"}}Meeting{{else if eq .Kind "events"}}Calendar event{{end}} | {{.Title}} | {{if not .At.IsZero}}{{.At.Format "2006-01-02 15:04"}}{{end}} | {{.Where}}{{with .Pinned}} | Confirmed facts (read-only, authoritative): {{.}}{{end}}
{{end}}{{else if eq .Language "日本語"}}既知のプロジェクト：{{template "kb/known_projects_localized" .}}
既存のタグ：{{join .Tags "、"}}

{{range .Items}}{{.Key}} | {{if eq .Kind "docs"}}文書{{else if eq .Kind "meetings"}}会議{{else if eq .Kind "events"}}予定{{end}} | {{.Title}} | {{if not .At.IsZero}}{{.At.Format "2006-01-02 15:04"}}{{end}} | {{.Where}}{{with .Pinned}} | 確認済みの事実（読み取り専用、最優先）：{{.}}{{end}}
{{end}}{{else}}已知项目：{{template "kb/known_projects_localized" .}}
已有标签：{{join .Tags "、"}}

{{range .Items}}{{.Key}} | {{if eq .Kind "docs"}}文档{{else if eq .Kind "meetings"}}会议{{else if eq .Kind "events"}}日程{{end}} | {{.Title}} | {{if not .At.IsZero}}{{.At.Format "2006-01-02 15:04"}}{{end}} | {{.Where}}{{with .Pinned}} | 已确认事实（只读，以此为准）：{{.}}{{end}}
{{end}}{{end}}
