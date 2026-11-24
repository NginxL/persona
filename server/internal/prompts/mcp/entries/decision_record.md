Record {text,scope?,decision:{options[],selected[],rejected[],reason,reversible:bool|null}}. user_quotes[]:{quote,source:{machine,session,msg_uuid|card_id|file,line}} stay inferred.{{template "shared/mcp_language" .}}

```json
{"type":"object","required":["entry"],"properties":{"entry":{"type":"object","required":["text","decision"],"properties":{"text":{"type":"string"},"decision":{"type":"object"}}},"user_quotes":{"type":"array","maxItems":20,"items":{"type":"object"}}},"additionalProperties":false}
```
