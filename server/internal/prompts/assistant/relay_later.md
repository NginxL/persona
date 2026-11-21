{{if eq .Language "English"}}<persona-context kind="context" title="Messages that did not wake you" url="">
Other conversations sent you these with conversation_send wake: false. They started no turn of their own and need no reply; read them along with the message below.
{{range .Items}}
From the conversation "{{ctx .FromTitle}}" ({{.FromID}}), {{.At}}:
{{ctx .Text}}
{{end}}</persona-context>
{{- else if eq .Language "日本語"}}<persona-context kind="context" title="あなたを起こさなかったメッセージ" url="">
ほかの会話が conversation_send の wake: false で送ってきたものです。それだけでターンを始めることはなく、返答も不要です。下のメッセージと合わせて読んでください。
{{range .Items}}
会話「{{ctx .FromTitle}}」（{{.FromID}}）から、{{.At}}：
{{ctx .Text}}
{{end}}</persona-context>
{{- else}}<persona-context kind="context" title="没有叫醒你的消息" url="">
下面是别的对话用 conversation_send 带 wake: false 发来的，它们没有单独叫醒你，也不用回复；和后面那条消息一起看。
{{range .Items}}
来自对话「{{ctx .FromTitle}}」（{{.FromID}}），{{.At}}：
{{ctx .Text}}
{{end}}</persona-context>
{{- end}}
