{{if eq .Lang "en"}}<persona-context kind="context" title="Direction · {{attr .Name}} · notes" url="">
This work belongs to the direction "{{.Name}}". Its owner wrote the notes below for every task in it: follow them. When they conflict with your task's own instructions, the task wins.
{{with .ProjectName}}
Knowledge-base project: {{ctx .}}{{end}}{{with .ProjectGoal}}
Goal: {{ctx .}}{{end}}

{{ctx .Notes}}
</persona-context>{{else if eq .Lang "ja"}}<persona-context kind="context" title="方向 · {{attr .Name}} · メモ" url="">
この仕事は方向「{{.Name}}」に属します。オーナーがこの方向のすべてのタスク向けに書いたメモです。従ってください。タスク自体の指示と食い違うときはタスクが優先です。
{{with .ProjectName}}
ナレッジベースのプロジェクト：{{ctx .}}{{end}}{{with .ProjectGoal}}
目標：{{ctx .}}{{end}}

{{ctx .Notes}}
</persona-context>{{else}}<persona-context kind="context" title="方向 · {{attr .Name}} · 说明" url="">
这件事属于方向「{{.Name}}」。下面是用户给这个方向所有任务写的说明，照着做；和任务本身的要求冲突时，以任务为准。
{{with .ProjectName}}
知识库项目：{{ctx .}}{{end}}{{with .ProjectGoal}}
目标：{{ctx .}}{{end}}

{{ctx .Notes}}
</persona-context>{{end}}
