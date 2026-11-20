Record requested/agreed work or reminders immediately, without confirmation; execute via task_plan. Open title/due duplicates return existing to-dos but refuse bindings: use todo_repositories_get/set. Short title in the user's language; details in notes. In leader chats: record that direction's work; task_plan dispatches, todo_complete finishes unassigned work. Bindings save atomically; IDs/catalog_revision from repositories_list. No files or task progress changes.

```json
{
  "type": "object",
  "required": [
    "title"
  ],
  "properties": {
    "title": {
      "type": "string",
      "description": "what is to be done, in a line, in the user's language"
    },
    "notes": {
      "type": "string",
      "description": "Details."
    },
    "due": {
      "type": "string",
      "description": "Day only: YYYY-MM-DD; otherwise YYYY-MM-DD HH:MM (user time zone) or RFC3339. Relative 2h/3d accepted."
    },
    "remind": {
      "type": "string",
      "description": "Future reminder (user time zone/RFC3339 or relative 30m/2h/1d). Only if requested or deadline close and important."
    },
    "priority": {
      "type": "string",
      "enum": [
        "high",
        "normal",
        "low"
      ],
      "description": "normal when left out"
    },
    "kind": {
      "type": "string",
      "enum": [
        "todo",
        "concern"
      ],
      "description": "todo (default); concern (main chat only) tracks a user-raised matter until concern_report reports its outcome; other completion tools cannot close it."
    },
    "project": {
      "type": "string",
      "description": "Existing kb_list project slug/name; prefer long-term work. Only when clear; no near-duplicates."
    },
    "part_of": {
      "type": "string",
      "description": "Request to-do id this is a piece of; hand out via task_plan todo_id."
    },
    "after": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "With part_of: sibling piece ids it waits for."
    },
    "repository_bindings": {
      "type": "object",
      "required": [
        "catalog_revision",
        "repositories"
      ],
      "properties": {
        "catalog_revision": {
          "type": "integer",
          "minimum": 0
        },
        "repositories": {
          "type": "array",
          "maxItems": 20,
          "items": {
            "type": "object",
            "required": [
              "repo_id"
            ],
            "properties": {
              "repo_id": {
                "type": "string"
              },
              "directory_alias": {
                "type": "string"
              },
              "requested_ref": {
                "type": "object",
                "properties": {
                  "kind": {
                    "type": "string",
                    "enum": [
                      "default",
                      "branch",
                      "tag",
                      "commit",
                      "ref"
                    ]
                  },
                  "value": {
                    "type": "string",
                    "minLength": 1
                  }
                },
                "additionalProperties": false,
                "required": [
                  "kind"
                ],
                "oneOf": [
                  {
                    "type": "object",
                    "properties": {
                      "kind": {
                        "type": "string",
                        "const": "default"
                      }
                    }
                  },
                  {
                    "required": [
                      "value"
                    ]
                  }
                ]
              },
              "links": {
                "type": "array",
                "items": {
                  "type": "object",
                  "properties": {
                    "url": {
                      "type": "string",
                      "minLength": 1
                    },
                    "kind": {
                      "type": "string",
                      "enum": [
                        "issue",
                        "pull_request"
                      ]
                    },
                    "number": {
                      "type": "string",
                      "pattern": "^[1-9][0-9]{0,19}$"
                    },
                    "title": {
                      "type": "string",
                      "description": "Plain source title; trim; ≤1024 UTF-8 bytes.",
                      "maxLength": 1024
                    }
                  },
                  "additionalProperties": false,
                  "oneOf": [
                    {
                      "required": [
                        "url"
                      ],
                      "not": {
                        "anyOf": [
                          {
                            "required": [
                              "kind"
                            ]
                          },
                          {
                            "required": [
                              "number"
                            ]
                          }
                        ]
                      }
                    },
                    {
                      "required": [
                        "kind",
                        "number"
                      ],
                      "not": {
                        "required": [
                          "url"
                        ]
                      }
                    }
                  ]
                },
                "maxItems": 50
              }
            },
            "additionalProperties": false
          }
        }
      },
      "additionalProperties": false
    }
  }
}
```
