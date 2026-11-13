import type { JsonObject, JsonValue, TranscriptItem } from './Domain';

/** The two restore modes accepted by POST /sessions/{machine}/{session}/rewind. */
export type SessionRevisionRestore = 'conversation' | 'code_and_conversation';

export class SessionRevisionPromptPart {
  id: string = '';
  kind: string = '';
  role: string = '';
  text: string = '';
  fullSize: string = '';
  queued: boolean = false;
  toolUseId: string = '';
}

/** One user message as Claude lists it. Multiple transcript items with one UUID form one prompt. */
export class SessionRevisionPrompt {
  entry: string = '';
  raw: string = '';
  cut: boolean = false;
  parts: SessionRevisionPromptPart[] = [];
}

export class SessionRevisionTarget {
  entry: string = '';
  prompt: string = '';
  occurrence: number = 0;
  promptsAfter: number = 0;
  cutItemId: string = '';
}

function fullSize(item: TranscriptItem): string {
  const value = item['full_size'];
  return typeof value === 'number' && value > 0 ? String(value) : '';
}

function queued(item: TranscriptItem): boolean {
  return item['queued'] === true;
}

function toolUseId(item: TranscriptItem): string {
  const snake = item.tool_use_id;
  if (snake !== undefined && snake !== null) return typeof snake === 'string' ? snake : String(snake);
  const camel = item.toolUseId;
  if (camel !== undefined && camel !== null) return typeof camel === 'string' ? camel : String(camel);
  return '';
}

function hasToolUseId(item: TranscriptItem): boolean {
  return (item.tool_use_id !== undefined && item.tool_use_id !== null) ||
    (item.toolUseId !== undefined && item.toolUseId !== null);
}

function promptPart(item: TranscriptItem): SessionRevisionPromptPart {
  const part = new SessionRevisionPromptPart();
  part.id = item.id;
  part.kind = item.kind;
  part.role = item.role || '';
  part.text = typeof item.text === 'string' ? item.text : '';
  part.fullSize = fullSize(item);
  part.queued = queued(item);
  part.toolUseId = toolUseId(item);
  return part;
}

/** `<transcript line UUID>#<part>` identifies a transcript line; other ids are synthetic notices. */
export function sessionRevisionEntry(id: string): string | undefined {
  const hash = id.indexOf('#');
  if (hash < 0) return undefined;
  const entry = id.slice(0, hash);
  if (!/^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/.test(entry)) return undefined;
  return entry;
}

/** Builds the same prompt list Claude sees, retaining raw tags and exact text. */
export function sessionRevisionPrompts(items: readonly TranscriptItem[]): SessionRevisionPrompt[] {
  const out: SessionRevisionPrompt[] = [];
  for (const item of items) {
    const entry = sessionRevisionEntry(item.id);
    if (item.role !== 'user' || queued(item) || (item.kind !== 'text' && item.kind !== 'image') || entry === undefined) continue;
    if (item.kind === 'image' && hasToolUseId(item)) continue;
    const text = item.kind === 'text' && typeof item.text === 'string' ? item.text : '';
    const last = out.length > 0 ? out[out.length - 1] : undefined;
    if (last !== undefined && last.entry === entry) {
      if (text.length > 0) last.raw += (last.raw.length > 0 ? '\n' : '') + text;
      if (fullSize(item).length > 0) last.cut = true;
      last.parts.push(promptPart(item));
      continue;
    }
    const prompt = new SessionRevisionPrompt();
    prompt.entry = entry;
    prompt.raw = text;
    prompt.cut = fullSize(item).length > 0;
    prompt.parts.push(promptPart(item));
    out.push(prompt);
  }
  return out;
}

function squash(value: string): string {
  return value.replace(/\s/gu, '');
}

/** Resolves a user message to Claude's raw prompt, reverse occurrence, and prompts-after count. */
export function sessionRevisionTarget(
  items: readonly TranscriptItem[],
  itemId: string,
  full?: string,
): SessionRevisionTarget | undefined {
  const entry = sessionRevisionEntry(itemId);
  if (entry === undefined) return undefined;
  const prompts = sessionRevisionPrompts(items);
  const at = prompts.findIndex((prompt: SessionRevisionPrompt) => prompt.entry === entry);
  if (at < 0) return undefined;
  const prompt = prompts[at].raw;
  const raw = full === undefined ? prompt : full;
  if (!raw.trim()) return undefined;
  const same = squash(raw);
  let occurrence = 1;
  for (let index = at + 1; index < prompts.length; index++) {
    if (squash(prompts[index].raw) === same) occurrence++;
  }
  const target = new SessionRevisionTarget();
  target.entry = entry;
  target.prompt = raw;
  target.occurrence = occurrence;
  target.promptsAfter = prompts.length - 1 - at;
  target.cutItemId = sessionRevisionCutItem(items, itemId);
  return target;
}

/** Returns the first text item of a cut user message so the caller can fetch its whole text. */
export function sessionRevisionCutItem(items: readonly TranscriptItem[], itemId: string): string {
  const entry = sessionRevisionEntry(itemId);
  if (entry === undefined) return '';
  const mine: TranscriptItem[] = [];
  let cut = false;
  for (const item of items) {
    if (item.role !== 'user' || item.kind !== 'text' || sessionRevisionEntry(item.id) !== entry) continue;
    mine.push(item);
    if (fullSize(item).length > 0) cut = true;
  }
  return cut && mine.length > 0 ? mine[0].id : '';
}

function fingerprintPart(item: TranscriptItem, full?: string, hydratedItemId: string = ''): string {
  const text = typeof item.text === 'string' ? item.text : '';
  const hydrated = full !== undefined && item.id === hydratedItemId;
  const values: string[] = [item.id, item.kind, item.role || '', hydrated ? '' : text,
    hydrated ? '' : fullSize(item), queued(item) ? '1' : '0', toolUseId(item), String(item.seq), hydrated ? 'hydrated' : ''];
  if (hydrated && fullSize(item).length === 0 && text !== full) values.push('hydrated_text_mismatch');
  return values.map((value: string) => String(value.length) + ':' + value).join('');
}

/**
 * Stable confirmation fence. A user target includes its prompt and every later user prompt because both
 * occurrence and prompts_after depend on that suffix. An assistant target includes its raw transcript line.
 */
export function sessionRevisionFingerprint(items: readonly TranscriptItem[], itemId: string, full?: string, hydratedItemId: string = ''): string {
  const entry = sessionRevisionEntry(itemId);
  if (entry === undefined) return '';
  const selected = items.find((item: TranscriptItem) => item.id === itemId);
  if (selected === undefined) return '';
  const parts: string[] = ['item', fingerprintPart(selected, full, hydratedItemId)];
  if (selected.role === 'user') {
    const prompts = sessionRevisionPrompts(items);
    const at = prompts.findIndex((prompt: SessionRevisionPrompt) => prompt.entry === entry);
    if (at < 0) return '';
    for (let index = at; index < prompts.length; index++) {
      const prompt = prompts[index];
      parts.push('prompt', prompt.entry, index === at && full !== undefined ? full : prompt.raw,
        index === at && full !== undefined ? 'hydrated' : prompt.cut ? '1' : '0');
      for (const promptItem of prompt.parts) parts.push(fingerprintPartFromPart(promptItem, full, hydratedItemId));
    }
  }
  return parts.map((value: string) => String(value.length) + ':' + value).join('');
}

function fingerprintPartFromPart(part: SessionRevisionPromptPart, full?: string, hydratedItemId: string = ''): string {
  const hydrated = full !== undefined && part.id === hydratedItemId;
  const values: string[] = [part.id, part.kind, part.role, hydrated ? '' : part.text, hydrated ? '' : part.fullSize,
    part.queued ? '1' : '0', part.toolUseId, hydrated ? 'hydrated' : ''];
  if (hydrated && part.fullSize.length === 0 && part.text !== full) values.push('hydrated_text_mismatch');
  return values.map((value: string) => String(value.length) + ':' + value).join('');
}

/** Allows the full-item endpoint's own transcript event while still detecting later prompt/target changes. */
export function sessionRevisionHydrationMatches(
  snapshot: readonly TranscriptItem[],
  current: readonly TranscriptItem[],
  itemId: string,
  full: string,
  hydratedItemId: string,
): boolean {
  const originalItem = snapshot.find((item: TranscriptItem) => item.id === hydratedItemId);
  const currentItem = current.find((item: TranscriptItem) => item.id === hydratedItemId);
  if (originalItem === undefined || currentItem === undefined || fullSize(originalItem).length === 0) return false;
  if (fullSize(currentItem).length > 0) {
    if (currentItem.text !== originalItem.text) return false;
  } else if (currentItem.text !== full) return false;
  const original = sessionRevisionTarget(snapshot, itemId, full);
  const latest = sessionRevisionTarget(current, itemId, full);
  if (original === undefined || latest === undefined || original.prompt !== latest.prompt ||
    original.occurrence !== latest.occurrence || original.promptsAfter !== latest.promptsAfter) return false;
  return sessionRevisionFingerprint(snapshot, itemId, full, hydratedItemId) ===
    sessionRevisionFingerprint(current, itemId, full, hydratedItemId);
}

function cloneJson(value: JsonValue | undefined): JsonValue | undefined {
  if (value === undefined || value === null || typeof value !== 'object') return value;
  if (Array.isArray(value)) {
    const out: JsonValue[] = [];
    for (const item of value) out.push(cloneJson(item) as JsonValue);
    return out;
  }
  const source = value as JsonObject;
  const out: Record<string, JsonValue | undefined> = {};
  for (const key of Object.keys(source)) out[key] = cloneJson(source[key]);
  return out as JsonObject;
}

/** Copies incoming raw transcript records so a confirmation uses an immutable snapshot. */
export function sessionRevisionSnapshot(items: readonly TranscriptItem[]): TranscriptItem[] {
  const out: TranscriptItem[] = [];
  for (const item of items) out.push(cloneJson(item as JsonValue) as TranscriptItem);
  return out;
}
