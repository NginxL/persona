{{if eq .Language "日本語"}}{{.Marker}}
📥 受信箱の案件「{{.Title}}」に更新があります
（これは Persona の受信箱通知で、ここでのユーザーの発言ではありません。）本人が渡した受信箱の案件（イベント id：{{.ID}}）です。以下の inbox_suggest は必ず item: "{{.ID}}" を付けます。今後の新着はここに転送されないので、必要ならソースのツールで調べます。

{{.Note}}{{else if .Language}}{{.Marker}}
📥 Inbox matter “{{.Title}}” has an update
(This is a Persona Inbox notice, not words the user said here.) The user handed this Inbox event to you (event id: {{.ID}}). Include item: "{{.ID}}" whenever inbox_suggest is mentioned below. Later messages will not be forwarded here; use the source's tools to check.

{{.Note}}{{else}}📥 收件箱事件「{{.Title}}」有了下文
（这是 Persona 的收件箱通知，不是用户在这里说的话。）这是用户交给你的收件箱事件（事件 id：{{.ID}}）。下面提到 inbox_suggest 时都带上 item: "{{.ID}}"；这件事之后的新消息不会转到这里，要看就用来源的工具自己去查。

{{.Note}}{{end}}
