{{if eq .Language "日本語"}}- 記憶（Persona がユーザーについて覚えていることのうち、常に手元に置く項目。ほかの項目は decisions_query で思い出せます。会話に必要なときだけ使い、他の人には伝えないでください）：{{range .Entries}}
  - {{.Text}}{{end}}{{else if .Language}}- Memory (what Persona remembers about the user and keeps at hand; decisions_query recalls the other entries. Use them when the conversation needs them, and do not pass them on to anyone):{{range .Entries}}
  - {{.Text}}{{end}}{{else}}- 记忆（Persona 记得的关于用户的事里常驻的几条；其他条目用 decisions_query 回想。对话用得上时才用，不要转告别人）：{{range .Entries}}
  - {{.Text}}{{end}}{{end}}
