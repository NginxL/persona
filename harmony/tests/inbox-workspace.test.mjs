import test from 'node:test';
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

globalThis.__inboxTestUUID = randomUUID;

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.ArkTS') return { url: 'persona-inbox:arkts', shortCircuit: true };
    if (specifier === '../platform/HubTransport' || specifier.endsWith('/platform/HubTransport'))
      return { url: 'persona-inbox:transport', shortCircuit: true };
    if (specifier === '../platform/LocalStore' || specifier.endsWith('/platform/LocalStore'))
      return { url: 'persona-inbox:local', shortCircuit: true };
    if (specifier === './ClientModel' && context.parentURL?.endsWith('/InboxWorkspace.ets'))
      return { url: 'persona-inbox:client', shortCircuit: true };
    if (context.parentURL?.endsWith('/MutationStore.ets') && specifier === '../core/StorageOwnership')
      return { url: new URL('../core/StorageOwnership.ts', context.parentURL).href, shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-inbox:arkts') return { format: 'module', source: `
      export const util = {
        generateRandomUUID: () => globalThis.__inboxTestUUID(),
        TextDecoder: { create(_encoding, options) { return { decodeToString(bytes) { return new TextDecoder('utf-8', options).decode(bytes); } }; } }
      };`, shortCircuit: true };
    if (url === 'persona-inbox:transport') return { format: 'module', source: `
      export class HttpFailure extends Error {
        constructor(status, code, resultUnknown = false, resultPending = false) {
          super(code); this.status = status; this.code = code; this.resultUnknown = resultUnknown; this.resultPending = resultPending;
        }
      }
      export class EndpointConfig { constructor(baseUrl = '', hubId = 'test-hub', token = '', pin = '', managerId = '') { Object.assign(this, {baseUrl, hubId, token, pin, managerId}); } }
      export class HubTransport {
        constructor(config) { this.config = config; }
        async request(path, method = 'GET', body = '', op = '') {
          const result = await globalThis.__inboxRequest({path, method, body: body ? JSON.parse(body) : {}, op, manager: this.config.managerId, transport: this});
          return JSON.stringify(result ?? {});
        }
        async requestBytes(path, method = 'GET', data = new ArrayBuffer(0), mime = '', op = '') {
          return globalThis.__inboxRequestBytes({path, method, data, mime, op, manager: this.config.managerId, transport: this});
        }
        close() {}
      }`, shortCircuit: true };
    if (url === 'persona-inbox:local') return { format: 'module', source: 'export class LocalStore {}', shortCircuit: true };
    if (url === 'persona-inbox:client') return { format: 'module', source: 'export class ClientModel {}', shortCircuit: true };
    if (url.endsWith('.ets') || url.endsWith('.ts')) {
      let source = readFileSync(new URL(url), 'utf8');
      if (url.endsWith('.ets')) source = source.replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { InboxAttachmentDraft, InboxWorkspace } = await import('../entry/src/main/ets/models/InboxWorkspace.ets');
const { HubRepository } = await import('../entry/src/main/ets/models/HubRepository.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');
const { HttpFailure, EndpointConfig } = await import('persona-inbox:transport');

class MemoryStore {
  values = new Map();
  async get(key) { return this.values.get(key) || ''; }
  async put(key, value) { this.values.set(key, value); }
  async remove(key) { this.values.delete(key); }
}

function item(id, fields = {}) {
  return {
    id, source: 'mail', kind: 'mail', account: 'mail-a', account_name: 'mail-a@example.test',
    title: 'Subject ' + id, sender: 'sender@example.test', preview: 'A preview', summary: 'A summary', manager: 'work',
    ...fields
  };
}

function mailDetail(id = 'mail-1', uid = 31, version = 'target-v1', fields = {}) {
  return {
    ...item(id), source: 'mail', mail: { account_id: 'mail-a', mailbox: 'INBOX', uids: [uid], message_id: '<m@example.test>',
      from: { name: 'Sender', email: 'sender@example.test' }, direct: true, thread: 'subject' },
    reply_target: { uid, version }, ...fields
  };
}

function conversationDetail(id = 'chat-1', options = {}) {
  const account = options.account || 'chat-a';
  return {
    ...item(id, { source: 'chat', account, account_name: 'Chat account', kind: 'message' }),
    source: 'chat', conversation: { source: 'chat', id: 'conversation-1', account, start_position: '10', topic: false },
    ...options.fields
  };
}

function rowFor(data) {
  const row = ResourceRow.from(data, 'inbox');
  row.manager = data.manager || 'work';
  return row;
}

function todoSourceRow({ channel = 'feishu', chatId = 'oc_source', messageId = 'om_source', anchor = '', account = '', title = 'Source request', manager = 'work' } = {}) {
  const row = new ResourceRow(chatId, title, 'todo_source');
  row.manager = manager; row.session = messageId; row.detail = channel;
  const data = { id: chatId, manager, channel, chat_id: chatId, message_id: messageId, anchor, title };
  if (account) data.account = account;
  row.raw = JSON.stringify(data);
  return row;
}

function todoThreadRow({ channel = 'feishu', chatId = 'oc_thread', threadId = 'omt_thread', rootMessageId = '', account = '', title = 'Bound topic', manager = 'work' } = {}) {
  const row = new ResourceRow(chatId, title, 'todo_thread');
  row.manager = manager; row.session = threadId; row.detail = channel;
  const data = { channel, chat_id: chatId, thread_id: threadId, title };
  if (account) data.account = account;
  if (rootMessageId) data.root_message_id = rootMessageId;
  row.raw = JSON.stringify(data);
  return row;
}

function message(id, position, text) {
  return { id, position, sender: { name: 'Alex' }, mine: false, text, parts: [{ kind: 'text', text }] };
}

function testStore() { return new MemoryStore(); }
function pathOnly(path) { return path.split('?')[0]; }
function queryValue(path, key) { return new URLSearchParams(path.split('?')[1] || '').get(key) || ''; }

function messagingReferenceHandler({ source = 'lark', account = 'acct-b', capabilities = ['accounts', 'threads', 'topics', 'reactions', 'attachments', 'attachment_replies'], positions = {}, messageIds = {}, messageAccounts = {} } = {}) {
  return async (call) => {
    const path = pathOnly(call.path);
    const activeAccount = typeof account === 'function' ? account() : account;
    if (path === '/api/v1/messaging') return { sources: [{ id: source, name: 'Lark', capabilities }] };
    if (path === '/api/v1/messaging/' + source + '/accounts') return { account: activeAccount, default: activeAccount, accounts: [{ id: activeAccount, name: 'Work chat' }] };
    if (path === '/api/v1/messaging/' + source + '/status') return { account: activeAccount, configured: true, connected: true, signed_in: true, name: 'Work chat' };
    if (path === '/api/v1/messaging/' + source + '/me') return { account: activeAccount, user_id: 'user-a', name: 'Me' };
    if (path === '/api/v1/messaging/' + source + '/reactions/quick') return { keys: ['👍'] };
    if (/^\/api\/v1\/messaging\/[^/]+\/messages\/[^/]+\/thread$/.test(path)) {
      const id = decodeURIComponent(path.split('/')[6]);
      return { account: activeAccount, root: { ...message(id, '20', 'Topic root'), can_reply: true }, messages: [message('reply-1', '21', 'Topic reply')], has_older: false };
    }
    if (/^\/api\/v1\/messaging\/[^/]+\/messages\/[^/]+$/.test(path)) {
      const id = decodeURIComponent(path.split('/')[6]);
      return { account: messageAccounts[id] || activeAccount, message: { ...message(messageIds[id] || id, positions[id] || '55', 'Source message'), can_reply: true } };
    }
    if (/^\/api\/v1\/messaging\/[^/]+\/conversations\/[^/]+\/messages$/.test(path) && call.method === 'GET')
      return { account: activeAccount, messages: [message('m-source', queryValue(call.path, 'around') || '55', 'Source message')], has_older: false };
    if (/^\/api\/v1\/messaging\/[^/]+\/conversations\/[^/]+\/messages$/.test(path) && call.method === 'POST') return { message_id: 'sent-1' };
    return undefined;
  };
}

function makeHarness({ rows = [], details = {}, handle = async () => undefined, bytes = async () => ({ data: new ArrayBuffer(0), contentType: 'application/octet-stream' }), manager = 'work', features = ['messaging'] } = {}) {
  const state = { manager, generation: 4, profileId: 'profile-1', repo: undefined, views: new Map(), calls: [], byteCalls: [] };
  const local = testStore();
  const repo = new HubRepository(new EndpointConfig('https://hub.example.test', 'hub-1', '', '', manager), local);
  state.repo = repo;
  globalThis.__inboxRequest = async (call) => {
    state.calls.push(call);
    const custom = await handle(call, state);
    if (custom !== undefined) return custom;
    if (pathOnly(call.path) === '/api/v1/inbox' && queryValue(call.path, 'sources') === 'all') return { items: rows };
    if (pathOnly(call.path) === '/api/v1/messaging') return { sources: [{ id: 'chat', name: 'Chat', capabilities: ['accounts', 'threads', 'topics', 'reactions', 'attachments', 'attachment_replies'] }] };
    if (call.path.startsWith('/api/v1/inbox/') && call.method === 'GET') {
      const id = decodeURIComponent(call.path.split('/')[4].split('?')[0]);
      return details[id] || rows.find((entry) => entry.id === id) || item(id);
    }
    if (pathOnly(call.path) === '/api/v1/mail/accounts') return { accounts: [{ id: 'mail-a', email: 'mail-a@example.test' }] };
    if (/^\/api\/v1\/mail\/messages\//.test(call.path) && call.method === 'GET') {
      const uid = Number(call.path.split('/')[6].split('?')[0]);
      return { message: { account_id: 'mail-a', mailbox: 'INBOX', uid, message_id: '<m@example.test>', subject: 'Subject',
        from: { name: 'Sender', email: 'sender@example.test' }, to: [{ email: 'me@example.test' }], date: '2026-11-11T10:00:00Z',
        text: 'Original message', seen: false, attachments: [] } };
    }
    if (pathOnly(call.path) === '/api/v1/messaging/chat/accounts') return { account: 'chat-a', default: 'chat-a', accounts: [{ id: 'chat-a', name: 'Work chat' }] };
    if (call.path.startsWith('/api/v1/messaging/chat/status')) return { account: 'chat-a', configured: true, connected: true, signed_in: true, name: 'Work chat' };
    if (call.path.startsWith('/api/v1/messaging/chat/me')) return { account: 'chat-a', user_id: 'user-a', name: 'Me' };
    if (call.path.startsWith('/api/v1/messaging/chat/reactions/quick')) return { keys: ['👍'] };
    if (call.path.includes('/api/v1/messaging/chat/conversations/conversation-1/messages'))
      return { account: 'chat-a', messages: [message('m-1', '10', 'Hello'), message('m-2', '11', 'World')], has_older: false };
    if (pathOnly(call.path) === '/api/v1/mail/send') return { state: 'sent' };
    if (call.path.includes('/reply')) return details[call.path.split('/')[4]] || item('mail-1');
    if (pathOnly(call.path).endsWith('/done')) {
      const id = decodeURIComponent(call.path.split('/')[4]);
      const current = details[id] || rows.find((entry) => entry.id === id) || item(id);
      return { ...current, id, manager: queryValue(call.path, 'manager') || current.manager || manager,
        done_at: call.body.done ? '2026-11-11T10:00:00Z' : null };
    }
    if (pathOnly(call.path).endsWith('/handoff') && call.method === 'POST') {
      const id = decodeURIComponent(call.path.split('/')[4]);
      const current = details[id] || rows.find((entry) => entry.id === id) || item(id);
      const handoffs = Array.isArray(current.handoffs) ? current.handoffs : [];
      return { item: { ...current, handoffs: handoffs.concat([{ machine_id: 'server', session_id: 'main-session', at: '2026-11-11T10:00:00Z' }]) },
        session: { machine_id: 'server', session_id: 'main-session' } };
    }
    if (call.method === 'POST') return { message_id: 'sent-message' };
    return {};
  };
  globalThis.__inboxRequestBytes = async (call) => { state.byteCalls.push(call); return bytes(call, state); };
  const client = {
    connected: true, demo: false, features, profile: { id: state.profileId, manager },
    scope() { return 'hub:' + state.manager; },
    connectionGeneration() { return state.generation; },
    repo() { if (!this.connected) throw new Error('offline'); return state.repo; },
    async loadView(key) { return state.views.get(this.scope() + ':' + key) || ''; },
    async saveView(key, value) { state.views.set(this.scope() + ':' + key, value); }
  };
  return { state, client, repo, local, calls: state.calls, byteCalls: state.byteCalls };
}

async function waitFor(predicate, limit = 1000) {
  const until = Date.now() + limit;
  while (!predicate()) {
    if (Date.now() >= until) throw new Error('fixture did not reach the expected request');
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

test('todo source references open the native generic conversation at the resolved message and scope drafts by source', async () => {
  const f = makeHarness({ manager: 'personal', handle: messagingReferenceHandler() });
  const row = todoSourceRow({ manager: 'work' }), workspace = new InboxWorkspace();
  await workspace.open(f.client, row);
  assert.equal(row.data().account, undefined, 'the Todo source contract does not carry an account');
  assert.equal(workspace.selected.kind, 'todo_source');
  assert.equal(workspace.selected.manager, 'work');
  assert.equal(workspace.selected.session, 'om_source');
  assert.equal(workspace.selected.detail, 'feishu');
  assert.equal(workspace.detail.conversation.source, 'lark');
  assert.equal(workspace.detail.conversation.id, 'oc_source');
  assert.equal(workspace.detail.account, 'acct-b');
  assert.equal(workspace.detail.conversation.account, 'acct-b');
  assert.equal(workspace.detail.conversation.start_position, '55');
  const sourceMessage = f.calls.find((call) => pathOnly(call.path) === '/api/v1/messaging/lark/messages/om_source');
  assert.ok(sourceMessage);
  assert.equal(queryValue(sourceMessage.path, 'manager'), 'work');
  assert.equal(queryValue(sourceMessage.path, 'account'), 'acct-b');
  assert.equal(sourceMessage.manager, 'personal', 'the selected manager is scoped on the route while transport owner stays captured');
  const page = f.calls.find((call) => pathOnly(call.path) === '/api/v1/messaging/lark/conversations/oc_source/messages' && call.method === 'GET');
  assert.ok(page);
  assert.equal(queryValue(page.path, 'around'), '55');
  assert.equal(queryValue(page.path, 'account'), 'acct-b');
  assert.equal(queryValue(page.path, 'manager'), 'work');
  assert.equal(f.calls.some((call) => pathOnly(call.path).startsWith('/api/v1/inbox/')), false);

  assert.equal(await workspace.processInbox(), false);
  assert.equal(await workspace.replyInbox('No synthetic reply', 'confirmation'), false);
  assert.equal(await workspace.handoff('No synthetic handoff'), undefined);
  assert.equal(f.calls.some((call) => pathOnly(call.path).startsWith('/api/v1/inbox/')), false);

  workspace.setReplyDraft('Source scoped draft'); await workspace.draftWrite; await workspace.close();
  const restored = new InboxWorkspace(); await restored.open(f.client, row);
  assert.equal(restored.replyDraft, 'Source scoped draft');
  const otherSource = todoSourceRow({ messageId: 'om_other' });
  await restored.openRow(otherSource);
  assert.equal(restored.replyDraft, '', 'another message in the same chat has a separate draft');
  await restored.close();
});

test('todo source draft keys use the resolved account and only exact source or anchor identities', async () => {
  let activeAccount = 'acct-b';
  const f = makeHarness({ handle: messagingReferenceHandler({ account: () => activeAccount,
    positions: { om_source: '66', om_anchor: '77' }, messageIds: { om_source: 'om_other' } }) });
  const row = todoSourceRow({ anchor: 'om_anchor' }), workspace = new InboxWorkspace();
  await workspace.open(f.client, row);
  assert.equal(workspace.detail.account, 'acct-b');
  assert.equal(workspace.detail.conversation.account, 'acct-b');
  const originRead = f.calls.find((call) => pathOnly(call.path) === '/api/v1/messaging/lark/messages/om_source');
  const anchorRead = f.calls.find((call) => pathOnly(call.path) === '/api/v1/messaging/lark/messages/om_anchor');
  assert.ok(originRead);
  assert.ok(anchorRead);
  const page = f.calls.find((call) => pathOnly(call.path) === '/api/v1/messaging/lark/conversations/oc_source/messages' && call.method === 'GET');
  assert.equal(queryValue(page.path, 'around'), '77', 'a mismatched source message id cannot choose the position');
  workspace.setReplyDraft('Account scoped draft'); await workspace.draftWrite; await workspace.close();

  activeAccount = 'acct-c';
  await workspace.open(f.client, row);
  assert.equal(workspace.detail.account, 'acct-c');
  assert.equal(workspace.replyDraft, '', 'a draft for the old default account is not restored');
  await workspace.close();

  activeAccount = 'acct-b';
  await workspace.open(f.client, row);
  assert.equal(workspace.replyDraft, 'Account scoped draft');
  await workspace.close();
});

test('a scoped todo source draft restores the exact native thread and reply target', async () => {
  const f = makeHarness({ handle: messagingReferenceHandler() });
  const row = todoSourceRow(), workspace = new InboxWorkspace();
  await workspace.open(f.client, row);
  const sourceRoot = workspace.feedMessages.find((message) => message.id === 'm-source');
  assert.ok(sourceRoot);
  await workspace.openThread(sourceRoot);
  workspace.setThreadTarget('reply-1', true);
  workspace.setReplyDraft('Thread reply draft'); await workspace.draftWrite; await workspace.close();

  const restored = new InboxWorkspace(); await restored.open(f.client, row);
  assert.equal(restored.replyDraft, 'Thread reply draft');
  assert.equal(restored.threadRoot, 'm-source');
  assert.equal(restored.threadReplyTo, 'reply-1');
  assert.equal(restored.threadReply, true);
  assert.deepEqual(restored.feedMessages.map((message) => message.id), ['reply-1']);
  await restored.close();
});

test('todo source refuses a source message read when its confirmed account differs', async () => {
  const f = makeHarness({ handle: messagingReferenceHandler({ messageAccounts: { om_source: 'acct-other' } }) });
  const workspace = new InboxWorkspace();
  await workspace.open(f.client, todoSourceRow());
  assert.equal(workspace.error, 'account_changed');
  assert.equal(workspace.hasConversation(), false);
  assert.equal(workspace.canSendGeneric(), false);
  assert.equal(f.calls.some((call) => pathOnly(call.path) === '/api/v1/messaging/lark/conversations/oc_source/messages'), false);
  await workspace.close();
});

test('a detail route mounted offline opens its pending row after the client becomes connected', async () => {
  const f = makeHarness({ handle: messagingReferenceHandler() });
  f.client.connected = false;
  const row = todoSourceRow(), workspace = new InboxWorkspace();
  await workspace.open(f.client, row);
  assert.equal(workspace.error, 'offline');
  assert.equal(workspace.selectedKey, '');
  assert.equal(f.calls.length, 0);

  workspace.onDisconnected();
  f.state.generation++;
  f.state.repo = new HubRepository(new EndpointConfig('https://hub.example.test', 'hub-1', '', '', f.state.manager), f.local);
  f.client.connected = true;
  await workspace.reconnect(row);
  assert.equal(workspace.selected.kind, 'todo_source');
  assert.equal(workspace.selected.manager, 'work');
  assert.equal(workspace.selected.session, 'om_source');
  assert.equal(workspace.detail.account, 'acct-b');
  assert.ok(f.calls.some((call) => pathOnly(call.path) === '/api/v1/messaging/lark/conversations/oc_source/messages'));
  assert.equal(f.calls.some((call) => pathOnly(call.path).startsWith('/api/v1/inbox/')), false);
  await workspace.close();
});

test('a replaced connected repository reloads the selected reference without losing its draft or filters', async () => {
  const f = makeHarness({ handle: messagingReferenceHandler() });
  const row = todoSourceRow(), workspace = new InboxWorkspace();
  await workspace.open(f.client, row);
  const sourceRoot = workspace.feedMessages.find((message) => message.id === 'm-source');
  assert.ok(sourceRoot);
  await workspace.openThread(sourceRoot);
  workspace.setThreadTarget('reply-1', true);
  workspace.setReplyDraft('Reconnect draft'); await workspace.draftWrite;
  workspace.sourceFilter = 'lark'; workspace.accountFilter = 'acct-b'; workspace.statusFilter = 'done'; workspace.search = 'source';
  workspace.writeState = 'unknown';
  const oldRepo = f.state.repo, selectedKey = workspace.selected.key(), before = f.calls.length;
  f.client.connected = false; workspace.onDisconnected();
  f.state.generation++;
  f.state.repo = new HubRepository(new EndpointConfig('https://hub.example.test', 'hub-1', '', '', f.state.manager), f.local);
  f.client.connected = true;

  await workspace.reconnect(row);
  assert.notEqual(f.client.repo(), oldRepo);
  assert.equal(workspace.selected.key(), selectedKey);
  assert.equal(workspace.replyDraft, 'Reconnect draft');
  assert.equal(workspace.threadRoot, 'm-source');
  assert.equal(workspace.threadReplyTo, 'reply-1');
  assert.equal(workspace.threadReply, true);
  assert.deepEqual(workspace.feedMessages.map((message) => message.id), ['reply-1']);
  assert.equal(workspace.sourceFilter, 'lark');
  assert.equal(workspace.accountFilter, 'acct-b');
  assert.equal(workspace.statusFilter, 'done');
  assert.equal(workspace.search, 'source');
  assert.equal(workspace.writeState, 'unknown', 'reconnection does not retry an unresolved send');
  assert.ok(f.calls.slice(before).some((call) => pathOnly(call.path) === '/api/v1/messaging/lark/messages/om_source'));
  assert.equal(f.calls.slice(before).some((call) => call.method === 'POST'), false);
  await workspace.close();
});

test('todo thread references keep the actual thread ID and never guess it is a message ID', async () => {
  const f = makeHarness({ handle: messagingReferenceHandler() });
  const row = todoThreadRow(), workspace = new InboxWorkspace();
  await workspace.open(f.client, row);
  assert.equal(row.data().account, undefined, 'the Todo thread contract does not carry an account');
  assert.equal(workspace.selected.kind, 'todo_thread');
  assert.equal(workspace.selected.detail, 'feishu');
  assert.equal(workspace.selected.session, 'omt_thread');
  assert.equal(workspace.referenceThreadId, 'omt_thread');
  assert.equal(workspace.detail.account, 'acct-b');
  assert.equal(workspace.detail.conversation.account, 'acct-b');
  assert.equal(workspace.hasConversation(), false);
  assert.equal(workspace.canSendGeneric(), false);
  assert.deepEqual(workspace.feedMessages, []);
  assert.equal(f.calls.some((call) => pathOnly(call.path).startsWith('/api/v1/inbox/')), false);
  assert.equal(f.calls.some((call) => pathOnly(call.path).includes('/messages/omt_thread')), false);
  assert.equal(f.calls.some((call) => pathOnly(call.path) === '/api/v1/messaging/lark/conversations/oc_thread/messages'), false);
  const before = f.calls.length;
  assert.equal(await workspace.sendConversation('No guessed thread reply', 'omt_thread', true), false);
  assert.equal(await workspace.processInbox(), false);
  assert.equal(await workspace.replyInbox('No synthetic reply', 'confirmation'), false);
  assert.equal(await workspace.handoff('No synthetic handoff'), undefined);
  assert.equal(f.calls.length, before);
  await workspace.close();
});

test('an explicit root message opens and replies to exactly the bound native thread', async () => {
  const f = makeHarness({ manager: 'personal', handle: messagingReferenceHandler({ positions: { 'om-root': '20' } }) });
  const row = todoThreadRow({ rootMessageId: 'om-root', manager: 'work' }), workspace = new InboxWorkspace();
  await workspace.open(f.client, row);
  assert.equal(workspace.hasConversation(), true);
  assert.equal(workspace.threadRoot, 'om-root');
  assert.equal(workspace.threadReplyTo, 'om-root');
  assert.equal(workspace.threadReply, true);
  assert.equal(workspace.canSendGeneric(), true);
  const threadRead = f.calls.find((call) => pathOnly(call.path) === '/api/v1/messaging/lark/messages/om-root/thread');
  assert.ok(threadRead);
  assert.equal(queryValue(threadRead.path, 'manager'), 'work');
  assert.equal(queryValue(threadRead.path, 'account'), 'acct-b');
  assert.equal(await workspace.sendConversation('Reply in bound thread', 'om-root', true), true);
  const sent = f.calls.find((call) => call.method === 'POST' && pathOnly(call.path) === '/api/v1/messaging/lark/conversations/oc_thread/messages');
  assert.ok(sent);
  assert.equal(sent.body.reply_to, 'om-root');
  assert.equal(sent.body.thread, true);
  assert.equal(queryValue(sent.path, 'manager'), 'work');
  assert.equal(queryValue(sent.path, 'account'), 'acct-b');
  assert.equal(f.calls.some((call) => pathOnly(call.path).startsWith('/api/v1/inbox/')), false);
  await workspace.close();
});

test('todo source fallback is Lark-only and thread capability gates root reads', async () => {
  const legacy = makeHarness({ features: [] });
  const larkRow = todoSourceRow();
  const legacyWorkspace = new InboxWorkspace(); await legacyWorkspace.open(legacy.client, larkRow);
  assert.equal(legacyWorkspace.hasConversation(), true);
  assert.ok(legacy.calls.some((call) => pathOnly(call.path) === '/api/v1/lark/conversations/oc_source/messages'));
  assert.equal(legacy.calls.some((call) => pathOnly(call.path).startsWith('/api/v1/inbox/')), false);
  await legacyWorkspace.close();

  const nonLark = makeHarness({ features: [] });
  const other = new InboxWorkspace(); await other.open(nonLark.client, todoSourceRow({ channel: 'slack' }));
  assert.equal(other.hasConversation(), false);
  assert.equal(other.error, 'messaging_source_unavailable');
  assert.equal(nonLark.calls.some((call) => pathOnly(call.path).startsWith('/api/v1/lark/')), false);
  assert.equal(nonLark.calls.some((call) => pathOnly(call.path).startsWith('/api/v1/inbox/')), false);
  await other.close();

  const restricted = makeHarness({ handle: messagingReferenceHandler({ capabilities: ['accounts', 'reactions'] }) });
  const restrictedWorkspace = new InboxWorkspace();
  await restrictedWorkspace.open(restricted.client, todoThreadRow({ rootMessageId: 'om-root' }));
  assert.equal(restrictedWorkspace.hasConversation(), false);
  assert.equal(restrictedWorkspace.canSendGeneric(), false);
  assert.equal(restricted.calls.some((call) => pathOnly(call.path) === '/api/v1/messaging/lark/messages/om-root'), false);
  assert.equal(restricted.calls.some((call) => pathOnly(call.path) === '/api/v1/messaging/lark/messages/om-root/thread'), false);
  await restrictedWorkspace.close();
});

test('source, account, and open/done filters keep generic connector rows out of mail', async () => {
  const rows = [
    item('mail-1'),
    item('chat-row', { source: 'chat', account: 'chat-a', account_name: 'Work chat', conversation: { source: 'chat', id: 'conversation-1', account: 'chat-a' } }),
    item('github-row', { source: 'github', account: 'github', account_name: 'GitHub', connector: { thread: 'pr-1', label: 'Review requested', can_reply: false } }),
    item('mail-done', { done_at: '2026-11-10T10:00:00Z' })
  ];
  const f = makeHarness({ rows });
  const workspace = new InboxWorkspace(); await workspace.open(f.client);
  workspace.statusFilter = 'open';
  assert.deepEqual(workspace.filteredItems.map((entry) => entry.id), ['mail-1', 'chat-row', 'github-row']);
  workspace.sourceFilter = 'mail';
  assert.deepEqual(workspace.filteredItems.map((entry) => entry.id), ['mail-1']);
  workspace.sourceFilter = 'all'; workspace.accountFilter = 'chat-a';
  assert.deepEqual(workspace.filteredItems.map((entry) => entry.id), ['chat-row']);
  workspace.accountFilter = 'all'; workspace.statusFilter = 'done';
  assert.deepEqual(workspace.filteredItems.map((entry) => entry.id), ['mail-done']);
  assert.equal(workspace.isMail(rowFor(rows[2])), false);
  assert.ok(workspace.sourceOptions.includes('github'));
  await workspace.close();
});

test('a merged row keeps its manager on detail and conversation reads without changing profile selection', async () => {
  const chat = conversationDetail('cross-manager-chat', { fields: { manager: 'work' } });
  const f = makeHarness({ manager: 'personal', rows: [chat], details: { 'cross-manager-chat': chat } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client);
  const list = f.calls.find((call) => pathOnly(call.path) === '/api/v1/inbox');
  assert.ok(list);
  assert.equal(queryValue(list.path, 'manager'), 'personal');
  assert.equal(workspace.items[0].manager, 'work');
  await workspace.openRow(workspace.items[0]);
  assert.equal(f.client.profile.manager, 'personal');
  assert.equal(workspace.selected.manager, 'work');
  const detail = f.calls.find((call) => pathOnly(call.path) === '/api/v1/inbox/cross-manager-chat');
  assert.ok(detail);
  assert.equal(queryValue(detail.path, 'manager'), 'work');
  assert.equal(detail.manager, 'personal', 'the captured repository remains bound to the selected profile');
  const page = f.calls.find((call) => pathOnly(call.path) === '/api/v1/messaging/chat/conversations/conversation-1/messages');
  assert.ok(page);
  assert.equal(queryValue(page.path, 'manager'), 'work');
  assert.equal(f.client.profile.manager, 'personal');
  await workspace.close();
});

test('done, reopen, and handoff writes keep the selected row owner and route the acknowledged conversation', async () => {
  const detail = mailDetail('cross-manager-mail', 51, 'target-v1', { manager: 'work' });
  const f = makeHarness({ manager: 'personal', details: { 'cross-manager-mail': detail } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client, rowFor(detail));
  assert.equal(await workspace.processInbox(), true);
  assert.equal(await workspace.processInbox(), true);
  const target = await workspace.handoff('Review this with me');
  assert.ok(target);
  assert.equal(target.kind, 'conversation');
  assert.equal(target.id, 'main-session');
  assert.equal(target.session, 'main-session');
  assert.equal(target.machine, 'server');
  assert.equal(target.manager, 'work');
  assert.equal(workspace.selected.manager, 'work');
  assert.equal(workspace.detail.handoffs.length, 1);
  const writes = f.calls.filter((call) => call.method === 'POST');
  assert.deepEqual(writes.map((call) => [pathOnly(call.path), call.body.done]), [
    ['/api/v1/inbox/cross-manager-mail/done', true],
    ['/api/v1/inbox/cross-manager-mail/done', false],
    ['/api/v1/inbox/cross-manager-mail/handoff', undefined]
  ]);
  assert.deepEqual(writes.map((call) => queryValue(call.path, 'manager')), ['work', 'work', 'work']);
  assert.equal(f.client.profile.manager, 'personal');
  await workspace.close();
});

test('handoff recovers an ACK without a session target from the latest detail handoff', async () => {
  const detail = item('cross-manager-mail', { manager: 'work' });
  const f = makeHarness({ manager: 'personal', details: { 'cross-manager-mail': detail }, handle: async (call) => {
    if (call.method === 'POST' && pathOnly(call.path).endsWith('/handoff')) return { item: detail, session: {} };
    if (call.method === 'GET' && pathOnly(call.path) === '/api/v1/inbox/cross-manager-mail') return {
      ...detail, handoffs: [{ machine_id: 'server', session_id: 'recovered-main', at: '2026-11-11T10:00:00Z' }]
    };
    return undefined;
  } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client, rowFor(detail));
  const target = await workspace.handoff('Review this with me');
  assert.ok(target);
  assert.equal(target.id, 'recovered-main');
  assert.equal(target.manager, 'work');
  assert.equal(f.calls.filter((call) => call.method === 'POST' && pathOnly(call.path).endsWith('/handoff')).length, 1);
  assert.equal(f.calls.filter((call) => call.method === 'GET' && pathOnly(call.path) === '/api/v1/inbox/cross-manager-mail').length, 2);
  await workspace.close();
});

test('pending and unknown handoff receipts preserve the draft and block another send', async () => {
  for (const receipt of [
    { name: 'pending', failure: new HttpFailure(202, 'result_unknown', true, true) },
    { name: 'unknown', failure: new HttpFailure(0, 'result_unknown', true, false) }
  ]) {
    const detail = item('mail-1');
    const f = makeHarness({ details: { 'mail-1': detail }, handle: async (call) => {
      if (call.method === 'POST' && pathOnly(call.path).endsWith('/handoff')) throw receipt.failure;
      return undefined;
    } });
    const workspace = new InboxWorkspace(); await workspace.open(f.client, rowFor(detail));
    assert.equal(await workspace.handoff('Review this with me'), undefined);
    assert.equal(workspace.writeState, receipt.name);
    assert.equal(workspace.handoffDraft, 'Review this with me');
    assert.equal(await workspace.handoff('Review this with me'), undefined);
    assert.equal(f.calls.filter((call) => call.method === 'POST' && pathOnly(call.path).endsWith('/handoff')).length, 1);
    await workspace.close();
  }
});

test('an acknowledged handoff with a failed target reread stays handed off', async () => {
  const detail = item('mail-1');
  let detailReads = 0;
  const f = makeHarness({ details: { 'mail-1': detail }, handle: async (call) => {
    if (call.method === 'POST' && pathOnly(call.path).endsWith('/handoff')) return { item: detail, session: {} };
    if (call.method === 'GET' && pathOnly(call.path) === '/api/v1/inbox/mail-1') {
      detailReads++;
      if (detailReads > 1) throw new HttpFailure(503, 'temporary');
      return detail;
    }
    return undefined;
  } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client, rowFor(detail));
  assert.equal(await workspace.handoff('Review this with me'), undefined);
  assert.equal(workspace.writeState, 'handed_off');
  assert.equal(workspace.handoffDraft, '');
  assert.equal(workspace.error, 'inbox_handoff_target_unavailable');
  assert.equal(detailReads, 2);
  assert.equal(f.calls.filter((call) => call.method === 'POST' && pathOnly(call.path).endsWith('/handoff')).length, 1);
  await workspace.close();
});

test('a confirmed mail reply sends the UID and version from the shown detail', async () => {
  const detail = mailDetail();
  const f = makeHarness({ rows: [detail], details: { 'mail-1': detail } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client, rowFor(detail));
  workspace.setReplyDraft('Approved response'); await workspace.draftWrite;
  const fingerprint = workspace.replyConfirmationSnapshot('Approved response');
  assert.equal(await workspace.replyInbox('Approved response', fingerprint), true);
  const sent = f.calls.find((call) => call.path.includes('/inbox/mail-1/reply'));
  assert.ok(sent);
  assert.match(sent.path, /manager=work$/);
  assert.equal(sent.body.expected_uid, 31);
  assert.equal(sent.body.expected_version, 'target-v1');
  assert.equal(sent.body.text, 'Approved response');
  await workspace.close();
});

test('a stale confirmed mail target refreshes detail and rejects the old confirmation', async () => {
  const first = mailDetail('mail-1', 31, 'target-v1');
  const next = mailDetail('mail-1', 32, 'target-v2');
  let detailReads = 0;
  const f = makeHarness({ rows: [first], details: { 'mail-1': first }, handle: async (call) => {
    if (call.path.includes('/api/v1/inbox/mail-1?manager=')) {
      detailReads++;
      return detailReads === 1 ? first : next;
    }
    if (call.path.includes('/inbox/mail-1/reply')) throw new HttpFailure(409, 'mail_reply_target_changed');
    return undefined;
  } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client, rowFor(first));
  workspace.setReplyDraft('Answer'); await workspace.draftWrite;
  const oldConfirmation = workspace.replyConfirmationSnapshot('Answer');
  assert.equal(await workspace.replyInbox('Answer', oldConfirmation), false);
  assert.equal(workspace.replyTargetChanged, true);
  assert.equal(workspace.replyTargetSnapshot(), JSON.stringify(['work', 'mail', 'mail-a', 'mail-a', 'INBOX', '32', 'target-v2', '', '', '', '', '', '', false, '',
    'inbox', '', '', '', '', '', '', '']));
  const replyCalls = f.calls.filter((call) => call.path.includes('/inbox/mail-1/reply'));
  assert.equal(replyCalls.length, 1);
  assert.equal(await workspace.replyInbox('Answer', oldConfirmation), false);
  assert.equal(f.calls.filter((call) => call.path.includes('/inbox/mail-1/reply')).length, 1);
  await workspace.close();
});

test('delayed detail from a replaced connection cannot update the selected item', async () => {
  const listItem = item('mail-1', { preview: 'List preview' });
  let releaseDetail, entered;
  const requestEntered = new Promise((resolve) => { entered = resolve; });
  const f = makeHarness({ rows: [listItem], handle: async (call) => {
    if (call.path.includes('/api/v1/inbox/mail-1?manager=')) {
      entered(); return new Promise((resolve) => { releaseDetail = resolve; });
    }
    return undefined;
  } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client);
  const opening = workspace.openRow(rowFor(listItem)); await requestEntered;
  f.state.generation++; f.state.manager = 'personal'; f.client.profile.manager = 'personal';
  workspace.onDisconnected();
    releaseDetail({ ...listItem, preview: 'Stale detail' });
  await opening;
  assert.equal(WirePreview(workspace.detail), 'List preview');
  await workspace.close();
});

function WirePreview(detail) { return typeof detail.preview === 'string' ? detail.preview : ''; }

test('a delayed reply completion from the old scope never uses the replacement repository', async () => {
  const detail = mailDetail();
  let releaseSend, entered;
  const requestEntered = new Promise((resolve) => { entered = resolve; });
  const f = makeHarness({ rows: [detail], details: { 'mail-1': detail }, handle: async (call) => {
    if (call.path.includes('/inbox/mail-1/reply')) {
      entered(); return new Promise((resolve) => { releaseSend = resolve; });
    }
    return undefined;
  } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client, rowFor(detail));
  const fingerprint = workspace.replyConfirmationSnapshot('A confirmed reply');
  const sending = workspace.replyInbox('A confirmed reply', fingerprint); await requestEntered;
  const oldRepo = f.state.repo;
  f.state.generation++; f.state.manager = 'personal'; f.client.profile.manager = 'personal';
  f.state.repo = new HubRepository(new EndpointConfig('https://other.example.test', 'hub-2', '', '', 'personal'), testStore());
  releaseSend({ ...detail, preview: 'Stale completion' });
  assert.equal(await sending, false);
  assert.equal(f.calls.filter((call) => call.path.includes('/inbox/mail-1/reply')).length, 1);
  assert.equal(f.state.repo === oldRepo, false);
  assert.equal(workspace.detail.preview, detail.preview);
  await workspace.close();
});

test('generic pagination keeps message IDs distinct and restores a target-scoped draft', async () => {
  const chat = conversationDetail();
  const f = makeHarness({ rows: [chat], details: { 'chat-1': chat }, handle: async (call) => {
    if (call.path.includes('/conversations/conversation-1/messages') && call.path.includes('before=opaque-cursor'))
      return { account: 'chat-a', messages: [message('m-0', '9', 'Older'), message('m-1', '10', 'Duplicate page copy')], has_older: false };
    if (call.path.includes('/conversations/conversation-1/messages'))
      return { account: 'chat-a', messages: [message('m-1', '10', 'Hello'), message('m-2', '11', 'World')], has_older: true, older_cursor: 'opaque-cursor' };
    return undefined;
  } });
  const row = rowFor(chat), workspace = new InboxWorkspace(); await workspace.open(f.client, row);
  assert.deepEqual(workspace.feedMessages.map((entry) => [entry.id, entry.position]), [['m-1', '10'], ['m-2', '11']]);
  await workspace.loadOlder();
  assert.deepEqual(workspace.feedMessages.map((entry) => [entry.id, entry.position]), [['m-0', '9'], ['m-1', '10'], ['m-2', '11']]);
  workspace.setThreadTarget('m-1', false); workspace.setReplyDraft('Draft reply'); await workspace.draftWrite;
  await workspace.close();
  const restored = new InboxWorkspace(); await restored.open(f.client, row);
  assert.equal(restored.replyDraft, 'Draft reply');
  assert.equal(restored.threadReplyTo, 'm-1');
  assert.equal(restored.threadReply, false);
  await restored.close();
});

test('generic message actions honor per-message reply and reaction permissions', async () => {
  const chat = conversationDetail();
  const restricted = { ...message('m-restricted', '10', 'Read only'), can_reply: false, can_react: false, reactions: [{ key: '👍', count: 1 }] };
  const f = makeHarness({ rows: [chat], details: { 'chat-1': chat }, handle: async (call) => {
    if (pathOnly(call.path) === '/api/v1/messaging/chat/conversations/conversation-1/messages')
      return { account: 'chat-a', messages: [restricted], has_older: false };
    return undefined;
  } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client, rowFor(chat));
  const target = workspace.feedMessages[0];
  assert.equal(workspace.canReplyToMessage(target), false);
  assert.equal(workspace.canReact(target), false);
  workspace.setThreadTarget(target.id, false);
  assert.equal(workspace.threadReplyTo, '');
  await workspace.close();
});

test('compose confirmation binds account and all recipients to the current draft', async () => {
  const f = makeHarness();
  const workspace = new InboxWorkspace(); await workspace.open(f.client);
  await workspace.openMailCompose();
  workspace.setMailComposeField('to', 'first@example.test');
  workspace.setMailComposeField('cc', 'copy@example.test');
  workspace.setMailComposeField('subject', 'Status');
  workspace.setMailComposeField('text', 'The approved message');
  await workspace.composeWrite;
  const old = workspace.mailComposeSnapshot(), oldConfirmation = workspace.mailComposeConfirmation(old);
  workspace.setMailComposeField('to', 'changed@example.test'); await workspace.composeWrite;
  assert.equal(await workspace.sendMailCompose(old, oldConfirmation), false);
  assert.equal(f.calls.filter((call) => pathOnly(call.path) === '/api/v1/mail/send').length, 0);
  const current = workspace.mailComposeSnapshot(), confirmation = workspace.mailComposeConfirmation(current);
  assert.equal(await workspace.sendMailCompose(current, confirmation), true);
  const send = f.calls.find((call) => pathOnly(call.path) === '/api/v1/mail/send');
  assert.deepEqual(send.body.to, [{ name: '', email: 'changed@example.test' }]);
  assert.deepEqual(send.body.cc, [{ name: '', email: 'copy@example.test' }]);
  assert.equal(send.body.account_id, 'mail-a');
  await workspace.close();
});

test('pending compose receipts retain the draft and prevent a second send after restore', async () => {
  const f = makeHarness({ handle: async (call) => pathOnly(call.path) === '/api/v1/mail/send' ? { state: 'pending' } : undefined });
  const workspace = new InboxWorkspace(); await workspace.open(f.client); await workspace.openMailCompose();
  workspace.setMailComposeField('to', 'first@example.test');
  workspace.setMailComposeField('subject', 'Status'); workspace.setMailComposeField('text', 'Keep this draft');
  await workspace.composeWrite;
  const first = workspace.mailComposeSnapshot();
  assert.equal(await workspace.sendMailCompose(first, workspace.mailComposeConfirmation(first)), false);
  assert.equal(workspace.mailComposeState, 'pending');
  assert.equal(workspace.mailComposeBlocked, true);
  workspace.closeMailCompose(); await workspace.openMailCompose();
  assert.equal(workspace.mailComposeText, 'Keep this draft');
  assert.equal(workspace.mailComposeBlocked, true);
  assert.equal(f.calls.filter((call) => pathOnly(call.path) === '/api/v1/mail/send').length, 1);
  await workspace.close();
});

test('a pending attachment upload blocks a different client ID for the same conversation', async () => {
  const chat = conversationDetail();
  let byteCount = 0;
  const f = makeHarness({ rows: [chat], details: { 'chat-1': chat },
    bytes: async () => {
      byteCount++;
      return { data: new TextEncoder().encode('{"state":"pending"}').buffer, contentType: 'application/json' };
    } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client, rowFor(chat));
  workspace.setAttachment(new InboxAttachmentDraft('notes.txt', 'text/plain', 'file', new TextEncoder().encode('hello').buffer));
  assert.equal(await workspace.sendAttachment(), false);
  assert.equal(workspace.writeState, 'pending');
  assert.ok(workspace.attachment);
  assert.equal(await workspace.sendAttachment(), false);
  assert.equal(byteCount, 1);
  assert.equal(f.byteCalls.length, 1);
  await workspace.close();
});

test('a superseded list metadata response cannot replace newer data or clear its loading state', async () => {
  let phase = 'initial', oldSources, newItems, enteredOld, enteredNew;
  const oldStarted = new Promise(resolve => { enteredOld = resolve; }), newStarted = new Promise(resolve => { enteredNew = resolve; });
  const f = makeHarness({ rows: [item('first')], handle: async call => {
    if (pathOnly(call.path) === '/api/v1/messaging' && phase === 'old') { enteredOld(); return new Promise(resolve => { oldSources = resolve; }); }
    if (pathOnly(call.path) === '/api/v1/inbox' && phase === 'new') { enteredNew(); return new Promise(resolve => { newItems = resolve; }); }
    if (pathOnly(call.path) === '/api/v1/messaging' && phase === 'new') return { sources: [{ id: 'fresh-source' }] };
  } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client);
  workspace.search = 'retain search'; workspace.sourceFilter = 'mail'; workspace.replyDraft = 'retain draft';
  phase = 'old'; const older = workspace.refresh(); await oldStarted;
  phase = 'new'; const latest = workspace.refresh(); await newStarted;
  oldSources({ sources: [{ id: 'obsolete-source' }] }); await older;
  assert.equal(workspace.loading, true); assert.notEqual(workspace.sourceDefinitions[0].id, 'obsolete-source');
  newItems({ items: [item('foreign', { manager: 'other-manager' }), item('newest')] }); await latest;
  assert.deepEqual(workspace.items.map(row => row.id), ['foreign', 'newest']);
  assert.equal(workspace.items[0].manager, 'other-manager'); assert.equal(workspace.sourceDefinitions[0].id, 'fresh-source');
  assert.equal(workspace.loading, false); assert.equal(workspace.search, 'retain search'); assert.equal(workspace.sourceFilter, 'mail');
  assert.equal(workspace.replyDraft, 'retain draft'); await workspace.close();
});

test('a late failed inbox read cannot overwrite a newer refresh with an error', async () => {
  let phase = 'initial', rejectOld, resolveNew, enteredOld, enteredNew;
  const oldStarted = new Promise(resolve => { enteredOld = resolve; }), newStarted = new Promise(resolve => { enteredNew = resolve; });
  const f = makeHarness({ rows: [item('first')], handle: async call => {
    if (pathOnly(call.path) !== '/api/v1/inbox') return undefined;
    if (phase === 'old') { enteredOld(); return new Promise((_resolve, reject) => { rejectOld = reject; }); }
    if (phase === 'new') { enteredNew(); return new Promise(resolve => { resolveNew = resolve; }); }
  } });
  const workspace = new InboxWorkspace(); await workspace.open(f.client);
  phase = 'old'; const older = workspace.refresh(); await oldStarted;
  phase = 'new'; const latest = workspace.refresh(); await newStarted;
  rejectOld(new Error('obsolete_failure')); await older;
  assert.equal(workspace.loading, true); assert.equal(workspace.error, '');
  resolveNew({ items: [item('current')] }); await latest;
  assert.deepEqual(workspace.items.map(row => row.id), ['current']); assert.equal(workspace.error, ''); await workspace.close();
});

test('an inbox list refresh never dispatches through a repository replaced by a scope change', async () => {
  const f = makeHarness({ rows: [item('first')] }), workspace = new InboxWorkspace(); await workspace.open(f.client);
  f.calls.length = 0;
  f.state.repo = {};
  await workspace.refresh();
  assert.equal(f.calls.length, 0); assert.deepEqual(workspace.items.map(row => row.id), ['first']);
  await workspace.close();
});
