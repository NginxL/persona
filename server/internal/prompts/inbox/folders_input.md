{{if eq .Language "日本語"}}{{with .Folders}}既存のフォルダ（案件数）：
{{range .}}- {{.Path}}（{{.Count}}）
{{end}}{{else}}まだフォルダはありません。

{{end}}{{with .Examples}}ユーザーが手動で分類した例（その習慣に従う）：
{{range .}}- {{.Title}}{{with .Sender}}（{{.}}）{{end}} → {{.Folder}}
{{end}}
{{end}}分類する案件：
{{range .Items}}
id：{{.ID}}
種類：{{.Kind}}{{with .Source}}（{{.}}）{{end}}
タイトル：{{.Title}}{{with .Sender}}
送信者：{{.}}{{end}}{{with .Summary}}
要約：{{.}}{{end}}{{with .Preview}}
内容：{{.}}{{end}}
{{end}}{{else if .Language}}{{with .Folders}}Existing folders (item counts):
{{range .}}- {{.Path}} ({{.Count}})
{{end}}{{else}}No folders yet.

{{end}}{{with .Examples}}Items the user filed by hand (follow their habits):
{{range .}}- {{.Title}}{{with .Sender}} ({{.}}){{end}} → {{.Folder}}
{{end}}
{{end}}Items to file:
{{range .Items}}
id: {{.ID}}
Type: {{.Kind}}{{with .Source}} ({{.}}){{end}}
Title: {{.Title}}{{with .Sender}}
From: {{.}}{{end}}{{with .Summary}}
Summary: {{.}}{{end}}{{with .Preview}}
Content: {{.}}{{end}}
{{end}}{{else}}{{with .Folders}}已有的文件夹（后面是里面的事项数）：
{{range .}}- {{.Path}}（{{.Count}}）
{{end}}
{{else}}还没有文件夹。

{{end}}{{with .Examples}}用户自己归档过的（照着用户的习惯来）：
{{range .}}- {{.Title}}{{with .Sender}}（{{.}}）{{end}} → {{.Folder}}
{{end}}
{{end}}要归档的事项：
{{range .Items}}
id：{{.ID}}
类型：{{.Kind}}{{with .Source}}（{{.}}）{{end}}
标题：{{.Title}}{{with .Sender}}
来自：{{.}}{{end}}{{with .Summary}}
摘要：{{.}}{{end}}{{with .Preview}}
内容：{{.}}{{end}}
{{end}}{{end}}
