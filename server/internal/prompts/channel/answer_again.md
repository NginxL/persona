<persona-context kind="context" title="{{if eq .Language "日本語"}}{{attr .Name}} ボット{{else if .Language}}{{attr .Name}} bot{{else}}{{.Name}}机器人{{end}}" url="">
{{if eq .Language "日本語"}}前のターンはテキストの回答がないまま終了し、{{ctx .Name}} には空欄や仮のメッセージしか表示されませんでした。
このターンの最終回答を、結論から直接書いてください。すでに済ませた作業を繰り返す必要はありません。本当に伝えることがなければ、その理由を一文で説明してください。{{else if .Language}}The previous turn ended without a text answer. {{ctx .Name}} showed only a blank or a placeholder.
Write the final answer for that turn directly, leading with the conclusion. Do not repeat work you have already done. If there really is nothing to say, explain why in one sentence.{{else}}上一轮结束时没有文字回答，{{.Name}}里只能看到空白或占位的话。
请把这一轮的最终回答直接写出来，结论先行；已经做过的事不用再做一遍。确实没有要说的，用一句话说明为什么。{{end}}
</persona-context>
