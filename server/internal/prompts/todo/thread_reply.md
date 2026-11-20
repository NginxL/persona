{{if eq .Language "日本語"}}ToDo「{{.Title}}」の新しい決定（Feishu のトピックでのユーザーの返信{{if .Recorded}}。この ToDo の決定事項に記録済み{{end}}）：
{{.Text}}{{else if .Language}}New decision on to-do “{{.Title}}” (the user's reply in the Feishu topic{{if .Recorded}}; recorded in this to-do's decisions{{end}}):
{{.Text}}{{else}}待办「{{.Title}}」在话题里的新决定（用户在飞书的话题里回的{{if .Recorded}}，已记进这个待办的决策{{end}}）：
{{.Text}}{{end}}
