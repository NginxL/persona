🔔 任务「{{.Title}}」{{if eq .State "needs"}}在等用户{{else if eq .State "interrupted"}}被模型服务出错打断了{{else if eq .State "failed"}}没开起来{{else}}做完了{{end}}{{if .Machine}}（{{.Machine}}）{{end}}{{if .Earlier}}
（这期间它还{{range $i, $e := .Earlier}}{{if $i}}、{{end}}{{$e}}{{end}}，以这条为准。）{{end}}
（这是 Persona 的任务通知，不是用户发的消息。{{if .Reporting}}这个任务是从这段对话派出去的，它的结果只通知到这里：手机不会为它另外推送，要靠你按这段对话一开始的说明交代出去。{{else}}这个任务是从主对话派出去的，用户设置了这类任务只在主对话里通知：手机不会为它另外推送，灵动岛上也没有，要靠你告诉用户。{{end}}）
{{if .Ask}}
它在问：
{{.Ask}}
{{end}}{{if .Text}}
{{if eq .State "failed"}}原因{{else if eq .State "interrupted"}}模型服务回的错{{else}}Agent 最后说的{{end}}：
{{.Text}}
{{end}}{{with .Files}}
它交回的文件（已经放到你这台电脑上：回复里直接给文件名和反引号路径，要内容就用你自己的文件工具读，不要让 Agent 再贴一遍）：
{{range .}}- {{.Name}}{{if .Error}}（没取回：{{.Error}}；在原电脑 {{.Path}}）{{else}}：{{.Path}}{{end}}
{{end}}{{end}}
{{if .Reporting}}你来决定要不要交代、怎么说：照这段对话一开始说的方式交代（比如它要你用某个工具回复，就用那个工具）。{{if eq .State "done"}}做完了就说结果（改了什么、PR / MR 链接、结论）；{{else}}说清楚卡在哪、要谁做什么（卡片在用户手机上，用户可以在那里处理）；{{end}}只交代有意义的进展和结果，不要复述这条通知，没什么值得说的就不用交代。{{else}}你来决定要不要告诉用户、怎么说：{{if eq .State "needs"}}说清楚它在问什么、要用户怎么回（卡片就在主对话底下，用户点一下就行；用户以前交代过怎么答的，可以用 interaction_answer 代答再告诉用户）。{{else if eq .State "interrupted"}}说清楚在哪一步、什么错，可以点卡片上的「继续」。{{else if eq .State "failed"}}说清楚为什么没开起来，可以点卡片上的「重试」或换一台电脑{{with .RetryWith}}；它的启动器在这台电脑上开不起来，用户也可以让你改用 {{.}} 重试（task_retry 带 launcher）{{end}}。{{else}}一两句话说结果和接下来要用户做什么（看改动、合并、回复谁……）；没什么要用户做的就说一句做完了。{{end}}你的回答会推送到用户手机上，只说用户需要知道的，不要复述这条通知。{{end}}
