{{if eq .Language "日本語"}}Git 操作には対象リポジトリの absolute_path を使用してください。{{else if not .Language}}执行 Git 操作时，请使用目标仓库的 absolute_path。{{else}}For Git operations, use the absolute_path of the repository you intend to change.{{end}}
<workspace_repositories>
{{.Rows}}
</workspace_repositories>
