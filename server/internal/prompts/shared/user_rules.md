{{with .Rules}}

{{if eq $.Language "日本語"}}ユーザー自身のルール（上の既定の判断より優先）：{{else if $.Language}}The user's own rules (take precedence over the defaults above):{{else}}用户自己定的规则（优先于上面的默认判断）：{{end}}
{{.}}{{end}}
