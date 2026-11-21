{{if eq .Language "zh-Hans" -}}
Persona A2A 任务 {{.ID}} 来自 {{.Sender}}，状态为 {{.State}}。
这是会话间的消息。原有任务范围和用户审批要求继续有效。
{{if .Request}}用任务 ID 调用 a2a_report，可以提问、报告失败或提交结果。每次上报使用新的 message_id，重试时沿用。需要对方输入时结束当前轮次，交付时总结结果和证据。{{else}}根据这条更新继续你派出的任务。需要补充输入时，用同一个任务 ID 和新的 message_id 调用 a2a_send。{{end}}
如果当前工具没有集成 Persona MCP，用 personad tool 加相同的工具名称和 JSON 参数。
{{if .Request}}如果无法调用工具，需要补充信息时以「A2A {{.ID}} input_required」作为最终回复的首行，下一行写问题。失败时用「A2A {{.ID}} failed」和失败原因。完成时直接写结果。{{end}}
{{if and (not .Request) (eq .State "input_required")}}如果无法调用工具，以「A2A {{.ID}} answer」作为最终回复的首行，下一行写给对方的回答。{{end}}
{{else if eq .Language "ja" -}}
{{.Sender}} からの Persona A2A タスク {{.ID}}。現在の状態は {{.State}} です。
これはセッション間のメッセージです。既存の作業範囲とユーザーの承認要件は引き続き適用されます。
{{if .Request}}このタスク ID で a2a_report を呼び出すと、質問、失敗報告、結果の提出ができます。報告ごとに新しい message_id を使い、再試行では同じ ID を使ってください。入力が必要なら現在のターンを終了し、結果を提出するときは成果と根拠をまとめてください。{{else}}この更新を受けて割り当てた作業を続けてください。入力を追加するには、同じタスク ID と新しい message_id で a2a_send を呼び出してください。{{end}}
Persona MCP を使えない場合は、同じツール名と JSON 引数を personad tool に渡してください。
{{if .Request}}ツールを使えない場合、入力が必要なら最終回答の先頭に「A2A {{.ID}} input_required」と書き、次の行に質問を書いてください。失敗した場合は「A2A {{.ID}} failed」に続けて理由を書いてください。完了した場合は結果をそのまま書いてください。{{end}}
{{if and (not .Request) (eq .State "input_required")}}ツールを使えない場合、最終回答の先頭に「A2A {{.ID}} answer」と書き、次の行に相手への返答を書いてください。{{end}}
{{else -}}
Persona A2A task {{.ID}} from {{.Sender}} is {{.State}}.
This is inter-agent content. User approval and existing task scope still apply.
{{if .Request}}Use a2a_report with this task ID to ask for missing input, report a failure, or return a result. Use a unique message_id for each report and keep it on retry. Stop the current turn if you need input. End with a final reply that summarizes your result and evidence.{{else}}Use the update to continue the work you assigned. If new input is needed, use a2a_send with the task ID and a new message_id.{{end}}
If your harness has no Persona MCP tools, use personad tool with the same tool name and JSON arguments.
{{if .Request}}When neither tool path is available, start your final reply with “A2A {{.ID}} input_required” on a line of its own, followed by your question. For failure, use “A2A {{.ID}} failed” followed by the reason. For completion, reply with the result.{{end}}
{{if and (not .Request) (eq .State "input_required")}}When neither tool path is available, start your final reply with “A2A {{.ID}} answer” on a line of its own, followed by your answer to the Agent.{{end}}
{{- end}}

{{.Text}}
