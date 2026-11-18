{{template "tools/translate" .}}

{{if .Language}}{{if eq .Language "日本語"}}入力は JSON {"texts":[…]} です。各項目を翻訳し、入力と同じ件数・順序で JSON {"texts":[…]} を返してください。{{else}}The input is JSON {"texts":[…]}. Translate each entry and return JSON {"texts":[…]} with exactly the same number and order of entries.{{end}}{{else}}输入是 JSON {"texts":[…]}，逐条翻译，回复 JSON {"texts":[…]}，条数和顺序与输入完全一致。{{end}}
