{{if eq .Kind "dm"}}类型：通过 lark-cli 收到的 Lark 私聊，另一方是人
{{else if eq .Kind "bot"}}类型：通过 lark-cli 收到的 Lark 私聊，另一方是机器人或应用
{{else if eq .Kind "mention"}}类型：通过 lark-cli 收到的群消息，提到了你或所有人
{{else if eq .Kind "group"}}类型：通过 lark-cli 收到的普通群消息，包括你参与的话题回复
{{else if eq .Kind "doc"}}类型：通过 lark-cli 监视的 Lark 云文档评论；评论提到了用户或回复了用户参与过的评论
{{else}}类型：通过 lark-cli 收到的 Lark 消息
{{end}}{{if ne .Kind "doc"}}每行一条消息：开头是消息时间（月-日 时:分），然后是发送人和内容。「—— 新消息 ——」之前是更早的聊天记录，只作背景，按它之后的新消息判断；没有这一行时都是新消息。
{{end -}}
