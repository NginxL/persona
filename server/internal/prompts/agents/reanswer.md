{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}前の質問にはアシスタントが先に回答しましたが、私が今、回答を変更します：
{{range .Items}}- {{.Question}} → {{.Answer}}{{with .Before}}（アシスタントの回答：{{.}}）{{end}}
{{end}}
私の回答に従って進めてください。すでにアシスタントの回答に従って作業した場合は、まず何を変更する必要があるか説明し、そのまま元に戻せるものは戻してください。{{else}}The assistant answered your earlier questions for me. I am changing those answers now:
{{range .Items}}- {{.Question}} → {{.Answer}}{{with .Before}} (the assistant answered: {{.}}){{end}}
{{end}}
Continue according to my answers. If you already acted on the assistant's answers, first explain what needs to change, and revert what can be reverted directly.{{end}}{{else}}你前面问的问题，助手先替我答了，我现在改答：
{{range .Items}}- {{.Question}} → {{.Answer}}{{with .Before}}（助手答的是：{{.}}）{{end}}
{{end}}
按我的答案往下做；已经按助手的答案做了的，先说一下要改哪些，能直接改回的就改回。{{end}}
