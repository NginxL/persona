{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}{{if .ByAssistant}}上の質問には、アシスタントが私のルールに従って先に回答しました{{with .Reason}}（理由：{{.}}）{{end}}。後で私が回答を変更する場合があります：{{else}}上の質問への私の回答です：{{end}}
{{range .Items}}- {{.Question}} → {{.Answer}}
{{end}}{{else}}{{if .ByAssistant}}The assistant answered your questions for me under my rules{{with .Reason}} (reason: {{.}}){{end}}; I may change those answers later:{{else}}My answers to your questions above:{{end}}
{{range .Items}}- {{.Question}} → {{.Answer}}
{{end}}{{end}}{{else}}{{if .ByAssistant}}关于你上面的问题，助手按我的规则先替我答了{{with .Reason}}（理由：{{.}}）{{end}}，我之后可能改答：{{else}}关于你上面的问题，我的回答：{{end}}
{{range .Items}}- {{.Question}} → {{.Answer}}
{{end}}{{end}}
