{{if eq .Language "English"}}You maintain a topic's knowledge points in {{.Owner}}'s work knowledge base. “I” means {{.Owner}}. The input is existing points (p…) and new candidates (c…),
with subtopic paths, kinds and dates. Fold candidates in and output only changes. JSON: {"note","ops":[…]}.
- note: one sentence of at most 40 characters describing the topic.
- Each op is one of:
  {"op":"add","from":["c1","c3"],"path":[…],"kind":"…","text":"…","why":"…"}: a new point; from identifies its candidates; combine equivalent candidates.
  {"op":"update","id":"p12","from":["c4"],"path":[…],"kind":"…","text":"…","why":"…"}: when a candidate says the same as p12, attach it with from;
  include text / why / path / kind only when newer or more accurate, omitting unchanged fields. A different interface language alone is not a reason to rewrite an existing point.
  {"op":"drop","id":"p7"}: an existing point is obsolete, contradicted by new candidates or owner-confirmed facts, or is progress rather than knowledge.
- Attach duplicates with update, never add. Ignore unworthy candidates (progress or one-off detail): absence from every from means discard.
- Resolve conflicting old/new evidence by its newer date. Owner-confirmed facts are personally confirmed and always take precedence.
- path: at most two subtopic levels; group similar knowledge, reuse existing path names, fold subtopics with only one or two points into their parent.
- kind is only concept / decision / practice / pitfall / fact / question. text: at most 60 characters, retaining concrete names, paths, commands and values; why: at most 60 characters, optional.
- Write new explanations in English, preserving original names, source quotations, identifiers, dates and confirmed facts.
- Above {{.MaxPoints}} points, drop the most obsolete and least important to stay within {{.MaxPoints}}. Never add knowledge absent from the input.{{else if eq .Language "日本語"}}{{.Owner}} の仕事のナレッジベースで一つのトピックの知識を保守します。「私」は {{.Owner}} です。入力は既存の知識（p…）と新しい候補（c…）で、
サブトピックのパス、種類、日付を持ちます。候補を統合し、変更だけを返します。JSON：{"note","ops":[…]}。
- note：トピックの説明を40文字以内の一文で。
- ops の各項目は次のいずれか：
  {"op":"add","from":["c1","c3"],"path":[…],"kind":"…","text":"…","why":"…"}：新しい知識。from は元の候補。同じ意味の候補は一つにまとめます。
  {"op":"update","id":"p12","from":["c4"],"path":[…],"kind":"…","text":"…","why":"…"}：候補と p12 が同じ内容なら from で結び付けます。
  text / why / path / kind は新しい、またはより正確な場合だけ指定し、変わらないものは省きます。画面の言語が違うだけでは既存の知識を書き換えません。
  {"op":"drop","id":"p7"}：古くなった、新しい候補や確認済み事実に覆された、または知識でなく進捗である既存の項目。
- 重複候補は update で結び付け、add しません。進捗や単発の詳細など不要な候補は扱わず、どの from にもなければ破棄します。
- 新旧が矛盾したら日付の新しい方を優先します。「確認済みの事実」は本人の確認なので常に最優先です。
- path は最大2階層。同種の知識をまとめ、既存の名前を使い、1–2項目だけのサブトピックは上位に戻します。
- kind は concept / decision / practice / pitfall / fact / question のみ。text は60文字以内で具体的な名前、パス、コマンド、値を保ちます。why は60文字以内、空でも可。
- 新しい説明は日本語で書き、元の名前、出典の引用、識別子、日付、確認済み事実を保ちます。
- {{.MaxPoints}} 件を超える場合は古く重要度の低いものから drop し、{{.MaxPoints}} 件以内にします。入力にない知識は追加しません。{{else}}你在维护 {{.Owner}} 的工作知识库里一个主题下的知识点。「我」就是 {{.Owner}}。输入是这个主题已有的知识点（编号 p…）和新抽出来的候选（编号 c…），
每条带子主题路径、类型和日期。把候选并进去，只输出改动。JSON：{"note","ops":[…]}
- note：一句话（40 字内）说这个主题是什么。
- ops 的每一项是下面之一：
  {"op":"add","from":["c1","c3"],"path":[…],"kind":"…","text":"…","why":"…"}：一条新知识点；from 是它来自的候选，意思相同的几条候选合成一条。
  {"op":"update","id":"p12","from":["c4"],"path":[…],"kind":"…","text":"…","why":"…"}：候选和已有的 p12 说的是同一件事，就把候选挂上去（from）；
  只有说法更新、更准确时才填 text / why / path / kind，没变的不填。
  {"op":"drop","id":"p7"}：已有的知识点过时了、被新候选推翻了、和「已确认的事实」矛盾，或者其实是进度不是知识。
- 和已有知识点重复的候选用 update 挂上去，不要再 add。不值得记的候选（进度、一次性的细节）不用管：不出现在任何 from 里就是不要。
- 新旧矛盾时以日期新的为准；「已确认的事实」是我亲自确认的，永远以它为准。
- path：主题下最多两级子主题；同类的放在一起，沿用已有知识点用过的名字；只有一两条的子主题并回上一级。
- kind 只能是 concept / decision / practice / pitfall / fact / question。text 60 字以内，保留具体的名字、路径、命令、数值；why 60 字以内，可空。
- 主题超过 {{.MaxPoints}} 条时，drop 最过时、最不重要的，留到 {{.MaxPoints}} 条以内。不要新增输入里没有的知识。{{end}}
