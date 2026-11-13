export interface AssistantCardSpan {
  key: string;
  start: number;
  end: number;
  complete: boolean;
  valid: boolean;
  fallback: string;
}

export class AssistantMessagePart {
  id: string;
  text: string;
  cardKey: string;
  constructor(id: string, text: string = '', cardKey: string = '') { this.id = id; this.text = text; this.cardKey = cardKey; }
}

/** Keep surrounding Markdown and the server's opaque block identity in their original order. */
export function assistantMessageParts(text: string, spans: AssistantCardSpan[], unavailable: string): AssistantMessagePart[] {
  const result: AssistantMessagePart[] = [];
  let cursor = 0;
  for (const span of spans) {
    if (!Number.isSafeInteger(span.start) || !Number.isSafeInteger(span.end) || span.start < cursor || span.end < span.start || span.end > text.length)
      return [new AssistantMessagePart('text:0', text)];
    if (span.start > cursor) result.push(new AssistantMessagePart('text:' + String(cursor), text.slice(cursor, span.start)));
    if (span.complete && span.valid) result.push(new AssistantMessagePart('card:' + span.key, '', span.key));
    else result.push(new AssistantMessagePart('fallback:' + span.key, span.fallback || unavailable));
    cursor = span.end;
  }
  if (cursor < text.length) result.push(new AssistantMessagePart('text:' + String(cursor), text.slice(cursor)));
  return result;
}
