Cancel a queued phone write by its receipt id. Only writes that have not been sent can be cancelled. A delivering or uncertain write cannot be recalled; check the phone before requesting another write. Repeating cancellation of an already cancelled receipt is safe.{{template "shared/mcp_language" .}}

```json
{"type":"object","properties":{"id":{"type":"string","description":"Queue receipt id returned when the write was queued."}},"required":["id"],"additionalProperties":false}
```
