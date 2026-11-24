{{if eq .Language "日本語"}}件名：{{.Title}}{{with .Sender}}
送信者：{{.}}{{end}}{{with .Source}}
ソース：{{.}}{{end}}{{with .Kind}}
種類：{{.}}{{end}}

メッセージ内容：
{{.Context}}{{with .ReplyText}}

送信予定の返信テキスト（最終確認）：
{{.}}{{end}}{{else if .Language}}Title: {{.Title}}{{with .Sender}}
Sender: {{.}}{{end}}{{with .Source}}
Source: {{.}}{{end}}{{with .Kind}}
Kind: {{.}}{{end}}

Item content:
{{.Context}}{{with .ReplyText}}

Reply text to evaluate:
{{.}}{{end}}{{else}}标题：{{.Title}}{{with .Sender}}
发送者：{{.}}{{end}}{{with .Source}}
来源：{{.}}{{end}}{{with .Kind}}
类型：{{.}}{{end}}

消息内容：
{{.Context}}{{with .ReplyText}}

待评估的回复文本：
{{.}}{{end}}{{end}}
