{{if eq .Language "日本語"}}Agent が{{if .Failed}}失敗しました{{else}}停止しました{{end}}（{{.Machine}} の会話 {{.Session}}）。{{if .Failed}}失敗理由{{else}}最後の発言{{end}}：

<persona-context kind="agent_result" title="{{if .Failed}}Agent の失敗理由{{else}}Agent の最後の発言{{end}}">
{{if .Text}}{{ctx .Text}}{{else}}（最後の発言なし）{{end}}
</persona-context>

読んで対応を続け、inbox_suggest で次の提案と更新した recap を提出してください。{{else if .Language}}The Agent {{if .Failed}}failed{{else}}stopped{{end}} (session {{.Session}} on {{.Machine}}). {{if .Failed}}Failure reason{{else}}Its final words{{end}}:

<persona-context kind="agent_result" title="{{if .Failed}}Agent failure reason{{else}}Agent's final words{{end}}">
{{if .Text}}{{ctx .Text}}{{else}}(No final words){{end}}
</persona-context>

Read this and continue the matter: call inbox_suggest with the next suggestion and an updated recap.{{else}}{{if .Failed}}Agent 失败了{{else}}Agent 停了{{end}}（{{.Machine}} 上的会话 {{.Session}}）。{{if .Failed}}失败原因{{else}}它最后说的{{end}}：

<persona-context kind="agent_result" title="{{if .Failed}}Agent 的失败原因{{else}}Agent 的最后一句{{end}}">
{{if .Text}}{{ctx .Text}}{{else}}（没有最后的话）{{end}}
</persona-context>

读完接着处理这件事：调 inbox_suggest 交下一步建议，同时更新 recap。{{end}}
