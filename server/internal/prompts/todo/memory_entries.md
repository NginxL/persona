{{if eq .Language "日本語"}}<persona-context kind="context" title="記憶 · この作業に関係する項目" url="">
ユーザーの記憶のうち、この作業に関係する項目です（Persona がタスクの内容から選んだもの。機微な項目は、この作業に必要な場合だけ含めています）。この作業にだけ使い、他の人に伝えたり、コード、ドキュメント、コミットに書いたりしないでください。{{range .Entries}}
{{.N}}. {{.Text}}{{end}}
</persona-context>{{else if .Language}}<persona-context kind="context" title="Memory · Entries relevant to this work" url="">
Entries from the user's memory that bear on this work (Persona picked them by what the task says; sensitive ones only when this work needs them). Use them for this work only: do not pass them on to anyone, or write them into code, docs or commits.{{range .Entries}}
{{.N}}. {{.Text}}{{end}}
</persona-context>{{else}}<persona-context kind="context" title="记忆 · 和这件事相关的条目" url="">
用户记忆里和这件事相关的几条（Persona 按任务内容挑出；敏感的只在这件事用得上时才带）。只在这件事里用，不要转告别人，也不要写进代码、文档或提交。{{range .Entries}}
{{.N}}. {{.Text}}{{end}}
</persona-context>{{end}}
