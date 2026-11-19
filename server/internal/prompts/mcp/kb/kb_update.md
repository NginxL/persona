Write to the knowledge base: create or change a project (name, project_kind short|long, status, goal, my_role, aliases, link chats / people, code dirs as "<machine>:<path>" so Agents there belong to it, append a timeline `log` line, replace its summary), or tag / annotate a chat, person, doc, meeting, event, recording (a recording's content is the phone's: only its tags, pinned and projects; importance P0–P3, relevance 0–100, tags, note, link projects), or a topic (its points are curated: only its name, note, aliases and tags). Tags set here are kept over the automatic ones. `remove_*` (ids) unlink for good: Persona won't relink it by itself; the rest still updates. Use it whenever a conversation settles something about a project or person. Two projects that are the same work under two names: `merge_into`; two that were merged but are not: `unmerge` on the one merged away.

```json
{
  "type": "object",
  "required": ["kind", "id"],
  "properties": {
    "kind": {
      "type": "string",
      "enum": ["projects", "chats", "people", "docs", "meetings", "events", "recordings", "topics"]
    },
    "id": {
      "type": "string",
      "description": "id, or a project's name (created when new)"
    },
    "merge_into": {
      "type": "string",
      "description": "projects only: fold this project into that one (slug or name); nothing else changes in the same call"
    },
    "unmerge": {
      "type": "boolean",
      "description": "projects only: undo the merge that folded this project (slug or name) into another; nothing else changes in the same call"
    },
    "name": {
      "type": "string"
    },
    "project_kind": {
      "type": "string",
      "enum": ["short", "long"]
    },
    "status": {
      "type": "string",
      "enum": ["active", "paused", "done"]
    },
    "goal": {
      "type": "string"
    },
    "my_role": {
      "type": "string"
    },
    "note": {
      "type": "string"
    },
    "importance": {
      "type": "string",
      "enum": ["P0", "P1", "P2", "P3"]
    },
    "relevance": {
      "type": "integer"
    },
    "tags": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "summary": {
      "type": "string",
      "description": "Markdown, replaces summary.md"
    },
    "log": {
      "type": "string",
      "description": "one line appended to a project's timeline"
    },
    "aliases": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "remove_aliases": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "projects: aliases that are not this project's (another's name, too generic)"
    },
    "chats": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "remove_chats": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "people": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "remove_people": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "dirs": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "projects": {
      "type": "array",
      "items": {
        "type": "string"
      }
    }
  }
}
```
