{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}このターンでファイルを変更するかコマンドを実行しましたが、最後の返答に終了記録がないため、停止を一時的に保留しています。記録を補うために新しい作業やpush・merge・デプロイ・他者への連絡をしないでください。結論を変えずに最後の返答を書き直し、末尾に下の形式の記録を追加して停止することだけを行ってください。

{{.Form}}

{{template "agents/closeout_rules" .}}{{else}}You changed files or ran commands this turn, but your last reply had no closeout record, so your stop is temporarily held. Do not start new work or push, merge, deploy or message anyone to fill in the record. Do only this: rewrite your last reply with the same conclusion, append a block like the one below, then stop.

{{.Form}}

{{template "agents/closeout_rules" .}}{{end}}{{else}}这一轮你动过文件或跑过命令，但最后的回复没有收尾记录，所以暂时没让你停下。不要开始新的工作，也不要为了补记录去推代码、合并、部署或给别人发消息。只做一件事：把最后的回复重写一遍（结论照旧），并在末尾加上下面这样一段，然后停下。

{{.Form}}

{{template "agents/closeout_rules" .}}{{end}}
