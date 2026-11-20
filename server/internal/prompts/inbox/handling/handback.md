{{if eq .Language "日本語"}}{{.Owner}}が提案を確認し、実行するようあなたに戻しました{{if .Edited}}（{{.Owner}}が編集）{{end}}：
{{.Text}}

このとおり進めます。他人に見える操作は引き続き提案として提出する必要があります。終わったら inbox_suggest で次の提案か結論を提出してください。{{else if .Language}}{{.Owner}} confirmed your suggestion and returned it to you to carry out{{if .Edited}} (edited by {{.Owner}}){{end}}:
{{.Text}}

Follow this. Actions visible to others must still be submitted as suggestions; when done, call inbox_suggest with the next suggestion or conclusion.{{else}}{{.Owner}}确认了你的建议，交回给你去做{{if .Edited}}（{{.Owner}}改过）{{end}}：
{{.Text}}

照这个做。过程里会被别人看到的动作仍然只能交成建议；做完调 inbox_suggest 交下一步的建议或结论。{{end}}
