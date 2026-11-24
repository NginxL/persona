{{- if eq .Language "English"}}New external information arrived while a needs_user ask is still open (card untouched):
   - New information{{with .Source}} ({{.}}){{end}}: {{.Summary}}
   - Pending needs_user ask: {{.PendingQuestion}}{{with .PendingRecommend}} (recommended: {{.}}){{end}}
   - Evaluate whether this resolves or supersedes the pending ask. If the owner still must decide, call supervision_finish(visibility="silent") so the original card stays untouched. If this resolves or supersedes it, call task_review with the new verdict and a clear natural-language reason to clear the stale card (never fabricate an owner selection with task_decided).
{{- else if or (eq .Language "Japanese") (eq .Language "日本語")}}needs_user の確認カードが保留中のまま、新しい外部情報が届きました（元のカードは未変更）：
   - 新しい情報{{with .Source}}（{{.}}）{{end}}：{{.Summary}}
   - 保留中の needs_user 質問：{{.PendingQuestion}}{{with .PendingRecommend}}（おすすめ：{{.}}）{{end}}
   - この情報が保留中の質問を解消または上書きするかを判断してください。引き続きオーナーの判断が必要なら supervision_finish(visibility="silent") で静かに終了し、元のカードをそのまま残してください。解消または上書きする場合は、task_review に新しい判定と明確な自然言語の理由を渡して古いカードを撤回してください（task_decided でオーナーの選択を捏造しないこと）。
{{- else}}待用户确认（needs_user）的卡片仍在等待时，收到了新的外部信息（原卡片未改动）：
   - 新信息{{with .Source}}（{{.}}）{{end}}：{{.Summary}}
   - 当前待决问题：{{.PendingQuestion}}{{with .PendingRecommend}}（推荐：{{.}}）{{end}}
   - 请判断这条新信息是否已解决或覆盖当前的待决问题。如果仍需用户拍板，请调用 supervision_finish(visibility="silent") 静默结束，原卡片会保持原样；如果已解决或覆盖，请调用 task_review 给出新结论并写明自然语言原因以自动撤掉旧卡片（切勿用 task_decided 伪造用户点击）。
{{- end}}
