{{if .Persona}}<persona-context kind="context" title="{{if eq .Language "日本語"}}{{attr .Name}} ボット{{else if .Language}}{{attr .Name}} bot{{else}}{{.Name}}机器人{{end}}" url="">
{{if eq .Language "日本語"}}{{ctx (or .Owner "ユーザー")}}が {{ctx .Name}} の個別チャットで Persona ボットにこのメッセージを送りました。このターンの最終回答はボットとして {{ctx .Name}} に返信されます{{if .Plain}}。プレーンテキストで結論から簡潔に書き、{{if .ImagesOut}}表は使わず（画像を見せるときは ![説明](https://…) の形で1行に1枚置くと画像として届きます）{{else}}表や画像を使わず{{end}}、見出しや太字は最小限にしてください{{else}}（Markdown）。結論から簡潔に書いてください{{end}}。notify_user を重ねて呼ぶ必要はありません。確認が必要なときは通常どおり ask_user を使ってください。{{if .Asks}}{{ctx .Name}} で番号やテキストを返信して回答できます。{{else}}ユーザーはアプリで回答します。{{end}}{{else if .Language}}{{ctx (or .Owner "The user")}} sent this private message to the Persona bot on {{ctx .Name}}. The final answer for this turn goes back to {{ctx .Name}} as the bot{{if .Plain}}, in plain text. Lead with the conclusion, keep it short, {{if .ImagesOut}}use no tables (to show a picture, put it on a line of its own as ![description](https://…); it arrives as a picture){{else}}use no tables or images{{end}}, and keep headings and bold text to a minimum{{else}} in Markdown. Lead with the conclusion and keep it short{{end}}. Do not call notify_user again. Use ask_user as usual when you need confirmation. {{if .Asks}}The user can reply with a number or text in {{ctx .Name}}.{{else}}The user must answer in the app.{{end}}{{else}}{{or .Owner "用户"}}在{{.Name}}里私聊 Persona 机器人发来这条消息。这一轮的最终回答会以机器人的身份发回{{.Name}}{{if .Plain}}，用纯文字显示：结论先行、短一些，{{if .ImagesOut}}不用表格（要给图就单独一行写 ![说明](https://…)，会作为图片发过去）{{else}}不用表格和图片{{end}}，标题、加粗尽量少用{{else}}（Markdown）：结论先行、短一些{{end}}，也不用再 notify_user。{{if .Asks}}需要确认的照常 ask_user，{{.Name}}里回复序号或文字就能答。{{else}}需要确认的照常 ask_user，用户要到 App 里答。{{end}}{{end}}
</persona-context>
{{end}}{{with .Quote}}<persona-context kind="context" title="{{if eq $.Language "日本語"}}引用されたメッセージ{{else if $.Language}}Quoted message{{else}}引用的消息{{end}}" url="">
{{ctx .}}
</persona-context>
{{end}}{{with .Forward}}<persona-context kind="context" title="{{if eq $.Language "日本語"}}転送されたチャット履歴{{else if $.Language}}Forwarded chat history{{else}}转发的聊天记录{{end}}" url="">
{{ctx .}}
</persona-context>
{{end}}{{.Text}}{{if .Files}}{{if not (blank .Text)}}

{{end}}{{if eq .Language "日本語"}}添付ファイル（このコンピューターに保存済み）：{{else if .Language}}Attachments (saved on this computer):{{else}}附件（已存到这台电脑上）：{{end}}{{range .Files}}
{{.}}{{end}}{{end}}{{if .Unsupported}}{{if or (not (blank .Text)) .Files}}

{{end}}{{if eq .Language "日本語"}}（{{.Name}} から次の項目も届きましたが、ボットは転送できません：{{join .Unsupported "、"}}）{{else if .Language}}({{.Name}} also sent these items, which the bot cannot forward: {{join .Unsupported ", "}}){{else}}（{{.Name}}里还发来了这些，机器人还不能转交：{{join .Unsupported "、"}}）{{end}}{{end}}
