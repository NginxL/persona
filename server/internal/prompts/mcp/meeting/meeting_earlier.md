The minutes of earlier meetings that may be used in this one (only those the owner marked not private), with the status of the projects they belong to and whether their action items' to-dos are open or done: at most 6 meetings, the most relevant to the query first. Never their transcripts. An empty result means none of them holds anything for the query.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": {
      "type": "string",
      "description": "What to look for: the question, or its key words."
    }
  }
}
```
