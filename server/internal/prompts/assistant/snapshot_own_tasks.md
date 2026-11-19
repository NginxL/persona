{{if or .Tasks .OpenTodosLoaded}}- 这个对话派出或督办的任务（别的对话派的任务和个人待办不列在这里，要看用 todo_list；最近结束的也会列出；问已有结果看这里，不够用 agent_read 读它的对话；要它新查、新做或改方向才用 agent_send）：{{if not .Tasks}}没有
{{else if .Omit.Tasks}}{{len .Tasks}} 个，{{template "assistant/snapshot_omitted" "todo_list"}}
{{else}}
{{range .Tasks}}{{template "assistant/snapshot_task" .}}
{{end}}{{end}}{{end}}{{with .OpenTodos}}- 这个对话派出或督办的其他开放任务（{{if $.Omit.OpenTodos}}仅列 ID{{else}}列出 ID 和标题{{end}}）：{{range $i, $t := .}}{{if $i}}；{{end}}{{$t.ID}}{{if not $.Omit.OpenTodos}}「{{$t.Title}}」{{with $t.Link}}（{{.}}）{{end}}{{end}}{{if $t.Failed}}（没开起来）{{end}}{{end}}
{{end}}
