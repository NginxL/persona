{{if eq .Language "日本語"}}{{if eq .Kind "dm"}}種類：lark-cli 経由の Lark 個人チャット。相手は人
{{else if eq .Kind "bot"}}種類：lark-cli 経由の Lark 個人チャット。相手はボットかアプリ
{{else if eq .Kind "mention"}}種類：lark-cli 経由のグループメッセージ。あなたか全員へのメンションあり
{{else if eq .Kind "group"}}種類：lark-cli 経由の通常のグループメッセージ。参加したトピックへの返信も含む
{{else if eq .Kind "doc"}}種類：lark-cli で監視する Lark ドキュメントのコメント。ユーザーへのメンション、または参加したコメントへの返信{{else}}種類：lark-cli 経由の Lark メッセージ{{end}}{{if ne .Kind "doc"}}
セクション分けされた会話では「Current messages (new activity):」と「Background history (context only, not new requests):」を区別し、後者は背景としてのみ扱う。古い形式で「—— 新消息 ——」の区切りがある場合、区切り以降だけを新着として判断する。
{{end -}}{{else if .Language}}{{if eq .Kind "dm"}}Type: Lark direct message received through lark-cli; the other party is a person
{{else if eq .Kind "bot"}}Type: Lark direct message received through lark-cli; the other party is a bot or app
{{else if eq .Kind "mention"}}Type: group message received through lark-cli, mentioning you or everyone
{{else if eq .Kind "group"}}Type: ordinary group message received through lark-cli, including replies in topics you joined
{{else if eq .Kind "doc"}}Type: comment on a Lark document watched through lark-cli, mentioning the user or replying to a comment they participated in{{else}}Type: Lark message received through lark-cli{{end}}{{if ne .Kind "doc"}}
If the chat has sections, judge new activity under “Current messages (new activity):” and use “Background history (context only, not new requests):” for context. In the older format, treat only messages after “—— 新消息 ——” as new.
{{end -}}{{else}}{{if eq .Kind "dm"}}类型：通过 lark-cli 收到的 Lark 私聊，另一方是人
{{else if eq .Kind "bot"}}类型：通过 lark-cli 收到的 Lark 私聊，另一方是机器人或应用
{{else if eq .Kind "mention"}}类型：通过 lark-cli 收到的群消息，提到了你或所有人
{{else if eq .Kind "group"}}类型：通过 lark-cli 收到的普通群消息，包括你参与的话题回复
{{else if eq .Kind "doc"}}类型：通过 lark-cli 监视的 Lark 云文档评论；评论提到了用户或回复了用户参与过的评论
{{else}}类型：通过 lark-cli 收到的 Lark 消息
{{end}}{{if ne .Kind "doc"}}聊天记录若按区段呈现，按「Current messages (new activity):」判断是否需要处理。「Background history (context only, not new requests):」只作背景。
较早的聊天记录若用「—— 新消息 ——」分隔，标记之前只作背景，按标记之后的新消息判断。
{{end -}}{{end}}
