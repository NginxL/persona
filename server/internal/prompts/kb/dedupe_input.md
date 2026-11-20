{{if eq .Language "English"}}{{template "kb/dedupe_side" .A}}

{{template "kb/dedupe_side" .B}}

People shared by both: {{.Shared}}{{else if eq .Language "日本語"}}{{template "kb/dedupe_side" .A}}

{{template "kb/dedupe_side" .B}}

共通する人数：{{.Shared}}{{else}}{{template "kb/dedupe_side" .A}}

{{template "kb/dedupe_side" .B}}

两边共同的人：{{.Shared}} 个{{end}}
