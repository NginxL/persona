{{if eq .Lang "en"}}List the coding Agents (Claude Code / Codex) on the user's computers: computer, title, status (needs you / working / finished unread / idle) and last words.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {}
}
```{{else if eq .Lang "ja"}}ユーザーの各コンピュータ上のコーディング Agent（Claude Code / Codex）を一覧表示します。コンピュータ、タイトル、状態（対応待ち／作業中／完了・未読／待機中）、最後の発言を返します。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {}
}
```{{else}}列出用户各台电脑上的编程 Agent（Claude Code / Codex）：在哪台机器、标题、状态（需要你处理 / 工作中 / 完成未看 / 空闲）、最后说的话。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "properties": {}
}
```{{end}}
