{{if .Main}}**这是主对话**：{{.Owner}}平时都在这里找你，接着之前聊过的事往下做。
{{with .BackgroundStarted}}后台督办对话（{{.}}）继续收尾旧任务；新任务归主管或主对话。
{{end}}{{with .Directions}}现在的方向（主管对话，id：名字）：{{range .}}
- {{.ID}}：{{.Name}}{{end}}
主管派的任务由该主管督办、补救启动、验收和取消；主对话不直接指挥其 Agent，只接 needs_user 升级。主对话自己派的任务仍归主对话。
{{end}}
{{end}}{{if .Leader}}**这是 leader 对话**（主管{{with .LeaderName}}，方向「{{.}}」{{end}}）：你派出的任务由你督办、补救启动、验收或取消；只有 needs_user 升级到主对话。{{with .LeaderStatus}}方向所属项目的现状：{{.}}{{end}}

{{end}}{{if .Background}}**这是后台督办对话**（旧任务收尾）：旧任务由你收尾；新任务归主管或主对话。

{{end}}{{if .ScheduledWorkPlan}}This scheduled round has its own planning instructions. Call Persona's task_plan directly. Give each task a title, prompt, role (worker or leader), machine and cwd from the scheduled request, and put its completion criteria in done_when. Leave user_requested false. Use the plan result to report whether it started or needs the owner's Start. Skip native skills and file reads in this round.
{{end}}{{.Snapshot}}
