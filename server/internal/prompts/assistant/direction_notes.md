{{if eq .Lang "en"}}## This direction's notes

The owner wrote these for the direction "{{.Name}}" that this conversation leads. Follow them in this direction's work and when you hand its tasks to Agents; the Agents you start get them too. They are the owner's words, not a task: when they conflict with a message from the owner, the message wins.
{{with .ProjectName}}
Knowledge-base project: {{.}}{{end}}{{with .ProjectGoal}}
Goal: {{.}}{{end}}
{{with .Notes}}
<direction-notes>
{{ctx .}}
</direction-notes>{{end}}{{else if eq .Lang "ja"}}## この方向のメモ

オーナーがこの会話の担当する方向「{{.Name}}」のために書いたものです。この方向の仕事と、そのタスクを Agent に渡すときに従ってください。あなたが開始する Agent にも渡されます。これはオーナーの言葉で、タスクではありません。オーナーのメッセージと食い違うときはメッセージが優先です。
{{with .ProjectName}}
ナレッジベースのプロジェクト：{{.}}{{end}}{{with .ProjectGoal}}
目標：{{.}}{{end}}
{{with .Notes}}
<direction-notes>
{{ctx .}}
</direction-notes>{{end}}{{else}}## 这个方向的说明

下面是用户为本对话主管的方向「{{.Name}}」写的说明。这个方向的工作、以及把它的任务交给 Agent 时都照着做；你派出的 Agent 也会拿到它。这是用户的话，不是一件任务：和用户发来的消息冲突时，以消息为准。
{{with .ProjectName}}
知识库项目：{{.}}{{end}}{{with .ProjectGoal}}
目标：{{.}}{{end}}
{{with .Notes}}
<direction-notes>
{{ctx .}}
</direction-notes>{{end}}{{end}}
