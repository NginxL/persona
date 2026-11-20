{{if eq .Language "日本語"}}{{if eq .Kind "group"}}種類：「メッセージ」（iMessage / SMS）のグループの新着。多くは参考情報で、質問や応答の依頼がある場合は対応が必要
{{- else}}種類：「メッセージ」（iMessage / SMS）の未返信の個人チャット{{end}}{{else if .Language}}{{if eq .Kind "group"}}Type: new message in a Messages (iMessage / SMS) group; usually FYI, needing the user only for a question or requested response
{{- else}}Type: an unanswered direct message to the user in Messages (iMessage / SMS){{end}}{{else}}{{if eq .Kind "group"}}类型：「信息」（iMessage / 短信）群聊里的新消息；多数知悉即可，有人问用户、要用户回应时才需要用户
{{- else}}类型：「信息」（iMessage / 短信）里有人私聊用户，还没回{{end}}{{end}}
