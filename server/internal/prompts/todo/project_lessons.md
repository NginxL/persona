{{if eq .Language "日本語"}}<persona-context kind="context" title="プロジェクト · {{attr .Name}} · 教訓" url="">
このプロジェクトの承認済みタスクから学んだ教訓です（繰り返しの多い順）。既知の遠回りを避けるのに使ってください。このタスクの指示と食い違うときは、指示を優先します。
{{range .Lessons}}- {{if eq .Kind "repeated_mistake"}}繰り返したミス{{else if eq .Kind "missing_context"}}知っておくべき背景{{else if eq .Kind "wasted_step"}}時間を無駄にした手順{{else}}よりよい進め方{{end}}：{{ctx .Text}}{{if gt .Seen 1}} · {{.Seen}} 回{{end}}{{with .When}} · {{ctx .}}{{end}}
{{end}}</persona-context>{{else if .Language}}<persona-context kind="context" title="Project · {{attr .Name}} · Lessons" url="">
Lessons learned from this project's accepted tasks, the most repeated first. Use them to avoid known detours; where one differs from this task's instructions, the instructions win.
{{range .Lessons}}- {{if eq .Kind "repeated_mistake"}}Mistake that came back{{else if eq .Kind "missing_context"}}Background to know{{else if eq .Kind "wasted_step"}}Step that wasted time{{else}}Better way to work{{end}}: {{ctx .Text}}{{if gt .Seen 1}} · {{.Seen}} times{{end}}{{with .When}} · {{ctx .}}{{end}}
{{end}}</persona-context>{{else}}<persona-context kind="context" title="项目 · {{attr .Name}} · 经验" url="">
这是这个项目之前验收过的任务里学到的经验（重复次数多的在前）。用它们少走已知的弯路；和这次任务的要求不一致时，以这次的要求为准。
{{range .Lessons}}- {{if eq .Kind "repeated_mistake"}}反复出现的错误{{else if eq .Kind "missing_context"}}该知道的背景{{else if eq .Kind "wasted_step"}}浪费时间的步骤{{else}}更好的做法{{end}}：{{ctx .Text}}{{if gt .Seen 1}} · {{.Seen}} 次{{end}}{{with .When}} · {{ctx .}}{{end}}
{{end}}</persona-context>{{end}}
