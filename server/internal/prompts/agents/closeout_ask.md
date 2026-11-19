{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}（Personaからの自動確認であり、ユーザーのメッセージではありません。）前のターンでファイルを変更するかコマンドを実行しましたが、到達点を明確に伝えておらず、現在この会話をフォローしている人もいません。新しい作業や続行をせず、回答のためにpush・merge・デプロイ・他者への連絡をしないでください。それらが必要ならmissingに書いてください。すでに行った作業の現状を、下の形式で説明することだけを行い、停止してください。

{{.Form}}

{{template "agents/closeout_rules" .}}{{else}}(Persona is asking automatically; this is not a user message.) You changed files or ran commands in your last turn but did not say clearly where the work stands, and nobody is following this session now. Do not start new work or continue, and do not push, merge, deploy or message anyone to answer this request. Put such prerequisites in missing. Do only this: describe the work already done and its current state in a block like the one below, then stop.

{{.Form}}

{{template "agents/closeout_rules" .}}{{end}}{{else}}（Persona 自动询问，不是用户发的。）你上一轮动过文件或跑过命令，停下时没有说清这件事做到哪了，现在也没有人在跟进这个会话。不要开始新的工作，不要接着往下做，也不要为了回答去推代码、合并、部署或给别人发消息；要这些才能往下走的，写进 missing。只做一件事：按现在的实际情况，用下面这样一段说明已经做的事做到哪了，然后停下。

{{.Form}}

{{template "agents/closeout_rules" .}}{{end}}
