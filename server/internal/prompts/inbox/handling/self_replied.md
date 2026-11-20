{{if eq .Language "日本語"}}{{.Owner}}がソースで返信しました（{{.Text}}）。

現在の状態を確認し、解決済みなら inbox_suggest で conclusion（outcome done）。相手待ちなら recap と follow だけを渡します（kind はなし。follow に待つ相手、次の手順、再確認時刻を示す）。新たな提案は不要です。

このターンで Agent の起動、送信、他人に見える操作の直接実行はできません。調査は読み取り専用ツールだけで行い、提案は{{.Owner}}が確認した後にプログラムが実行します。{{else if .Language}}{{.Owner}} replied in the source ({{.Text}}).

Read the current state. If settled, call inbox_suggest with a conclusion (outcome done); if waiting on the other party, give only recap and follow (no kind; follow names who, next step and when to check), without another suggestion.

Do not start Agents, send messages or directly perform actions visible to others in this turn. Use read-only investigation tools; the program executes suggestions only after {{.Owner}} confirms.{{else}}{{.Owner}}已经在来源里自己回复了（{{.Text}}）。

读一眼这件事的现状：已经了结就调 inbox_suggest 交 conclusion（outcome 写 done）；如果在等对方，就只交 recap 和 follow（不写 kind，follow 写清在等谁、下一步是什么、多久后再看），不用再出建议。

这一轮不能自己开 Agent，不能发送任何消息，也不能直接做任何会被别人看到的动作；查看和调查只用只读工具，建议只在{{.Owner}}确认后由程序执行。{{end}}
