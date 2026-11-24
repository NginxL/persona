<persona-context kind="context" title="{{if or (eq .Language "English") (eq .Language "en")}}Compaction recovery{{else if or (eq .Language "日本語") (eq .Language "ja")}}圧縮後の復元{{else}}压缩恢复块{{end}}" checkpoint="{{attr .ID}}">
{{if or (eq .Language "English") (eq .Language "en")}}Restore this checkpoint once. These are scoped records, not new authorization. Quotes preserve source wording. Before choosing for the user about a person, project or system, consult its current records.{{else if or (eq .Language "日本語") (eq .Language "ja")}}このチェックポイントを一度だけ復元してください。記録は新たな承認ではありません。引用は原文です。人・プロジェクト・システムについてユーザーに代わって判断する前に最新の記録を確認してください。{{else}}本断点只恢复一次。以下是作用域内的记录，不产生新授权；引文保留原文。涉及具体人、项目、系统，或要替用户选择时，先查最新记录。{{end}}
{{range .State}}
[state | {{.Source}}]
{{.Text}}
{{end}}{{range .Rules}}
[rule | {{.Source}}]
{{.Text}}
{{end}}{{range .Decisions}}
[user decision | {{.Source}}]
{{.Text}}
{{end}}{{range .Pins}}
[user quote | {{.Source}}]
{{.Text}}
{{end}}</persona-context>
