{{if eq .Language "日本語"}}{{with .Rules}}

ユーザー自身のルール（上の既定の判断より優先）：
{{.}}{{end}}{{else if .Language}}{{with .Rules}}

The user's own rules (take precedence over the defaults above):
{{.}}{{end}}{{else}}{{with .Rules}}

用户自己定的规则（优先于上面的默认判断）：
{{.}}{{end}}{{end}}
