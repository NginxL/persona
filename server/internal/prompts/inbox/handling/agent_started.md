{{if eq .Language "日本語"}}{{.Owner}}が確認し、Agent が起動しました（{{.Machine}} の会話 {{.Session}}）。指示{{if .Edited}}（{{.Owner}}が編集）{{end}}：
{{.Text}}

停止時に最後の発言か失敗理由が届くので、そのとき提案してください。今は inbox_suggest に recap だけを渡して要約を更新します。{{else if .Language}}{{.Owner}} confirmed; the Agent has started (session {{.Session}} on {{.Machine}}). Instructions{{if .Edited}} (edited by {{.Owner}}){{end}}:
{{.Text}}

When it stops, you will receive its final words or failure reason, then submit a suggestion. For now call inbox_suggest with recap only to update the summary.{{else}}{{.Owner}}确认了，Agent 已经开了（{{.Machine}} 上的会话 {{.Session}}），指令{{if .Edited}}（{{.Owner}}改过）{{end}}：
{{.Text}}

它停下来时，你会收到它最后说的话或失败原因，那时再交建议。现在调 inbox_suggest 只给 recap 更新总结。{{end}}
