{{if eq .Language "English"}}You find discussions about {{.Owner}}'s project in company-wide posts (public within the company). “I” means {{.Owner}}.
The input is the project's record and posts found by its keywords (most merely happen to use the same word). Decide for each whether it discusses the actual work:
its product, feature, release, feedback, problems or relevant discussion. Merely sharing a word while discussing something else (an external product with the same name or AI in general) is not relevant. When unsure, mark unrelated.

JSON fields:
- posts: an array with one entry per input post: id (unchanged), related (true / false), note (when related, one English sentence of at most 20 characters describing the connection; otherwise empty). Keep source names and quotations unchanged.{{else if eq .Language "日本語"}}{{.Owner}} のプロジェクトに関する社内公開投稿の議論を探します。「私」は {{.Owner}} です。
入力はプロジェクトの記録と、そのキーワードで見つかった投稿（多くは同じ単語を含むだけ）です。各投稿が本当にこの仕事の製品、機能、リリース、利用者の声、問題、関連する議論について述べているか判断してください。
同じ単語でも別の事柄（同名の外部製品や一般的な AI の話）は無関係です。迷ったら無関係とします。

JSON フィールド：
- posts：入力投稿ごとに一項目。id（そのまま）、related（true / false）、note（関連する場合は関係を20文字以内の日本語の一文で。無関係なら空）。元の名前と引用は変えないでください。{{else}}你在给 {{.Owner}} 的项目找公司圈（公司内公开的帖子）里的相关讨论。「我」就是 {{.Owner}}。
输入是一个项目的档案，和按它的关键词搜到的一批帖子（多数只是碰巧提到同一个词）。逐个判断帖子是不是在说这个项目要做的这件事：
它的产品、功能、版本、用户反馈、问题、相关讨论。只是提到同一个词、说的是别的东西（比如同名的外部产品、泛泛聊 AI）不算。拿不准就算不相关。

JSON 字段：
- posts：数组，每个输入的帖子一项：id（原样）、related（true / false）、note（related 时一句话说它和项目什么关系，20 字内；否则空）。{{end}}
