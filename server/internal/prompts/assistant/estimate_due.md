{{if eq .Language "English"}}The automatic estimate reached its review point: expected {{.Expected}} minutes; {{.Elapsed}} minutes have elapsed since dispatch or the last estimate change. This is a review cue, not an overdue judgment.
{{else if or (eq .Language "Japanese") (eq .Language "日本語")}}自動見積もりのレビュー時点になりました：見積もりは {{.Expected}} 分、派工または前回の見積もり変更から {{.Elapsed}} 分が経過しています。これは遅延判定ではなく、進捗と結果を確認する合図です。
{{else}}到了系统预估的验收点：预估 {{.Expected}} 分钟，从派工或上次调整预估起已过 {{.Elapsed}} 分钟。这只是验收提醒，不是超期判定。
{{end}}
