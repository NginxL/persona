{{if eq .Language "日本語"}}会話：{{.Thread}}{{with .Kind}}（{{.}}）{{end}}
分類する新着メッセージ：{{join .New "、"}}

{{.Context}}

この会話の現在の案件（query なしの events の結果）：
{{.Events}}{{range .Calls}}

{{.Tool}} {{.Args}} を呼び出した結果：
{{.Result}}{{end}}{{with .Problem}}

前の結果は無効です：{{.}}。修正して完全な結果（assign と events の両方）を返してください。{{end}}

{{if .Left}}あと {{.Left}} 回調べられます。{{else}}もう調べられません。今、結果を返してください。{{end}}{{else if .Language}}Conversation: {{.Thread}}{{with .Kind}} ({{.}}){{end}}
New messages to assign: {{join .New ", "}}

{{.Context}}

Current events in this conversation (events without query):
{{.Events}}{{range .Calls}}

You called {{.Tool}} {{.Args}}; result:
{{.Result}}{{end}}{{with .Problem}}

Your last result was invalid: {{.}}. Correct it and return the full result (both assign and events).{{end}}

{{if .Left}}{{.Left}} lookups remain.{{else}}No lookups remain; return the result now.{{end}}{{else}}会话：{{.Thread}}{{with .Kind}}（{{.}}）{{end}}
要归类的新消息：{{join .New "、"}}

{{.Context}}

这个会话里现在的事（events 不带 query 的结果）：
{{.Events}}{{range .Calls}}

你调了 {{.Tool}} {{.Args}}，结果：
{{.Result}}{{end}}{{with .Problem}}

你上一次给的结果不合格：{{.}}。改好后重新给出完整结果（assign 和 events 都要）。{{end}}

{{if .Left}}还能查 {{.Left}} 次。{{else}}不能再查了，现在给出结果。{{end}}{{end}}
