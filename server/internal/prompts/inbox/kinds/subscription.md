{{if eq .Language "日本語"}}{{if eq .Kind "sub_mail"}}購読「{{.Label}}」が定期取得したメール
{{- else if eq .Kind "sub_message"}}購読「{{.Label}}」が定期取得したチャットメッセージ
{{- else if eq .Kind "sub_notice"}}購読「{{.Label}}」が定期取得したお知らせ・通知。多くは参考情報で、申込、確認、支払などが必要なら要対応
{{- else if eq .Kind "sub_deadline"}}購読「{{.Label}}」が定期取得した締切事項（課題、請求、申請など）。未完了で締切が近ければ要対応
{{- else if eq .Kind "sub_event"}}購読「{{.Label}}」が定期取得した予定・イベント。招待の返答が必要か、開始間近なら要対応
{{- else if eq .Kind "sub_update"}}購読「{{.Label}}」が取得した状態変化（残高、保有、注文状況などの最新スナップショット）。本人のルールに閾値があれば従い、なければ参考情報
{{- else if eq .Kind "sub_digest"}}購読「{{.Label}}」の有効化時に取得した前日分のまとめ。一行一件で、要約に対応が必要な項目を示す
{{- else if eq .Kind "sub_failing"}}Persona の通知：購読「{{.Label}}」の取得が丸一日失敗している
{{- else}}購読「{{.Label}}」が定期取得した項目{{end}}{{else if .Language}}{{if eq .Kind "sub_mail"}}Email fetched periodically by subscription “{{.Label}}”
{{- else if eq .Kind "sub_message"}}Chat messages fetched periodically by subscription “{{.Label}}”
{{- else if eq .Kind "sub_notice"}}Announcements / notices fetched periodically by subscription “{{.Label}}”, usually FYI; needs the user for registration, confirmation, payment, etc.
{{- else if eq .Kind "sub_deadline"}}Deadline items fetched periodically by subscription “{{.Label}}” (assignments, bills, applications, etc.); unfinished items near their deadline need the user
{{- else if eq .Kind "sub_event"}}Calendar / event items fetched periodically by subscription “{{.Label}}”; needs the user to answer an invitation or when about to start
{{- else if eq .Kind "sub_update"}}State changes fetched by subscription “{{.Label}}” (latest balance, holdings, order status, etc.); apply thresholds in user rules if any, otherwise FYI
{{- else if eq .Kind "sub_digest"}}Previous day's items fetched when subscription “{{.Label}}” was enabled, one digest with one line per item; identify which need the user in the summary
{{- else if eq .Kind "sub_failing"}}Persona reminder: subscription “{{.Label}}” has failed to fetch for a full day
{{- else}}Items fetched periodically by subscription “{{.Label}}”{{end}}{{else}}{{if eq .Kind "sub_mail"}}订阅「{{.Label}}」定时拉来的邮件
{{- else if eq .Kind "sub_message"}}订阅「{{.Label}}」定时拉来的聊天消息
{{- else if eq .Kind "sub_notice"}}订阅「{{.Label}}」定时拉来的公告 / 通知，多数知悉即可；要用户报名、确认、付款等才需要用户
{{- else if eq .Kind "sub_deadline"}}订阅「{{.Label}}」定时拉来的截止事项（作业、账单、申请等），临近截止、还没完成的需要用户
{{- else if eq .Kind "sub_event"}}订阅「{{.Label}}」定时拉来的日程 / 活动；要用户回复邀请或临近开始的需要用户
{{- else if eq .Kind "sub_update"}}订阅「{{.Label}}」拉到的状态变化（余额、持仓、订单状态等的最新快照）；用户的规则给了阈值时按规则判断，否则知悉即可
{{- else if eq .Kind "sub_digest"}}订阅「{{.Label}}」刚打开时补拉的前一天的条目，合成一条汇总，每行一条；摘要里点出哪几条需要用户
{{- else if eq .Kind "sub_failing"}}Persona 的提醒：订阅「{{.Label}}」连续一天拉取失败
{{- else}}订阅「{{.Label}}」定时拉来的条目{{end}}{{end}}
