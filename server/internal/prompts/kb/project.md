{{if eq .Language "English"}}You maintain {{.Owner}}'s project record. “I” means {{.Owner}}. The input contains its existing record, recent timeline and summaries of related chats.
Rewrite the project's record in English. Keep original names, source quotations, identifiers, dates and confirmed facts unchanged. Do not translate source records merely to match the interface. JSON fields:
- summary: Markdown, at most 1500 characters for short projects (kind short), 2500 for long-term directions (kind long).
  If the existing record is longer, bring it back within that limit: combine duplicates, remove outdated progress, retain the newest and most important information.
  Fixed sections: “## Goal”, “## Current state” (latest progress with dates), “## Next steps” (who, what, when),
  “## Risks and blockers”, “## My role”, “## People” (name and one-sentence role). Write “None” for empty sections.
- one_line: one sentence of at most 30 characters about the project's current state.
- goal: a one-sentence goal. kind: short (a deliverable) or long (team mission / long-term direction). status: active / paused / done.
- importance: P0–P3 (importance to me); relevance: 0–100 (relevance to me); tags: 2–6 short tags, reusing existing ones.
- keywords: 3–8 words people would use to discuss this work publicly within the company (product, feature, model or public name), to find related posts.
  Exclude internal code names, people's names and overly broad words (Agent, AI, launch, optimization).
- Related company posts are colleagues' posts about this work. Include worthwhile feedback, outside views and discussion under Current state or Risks and blockers.
- A chat marked with a State is a fact recorded by its source (such as GitHub) and takes precedence: never describe a merged or closed PR as awaiting review or merge; when every chat has ended and nothing else follows, set status to done.
Do not invent what you do not know.{{else if eq .Language "日本語"}}{{.Owner}} のプロジェクト記録を保守します。「私」は {{.Owner}} です。入力は既存の記録、最近のタイムライン、関連する会話の要約です。
プロジェクト記録を日本語で書き直します。元の名前、引用、識別子、日付、確認済みの事実は保ち、画面の言語に合わせるためだけに出典を書き換えないでください。JSON フィールド：
- summary：Markdown。短期プロジェクト（kind short）は1500文字以内、長期の方向性（kind long）は2500文字以内。
  既存の記録が上限を超える場合、重複と古い進捗を整理し、最新かつ重要な情報を残して上限内に収めます。
  固定見出し：「## 目標」「## 現状」（日付付きの最新進捗）「## 次のステップ」（誰が、何を、いつ）
  「## リスクと障害」「## 私の役割」「## 関係者」（名前と一文の役割）。空なら「なし」。
- one_line：現在の状態を30文字以内の一文で。
- goal：一文の目標。kind：short（成果物のある短期仕事）または long（チームの使命や長期の方向性）。status：active / paused / done。
- importance：P0–P3（私にとっての重要度）、relevance：0–100（私との関連度）、tags：短いタグを2–6個。既存のタグを優先します。
- keywords：社内でこの仕事を公開の場で話すときの単語を3–8個（製品名、機能名、モデル名、対外的な呼び名）。関連投稿の検索に使います。
  内部コード名、人名、広すぎる単語（Agent、AI、公開、最適化）は含めません。
- 関連する社内投稿で記録に値する利用者の声、外部の意見、議論は「現状」または「リスクと障害」に含めます。
- 「状態」が付いた会話は、出典（GitHub など）が記録した事実で、それを優先します。マージ済み・クローズ済みの PR を「レビュー待ち」「マージ待ち」と書かず、すべての会話が終わり後続もなければ status は done にします。
不明なことは作らないでください。{{else}}你在维护 {{.Owner}} 的项目档案。「我」就是 {{.Owner}}。输入是一个项目已有的档案、最近的时间线、相关会话的压缩上下文。
重写这个项目的档案，全部用中文。JSON 字段：
- summary：Markdown。长度按项目大小：短期项目（kind 是 short）1500 字以内，长期方向（kind 是 long）2500 字以内。
  已有档案超过这个长度的，这次重写就压回去：合并重复的、删掉过时的进展，留最新、最重要的。
  固定小节：「## 目标」「## 现状」（最新进展，带日期）「## 下一步」（谁做什么、什么时候）
  「## 风险和阻塞」「## 我的角色」「## 相关的人」（名字 + 一句话角色）。没有的写「无」。
- one_line：一句话（30 字内）说项目现在到哪了。
- goal：一句话目标。kind：short（有交付的短期项目）或 long（团队使命 / 长期方向）。status：active / paused / done。
- importance：P0–P3（对我的重要级）；relevance：0–100（与我相关性）；tags：2–6 个短标签，优先复用已有标签。
- keywords：3–8 个公司里公开讨论这件事时会用的词（产品名、功能名、模型名、对外叫法），用来在公司圈里找相关的帖子。
  不要内部代号、人名，也不要太泛的词（Agent、AI、上线、优化）。
- 「相关公司圈」是同事在公司圈里关于这件事的帖子：值得写的（用户反馈、外部声音、相关讨论）写进「## 现状」或「## 风险和阻塞」。
- 会话标了「状态」的，那是来源（比如 GitHub）记录的事实，以它为准：已合并、已关闭的 PR 不要再写成「等评审」「等合并」；所有会话都已结束、也没有别的后续时，status 写 done。
不知道的不要编。{{end}}
