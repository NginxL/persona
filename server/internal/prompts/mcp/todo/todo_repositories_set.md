Replace atomically using revisions from todo_repositories_get/repositories_list. [] removes metadata. Frozen source snapshots: remove/reselect to adopt catalog identity changes. Editable after execution. Omit links to keep; [] clears. Omitted/empty titles keep. Reread/reconcile conflicts. No files, progress or completion changes.

```json
{
  "type": "object",
  "required": [
    "id",
    "expected_revision",
    "expected_catalog_revision",
    "repositories"
  ],
  "properties": {
    "id": {
      "type": "string"
    },
    "expected_revision": {
      "type": "integer",
      "minimum": 0
    },
    "expected_catalog_revision": {
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
                "properties": {
                  "kind": {
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
```
