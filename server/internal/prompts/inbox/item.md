{{if .Described}}类型：{{.Described}}
{{else if eq .Source "github"}}{{template "inbox/kinds/github" .}}
{{else if eq .Source "imessage"}}{{template "inbox/kinds/imessage" .}}
{{else if eq .Source "larkcli"}}{{template "inbox/kinds/larkcli" .}}
{{else if eq .Kind "dm"}}类型：飞书私聊
{{else if eq .Kind "mention"}}类型：飞书群里 @ 了用户
{{else if eq .Kind "group"}}类型：飞书群的新消息（没有 @ 用户）
{{else if eq .Kind "bot"}}类型：飞书机器人 / 应用发来的消息
{{else if eq .Kind "doc"}}类型：飞书云文档里 @ 了用户或评论了用户（表格、多维表格里可能要用户跟进某一行、改状态）
{{else if eq .Kind "moment"}}类型：公司圈（飞书 Moments，公司内的公开动态，类似朋友圈）里有人评论了用户的帖子、回复了用户的评论或 @ 了用户；多数是闲聊和捧场，有人在问用户问题、要用户回应时才需要用户
{{else if eq .Kind "mail"}}{{if .MailDirect}}类型：邮件（直接发给用户）
{{else}}类型：邮件（抄送、群发或邮件列表）
{{end}}{{end}}{{if .Latest}}本次新消息：{{ctx .Latest}}

会话上下文（含较早消息）：
{{end}}{{ctx .Context}}
