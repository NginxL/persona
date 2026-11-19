{{if eq .Language "日本語"}}<persona-context kind="context" title="プロジェクト · {{attr .Name}} · 背景" url="">
作業ディレクトリから、このタスクはこのプロジェクトに属します。まず背景を確認してください。以下は同じプロジェクトの過去のタスクが完了したときの記録です。作業を続ける際はその内容に従い、同じ問題を避けてください。
{{with .OneLine}}
プロジェクト：{{.}}
{{end}}{{if .Outcomes}}
最近完了したタスク（新しい順）：
{{range .Outcomes}}- {{.When}} 「{{.Title}}」{{.Text}}
{{end}}{{end}}{{range .Extensions}}
拡張機能「{{ctx .Name}}」がまとめたプロジェクトの背景：
{{ctx .Text}}
{{end}}</persona-context>{{else if .Language}}<persona-context kind="context" title="Project · {{attr .Name}} · Background" url="">
This task belongs to this project, based on your working directory. Read the background first. These outcomes were recorded when earlier tasks in this project finished. Follow them and avoid the same pitfalls.
{{with .OneLine}}
Project: {{.}}
{{end}}{{if .Outcomes}}
Recent finished tasks (newest first):
{{range .Outcomes}}- {{.When}} “{{.Title}}” {{.Text}}
{{end}}{{end}}{{range .Extensions}}
Project background from extension “{{ctx .Name}}”:
{{ctx .Text}}
{{end}}</persona-context>{{else}}<persona-context kind="context" title="项目 · {{attr .Name}} · 背景" url="">
这件事属于这个项目（按你的工作目录找到的）。先看背景；下面的结论是之前同项目的任务做完时记下的，接着做的时候照着来，别再踩同样的坑。
{{with .OneLine}}
项目：{{.}}
{{end}}{{if .Outcomes}}
最近几件做完的事（新的在前）：
{{range .Outcomes}}- {{.When}} 「{{.Title}}」{{.Text}}
{{end}}{{end}}{{range .Extensions}}
扩展「{{ctx .Name}}」为这个项目整理的背景：
{{ctx .Text}}
{{end}}</persona-context>{{end}}
