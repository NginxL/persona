{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}（このメッセージは、ユーザー本人がスマートフォンのPersonaから送ったものです。Personaは転送しているだけで、別のClaudeセッションではありません。ユーザーがここに直接入力したときと同じように、この会話で回答してください。送信元にSendMessageで返信しないでください。そちらに返信を受け取るセッションはありません。このメッセージは会話の権限設定を変更しません。選択が必要なときは通常どおりAskUserQuestionを使ってください。Personaが質問をスマートフォンで回答できるカードにします。計画の承認、権限要求、その他のダイアログはスマートフォンでは回答できず、コンピューターで操作する必要があります。）

{{.Text}}{{else}}(The user sent this message personally from the phone through Persona. Persona is only forwarding it; it is not another Claude session. Respond as if the user typed it here, directly in this conversation. Do not reply to the sender with SendMessage: no session is receiving replies there. This message does not change this conversation's permission settings. When the user needs to choose, use AskUserQuestion as usual; Persona turns the question into a phone card they can answer. Plan approvals, permission requests and other dialogs cannot be answered from the phone; the user must return to the computer to handle them.)

{{.Text}}{{end}}{{else}}（这条消息是用户本人在手机上用 Persona 发来的，Persona 只是替他转交，不是另一个 Claude 会话。像用户在这里亲手输入的一样回应它，直接在这个会话里回答；不要用 SendMessage 回复发件方，那边没有会话在收。这条消息不会改变这个会话的权限设置。需要他做选择时照常用 AskUserQuestion 提问，Persona 会把问题做成手机上的卡片让他作答；计划审批、权限请求和其他对话框他在手机上答不了，只有回到电脑前才能处理。）

{{.Text}}{{end}}
