/** Bounded, fail-closed native tree reducer for extension UI stream protocol 1. */
import { util } from '@kit.ArkTS';

export type ExtensionViewRecord = Record<string, Object>;

export interface ExtensionUIStreamSnapshot {
  protocol: number;
  session: string;
  epoch: string;
  version: number;
  root_key: string;
  view: ExtensionViewRecord;
}

export interface ExtensionUIStreamResult {
  snapshot: ExtensionUIStreamSnapshot | undefined;
  recover: boolean;
  reason: string;
}

export interface ExtensionUIActionSlot {
  key: string;
  event: string;
  action: ExtensionViewRecord;
}

const FRAME_BYTES: number = 1024 * 1024;
const REQUEST_BYTES: number = 64 * 1024;
const MAX_NODES: number = 300;
const MAX_OPERATIONS: number = 256;
const MAX_SAFE_VERSION: string = '9007199254740991';

function utf8Length(value: string): number { return util.TextEncoder.create('utf-8').encodeInto(value).byteLength; }
function record(value: Object | null | undefined): ExtensionViewRecord {
  if (value === null || value === undefined || typeof value !== 'object' || Array.isArray(value)) return {};
  return value as ExtensionViewRecord;
}
function array(value: Object | undefined): Object[] { return Array.isArray(value) ? value as Object[] : []; }
function text(value: ExtensionViewRecord, key: string): string {
  const item = value[key]; return typeof item === 'string' ? item as string : '';
}
function has(value: ExtensionViewRecord, key: string): boolean { return Object.keys(value).includes(key); }
function count(value: ExtensionViewRecord, key: string): number { return array(value[key]).length; }
function fail(message: string): never { throw new Error(message); }
function clone(value: Object): Object { return JSON.parse(JSON.stringify(value)) as Object; }
function cloneRecord(value: ExtensionViewRecord): ExtensionViewRecord { return clone(value) as ExtensionViewRecord; }
function safeId(value: string): boolean { return value.length > 0 && Array.from(value).length <= 128; }
function safeVersion(value: Object | undefined): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value as number) || (value as number) < 1) return 0;
  return value as number;
}

/** Check a numeric JSON token before JSON.parse can round a large version into a safe float. */
function integralVersionToken(token: string): boolean {
  const match = /^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(token);
  if (!match || match[1] === '-') return false;
  let digits = match[2] + (match[3] || '');
  digits = digits.replace(/^0+/, '');
  if (!digits) return false;
  const exponent = Number(match[4] || '0') - (match[3] || '').length;
  if (!Number.isSafeInteger(exponent) || exponent > 32 || exponent < -digits.length) return false;
  let whole = '';
  if (exponent < 0) {
    const cut = digits.length + exponent;
    if (cut <= 0 || !/^0*$/.test(digits.slice(cut))) return false;
    whole = digits.slice(0, cut);
  } else {
    if (digits.length + exponent > 16) return false;
    whole = digits + '0'.repeat(exponent);
  }
  whole = whole.replace(/^0+/, '');
  if (!whole) return false;
  return whole.length < MAX_SAFE_VERSION.length || whole.length === MAX_SAFE_VERSION.length && whole <= MAX_SAFE_VERSION;
}

function inspectVersionNumbers(raw: string): void {
  let depth = 0;
  for (let index = 0; index < raw.length; index++) {
    const char = raw[index];
    if (char === '"') {
      const start = index;
      index++;
      while (index < raw.length && raw[index] !== '"') {
        if (raw[index] === '\\') index++;
        index++;
      }
      const name = JSON.parse(raw.slice(start, index + 1)) as string;
      if (name === 'version' || name === 'base_version') {
        const rest = raw.slice(index + 1);
        const number = /^\s*:\s*(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(rest);
        if (number && !integralVersionToken(number[1])) fail('invalid_stream_version');
      }
    } else if (char === '{' || char === '[') {
      depth++;
      if (depth > 32) fail('stream_depth_limit');
    } else if (char === '}' || char === ']') depth--;
  }
}

export function parseExtensionUIEvent(raw: string): ExtensionViewRecord {
  if (!raw || utf8Length(raw) > FRAME_BYTES) fail('stream_frame_limit');
  inspectVersionNumbers(raw);
  const value = record(JSON.parse(raw) as Object);
  if (!['extension_ui_stream', 'extension_ui'].includes(text(value, 'type'))) fail('stream_frame_type');
  return value;
}

export function parseExtensionUIFrame(raw: string): ExtensionViewRecord {
  const value = parseExtensionUIEvent(raw);
  if (text(value, 'type') !== 'extension_ui_stream') fail('stream_frame_type');
  return value;
}

interface IndexedNode {
  kind: string;
  value: ExtensionViewRecord;
  section: number;
  index: number;
}

function addNode(index: Map<string, IndexedNode>, key: string, node: IndexedNode): void {
  if (!safeId(key) || index.has(key) || index.size >= MAX_NODES) fail('stream_key_limit');
  index.set(key, node);
}

function indexTree(snapshot: ExtensionUIStreamSnapshot): Map<string, IndexedNode> {
  const nodes = new Map<string, IndexedNode>();
  addNode(nodes, snapshot.root_key, { kind: 'root', value: snapshot.view, section: -1, index: -1 });
  const sections = array(snapshot.view['sections']);
  if (sections.length > MAX_NODES) fail('stream_node_limit');
  for (let sectionIndex = 0; sectionIndex < sections.length; sectionIndex++) {
    const section = record(sections[sectionIndex]);
    addNode(nodes, text(section, 'key'), { kind: 'section', value: section, section: sectionIndex, index: -1 });
    const items = array(section['items']);
    if (items.length > MAX_NODES) fail('stream_node_limit');
    for (let itemIndex = 0; itemIndex < items.length; itemIndex++) {
      const item = record(items[itemIndex]);
      addNode(nodes, text(item, 'key'), { kind: 'item', value: item, section: sectionIndex, index: itemIndex });
      const kind = text(item, 'type');
      if (kind === 'grid' || kind === 'carousel') {
        const tiles = array(item['tiles']);
        for (let tileIndex = 0; tileIndex < tiles.length; tileIndex++) {
          const tile = record(tiles[tileIndex]);
          addNode(nodes, text(tile, 'key'), { kind: 'tile', value: tile, section: sectionIndex, index: itemIndex });
        }
      }
    }
  }
  return nodes;
}

function validateSnapshot(snapshot: ExtensionUIStreamSnapshot): void {
  if (snapshot.protocol !== 1 || !safeId(snapshot.session) || !safeId(snapshot.epoch) || !safeId(snapshot.root_key) || !safeVersion(snapshot.version)) fail('invalid_stream_snapshot');
  const view = record(snapshot.view);
  if (!Array.isArray(view['sections'])) fail('invalid_stream_view');
  if (has(view, 'state') && (typeof view['state'] !== 'string' || utf8Length(view['state'] as string) > 16 * 1024)) fail('stream_state_limit');
  if (has(view, 'pages') && Object.keys(record(view['pages'])).length > 0) fail('stream_pages_unsupported');
  snapshot.view = view;
  indexTree(snapshot);
  if (utf8Length(JSON.stringify(snapshot)) > FRAME_BYTES) fail('stream_materialized_limit');
}

function nodeAt(nodes: Map<string, IndexedNode>, key: string): IndexedNode {
  const value = nodes.get(key);
  if (!value) return fail('stream_node_missing');
  return value;
}

function action(value: Object | undefined): ExtensionViewRecord { return record(value); }
function slotsForNode(key: string, node: ExtensionViewRecord, result: ExtensionUIActionSlot[], defaultEvent: string = 'press'): void {
  const type = text(node, 'type');
  const actionTypes = ['', 'row', 'button', 'segmented', 'file_tree', 'code', 'diff', 'comment_thread', 'composer', 'grid', 'carousel', 'breadcrumb', 'empty'];
  if (!actionTypes.includes(type)) return;
  const add = (candidate: Object | undefined, event: string): void => {
    const saved = action(candidate), id = text(saved, 'id');
    if (id) result.push({ key: key, event: event, action: saved });
  };
  if (has(node, 'action')) add(node['action'], type === 'segmented' ? 'change' : defaultEvent);
  if (has(node, 'control') && !record(node['control'])['disabled']) add(record(node['control'])['action'], 'change');
  if (has(node, 'actions')) {
    const actions = array(node['actions']);
    for (const candidate of actions) add(record(candidate)['action'], 'press');
  }
  if (has(node, 'button')) add(record(node['button'])['action'], 'press');
  if (has(node, 'buttons')) {
    const buttons = array(node['buttons']);
    for (const candidate of buttons) add(record(candidate)['action'], 'submit');
  }
  if (has(node, 'crumbs')) {
    const crumbs = array(node['crumbs']);
    for (const candidate of crumbs) add(record(candidate)['action'], 'press');
  }
  for (const name of ['expand', 'load', 'context', 'mark_viewed']) if (has(node, name)) add(node[name], 'press');
  if (has(node, 'draft')) add(node['draft'], 'submit');
  if (has(node, 'comment')) add(node['comment'], 'submit');
  if (has(node, 'reply')) add(node['reply'], type === 'comment_thread' ? 'submit' : 'press');
  if (has(node, 'threads')) {
    const threads = array(node['threads']);
    for (const value of threads) {
      const thread = record(value);
      add(thread['reply'], 'submit'); add(thread['resolve'], 'press');
    }
  }
}

export function extensionUIActionSlots(snapshot: ExtensionUIStreamSnapshot): ExtensionUIActionSlot[] {
  const result: ExtensionUIActionSlot[] = [];
  for (const name of ['search', 'filter', 'tabs']) {
    if (has(snapshot.view, name)) slotsForNode(snapshot.root_key, record(snapshot.view[name]), result, 'change');
  }
  if (has(snapshot.view, 'review')) {
    const review = record(snapshot.view['review']);
    const buttons = array(review['buttons']);
    for (const value of buttons) addReviewButton(snapshot.root_key, record(value), result);
    if (has(review, 'discard')) slotsForNode(snapshot.root_key, { action: review['discard'] as Object }, result);
  }
  const sections = array(snapshot.view['sections']);
  for (const value of sections) {
    const section = record(value), sectionKey = text(section, 'key');
    if (has(section, 'more')) slotsForNode(sectionKey, { action: record(section['more'])['action'] as Object }, result);
    for (const itemValue of array(section['items'])) {
      const item = record(itemValue), key = text(item, 'key');
      slotsForNode(key, item, result);
      const type = text(item, 'type');
      if (type === 'grid' || type === 'carousel') for (const tileValue of array(item['tiles'])) {
        const tile = record(tileValue); slotsForNode(text(tile, 'key'), tile, result);
      }
    }
  }
  return result;
}

export function extensionUIActionSlotsForView(view: ExtensionViewRecord, rootKey: string, sectionKeyPrefix: string = '@section:', itemKeyPrefix: string = '@item:'): ExtensionUIActionSlot[] {
  const result: ExtensionUIActionSlot[] = [];
  for (const name of ['search', 'filter', 'tabs']) if (has(view, name)) slotsForNode(rootKey, record(view[name]), result, 'change');
  if (has(view, 'review')) {
    const review = record(view['review']);
    for (const value of array(review['buttons'])) addReviewButton(rootKey, record(value), result);
    if (has(review, 'discard')) slotsForNode(rootKey, { action: review['discard'] as Object }, result);
  }
  const sections = array(view['sections']);
  for (let si = 0; si < sections.length; si++) {
    const section = record(sections[si]);
    const sectionKey = text(section, 'key') || sectionKeyPrefix + String(si);
    if (has(section, 'more')) slotsForNode(sectionKey, { action: record(section['more'])['action'] as Object }, result);
    const items = array(section['items']);
    for (let ii = 0; ii < items.length; ii++) {
      const item = record(items[ii]), key = text(item, 'key') || itemKeyPrefix + String(si) + ':' + String(ii);
      slotsForNode(key, item, result);
      const type = text(item, 'type');
      if (type === 'grid' || type === 'carousel') for (let ti = 0; ti < array(item['tiles']).length; ti++) {
        const tile = record(array(item['tiles'])[ti]);
        slotsForNode(text(tile, 'key') || key + ':tile:' + String(ti), tile, result);
      }
    }
  }
  return result;
}

function addReviewButton(key: string, button: ExtensionViewRecord, result: ExtensionUIActionSlot[]): void {
  const saved = action(button['action']), id = text(saved, 'id');
  if (id) result.push({ key: key, event: 'submit', action: saved });
}

export function actionForStreamEvent(snapshot: ExtensionUIStreamSnapshot, key: string, actionID: string, event: string): ExtensionViewRecord | undefined {
  const slots = extensionUIActionSlots(snapshot);
  for (const slot of slots) if (slot.key === key && slot.event === event && text(slot.action, 'id') === actionID) return cloneRecord(slot.action);
  return undefined;
}

function applyOperation(snapshot: ExtensionUIStreamSnapshot, operation: ExtensionViewRecord): ExtensionUIStreamSnapshot {
  const nodes = indexTree(snapshot);
  const op = text(operation, 'op');
  const sections = array(snapshot.view['sections']).map((value: Object) => record(clone(value)));
  let view = snapshot.view;
  const editItems = (sectionIndex: number, edit: (items: ExtensionViewRecord[]) => void): void => {
    if (sectionIndex < 0 || sectionIndex >= sections.length) fail('stream_section_missing');
    const items = array(sections[sectionIndex]['items']).map((value: Object) => record(clone(value)));
    edit(items);
    const next = cloneRecord(sections[sectionIndex]); next['items'] = items; sections[sectionIndex] = next;
  };
  if (op === 'replace') {
    const key = text(operation, 'key'), target = nodeAt(nodes, key);
    const payloadCount = ['view', 'section', 'item'].filter((name: string) => has(operation, name)).length;
    if (payloadCount !== 1) fail('stream_replace_payload');
    if (target.kind === 'root' && has(operation, 'view')) view = record(clone(operation['view'] as Object));
    else if (target.kind === 'section' && has(operation, 'section')) {
      const replacement = record(clone(operation['section'] as Object));
      if (text(replacement, 'key') !== key) fail('stream_replace_key');
      sections[target.section] = replacement;
    } else if (target.kind === 'item' && has(operation, 'item')) {
      const replacement = record(clone(operation['item'] as Object));
      if (text(replacement, 'key') !== key || text(replacement, 'type') !== text(target.value, 'type')) fail('stream_replace_kind');
      editItems(target.section, (items: ExtensionViewRecord[]) => { items[target.index] = replacement; });
    } else fail('stream_replace_kind');
  } else if (op === 'remove') {
    const key = text(operation, 'key'), target = nodeAt(nodes, key);
    if (target.kind === 'section') sections.splice(target.section, 1);
    else if (target.kind === 'item') editItems(target.section, (items: ExtensionViewRecord[]) => { items.splice(target.index, 1); });
    else fail('stream_remove_kind');
  } else if (op === 'insert' || op === 'move') {
    const parent = nodeAt(nodes, text(operation, 'parent'));
    const moving = op === 'move' ? nodeAt(nodes, text(operation, 'key')) : undefined;
    const payloadCount = ['section', 'item'].filter((name: string) => has(operation, name)).length;
    if (op === 'insert' && payloadCount !== 1 || op === 'move' && payloadCount !== 0) fail('stream_insert_payload');
    const sectionPayload = op === 'insert' && has(operation, 'section') ? record(clone(operation['section'] as Object)) : undefined;
    const itemPayload = op === 'insert' && has(operation, 'item') ? record(clone(operation['item'] as Object)) : undefined;
    const movingKind = moving ? moving.kind : sectionPayload ? 'section' : 'item';
    const value = moving ? moving.value : sectionPayload || itemPayload || {};
    if (!(movingKind === 'section' && parent.kind === 'root' || movingKind === 'item' && parent.kind === 'section')) fail('stream_parent_kind');
    if (op === 'move' && (text(operation, 'key') === text(operation, 'parent') || text(operation, 'key') === text(operation, 'before'))) fail('stream_move_cycle');
    const before = text(operation, 'before');
    if (before) {
      const sibling = nodeAt(nodes, before);
      if (!(sibling.kind === 'section' && parent.kind === 'root' || sibling.kind === 'item' && parent.kind === 'section' && sibling.section === parent.section)) fail('stream_sibling_kind');
    }
    if (moving && moving.kind === 'section') sections.splice(moving.section, 1);
    else if (moving && moving.kind === 'item') editItems(moving.section, (items: ExtensionViewRecord[]) => { items.splice(moving.index, 1); });
    if (movingKind === 'section') {
      const at = before ? sections.findIndex((candidate: ExtensionViewRecord) => text(candidate, 'key') === before) : sections.length;
      if (at < 0) fail('stream_sibling_missing');
      sections.splice(at, 0, value);
    } else {
      const destination = parent.kind === 'section' ? sections.findIndex((candidate: ExtensionViewRecord) => text(candidate, 'key') === text(parent.value, 'key')) : -1;
      if (destination < 0) fail('stream_parent_missing');
      editItems(destination, (items: ExtensionViewRecord[]) => {
        const at = before ? items.findIndex((candidate: ExtensionViewRecord) => text(candidate, 'key') === before) : items.length;
        if (at < 0) fail('stream_sibling_missing');
        items.splice(at, 0, value);
      });
    }
  } else fail('stream_unknown_operation');
  const nextView = cloneRecord(view);
  if (view === snapshot.view) nextView['sections'] = sections;
  return { protocol: snapshot.protocol, session: snapshot.session, epoch: snapshot.epoch, version: snapshot.version, root_key: snapshot.root_key, view: nextView };
}

function validateValue(value: Object | null): boolean {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value === null || typeof value !== 'string' || Array.from(value as string).length <= 4096;
  if (typeof value === 'number') return Number.isFinite(value as number) && Math.abs(value as number) <= 9007199254740991;
  if (Array.isArray(value)) {
    const values = value as Object[];
    if (values.length > 64) return false;
    for (const item of values) if (!validateValue(item as Object | null)) return false;
    return true;
  }
  if (typeof value === 'object') {
    const objectValue = record(value);
    const keys = Object.keys(objectValue);
    if (keys.length > 64) return false;
    for (const key of keys) {
      const item = objectValue[key];
      if (!safeId(key) || item !== null && typeof item === 'object' || !validateValue(item as Object | null)) return false;
    }
    return true;
  }
  return false;
}

export function reduceExtensionUIStream(current: ExtensionUIStreamSnapshot | undefined, kind: string, input: ExtensionViewRecord, allowEpochChange: boolean = false): ExtensionUIStreamResult {
  try {
    if (utf8Length(JSON.stringify(input)) > FRAME_BYTES) fail('stream_frame_limit');
    if (kind === 'snapshot') {
      const candidate = record(input['snapshot'] || input);
      const snapshot: ExtensionUIStreamSnapshot = {
        protocol: Number(candidate['protocol']), session: text(candidate, 'session'), epoch: text(candidate, 'epoch'),
        version: safeVersion(candidate['version'] as Object | undefined), root_key: text(candidate, 'root_key'), view: record(candidate['view'])
      };
      validateSnapshot(snapshot);
      if (current && (snapshot.session !== current.session || snapshot.root_key !== current.root_key || snapshot.epoch === current.epoch && snapshot.version < current.version || snapshot.epoch !== current.epoch && !allowEpochChange)) fail('stream_snapshot_stale');
      if (current && snapshot.epoch === current.epoch && snapshot.version === current.version && JSON.stringify(snapshot) !== JSON.stringify(current)) fail('stream_snapshot_conflict');
      return { snapshot: snapshot, recover: false, reason: '' };
    }
    if (kind !== 'delta' || !current) fail('stream_delta_without_snapshot');
    const delta = record(input['delta'] || input);
    const protocol = Number(delta['protocol']), session = text(delta, 'session'), epoch = text(delta, 'epoch');
    const baseVersion = safeVersion(delta['base_version'] as Object | undefined), version = safeVersion(delta['version'] as Object | undefined);
    if (!Array.isArray(delta['operations'])) fail('stream_delta_operations');
    const operations = array(delta['operations']);
    if (protocol !== 1 || session !== current.session || epoch !== current.epoch || baseVersion !== current.version || operations.length > MAX_OPERATIONS) fail('stream_delta_identity');
    if (operations.length === 0 ? version !== baseVersion && version !== baseVersion + 1 : version !== baseVersion + 1) fail('stream_delta_version');
    if (operations.length === 0) return { snapshot: version === current.version ? current : { ...current, version: version }, recover: false, reason: '' };
    let staged: ExtensionUIStreamSnapshot = current;
    for (const value of operations) staged = applyOperation(staged, record(value));
    staged.version = version;
    validateSnapshot(staged);
    return { snapshot: staged, recover: false, reason: '' };
  } catch (error) {
    return { snapshot: current, recover: true, reason: error instanceof Error ? error.message : 'invalid_stream_frame' };
  }
}

export function validStreamInteractionValue(value: Object | null): boolean { return validateValue(value); }

export function extensionUIFieldValues(view: ExtensionViewRecord, drafts: Map<string, string>): Record<string, Object> {
  const values: Record<string, Object> = {};
  const draft = (key: string, fallback: string): string => drafts.has(key) ? drafts.get(key) || '' : fallback;
  const sections = array(view['sections']);
  for (let sectionIndex = 0; sectionIndex < sections.length; sectionIndex++) {
    const section = record(sections[sectionIndex]), items = array(section['items']);
    for (let itemIndex = 0; itemIndex < items.length; itemIndex++) {
      const itemValue = items[itemIndex];
      const item = record(itemValue);
      const type = text(item, 'type'), name = text(item, 'name'), key = text(item, 'key') || '@item:' + String(sectionIndex) + ':' + String(itemIndex), kind = text(item, 'kind');
      if (!name) continue;
      if (type === 'field') {
        if (kind === 'secret') continue;
        let initial = text(item, 'value');
        if (kind === 'toggle' && initial !== 'true') initial = 'false';
        if (kind === 'multichoice' && !initial) initial = '[]';
        if (kind === 'choice' && !initial) initial = text(record(array(item['choices'])[0]), 'value');
        values[name] = draft(key, initial);
      } else if (type === 'segmented') {
        const choices = array(item['choices']);
        values[name] = draft(key, text(item, 'value') || text(record(choices[0]), 'value'));
      } else if (type === 'composer') {
        values[name] = draft(key, text(item, 'value'));
      }
    }
  }
  const review = record(view['review']), reviewName = text(review, 'name');
  if (reviewName) values[reviewName] = draft('@review', '');
  return values;
}

export function streamRequestText(request: ExtensionViewRecord, manager: string): string {
  const envelope: ExtensionViewRecord = { type: 'extension_ui_stream', request: request };
  if (manager) envelope['manager'] = manager;
  const encoded = JSON.stringify(envelope);
  if (utf8Length(encoded) > REQUEST_BYTES) fail('stream_request_limit');
  return encoded;
}
