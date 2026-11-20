{{if eq .Language "日本語"}}<persona-context kind="context" title="プロジェクト · {{attr .Name}} · 方向メモリー" url="">
このタスクの明示されたプロジェクトと方向の記録です。要約、結果、決定と理由、失敗からの教訓の順に読んでください。agent_reported はスーパーバイザーがまだ承認していない報告であることを意味します。
{{with .Summary}}
プロジェクト要約：
{{ctx .}}
{{end}}{{if .Outcomes}}
最近の結果（新しい順）：
{{range .Outcomes}}- {{ctx .Text}}{{range .Links}} {{ctx .}}{{end}}{{if .When}} · {{ctx .When}}{{end}}{{if .Unaccepted}} · agent_reported · スーパーバイザー未承認{{else if .Verification}} · {{ctx .Verification}}{{end}}
{{end}}{{end}}{{if .Decisions}}
有効な決定と理由（新しい順。異なる ID の矛盾する決定も保持）：
{{range .Decisions}}- [{{ctx .ID}}] {{ctx .Question}} → {{ctx .Choice}}{{if .Reason}}；理由：{{ctx .Reason}}{{end}}{{if .When}} · {{ctx .When}}{{end}}{{if .Unaccepted}} · agent_reported · スーパーバイザー未承認{{else if .Verification}} · {{ctx .Verification}}{{end}}
{{end}}{{end}}{{if .Pitfalls}}
失敗からの教訓（新しい順）：
{{range .Pitfalls}}- {{ctx .Text}}{{if .When}} · {{ctx .When}}{{end}}{{if .Unaccepted}} · agent_reported · スーパーバイザー未承認{{else if .Verification}} · {{ctx .Verification}}{{end}}
{{end}}{{end}}{{range .Extensions}}
拡張機能「{{ctx .Name}}」がまとめたプロジェクトの背景：
{{ctx .Text}}
{{end}}</persona-context>{{else if .Language}}<persona-context kind="context" title="Project · {{attr .Name}} · Direction memory" url="">
This is the record of the task's explicit project and direction. Read the summary, outcomes, decisions with reasons, and pitfalls in that order. agent_reported means the supervisor has not accepted the report.
{{with .Summary}}
Project summary:
{{ctx .}}
{{end}}{{if .Outcomes}}
Recent outcomes (newest first):
{{range .Outcomes}}- {{ctx .Text}}{{range .Links}} {{ctx .}}{{end}}{{if .When}} · {{ctx .When}}{{end}}{{if .Unaccepted}} · agent_reported · not supervisor-accepted{{else if .Verification}} · {{ctx .Verification}}{{end}}
{{end}}{{end}}{{if .Decisions}}
Effective decisions and reasons (newest first; conflicting decisions with distinct IDs are kept):
{{range .Decisions}}- [{{ctx .ID}}] {{ctx .Question}} → {{ctx .Choice}}{{if .Reason}}; reason: {{ctx .Reason}}{{end}}{{if .When}} · {{ctx .When}}{{end}}{{if .Unaccepted}} · agent_reported · not supervisor-accepted{{else if .Verification}} · {{ctx .Verification}}{{end}}
{{end}}{{end}}{{if .Pitfalls}}
Pitfalls (newest first):
{{range .Pitfalls}}- {{ctx .Text}}{{if .When}} · {{ctx .When}}{{end}}{{if .Unaccepted}} · agent_reported · not supervisor-accepted{{else if .Verification}} · {{ctx .Verification}}{{end}}
{{end}}{{end}}{{range .Extensions}}
Project background from extension “{{ctx .Name}}”:
{{ctx .Text}}
{{end}}</persona-context>{{else}}<persona-context kind="context" title="项目 · {{attr .Name}} · 方向记忆" url="">
这是这件事明确所属项目和方向的记录。按顺序先看摘要，再看结果、决定和原因，最后看踩过的坑。agent_reported 表示主管还没有验收。
{{with .Summary}}
项目摘要：
{{ctx .}}
{{end}}{{if .Outcomes}}
最近的结果（新的在前）：
{{range .Outcomes}}- {{ctx .Text}}{{range .Links}} {{ctx .}}{{end}}{{if .When}} · {{ctx .When}}{{end}}{{if .Unaccepted}} · agent_reported · 主管未验收{{else if .Verification}} · {{ctx .Verification}}{{end}}
{{end}}{{end}}{{if .Decisions}}
仍有效的决定和原因（新的在前；不同 ID 的矛盾决定都保留）：
{{range .Decisions}}- [{{ctx .ID}}] {{ctx .Question}} → {{ctx .Choice}}{{if .Reason}}；理由：{{ctx .Reason}}{{end}}{{if .When}} · {{ctx .When}}{{end}}{{if .Unaccepted}} · agent_reported · 主管未验收{{else if .Verification}} · {{ctx .Verification}}{{end}}
{{end}}{{end}}{{if .Pitfalls}}
踩过的坑（新的在前）：
{{range .Pitfalls}}- {{ctx .Text}}{{if .When}} · {{ctx .When}}{{end}}{{if .Unaccepted}} · agent_reported · 主管未验收{{else if .Verification}} · {{ctx .Verification}}{{end}}
{{end}}{{end}}{{range .Extensions}}
扩展「{{ctx .Name}}」为这个项目整理的背景：
{{ctx .Text}}
{{end}}</persona-context>{{end}}
