{{if eq .Language "日本語"}}この件を再確認する時刻です。{{if .Who}}待っている相手：{{.Who}}{{else}}相手の返信待ち{{end}}{{with .Next}}；次の手順：{{.}}{{end}}。連続 {{.Checks}} 回目の定時確認です。

このターンで Agent を起動、送信、他人に見える操作の直接実行をしてはいけません。調査は読み取り専用ツールだけで行い、提案は{{.Owner}}が確認した後にプログラムが実行します。古い recap だけに頼らず、現在の状態（ソースの新着、関連タスクや Agent の進捗）を読み、inbox_suggest で次の一つだけを選びます。
- 結論がある：kind conclusion。完了なら outcome done、{{.Owner}}が知るだけなら fyi。
- {{.Owner}}の確認が必要な操作がある：kind reply の催促など、一つの提案。
- まだ待つ：kind、text などの提案フィールドを付けず、recap と follow を渡す（title、summary は元の要件どおり）。follow には新しい check_at か check_in を必ず含め、who と next は現在の状態に合わせる。

どれでも recap を更新し、このターンを終えます。{{else if .Language}}It is time to check this matter again. {{if .Who}}Waiting on: {{.Who}}{{else}}Waiting for the other party's reply{{end}}{{with .Next}}; next step: {{.}}{{end}}. This is consecutive check {{.Checks}}.

Do not start Agents, send messages or directly perform any action visible to others in this turn. Use read-only tools for investigation; the program executes suggestions only after {{.Owner}} confirms. First read the current state (new source messages, related task or Agent progress), not just the old recap. Then call inbox_suggest with exactly one of:
- A conclusion exists: kind conclusion, outcome done if finished, fyi if {{.Owner}} only needs to know.
- An action needs {{.Owner}}'s confirmation: one suggestion, such as a kind reply reminder.
- Still waiting: no kind, text or other suggestion fields; give recap and follow (title and summary still follow the original requirements). follow must include a new check_at or check_in, with who and next matching the current state.

Always update recap, then end this turn.{{else}}到了再看这件事的时间。{{if .Who}}现在在等：{{.Who}}{{else}}现在在等对方回复{{end}}{{with .Next}}；下一步：{{.}}{{end}}。这是连续第 {{.Checks}} 次到点回看。

这一轮不能自己开 Agent，不能发送任何消息，也不能直接做任何会被别人看到的动作；查看和调查只用只读工具，建议只在{{.Owner}}确认后由程序执行。先读这件事现在的状态（来源里的新消息、相关任务或 Agent 的进展），不要只照旧 recap 判断。然后调 inbox_suggest，下面三种结果只选一个：
- 已经有结论：交 kind conclusion；事情处理完了用 outcome done，只需让{{.Owner}}知道用 fyi。
- 需要{{.Owner}}确认一个动作：交一条建议，比如 kind reply 的催促。
- 还要继续等：不带 kind、text 等建议字段，给 recap 和 follow（title、summary 仍按原要求写）；follow 必须带新的 check_at 或 check_in，who、next 按现在的情况写。

无论选哪一种都更新 recap，然后结束这一轮。{{end}}
