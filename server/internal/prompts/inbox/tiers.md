{{if eq .Language "日本語"}}- action（要対応）：ユーザー本人に向けられ、返答、判断、作業が必要。しなければ誰かを待たせるか、本人の用事が遅れる。例：直接の質問、本人への作業・意思表示のメンション、
  人からの仕事の個人チャット（質問、依頼、会う約束、問題や分析の共有）、本人宛ての承認・確認・レビュー依頼、本人に割り当てられた障害、
  返答が必要なメール、本人の用事の迫った締切。
- fyi（参考情報）：関係があり目を通す価値はあるが、本人の作業は不要。グループの進捗や結論、CC メール、情報のある通知など。
- noise（ノイズ）：営業、購読、newsletter、広告、自動通知、無関係なグループ雑談、ボットの連投。

送信者の後の原文マーカー「（@了我）」は本人へのメンション（「我」はユーザー）、「（@所有人）」は全員へのメンション。両方あれば本人宛てと扱う。
Lark（lark-cli 経由を含む）のグループは必ずこの方式を使う。両方なければ本人宛てではなく、本文中の @ の名前は他人やボット。別のソースはこのマーカーを使わないので、
マーカーがなくても判断できない。種類と本文を読む。本人の発言に続く返信は本人への話しかけである。

次は action ではなく、関連度に応じ fyi または noise：
- グループで他人、ボット、Oncall アシスタント、Agent に @ している（相手が答える）。本人への @ や名指し、本人への返信がない議論、質問、支援・レビュー依頼。
- アラート、監視、使用量・容量報告、Oncall 通知、チケット進捗などの自動メッセージ、特に同じ警告の繰り返し。ただし本人に即時対応を明示的に割り当てた場合を除く。
- 全員宛ての通知やリマインダー（週報、OKR 更新、まだ使っている人の確認など）。
- 個人・グループの雑談、挨拶、冗談、絵文字、スタンプ、感想で、返事を待っていないもの。
- 現在の依頼を誰かが明確に引き受けた、回答・対応した、または相手が現在の問題は解決済み・対応不要と確認したもの。
- Persona 自身が Lark 経由で本人に送った承認依頼、質問、端末での待機、検収依頼、進捗、返事待ちの通知。これらは Persona アプリ内で扱う。


各案件を個別に判断し、その案件に割り当てられた新しい動きと未解決の依頼を考慮する。バッチの最後のメッセージや別件の返信・確認だけで案件を完了にしない。
古い返信は、同じ案件で後から出た新しい依頼の解決を示さない。対応予定という約束や、別件を確認したという発言だけでは完了にしない。
[Image: ...] は未確認の画像を示すプレースホルダー。人からの仕事の個人チャットの新着に画像が含まれ、情報共有のみ・解決済みと明記されていない場合は action を選ぶ。

人からの個人チャットは既定で action。本人を選んで連絡しており、疑問符がなくても（リンク、分析、「後で見てもらう」など）同じ。明らかな雑談、挨拶、
冗談、絵文字だけなら fyi。ボットやアプリの個人チャットは人のものではない。他で迷ったら、本人が見なければ誰かを待たせるか、必要な用事が遅れるかを考える。そうなら action、
そうでなければ fyi。{{else if .Language}}- action (Needs you): directed at the user personally; they must reply, decide or act, or someone will be left waiting or their work delayed. Examples: a direct question; a mention asking them to act or decide;
  a human DM about work (a question, request, invitation to meet, problem or analysis); a named approval / confirmation / review request; an incident assigned to them personally;
  email needing their reply; an approaching deadline for their own work.
- fyi (For your information): relevant and worth a glance, without requiring action, such as group progress or conclusions, copied email, informative notices.
- noise: marketing, subscriptions, newsletters, ads, automated notices, unrelated group chatter, bot spam.

The literal sender marker “（@了我）” means the user personally was mentioned (“me” is the user); “（@所有人）” means everyone was mentioned. Both means a personal mention.
Lark group messages (including lark-cli) always use these markers: neither means no mention of the user; names mentioned in the body are other people or bots. Other sources do not use these markers,
so their absence proves nothing; judge the type and content. A reply continuing what the user said in the conversation is addressed to them.

These are not action; choose fyi or noise by relevance:
- Group mentions of someone else, a bot, Oncall assistant or Agent (they will answer); group discussions, questions, requests for help or review without a mention/name of the user or a reply to them;
- Automated alerts, monitoring, water/capacity reports, Oncall notices, ticket progress, especially repeated alerts, unless explicitly assigned to the user personally for immediate action;
- @everyone announcements and reminders (weekly reports, OKR updates, asking who still uses something);
- DM/group chatter, greetings, jokes, emoji, stickers or musings with no reply expected;
- Someone has explicitly taken over, answered or handled the current request, or the other party confirms the current issue is resolved or no longer needed;
- Reminders and progress Persona itself sent to the user through Lark, such as “Approval needed: …”, “A question for you: …”, “… is waiting in the terminal”, “Please verify: …”, “Task progress: …”, “Still waiting for you: …”; these are handled in the Persona app.


Judge each event separately from the new activity assigned to it and any outstanding requests. The last message in the batch or a reply resolving another event cannot settle it.
An earlier reply does not resolve a request raised later in the same event. A promise to look at it or confirmation about a different issue does not close it.
[Image: ...] is a placeholder for an unread image. In a human work DM, new activity containing such an image calls for action unless text clearly says the current request is informational or resolved.

A human DM defaults to action: the person deliberately approached the user, even without a question mark (a link, analysis, or “I'll ask you to look shortly”); only clear chatter, greetings,
jokes or emoji are fyi. Bot/app DMs are not human DMs. Otherwise, when unsure, ask: if the user skips this, will someone wait or something they should do be delayed? If yes, choose action;
otherwise choose fyi.{{else}}- action（需要你）：冲着用户本人来，要用户本人回复、决定或动手，不然有人干等着他、或者他的事会被耽误。例如：有人直接问用户问题、@用户要用户做事或表态、
  私聊里真人跟用户说工作上的事（提问、请求、约他见面、把问题或分析发给他）、点名给用户的审批 / 确认 / 评审请求、派给用户本人处理的故障、
  需要用户回复的邮件、用户自己的事截止时间临近。
- fyi（知悉即可）：和用户有关、值得扫一眼，但不需要用户做什么。例如：群里同步的进展和结论、抄送给用户的邮件、有信息量的通知。
- noise（噪音）：营销、订阅、newsletter、广告、自动化通知、和用户无关的群聊闲聊、机器人刷屏。

消息里发送人后面标着「（@了我）」的 @ 了用户本人（这里的「我」是用户），「（@所有人）」是 @ 了所有人，两个都标着的按 @ 了用户本人算。
飞书（Lark，包括经 lark-cli 收到的）群消息都按这个规矩标：没有这两个标记就是没有 @ 用户，正文里 @ 的名字是别人或机器人，不是用户。别的来源的消息不带这两个标记，
没有标记说明不了什么，看类型和正文判断 @ 的是不是用户。别人接着用户自己在会话里说过的话往下回的，是在跟用户说。

下面这些不是 action，按和用户的相关程度给 fyi 或 noise：
- 群里 @ 的是别人，或者 @ 的是机器人、Oncall 助手、Agent（它们会去答）；群里没有 @ 用户、没点用户名字、也不是接着用户的话说的讨论、提问、求助、评审请求；
- 告警、监控、水位 / 容量报告、Oncall 通知、工单进度这类自动发的消息，同一个告警反复来的更是；除非消息明确派给用户本人、要他马上处理；
- @所有人 的通知和提醒（填周报、更新 OKR、问谁还在用某个东西）；
- 私聊或群里的闲聊、寒暄、玩笑、表情、贴纸、感慨，对方并没有在等用户回什么；
- 当前请求已有别人明确接手去处理、已经答清楚或处理完，或者对方明确确认当前问题已解决 / 不用了；
- 助手（Persona）自己经飞书发给用户的提醒和进展，比如「要你批准：…」「在问你：…」「…在终端里等你」「请验收：…」「任务进展：…」「还在等你：…」：这些在 Persona App 里处理。

分别判断每个事项的整体状态：结合归入该事项的新活动及其仍未解决的请求，不能只看批次最后一条消息，也不能用其他事项的回复或确认替它结案。
较早的回复不能证明同一事项后来出现的新请求已解决。「我看看」「稍后处理」只是承诺处理，确认另一个问题也不能关闭后来的新问题。
[Image: ...] 只是未读取的图片占位，不代表你看到了图里的内容。真人工作私聊里，归入某事项的新活动包含这样的图片，没有明确文字说明该事项的当前请求仅供同步或已解决时，
需要用户查看或回应，选 action；不要仅凭旧对话里的回复或确认就判为 fyi。

私聊里真人发来的消息默认是 action：他特意找的就是用户，哪怕没写问号（发个链接、发个分析、说一句「一会儿找你看下」）；只有明显是闲聊、寒暄、
玩笑、表情时才是 fyi。机器人和应用发来的私聊不算真人。别的拿不准时问自己：用户不看这一条，会不会有人干等着他、或者他该做的事被耽误？会，选 action；
不会，选 fyi。{{end}}
