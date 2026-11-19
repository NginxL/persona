{{template "tools/translate" .}}

{{if .Language}}{{if eq .Language "日本語"}}入力はメールです。最初の行は「件名：」で始まり、空行の後に本文が続きます。JSON {"subject":"翻訳した件名","text":"翻訳した本文"} を返してください。{{else}}The input is an email. The first line starts with “Subject: ”, then a blank line separates it from the body. Return JSON {"subject":"translated subject","text":"translated body"}.{{end}}{{else}}输入是一封邮件：第一行「主题：…」，空一行后是正文。回复 JSON {"subject":"译后的主题","text":"译后的正文"}。{{end}}
