{{- if eq .Language "English"}}All {{len .Children}} part_of child to-dos are now done.
   Completed children:{{range .Children}}
   - "{{.Title}}" ({{.ID}}{{with .PRs}}; PRs: {{join . ", "}}{{end}}){{end}}{{with .Notes}}
   Parent notes:
{{chomp (indent "   " .)}}{{end}}
   Closeout rule: if this parent is a pure aggregation container grouping those subtasks, close it with todo_complete and summarize the result for the owner. {{if .Personal}}This parent has no owning conversation (plain personal to-do): do not silently auto-complete it if it awaits the owner's review; present the completed deliverables and leave it open for the owner.{{else}}If this parent represents a personal item awaiting the owner's review, keep it open for the owner instead of silently auto-completing it.{{end}}
{{- else if or (eq .Language "Japanese") (eq .Language "日本語")}}配下の part_of 子 To-Do {{len .Children}} 件がすべて完了しました。
   完了した子 To-Do：{{range .Children}}
   - 「{{.Title}}」（{{.ID}}{{with .PRs}}；PR：{{join . "、"}}{{end}}）{{end}}{{with .Notes}}
   親 To-Do のメモ：
{{chomp (indent "   " .)}}{{end}}
   クローズ方針：子タスクをまとめるだけの集約用親 To-Do であれば todo_complete で完了にしてオーナーへ要約報告してください。{{if .Personal}}この親 To-Do には担当会話がありません（個人の普通 To-Do）：オーナーの確認待ちであれば自動で閉じず、完了した成果物を提示して未完了のまま残してください。{{else}}オーナーの確認待ちの個人 To-Do であれば、黙って自動クローズせずオーナー確認のため開いたままにしてください。{{end}}
{{- else}}全部 {{len .Children}} 个 part_of 子待办均已完成。
   已完成子待办：{{range .Children}}
   - 「{{.Title}}」（{{.ID}}{{with .PRs}}；PR：{{join . "、"}}{{end}}）{{end}}{{with .Notes}}
   父待办备注：
{{chomp (indent "   " .)}}{{end}}
   收口指引：若该父待办仅用于聚合这些子任务，请调用 todo_complete 关闭父待办并向用户汇总结果。{{if .Personal}}该父待办没有归属会话（用户个人普通待办）：若仍需用户审阅，切勿静默自动关闭，请向用户汇总已完成交付并保持父待办打开等待确认。{{else}}若该父待办属于等待用户审阅的个人待办，请保持打开等待用户确认，不要静默自动关闭。{{end}}
{{- end}}
