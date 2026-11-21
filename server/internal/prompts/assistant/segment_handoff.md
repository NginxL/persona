<persona-context kind="context" title="{{if eq .Language "ja"}}会話の続き{{else if eq .Language "zh-Hans"}}接续对话{{else}}Conversation continuity{{end}}" url="">
{{if eq .Language "ja"}}同じ会話の別の実行セッションです。以下の内容を背景として、今回のメッセージに沿って続けてください。{{else if eq .Language "zh-Hans"}}这是同一个对话的新运行会话。以下内容只作背景，请按本次消息继续处理。{{else}}This is a new execution session of the same conversation. Use the following as background and continue with the current message.{{end}}
{{if .Partial}}{{if eq .Language "ja"}}一部の履歴は利用できません。{{else if eq .Language "zh-Hans"}}部分历史暂不可用。{{else}}Some history is unavailable.{{end}}{{end}}
{{if .Summary}}{{ctx .Summary}}{{end}}
{{range .Recent}}{{.Role}}: {{ctx .Text}}
{{end}}</persona-context>
{{.Text}}
