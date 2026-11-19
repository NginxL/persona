{{if eq .Language "日本語"}}<persona-context kind="context" title="ToDo · {{attr .Title}} · 有効な決定事項" url="">
この作業ですでに決まっている事項（現在有効なもののみ、古い順）です。これらに従い、同じ質問を繰り返さないでください。変更する場合は先にユーザーに確認してください。
{{range .Decisions}}
{{.N}}. {{.Question}}
   選択：{{.Choice}}{{if .Reason}}
   理由：{{.Reason}}{{end}}
   （{{.When}}{{if eq .Source "outcome"}}、完了時に Agent が記録したもので、ユーザーの決定ではない{{else if eq .Source "earlier-outcome"}}、以前の段階の記録であり、現在の ToDo の完了やユーザーの決定を示すものではない{{else if eq .Source "agent"}}、作業中に Agent が記録したもので、ユーザーの決定ではない{{else if eq .Source "thread"}}、Feishu のトピックでのユーザーの返信{{else if eq .Source "card"}}、{{if .Asker}}「{{.Asker}}」{{else}}Agent{{end}}の質問に対するユーザーの回答{{end}}{{if .Ancestor}}、先行タスク「{{.Ancestor}}」{{end}}）
{{end}}</persona-context>{{else if .Language}}<persona-context kind="context" title="To-do · {{attr .Title}} · Decisions in effect" url="">
Decisions already settled for this work (only those in effect, oldest first). Follow them and do not ask the same questions again. Ask the user before changing any of them.
{{range .Decisions}}
{{.N}}. {{.Question}}
   Chose: {{.Choice}}{{if .Reason}}
   Reason: {{.Reason}}{{end}}
   ({{.When}}{{if eq .Source "outcome"}}, recorded by the Agent at completion, not decided by the user{{else if eq .Source "earlier-outcome"}}, recorded in an earlier phase; this neither marks the current to-do complete nor represents a user decision{{else if eq .Source "agent"}}, recorded by the Agent during the work, not decided by the user{{else if eq .Source "thread"}}, the user's reply in the Feishu topic{{else if eq .Source "card"}}, the user's answer to a question from {{if .Asker}}“{{.Asker}}”{{else}}an Agent{{end}}{{end}}{{if .Ancestor}}; earlier task “{{.Ancestor}}”{{end}})
{{end}}</persona-context>{{else}}<persona-context kind="context" title="待办 · {{attr .Title}} · 已定的决策" url="">
这件事上用户已经拍过板的决定（只列现在生效的，按时间先后）。照这些做，不要再问同样的问题；要改其中哪一条，先问用户。
{{range .Decisions}}
{{.N}}. {{.Question}}
   选了：{{.Choice}}{{if .Reason}}
   原因：{{.Reason}}{{end}}
   （{{.When}}{{if eq .Source "outcome"}}，Agent 做完时记的，不是用户定的{{else if eq .Source "earlier-outcome"}}，前一阶段的记录，不代表待办当前已完成，也不是用户定的{{else if eq .Source "agent"}}，Agent 干活时记的，不是用户定的{{else if eq .Source "thread"}}，用户在飞书话题里回的{{else if eq .Source "card"}}，{{if .Asker}}在「{{.Asker}}」{{else}}在 Agent {{end}}的提问里答的{{end}}{{if .Ancestor}}{{if .Source}}；{{else}}，{{end}}前序任务「{{.Ancestor}}」{{end}}）
{{end}}</persona-context>{{end}}
