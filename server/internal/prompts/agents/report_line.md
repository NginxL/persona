{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}開発AgentからPersonaへの一回の報告を、一文に言い換えてください。ユーザーのタスク進捗一覧でタイトルの下に表示され、ひと目で状況が分かるようにします。原文はタスクを開くと読めます。

この一文は：
- 日本語で{{.Max}}文字以内、一文だけにします。
- 報告にある事実だけを伝え、補足・推測・評価はしません。長い報告なら最も知らせるべき点を選びます。
- progressは完了したこと・作業中のこと、questionは判断が必要なこと、blockedは障害、doneは結論を書きます。
- 種類や手順番号は画面に別表示されるので、「質問」「停止中」「完了」「第3段階」などの接頭辞は不要です。
- タスク名を繰り返さず、「Agent」「それ」のような主語を書きません。pane・session・バッチ・todo_などの内部IDは写さず、PR・issue番号は使えます。
- Markdown、引用符、かぎ括弧、末尾の句点は使いません。

その一文だけを一行で返してください。{{else}}Paraphrase one report from a coding Agent to Persona in one sentence. It appears below the task title in the user's progress list, so the user can see where the task stands at a glance; opening the task shows the original report.

The sentence:
- Write in {{.Language}}, at most {{.Max}} characters, one sentence only.
- Restate only what the report says. Do not add facts, speculate or evaluate. For a long report, choose what matters most to the user.
- Match the report kind: progress says what was completed or is being done; question says what needs deciding; blocked says what prevents progress; done states the result.
- The interface separately shows kind and step number. Do not repeat prefixes such as “question”, “blocked”, “done” or “step 3”.
- Do not repeat the task title or use subjects such as “the Agent” or “it”. Do not copy internal pane/session/batch/todo_ identifiers; PR and issue numbers are allowed.
- No Markdown, quotation marks or final full stop.

Return only that sentence on one line.{{end}}{{else}}你替用户把编程 Agent 向 Persona 的一次汇报转述成一句话。这句话显示在用户的任务进度列表里、这件任务的标题下面，让用户扫一眼就知道这件任务到哪了；用户点开任务才看汇报原文。

这句话：
- {{if .Language}}用{{.Language}}写，不超过 {{.Max}} 个字符{{else}}不超过 {{.Max}} 个字{{end}}，只写一句。
- 只转述汇报里说了的事，不补充、不推测、不评价；汇报很长就挑用户最该知道的那件。
- 按汇报类型写：progress 写做到哪了、正在做什么（「迁移脚本跑完，在补测试」）；question 写要定的事（「先灰度还是直接全量」）；blocked 写卡在哪（「CI 机器连不上私有源」）；done 写结论（「已合并 PR #123，测试全过」）。
- 界面会另外标出汇报类型和第几步：不要再写「问」「卡住了」「做完了」「第 3 步」这类前缀。
- 不要重复任务标题。不要写「Agent」「它」这类主语，也不要照抄 pane id、session id、批次号、todo_… 这类内部编号（PR、issue 编号可以写）。
- 不用 Markdown、引号和书名号，不加句号。

只回这一句话本身，一行。{{end}}
