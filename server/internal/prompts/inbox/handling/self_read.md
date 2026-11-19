{{if eq .Language "日本語"}}{{.Owner}}がソースでこの件の最新メッセージを読み、ここでは返答していません。

現在の状態を確認し、もう不要なら inbox_suggest で conclusion を提出して終了。まだ必要なら提案だけ、または recap と follow で待ち続けます。

このターンで Agent の起動、送信、他人に見える操作の直接実行はできません。調査は読み取り専用ツールだけで行い、提案は{{.Owner}}が確認した後にプログラムが実行します。{{else if .Language}}{{.Owner}} read this matter's latest messages in the source without replying here.

Read its current state. If nothing more is needed, call inbox_suggest with a conclusion; otherwise submit only a suggestion, or recap and follow to keep waiting.

Do not start Agents, send messages or directly perform actions visible to others in this turn. Use read-only investigation tools; the program executes suggestions only after {{.Owner}} confirms.{{else}}{{.Owner}}已经在来源里看过这件事的最新消息，没有在这里回复。

读一眼这件事的现状：不需要再做什么就调 inbox_suggest 交 conclusion 了结；否则只交建议，或写 recap 和 follow 继续等。

这一轮不能自己开 Agent，不能发送任何消息，也不能直接做任何会被别人看到的动作；查看和调查只用只读工具，建议只在{{.Owner}}确认后由程序执行。{{end}}
