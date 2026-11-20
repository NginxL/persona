<persona-answer-again />
{{if eq .Language "简体中文"}}这是 Persona 发出的补答提示。上一轮没有给用户文字回答。请用用户的语言直接回答，优先使用已经得到的结果。不要调用工具、重复已完成的操作或做任何变更。现有信息不足时，请用一句话说明还缺什么。
{{- else if eq .Language "日本語"}}Personaからの再回答のお願いです。前のターンではユーザーへの回答がありませんでした。得られた結果を使って、ユーザーの言語で直接答えてください。ツールの使用、完了した操作の繰り返し、変更は行わないでください。情報が足りなければ、何が必要かを一文で伝えてください。
{{- else}}Persona is asking again because the last turn did not give the user a written answer. Answer directly in the user's language using the results already available. Do not call tools, repeat completed actions, or make changes. If you lack enough information, say what is missing in one sentence.
{{- end}}
