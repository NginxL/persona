import { canonicalPersonaLink } from '../core/WireCompatibility';
import { url, util } from '@kit.ArkTS';
import type { ExtensionPresentation } from '../core/ExtensionPresentation';
import { extensionUIActionSlotsForView } from '../core/ExtensionViewTree';
import type { ExtensionViewRecord } from '../core/ExtensionViewTree';
import type { ClientModel } from './ClientModel';
import type { AssistantFeedModel } from './AssistantFeed';
import { ResourceRow, Wire } from './Wire';
import type { HubRepository } from './HubRepository';

const MAX_VIEW_BYTES: number = 64 * 1024;
const MAX_CACHE_ENTRIES: number = 48;
const MAX_CACHE_BYTES: number = 5 * 1024 * 1024;
const MAX_DRAFT_BYTES: number = 512 * 1024;
const MAX_PAGE_DEPTH: number = 4;

export interface AssistantViewBlock {
  key: string;
  index: number;
  start: number;
  end: number;
  language: string;
  raw: string;
  complete: boolean;
  valid: boolean;
  view: ExtensionViewRecord;
  fallback: string;
}

export interface AssistantViewOpenTarget {
  kind: string;
  address: string;
}

export interface AssistantViewConfirmationLease {
  token: string;
  profileID: string;
  owner: string;
  conversation: string;
  key: string;
  generation: number;
  scope: string;
  target: string;
  activityVisit: number;
}

interface SafeViewResult {
  view: ExtensionViewRecord;
  fallback: string;
  valid: boolean;
}

interface FenceLine {
  character: string;
  length: number;
  info: string;
  valid: boolean;
}

interface AssistantField {
  name: string;
  key: string;
  kind: string;
  label: string;
  value: string;
  item: ExtensionViewRecord;
}

interface AssistantAttempt {
  action: string;
  selectionToken: string;
  page: string;
  values: Record<string, string>;
  clientMsgID: string;
  version: number;
  confirmed: boolean;
  path: string;
  body: string;
  state: string;
  sending: boolean;
  epoch: number;
}

interface AssistantSelection {
  token: string;
  actionKey: string;
  action: ExtensionViewRecord;
  page: string;
  values: Record<string, string>;
  version: number;
  message: string;
  needsConfirmation: boolean;
  profileID: string;
  owner: string;
  conversation: string;
  key: string;
  generation: number;
  scope: string;
  target: string;
  activityVisit: number;
}

interface AssistantActionCompletion {
  resolve: () => void;
  reject: (error: Error) => void;
  taken: boolean;
  accepted: boolean;
}

function segment(value: string): string { return encodeURIComponent(value); }
function array(value: Object | undefined): Object[] { return Array.isArray(value) ? value as Object[] : []; }
function recordValues(value: Object | undefined): Record<string, Object> { return Wire.record(value); }
function stringValues(value: Record<string, Object>): Record<string, string> {
  const result: Record<string, string> = {};
  for (const key of Object.keys(value)) if (typeof value[key] === 'string') result[key] = value[key] as string;
  return result;
}
function utf8Length(value: string): number { return new util.TextEncoder().encodeInto(value).byteLength; }
function stableJSON(value: Object): string {
  if (value === null) return 'null';
  if (Array.isArray(value)) {
    const items = value as Object[];
    const parts: string[] = [];
    for (let index = 0; index < items.length; index++) parts.push(stableJSON(items[index]));
    return '[' + parts.join(',') + ']';
  }
  if (typeof value === 'object') {
    const record = value as Record<string, Object>;
    const keys = Object.keys(record).sort();
    const parts: string[] = [];
    for (const key of keys) parts.push(JSON.stringify(key) + ':' + stableJSON(record[key]));
    return '{' + parts.join(',') + '}';
  }
  const encoded = JSON.stringify(value);
  return typeof encoded === 'string' ? encoded : '';
}
function has(value: Record<string, Object>, key: string): boolean { return Object.keys(value).includes(key); }
function safePathPart(value: string): boolean {
  return !!value && value !== '.' && value !== '..' && !/[\\\u0000-\u001f\u007f]/.test(value);
}

function unsafePath(value: string): boolean {
  let current = value;
  for (let depth = 0; depth < 3; depth++) {
    if (/[\\\u0000-\u001f\u007f]/.test(current) || current.split('/').some((part: string) => part === '.' || part === '..')) return true;
    if (!/%[0-9a-fA-F]{2}/.test(current)) return false;
    try { current = decodeURIComponent(current); } catch (_) { return false; }
  }
  return /[\\\u0000-\u001f\u007f]/.test(current) || current.split('/').some((part: string) => part === '.' || part === '..');
}

function encodedSeparator(value: string): boolean {
  let current = value;
  for (let depth = 0; depth < 3; depth++) {
    if (current.includes('/')) return true;
    if (!/%[0-9a-fA-F]{2}/.test(current)) return false;
    try { current = decodeURIComponent(current); } catch (_) { return false; }
  }
  return current.includes('/');
}

function fenceLine(raw: string): FenceLine {
  const line = raw.replace(/[\r\n]+$/, '');
  const indent = line.length - line.replace(/^ +/, '').length;
  if (indent > 3) return { character: '', length: 0, info: '', valid: false };
  const text = line.slice(indent);
  if (text.length < 3 || (text[0] !== '`' && text[0] !== '~')) return { character: '', length: 0, info: '', valid: false };
  const character = text[0];
  let count = 0;
  while (count < text.length && text[count] === character) count++;
  if (count < 3) return { character: '', length: 0, info: '', valid: false };
  const info = text.slice(count).trim();
  if (character === '`' && info.includes('`')) return { character: '', length: 0, info: '', valid: false };
  const first = info.split(/\s+/)[0] || '';
  return { character: character, length: count, info: first.toLowerCase(), valid: true };
}

function normalizeView(value: Record<string, Object>): ExtensionViewRecord | undefined {
  if (!Array.isArray(value['sections'])) {
    if (!Wire.text(value, 'type')) return undefined;
    const component: Record<string, Object> = {};
    for (const name of Object.keys(value)) if (name !== 'fallback') component[name] = value[name];
    const section = Wire.payload(['items'], [[component]]);
    const wrapped = Wire.payload(['fallback', 'sections'], [Wire.text(value, 'fallback'), [section]]);
    return wrapped;
  }
  return value;
}

function safeFallback(value: Record<string, Object>): string {
  const fallback = Wire.text(value, 'fallback');
  return fallback && Array.from(fallback).length <= 2000 ? fallback : '';
}

function quotedEnd(raw: string, start: number): number {
  if (raw[start] !== '"') return -1;
  for (let index = start + 1; index < raw.length; index++) {
    if (raw[index] === '\\') { index++; continue; }
    if (raw[index] === '"') return index + 1;
  }
  return -1;
}

function skipJSONValue(raw: string, start: number): number {
  let index = start;
  while (index < raw.length && /\s/.test(raw[index])) index++;
  if (raw[index] === '"') return quotedEnd(raw, index);
  if (raw[index] === '{' || raw[index] === '[') {
    const stack: string[] = [raw[index] === '{' ? '}' : ']'];
    let quoted = false;
    for (index++; index < raw.length; index++) {
      const character = raw[index];
      if (quoted) {
        if (character === '\\') index++;
        else if (character === '"') quoted = false;
      } else if (character === '"') quoted = true;
      else if (character === '{') stack.push('}');
      else if (character === '[') stack.push(']');
      else if ((character === '}' || character === ']') && stack[stack.length - 1] === character) {
        stack.pop(); if (!stack.length) return index + 1;
      }
    }
    return -1;
  }
  while (index < raw.length && raw[index] !== ',' && raw[index] !== '}') index++;
  return index;
}

function fallbackFromRaw(raw: string): string {
  if (!raw || utf8Length(raw) > MAX_VIEW_BYTES) return '';
  let index = 0;
  while (index < raw.length && /\s/.test(raw[index])) index++;
  if (raw[index] !== '{') return '';
  index++;
  while (index < raw.length) {
    while (index < raw.length && /[\s,]/.test(raw[index])) index++;
    if (raw[index] !== '"') return '';
    const keyEnd = quotedEnd(raw, index);
    if (keyEnd < 0) return '';
    let key = '';
    try { key = JSON.parse(raw.slice(index, keyEnd)) as string; } catch (_) { return ''; }
    index = keyEnd;
    while (index < raw.length && /\s/.test(raw[index])) index++;
    if (raw[index] !== ':') return '';
    index++;
    while (index < raw.length && /\s/.test(raw[index])) index++;
    if (key === 'fallback') {
      const end = quotedEnd(raw, index);
      if (end < 0) return '';
      try {
        const value: Object = JSON.parse(raw.slice(index, end)) as Object;
        if (typeof value === 'string' && Array.from(value as string).length <= 2000) return value as string;
      } catch (_) { return ''; }
      return '';
    }
    const next = skipJSONValue(raw, index);
    if (next < 0 || next === index) return '';
    index = next;
    while (index < raw.length && /\s/.test(raw[index])) index++;
    if (raw[index] === '}') return '';
  }
  return '';
}

function invalidSafeView(fallback: string = ''): SafeViewResult {
  return { view: Wire.payload([], []), fallback: fallback, valid: false };
}

function safeView(raw: string): SafeViewResult {
  if (!raw || utf8Length(raw) > MAX_VIEW_BYTES) return invalidSafeView();
  try {
    const parsed: Object = JSON.parse(raw) as Object;
    const source = Wire.record(parsed);
    const candidate = normalizeView(source);
    if (!candidate) return invalidSafeView(safeFallback(source));
    const pages = Wire.object(candidate, 'pages');
    if (Object.keys(pages).length > 8) return invalidSafeView(safeFallback(candidate));
    const pagesValid = Object.keys(pages).every((name: string) => {
      const page = Wire.record(pages[name]);
      if (Object.keys(Wire.object(page, 'pages')).length > 0 || !Array.isArray(page['sections'])) return false;
      return !containsSecret(page);
    });
    if (!pagesValid || containsSecret(candidate)) return invalidSafeView(safeFallback(candidate));
    const fallback = safeFallback(candidate);
    if (!fallback) return invalidSafeView();
    return { view: candidate, fallback: fallback, valid: true };
  } catch (_) { return invalidSafeView(fallbackFromRaw(raw)); }
}

function containsSecret(view: Record<string, Object>): boolean {
  for (const sectionValue of array(view['sections'])) {
    const section = Wire.record(sectionValue);
    for (const itemValue of array(section['items'])) {
      const item = Wire.record(itemValue);
      if (Wire.text(item, 'type') === 'field' && Wire.text(item, 'kind') === 'secret') return true;
    }
  }
  return false;
}

/** Recognize the shared standalone-fence format; invalid blocks still occupy their stable ordinal. */
export function parseAssistantViewBlocks(itemID: string, text: string): AssistantViewBlock[] {
  const result: AssistantViewBlock[] = [];
  if (!itemID || !text) return result;
  let fenceCharacter = '';
  let fenceLength = 0;
  let language = '';
  let start = 0;
  let contentStart = 0;
  let viewIndex = 0;
  let position = 0;
  while (position < text.length) {
    let end = position;
    while (end < text.length && text[end] !== '\r' && text[end] !== '\n') end++;
    if (end < text.length) {
      if (text[end] === '\r' && text[end + 1] === '\n') end += 2;
      else end++;
    }
    const lineEnd = end;
    const parsed = fenceLine(text.slice(position, lineEnd));
    if (!fenceCharacter) {
      if (parsed.valid) {
        fenceCharacter = parsed.character;
        fenceLength = parsed.length;
        language = parsed.info;
        start = position;
        contentStart = lineEnd;
      }
    } else if (parsed.valid && parsed.character === fenceCharacter && parsed.length >= fenceLength && !parsed.info) {
      if (language === 'persona-view') {
        const index = viewIndex++;
        const raw = text.slice(contentStart, position);
        const loaded: SafeViewResult = index < 3 ? safeView(raw) : invalidSafeView();
        result.push({ key: itemID + '/' + String(index), index: index, start: start, end: lineEnd, language: language, raw: raw,
          complete: true, valid: loaded.valid, view: loaded.view, fallback: loaded.fallback });
      }
      fenceCharacter = '';
      fenceLength = 0;
      language = '';
      start = 0;
      contentStart = 0;
    }
    position = lineEnd;
  }
  if (fenceCharacter && language === 'persona-view') {
    const index = viewIndex;
    const raw = text.slice(contentStart);
    const loaded: SafeViewResult = index < 3 ? safeView(raw) : invalidSafeView();
    result.push({ key: itemID + '/' + String(index), index: index, start: start, end: text.length, language: language, raw: raw,
      complete: false, valid: loaded.valid, view: loaded.view, fallback: loaded.fallback });
  }
  return result;
}

export function assistantViewOpenTarget(raw: string): AssistantViewOpenTarget | undefined {
  if (!raw || raw.length > 4096 || /[\\\u0000-\u001f\u007f]/.test(raw)) return undefined;
  if (/%(?![0-9a-fA-F]{2})/.test(raw)) return undefined;
  try { if (/[\\\u0000-\u001f\u007f]/.test(decodeURIComponent(raw))) return undefined; } catch (_) { return undefined; }
  let parsed: url.URL;
  try { parsed = url.URL.parseURL(raw); } catch (_) { return undefined; }
  if (parsed.username || parsed.password || parsed.hostname === '') return undefined;
  if (parsed.protocol.toLowerCase() === 'https:') return { kind: 'link', address: raw };
  const canonical = canonicalPersonaLink(raw);
  if (!canonical || parsed.search || parsed.hash) return undefined;
  const rest = canonical.slice('persona://'.length);
  const slash = rest.indexOf('/');
  if (slash <= 0) return undefined;
  const host = rest.slice(0, slash).toLowerCase();
  const encoded = rest.slice(slash + 1);
  if (!encoded || encoded.endsWith('/')) return undefined;
  const segments = encoded.split('/');
  const decoded: string[] = [];
  try { for (const part of segments) decoded.push(decodeURIComponent(part)); } catch (_) { return undefined; }
  if (decoded.some((part: string) => !safePathPart(part) || unsafePath(part))) return undefined;
  if (host === 'session' && decoded.length === 2 && decoded.every((part: string) => !encodedSeparator(part)))
    return { kind: 'resource', address: canonical };
  if (host === 'task' && decoded.length === 1 && !encodedSeparator(decoded[0])) return { kind: 'resource', address: canonical };
  if (host === 'kb' && decoded.length >= 2 && !encodedSeparator(decoded[0]) &&
    decoded.slice(1).every((part: string) => !encodedSeparator(part)) &&
    ['projects', 'chats', 'people', 'docs', 'meetings', 'events', 'recordings'].includes(decoded[0]))
    return { kind: 'resource', address: canonical };
  if (host === 'file' && decoded.length === 2 && !encodedSeparator(decoded[0]) && decoded[1].startsWith('/')) {
    const path = decoded.slice(1).join('/');
    if (!path || unsafePath(path)) return undefined;
    return { kind: 'resource', address: canonical };
  }
  return undefined;
}

function actionMode(action: Record<string, Object>): string {
  let count = 0;
  let mode = '';
  if (Wire.text(action, 'reply')) { count++; mode = 'reply'; }
  if (Wire.text(action, 'open')) { count++; mode = 'open'; }
  if (Wire.text(Wire.object(action, 'push'), 'page')) { count++; mode = 'push'; }
  if (Wire.bool(action, 'pop')) { count++; mode = 'pop'; }
  return count === 1 && (!Wire.bool(action, 'submit') || mode === 'reply') ? mode : '';
}

function dateValue(kind: string, value: string): boolean {
  if (kind === 'date') {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
    const parsed = new Date(value + 'T00:00:00.000Z');
    return !Number.isNaN(parsed.getTime()) && parsed.toISOString().slice(0, 10) === value;
  }
  if (kind === 'time') return /^(?:[01]\d|2[0-3]):[0-5]\d$/.test(value);
  if (kind === 'datetime') {
    if (!/^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3]):[0-5]\d$/.test(value)) return false;
    const parsed = new Date(value + ':00.000Z');
    return !Number.isNaN(parsed.getTime()) && parsed.toISOString().slice(0, 16) === value;
  }
  return true;
}

function copyStrings(values: Record<string, string>): Record<string, string> {
  const next: Record<string, string> = {};
  for (const key of Object.keys(values)) next[key] = values[key];
  return next;
}

/** Bounded, volatile per-owner presentation and action adapter for the shared native ExtensionView renderer. */
@ObservedV2
export class AssistantViewModel implements ExtensionPresentation {
  private static cache: Map<string, AssistantViewModel> = new Map();
  private static cacheClock: number = 0;

  @Trace busy: boolean = false;
  @Trace revision: number = 0;
  @Trace viewVersion: number = 1;
  @Trace lineResultRevision: number = 0;
  @Trace complete: boolean = false;
  @Trace valid: boolean = false;
  @Trace registered: boolean = false;
  @Trace active: boolean = false;
  @Trace pageStack: string[] = [];
  @Trace error: string = '';
  @Trace acknowledged: boolean = false;
  @Trace locked: boolean = false;
  @Trace actionRevision: number = 0;
  @Trace confirmationRevision: number = 0;
  @Trace effectRevision: number = 0;
  @Trace changedNeedsConfirmation: boolean = false;
  @Trace effect: Record<string, Object> = {};

  private rootView: ExtensionViewRecord = {};
  private fallback: string = '';
  private drafts: Record<string, Object> = {};
  private selection: AssistantSelection | undefined = undefined;
  private pendingActions: Map<string, AssistantActionCompletion> = new Map();
  private attempt: AssistantAttempt | undefined = undefined;
  private actionRecord: Record<string, Object> | undefined = undefined;
  private client: ClientModel | undefined = undefined;
  private feed: AssistantFeedModel | undefined = undefined;
  private repository: HubRepository | undefined = undefined;
  private item: Record<string, Object> = {};
  private itemSignature: string = '';
  private key: string = '';
  private profileID: string = '';
  private owner: string = '';
  private conversation: string = '';
  private scopeKey: string = '';
  private targetKey: string = '';
  private clientGeneration: number = -1;
  private visit: number = 0;
  private activityVisit: number = 0;
  private cardSize: number = 0;
  private forcedReadOnly: boolean = false;
  private drafted: boolean = false;

  private constructor() {}

  static acquire(client: ClientModel, feed: AssistantFeedModel, item: Record<string, Object>, key: string,
    active: boolean): AssistantViewModel | undefined {
    const owner = feed.owner || client.profile.manager;
    const conversation = feed.conversationId;
    if (!client.profile.id || !owner || owner !== client.profile.manager || !conversation || !key) return undefined;
    const identity = JSON.stringify([client.profile.id, owner, conversation, key]);
    let presentation = AssistantViewModel.cache.get(identity);
    if (!presentation) {
      if (!AssistantViewModel.makeRoom(MAX_VIEW_BYTES + 2048, true)) return undefined;
      presentation = new AssistantViewModel();
      presentation.profileID = client.profile.id;
      presentation.owner = owner;
      presentation.conversation = conversation;
      presentation.key = key;
      AssistantViewModel.cache.set(identity, presentation);
    }
    presentation.bind(client, feed, item, active);
    presentation.visit = ++AssistantViewModel.cacheClock;
    presentation.cardSize = presentation.estimatedSize();
    if (!AssistantViewModel.makeRoom(0, false, presentation)) {
      if (presentation.attempt || presentation.selection || presentation.pendingActions.size > 0 || presentation.drafted) return presentation;
      AssistantViewModel.discard(presentation);
      presentation.hide();
      return undefined;
    }
    return presentation;
  }

  static forgetProfile(profileID: string): void {
    if (!profileID) return;
    const presentations: AssistantViewModel[] = [];
    for (const identity of AssistantViewModel.cache.keys()) {
      const presentation = AssistantViewModel.cache.get(identity);
      if (presentation) presentations.push(presentation);
    }
    for (const presentation of presentations) presentation.forget(profileID);
  }

  private static makeRoom(requiredBytes: number = 0, reserveSlot: boolean = false,
    exclude: AssistantViewModel | undefined = undefined): boolean {
    while ((reserveSlot && AssistantViewModel.cache.size >= MAX_CACHE_ENTRIES) ||
      AssistantViewModel.cache.size > MAX_CACHE_ENTRIES || AssistantViewModel.cacheBytes() + requiredBytes > MAX_CACHE_BYTES) {
      let victimKey = '';
      let oldest = Number.MAX_SAFE_INTEGER;
      for (const identity of AssistantViewModel.cache.keys()) {
        const candidate = AssistantViewModel.cache.get(identity);
        if (!candidate) continue;
        if (candidate === exclude) continue;
        if (candidate.active || candidate.busy || candidate.attempt || candidate.selection || candidate.pageStack.length > 0 ||
          candidate.drafted || candidate.changedNeedsConfirmation || candidate.locked && !candidate.actionRecord) continue;
        if (candidate.visit < oldest) { oldest = candidate.visit; victimKey = identity; }
      }
      if (!victimKey) return false;
      AssistantViewModel.cache.delete(victimKey);
    }
    return true;
  }

  private static discard(model: AssistantViewModel): void {
    const identities = Array.from(AssistantViewModel.cache.keys());
    for (const identity of identities) if (AssistantViewModel.cache.get(identity) === model) AssistantViewModel.cache.delete(identity);
  }

  private static cacheBytes(): number {
    let total = 0;
    for (const candidate of AssistantViewModel.cache.values()) total += candidate.cardSize;
    return total;
  }

  private estimatedSize(rootView: ExtensionViewRecord = this.rootView,
    drafts: Record<string, Object> = this.drafts): number {
    let total = 1024;
    try { total += utf8Length(JSON.stringify(rootView)); } catch (_) { total += MAX_VIEW_BYTES; }
    try { total += utf8Length(JSON.stringify(drafts)); } catch (_) { total += MAX_DRAFT_BYTES; }
    if (this.attempt) total += utf8Length(this.attempt.body) + 512;
    if (this.actionRecord) total += utf8Length(JSON.stringify(this.actionRecord));
    return total;
  }

  private row(): ResourceRow {
    const selected = this.client?.selected;
    if (selected && selected.kind === 'conversation' && selected.id === this.conversation &&
      (!selected.group || selected.group === this.profileID) && (!selected.manager || selected.manager === this.owner)) return selected;
    const row = new ResourceRow(this.conversation, '', 'conversation');
    row.manager = this.owner;
    row.group = this.profileID;
    return row;
  }

  private bind(client: ClientModel, feed: AssistantFeedModel, item: Record<string, Object>, active: boolean): void {
    const itemSignature = stableJSON(item as Object);
    const bindingChanged = this.client !== client || this.feed !== feed || this.itemSignature !== itemSignature ||
      this.clientGeneration !== client.connectionGeneration() || this.scopeKey !== client.scope() ||
      this.targetKey !== client.target(this.row()) || this.forcedReadOnly !== !client.connected;
    if (bindingChanged) {
      this.activityVisit++;
      if (this.selection) this.cancelSelection(new Error('assistant_view_changed'));
      else this.confirmationRevision++;
      this.rejectPendingActions(new Error('assistant_view_changed'));
      if (this.attempt?.sending) {
        this.attempt.sending = false;
        this.attempt.state = 'uncertain';
        this.attempt.epoch = ++this.visit;
        this.busy = false;
        this.error = 'assistant_view_result_unknown';
      }
      this.client = client;
      this.feed = feed;
      try { this.repository = client.connected ? client.repo() : undefined; }
      catch (_) { this.repository = undefined; }
      this.item = item;
      this.itemSignature = itemSignature;
      this.clientGeneration = client.connectionGeneration();
      this.scopeKey = client.scope();
      this.targetKey = client.target(this.row());
      this.forcedReadOnly = !client.connected;
    }
    const activeChanged = this.active !== active;
    if (activeChanged) this.setVisible(active);
    if (!activeChanged || !active) this.mergeSources();
  }

  private ownerCurrent(generation: number = this.clientGeneration, scope: string = this.scopeKey,
    target: string = this.targetKey): boolean {
    const client = this.client;
    const feed = this.feed;
    if (!client || !feed) return false;
    return client.profile.id === this.profileID && client.profile.manager === this.owner &&
      (feed.owner || client.profile.manager) === this.owner && feed.conversationId === this.conversation &&
      client.owns(generation, scope) && client.scope() === scope && client.target() === target &&
      client.target(this.row()) === target;
  }

  private admitted(generation: number = this.clientGeneration, scope: string = this.scopeKey,
    target: string = this.targetKey): boolean {
    return this.active && this.ownerCurrent(generation, scope, target) && !!this.repository &&
      !!this.client && this.client.connected && !this.forcedReadOnly;
  }

  private sourceBlock(): AssistantViewBlock | undefined {
    if (this.key.startsWith('view_') || Wire.text(this.item, 'role') !== 'assistant') return undefined;
    const id = Wire.text(this.item, 'item_id', Wire.text(this.item, 'itemId', Wire.text(this.item, 'id')));
    const body = Wire.object(this.item, 'body');
    const text = Wire.text(this.item, 'text', Wire.text(body, 'text', Wire.text(this.item, 'line')));
    return parseAssistantViewBlocks(id, text).find((block: AssistantViewBlock) => block.key === this.key);
  }

  private mergeSources(): void {
    const feed = this.feed;
    const envelope = Wire.record(feed ? feed.views[this.key] : undefined);
    const envelopeID = Wire.text(envelope, 'id');
    const conversation = Wire.text(envelope, 'conversation');
    const version = Wire.number(envelope, 'version');
    let candidate: ExtensionViewRecord = {};
    let nextFallback = '';
    let nextComplete = true;
    let nextValid = false;
    let nextVersion = 1;
    if (envelopeID === this.key && (!conversation || conversation === this.conversation) && version > 0) {
      const view = Wire.object(envelope, 'view');
      const checked = safeView(JSON.stringify(view));
      candidate = checked.view; nextFallback = checked.fallback; nextValid = checked.valid;
      nextComplete = true; nextVersion = version;
    } else {
      const block = this.sourceBlock();
      const body = Wire.object(this.item, 'body');
      let direct = Wire.object(body, 'view');
      if (!Object.keys(direct).length) direct = Wire.object(this.item, 'view');
      const trustedInline = !this.key.startsWith('view_') && Wire.text(this.item, 'role') === 'assistant' &&
        (!Wire.text(this.item, 'kind') || Wire.text(this.item, 'kind') === 'text');
      if (trustedInline && block && block.valid) { candidate = block.view; nextFallback = block.fallback; nextComplete = block.complete; nextValid = block.valid; }
      else if (trustedInline && Object.keys(direct).length) {
        const checked = safeView(JSON.stringify(direct)); candidate = checked.view; nextFallback = checked.fallback;
        nextValid = checked.valid; nextComplete = Wire.bool(this.item, 'complete', true);
      } else {
        nextComplete = block ? block.complete : false;
        nextFallback = block?.fallback || Wire.text(body, 'fallback', Wire.text(this.item, 'fallback'));
      }
      nextVersion = 1;
    }
    if (nextValid && (nextVersion > this.viewVersion || !this.valid)) {
      if (!this.replaceView(candidate, nextVersion)) {
        this.mergeActionRecord(feed ? Wire.record(feed.viewActions[this.key]) : Wire.payload([], []));
        const actionPath = this.actionPath();
        this.locked = this.locked || (!!this.client && this.client.operationBlocked(actionPath) && !this.attempt && !this.actionRecord);
        this.revision++;
        return;
      }
      this.valid = true;
      this.fallback = nextFallback;
    } else if (!this.valid && nextFallback) this.fallback = nextFallback;
    this.complete = nextComplete;
    if (this.key.startsWith('view_')) this.registered = true;
    else if (feed && feed.viewBlocks.includes(this.key)) this.registered = true;
    this.mergeActionRecord(feed ? Wire.record(feed.viewActions[this.key]) : {});
    const actionPath = this.actionPath();
    this.locked = this.locked || (!!this.client && this.client.operationBlocked(actionPath) && !this.attempt && !this.actionRecord);
    this.revision++;
  }

  private replaceView(next: ExtensionViewRecord, version: number): boolean {
    if (version < this.viewVersion) return false;
    const prior = this.rootView;
    const changed = this.valid && version > this.viewVersion;
    const nextDrafts = this.rebuiltDrafts(prior, next);
    const nextSize = this.estimatedSize(next, nextDrafts);
    if (nextSize > MAX_CACHE_BYTES || !AssistantViewModel.makeRoom(Math.max(0, nextSize - this.cardSize), false, this)) {
      this.error = 'assistant_view_cache_full';
      this.revision++;
      return false;
    }
    this.rootView = next;
    this.viewVersion = version;
    this.drafts = nextDrafts;
    this.cardSize = nextSize;
    if (changed) {
      if (this.selection) this.cancelSelection(new Error('assistant_view_changed'));
      else this.confirmationRevision++;
      this.changedNeedsConfirmation = true;
      this.error = this.attempt ? this.error : 'assistant_view_changed';
    }
    const pages = Wire.object(next, 'pages');
    this.pageStack = this.pageStack.filter((name: string) => has(pages, name));
    this.revision++;
    return true;
  }

  private fields(view: ExtensionViewRecord, existing: Record<string, Object> = {}): Record<string, AssistantField> {
    const result: Record<string, AssistantField> = {};
    const sections = array(view['sections']);
    for (let si = 0; si < sections.length; si++) {
      const section = Wire.record(sections[si]);
      const items = array(section['items']);
      for (let ii = 0; ii < items.length; ii++) {
        const item = Wire.record(items[ii]);
        const type = Wire.text(item, 'type');
        const name = Wire.text(item, 'name');
        if (!name || type !== 'field' || Wire.text(item, 'kind') === 'secret') continue;
        const key = Wire.text(item, 'key', '@item:' + String(si) + ':' + String(ii));
        const kind = Wire.text(item, 'kind', 'text');
        let value = Wire.text(item, 'value');
        if (kind === 'toggle' && value !== 'true') value = 'false';
        if (kind === 'multichoice' && !value) value = '[]';
        if (kind === 'choice' && !value) value = Wire.text(Wire.record(array(item['choices'])[0]), 'value');
        const field: AssistantField = { name: name, key: key, kind: kind, label: Wire.text(item, 'label', name), value: value, item: item };
        const previous = Wire.record(existing[key]);
        if (Wire.text(previous, 'name') === name && Wire.text(previous, 'kind') === kind && Wire.has(previous, 'value'))
          field.value = Wire.text(previous, 'value');
        result[name] = field;
      }
    }
    return result;
  }

  private rebuiltDrafts(previous: ExtensionViewRecord, next: ExtensionViewRecord): Record<string, Object> {
    const oldDrafts = this.drafts;
    const nextDrafts: Record<string, Object> = {};
    const pages = Wire.object(next, 'pages');
    const oldPages = Wire.object(previous, 'pages');
    const pageNames = [''];
    for (const name of Object.keys(pages)) pageNames.push(name);
    for (const pageName of pageNames) {
      const view = pageName ? Wire.record(pages[pageName]) : next;
      const oldView = pageName ? Wire.record(oldPages[pageName]) : previous;
      const oldPageDraft = Wire.object(oldDrafts, pageName);
      const previousFields = this.fields(oldView, oldPageDraft);
      const newFields = this.fields(view);
      const nextPage: Record<string, Object> = {};
      for (const name of Object.keys(newFields)) {
        const current = newFields[name];
        const old = previousFields[name];
        const oldDraft = old && old.key === current.key && old.kind === current.kind && has(oldPageDraft, current.key)
          ? oldPageDraft[current.key] : current.value;
        nextPage[current.key] = typeof oldDraft === 'string' ? oldDraft as string : current.value;
      }
      nextDrafts[pageName] = nextPage;
    }
    return nextDrafts;
    this.syncActionValues();
  }

  private syncActionValues(): void {
    const action = this.actionRecord;
    if (!action) return;
    const pageName = Wire.text(action, 'page');
    const view = this.pageView(pageName);
    const fields = this.fields(view);
    const saved = Wire.record(action['values']);
    const oldPage = Wire.object(this.drafts, pageName);
    const nextPage: Record<string, Object> = {};
    for (const key of Object.keys(oldPage)) nextPage[key] = oldPage[key];
    for (const name of Object.keys(fields)) {
      if (typeof saved[name] === 'string') nextPage[fields[name].key] = saved[name] as string;
    }
    this.drafts[pageName] = nextPage;
  }

  private pageView(pageName: string = this.pageStack.length ? this.pageStack[this.pageStack.length - 1] : ''): ExtensionViewRecord {
    if (!pageName) return this.rootView;
    return Wire.object(Wire.object(this.rootView, 'pages'), pageName);
  }

  get view(): ExtensionViewRecord { return this.pageView(); }
  get currentPage(): string { return this.pageStack.length ? this.pageStack[this.pageStack.length - 1] : ''; }
  get pageTitle(): string { return Wire.text(this.view, 'title', Wire.text(this.rootView, 'title')); }
  get fallbackText(): string { return this.fallback || Wire.text(this.rootView, 'fallback'); }
  get selectionSnapshot(): Record<string, Object> {
    if (!this.selection) return Wire.payload([], []);
    return Wire.payload(['token', 'action', 'page', 'version', 'message'], [this.selection.token, this.selection.action,
      this.selection.page, this.selection.version, this.selection.message]);
  }
  confirmationLease(token: string): AssistantViewConfirmationLease | undefined {
    const selection = this.selection;
    if (!selection || selection.token !== token) return undefined;
    return { token: selection.token, profileID: selection.profileID, owner: selection.owner,
      conversation: selection.conversation, key: selection.key, generation: selection.generation,
      scope: selection.scope, target: selection.target, activityVisit: selection.activityVisit };
  }
  confirmationLeaseIsCurrent(lease: AssistantViewConfirmationLease): boolean {
    const selection = this.selection;
    return !!selection && selection.token === lease.token && this.leaseMatchesSelection(selection, lease) &&
      this.selectionIsCurrent(selection);
  }
  selectionMatchesLease(value: Record<string, Object>, lease: AssistantViewConfirmationLease): boolean {
    const selection = value as Object as AssistantSelection;
    return !!selection && this.leaseMatchesSelection(selection, lease) && this.selectionIsCurrent(selection);
  }
  get hasActionRecord(): boolean { return !!this.actionRecord; }
  get actionState(): string { return Wire.text(this.actionRecord || {}, 'status'); }
  get actionError(): string { return Wire.text(this.actionRecord || {}, 'error'); }
  get canRetryOriginal(): boolean { return !!this.attempt && !this.attempt.sending && this.attempt.state === 'uncertain' && !this.acknowledged; }
  get selectedSummaryKey(): string {
    if (this.actionRecord) return 'assistant_view_selected_' + (['pending', 'sent', 'failed'].includes(this.actionState) ? this.actionState : 'unknown');
    if (this.acknowledged) return 'assistant_view_syncing';
    if (this.locked) return 'assistant_view_syncing';
    return '';
  }

  private leaseMatchesSelection(selection: AssistantSelection, lease: AssistantViewConfirmationLease): boolean {
    return selection.token === lease.token && selection.profileID === lease.profileID && selection.owner === lease.owner &&
      selection.conversation === lease.conversation && selection.key === lease.key &&
      selection.generation === lease.generation && selection.scope === lease.scope && selection.target === lease.target &&
      selection.activityVisit === lease.activityVisit;
  }

  private selectionIsCurrent(selection: AssistantSelection): boolean {
    return selection.profileID === this.profileID && selection.owner === this.owner &&
      selection.conversation === this.conversation && selection.key === this.key &&
      selection.generation === this.clientGeneration && selection.scope === this.scopeKey &&
      selection.target === this.targetKey && selection.activityVisit === this.activityVisit &&
      selection.version === this.viewVersion && selection.page === this.currentPage && this.active &&
      this.complete && this.valid && this.registered && this.ownerCurrent(selection.generation, selection.scope, selection.target);
  }

  canInteract(): boolean {
    return this.active && this.ownerCurrent() && this.complete && this.valid && this.registered && !this.selection;
  }

  canDispatchEffects(): boolean { return this.interactionCurrent(); }

  back(): void {
    if (!this.pageStack.length || !this.interactionCurrent()) return;
    this.pageStack = this.pageStack.slice(0, -1);
    this.selection = undefined;
    this.confirmationRevision++;
    this.revision++;
  }

  fieldValue(key: string, fallback: string = ''): string {
    if (this.actionRecord && Wire.text(this.actionRecord, 'page') === this.currentPage) {
      const fields = this.fields(this.view);
      for (const name of Object.keys(fields)) if (fields[name].key === key) {
        const saved = Wire.text(Wire.record(this.actionRecord['values']), name, '\u0000');
        if (saved !== '\u0000') return saved;
      }
    }
    const values = Wire.object(this.drafts, this.currentPage);
    return typeof values[key] === 'string' ? values[key] as string : fallback;
  }

  setFieldValue(key: string, value: string, secret: boolean = false): void {
    if (secret || !this.canInteract() || this.actionRecord || this.attempt || this.locked ||
      Array.from(value).length > 2000) return;
    const nextPage: Record<string, Object> = {};
    const current = Wire.object(this.drafts, this.currentPage);
    for (const name of Object.keys(current)) nextPage[name] = current[name];
    nextPage[key] = value;
    const nextDrafts: Record<string, Object> = {};
    for (const page of Object.keys(this.drafts)) nextDrafts[page] = this.drafts[page];
    nextDrafts[this.currentPage] = nextPage;
    const encoded = JSON.stringify(nextDrafts);
    if (utf8Length(encoded) > MAX_DRAFT_BYTES) { this.error = 'assistant_view_form_limit'; return; }
    const previousDrafts = this.drafts;
    const previousSize = this.cardSize;
    this.drafts = nextDrafts;
    const nextSize = this.estimatedSize();
    if (!AssistantViewModel.makeRoom(Math.max(0, nextSize - previousSize), false, this)) {
      this.drafts = previousDrafts;
      this.error = 'assistant_view_cache_full';
      this.revision++;
      return;
    }
    this.cardSize = nextSize;
    this.drafted = true;
    this.error = '';
    this.revision++;
  }

  toggleValue(key: string, choice: string): string {
    if (!this.canInteract() || this.actionRecord || this.attempt || this.locked) return this.fieldValue(key, '[]');
    let selected: string[] = [];
    try {
      const parsed: Object = JSON.parse(this.fieldValue(key, '[]')) as Object;
      if (Array.isArray(parsed)) selected = (parsed as Object[]).filter((item: Object) => typeof item === 'string').map((item: Object) => item as string);
    } catch (_) { selected = []; }
    if (selected.includes(choice)) selected = selected.filter((item: string) => item !== choice);
    else selected.push(choice);
    const next = JSON.stringify(selected);
    this.setFieldValue(key, next, false);
    return next;
  }

  get presentationKey(): string { return JSON.stringify([this.profileID, this.owner, this.conversation, this.key, this.pageStack]); }
  getSnapshot(): undefined { return undefined; }
  lineResultFor(_key: string): ExtensionViewRecord { return {}; }
  async imageBytes(_path: string): Promise<ArrayBuffer> { return new ArrayBuffer(0); }

  private actionFor(key: string, actionID: string): ExtensionViewRecord | undefined {
    const slots = extensionUIActionSlotsForView(this.view, '@root');
    const found = slots.find((slot) => slot.key === key && Wire.text(slot.action, 'id') === actionID);
    return found ? found.action : undefined;
  }

  private addFieldValues(view: ExtensionViewRecord, values: Record<string, string>, own: Record<string, Object>, submit: boolean,
    actionID: string): void {
    const fields = this.fields(view);
    const included: string[] = [];
    if (submit) for (const name of Object.keys(fields)) included.push(name);
    for (const sectionValue of array(view['sections'])) {
      const section = Wire.record(sectionValue);
      for (const itemValue of array(section['items'])) {
        const item = Wire.record(itemValue);
        if (Wire.text(item, 'type') === 'segmented' && Wire.text(Wire.object(item, 'action'), 'id') === actionID) {
          const name = Wire.text(item, 'name'); if (name && !included.includes(name)) included.push(name);
        }
      }
    }
    const search = Wire.object(view, 'search');
    if ((submit || Wire.text(Wire.object(search, 'action'), 'id') === actionID) && has(view, 'search')) included.push('q');
    const filter = Wire.object(view, 'filter');
    const filterName = Wire.text(filter, 'name');
    if ((submit || Wire.text(Wire.object(filter, 'action'), 'id') === actionID) && filterName) included.push(filterName);
    for (const name of included) {
      let value = '';
      if (has(own, name)) {
        const selected = own[name];
        if (typeof selected === 'string') value = selected as string;
        else if (typeof selected === 'number' || typeof selected === 'boolean') value = String(selected);
      } else if (name === 'q') value = Wire.text(search, 'value');
      else if (name === filterName && filterName) value = Wire.text(filter, 'value');
      else if (has(fields, name)) value = this.fieldValue(fields[name].key, fields[name].value);
      values[name] = value;
    }
  }

  private selectionMessage(action: Record<string, Object>, values: Record<string, string>, mode: string): string {
    let message = Wire.text(action, 'reply', Wire.text(action, 'open'));
    if (mode === 'push') {
      const pageName = Wire.text(Wire.object(action, 'push'), 'page');
      const page = Wire.object(Wire.object(this.rootView, 'pages'), pageName);
      message = Wire.text(page, 'title', Wire.text(Wire.object(action, 'push'), 'title', pageName));
    } else if (mode === 'pop') message = Wire.text(this.rootView, 'title');
    for (const name of Object.keys(values).sort()) message += '\n' + (has(this.fields(this.view), name) ? this.fields(this.view)[name].label : name) + ': ' + values[name];
    return message;
  }

  async performAction(key: string, actionID: string, _event: string = 'press', ownValues: ExtensionViewRecord = {}, _title: string = ''): Promise<void> {
    if (!this.canInteract()) return;
    const action = this.actionFor(key, actionID);
    if (!action) return;
    const mode = actionMode(action);
    if (!mode) { this.error = 'assistant_view_unavailable'; throw new Error('assistant_view_unavailable'); }
    if (mode === 'reply' && (this.actionRecord || this.attempt || this.acknowledged || this.locked))
      throw new Error(this.actionRecord || this.acknowledged || this.locked ? 'already_answered' : 'pending_action');
    const capturedValues: Record<string, string> = {};
    if (mode === 'reply') this.addFieldValues(this.view, capturedValues, ownValues, Wire.bool(action, 'submit'), actionID);
    if (mode === 'reply') {
      const failure = this.validateValues(action, capturedValues);
      if (failure) { this.error = failure; this.revision++; throw new Error(failure); }
    }
    const selection: AssistantSelection = {
      token: util.generateRandomUUID(), actionKey: key, action: this.cloneRecord(action), page: this.currentPage,
      values: capturedValues, version: this.viewVersion,
      message: this.selectionMessage(action, capturedValues, mode),
      needsConfirmation: !!Wire.text(action, 'confirm') || (mode === 'reply' && this.changedNeedsConfirmation),
      profileID: this.profileID, owner: this.owner, conversation: this.conversation, key: this.key,
      generation: this.clientGeneration, scope: this.scopeKey, target: this.targetKey, activityVisit: this.activityVisit
    };
    if (selection.needsConfirmation) {
      const pending = new Promise<void>((resolve, reject) => {
        this.pendingActions.set(selection.token, { resolve: resolve, reject: reject, taken: false, accepted: false });
      });
      this.selection = selection;
      this.error = '';
      this.confirmationRevision++;
      this.revision++;
      await pending;
      return;
    }
    await this.execute(selection, false);
  }

  private resolvePendingAction(token: string): void {
    const pending = this.pendingActions.get(token);
    if (!pending) return;
    this.pendingActions.delete(token);
    pending.resolve();
  }

  private rejectPendingAction(token: string, error: Error): void {
    const pending = this.pendingActions.get(token);
    if (!pending) return;
    this.pendingActions.delete(token);
    pending.reject(error);
  }

  private cancelSelection(error: Error): void {
    const selection = this.selection;
    this.selection = undefined;
    if (!selection) return;
    this.confirmationRevision++;
    this.rejectPendingAction(selection.token, error);
  }

  private rejectPendingActions(error: Error): void {
    const tokens = Array.from(this.pendingActions.keys());
    for (const token of tokens) this.rejectPendingAction(token, error);
  }

  private cloneRecord(source: Record<string, Object>): ExtensionViewRecord {
    const result: ExtensionViewRecord = {};
    for (const key of Object.keys(source)) result[key] = source[key];
    return result;
  }

  declineConfirmation(token: string): void {
    if (this.selection && this.selection.token === token) {
      this.selection = undefined;
      this.confirmationRevision++;
      this.revision++;
      this.rejectPendingAction(token, new Error('cancelled'));
      return;
    }
    const pending = this.pendingActions.get(token);
    if (pending && pending.taken && !pending.accepted) this.rejectPendingAction(token, new Error('cancelled'));
  }

  takeConfirmation(expectedToken: string): Record<string, Object> | undefined {
    if (!this.selection || this.selection.token !== expectedToken) return undefined;
    const selection = this.selection;
    this.selection = undefined;
    this.confirmationRevision++;
    if (!this.selectionIsCurrent(selection)) {
      this.error = 'assistant_view_changed';
      this.revision++;
      this.rejectPendingAction(selection.token, new Error('assistant_view_changed'));
      return undefined;
    }
    const pending = this.pendingActions.get(selection.token);
    if (pending) pending.taken = true;
    return Wire.record(selection as Object);
  }

  async confirmSelection(value: Record<string, Object>, lease: AssistantViewConfirmationLease): Promise<void> {
    const selection = value as Object as AssistantSelection;
    if (!selection || !this.pendingActions.has(selection.token)) throw new Error('cancelled');
    if (!this.leaseMatchesSelection(selection, lease) || !this.selectionIsCurrent(selection)) {
      this.error = 'assistant_view_changed'; this.revision++;
      const error = new Error('assistant_view_changed'); this.rejectPendingAction(selection.token, error); throw error;
    }
    const currentAction = this.actionFor(selection.actionKey, Wire.text(selection.action, 'id'));
    if (!currentAction || JSON.stringify(currentAction) !== JSON.stringify(selection.action)) {
      this.error = 'assistant_view_changed'; this.revision++;
      const error = new Error('assistant_view_changed'); this.rejectPendingAction(selection.token, error); throw error;
    }
    const pending = this.pendingActions.get(selection.token);
    if (pending) pending.accepted = true;
    try {
      await this.execute(selection, true);
      this.resolvePendingAction(selection.token);
    } catch (error) {
      const failure = error as Error;
      this.rejectPendingAction(selection.token, failure);
      throw failure;
    }
  }

  private validateValues(action: ExtensionViewRecord, values: Record<string, string>): string {
    const page = this.view;
    const fields = this.fields(page);
    if (Wire.bool(action, 'submit')) {
      for (const name of Object.keys(fields)) if (!has(values, name)) return 'assistant_view_form_incomplete';
    }
    for (const name of Object.keys(values)) {
      let field: AssistantField | undefined = fields[name];
      if (!field) field = this.segmentedField(page, name, Wire.text(action, 'id'));
      if (!field) {
        const filterName = Wire.text(Wire.object(page, 'filter'), 'name');
        const searchAllowed = has(page, 'search') && (Wire.bool(action, 'submit') ||
          Wire.text(Wire.object(Wire.object(page, 'search'), 'action'), 'id') === Wire.text(action, 'id'));
        const filterAllowed = !!filterName && (Wire.bool(action, 'submit') ||
          Wire.text(Wire.object(Wire.object(page, 'filter'), 'action'), 'id') === Wire.text(action, 'id'));
        if (!(name === 'q' && searchAllowed) && !(name === filterName && filterAllowed)) return 'assistant_view_form_invalid';
      }
      const value = values[name];
      if (Array.from(value).length > 2000) return 'assistant_view_form_invalid';
      if (!field) continue;
      if (field.kind === 'number') {
        if (!/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(value) || !Number.isFinite(Number(value)))
          return 'assistant_view_form_invalid';
      } else if (field.kind === 'toggle') {
        if (value !== 'true' && value !== 'false') return 'assistant_view_form_invalid';
      } else if (field.kind === 'choice') {
        const choices = array(field.item['choices']).map((item: Object) => Wire.text(Wire.record(item), 'value'));
        if (!choices.includes(value)) return 'assistant_view_form_invalid';
      } else if (['date', 'time', 'datetime'].includes(field.kind) && value && !dateValue(field.kind, value)) return 'assistant_view_form_invalid';
      else if (field.kind === 'multichoice') {
        let selected: string[] = [];
        try {
          const raw: Object = JSON.parse(value) as Object;
          if (!Array.isArray(raw)) return 'assistant_view_form_invalid';
          selected = (raw as Object[]).filter((item: Object) => typeof item === 'string').map((item: Object) => item as string);
        } catch (_) { return 'assistant_view_form_invalid'; }
        const choices = array(field.item['choices']).map((item: Object) => Wire.text(Wire.record(item), 'value'));
        if (new Set(selected).size !== selected.length || selected.some((item: string) => !choices.includes(item))) return 'assistant_view_form_invalid';
        values[name] = JSON.stringify(selected);
      }
      if (['date', 'time', 'datetime'].includes(field.kind) && value) {
        const earliest = Wire.text(field.item, 'earliest'), latest = Wire.text(field.item, 'latest');
        if (earliest && value < earliest || latest && value > latest) return 'assistant_view_form_invalid';
      }
    }
    return '';
  }

  private segmentedField(view: ExtensionViewRecord, name: string, actionID: string): AssistantField | undefined {
    const sections = array(view['sections']);
    for (let si = 0; si < sections.length; si++) {
      const section = Wire.record(sections[si]);
      const items = array(section['items']);
      for (let ii = 0; ii < items.length; ii++) {
        const item = Wire.record(items[ii]);
        if (Wire.text(item, 'type') !== 'segmented' || Wire.text(item, 'name') !== name ||
          Wire.text(Wire.object(item, 'action'), 'id') !== actionID) continue;
        return { name: name, key: Wire.text(item, 'key', '@item:' + String(si) + ':' + String(ii)), kind: 'choice',
          label: Wire.text(item, 'title', name), value: Wire.text(item, 'value'), item: item };
      }
    }
    return undefined;
  }

  private async execute(selection: AssistantSelection, confirmed: boolean): Promise<void> {
    if (!this.selectionIsCurrent(selection) || !this.interactionCurrent()) {
      this.error = 'assistant_view_changed'; this.revision++; throw new Error('assistant_view_changed');
    }
    const mode = actionMode(selection.action);
    if (!mode) { this.error = 'assistant_view_unavailable'; throw new Error('assistant_view_unavailable'); }
    if (mode === 'push') {
      const pageName = Wire.text(Wire.object(selection.action, 'push'), 'page');
      if (!has(Wire.object(this.rootView, 'pages'), pageName)) { this.error = 'assistant_view_page_unavailable'; this.revision++; throw new Error('assistant_view_page_unavailable'); }
      if (this.pageStack.length >= MAX_PAGE_DEPTH) { this.error = 'assistant_view_page_depth'; this.revision++; throw new Error('assistant_view_page_depth'); }
      this.pageStack = this.pageStack.concat(pageName); this.error = ''; this.revision++; return;
    }
    if (mode === 'pop') {
      if (this.pageStack.length) this.pageStack = this.pageStack.slice(0, -1);
      this.error = ''; this.revision++; return;
    }
    if (mode === 'open') {
      const target = assistantViewOpenTarget(Wire.text(selection.action, 'open'));
      if (!target) { this.error = 'assistant_view_invalid_target'; this.revision++; throw new Error('assistant_view_invalid_target'); }
      if (!this.interactionCurrent()) { this.error = 'assistant_view_changed'; throw new Error('assistant_view_changed'); }
      this.effect = Wire.payload(['kind', 'address'], [target.kind, target.address]);
      this.effectRevision++;
      return;
    }
    if (!this.registered) { this.error = 'assistant_view_unavailable'; throw new Error('assistant_view_unavailable'); }
    if (this.actionRecord || this.acknowledged || this.locked) throw new Error('already_answered');
    if (this.attempt) throw new Error('pending_action');
    if (!this.admitted()) { this.error = 'offline'; this.revision++; throw new Error('offline'); }
    const id = util.generateRandomUUID();
    const path = this.actionPath();
    const keys = ['action', 'client_msg_id', 'confirmed', 'expected_version'];
    const body = Wire.payload(keys, [Wire.text(selection.action, 'id'), id, confirmed, selection.version]);
    if (selection.page) body['page'] = selection.page;
    body['values'] = this.toWireValues(selection.values);
    const rawBody = JSON.stringify(body);
    if (utf8Length(rawBody) > MAX_DRAFT_BYTES) { this.error = 'assistant_view_form_limit'; this.revision++; throw new Error('assistant_view_form_limit'); }
    const attempt: AssistantAttempt = { action: Wire.text(selection.action, 'id'), selectionToken: selection.token, page: selection.page,
      values: copyStrings(selection.values), clientMsgID: id, version: selection.version, confirmed: confirmed,
      path: path, body: rawBody, state: 'sending', sending: true, epoch: ++this.visit };
    const previousSize = this.cardSize;
    this.attempt = attempt;
    const nextSize = this.estimatedSize();
    if (!AssistantViewModel.makeRoom(Math.max(0, nextSize - previousSize), false, this)) {
      this.attempt = undefined;
      this.error = 'assistant_view_cache_full';
      this.revision++;
      throw new Error('assistant_view_cache_full');
    }
    this.cardSize = nextSize;
    this.busy = true;
    this.error = '';
    this.revision++;
    await this.sendAttempt(attempt, false);
  }

  private toWireValues(values: Record<string, string>): Record<string, Object> {
    const result: Record<string, Object> = {};
    for (const name of Object.keys(values)) result[name] = values[name];
    return result;
  }

  private actionPath(): string {
    return '/api/v1/assistant/conversations/' + segment(this.conversation) + '/views/' + segment(this.key) +
      '/actions?manager=' + segment(this.owner);
  }

  private async sendAttempt(attempt: AssistantAttempt, retry: boolean): Promise<void> {
    const attemptEpoch = attempt.epoch;
    const repository = this.repository;
    const client = this.client;
    if (!repository || !client || !this.active || !this.ownerCurrent()) {
      attempt.sending = false; attempt.state = 'uncertain'; this.busy = false;
      this.error = 'assistant_view_result_unknown'; this.revision++; throw new Error('assistant_view_result_unknown');
    }
    const generation = this.clientGeneration, scope = this.scopeKey, target = this.targetKey;
    const activityVisit = this.activityVisit;
    const admit = (): boolean => this.attempt === attempt && attempt.epoch === attemptEpoch &&
      this.activityVisit === activityVisit && this.admitted(generation, scope, target);
    try {
      let result: Record<string, Object>;
      if (!retry) {
        result = await repository.call(attempt.path, 'POST', Wire.parse(attempt.body), attempt.clientMsgID, admit);
      } else {
        if (!admit()) throw new Error('request_updated');
        result = Wire.parse(await repository.transport.request(attempt.path, 'POST', attempt.body, attempt.clientMsgID));
        await this.finishJournal(repository, attempt.path, attempt.clientMsgID, 'done', client, generation, scope, target,
          attempt, attemptEpoch);
      }
      if (this.attempt !== attempt || attempt.epoch !== attemptEpoch) throw new Error('cancelled');
      this.receiveAcknowledgement(attempt, attemptEpoch, result);
    } catch (error) {
      if (this.attempt !== attempt || attempt.epoch !== attemptEpoch) throw new Error('cancelled');
      await this.handleAttemptError(attempt, attemptEpoch, error as Error, retry, repository, client, generation, scope, target);
      throw error as Error;
    }
  }

  private async finishJournal(repository: HubRepository, path: string, id: string, state: string, client: ClientModel,
    generation: number, scope: string, target: string, attempt: AssistantAttempt, attemptEpoch: number): Promise<void> {
    const mutations = repository.mutations;
    if (!mutations) return;
    const record = mutations.blocked().find((item) => item.id === id && item.path === path && item.method === 'POST');
    if (record) await mutations.finish(record, state);
    if (this.client === client && this.repository === repository && this.attempt === attempt && attempt.epoch === attemptEpoch &&
      this.clientGeneration === generation && this.scopeKey === scope && this.targetKey === target &&
      this.ownerCurrent(generation, scope, target)) client.unresolvedWrites = mutations.blocked();
  }

  private receiveAcknowledgement(attempt: AssistantAttempt, attemptEpoch: number, result: Record<string, Object>): void {
    if (this.attempt !== attempt || attempt.epoch !== attemptEpoch) return;
    const action = Wire.object(result, 'action');
    if (this.validActionRecord(action)) this.mergeActionRecord(action);
    else {
      attempt.sending = false; attempt.state = 'acknowledged';
      this.acknowledged = true;
    }
    this.busy = false;
    this.error = this.actionRecord ? '' : 'assistant_view_syncing';
    this.revision++;
    this.resolvePendingAction(attempt.selectionToken);
    const feed = this.feed;
    const generation = this.clientGeneration, scope = this.scopeKey, target = this.targetKey;
    if (feed) feed.refresh().then(() => {
      if (attempt.epoch !== attemptEpoch || !this.active || !this.ownerCurrent(generation, scope, target)) return;
      this.mergeSources();
    }).catch(() => {
      if (attempt.epoch === attemptEpoch && this.active && this.ownerCurrent(generation, scope, target)) {
        this.error = 'assistant_view_syncing';
        this.revision++;
      }
    });
  }

  private async handleAttemptError(attempt: AssistantAttempt, attemptEpoch: number, error: Error, retry: boolean,
    repository: HubRepository, client: ClientModel, generation: number, scope: string, target: string): Promise<void> {
    if (this.attempt !== attempt || attempt.epoch !== attemptEpoch) return;
    const fields = error as Object as Record<string, Object>;
    const status = typeof fields['status'] === 'number' ? fields['status'] as number : 0;
    const code = Wire.text(fields, 'code', error.message);
    const definitelyNotSent = ['pending_action', 'request_updated', 'storage_unavailable'].includes(code);
    const unknown = !definitelyNotSent && (Wire.bool(fields, 'resultUnknown') || status === 0 || status >= 500 || status === 202);
    attempt.sending = false;
    this.busy = false;
    if (retry && !unknown) await this.finishJournal(repository, attempt.path, attempt.clientMsgID, 'failed', client,
      generation, scope, target, attempt, attemptEpoch);
    if (status === 409 && code === 'view_changed') {
      this.attempt = undefined;
      this.acknowledged = false;
      this.locked = false;
      this.error = 'assistant_view_changed';
      this.changedNeedsConfirmation = true;
      this.selection = undefined;
      this.confirmationRevision++;
      this.revision++;
      await this.recoverFeed();
      return;
    }
    if (status === 409 && code === 'already_answered') {
      this.attempt = undefined;
      this.acknowledged = false;
      this.locked = true;
      this.error = 'assistant_view_syncing';
      this.revision++;
      await this.recoverFeed();
      return;
    }
    if (code === 'pending_action') {
      this.attempt = undefined;
      this.locked = true;
      this.error = 'assistant_view_syncing';
      this.revision++;
      await this.recoverFeed();
      return;
    }
    if (definitelyNotSent) {
      this.attempt = undefined;
      this.error = code === 'request_updated' ? 'request_updated' : 'assistant_view_submit_failed';
      this.revision++;
      return;
    }
    if (unknown) {
      attempt.state = 'uncertain';
      this.error = 'assistant_view_result_unknown';
    } else {
      this.attempt = undefined;
      this.error = status === 401 || status === 403 ? 'permission_denied' : status === 404 ? 'assistant_view_unavailable' : 'assistant_view_submit_failed';
    }
    this.revision++;
  }

  async retryOriginal(): Promise<void> {
    const attempt = this.attempt;
    if (!attempt || !this.canRetryOriginal || !this.admitted()) return;
    attempt.sending = true;
    attempt.state = 'sending';
    attempt.epoch = ++this.visit;
    this.busy = true;
    this.error = '';
    this.revision++;
    await this.sendAttempt(attempt, true);
  }

  private async recoverFeed(): Promise<void> {
    const feed = this.feed;
    if (!feed || !this.ownerCurrent()) return;
    const generation = this.clientGeneration, scope = this.scopeKey, target = this.targetKey;
    try { await feed.refresh(); } catch (_) { /* The card remains locked or requires a fresh confirmation. */ }
    if (this.active && this.ownerCurrent(generation, scope, target)) this.mergeSources();
  }

  private validActionRecord(value: Record<string, Object>): boolean {
    return Wire.text(value, 'key') === this.key && !!Wire.text(value, 'action') && !!Wire.text(value, 'client_msg_id') &&
      ['pending', 'sent', 'failed'].includes(Wire.text(value, 'status'));
  }

  private immutableAction(value: Record<string, Object>): string {
    const stable: Record<string, Object> = Wire.payload(['key', 'action', 'page', 'client_msg_id', 'values', 'kind', 'version', 'text'], [
      Wire.text(value, 'key'), Wire.text(value, 'action'), Wire.text(value, 'page'), Wire.text(value, 'client_msg_id'),
      Wire.object(value, 'values'), Wire.text(value, 'kind'), Wire.number(value, 'version'), Wire.text(value, 'text')
    ]);
    return JSON.stringify(stable);
  }

  private mergeActionRecord(incoming: Record<string, Object>): void {
    if (!this.validActionRecord(incoming)) return;
    const current = this.actionRecord;
    if (current) {
      if (this.immutableAction(current) !== this.immutableAction(incoming)) return;
      const currentRevision = Wire.number(current, 'revision');
      const nextRevision = Wire.number(incoming, 'revision');
      if (nextRevision <= currentRevision) return;
    }
    if (this.selection) this.cancelSelection(new Error('already_answered'));
    this.actionRecord = incoming;
    this.acknowledged = true;
    this.locked = true;
    this.attempt = undefined;
    this.busy = false;
    this.selection = undefined;
    this.confirmationRevision++;
    this.actionRevision = Math.max(this.actionRevision, Wire.number(incoming, 'revision'));
    this.changedNeedsConfirmation = false;
    this.drafted = false;
    this.syncActionValues();
    this.revision++;
  }

  setVisible(active: boolean): void {
    if (this.active === active) return;
    this.active = active;
    this.activityVisit++;
    if (!active) {
      if (this.selection) this.cancelSelection(new Error('cancelled'));
      else this.confirmationRevision++;
      this.rejectPendingActions(new Error('cancelled'));
      this.effect = {};
      const attempt = this.attempt;
      if (attempt?.sending) {
        attempt.sending = false;
        attempt.state = 'uncertain';
        attempt.epoch = ++this.visit;
        this.busy = false;
        this.error = 'assistant_view_result_unknown';
      } else if (!attempt && !this.locked && !this.actionRecord) this.error = '';
    } else this.mergeSources();
    this.revision++;
  }

  clearEffect(): void { this.effect = {}; }

  hide(): void { this.setVisible(false); }

  forget(profileID: string): void {
    if (profileID !== this.profileID) return;
    this.active = false;
    this.activityVisit++;
    this.cancelSelection(new Error('cancelled'));
    this.rejectPendingActions(new Error('cancelled'));
    this.effect = {};
    this.confirmationRevision++;
    this.effectRevision++;
    this.attempt = undefined;
    this.actionRecord = undefined;
    this.busy = false;
    this.acknowledged = false;
    this.complete = false;
    this.changedNeedsConfirmation = false;
    this.error = '';
    this.drafts = {};
    this.rootView = {};
    this.fallback = '';
    this.valid = false;
    this.registered = false;
    this.locked = true;
    this.pageStack = [];
    this.drafted = false;
    this.item = {};
    this.key = '';
    this.owner = '';
    this.conversation = '';
    this.scopeKey = '';
    this.targetKey = '';
    this.clientGeneration = -1;
    this.viewVersion = 1;
    this.actionRevision = 0;
    this.lineResultRevision = 0;
    this.cardSize = 0;
    this.client = undefined;
    this.feed = undefined;
    this.repository = undefined;
    this.revision++;
    AssistantViewModel.discard(this);
    this.profileID = '';
  }

  private interactionCurrent(): boolean {
    return this.active && this.ownerCurrent() && this.complete && this.valid && this.registered && !this.selection;
  }
}
