{{if eq .Language "日本語"}}{{if .Described}}種類：{{.Described}}
{{else if eq .Source "github"}}{{template "inbox/kinds/github" .}}
{{else if eq .Source "imessage"}}{{template "inbox/kinds/imessage" .}}
{{else if eq .Source "larkcli"}}{{template "inbox/kinds/larkcli" .}}
{{else if eq .Kind "dm"}}種類：Lark の個人チャット
{{else if eq .Kind "mention"}}種類：Lark グループでユーザーがメンションされた
{{else if eq .Kind "group"}}種類：Lark グループの新着メッセージ（ユーザーへのメンションなし）
{{else if eq .Kind "bot"}}種類：Lark のボット / アプリからのメッセージ
{{else if eq .Kind "doc"}}種類：Lark ドキュメントでユーザーがメンションされたか、ユーザーへのコメントがある（表や多次元テーブルの行の対応や状態変更が必要な場合もある）
{{else if eq .Kind "moment"}}種類：Lark Moments（社内全体への公開投稿）でユーザーの投稿やコメントへの返信、またはメンションがある。多くは雑談や賛同で、質問や応答の依頼がある場合だけ対応が必要
{{else if eq .Kind "mail"}}{{if .MailDirect}}種類：ユーザー宛てのメール
{{else}}種類：CC、一斉送信、メーリングリストのメール
{{end}}{{end}}{{if .Latest}}
最新のメッセージ：{{ctx .Latest}}

会話の文脈（以前のメッセージを含む）：
{{end}}{{ctx .Context}}{{else if .Language}}{{if .Described}}Type: {{.Described}}
{{else if eq .Source "github"}}{{template "inbox/kinds/github" .}}
{{else if eq .Source "imessage"}}{{template "inbox/kinds/imessage" .}}
{{else if eq .Source "larkcli"}}{{template "inbox/kinds/larkcli" .}}
{{else if eq .Kind "dm"}}Type: Lark direct message
{{else if eq .Kind "mention"}}Type: Lark group message mentioning the user
{{else if eq .Kind "group"}}Type: new Lark group message (not mentioning the user)
{{else if eq .Kind "bot"}}Type: message from a Lark bot / app
{{else if eq .Kind "doc"}}Type: a Lark document mentions the user or has a comment for them (a spreadsheet or multidimensional table may need a row followed up or its status changed)
{{else if eq .Kind "moment"}}Type: someone commented on the user's post, replied to their comment or mentioned them in Lark Moments (company-wide public posts, like a social feed); most are chatter or appreciation, needing the user only for a question or a requested response
{{else if eq .Kind "mail"}}{{if .MailDirect}}Type: email sent directly to the user
{{else}}Type: email copied to the user, bulk mail or a mailing list
{{end}}{{end}}{{if .Latest}}
Newest message: {{ctx .Latest}}

Conversation context (including earlier messages):
{{end}}{{ctx .Context}}{{else}}{{if .Described}}类型：{{.Described}}
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
{{end}}{{ctx .Context}}{{end}}
