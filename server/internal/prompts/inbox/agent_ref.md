{{if eq .Language "日本語"}}{{if .Lark}}Lark 会話 id：{{.Lark.FeedID}}{{if .Lark.Account}}、Lark アカウント {{.Lark.Account}}{{end}}
{{else if .Doc}}文書 URL：{{.Doc.URL}}
{{if .Doc.Rows}}行の状態を変更すべき場合（完了、対応開始など）は inbox_suggest（kind reply）の changes に [{ref, field, value}] を指定します（上記の ref、field、許可値の一つ）。本人が送信するとき一緒に変更。不要なら省略。{{end}}返信は文書のコメントになります。
{{else if .Moment}}Moments 投稿 id：{{.Moment.PostID}}。返信は投稿のコメント（上で返信対象とされたコメントへの返信）として全社公開されるので、公開可能なことだけを書きます。
{{else if .Connector}}{{with .Connector}}{{if .Via}}購読「{{.Name}}」から来ています。コネクタ {{.Via}}{{with .ViaSource}} の「{{.}}」{{end}}を読み取っています。過去の文脈はそのコネクタの読取ツールで確認します。
{{end}}{{if or (eq .Source "github") (eq .Via "github")}}GitHub URL：{{.URL}}（github_read で全文とコメント、github_diff で差分、github_checks で CI）
{{else if .URL}}URL：{{.URL}}
{{end}}{{with .ReplyTo}}返信先（ソースの説明）：{{.}}。
{{end}}{{end}}{{else if .Mail}}メールアカウント {{.Mail.Account}}、フォルダ {{.Mail.Mailbox}}、最新 uid {{.Mail.UID}}（mail_* ツール用）
{{end}}{{else if .Language}}{{if .Lark}}Lark conversation id: {{.Lark.FeedID}}{{if .Lark.Account}}, in Lark account {{.Lark.Account}}{{end}}
{{else if .Doc}}Document URL: {{.Doc.URL}}
{{if .Doc.Rows}}If a row's status should change (for example finished or now followed up), include [{ref, field, value}] in inbox_suggest (kind reply) changes, using one of the refs, fields and allowed values listed above. It changes when the user sends. Omit if no change is needed.{{end}}The reply will be a document comment.
{{else if .Moment}}Moments post id: {{.Moment.PostID}}. The reply will be a comment under this post (replying to the comment marked as the one to reply to above), visible company-wide: include only what can be public.
{{else if .Connector}}{{with .Connector}}{{if .Via}}This came from subscription “{{.Name}}”, reading connector {{.Via}}{{with .ViaSource}} source “{{.}}”{{end}}. Use that connector's read tools for earlier context.
{{end}}{{if or (eq .Source "github") (eq .Via "github")}}GitHub URL: {{.URL}} (github_read for full text and comments, github_diff for changes, github_checks for CI)
{{else if .URL}}URL: {{.URL}}
{{end}}{{with .ReplyTo}}Reply destination (source description): {{.}}.
{{end}}{{end}}{{else if .Mail}}Email account {{.Mail.Account}}, mailbox {{.Mail.Mailbox}}, newest uid {{.Mail.UID}} (for mail_* tools)
{{end}}{{else}}{{if .Lark}}飞书会话 id：{{.Lark.FeedID}}{{if .Lark.Account}}，在飞书账号 {{.Lark.Account}}{{end}}
{{else if .Doc}}文档链接：{{.Doc.URL}}
{{if .Doc.Rows}}如果某一行的状态该变（比如事情已经做完、开始跟进），在 inbox_suggest（kind reply）的 changes 里给出 [{ref, field, value}]（上面列出的 ref、field 和可选值之一），用户点发送时一起改；不该改就不给。{{end}}回复会作为评论发在文档里。
{{else if .Moment}}公司圈帖子 id：{{.Moment.PostID}}。回复会作为评论发在这个帖子下（回复上面标了「要回复的这条」的那条评论），全公司可见：只说能公开说的。
{{else if .Connector}}{{with .Connector}}{{if .Via}}这条来自订阅「{{.Name}}」：读的是连接器 {{.Via}}{{with .ViaSource}} 的「{{.}}」{{end}}，要看更早的上下文用这个连接器的读工具。
{{end}}{{if or (eq .Source "github") (eq .Via "github")}}GitHub 链接：{{.URL}}（github_read 读全文和评论，github_diff 看改动，github_checks 看 CI）
{{else if .URL}}链接：{{.URL}}
{{end}}{{with .ReplyTo}}回复会{{.}}。
{{end}}{{end}}{{else if .Mail}}邮箱账号 {{.Mail.Account}}，文件夹 {{.Mail.Mailbox}}，最新一封 uid {{.Mail.UID}}（mail_* 工具用）
{{end}}{{end}}
