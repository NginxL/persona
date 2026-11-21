{{if hasPrefix .Form "### Persona "}}{{if eq .Language "日本語"}}コードブロックや引用を使わず、見出し、最初の3つの必須項目、各行の「- 」を見本どおりに書き、最後の行を --- にしてください。作業が完了し、続きの作業を提案する場合だけ、最後の区切りの前に「- Next: 」を1行加え、ユーザーがその作業を依頼する短い文を書いてください。
{{else if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}Copy the heading and first three required fields, start each field with “- ”, and end the report with --- on its own line. Use ordinary Markdown without a code fence or a quotation. Only if the work is done and you propose a specific follow-up, add one “- Next: ” line before the final separator. Write a short reply in the owner’s voice naming the action.
{{else}}照抄标题和前三个必填字段，每个字段以“- ”开头，末尾单独一行写 ---。直接使用 Markdown，不加代码围栏或引用。仅在本轮已完成且有具体的后续工作可提议时，在最后的分隔线前加一行“- Next: ”，用用户的口吻简短写明要做什么。
{{end}}{{end}}{{if and .Language (ne .Language "zh-Hans") (ne .Language "简体中文")}}{{if eq .Language "日本語"}}記入ルール：
- status：このターンの作業を完了して届けたらdone、停止したが未完了ならnot_done、誰かや何かを待つ必要があるならblockedとし、waitingに誰・何を待つか書きます。
- merged / deployed：事実だけを書きます。この変更がマージ済み・本番で稼働中の場合だけyes、そうでなければno、該当しなければn/aです。後で照合されるため、誤ったyesはnoより悪い結果になります。
- did：変更したことを平易な一文で一行ずつ、最大三行。変更がなければ省略します。
- artifact：ブランチ、PR、コミット、文書、ビルド、リンクを一行に一つ。なければ省略します。
- verified：実際に実行・測定した検証と数値。未検証はuntestedとし、推測を実測として書きません。
- missing：ユーザーや他者からまだ必要なもの。なければ行を省略します。
- 属性名、行頭の語、statusの値は原文どおりで翻訳しません。コロン以降の内容は日本語にします。{{else}}How to fill it in:
- status: done when this turn's work is completed and delivered; not_done when stopped but unfinished; blocked when a person or prerequisite must be awaited, identifying who or what in waiting.
- merged / deployed: facts only. yes only if this exact change was merged or is running in production; otherwise no; n/a when merging or deployment does not apply. These claims will be checked; a false yes is worse than no.
- did: at most three lines, each a plain-language sentence describing a change; omit if nothing changed.
- artifact: one branch, PR, commit, document, build or link per line; omit if none.
- verified: checks or measurements actually run and their readings; write untested if unverified. Never present inference as testing.
- missing: what the user or someone else still needs to supply; omit the line if nothing is missing.
- Copy attribute names, line-leading words and status values exactly; do not translate them. Write content after colons in {{.Language}}.{{end}}{{else}}填写规则：
- status：这一轮要做的事做完并交付了写 done；停下了但还没做完写 not_done；在等某个人或某件事才能往下走写 blocked，并在 waiting 写等的是谁、等什么。
- merged / deployed：只写事实。合并了、线上跑着的确实是这次的改动才写 yes；没有就写 no；这件事谈不上合并或部署写 n/a。之后会有人去核对，写错比写 no 更糟。
- delivery / unblock：只有任务要求交付标准时填写；delivery 是 live、pr、merged、off 或 none，unblock 是 user、agent 或 other。默认关闭但已部署时，开头写 deployed="yes"，正文另起一行写 delivery: off。交付卡点写 missing，具体等谁或什么写 waiting。
- did：每行一句白话，说改了什么，最多三行；什么都没改就不写。
- artifact：每行一个产物（分支、PR、提交、文档、构建、链接、给用户的文件），没有就不写；给用户的文件写 `artifact: file <这台电脑上的绝对路径>`。
- verified：你实际跑过、量过的东西和读数；没验证就写 untested，不要把推断写成已测。
- missing：还需要用户或别人给什么；不缺就不写这一行。
- 属性名、每行开头的词和 status 的取值照抄，不要翻译；冒号后面的内容{{if .Language}}用{{.Language}}写{{else}}用用户的语言写{{end}}。{{end}}
