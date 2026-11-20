Write settled facts to the KB. Projects: edit name, project_kind (short|long), status, goal, my_role, aliases, chats/people, code dirs (`<machine>:<path>`, linking Agents), timeline log or summary; merge_into folds duplicates, unmerge reverses it, with no other changes in that call. Annotate chats, people, docs, meetings, events, recordings (phone-owned content; tags, pinned and projects only) and topics (name, note, aliases, tags only). Explicit tags override automatic ones. remove_* (ids) permanently unlinks only that link; other updates proceed. Change project_mode/source_urls only at the user's direction: reference-only keeps pointers and aliases but leaves details at the source; full in a separate call restores detail writes.

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
      "description": "projects only: fold this project into the target (slug or name)"
    },
    "unmerge": {
      "type": "boolean",
      "description": "projects only: undo the merge that folded this project into another"
    },
    "name": {
      "type": "string"
    },
    "project_kind": {
      "type": "string",
      "enum": ["short", "long"]
    },
    "project_mode": {
      "type": "string",
      "enum": ["full", "reference-only"],
      "description": "user-directed"
    },
    "source_urls": {
      "type": "array",
      "items": { "type": "string" },
      "description": "user-directed; replace; empty list clears"
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
