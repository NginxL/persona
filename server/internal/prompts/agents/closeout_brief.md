{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}{{if .Session}}（この会話についてのPersonaの取り決めであり、ユーザーの発言ではありません。返答は不要で、そのまま従ってください。）
{{end}}終了記録（hubがこのターンの到達点を把握し、再確認を不要にするためのもの）：ファイルを変更するかコマンドを実行したターンの最後の返答は、下の形式の記録で終えてください{{if .AfterDelivery}}。納品・障害・対応者はその中のdelivery / missing / unblockで記録し、納品行は別に書かないでください{{end}}。質問に答えただけでファイル変更もコマンド実行もないターンには不要です。ただし、末尾が会話を続けるだけの質問や任意の提案で、返答がなくても作業に支障がなければ、status="done"でmissingのない記録を書き、hubにユーザー待ちと誤認させないでください。

{{.Form}}

{{template "agents/closeout_rules" .}}{{else}}{{if .Session}}(Persona's convention for this session, not the user's words. No response needed; follow it.)
{{end}}Closeout record (the hub uses it to see where this turn stands, so nobody has to ask again): end the final reply of any turn that changed files or ran commands with a block like the one below{{if .AfterDelivery}}, recording the delivery, what blocks it and who must act in its delivery / missing / unblock instead of a separate delivery line{{end}}. A turn that only answered a question without file changes or commands needs no record, except when it ends with an optional conversational question or offer that does not block work if unanswered: include status="done" and no missing so the hub does not mistake it for waiting on the user.

{{.Form}}

{{template "agents/closeout_rules" .}}{{end}}{{else}}{{if .Session}}（Persona 给这个会话的约定，不是用户说的话，不用回应，照做就行。）
{{end}}收尾记录（hub 靠它知道这一轮做到哪了，写了就不用别人再来问你）：凡是动过文件或跑过命令的一轮，最后的回复都以下面这样一段结尾{{if .AfterDelivery}}，用其中的 delivery / missing / unblock 记录交付、卡点和负责方，不再另写交付行{{end}}。只回答问题、没动文件也没跑命令的一轮不用写；但这样的一轮如果以问话结尾，而这个问题只是接着聊或顺口提议、用户不回也不耽误什么，就照样写一段 status="done"、不带 missing 的记录，hub 才不会把这一轮当成在等用户回复。

{{.Form}}

{{template "agents/closeout_rules" .}}{{end}}
