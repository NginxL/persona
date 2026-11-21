{{if eq .Lang "en"}}Search the user's knowledge base: archived chats, people, groups, projects, documents, meeting summaries and minutes of recorded meetings. Returns matching lines, newest first. Separate keywords with spaces; every keyword must match. Use specific terms such as a person, project or group name. For past recorded meetings (decisions, responsibilities or unresolved points), set kind=recordings and search by meeting title or words spoken in it.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "query"
  ],
  "properties": {
    "query": {
      "type": "string",
      "description": "Keywords separated by spaces"
    },
    "kind": {
      "type": "string",
      "enum": [
        "projects",
        "chats",
        "people",
        "docs",
        "meetings",
        "events",
        "recordings",
        "topics"
      ],
      "description": "Search only this kind: recordings = recorded meetings (minutes and retained transcripts), meetings = calendar meetings, projects = projects, chats = groups and chat history, people = people, docs = documents, events = events, topics = knowledge topics. Omit to search all kinds"
    }
  }
}
```{{else if eq .Lang "ja"}}ユーザーのナレッジベースを検索します。保存されたチャット、人、グループ、プロジェクト、文書、会議の要約、録音した会議の議事録が対象です。一致する行を新しい順に返します。キーワードは空白で区切り、すべての語が一致する必要があります。人名、プロジェクト名、グループ名など具体的な語を使ってください。過去に録音した会議の決定事項、担当、未決事項を尋ねる場合は kind=recordings を指定し、会議のタイトルや会議で使われた語で検索してください。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": [
    "query"
  ],
  "properties": {
    "query": {
      "type": "string",
      "description": "空白で区切ったキーワード"
    },
    "kind": {
      "type": "string",
      "enum": [
        "projects",
        "chats",
        "people",
        "docs",
        "meetings",
        "events",
        "recordings",
        "topics"
      ],
      "description": "この種類だけを検索します。recordings は録音した会議（議事録と保持されている文字起こし）、meetings はカレンダー上の会議、projects はプロジェクト、chats はグループとチャット履歴、people は人、docs は文書、events はイベント、topics は知識のトピックです。省略すると全種類を検索します。"
    }
  }
}
```{{else}}搜索用户的知识库（聊天记录归档、人、群、项目、文档、会议的摘要、录过的会的纪要）。返回匹配的行，按时间从新到旧。关键词用空格分开，每个词都要命中；用人名、项目名、群名这类具体的词。问以前开过的会（定了什么、谁做什么、还有什么没定）时给 kind=recordings，用会的标题或会上说过的词找。{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": {"type": "string", "description": "关键词，空格分隔"},
    "kind": {
      "type": "string",
      "enum": ["projects", "chats", "people", "docs", "meetings", "events", "recordings", "topics"],
      "description": "只搜这一类：recordings 录过的会（纪要和还留着的转写），meetings 日历上的会，projects 项目，chats 群和聊天记录，people 人，docs 文档，events 事件，topics 知识点。不给就全搜"
    }
  }
}
```{{end}}
