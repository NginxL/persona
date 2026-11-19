{{if eq .Language "English"}}Chat: {{.Name}} ({{if eq .Kind "dm"}}direct message{{else if eq .Kind "group"}}group{{else if eq .Kind "bot"}}bot / AI{{else if eq .Kind "mail"}}email conversation{{else if eq .Kind "moment"}}company-wide post and comments (public within the company){{else if eq .Kind "pr"}}GitHub PR: description, comments and reviews{{else if eq .Kind "issue"}}GitHub issue: description and comments{{end}}{{if .Automated}}, automated (notification / marketing address, not a person){{end}})

Known projects:{{template "kb/known_projects_localized" .}}
Existing tags:{{with .Tags}} {{join . "、"}}{{end}}

# Existing compressed context
{{if blank .Summary}}(none yet){{else}}{{.Summary}}{{end}}
{{with .Pinned}}
# Confirmed facts (read-only, authoritative, do not rewrite)
{{.}}
{{end}}{{with .People}}
# Confirmed facts about people (read-only, authoritative)
{{range .}}- {{.Name}}: {{.Pinned}}
{{end}}{{end}}
# New messages
{{.News}}{{else if eq .Language "日本語"}}会話：{{.Name}}（{{if eq .Kind "dm"}}個別メッセージ{{else if eq .Kind "group"}}グループ{{else if eq .Kind "bot"}}ボット / AI{{else if eq .Kind "mail"}}メールのやり取り{{else if eq .Kind "moment"}}社内公開の投稿とコメント{{else if eq .Kind "pr"}}GitHub PR：説明、コメント、レビュー{{else if eq .Kind "issue"}}GitHub issue：説明とコメント{{end}}{{if .Automated}}、自動送信（通知・販促アドレス。本人の執筆ではない）{{end}}）

既知のプロジェクト：{{template "kb/known_projects_localized" .}}
既存のタグ：{{join .Tags "、"}}

# 既存の圧縮コンテキスト
{{if blank .Summary}}（まだありません）{{else}}{{.Summary}}{{end}}
{{with .Pinned}}
# 確認済みの事実（読み取り専用。最優先で、書き換え禁止）
{{.}}
{{end}}{{with .People}}
# 人物の確認済み事実（読み取り専用、最優先）
{{range .}}- {{.Name}}：{{.Pinned}}
{{end}}{{end}}
# 新しいメッセージ
{{.News}}{{else}}会话：{{.Name}}（{{if eq .Kind "dm"}}私聊{{else if eq .Kind "group"}}群聊{{else if eq .Kind "bot"}}机器人 / AI{{else if eq .Kind "mail"}}邮件往来{{else if eq .Kind "moment"}}公司圈帖子和评论（公司内公开）{{else if eq .Kind "pr"}}GitHub PR：描述、评论和 review{{else if eq .Kind "issue"}}GitHub issue：描述和评论{{end}}{{if .Automated}}，自动发出（通知 / 营销地址，不是真人写的）{{end}}）

已知项目：{{template "kb/known_projects_localized" .}}
已有标签：{{join .Tags "、"}}

# 已有的压缩上下文
{{if blank .Summary}}（还没有）{{else}}{{.Summary}}{{end}}
{{with .Pinned}}
# 已确认的事实（只读，以此为准，不得改写）
{{.}}
{{end}}{{with .People}}
# 人物已确认的事实（只读，以此为准）
{{range .}}- {{.Name}}：{{.Pinned}}
{{end}}{{end}}
# 新消息
{{.News}}{{end}}
