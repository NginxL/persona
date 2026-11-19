{{if .Language}}{{if eq .Language "日本語"}}{{if .Source}}（この会話はアプリの外から開始されました。拡張機能が付けた「{{.Source}}」は参考にとどめ、会話で取り組む内容をタイトルにしてください。）

{{end}}ユーザー：
{{.First}}{{if .Reply}}

アシスタント：
{{.Reply}}{{end}}{{if .Retry}}

（前の回答「{{.Retry}}」は形式に合っていません。種類はシステム指示にある中国語の10種類から選び、トピックは{{.TopicMax}}文字以内にして、もう一度答えてください。）{{end}}{{else}}{{if .Source}}(This conversation was opened outside the app. Its extension called it “{{.Source}}”. Use that only as a hint and name the work this conversation is about.)

{{end}}User:
{{.First}}{{if .Reply}}

Assistant:
{{.Reply}}{{end}}{{if .Retry}}

(The last answer “{{.Retry}}” did not match the format. Use one of the ten Chinese kinds in the system instructions and keep the topic within {{.TopicMax}} characters. Try again.){{end}}{{end}}{{else}}{{if .Source}}（这段对话不是在 App 里开的。开它的扩展管它叫「{{.Source}}」，只作参考：标题照样写这段对话要做的那件事。）

{{end}}用户：
{{.First}}{{if .Reply}}

助手：
{{.Reply}}{{end}}{{if .Retry}}

（上一次回的「{{.Retry}}」不合格式：类型只能是上面十个之一，主题不超过 14 个字。重新给一个。）{{end}}{{end}}
