项目：{{.Name}}（{{.Kind}}）
别称：{{join .Aliases "、"}}
已有标签：{{join .Tags "、"}}

# 已有档案
{{if blank .Summary}}（还没有）{{else}}{{.Summary}}{{end}}
{{with .Pinned}}
# 已确认的事实（只读，以此为准，不得改写）
{{.}}
{{end}}
# 最近的时间线
{{join .Log "\n"}}

# 相关会话
{{range .Chats}}
## {{.ID}}
{{if .State}}状态（来源记录的事实）：{{if eq .State "merged"}}已合并{{else if eq .State "closed"}}已关闭{{else}}进行中{{end}}{{with .StateAt}}（{{.}}）{{end}}
{{end}}{{.Summary}}
{{end}}{{with .Moments}}
# 相关公司圈
{{range .}}
- {{.At}} · {{.Author}} · 评论 {{.Comments}}{{with .Note}} · {{.}}{{end}}
  {{.Text}}
{{end}}{{end}}
