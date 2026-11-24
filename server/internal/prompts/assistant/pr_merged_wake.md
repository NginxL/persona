{{- if eq .Language "English"}}Delivery PR {{.PRURL}} has merged{{with .OtherPRs}} (along with {{join . ", "}}){{end}} and no other delivery PR remains open. Verify done_when independently: call task_review(accepted) if only PR merge remained, or continue if post-merge steps (deploy, verification, or follow-up) still remain.
{{- else if or (eq .Language "Japanese") (eq .Language "日本語")}}交付 PR {{.PRURL}} がマージされ{{with .OtherPRs}}（あわせて {{join . "、"}}）{{end}}、他に未完了の交付 PR は残っていません。done_when を独立して確認し、マージのみが残っていた場合は task_review(accepted) で承認・クローズし、マージ後の手順（デプロイや検証など）が残っている場合はそのまま継続してください。
{{- else}}交付 PR {{.PRURL}} 已合并{{with .OtherPRs}}（其余交付 PR：{{join . "、"}}）{{end}}，且已无其他未关闭的交付 PR。请独立核对 done_when：若只差 PR 合并，请调用 task_review(accepted) 验收关单；若仍有合入后步骤（如部署、线上验证等），请继续推进。
{{- end}}
