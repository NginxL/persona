{{if eq .Language "日本語"}}{{.Owner}}が確認し、送信されました{{if .Edited}}（{{.Owner}}が編集。送った本文は以下）{{end}}：
{{.Text}}

{{if .Handoff}}その後の相手の返信はここに転送されません。追加の提案は不要です。{{else}}相手の返信を待ちます。相手、次の手順、再確認時刻が分かれば inbox_suggest に recap と follow（who、next、check_in または check_at）だけを渡します。who が空なら相手の返信待ちです。不明なら recap だけで要約を更新し、新しい提案は不要。返事が来たらここに届きます。{{end}}{{else if .Language}}{{.Owner}} confirmed and it was sent{{if .Edited}} ({{.Owner}} edited the text; the sent text follows){{end}}:
{{.Text}}

{{if .Handoff}}Later replies will not be forwarded here. No further suggestion is needed.{{else}}Wait for the other party. If you know who, the next step and when to check, call inbox_suggest with only recap and follow (who, next, check_in or check_at); empty who means waiting for the other party's reply. Otherwise give recap only; no new suggestion. Their reply will be forwarded here.{{end}}{{else}}{{.Owner}}确认了，已经发出{{if .Edited}}（{{.Owner}}改过文字，发出去的是下面这段）{{end}}：
{{.Text}}

{{if .Handoff}}对方之后的回复不会转到这里，不用再交建议。{{else}}现在等对方回。知道在等谁、下一步、多久后再看时，调 inbox_suggest 只给 recap 和 follow（who、next、check_in，或 check_at）；who 空着就是等对方回复。不知道就只给 recap 更新总结，不用再出建议；对方回了会发到这里。{{end}}{{end}}
