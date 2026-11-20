{{if .Handled}}Owner reply in {{.Machine}}/{{.Session}}: {{printf "%q" .Text}}. {{with .Closeout}}Agent closeout: {{printf "%q" .}}. {{end}}Owner ask withdrawn. Verify the task's result or clarify; record only an explicit choice.
{{else if eq .Language "English"}}Owner wrote in {{.Machine}}/{{.Session}}; default paused. Read with `agent_read`, then confirm via `task_decided` or clarify.
{{else if eq .Language "Japanese"}}ユーザーが {{.Machine}}/{{.Session}} に発言。既定の実行は停止中。`agent_read` で確認し、`task_decided` または質問で確定する。
{{else}}用户已在 {{.Machine}}/{{.Session}} 发话，默认执行已暂停；用 `agent_read` 核对，再 `task_decided` 或澄清。
{{end}}
