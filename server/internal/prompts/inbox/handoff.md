{{if eq .Language "日本語"}}<persona-context kind="context" title="受信箱の案件 · {{attr .Title}}" url="">
ユーザーが受信箱からこの件をあなたに渡しました（イベント id：{{ctx .ID}}）。依頼はこの文脈の後に続きます。依頼に従いここで答え、案件に書き戻すには inbox_suggest に item: "{{ctx .ID}}" を付けます。
- recap：背景、進捗、結論の要約。本人に最初に表示されます。
- kind reply：本人の口調でそのまま送れる返信。kind agent：Agent に任せ、text は指示。kind conclusion：対応不要で text は結論、outcome は fyi（知るだけ）か done（アーカイブ可）。
本人が返信・結論を求めたとき、またはここで合意したとき（了承、このまま返信、終了の指示）に書き戻します。結論や返信が変わったら再度呼び、新版で置換。終わらせるよう言われたら最終版を提出します。
要約と返信はこの会話と受信箱に一枚のカードで表示されます。本人の確認前には送信されません。自分で送らず、回答に返信全文を重ねて貼る必要もありません。確認、却下、発言があれば通知されます。
この件の今後の新着はここに転送されません。本人に聞かれたらソースのツールで調べます。
</persona-context>
{{.Text}}{{else if .Language}}<persona-context kind="context" title="Inbox matter · {{attr .Title}}" url="">
The user handed this matter to you from the Inbox (event id: {{ctx .ID}}); their request follows this context. Answer them here as requested. To write back to the matter, call inbox_suggest with item: "{{ctx .ID}}":
- recap: background, progress and conclusion; the user sees this first;
- kind reply: a reply in the user's voice ready to send verbatim; kind agent: dispatch an Agent, text is its instructions; kind conclusion: nothing more to do, text is the conclusion and outcome is fyi (for information) or done (ready to archive).
Write back when the user asks for a reply or conclusion, or when you settle the matter here (they say OK, send that, wrap up). If the conclusion or reply changes, call again to replace it; submit the final version when asked to wrap up.
The recap and reply appear as one card here and in the Inbox. Nothing is sent before user confirmation; do not send it yourself or repeat the whole reply in your answer. You will hear when the user confirms, rejects or says something.
Later new messages for this matter will not be forwarded here; use the source's tools to check when the user asks.
</persona-context>
{{.Text}}{{else}}<persona-context kind="context" title="收件箱事件 · {{attr .Title}}" url="">
用户从收件箱把这件事交给你（事件 id：{{ctx .ID}}），要求在这段上下文后面。照要求在这里回答用户；要把结果写回这件事，调 inbox_suggest 并带上 item: "{{ctx .ID}}"：
- recap：这件事的总结（背景、进展、结论），用户会先看到它；
- kind reply：以用户的口吻写好、能原样发出去的回复；kind agent：派个 Agent 去做，text 是给它的指令；kind conclusion：不用再做什么，text 是结论，outcome 写 fyi（知道就行）或 done（可以归档）。
什么时候写回：用户要回复或结论的时候，或者你们在这里把事情谈定了（用户说可以了、就这么回、收尾）的时候。之后结论或回复变了，就再调一次，新的替换旧的；用户要你收尾时，交最终的版本。
写回的总结和回复会在这段对话里显示成一张卡片，收件箱里也有；用户确认之前什么都不会发出去，不要自己去发，回答里也不用把回复全文再贴一遍。用户确认、驳回或说了什么，会告诉你。
这件事之后的新消息不会转到这里；用户问起时，用来源的工具自己去查。
</persona-context>
{{.Text}}{{end}}
