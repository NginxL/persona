Atomically add 1–50 exact issue/PR URLs across links:[{url,title}] and legacy urls. Auto-group even unlisted repositories; no manual entry. Actual titles only: nonempty refreshes, omitted/empty keeps. Deduplicate; explicit adds can restore removed links. Preserve aliases, refs and source snapshots. Replace/remove via todo_repositories_get and revision-checked set tools. Associations never prove completion.

```json
{
  "type": "object",
  "required": [
    "id"
  ],
  "properties": {
    "id": {
      "type": "string"
    },
    "urls": {
      "type": "array",
      "minItems": 1,
      "maxItems": 50,
      "items": {
        "type": "string"
      }
    },
    "links": {
      "type": "array",
      "maxItems": 50,
      "items": {
        "type": "object",
        "required": [
          "url"
        ],
        "properties": {
          "url": {
            "type": "string"
          },
          "title": {
            "type": "string",
            "description": "Full plain source title; ≤1,024 UTF-8 bytes after trimming.",
            "maxLength": 1024
          }
        },
        "additionalProperties": false
      }
    }
  },
  "additionalProperties": false,
  "anyOf": [
    {
      "required": [
        "urls"
      ]
    },
    {
      "required": [
        "links"
      ]
    }
  ]
}
```
