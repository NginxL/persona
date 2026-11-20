{{if eq .Language "English"}}{{range $i, $p := .Projects}}{{if not $i}}Key projects (knowledge base projects/):
{{end}}- {{$p.Name}} [{{or $p.Importance "-"}} relevance {{$p.Relevance}}] {{$p.OneLine}}（projects/{{$p.ID}}）
{{end}}{{range $i, $c := .Chats}}{{if not $i}}Key chats (knowledge base chats/):
{{end}}- {{$c.Name}} [{{or $c.Importance "-"}} relevance {{$c.Relevance}}] {{$c.OneLine}}（chats/{{$c.ID}}）
{{end}}{{else if eq .Language "日本語"}}{{range $i, $p := .Projects}}{{if not $i}}重要なプロジェクト（ナレッジベース projects/）：
{{end}}- {{$p.Name}} [{{or $p.Importance "-"}} 関連度 {{$p.Relevance}}] {{$p.OneLine}}（projects/{{$p.ID}}）
{{end}}{{range $i, $c := .Chats}}{{if not $i}}重要な会話（ナレッジベース chats/）：
{{end}}- {{$c.Name}} [{{or $c.Importance "-"}} 関連度 {{$c.Relevance}}] {{$c.OneLine}}（chats/{{$c.ID}}）
{{end}}{{else}}{{range $i, $p := .Projects}}{{if not $i}}重点项目（知识库 projects/）：
{{end}}- {{$p.Name}} [{{or $p.Importance "-"}} 相关{{$p.Relevance}}] {{$p.OneLine}}（projects/{{$p.ID}}）
{{end}}{{range $i, $c := .Chats}}{{if not $i}}重点会话（知识库 chats/）：
{{end}}- {{$c.Name}} [{{or $c.Importance "-"}} 相关{{$c.Relevance}}] {{$c.OneLine}}（chats/{{$c.ID}}）
{{end}}{{end}}
