会话：{{.Thread}}{{with .Kind}}（{{.}}）{{end}}
要归类的新消息：{{join .New "、"}}

{{.Context}}

这个会话里现在的事（events 不带 query 的结果）：
{{.Events}}{{range .Calls}}

你调了 {{.Tool}} {{.Args}}，结果：
{{.Result}}{{end}}{{with .Problem}}

你上一次给的结果不合格：{{.}}。改好后重新给出完整结果（assign 和 events 都要）。{{end}}

{{if .Left}}还能查 {{.Left}} 次。{{else}}不能再查了，现在给出结果。{{end}}
