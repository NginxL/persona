{{if eq .Kind "dm"}}类型：通过 lark-cli 收到的 Lark 私聊，另一方是人
{{else if eq .Kind "bot"}}类型：通过 lark-cli 收到的 Lark 私聊，另一方是机器人或应用
{{else if eq .Kind "mention"}}类型：通过 lark-cli 收到的群消息，提到了你或所有人
{{else if eq .Kind "group"}}类型：通过 lark-cli 收到的普通群消息，包括你参与的话题回复
{{else if eq .Kind "doc"}}类型：通过 lark-cli 监视的 Lark 云文档评论；评论提到了用户或回复了用户参与过的评论
{{else}}类型：通过 lark-cli 收到的 Lark 消息
{{end}}{{if ne .Kind "doc"}}聊天记录若按区段呈现，按「Current messages (new activity):」判断是否需要处理。「Background history (context only, not new requests):」只作背景。
较早的聊天记录若用「—— 新消息 ——」分隔，标记之前只作背景，按标记之后的新消息判断。
{{end -}}
