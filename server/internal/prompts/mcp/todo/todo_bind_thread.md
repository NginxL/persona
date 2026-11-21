Bind the Persona todo you work on to a chat thread (a topic in a group chat or a conversation), so the thread and the todo are known to belong together. One thread per todo, and a thread can belong to one todo only. It only records the link: it posts nothing to the thread. Use it when you were given the thread to work in or opened it for this todo; the identifiers are the ones the thread's own messages carry, never guessed. Only the todo you work on: naming another todo in todo_id is refused. A todo that already has another thread keeps it: changing it is the user's call, ask them. Binding the same thread again only updates its title.{{template "shared/mcp_language" .}}

```json
{
  "type": "object",
  "required": ["channel", "chat_id", "thread_id"],
  "properties": {
    "todo_id": {
      "type": "string",
      "description": "the todo you work on; leave it out, or give the id you know it by (any other id is refused)"
    },
    "channel": {
      "type": "string",
      "description": "the channel the thread is on, by its channel id: feishu for a Feishu chat (not the app name written otherwise, not a guess)"
    },
    "chat_id": {
      "type": "string",
      "description": "the chat the thread is in; for feishu the chat id (oc_…)"
    },
    "thread_id": {
      "type": "string",
      "description": "the thread in that chat; for feishu the thread_id of the message events (omt_…), not the root message's message_id (om_…)"
    },
    "title": {
      "type": "string",
      "description": "a short label for the thread, shown beside the todo"
    }
  }
}
```
