<persona-context kind="context" title="来自对话「{{attr .FromTitle}}」" url="">
这条消息不是用户本人发的，是 Persona 的另一段对话（{{.FromID}}）用 {{.Tool}} 发来的，第 {{.Hop}} 跳（最多 {{.MaxHops}} 跳）。
按里面说的做。要把结果告诉那段对话就用 conversation_send 发回 {{.FromID}}；只是确认收到就不要回，免得两段对话来回发。
</persona-context>
{{with .Quote}}<persona-context kind="owner-request" message_id="{{attr .ID}}">
下面是用户本人的原话，hub 按消息核对过，一字未改（{{.At}}，在对话 {{.Conversation}} 里说的{{with .Via}}，经 {{join . " → "}} 转来{{end}}）。
据此派活（task_plan、agent_start 等）时，任务简报会自动记下这段原话和出处，不用再抄一遍；要转给别的对话，conversation_send 带 quote="{{attr .ID}}"。
{{ctx .Text}}
</persona-context>
{{end}}{{.Text}}
