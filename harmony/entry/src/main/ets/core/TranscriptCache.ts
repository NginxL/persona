import type { TranscriptSnapshot, TranscriptItem } from './Domain';

// Stay well below the native RDB result-window limit, including four-byte Unicode.
export const TRANSCRIPT_CACHE_BYTES: number = 384 * 1024;
const MAX_ITEMS: number = 256;

export function utf8Bytes(value: string): number {
  let bytes = 0;
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code < 0x80) bytes++;
    else if (code < 0x800) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff && index + 1 < value.length &&
      value.charCodeAt(index + 1) >= 0xdc00 && value.charCodeAt(index + 1) <= 0xdfff) { bytes += 4; index++; }
    else bytes += 3;
  }
  return bytes;
}

/** Cache a complete suffix only; a partial message must never look like a final answer. */
export function boundedTranscriptCache(snapshot: TranscriptSnapshot): string {
  const items: TranscriptItem[] = [];
  let bytes = 1024;
  for (let index = snapshot.items.length - 1; index >= 0 && items.length < MAX_ITEMS; index--) {
    const item = snapshot.items[index];
    const size = utf8Bytes(JSON.stringify(item)) + 1;
    if (bytes + size > TRANSCRIPT_CACHE_BYTES) break;
    items.unshift(item); bytes += size;
  }
  const trimmed = items.length !== snapshot.items.length;
  const cached: TranscriptSnapshot = {
    items: items,
    covered: items.length > 0 ? snapshot.covered : 0,
    history: snapshot.history,
    hasMore: trimmed ? true : snapshot.hasMore,
    loadedOnce: items.length > 0 && snapshot.loadedOnce
  };
  return JSON.stringify(cached);
}
