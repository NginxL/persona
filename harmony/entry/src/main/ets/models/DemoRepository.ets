import { EndpointConfig, HttpFailure } from '../platform/HubTransport';
import { LocalStore } from '../platform/LocalStore';
import { ChatMessage, ResourceRow, Wire } from './Wire';
import { HubRepository } from './HubRepository';
import { DemoTransport } from './DemoTransport';

type DemoRecord = Record<string, Object>;

function demoRecord(keys: string[], values: Object[]): DemoRecord { return Wire.payload(keys, values); }

function omitFields(source: DemoRecord, excluded: string[]): DemoRecord {
  const keys: string[] = [], values: Object[] = [];
  Object.keys(source).forEach((key: string) => {
    if (!excluded.includes(key)) { keys.push(key); values.push(source[key]); }
  });
  return Wire.payload(keys, values);
}

const DEMO_HUB: string = 'demo';
const DEMO_MANAGER: string = 'demo';
const DEMO_MACHINE: string = 'demo-mac';
const DEMO_BASE_URL: string = 'http://127.0.0.1:1/';
const DEMO_LARK_ACCOUNT: string = 'demo-lark-account';
const DEMO_LARK_CONVERSATION: string = 'demo-lark-room';
const DEMO_MAIL_ACCOUNT: string = 'demo-mail-account';
const MAX_TEXT_BYTES: number = 100000;
const MAX_BODY_BYTES: number = 256 * 1024;
const MAX_RESPONSE_BYTES: number = 4 * 1024 * 1024;
const MAX_TRANSCRIPT_ITEMS: number = 80;
const MAX_DATA_ROWS: number = 256;
const MAX_CACHED_OPERATIONS: number = 128;

interface DemoPath {
  parts: string[];
  query: Record<string, string>;
}

/** A synchronous roster for the demo entry screen; every call returns detached rows. */
export class DemoRoster {
  features: string[] = [];
  mainId: string = '';
  machines: ResourceRow[] = [];
  sessions: ResourceRow[] = [];
  conversations: ResourceRow[] = [];
  todos: ResourceRow[] = [];
  knowledge: ResourceRow[] = [];
  inbox: ResourceRow[] = [];
}

function utf8Bytes(value: string): number {
  let bytes = 0;
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code <= 0x7f) bytes++;
    else if (code <= 0x7ff) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff && index + 1 < value.length) {
      const next = value.charCodeAt(index + 1);
      if (next >= 0xdc00 && next <= 0xdfff) { bytes += 4; index++; }
      else bytes += 3;
    } else bytes += 3;
  }
  return bytes;
}

function cloneRecord(value: DemoRecord): DemoRecord {
  const encoded = JSON.stringify(value);
  if (!encoded || utf8Bytes(encoded) > MAX_RESPONSE_BYTES) throw new HttpFailure(413, 'demo_payload_too_large');
  return Wire.record(JSON.parse(encoded) as Object);
}

function cloneRecords(values: DemoRecord[]): DemoRecord[] { return values.map((value: DemoRecord) => cloneRecord(value)); }

function nonEmpty(value: string, limit: number = 500): string {
  const trimmed = value.trim();
  if (!trimmed || utf8Bytes(trimmed) > limit) throw new HttpFailure(400, 'demo_invalid_input');
  return trimmed;
}

function textWithin(value: string, limit: number): string {
  if (utf8Bytes(value) > limit) throw new HttpFailure(413, 'demo_text_too_large');
  return value;
}

function safePath(value: string): boolean {
  if (!value || value.length > 512 || !value.startsWith('/demo/')) return false;
  if (/[\\\r\n\u0000-\u001f]/.test(value)) return false;
  return value.split('/').slice(1).every((part: string) => !!part && part !== '.' && part !== '..');
}

function queryDecode(value: string): string {
  try { return decodeURIComponent(value.replace(/\+/g, ' ')); }
  catch (_) { throw new HttpFailure(400, 'demo_invalid_path'); }
}

function parsePath(path: string): DemoPath {
  if (!path || path.length > 8192 || !path.startsWith('/api/v1/') || path.startsWith('//') || path.includes('#') ||
    /[\\\r\n\u0000-\u001f]/.test(path)) throw new HttpFailure(400, 'demo_invalid_path');
  const queryAt = path.indexOf('?'), pathname = queryAt < 0 ? path : path.slice(0, queryAt);
  const parts = pathname.split('/').slice(3);
  if (!parts.length || parts.some((part: string) => !part)) throw new HttpFailure(400, 'demo_invalid_path');
  const decoded = parts.map((part: string): string => {
    let value = '';
    try { value = decodeURIComponent(part); } catch (_) { throw new HttpFailure(400, 'demo_invalid_path'); }
    if (!value || value === '.' || value === '..' || value.includes('/') || value.includes('\\') || /[\u0000-\u001f]/.test(value))
      throw new HttpFailure(400, 'demo_invalid_path');
    return value;
  });
  const query: Record<string, string> = {};
  if (queryAt >= 0 && queryAt < path.length - 1) {
    for (const component of path.slice(queryAt + 1).split('&')) {
      if (!component) continue;
      const equals = component.indexOf('='), key = queryDecode(equals < 0 ? component : component.slice(0, equals));
      const value = queryDecode(equals < 0 ? '' : component.slice(equals + 1));
      if (!key || Object.keys(query).includes(key)) throw new HttpFailure(400, 'demo_invalid_path');
      query[key] = value;
    }
  }
  return { parts: decoded, query: query };
}

function rowFrom(data: DemoRecord, kind: string): ResourceRow {
  const row = ResourceRow.from(cloneRecord(data), kind);
  row.manager = Wire.text(data, 'manager', DEMO_MANAGER);
  return row;
}

function copyRows(data: DemoRecord[], kind: string): ResourceRow[] { return data.map((item: DemoRecord) => rowFrom(item, kind)); }

function lower(value: string): string { return value.toLowerCase(); }

/**
 * A bounded, in-memory HubRepository for the local experience. Its endpoint is deliberately inert,
 * its call override never reaches HubTransport, and its rows use only synthetic identities/paths.
 */
export class DemoRepository extends HubRepository {
  readonly features: string[] = ['managers', 'messaging'];
  private sessionsData: DemoRecord[] = [];
  private conversationsData: DemoRecord[] = [];
  private todosData: DemoRecord[] = [];
  private knowledgeData: DemoRecord[] = [];
  private inboxData: DemoRecord[] = [];
  private mailData: DemoRecord[] = [];
  private larkMessages: DemoRecord[] = [];
  private transcripts: Map<string, DemoRecord[]> = new Map();
  private pinsData: DemoRecord = {};
  private mutesData: DemoRecord = {};
  private interactionsData: DemoRecord[] = [];
  private operationResults: Map<string, DemoRecord> = new Map();
  private operationOrder: string[] = [];
  private counter: number = 0;
  private messageSequence: number = 0;
  readonly mainId: string = 'demo-main';

  constructor(_local?: LocalStore) {
    // Do not give HubRepository the app database: local demo mutations have no remote receipt/journal.
    super(new EndpointConfig(DEMO_BASE_URL, DEMO_HUB, '', '', DEMO_MANAGER), undefined, () => {}, DEMO_MANAGER,
      new DemoTransport(new EndpointConfig(DEMO_BASE_URL, DEMO_HUB, '', '', DEMO_MANAGER)));
    this.reset();
  }

  /** Reset only the synthetic in-memory world; no user or real-Hub storage is touched. */
  reset(): void {
    this.counter = 0; this.messageSequence = 0; this.transcripts = new Map(); this.operationResults = new Map(); this.operationOrder = [];
    this.pinsData = demoRecord(['spaces', 'sessions'], [[], []]);
    this.mutesData = demoRecord(['spaces', 'sessions', 'allowed'], [[], [], []]);
    const capabilities: DemoRecord = demoRecord(['read', 'send', 'approve', 'interrupt', 'terminal', 'close', 'rename'],
      [true, true, true, true, true, true, true]);
    const session = (id: string, title: string, state: string, status: string, cwd: string, branch: string): DemoRecord => demoRecord(
      ['id', 'session_id', 'machine_id', 'manager', 'title', 'state', 'status', 'cwd', 'repo_root', 'worktree', 'branch', 'space_name',
        'source_kind', 'capabilities', 'pane_id', 'slot_name', 'assistant', 'archived', 'read_only', 'updated_at'],
      [id, id, DEMO_MACHINE, DEMO_MANAGER, title, state, status, cwd, '/demo/ReleaseDesk', cwd, branch, 'ReleaseDesk', 'live',
        cloneRecord(capabilities), 'demo:pane-' + id, title, false, false, false, '2026-11-11T09:00:00Z']);
    this.sessionsData = [
      session('demo-agent-review', 'Review release checks', 'needs_you', 'idle', '/demo/ReleaseDesk', 'main'),
      session('demo-agent-changelog', 'Prepare the changelog', 'working', 'working', '/demo/ReleaseDesk', 'docs'),
      session('demo-agent-notes', 'Organize project notes', 'unread', 'done', '/demo/Notes', 'main')
    ];
    this.conversationsData = [
      demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind'],
        [this.mainId, this.mainId, DEMO_MACHINE, DEMO_MANAGER, 'Main conversation', true, 'main']),
      demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind'],
        ['demo-recent-review', 'demo-recent-review', DEMO_MACHINE, DEMO_MANAGER, 'Release checklist', false, 'recent']),
      demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind', 'automatic'],
        ['demo-auto-summary', 'demo-auto-summary', DEMO_MACHINE, DEMO_MANAGER, 'Daily summary', false, 'automatic', true])
    ];
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, this.mainId), [
      this.item('assistant', 'Ready to explore the local Persona demo. Messages stay on this device and do not reach an agent or service.')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-recent-review'), [
      this.item('user', 'What should I review before the release?'), this.item('assistant', 'Start with the release checks, then confirm the changelog and open issues.')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-auto-summary'), [
      this.item('assistant', 'Three sample tasks are ready to inspect in this local demo.')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-agent-review'), [
      this.item('assistant', 'The release checklist is ready for review.'), this.item('assistant', 'A sample plan is waiting for your decision.')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-agent-changelog'), [
      this.item('user', 'Prepare a concise changelog draft.'), this.item('assistant', 'The local draft groups the sample changes by feature and fix.')
    ]);
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, 'demo-agent-notes'), [
      this.item('assistant', 'The sample notes are organized. This session is complete.')
    ]);
    this.interactionsData = [demoRecord(['id', 'machine_id', 'session_id', 'manager', 'status', 'revision', 'interaction'], [
      'demo-plan-review', DEMO_MACHINE, 'demo-agent-review', DEMO_MANAGER, 'open', 'demo-rev-1',
      demoRecord(['id', 'kind', 'plan', 'status'], ['demo-plan-review', 'plan', 'Run the sample release checklist in this local demo?', 'open'])
    ])];
    this.todosData = [
      demoRecord(['id', 'manager', 'title', 'notes', 'state', 'status', 'origin', 'created_at', 'updated_at', 'agents', 'spaces'], [
        'demo-todo-release', DEMO_MANAGER, 'Review the release checklist', 'Check the sample release notes and confirm the open plan.',
        'needs_you', 'needs_you', 'manual', '2026-11-10T08:00:00Z', '2026-11-11T09:00:00Z', [demoRecord(
          ['machine_id', 'session_id', 'title', 'cwd', 'agent', 'source', 'linked_at'],
          [DEMO_MACHINE, 'demo-agent-review', 'Review release checks', '/demo/ReleaseDesk', '', 'manual', '2026-11-11T09:00:00Z'])], []]),
      demoRecord(['id', 'manager', 'title', 'notes', 'state', 'status', 'origin', 'created_at', 'updated_at', 'agents', 'spaces'], [
        'demo-todo-docs', DEMO_MANAGER, 'Prepare release notes', 'Collect the sample changes in one concise note.',
        'not_started', 'not_started', 'manual', '2026-11-10T09:00:00Z', '2026-11-10T09:00:00Z', [], []]),
      demoRecord(['id', 'manager', 'title', 'notes', 'state', 'status', 'done_at', 'created_at', 'updated_at', 'agents', 'spaces'], [
        'demo-todo-done', DEMO_MANAGER, 'Check the example project', 'This sample task is complete.', 'done', 'done',
        '2026-11-10T10:00:00Z', '2026-11-09T10:00:00Z', '2026-11-10T10:00:00Z', [], []])
    ];
    this.knowledgeData = [
      this.knowledgeEntry('projects', 'demo-project-release', 'Release Desk', 'A sample project for trying the local workspace.', 'The Release Desk example keeps the checklist, release notes, and follow-up tasks together.', ['demo', 'release'], demoRecord(['kind', 'status', 'dirs'], ['short', 'active', [DEMO_MACHINE + ':/demo/ReleaseDesk']])),
      this.knowledgeEntry('docs', 'demo-doc-release', 'Release checklist', 'A short checklist for the example release.', '1. Review the plan.\n2. Check the changelog.\n3. Close or reopen the follow-up task.', ['demo', 'checklist']),
      this.knowledgeEntry('chats', 'demo-chat-notes', 'Planning discussion', 'A synthetic discussion about the sample release.', 'The team agreed to review the plan before preparing the release notes.', ['demo', 'planning']),
      this.knowledgeEntry('people', 'demo-person-reviewer', 'Alex Example', 'A sample reviewer in the demo workspace.', 'This contact is synthetic and is not connected to a real account.', ['demo', 'review']),
      this.knowledgeEntry('meetings', 'demo-meeting-kickoff', 'Release kickoff', 'A sample meeting record.', 'Review scope, identify risks, and agree on the next step.', ['demo', 'release']),
      this.knowledgeEntry('events', 'demo-event-check', 'Checklist review', 'A sample project event.', 'The example checklist was added to the Release Desk project.', ['demo', 'checklist']),
      this.knowledgeEntry('recordings', 'demo-recording-notes', 'Planning notes', 'A synthetic recording entry.', 'No audio file or personal recording is stored in the local demo.', ['demo', 'planning'])
    ];
    this.mailData = [demoRecord(['account_id', 'mailbox', 'uid', 'message_id', 'subject', 'from', 'to', 'date', 'text', 'seen', 'attachments'], [
      DEMO_MAIL_ACCOUNT, 'INBOX', 42, '<demo-mail-42@example.test>', 'Release notes review',
      demoRecord(['name', 'email'], ['Alex Example', 'alex@example.test']),
      [demoRecord(['name', 'email'], ['Demo Inbox', 'inbox@example.test'])], '2026-11-11T08:30:00Z',
      'Could you review the sample release notes and share any missing items?', false, []
    ])];
    this.inboxData = [
      demoRecord(['id', 'manager', 'kind', 'source', 'account', 'account_name', 'title', 'sender', 'summary', 'state', 'unread', 'version', 'received_at', 'conversation', 'handoffs'], [
        'demo-inbox-lark', DEMO_MANAGER, 'inbox', 'lark', DEMO_LARK_ACCOUNT, 'Demo Team', 'Release room', 'Mina Example',
        'Can someone review the sample release checklist?', 'unread', true, 'demo-lark-v1', '2026-11-11T08:15:00Z',
        demoRecord(['source', 'id', 'account', 'name', 'start_position', 'topic'],
          ['lark', DEMO_LARK_CONVERSATION, DEMO_LARK_ACCOUNT, 'Release room', '1001', false]), []]),
      demoRecord(['id', 'manager', 'kind', 'source', 'account', 'account_name', 'title', 'sender', 'summary', 'state', 'unread', 'version', 'received_at', 'mail', 'reply_target', 'handoffs'], [
        'demo-inbox-mail', DEMO_MANAGER, 'inbox', 'mail', DEMO_MAIL_ACCOUNT, 'Demo Mailbox', 'Release notes review', 'Alex Example',
        'Could you review the sample release notes?', 'unread', true, 'demo-mail-v1', '2026-11-11T08:30:00Z',
        demoRecord(['account_id', 'mailbox', 'uids', 'account_name', 'from'], [DEMO_MAIL_ACCOUNT, 'INBOX', [42], 'Demo Mailbox',
          demoRecord(['name', 'email'], ['Alex Example', 'alex@example.test'])]),
        demoRecord(['uid', 'version'], [42, 'demo-mail-target-v1']), []])
    ];
    this.larkMessages = [
      demoRecord(['id', 'position', 'sender', 'sender_name', 'text', 'mine', 'state', 'can_reply', 'thread_kind', 'reactions'], [
        'demo-lark-message-1', '1001', demoRecord(['id', 'name'], ['demo-mina', 'Mina Example']), 'Mina Example',
        'Can someone review the sample release checklist?', false, 'unread', true, 'message', []]),
      demoRecord(['id', 'position', 'sender', 'sender_name', 'text', 'mine', 'state', 'can_reply', 'thread_kind', 'reactions'], [
        'demo-lark-message-2', '1002', demoRecord(['id', 'name'], ['demo-you', 'Demo user']), 'Demo user',
        'I will check the sample plan first.', true, 'read', true, 'message', []]),
      demoRecord(['id', 'position', 'sender', 'sender_name', 'text', 'mine', 'state', 'can_reply', 'thread_kind', 'thread_root', 'reactions'], [
        'demo-lark-message-3', '1003', demoRecord(['id', 'name'], ['demo-mina', 'Mina Example']), 'Mina Example',
        'Thanks. Leave a note here when you are done.', false, 'read', true, 'message', 'demo-lark-message-1', []])
    ];
  }

  /** `seed` is an explicit reset for callers that want the original sample data again. */
  seed(): DemoRoster { this.reset(); return this.roster(); }

  roster(): DemoRoster {
    const roster = new DemoRoster(); roster.features = this.features.slice(); roster.mainId = this.mainId;
    roster.machines = copyRows([this.machineData()], 'machine'); roster.sessions = copyRows(this.activeSessions(), 'session');
    roster.conversations = copyRows(this.conversationsData, 'conversation'); roster.todos = copyRows(this.todosData, 'todo');
    roster.knowledge = copyRows(this.knowledgeData.filter((item: DemoRecord) => Wire.text(item, 'kind') === 'projects'), 'knowledge');
    roster.inbox = copyRows(this.inboxData, 'inbox');
    roster.sessions.forEach((row: ResourceRow) => row.group = DEMO_HUB);
    roster.machines.forEach((row: ResourceRow) => row.group = DEMO_HUB);
    roster.conversations.forEach((row: ResourceRow) => { row.group = DEMO_HUB; row.machine = DEMO_MACHINE; row.session = row.id; });
    roster.todos.forEach((row: ResourceRow) => row.group = DEMO_HUB);
    roster.knowledge.forEach((row: ResourceRow) => { row.group = DEMO_HUB; row.path = Wire.text(row.data(), 'kind'); });
    roster.inbox.forEach((row: ResourceRow) => row.group = DEMO_HUB);
    return roster;
  }

  /** Chat rows for the demo-specific ClientModel.loadChat path. */
  history(row: ResourceRow): ChatMessage[] {
    return this.historyItems(row).map((item: DemoRecord): ChatMessage => {
      const message = new ChatMessage(Wire.text(item, 'id'), Wire.text(item, 'role', 'assistant'), Wire.text(item, 'text'));
      message.at = Date.parse(Wire.text(item, 'ts')) || 0; message.itemId = message.id;
      return message;
    });
  }

  historyItems(row: ResourceRow): DemoRecord[] {
    const key = this.transcriptKey(row.machine || Wire.text(row.data(), 'machine_id'), row.session || row.id);
    return cloneRecords(this.transcripts.get(key) || []);
  }

  /** Add a user message and a clearly synthetic reply to this demo chat only. */
  sendLocal(row: ResourceRow, text: string): ChatMessage[] {
    const value = nonEmpty(text, MAX_TEXT_BYTES);
    const session = this.chatTarget(row);
    this.appendChat(Wire.text(session, 'machine_id'), Wire.text(session, 'session_id'), value, 'local-send-' + (++this.counter));
    return this.history(row);
  }

  async call(path: string, method: string = 'GET', body: DemoRecord = {}, operationId: string = '', admit?: () => boolean,
    _completedAsAck: boolean = false): Promise<DemoRecord> {
    const request = parsePath(path), parts = request.parts, query = request.query;
    const verb = method.toUpperCase();
    if (!['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].includes(verb)) throw new HttpFailure(405, 'demo_unsupported');
    if (query['manager'] && query['manager'] !== DEMO_MANAGER) throw new HttpFailure(404, 'demo_owner_not_found');
    if (body !== undefined && body !== null && (typeof body !== 'object' || Array.isArray(body))) throw new HttpFailure(400, 'demo_invalid_input');
    const encodedBody = JSON.stringify(body || {});
    if (!encodedBody || utf8Bytes(encodedBody) > MAX_BODY_BYTES) throw new HttpFailure(413, 'demo_payload_too_large');
    const input = Wire.record(JSON.parse(encodedBody) as Object);
    if (admit && !admit()) throw new HttpFailure(0, 'request_updated', false);

    const resource = parts[0];
    if (resource === 'assistant') return this.assistant(parts, verb, input, operationId);
    if (resource === 'managers') return this.managers(parts, verb);
    if (resource === 'machines') return this.machinesRoute(parts, verb);
    if (resource === 'sessions') return this.sessionsRoute(parts, verb, input, operationId);
    if (resource === 'interactions') return this.interactions(parts, verb, input);
    if (resource === 'pins' || resource === 'mutes') return this.preferences(resource, parts, verb, input);
    if (resource === 'todos') return this.todosRoute(parts, query, verb, input);
    if (resource === 'kb-search') return this.searchKnowledge(query, verb);
    if (resource === 'kb') return this.knowledgeRoute(parts, query, verb, input);
    if (resource === 'inbox') return this.inboxRoute(parts, verb, input, operationId);
    if (resource === 'messaging') return this.messaging(parts, query, verb, input, operationId);
    if (resource === 'lark') return this.legacyLark(parts, query, verb, input);
    if (resource === 'mail') return this.mail(parts, query, verb, input);
    throw new HttpFailure(404, 'demo_unsupported');
  }

  /** Binary uploads are intentionally unavailable in the local demo. */
  async upload(_path: string, _bytes: ArrayBuffer, _admit?: () => boolean): Promise<DemoRecord> {
    throw new HttpFailure(501, 'demo_unsupported');
  }

  close(): void { /* The demo never connects its inert transport. */ }

  private machineData(): DemoRecord {
    const repositories: DemoRecord[] = [demoRecord(['path', 'name'], ['/demo/ReleaseDesk', 'ReleaseDesk'])];
    return demoRecord(['id', 'manager', 'name', 'machine_id', 'platform', 'os', 'online', 'trust', 'pending', 'pending_confirm', 'directories', 'repos'],
      [DEMO_MACHINE, DEMO_MANAGER, 'My Mac', DEMO_MACHINE, 'darwin', 'macOS', true, 'personal', false, false,
        ['/demo/ReleaseDesk', '/demo/Notes'], repositories]);
  }

  private activeSessions(): DemoRecord[] { return this.sessionsData.filter((item: DemoRecord) => !Wire.bool(item, 'archived')); }

  private knowledgeEntry(kind: string, id: string, title: string, summary: string, content: string, tags: string[], extra: DemoRecord = {}): DemoRecord {
    const meta = cloneRecord(extra);
    meta['importance'] = 'P2'; meta['relevance'] = 75; meta['tags'] = tags.slice();
    return demoRecord(['id', 'kind', 'manager', 'title', 'name', 'summary', 'content', 'meta', 'tags', 'updated_at', 'related'],
      [id, kind, DEMO_MANAGER, title, title, summary, content, meta, tags.slice(), '2026-11-11T09:00:00Z', []]);
  }

  private item(role: string, text: string, clientId: string = ''): DemoRecord {
    this.messageSequence++;
    const item = demoRecord(['id', 'seq', 'role', 'kind', 'text', 'ts'],
      ['demo-item-' + this.messageSequence, this.messageSequence, role, 'text', textWithin(text, MAX_TEXT_BYTES), '2026-11-11T09:00:00Z']);
    if (clientId) item['client_msg_id'] = clientId;
    return item;
  }

  private transcriptKey(machine: string, session: string): string { return JSON.stringify([machine, session]); }

  private appendTranscript(machine: string, session: string, items: DemoRecord[]): void {
    const key = this.transcriptKey(machine, session), current = this.transcripts.get(key) || [];
    const next = current.concat(items);
    let kept = next.slice(-MAX_TRANSCRIPT_ITEMS);
    while (kept.length > 2 && utf8Bytes(JSON.stringify(kept)) > 1024 * 1024) kept = kept.slice(2);
    this.transcripts.set(key, kept);
  }

  private appendChat(machine: string, session: string, text: string, clientId: string): void {
    if (!this.findChat(machine, session)) throw new HttpFailure(404, 'demo_chat_not_found');
    const current = this.transcripts.get(this.transcriptKey(machine, session)) || [];
    if (current.some((item: DemoRecord) => Wire.text(item, 'client_msg_id') === clientId)) return;
    this.appendTranscript(machine, session, [this.item('user', text, clientId), this.item('assistant', 'Received in the local demo. No message was sent to a real agent or service.')]);
  }

  private chatTarget(row: ResourceRow): DemoRecord {
    if (!row || !['session', 'conversation'].includes(row.kind)) throw new HttpFailure(400, 'demo_chat_not_found');
    const machine = row.machine || Wire.text(row.data(), 'machine_id');
    const session = row.session || row.id || Wire.text(row.data(), 'session_id');
    if (!this.findChat(machine, session)) throw new HttpFailure(404, 'demo_chat_not_found');
    return demoRecord(['machine_id', 'session_id'], [machine, session]);
  }

  private findChat(machine: string, session: string): DemoRecord | undefined {
    if (machine !== DEMO_MACHINE || !session) return undefined;
    return this.sessionsData.concat(this.conversationsData).find((item: DemoRecord) =>
      Wire.text(item, 'machine_id') === machine && (Wire.text(item, 'session_id') || Wire.text(item, 'id')) === session);
  }

  private replaceChatRecord(previous: DemoRecord, next: DemoRecord): void {
    if (this.sessionsData.includes(previous)) this.sessionsData = this.sessionsData.map((item: DemoRecord) => item === previous ? next : item);
    else this.conversationsData = this.conversationsData.map((item: DemoRecord) => item === previous ? next : item);
  }

  private ensureRoom(rows: DemoRecord[]): void { if (rows.length >= MAX_DATA_ROWS) throw new HttpFailure(413, 'demo_limit_reached'); }

  private rememberOperation(id: string, result: DemoRecord): void {
    this.operationResults.set(id, cloneRecord(result)); this.operationOrder.push(id);
    while (this.operationOrder.length > MAX_CACHED_OPERATIONS) {
      const expired = this.operationOrder.shift() || '';
      if (expired && !this.operationOrder.includes(expired)) this.operationResults.delete(expired);
    }
  }

  private assistant(parts: string[], method: string, body: DemoRecord, _operationId: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['main', 'machine_id', 'conversations'],
      [this.mainId, DEMO_MACHINE, cloneRecords(this.conversationsData)]);
    if (parts.length === 2 && parts[1] === 'conversations' && method === 'POST') {
      if (Wire.bool(body, 'main')) {
        const main = this.conversationsData.find((item: DemoRecord) => Wire.bool(item, 'main'));
        if (!main) throw new HttpFailure(404, 'demo_chat_not_found');
        return demoRecord(['session'], [cloneRecord(main)]);
      }
      this.ensureRoom(this.conversationsData);
      const id = 'demo-conversation-' + (++this.counter);
      const conversation = demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind'],
        [id, id, DEMO_MACHINE, DEMO_MANAGER, 'New conversation', false, 'recent']);
      this.conversationsData.push(conversation); this.transcripts.set(this.transcriptKey(DEMO_MACHINE, id), []);
      return demoRecord(['session'], [cloneRecord(conversation)]);
    }
    if (parts.length === 4 && parts[1] === 'conversations' && parts[3] === 'feed' && method === 'GET') {
      if (!this.findChat(DEMO_MACHINE, parts[2])) throw new HttpFailure(404, 'demo_chat_not_found');
      return demoRecord(['items'], [[]]);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private managers(parts: string[], method: string): DemoRecord {
    if (parts.length !== 1 || method !== 'GET') throw new HttpFailure(404, 'demo_unsupported');
    const rows: DemoRecord[] = [demoRecord(['id', 'name', 'default'], [DEMO_MANAGER, 'Local demo', true])];
    return demoRecord(['default', 'managers'], [DEMO_MANAGER, rows]);
  }

  private machinesRoute(parts: string[], method: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['machines'], [[this.machineData()]]);
    if (parts.length === 2 && parts[1] === DEMO_MACHINE && method === 'GET') return demoRecord(['machine'], [this.machineData()]);
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private sessionsRoute(parts: string[], method: string, body: DemoRecord, operationId: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['sessions'], [cloneRecords(this.activeSessions())]);
    if (parts.length === 2 && parts[1] === 'archived' && method === 'GET')
      return demoRecord(['sessions'], [cloneRecords(this.sessionsData.filter((item: DemoRecord) => Wire.bool(item, 'archived')))]);
    if (parts.length === 1 && method === 'POST') return this.createAgentRecord(body);
    if (parts.length < 3 || parts[1] !== DEMO_MACHINE) throw new HttpFailure(404, 'demo_session_not_found');
    const sessionId = parts[2], action = parts[3] || '';
    const session = this.sessionsData.find((item: DemoRecord) => Wire.text(item, 'machine_id') === parts[1] && Wire.text(item, 'session_id') === sessionId);
    const record = session || this.conversationsData.find((item: DemoRecord) =>
      Wire.text(item, 'machine_id') === parts[1] && (Wire.text(item, 'session_id') || Wire.text(item, 'id')) === sessionId);
    if (!record) throw new HttpFailure(404, 'demo_session_not_found');
    if (!action && method === 'GET') return demoRecord(['session'], [cloneRecord(record)]);
    if (action === 'items' && method === 'GET') return this.transcript(record, sessionId);
    if (action === 'messages' && method === 'POST') {
      if (Wire.has(body, 'images') || Wire.has(body, 'files')) throw new HttpFailure(501, 'demo_unsupported');
      const text = nonEmpty(Wire.text(body, 'text'), MAX_TEXT_BYTES), clientId = nonEmpty(Wire.text(body, 'client_msg_id', operationId), 200);
      this.appendChat(DEMO_MACHINE, sessionId, text, clientId); return demoRecord(['accepted', 'state'], [true, 'done']);
    }
    if (action === 'rename' && method === 'POST') {
      const title = Wire.text(body, 'title'), tab = Wire.text(body, 'tab_name');
      if (!title.trim() && !tab.trim()) throw new HttpFailure(400, 'demo_invalid_input');
      const next = cloneRecord(record);
      if (title.trim()) next['title'] = nonEmpty(title);
      if (tab.trim()) next['slot_name'] = nonEmpty(tab);
      this.replaceChatRecord(record, next);
      return demoRecord(['session'], [cloneRecord(next)]);
    }
    if (action === 'seen' && method === 'POST') {
      record['state'] = 'idle'; record['unread'] = false;
      return demoRecord(['session'], [cloneRecord(record)]);
    }
    if (action === 'close' && method === 'POST') {
      if (!session) throw new HttpFailure(404, 'demo_unsupported');
      record['status'] = 'done'; record['state'] = 'done'; record['closed_at'] = '2026-11-11T09:30:00Z';
      return demoRecord(['session'], [cloneRecord(record)]);
    }
    if (!action && method === 'DELETE') {
      if (!session) throw new HttpFailure(404, 'demo_unsupported');
      this.sessionsData = this.sessionsData.filter((item: DemoRecord) => item !== record);
      this.interactionsData = this.interactionsData.filter((item: DemoRecord) => Wire.text(item, 'session_id') !== sessionId);
      this.transcripts.delete(this.transcriptKey(DEMO_MACHINE, sessionId));
      return demoRecord(['deleted'], [true]);
    }
    if (action === 'interrupt' && method === 'POST') {
      record['status'] = 'interrupted'; record['state'] = 'idle'; return demoRecord(['state'], ['done']);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private transcript(record: DemoRecord, session: string): DemoRecord {
    const items = cloneRecords(this.transcripts.get(this.transcriptKey(Wire.text(record, 'machine_id'), session)) || []);
    const last = items.length ? Wire.number(items[items.length - 1], 'seq') : 0;
    return demoRecord(['items', 'from_seq', 'to_seq', 'has_more', 'more_after', 'backlog'],
      [items, items.length ? Wire.number(items[0], 'seq') : 0, last, false, false, false]);
  }

  private createAgentRecord(body: DemoRecord): DemoRecord {
    const machine = Wire.text(body, 'machine_id'), requestedCwd = Wire.text(body, 'cwd'), cwd = requestedCwd || '/demo/ReleaseDesk';
    const launcher = Wire.text(body, 'launcher');
    const prompt = nonEmpty(Wire.text(body, 'prompt'), MAX_TEXT_BYTES);
    if (machine !== DEMO_MACHINE || !safePath(cwd) || !['', 'codex', 'claude', 'gemini'].includes(launcher)) throw new HttpFailure(400, 'demo_invalid_input');
    this.ensureRoom(this.sessionsData);
    const todoId = Wire.text(body, 'todo_id');
    const todo = todoId ? this.todosData.find((item: DemoRecord) => Wire.text(item, 'id') === todoId) : undefined;
    if (todoId && !todo) throw new HttpFailure(404, 'demo_todo_not_found');
    const id = 'demo-agent-' + (++this.counter), title = prompt.split(/\r?\n/)[0].trim().slice(0, 80) || 'New agent';
    const capabilities = demoRecord(['read', 'send', 'approve', 'interrupt', 'terminal', 'close', 'rename'],
      [true, true, true, true, true, true, true]);
    const session = demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'state', 'status', 'cwd', 'repo_root', 'worktree',
      'branch', 'source_kind', 'assistant', 'archived', 'capabilities', 'created_at', 'updated_at'],
      [id, id, DEMO_MACHINE, DEMO_MANAGER, title, 'working', 'working', cwd, cwd, cwd, 'demo', 'live', false, false,
        capabilities, '2026-11-11T09:30:00Z', '2026-11-11T09:30:00Z']);
    if (launcher) session['launcher'] = launcher;
    this.sessionsData.unshift(session);
    if (todo) {
      const agents = Wire.rows(todo, 'agents').filter((item: DemoRecord) => Wire.text(item, 'session_id') !== id);
      agents.push(demoRecord(['machine_id', 'session_id', 'title', 'cwd', 'agent', 'source', 'linked_at'],
        [DEMO_MACHINE, id, title, cwd, launcher, 'manual', '2026-11-11T09:30:00Z']));
      todo['agents'] = agents;
    }
    this.transcripts.set(this.transcriptKey(DEMO_MACHINE, id), [this.item('user', prompt), this.item('assistant', 'A sample agent was created inside the local demo.')]);
    return demoRecord(['session'], [cloneRecord(session)]);
  }

  private interactions(parts: string[], method: string, body: DemoRecord): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['interactions'],
      [cloneRecords(this.interactionsData.filter((item: DemoRecord) => Wire.text(item, 'status') === 'open'))]);
    if (parts.length === 3 && parts[2] === 'resolve' && method === 'POST') {
      const request = this.interactionsData.find((item: DemoRecord) => Wire.text(item, 'id') === parts[1]);
      if (!request || Wire.text(request, 'status') !== 'open') throw new HttpFailure(409, 'request_updated');
      const interaction = Wire.object(request, 'interaction');
      if (Wire.text(interaction, 'kind') !== 'plan' || typeof body['approve'] !== 'boolean' || Object.keys(body).some((key: string) => !['approve', 'feedback'].includes(key)))
        throw new HttpFailure(400, 'demo_invalid_resolution');
      request['status'] = 'resolved'; request['resolution'] = cloneRecord(body);
      interaction['status'] = 'resolved'; request['interaction'] = interaction;
      const session = this.sessionsData.find((item: DemoRecord) => Wire.text(item, 'session_id') === Wire.text(request, 'session_id'));
      if (session) { session['status'] = body['approve'] ? 'working' : 'interrupted'; session['state'] = body['approve'] ? 'working' : 'idle'; }
      if (session) this.appendTranscript(DEMO_MACHINE, Wire.text(session, 'session_id'), [this.item('assistant', body['approve'] ? 'Plan approved in the local demo.' : 'Plan declined in the local demo.')]);
      return demoRecord(['status'], ['resolved']);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private preferences(kind: string, parts: string[], method: string, body: DemoRecord): DemoRecord {
    if (parts.length !== 1) throw new HttpFailure(404, 'demo_unsupported');
    const current = kind === 'pins' ? this.pinsData : this.mutesData;
    if (method === 'GET') return cloneRecord(current);
    if (method !== 'PUT') throw new HttpFailure(404, 'demo_unsupported');
    const spaces = this.safeStringList(body, 'spaces'), sessions = this.safeStringList(body, 'sessions');
    const next = demoRecord(['spaces', 'sessions'], [spaces, sessions]);
    if (kind === 'mutes') next['allowed'] = this.safeStringList(body, 'allowed');
    if (Object.keys(body).some((key: string) => !['spaces', 'sessions', 'allowed'].includes(key)) || kind === 'pins' && Wire.has(body, 'allowed'))
      throw new HttpFailure(400, 'demo_invalid_input');
    if (kind === 'pins') this.pinsData = next; else this.mutesData = next;
    return cloneRecord(next);
  }

  private safeStringList(body: DemoRecord, key: string): string[] {
    const value = body[key];
    if (!Array.isArray(value) || (value as Object[]).length > 512 || (value as Object[]).some((item: Object) => typeof item !== 'string' || utf8Bytes(item as string) > 512))
      throw new HttpFailure(400, 'demo_invalid_input');
    return (value as Object[]).map((item: Object) => item as string).filter((item: string, index: number, all: string[]) => all.indexOf(item) === index);
  }

  private todosRoute(parts: string[], query: Record<string, string>, method: string, body: DemoRecord): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['todos'], [cloneRecords(this.todosData)]);
    if (parts.length === 1 && method === 'POST') {
      this.ensureRoom(this.todosData);
      const title = nonEmpty(Wire.text(body, 'title')), notes = textWithin(Wire.text(body, 'notes'), 20000);
      const todo = demoRecord(['id', 'manager', 'title', 'notes', 'state', 'status', 'origin', 'created_at', 'updated_at', 'agents', 'spaces'],
        ['demo-todo-' + (++this.counter), DEMO_MANAGER, title, notes, 'not_started', 'not_started', 'manual',
          '2026-11-11T09:30:00Z', '2026-11-11T09:30:00Z', [], []]);
      this.todosData.unshift(todo); return demoRecord(['todo'], [cloneRecord(todo)]);
    }
    if (parts.length < 2) throw new HttpFailure(404, 'demo_todo_not_found');
    const id = parts[1], todo = this.todosData.find((item: DemoRecord) => Wire.text(item, 'id') === id);
    if (!todo) throw new HttpFailure(404, 'demo_todo_not_found');
    if (parts.length === 2 && method === 'GET') return demoRecord(['todo'], [cloneRecord(todo)]);
    if (parts.length === 2 && method === 'PATCH') {
      const allowed = ['title', 'notes', 'done'];
      if (Object.keys(body).some((key: string) => !allowed.includes(key))) throw new HttpFailure(400, 'demo_invalid_input');
      let updated = cloneRecord(todo);
      if (Wire.has(body, 'title')) updated['title'] = nonEmpty(Wire.text(body, 'title'));
      if (Wire.has(body, 'notes')) updated['notes'] = textWithin(Wire.text(body, 'notes'), 20000);
      if (Wire.has(body, 'done')) {
        if (typeof body['done'] !== 'boolean') throw new HttpFailure(400, 'demo_invalid_input');
        if (body['done']) { updated['done_at'] = '2026-11-11T09:30:00Z'; updated['done_by'] = 'demo'; updated['state'] = 'done'; updated['status'] = 'done'; }
        else {
          updated = omitFields(updated, ['done_at', 'done_by']);
          updated['reopened_at'] = '2026-11-11T09:30:00Z'; updated['state'] = 'not_started'; updated['status'] = 'not_started';
        }
      }
      updated['updated_at'] = '2026-11-11T09:30:00Z'; this.todosData = this.todosData.map((item: DemoRecord) => item === todo ? updated : item);
      return demoRecord(['todo'], [cloneRecord(updated)]);
    }
    if (parts.length === 2 && method === 'DELETE') {
      this.todosData = this.todosData.filter((item: DemoRecord) => item !== todo); return demoRecord(['deleted'], [true]);
    }
    if (parts.length === 5 && parts[2] === 'agents' && ['PUT', 'DELETE'].includes(method)) {
      const machine = parts[3], sessionId = parts[4];
      const agent = this.sessionsData.find((item: DemoRecord) => Wire.text(item, 'machine_id') === machine && Wire.text(item, 'session_id') === sessionId);
      if (!agent || machine !== DEMO_MACHINE) throw new HttpFailure(404, 'demo_agent_not_found');
      const rows = Wire.rows(todo, 'agents').filter((item: DemoRecord) => !(Wire.text(item, 'machine_id') === machine && Wire.text(item, 'session_id') === sessionId));
      if (method === 'PUT') rows.push(demoRecord(['machine_id', 'session_id', 'title', 'cwd', 'agent', 'source', 'linked_at'],
        [machine, sessionId, Wire.text(agent, 'title'), Wire.text(agent, 'cwd'), Wire.text(agent, 'launcher'), 'manual', '2026-11-11T09:30:00Z']));
      todo['agents'] = rows; return demoRecord(['todo'], [cloneRecord(todo)]);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private knowledgeKinds(): string[] { return ['projects', 'chats', 'people', 'docs', 'meetings', 'events', 'recordings']; }

  private searchKnowledge(query: Record<string, string>, method: string): DemoRecord {
    if (method !== 'GET') throw new HttpFailure(404, 'demo_unsupported');
    const needle = lower(nonEmpty(query['q'] || '', 2000));
    const matches = this.knowledgeData.map((entry: DemoRecord) => {
      const body = [Wire.text(entry, 'title'), Wire.text(entry, 'summary'), Wire.text(entry, 'content'), Wire.strings(Wire.object(entry, 'meta'), 'tags').join(' ')].join('\n');
      const lines = body.split('\n').filter((line: string) => line.toLowerCase().includes(needle)).slice(0, 20);
      const lineHits: DemoRecord[] = lines.map((line: string): DemoRecord => demoRecord(['line'], [line]));
      return demoRecord(['entry', 'in_name', 'count', 'hits'],
        [cloneRecord(entry), Wire.text(entry, 'title').toLowerCase().includes(needle), lines.length, lineHits]);
    }).filter((item: DemoRecord) => Wire.bool(item, 'in_name') || Wire.number(item, 'count') > 0);
    if (query['results'] === '1') return demoRecord(['results'], [cloneRecords(matches)]);
    if (query['messages'] === '0') {
      const hits: DemoRecord[] = matches.map((item: DemoRecord): DemoRecord => {
        const entry = Wire.object(item, 'entry');
        return demoRecord(['path', 'line'], [Wire.text(entry, 'kind') + '/' + Wire.text(entry, 'id'),
          Wire.text(Wire.rows(item, 'hits')[0] || {}, 'line', Wire.text(entry, 'title'))]);
      });
      return demoRecord(['hits'], [hits]);
    }
    return demoRecord(['results'], [cloneRecords(matches)]);
  }

  private knowledgeRoute(parts: string[], query: Record<string, string>, method: string, body: DemoRecord): DemoRecord {
    if (parts.length < 2 || !(this.knowledgeKinds().includes(parts[1]) || parts[1] === 'moments')) throw new HttpFailure(404, 'demo_unsupported');
    const kind = parts[1];
    if (kind === 'moments' && parts.length === 3 && method === 'GET' && parts[2] === 'demo-moment-release')
      return demoRecord(['markdown'], ['The sample release checklist was added to Release Desk.']);
    if (parts.length === 2 && method === 'GET') {
      const tag = query['tag'] || '', items = this.knowledgeData.filter((item: DemoRecord) => Wire.text(item, 'kind') === kind &&
        (!tag || Wire.strings(Wire.object(item, 'meta'), 'tags').includes(tag)));
      const tags: string[] = [];
      this.knowledgeData.filter((item: DemoRecord) => Wire.text(item, 'kind') === kind).forEach((item: DemoRecord) => {
        Wire.strings(Wire.object(item, 'meta'), 'tags').forEach((tagValue: string) => { if (!tags.includes(tagValue)) tags.push(tagValue); });
      });
      return demoRecord(['items', 'tags'], [cloneRecords(items), tags]);
    }
    if (parts.length === 2 && method === 'POST') {
      this.ensureRoom(this.knowledgeData);
      const title = nonEmpty(Wire.text(body, 'title')), summary = textWithin(Wire.text(body, 'summary'), 10000);
      const content = textWithin(Wire.text(body, 'content'), MAX_TEXT_BYTES), tags = this.bodyStringArray(body, 'tags');
      const entry = this.knowledgeEntry(kind, 'demo-' + kind + '-' + (++this.counter), title, summary || content.slice(0, 500), content, tags);
      this.knowledgeData.unshift(entry); return demoRecord(['item'], [cloneRecord(entry)]);
    }
    if (parts.length !== 3) throw new HttpFailure(404, 'demo_unsupported');
    const entry = this.knowledgeData.find((item: DemoRecord) => Wire.text(item, 'kind') === kind && Wire.text(item, 'id') === parts[2]);
    if (!entry) throw new HttpFailure(404, 'demo_knowledge_not_found');
    if (method === 'GET') {
      const q = query['q'] || '', lines = q ? Wire.text(entry, 'content').split('\n').filter((line: string) => line.toLowerCase().includes(q.toLowerCase())) : [];
      const matches: DemoRecord[] = lines.map((line: string, index: number): DemoRecord => demoRecord(['line', 'index'], [line, index]));
      return demoRecord(['detail', 'matches', 'agents'], [cloneRecord(entry), matches,
        cloneRecords(this.sessionsData.filter((session: DemoRecord) => Wire.text(session, 'project_id') === Wire.text(entry, 'id')))]);
    }
    if (method === 'PUT') {
      if (Object.keys(body).some((key: string) => !['tags', 'importance', 'relevance', 'status', 'project_kind'].includes(key))) throw new HttpFailure(400, 'demo_invalid_input');
      const updated = cloneRecord(entry), meta = cloneRecord(Wire.object(entry, 'meta'));
      const tags = Wire.has(body, 'tags') ? this.bodyStringArray(body, 'tags') : [];
      let importance = '', relevance = -1, status = '', projectKind = '';
      if (Wire.has(body, 'importance')) { importance = nonEmpty(Wire.text(body, 'importance'), 2); if (!['P0', 'P1', 'P2', 'P3'].includes(importance)) throw new HttpFailure(400, 'demo_invalid_input'); }
      if (Wire.has(body, 'relevance')) { relevance = Wire.number(body, 'relevance', -1); if (!Number.isInteger(relevance) || relevance < 0 || relevance > 100) throw new HttpFailure(400, 'demo_invalid_input'); }
      if (Wire.has(body, 'status')) { status = Wire.text(body, 'status'); if (!['active', 'paused', 'done', 'ignored'].includes(status)) throw new HttpFailure(400, 'demo_invalid_input'); }
      if (Wire.has(body, 'project_kind')) { projectKind = Wire.text(body, 'project_kind'); if (!['short', 'long'].includes(projectKind)) throw new HttpFailure(400, 'demo_invalid_input'); }
      if (Wire.has(body, 'tags')) { meta['tags'] = tags; updated['tags'] = tags.slice(); }
      if (importance) meta['importance'] = importance;
      if (relevance >= 0) meta['relevance'] = relevance;
      if (status) meta['status'] = status;
      if (projectKind) meta['kind'] = projectKind;
      updated['meta'] = meta; updated['updated_at'] = '2026-11-11T09:30:00Z';
      this.knowledgeData = this.knowledgeData.map((item: DemoRecord) => item === entry ? updated : item);
      return demoRecord([], []);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private bodyStringArray(body: DemoRecord, key: string): string[] {
    const value = body[key];
    if (value === undefined) return [];
    if (!Array.isArray(value) || (value as Object[]).length > 64 || (value as Object[]).some((item: Object) => typeof item !== 'string' || utf8Bytes(item as string) > 120))
      throw new HttpFailure(400, 'demo_invalid_input');
    return (value as Object[]).map((item: Object) => (item as string).trim().replace(/^#/, '')).filter((item: string, index: number, all: string[]) => !!item && all.indexOf(item) === index);
  }

  private inboxRoute(parts: string[], method: string, body: DemoRecord, operationId: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') return demoRecord(['items'], [cloneRecords(this.inboxData)]);
    if (parts.length < 2) throw new HttpFailure(404, 'demo_inbox_not_found');
    const item = this.inboxData.find((value: DemoRecord) => Wire.text(value, 'id') === parts[1]);
    if (!item) throw new HttpFailure(404, 'demo_inbox_not_found');
    if (parts.length === 2 && method === 'GET') return demoRecord(['item'], [cloneRecord(item)]);
    const action = parts[2] || '';
    if (action === 'done' && method === 'POST') {
      if (typeof body['done'] !== 'boolean') throw new HttpFailure(400, 'demo_invalid_input');
      if (body['done']) { item['done'] = true; item['done_at'] = '2026-11-11T09:30:00Z'; item['state'] = 'done'; item['unread'] = false; }
      else {
        const updated = omitFields(item, ['done_at']); updated['done'] = false; updated['state'] = 'idle';
        this.inboxData = this.inboxData.map((current: DemoRecord) => current === item ? updated : current);
        return cloneRecord(updated);
      }
      return cloneRecord(item);
    }
    if (action === 'reply' && method === 'POST') return this.replyInbox(item, body, operationId);
    if (action === 'handoff' && method === 'POST') return this.handoffInbox(item, body, operationId);
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private replyInbox(item: DemoRecord, body: DemoRecord, operationId: string): DemoRecord {
    const text = nonEmpty(Wire.text(body, 'text'), MAX_TEXT_BYTES), clientId = nonEmpty(Wire.text(body, 'client_id', operationId), 200);
    if (operationId && operationId !== clientId) throw new HttpFailure(400, 'demo_invalid_input');
    const cached = this.operationResults.get(clientId); if (cached) return cloneRecord(cached);
    if (Wire.text(item, 'source') === 'mail') {
      const target = Wire.object(item, 'reply_target');
      if (Wire.number(body, 'expected_uid', -1) !== Wire.number(target, 'uid') || Wire.text(body, 'expected_version') !== Wire.text(target, 'version'))
        throw new HttpFailure(409, 'mail_reply_target_changed');
    }
    this.appendInboxReply(item, text, clientId);
    const version = Wire.text(item, 'version'); item['version'] = version + '-reply';
    const target = Wire.object(item, 'reply_target');
    if (Wire.has(target, 'version')) target['version'] = Wire.text(target, 'version') + '-reply';
    item['reply_target'] = target;
    const updated = cloneRecord(item); this.rememberOperation(clientId, updated); return updated;
  }

  private appendInboxReply(item: DemoRecord, text: string, clientId: string, threadRoot: string = '', replyTo: string = ''): void {
    const source = Wire.text(item, 'source');
    if (source === 'lark') {
      const message = demoRecord(['id', 'position', 'sender', 'sender_name', 'text', 'mine', 'state', 'can_reply', 'thread_kind', 'client_id', 'reactions'],
        ['demo-lark-message-' + (++this.messageSequence), String(1000 + this.messageSequence),
          demoRecord(['id', 'name'], ['demo-you', 'Demo user']), 'Demo user', text, true, 'read', true, 'message', clientId, []]);
      if (replyTo) message['reply_to'] = replyTo;
      if (threadRoot) message['thread_root'] = threadRoot;
      this.larkMessages.push(message);
      this.larkMessages = this.larkMessages.slice(-MAX_TRANSCRIPT_ITEMS);
      this.rememberOperation('messaging:' + clientId,
        demoRecord(['text', 'reply_to', 'thread_root'], [text, replyTo, threadRoot]));
    }
    item['summary'] = 'Demo reply: ' + text.slice(0, 160); item['last_reply'] = demoRecord(['client_id', 'text'], [clientId, text]);
    item['unread'] = false; if (Wire.text(item, 'state') === 'unread') item['state'] = 'idle';
  }

  private handoffInbox(item: DemoRecord, body: DemoRecord, operationId: string): DemoRecord {
    const text = nonEmpty(Wire.text(body, 'text'), MAX_TEXT_BYTES), clientId = nonEmpty(Wire.text(body, 'client_id', operationId), 200);
    if (operationId && operationId !== clientId) throw new HttpFailure(400, 'demo_invalid_input');
    const cached = this.operationResults.get(clientId); if (cached) return cloneRecord(cached);
    this.ensureRoom(this.conversationsData);
    const sessionId = 'demo-inbox-followup-' + (++this.counter), title = 'Follow up: ' + Wire.text(item, 'title', 'Inbox item');
    const capabilities = demoRecord(['read', 'send', 'approve', 'interrupt', 'terminal', 'close', 'rename'],
      [true, true, true, true, true, true, true]);
    const session = demoRecord(['id', 'session_id', 'machine_id', 'manager', 'title', 'main', 'kind', 'status', 'state', 'source_kind',
      'cwd', 'repo_root', 'capabilities'],
      [sessionId, sessionId, DEMO_MACHINE, DEMO_MANAGER, title, false, 'recent', 'working', 'working', 'live',
        '/demo/ReleaseDesk', '/demo/ReleaseDesk', capabilities]);
    this.conversationsData.push(session); this.transcripts.set(this.transcriptKey(DEMO_MACHINE, sessionId), [
      this.item('user', 'Follow up on “' + Wire.text(item, 'title') + '”: ' + text, clientId),
      this.item('assistant', 'This follow-up conversation is local to the demo.')
    ]);
    const handoffs = Wire.rows(item, 'handoffs').concat([demoRecord(['machine_id', 'session_id', 'created_at'],
      [DEMO_MACHINE, sessionId, '2026-11-11T09:30:00Z'])]);
    item['handoffs'] = handoffs;
    const result = demoRecord(['item', 'session'], [cloneRecord(item), demoRecord(['machine_id', 'session_id'], [DEMO_MACHINE, sessionId])]);
    this.rememberOperation(clientId, result); return cloneRecord(result);
  }

  private messaging(parts: string[], query: Record<string, string>, method: string, body: DemoRecord, operationId: string): DemoRecord {
    if (parts.length === 1 && method === 'GET') {
      const sources: DemoRecord[] = [demoRecord(['id', 'name', 'capabilities'], ['lark', 'Feishu demo', ['accounts', 'threads', 'topics']])];
      return demoRecord(['sources'], [sources]);
    }
    if (parts.length < 2 || parts[1] !== 'lark') throw new HttpFailure(404, 'demo_unsupported');
    const account = query['account'] || '';
    if (account && account !== DEMO_LARK_ACCOUNT) throw new HttpFailure(409, 'account_changed');
    const rest = parts.slice(2);
    if (rest.length === 1 && rest[0] === 'accounts' && method === 'GET') {
      const accounts: DemoRecord[] = [demoRecord(['id', 'name', 'email'], [DEMO_LARK_ACCOUNT, 'Demo Team', 'team@example.test'])];
      return demoRecord(['accounts', 'default'], [accounts, DEMO_LARK_ACCOUNT]);
    }
    if (rest.length === 1 && rest[0] === 'status' && method === 'GET')
      return demoRecord(['account', 'name', 'configured', 'connected', 'signed_in'], [DEMO_LARK_ACCOUNT, 'Demo Team', true, true, true]);
    if (rest.length === 1 && rest[0] === 'me' && method === 'GET')
      return demoRecord(['account', 'user_id', 'name'], [DEMO_LARK_ACCOUNT, 'demo-user', 'Demo user']);
    if (rest.length === 3 && rest[0] === 'conversations' && rest[1] === DEMO_LARK_CONVERSATION && rest[2] === 'messages') {
      if (method === 'GET') return demoRecord(['account', 'messages', 'topic', 'has_older', 'older_cursor'],
        [DEMO_LARK_ACCOUNT, cloneRecords(this.larkMessages.filter((item: DemoRecord) => !Wire.text(item, 'thread_root'))), false, false, '']);
      if (method === 'POST') {
        const text = nonEmpty(Wire.text(body, 'text'), MAX_TEXT_BYTES), clientId = nonEmpty(Wire.text(body, 'client_id', operationId), 200);
        const replyTo = Wire.text(body, 'reply_to');
        if (Wire.text(body, 'expect_user') !== 'demo-user') throw new HttpFailure(403, 'unknown_sender');
        if (operationId && clientId !== operationId) throw new HttpFailure(400, 'demo_invalid_input');
        if (Wire.has(body, 'reply_to') && !replyTo || Wire.has(body, 'thread') && typeof body['thread'] !== 'boolean')
          throw new HttpFailure(400, 'demo_invalid_input');
        const thread = Wire.bool(body, 'thread');
        if (thread && !replyTo) throw new HttpFailure(400, 'demo_invalid_input');
        let threadRoot = '';
        if (replyTo) {
          const parent = this.larkMessages.find((item: DemoRecord) => Wire.text(item, 'id') === replyTo);
          if (!parent) throw new HttpFailure(409, 'reply_target_changed');
          if (!Wire.bool(parent, 'can_reply', true)) throw new HttpFailure(409, 'reply_target_changed');
          if (thread) threadRoot = Wire.text(parent, 'thread_root') || Wire.text(parent, 'id');
        }
        const cached = this.operationResults.get('messaging:' + clientId);
        if (cached) {
          if (Wire.text(cached, 'text') !== text || Wire.text(cached, 'reply_to') !== replyTo || Wire.text(cached, 'thread_root') !== threadRoot)
            throw new HttpFailure(409, 'demo_client_id_conflict');
          return demoRecord(['state'], ['done']);
        }
        const previous = this.larkMessages.find((item: DemoRecord) => Wire.text(item, 'client_id') === clientId);
        if (previous) {
          if (Wire.text(previous, 'text') !== text || Wire.text(previous, 'reply_to') !== replyTo || Wire.text(previous, 'thread_root') !== threadRoot)
            throw new HttpFailure(409, 'demo_client_id_conflict');
          this.rememberOperation('messaging:' + clientId,
            demoRecord(['text', 'reply_to', 'thread_root'], [text, replyTo, threadRoot]));
          return demoRecord(['state'], ['done']);
        }
        this.appendInboxReply(this.inboxData[0], text, clientId, threadRoot, replyTo);
        return demoRecord(['state'], ['done']);
      }
    }
    if (rest.length === 3 && rest[0] === 'conversations' && rest[1] === DEMO_LARK_CONVERSATION && rest[2] === 'read' && method === 'POST') {
      const ids = this.bodyStringArray(body, 'message_ids'), position = Wire.text(body, 'position');
      if (!position || ids.length > 100) throw new HttpFailure(400, 'demo_invalid_input');
      this.larkMessages = this.larkMessages.map((message: DemoRecord): DemoRecord => {
        if (!ids.includes(Wire.text(message, 'id'))) return message;
        const updated = cloneRecord(message); updated['state'] = 'read'; return updated;
      });
      this.inboxData[0]['unread'] = false; if (Wire.text(this.inboxData[0], 'state') === 'unread') this.inboxData[0]['state'] = 'idle';
      return demoRecord(['account', 'read'], [DEMO_LARK_ACCOUNT, true]);
    }
    if (rest.length === 2 && rest[0] === 'messages' && method === 'GET') {
      const message = this.larkMessages.find((item: DemoRecord) => Wire.text(item, 'id') === rest[1]);
      if (!message) throw new HttpFailure(404, 'demo_message_not_found');
      return demoRecord(['account', 'message'], [DEMO_LARK_ACCOUNT, cloneRecord(message)]);
    }
    if (rest.length === 3 && rest[0] === 'messages' && rest[2] === 'thread' && method === 'GET') return this.larkThread(rest[1]);
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private larkThread(rootId: string): DemoRecord {
    const root = this.larkMessages.find((item: DemoRecord) => Wire.text(item, 'id') === rootId && !Wire.text(item, 'thread_root'));
    if (!root) throw new HttpFailure(404, 'demo_message_not_found');
    const messages = [root].concat(this.larkMessages.filter((item: DemoRecord) => Wire.text(item, 'thread_root') === rootId));
    return demoRecord(['account', 'root', 'messages', 'topic', 'has_older', 'older_cursor'],
      [DEMO_LARK_ACCOUNT, demoRecord(['id'], [rootId]), cloneRecords(messages), false, false, '']);
  }

  private legacyLark(parts: string[], query: Record<string, string>, method: string, body: DemoRecord): DemoRecord {
    if (query['ol_account'] && query['ol_account'] !== DEMO_LARK_ACCOUNT) throw new HttpFailure(409, 'account_changed');
    if (parts.length === 4 && parts[1] === 'conversations' && parts[3] === 'messages' && method === 'GET')
      return demoRecord(['messages', 'topic', 'has_older'],
        [cloneRecords(this.larkMessages.filter((item: DemoRecord) => !Wire.text(item, 'thread_root'))), false, false]);
    if (parts.length === 4 && parts[1] === 'messages' && parts[3] === 'thread' && method === 'GET') return this.larkThread(parts[2]);
    if (parts.length === 4 && parts[1] === 'conversations' && parts[3] === 'read' && method === 'POST') {
      const ids = this.bodyStringArray(body, 'message_ids');
      this.larkMessages = this.larkMessages.map((message: DemoRecord): DemoRecord => {
        if (!ids.includes(Wire.text(message, 'id'))) return message;
        const updated = cloneRecord(message); updated['state'] = 'read'; return updated;
      });
      this.inboxData[0]['unread'] = false; this.inboxData[0]['state'] = 'idle'; return demoRecord(['read'], [true]);
    }
    throw new HttpFailure(404, 'demo_unsupported');
  }

  private mail(parts: string[], query: Record<string, string>, method: string, body: DemoRecord): DemoRecord {
    if (parts.length === 2 && parts[1] === 'accounts' && method === 'GET') {
      const accounts: DemoRecord[] = [demoRecord(['id', 'name', 'email'], [DEMO_MAIL_ACCOUNT, 'Demo Mailbox', 'inbox@example.test'])];
      return demoRecord(['accounts'], [accounts]);
    }
    if (parts.length < 3 || parts[1] !== 'messages' || parts[2] !== DEMO_MAIL_ACCOUNT) throw new HttpFailure(404, 'demo_unsupported');
    const uid = Number(parts[3]), message = this.mailData.find((item: DemoRecord) => Wire.number(item, 'uid') === uid);
    if (!message || query['mailbox'] && query['mailbox'] !== Wire.text(message, 'mailbox')) throw new HttpFailure(404, 'demo_mail_not_found');
    if (parts.length === 4 && method === 'GET') return demoRecord(['message'], [cloneRecord(message)]);
    if (parts.length === 5 && parts[4] === 'flags' && method === 'POST') {
      if (Wire.text(body, 'mailbox', 'INBOX') !== Wire.text(message, 'mailbox') || typeof body['seen'] !== 'boolean') throw new HttpFailure(400, 'demo_invalid_input');
      message['seen'] = body['seen'];
      if (body['seen']) { const item = this.inboxData.find((value: DemoRecord) => Wire.text(value, 'source') === 'mail'); if (item) { item['state'] = 'idle'; item['unread'] = false; } }
      return demoRecord(['message'], [cloneRecord(message)]);
    }
    throw new HttpFailure(501, 'demo_unsupported');
  }
}
