{{- if eq .Kind "auth"}}
{{- if eq .Language "English"}}The background supervisor hit a tool permission denial (assistant_denied: {{.Tool}}). Background retries are paused and this is handed to the main conversation.
{{- else if or (eq .Language "Japanese") (eq .Language "日本語")}}バックグラウンドの監督でツールの権限拒否が発生しました（assistant_denied: {{.Tool}}）。バックグラウンドでの再試行を停止し、メイン会話に引き継ぎます。
{{- else}}后台督办触发工具权限拒绝（assistant_denied: {{.Tool}}），已暂停后台重试并转交主对话处理。
{{- end}}
{{- else}}
{{- if eq .Language "English"}}The background supervisor made no progress on the all_stopped signal for {{.Rounds}} rounds in a row ({{.Reason}}). The circuit breaker paused its retries and this is handed to the main conversation.
{{- else if or (eq .Language "Japanese") (eq .Language "日本語")}}バックグラウンドの監督は all_stopped シグナルで {{.Rounds}} ラウンド連続して進展がありませんでした（{{.Reason}}）。サーキットブレーカーで再試行を停止し、メイン会話に引き継ぎます。
{{- else}}后台督办在 all_stopped 信号上连续 {{.Rounds}} 轮无进展（{{.Reason}}），已熔断暂停重试并转交主对话处理。
{{- end}}
{{- end -}}
