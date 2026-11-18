{{- /* Part of voice/report: what sort of model error a turn ended on (.Failure), said instead of the error's text (it
may say what the service runs on); without one, why there is no result (.Reason). One sentence that ends the clause it
is in. Data: VoiceReport. */ -}}
{{- if eq .Lang "en" -}}
{{if eq .Failure "not_logged_in"}}it is not signed in on that computer.{{else if eq .Failure "auth_failed"}}the model's key is wrong or has expired.{{else if eq .Failure "model_route"}}the model it uses cannot be used now.{{else if eq .Failure "rate_limited"}}the model was used too fast and hit its limit.{{else if eq .Failure "transient"}}the model service failed for a moment; telling it to go on may work.{{else if eq .Failure "retried"}}the model service failed, and going on did not help.{{else if eq .Failure "no_output"}}the model service answered nothing; telling it to go on may work.{{else if .Failure}}the model service failed.{{else}}{{.Reason}}{{end}}
{{- else if eq .Lang "ja" -}}
{{if eq .Failure "not_logged_in"}}そのパソコンでログインしていません。{{else if eq .Failure "auth_failed"}}モデルのキーが間違っているか期限切れです。{{else if eq .Failure "model_route"}}使っているモデルがいま使えません。{{else if eq .Failure "rate_limited"}}モデルの利用が速すぎて上限に達しました。{{else if eq .Failure "transient"}}モデルのサービスで一時的なエラーが起きました。続けるよう頼めば直るかもしれません。{{else if eq .Failure "retried"}}モデルのサービスでエラーが起き、続けても直りませんでした。{{else if eq .Failure "no_output"}}モデルのサービスが何も返しませんでした。続けるよう頼めば直るかもしれません。{{else if .Failure}}モデルのサービスでエラーが起きました。{{else}}{{.Reason}}{{end}}
{{- else -}}
{{if eq .Failure "not_logged_in"}}它所在的电脑上没登录。{{else if eq .Failure "auth_failed"}}模型的 Key 不对或过期了。{{else if eq .Failure "model_route"}}它用的模型现在用不了。{{else if eq .Failure "rate_limited"}}模型用得太快，到了限额。{{else if eq .Failure "transient"}}模型服务临时出了问题，让它接着做也许就好了。{{else if eq .Failure "retried"}}模型服务出了问题，接着做也没好。{{else if eq .Failure "no_output"}}模型服务什么都没返回，让它接着做也许就好了。{{else if .Failure}}模型服务出错了。{{else}}{{.Reason}}{{end}}
{{- end -}}
