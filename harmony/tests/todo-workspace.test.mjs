import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === './ClientModel' && context.parentURL?.endsWith('/TodoWorkspace.ets'))
      return { url: 'persona-todo:client', shortCircuit: true };
    if (specifier === './HubRepository' && context.parentURL?.endsWith('/TodoWorkspace.ets'))
      return { url: 'persona-todo:repository', shortCircuit: true };
    if (specifier === '../platform/HubTransport' && context.parentURL?.endsWith('/TodoWorkspace.ets'))
      return { url: 'persona-todo:transport', shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-todo:client') return { format: 'module', source: 'export class ClientModel {}', shortCircuit: true };
    if (url === 'persona-todo:repository') return { format: 'module', source: `
      export function segment(value) { return encodeURIComponent(value); }
      export class HubRepository {}
    `, shortCircuit: true };
    if (url === 'persona-todo:transport') return { format: 'module', source: `
      export class HttpFailure extends Error {
        constructor(status, code, resultUnknown = false, resultPending = false) {
          super(code); this.status = status; this.code = code; this.resultUnknown = resultUnknown; this.resultPending = resultPending;
        }
      }
    `, shortCircuit: true };
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { TodoWorkspace, TodoWorkspaceItem, TodoWorkspaceSpace, TodoWorkspaceSpaceChoice,
  todoWorkspaceStatus, relatedTodoMembers } = await import('../entry/src/main/ets/models/TodoWorkspace.ets');
const { reconcileBoundRows } = await import('../entry/src/main/ets/models/RowBinding.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');
const { HttpFailure } = await import('persona-todo:transport');

function todo(id, fields = {}) {
  return {
    id, title: 'Todo ' + id, notes: '', manager: 'work', created_at: '2026-11-10T10:00:00Z',
    updated_at: '2026-11-10T10:00:00Z', agents: [], spaces: [], ...fields
  };
}

function session(id, state = 'offline', fields = {}) {
  return ResourceRow.from({ id, session_id: id, machine_id: 'm1', manager: 'work', title: 'Agent ' + id,
    state, status: state, ...fields }, 'session');
}

function machine(id = 'm1', fields = {}) {
  return ResourceRow.from({ id, manager: 'work', name: 'Computer ' + id, online: true, trust: '', ...fields }, 'machine');
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function makeHarness(options = {}) {
  const state = {
    manager: options.manager || 'work', group: options.group || 'profile-1', generation: 4,
    scope: 'hub:' + (options.manager || 'work'), connected: true, demo: false,
    todos: (options.todos || []).map((item) => structuredClone(item)),
    sessions: options.sessions || [], machines: options.machines || [machine()], conversations: options.conversations || [],
    calls: [], views: new Map(), blocked: new Set(), request: options.request, viewLoad: options.viewLoad
  };
  const repo = {
    async call(path, method = 'GET', body = {}) {
      const call = { path, method, body: structuredClone(body) };
      state.calls.push(call);
      if (state.request) return state.request(call, state);
      if (path.split('?')[0] === '/api/v1/todos' && method === 'GET') return { todos: structuredClone(state.todos) };
      if (path.split('?')[0] === '/api/v1/todos' && method === 'POST') {
        const item = todo('created-1', { ...body, created_at: '2026-11-11T10:00:00Z' });
        state.todos.unshift(item);
        return { todo: structuredClone(item) };
      }
      if (path.startsWith('/api/v1/todos/') && method === 'PATCH') {
        const id = decodeURIComponent(path.split('/')[4].split('?')[0]);
        const index = state.todos.findIndex((item) => item.id === id);
        const current = state.todos[index] || todo(id);
        const updated = { ...current, ...body, updated_at: '2026-11-11T10:01:00Z' };
        if (body.done === true) { updated.done_at = current.start_error ? '2026-11-11T10:01:00Z' : (current.done_at || '2026-11-11T10:01:00Z'); updated.start_error = ''; }
        if (body.done === false) { updated.done_at = null; updated.start_error = ''; updated.reopened_at = '2026-11-11T10:01:00Z'; }
        state.todos[index] = updated;
        return { todo: structuredClone(updated) };
      }
      if (path.startsWith('/api/v1/todos/') && method === 'DELETE') {
        const id = decodeURIComponent(path.split('/')[4].split('?')[0]);
        state.todos = state.todos.filter((item) => item.id !== id);
        return { ok: true };
      }
      return { ok: true };
    }
  };
  state.repo = repo;
  const client = {
    get connected() { return state.connected; },
    get demo() { return state.demo; },
    get sessions() { return state.sessions; },
    get machines() { return state.machines; },
    get conversations() { return state.conversations; },
    get knowledge() { return options.knowledge || []; },
    profile: { id: state.group, manager: state.manager },
    repo() { if (!state.connected) throw new Error('offline'); return state.repo; },
    scope() { return state.scope; },
    connectionGeneration() { return state.generation; },
    owns(generation, scope) { return generation === state.generation && scope === state.scope; },
    operationBlocked(path) { return state.blocked.has(path); },
    async loadView(key) {
      if (state.viewLoad) return state.viewLoad(key, state);
      return state.views.get(state.scope + ':' + key) || '';
    },
    viewRevision(){return state.epoch||0;},
    async saveView(key, value) { state.views.set(state.scope + ':' + key, value); },
    async viewFor(row) {
      if (row.manager === state.manager) return client;
      return state.otherClient;
    },
    todos: [], features: []
  };
  return { client, repo, state };
}

async function opened(options = {}) {
  const harness = makeHarness(options);
  const workspace = new TodoWorkspace();
  await workspace.open(harness.client);
  return { ...harness, workspace };
}

function pathOnly(path) { return path.split('?')[0]; }

test('projects server lifecycle fields into the six native states with SOP priority', () => {
  const linked = [{ machine_id: 'm1', session_id: 's1' }];
  const state = (fields, sessionRows = []) => todoWorkspaceStatus(new TodoWorkspaceItem(todo('t', { agents: linked, ...fields }), 'work'), sessionRows);

  assert.equal(state({ done_at: '2026-11-11T09:00:00Z', start_error: 'node unavailable' }), 'needs_you');
  assert.equal(state({ done_at: '2026-11-11T09:00:00Z' }, [session('s1', 'working')]), 'done');
  assert.equal(state({ ask: { verdict: 'choose one', outcome: 'decide', asked_at: '2026-11-11T09:00:00Z' } }), 'needs_you');
  assert.equal(state({ supervision: { state: 'needs_user' } }), 'needs_you');
  assert.equal(state({}, [session('s1', 'needs_you')] ), 'needs_you');
  assert.equal(state({ ask: { verdict: 'verify result', outcome: 'done', asked_at: '2026-11-11T09:00:00Z' } }), 'checking');
  assert.equal(state({ supervision: { state: 'pending_review' } }), 'checking');
  assert.equal(state({ supervision: { state: 'pending_review' } }, [session('s1', 'working')]), 'working');
  assert.equal(state({}, [session('s1', 'unread')]), 'checking');
  assert.equal(state({}, [session('s1', 'working')]), 'working');
  assert.equal(state({}, [session('s1', 'waiting')]), 'working');
  assert.equal(state({ ask: { verdict: 'verify', outcome: 'done', asked_at: '2026-11-11T09:00:00Z' } }, [session('s1', 'working')]), 'working');
  assert.equal(state({ supervision: { state: 'accepted', reviews: [{ verdict: 'accepted' }] } }), 'done');
  assert.equal(state({ supervision: { state: 'accepted', reviews: [{ verdict: 'rework' }] } }, [session('s1', 'offline')]), 'followup');
  assert.equal(state({ delivery: { state: 'live' } }), 'done');
  assert.equal(state({ pr_url: 'https://github.com/example/repo/pull/7', delivery: { state: 'pr' } }), 'followup');
  assert.equal(state({}, [session('s1', 'idle', { turn_error: 'rate limit' })]), 'needs_you');
  assert.equal(state({}, [session('s1', 'idle', { turn_error: 'usage limit', turn_error_kind: 'usage_limit' })]), 'checking');
  assert.equal(state({}, [session('s1', 'offline')]), 'followup');
  assert.equal(todoWorkspaceStatus(new TodoWorkspaceItem(todo('empty'), 'work'), []), 'not_started');
  const mixed = new TodoWorkspaceItem(todo('mixed', { agents: linked.concat([{ machine_id: 'm1', session_id: 's2' }]) }), 'work');
  assert.equal(todoWorkspaceStatus(mixed, [session('s1', 'working'), session('s2', 'unread')]), 'checking');
});

test('failed starts remain owner-actionable while related rows follow the server done_at boundary', () => {
  const failed = new TodoWorkspaceItem(todo('failed', { done_at: '2026-11-11T09:00:00Z', start_error: 'launch failed', plan_id: 'plan-a' }), 'work');
  const related = new TodoWorkspaceItem(todo('related', { plan_id: 'plan-a' }), 'work');
  const completed = new TodoWorkspaceItem(todo('completed', { done_at: '2026-11-11T09:00:00Z', plan_id: 'plan-a' }), 'work');
  assert.equal(failed.isDone(), true, 'the server stores a failed start as done_at plus start_error');
  assert.equal(todoWorkspaceStatus(failed, []), 'needs_you');
  assert.equal(relatedTodoMembers([failed, related], failed).map((item) => item.id).join(','), 'failed');
  assert.equal(relatedTodoMembers([failed, completed], failed).map((item) => item.id).join(','), 'failed,completed');
});

test('relationship keys normalize Unicode titles and PR URL suffixes like the shared triage contract', () => {
  const titleA = new TodoWorkspaceItem(todo('title-a', { title: '任务；确认更新！' }), 'work');
  const titleB = new TodoWorkspaceItem(todo('title-b', { title: '任务 确认更新' }), 'work');
  const prA = new TodoWorkspaceItem(todo('pr-a', { pr_url: 'https://example.test/pull/42/?utm_source=app#overview' }), 'work');
  const prB = new TodoWorkspaceItem(todo('pr-b', { pr_url: 'https://example.test/pull/42' }), 'work');
  assert.equal(relatedTodoMembers([titleA, titleB], titleA).length, 2);
  assert.equal(relatedTodoMembers([prA, prB], prA).length, 2);
});

test('search, filters, and status, project, and thread groupings use the selected values', async () => {
  const items = [
    todo('one', { title: 'Fix login', project: 'mobile', thread: { channel: 'lark', chat_id: 'room', thread_id: 'thread-a', title: 'Release' }, plan_id: 'p1' }),
    todo('two', { title: 'Fix login', project: 'mobile', thread: { channel: 'lark', chat_id: 'room', thread_id: 'thread-a', title: 'Release' }, plan_id: 'p1' }),
    todo('three', { title: 'Buy tea', project: 'home' })
  ];
  const { workspace } = await opened({ todos: items });
  assert.equal(workspace.sections().length, 1, 'related items share one status row');
  assert.equal(workspace.sections()[0].rows[0].related().length, 1);
  workspace.setGroupBy('project');
  assert.deepEqual(workspace.sections().map((section) => section.title), ['home', 'mobile']);
  workspace.setGroupBy('thread');
  assert.deepEqual(workspace.sections().map((section) => section.title), ['Release', 'todo_no_thread']);
  workspace.setSearch('tea');
  assert.equal(workspace.sections().flatMap((section) => section.rows).length, 1);
  workspace.setStatusFilter('done');
  assert.equal(workspace.sections().length, 0);
  workspace.close();
});

test('same-key Todo group bindings replace immutable values and deferred row actions use the current lead', async () => {
  const { workspace } = await opened({ todos: [todo('t1', { plan_id: 'plan-a' }), todo('t2', { plan_id: 'plan-a' })] });
  const owner = {};
  const initialGroup = workspace.sections()[0].rows[0];
  const initialBindings = reconcileBoundRows([], [initialGroup], (group) => group.key, undefined, owner, '', 'scope-a');
  const originalNativeKey = initialBindings[0].key;

  workspace.items = workspace.items.map((item) => item.id === 't1'
    ? new TodoWorkspaceItem({ ...item.data(), title: 'Renamed latest', updated_at: '2026-11-11T11:00:00Z' }, 'work')
    : item);
  const currentGroup = workspace.sections()[0].rows[0];
  const currentBindings = reconcileBoundRows(initialBindings, [currentGroup], (group) => group.key, owner, owner, 'scope-a', 'scope-a');
  let openedTitle = '';
  const deferredOpen = () => { openedTitle = currentBindings[0].value.lead.title; };
  deferredOpen();

  assert.strictEqual(currentBindings[0], initialBindings[0]);
  assert.equal(currentBindings[0].key, originalNativeKey);
  assert.equal(currentBindings[0].value.lead.title, 'Renamed latest');
  assert.equal(openedTitle, 'Renamed latest');

  const page = readFileSync(new URL('../entry/src/main/ets/components/TodoWorkspacePage.ets', import.meta.url), 'utf8');
  assert.match(page, /ForEach\(section\.rows, \(binding: BoundRow<TodoWorkspaceGroup>\) => this\.todoRow\(binding\),\s*\(binding: BoundRow<TodoWorkspaceGroup>\) => binding\.key\)/);
  assert.match(page, /\.onClick\(\(\) => this\.onOpenResource\(this\.workspace\.todoResource\(binding\.value\.lead\)\)\)/);
  assert.match(page, /ForEach\(this\.lineageRowBindings/);
  assert.match(page, /ForEach\(this\.relatedTodoBindings/);
  assert.match(page, /ForEach\(this\.spaceBindings, \(binding: BoundRow<TodoWorkspaceSpace>\) => this\.spaceRow\(binding\)/);
  assert.match(page, /ForEach\(this\.activeAgentBindings, \(binding: BoundRow<TodoWorkspaceAgent>\) => this\.agentRow\(binding\)/);
  assert.match(page, /ForEach\(this\.previousAgentBindings, \(binding: BoundRow<TodoWorkspaceAgent>\) => this\.agentRow\(binding\)/);
  assert.match(page, /\.onClick\(\(\) => this\.confirmUnlinkAgent\(this\.workspace\.selected, binding\.value\)\)/);
  assert.match(page, /\.onClick\(\(\) => this\.confirmUnlinkSpace\(this\.workspace\.selected, binding\.value\)\)/);
  workspace.close();
});

test('create, edit, done, reopen, existing-Agent link, project link, and delete use exact Todo routes', async () => {
  const linked = todo('t1', { agents: [], spaces: [], thread: { channel: 'lark', chat_id: 'room', thread_id: 'thread-1', title: 'Release thread' } });
  const agent = session('s1', 'working', { cwd: '/repo', agent: 'claude', space_name: 'persona project' });
  const { workspace, state } = await opened({ todos: [linked], sessions: [agent], machines: [machine()] });
  await workspace.openRow(workspace.items[0]);
  workspace.setTitleDraft('  Updated title  ');
  workspace.setNotesDraft('Details');
  assert.equal(await workspace.save(), true);
  assert.deepEqual(state.calls.at(-1), {
    path: '/api/v1/todos/t1?manager=work', method: 'PATCH', body: { title: 'Updated title', notes: 'Details' }
  });
  assert.equal(await workspace.setDone(workspace.selected, true), true);
  assert.deepEqual(state.calls.at(-1), { path: '/api/v1/todos/t1?manager=work', method: 'PATCH', body: { done: true } });
  assert.equal(await workspace.setDone(workspace.selected, false), true);
  assert.deepEqual(state.calls.at(-1), { path: '/api/v1/todos/t1?manager=work', method: 'PATCH', body: { done: false } });

  assert.equal(await workspace.linkAgent(workspace.selected, agent), true);
  assert.deepEqual(state.calls.at(-1), { path: '/api/v1/todos/t1/agents/m1/s1?manager=work', method: 'PUT', body: {} });
  assert.equal(workspace.selected.activeAgents()[0].source, 'manual');
  assert.equal(state.calls.some((call) => pathOnly(call.path).includes('/messages')), false, 'linking never sends a message');
  assert.equal(await workspace.linkSpace(workspace.selected, new TodoWorkspaceSpaceChoice('m1', 'persona project', 'Computer m1 · persona project')), true);
  assert.deepEqual(state.calls.at(-1), {
    path: '/api/v1/todos/t1/spaces/m1/persona%20project?manager=work', method: 'PUT', body: {}
  });
  const active = workspace.selected.activeAgents()[0];
  assert.equal(await workspace.unlinkAgent(workspace.selected, active), true);
  assert.deepEqual(state.calls.at(-1), { path: '/api/v1/todos/t1/agents/m1/s1?manager=work', method: 'DELETE', body: {} });
  const space = new TodoWorkspaceSpace({ machine_id: 'm1', space: 'persona project', linked_at: '2026-11-11T09:00:00Z' });
  assert.equal(await workspace.unlinkSpace(workspace.selected, space), true);
  assert.deepEqual(state.calls.at(-1), { path: '/api/v1/todos/t1/spaces/m1/persona%20project?manager=work', method: 'DELETE', body: {} });
  assert.equal(await workspace.remove(workspace.selected), true);
  assert.deepEqual(state.calls.at(-1), { path: '/api/v1/todos/t1?manager=work', method: 'DELETE', body: {} });
  assert.equal(state.calls.filter((call) => call.method === 'POST' && pathOnly(call.path) === '/api/v1/todos').length, 0);
  workspace.close();
});

test('new drafts are scoped to group and manager, and successful create needs no follow-up read', async () => {
  const { workspace, state } = await opened({ todos: [] });
  const key = 'hub:work:todo:draft:["profile-1","work","new"]';
  state.views.set(key, JSON.stringify({ title: 'Recovered task', notes: 'Recovered notes' }));
  workspace.startCreate();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(workspace.titleDraft, 'Recovered task');
  assert.equal(workspace.notesDraft, 'Recovered notes');
  state.calls.length = 0;
  assert.equal(await workspace.save(), true);
  assert.deepEqual(state.calls, [{ path: '/api/v1/todos?manager=work', method: 'POST', body: { title: 'Recovered task', notes: 'Recovered notes' } }]);
  workspace.close();
});

test('existing edits restore and persist only the matching group, manager, and Todo draft', async () => {
  const draftKey = 'hub:work:todo:draft:["profile-1","work","t1"]';
  const { workspace, state } = await opened({ todos: [todo('t1')] });
  state.views.set(draftKey, JSON.stringify({ title: 'Local title', notes: 'Local notes' }));
  await workspace.openRow(workspace.items[0]);
  assert.equal(workspace.titleDraft, 'Local title');
  assert.equal(workspace.notesDraft, 'Local notes');
  workspace.setTitleDraft('New local title');
  workspace.setNotesDraft('New local notes');
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(JSON.parse(state.views.get(draftKey)), { title: 'New local title', notes: 'New local notes' });
  workspace.close();
});

test('offline and untrusted computers cannot be linked, and an active association is removed only after ACK', async () => {
  const item = todo('t1');
  const agent = session('s1', 'working');
  const { workspace, state } = await opened({ todos: [item], sessions: [agent], machines: [machine('m1', { online: true, trust: 'pending' })] });
  assert.equal(workspace.candidateSessions().length, 0);
  assert.equal(await workspace.linkAgent(workspace.items[0], agent), false);
  assert.equal(state.calls.filter((call) => call.method === 'PUT').length, 0);
  workspace.close();
});

test('unknown and pending writes stay blocked without a replacement request', async () => {
  for (const failure of [new HttpFailure(0, 'result_unknown', true, false), new HttpFailure(202, 'result_unknown', true, true)]) {
    const { workspace, state } = await opened({ todos: [todo('t1')], request: async (call, fixture) => {
      if (call.method === 'GET') return { todos: fixture.todos };
      fixture.blocked.add(call.path);
      throw failure;
    } });
    await workspace.openRow(workspace.items[0]);
    assert.equal(await workspace.setDone(workspace.selected, true), false);
    assert.equal(state.calls.filter((call) => call.method === 'PATCH').length, 1);
    assert.ok(['todo_write_unknown', 'todo_write_pending'].includes(workspace.error));
    assert.equal(await workspace.setDone(workspace.selected, true), false);
    assert.equal(state.calls.filter((call) => call.method === 'PATCH').length, 1);
    workspace.close();
  }
});

test('a parent list refresh shows independently completed, renamed, and deleted detail mutations', async () => {
  const harness = makeHarness({ todos: [todo('t1')] });
  const parent = new TodoWorkspace(), detail = new TodoWorkspace();
  await parent.open(harness.client);
  const row = new ResourceRow('t1', 'Todo t1', 'todo');
  row.manager = 'work';
  await detail.open(harness.client, row, false);
  parent.setSearch('kept search');
  parent.setStatusFilter('all');
  parent.setGroupBy('project');
  parent.startCreate();
  parent.setTitleDraft('Unsubmitted parent draft');
  parent.setNotesDraft('Keep this text');

  assert.equal(await detail.setDone(detail.selected, true), true);
  assert.equal(parent.items[0].isDone(), false, 'the parent has an independent list snapshot before returning');
  await parent.refresh();
  assert.equal(parent.items[0].isDone(), true);

  await detail.openRow(detail.selected);
  detail.setTitleDraft('Renamed in detail');
  assert.equal(await detail.save(), true);
  await parent.refresh();
  assert.equal(parent.items[0].title, 'Renamed in detail');

  assert.equal(await detail.remove(detail.selected), true);
  await parent.refresh();
  assert.equal(parent.items.length, 0);
  assert.equal(parent.search, 'kept search');
  assert.equal(parent.statusFilter, 'all');
  assert.equal(parent.groupBy, 'project');
  assert.equal(parent.editorOpen, true);
  assert.equal(parent.titleDraft, 'Unsubmitted parent draft');
  assert.equal(parent.notesDraft, 'Keep this text');
  parent.close();
  detail.close();
});

for (const editorOpen of [false, true]) {
  test(`a deleted Todo detail stays selected, keeps its draft, and blocks writes when editorOpen=${editorOpen}`, async () => {
    const linked = todo('t1', {
      agents: [{ machine_id: 'm1', session_id: 's1', title: 'Linked agent', linked_at: '2026-11-11T10:00:00Z' }],
      spaces: [{ machine_id: 'm1', space: 'space-old', linked_at: '2026-11-11T10:00:00Z' }]
    });
    const harness = makeHarness({ todos: [linked], sessions: [session('s2', 'working')] });
    const workspace = new TodoWorkspace(), row = new ResourceRow('t1', 'Todo t1', 'todo');
    row.manager = 'work';
    await workspace.open(harness.client, row, false);
    workspace.setTitleDraft('Keep title draft');
    workspace.setNotesDraft('Keep notes draft');
    await workspace.draftWrites;
    workspace.editorOpen = editorOpen;
    harness.state.todos = [];

    await workspace.refresh();
    const selected = workspace.selected;
    assert.equal(workspace.targetGone, true);
    assert.equal(workspace.error, 'todo_gone');
    assert.equal(selected.id, 't1');
    assert.equal(workspace.titleDraft, 'Keep title draft');
    assert.equal(workspace.notesDraft, 'Keep notes draft');
    assert.equal(workspace.editorOpen, editorOpen);
    assert.equal(await workspace.save(), false);
    assert.equal(await workspace.setDone(selected, true), false);
    assert.equal(await workspace.remove(selected), false);
    assert.equal(await workspace.linkAgent(selected, session('s2', 'working')), false);
    assert.equal(await workspace.unlinkAgent(selected, selected.agents[0]), false);
    assert.equal(await workspace.linkSpace(selected, new TodoWorkspaceSpaceChoice('m1', 'space-new', 'Computer m1 · space-new')), false);
    assert.equal(await workspace.unlinkSpace(selected, selected.spaces[0]), false);
    assert.equal(harness.state.calls.filter((call) => call.method !== 'GET').length, 0);
    assert.equal(workspace.selected.id, 't1');
    assert.equal(workspace.titleDraft, 'Keep title draft');
    assert.equal(workspace.notesDraft, 'Keep notes draft');
    workspace.close();
  });
}

test('only the latest Todo list request replaces rows or clears the loading state', async () => {
  const requests = [];
  const harness = makeHarness({ request: async (call) => {
    if (call.method !== 'GET') return { ok: true };
    const pending = deferred();
    requests.push(pending);
    return pending.promise;
  } });
  const workspace = new TodoWorkspace();
  const opening = workspace.open(harness.client);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(requests.length, 1);

  const secondRefresh = workspace.refresh();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(requests.length, 2);
  requests[1].resolve({ todos: [todo('second', { title: 'Second result' })] });
  await secondRefresh;
  assert.equal(workspace.items[0].title, 'Second result');

  const thirdRefresh = workspace.refresh();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(requests.length, 3);
  assert.equal(workspace.loading, true);
  requests[0].resolve({ todos: [todo('stale', { title: 'Stale result' })] });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(workspace.items[0].title, 'Second result');
  assert.equal(workspace.loading, true, 'a stale read cannot clear the newer request spinner');

  requests[2].resolve({ todos: [todo('third', { title: 'Third result' })] });
  await Promise.all([thirdRefresh, opening]);
  assert.equal(workspace.items[0].title, 'Third result');
  assert.equal(workspace.loading, false);
  workspace.close();
});

test('an initial detail open waits for the newest list read before restoring its target', async () => {
  const firstRead = deferred(), secondRead = deferred();
  let getCount = 0;
  const harness = makeHarness({ request: async (call) => {
    if (call.method !== 'GET') return { ok: true };
    getCount++;
    return getCount === 1 ? firstRead.promise : secondRead.promise;
  } });
  const workspace = new TodoWorkspace(), row = new ResourceRow('t1', 'Todo t1', 'todo');
  row.manager = 'work';
  const opening = workspace.open(harness.client, row, false);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(getCount, 1);
  const newestRead = workspace.refresh();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(getCount, 2);

  firstRead.resolve({ todos: [] });
  await new Promise((resolve) => setImmediate(resolve));
  assert.notEqual(workspace.error, 'todo_gone', 'a replaced read cannot report that the detail target is missing');
  assert.equal(workspace.selected.id, '');

  secondRead.resolve({ todos: [todo('t1')] });
  await Promise.all([opening, newestRead]);
  assert.equal(workspace.selected.id, 't1');
  assert.equal(workspace.draftReady, true);
  assert.equal(workspace.error, '');
  workspace.close();
});

test('a successful Todo write invalidates an older read without rolling back its row or spinner', async () => {
  const staleRead = deferred();
  let holdRead = false, held = false;
  const request = async (call, fixture) => {
    if (call.method === 'GET') {
      if (holdRead && !held) { held = true; return staleRead.promise; }
      return { todos: structuredClone(fixture.todos) };
    }
    if (call.method === 'PATCH') {
      const id = decodeURIComponent(call.path.split('/')[4].split('?')[0]);
      const index = fixture.todos.findIndex((item) => item.id === id);
      const updated = { ...fixture.todos[index], ...call.body, updated_at: '2026-11-11T10:01:00Z' };
      if (call.body.done === true) { updated.done_at = '2026-11-11T10:01:00Z'; updated.start_error = ''; }
      fixture.todos[index] = updated;
      return { todo: structuredClone(updated) };
    }
    return { ok: true };
  };
  const { workspace } = await opened({ todos: [todo('t1')], request });
  await workspace.openRow(workspace.items[0]);
  holdRead = true;
  const read = workspace.refresh();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(held, true);
  assert.equal(workspace.loading, true);

  assert.equal(await workspace.setDone(workspace.selected, true), true);
  assert.equal(workspace.items[0].isDone(), true);
  assert.equal(workspace.loading, false, 'the acknowledged write ends the superseded spinner');
  staleRead.resolve({ todos: [todo('t1', { title: 'Stale row' })] });
  await read;
  assert.equal(workspace.items[0].isDone(), true);
  assert.equal(workspace.selected.isDone(), true);
  assert.equal(workspace.loading, false);
  workspace.close();
});

test('a list read from a replaced root source cannot replace the new source rows', async () => {
  const oldRead = deferred();
  const oldHarness = makeHarness({ manager: 'parent', request: async (call) => {
    if (call.method === 'GET') return oldRead.promise;
    return { ok: true };
  } });
  const newHarness = makeHarness({ manager: 'detail', todos: [todo('detail-source', { title: 'Detail source', manager: 'detail' })] });
  const workspace = new TodoWorkspace();
  const oldOpen = workspace.open(oldHarness.client);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(oldHarness.state.calls.length, 1);

  await workspace.open(newHarness.client);
  assert.equal(workspace.items[0].title, 'Detail source');
  oldRead.resolve({ todos: [todo('old-source', { title: 'Old parent source', manager: 'parent' })] });
  await oldOpen;
  assert.equal(workspace.items[0].title, 'Detail source');
  assert.equal(workspace.items[0].manager, 'detail');
  assert.equal(workspace.loading, false);
  workspace.close();
});

test('a delayed mutation response from a previous owner cannot update the current workspace', async () => {
  const response = deferred();
  const { workspace, state } = await opened({ todos: [todo('t1')], request: async (call, fixture) => {
    if (call.method === 'GET') return { todos: fixture.todos };
    return response.promise;
  } });
  await workspace.openRow(workspace.items[0]);
  const write = workspace.setDone(workspace.selected, true);
  assert.equal(state.calls.at(-1).method, 'PATCH');
  state.scope = 'hub:other';
  state.generation++;
  response.resolve({ todo: todo('t1', { done_at: '2026-11-11T10:00:00Z' }) });
  assert.equal(await write, false);
  assert.equal(workspace.items[0].isDone(), false);
  assert.equal(state.calls.filter((call) => call.method === 'PATCH').length, 1);
  workspace.close();
});

test('a delayed draft restore from a previous owner cannot replace the new target draft', async () => {
  const restore = deferred();
  const { workspace, state } = await opened({ todos: [todo('t1')], viewLoad: async (key, fixture) => {
    if (key.startsWith('todo:draft:')) return restore.promise;
    return fixture.views.get(fixture.scope + ':' + key) || '';
  } });
  const opening = workspace.openRow(workspace.items[0]);
  state.scope = 'hub:other';
  state.generation++;
  restore.resolve(JSON.stringify({ title: 'stale owner title', notes: 'stale' }));
  await opening;
  assert.equal(workspace.titleDraft, 'Todo t1');
  assert.equal(workspace.notesDraft, '');
  workspace.close();
});

test('source conversation, chat thread, projects, and lineage preserve their owning identifiers', () => {
  const workspace = new TodoWorkspace();
  workspace.managerId = 'work';
  workspace.conversations = [ResourceRow.from({ id: 'origin-1', manager: 'work', title: 'Chat' }, 'conversation')];
  const item = new TodoWorkspaceItem(todo('t1', { origin: 'origin-1', project: 'persona',
    thread: { channel: 'lark', chat_id: 'chat-1', thread_id: 'thread-1', title: 'Build thread' },
    source: { channel: 'lark', chat_id: 'external-1', message_id: 'message-9', title: 'Source chat' },
    lineage: [{ todo_id: 't0', title: 'Earlier task', why: 'continued' }] }), 'work');
  assert.equal(workspace.sourceResource(item).kind, 'conversation');
  assert.equal(workspace.sourceResource(item).id, 'origin-1');
  assert.equal(workspace.threadResource(item).kind, 'todo_thread');
  assert.equal(workspace.threadResource(item).session, 'thread-1');
  assert.equal(workspace.projectResource(item).path, 'projects');
  assert.deepEqual(workspace.lineageResources(item).map((row) => [row.kind, row.id, row.detail]), [['todo', 't0', 'continued']]);
  const sourced = new TodoWorkspaceItem(todo('t2', { source: { channel: 'lark', chat_id: 'external-1', message_id: 'message-9' } }), 'work');
  assert.equal(workspace.sourceResource(sourced).kind, 'todo_source');
  assert.equal(workspace.sourceResource(sourced).session, 'message-9');
});

test('native page exposes parent navigation, Markdown links, and Todo-aware Agent creation callbacks', () => {
  const page = readFileSync(new URL('../entry/src/main/ets/components/TodoWorkspacePage.ets', import.meta.url), 'utf8');
  assert.match(page, /@Param row: ResourceRow \| undefined/);
  assert.match(page, /@Param list: boolean/);
  assert.match(page, /@Param visible: boolean/);
  assert.match(page, /@Event onOpen: \(row: ResourceRow\) => void/);
  assert.match(page, /@Event onOpenResource: \(row: ResourceRow\) => void/);
  assert.match(page, /@Event onLink: \(address: string\) => void/);
  assert.match(page, /@Event onCreateAgent: \(todo: ResourceRow\) => void/);
  assert.match(page, /@Event onTitle: \(title: string\) => void/);
  assert.match(page, /@Monitor\('workspace\.selected'\) selectedChanged\(\): void/);
  assert.match(page, /this\.onTitle\(selected\.title\)/);
  assert.match(page, /this\.onCreateAgent\(this\.workspace\.todoResource\(this\.workspace\.selected\)\)/);
  assert.match(page, /row\.id !== selected\.id/);
  assert.match(page, /@Monitor\('visible'\) visibilityChanged\(\): void/);
  assert.match(page, /if \(becameVisible && this\.pageAvailable\) \{\s*this\.publishTitle\(\);\s*this\.refreshWhenReady\(\);/);
  assert.match(page, /if \(this\.model\.connected\) this\.refreshWhenReady\(true\)/);
  assert.match(page, /@Monitor\('workspace\.selected'\) selectedChanged\(\): void/);
  assert.match(page, /@Monitor\('workspace\.items', 'workspace\.search', 'workspace\.statusFilter', 'workspace\.groupBy',[\s\S]*'workspace\.projects', 'workspace\.sessions', 'workspace\.machines'/);
  assert.match(page, /!this\.workspace\.targetGone/);
});


test('Todo Agent association excludes Persona and Health assistant conversations',async()=>{
 const {workspace}=await opened({todos:[todo('t1')],sessions:[session('agent','idle'),session('main','idle',{assistant:true}),session('health','idle',{assistant:true,space:'health'})],machines:[machine()]});
 assert.deepEqual(workspace.candidateSessions().map(row=>row.id),['agent']);workspace.close();
});


test('a Todo detail restored before the first connection binds after the owner becomes available',async()=>{
 const harness=makeHarness({todos:[todo('t1')]}),workspace=new TodoWorkspace(),row=new ResourceRow('t1','Task','todo');row.manager='work';harness.state.connected=false;
 await workspace.open(harness.client,row,false);assert.equal(workspace.error,'offline');harness.state.connected=true;harness.state.generation++;await workspace.refresh();assert.equal(workspace.error,'');assert.equal(workspace.selected.id,'t1');workspace.close();
});

test('a same-owner reconnect rebinds reads while retaining and saving an offline Todo editor draft',async()=>{
 const {workspace,client,state}=await opened({todos:[todo('t1')]});await workspace.openRow(workspace.items[0]);workspace.editorOpen=true;state.connected=false;state.generation++;workspace.setTitleDraft('Offline draft');workspace.setNotesDraft('Keep after reconnect');await workspace.draftWrites;
 assert.ok([...state.views.values()].some(value=>value.includes('Offline draft')));state.connected=true;await workspace.refresh();assert.equal(workspace.error,'');assert.equal(workspace.editorOpen,true);assert.equal(workspace.titleDraft,'Offline draft');assert.equal(workspace.notesDraft,'Keep after reconnect');workspace.close();
});
