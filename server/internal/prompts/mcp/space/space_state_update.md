Replace the state block of this space's SPACE.md (the lines between the persona:managed:state markers) with text: a short snapshot of where things stand now, rewritten whole after a change that matters (current goals, what is in progress, what was decided, what comes next). Persona writes only that block, keeps the previous file in .space/history/, and never touches the user's part of SPACE.md; to change the user's part, edit the file only when the user asks. At most 2048 bytes, no persona:managed markers; an empty text empties the block. Only conversations inside a space have this tool.{{template "shared/mcp_language" .}}

```json
{"type":"object","required":["text"],"properties":{"text":{"type":"string","description":"The whole new state, plain Markdown, at most 2048 bytes. It replaces the block's old text."}},"additionalProperties":false}
```
