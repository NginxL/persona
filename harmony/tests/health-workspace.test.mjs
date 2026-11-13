import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/HealthWorkspace.ets') && specifier === './Wire')
      return { url: new URL('./Wire.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { HealthWorkspace, HealthMemoryFile } = await import('../entry/src/main/ets/models/HealthWorkspace.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');

function clone(value) { return structuredClone(value); }

function deferred() {
  let resolve;
  const promise = new Promise((yes) => { resolve = yes; });
  return { promise, resolve };
}

function failure(status, code, resultUnknown = false, resultPending = false) {
  const error = new Error(code);
  error.status = status; error.resultUnknown = resultUnknown; error.resultPending = resultPending;
  return error;
}

function chat(id, space = 'health', fields = {}) {
  return { id, session_id: id, machine_id: 'health-machine', title: 'Health ' + id, cwd: '/home/user/.persona/health',
    space, last_text: 'A private health question', ...fields };
}

function fixture(options = {}) {
  const shared = { calls: [], views: new Map(), defaultManager: options.defaultManager || 'health-owner', managers: options.managers || ['health-owner'],
    request: options.request, childClients: new Map(), rootState: undefined };
  const roster = () => ({ default: shared.defaultManager, managers: shared.managers.map((value) => {
    const id = typeof value === 'string' ? value : value.id;
    return { id, name: id, online: true, default: id === shared.defaultManager, ...(typeof value === 'string' ? {} : value) };
  }) });

  function clientState(manager, scope) {
    return { manager, scope, generation: 5, connected: true, machine: 'health-machine', calls: [], blocked: new Set(),
      conversations: options.conversations || [chat('health-1'), chat('main-leak', 'main')],
      memories: options.memories || [
        { name: 'MEMORY.md', content: '- [Sleep](sleep.md) — sleep goal', updated_at: '2026-11-10T10:00:00Z' },
        { name: 'sleep.md', content: '# Sleep\nAim for seven hours.', updated_at: '2026-11-10T11:00:00Z' }
      ],
      reports: options.reports || [{ name: 'checkup.pdf', title: 'Annual Checkup', path: '/health/reports/checkup.pdf',
        record_path: '/health/reports/checkup.pdf.md', size: 2048, modified_at: '2026-11-11T10:00:00Z' }],
      machineTrust: options.machineTrust || 'personal',
      machineOnline: options.machineOnline !== false,
      beforeUploadAdmit: options.beforeUploadAdmit,
      beforeCallAdmit: options.beforeCallAdmit,
      onCall: options.childOnCall
    };
  }

  function makeClient(state, isRoot = false) {
    const repo = {
      async call(path, method = 'GET', body = {}, op = '', admit) {
        if (method !== 'GET') {
          if (state.beforeCallAdmit) await state.beforeCallAdmit(state, path);
          if (admit && !admit()) throw failure(0, 'request_updated');
        }
        const call = { path, method, body: clone(body), op };
        shared.calls.push(call); state.calls.push(call);
        if (isRoot && path === '/api/v1/managers') return clone(roster());
        if (state.onCall) {
          try {
            const result = await state.onCall(call, state, shared);
            if (result !== undefined) return clone(result);
          } catch (error) {
            if (error.resultUnknown || error.resultPending) state.blocked.add(path);
            throw error;
          }
        }
        if (path === '/api/v1/assistant?space=health' && method === 'GET')
          return { machine_id: state.machine, conversations: clone(state.conversations) };
        if (path === '/api/v1/machines' && method === 'GET')
          return { machines: [{ id: state.machine, manager: state.manager, online: state.machineOnline, trust: state.machineTrust }] };
        if (path === '/api/v1/assistant/memory?space=health' && method === 'GET') return { files: clone(state.memories) };
        if (path.startsWith('/api/v1/machines/') && path.endsWith('/health/reports') && method === 'GET')
          return { reports: clone(state.reports) };
        if (path === '/api/v1/assistant/conversations' && method === 'POST') {
          const session = chat('created-' + (state.calls.filter((item) => item.method === 'POST' && item.path === path).length), 'health', {
            title: body.prompt, session_id: 'created-1'
          });
          state.conversations.unshift(session); return { session };
        }
        if (path.startsWith('/api/v1/assistant/conversations/') && path.endsWith('/archive') && method === 'POST') {
          const id = decodeURIComponent(path.split('/')[5]); state.conversations = state.conversations.filter((item) => item.id !== id);
          return { ok: true, archived: true };
        }
        if (path.startsWith('/api/v1/assistant/memory/') && method === 'PUT') {
          const name = decodeURIComponent(path.split('/')[5].split('?')[0]);
          const file = state.memories.find((item) => item.name === name);
          if (file) file.content = body.content;
          return { ok: true };
        }
        if (path.startsWith('/api/v1/assistant/memory/') && method === 'DELETE') {
          const name = decodeURIComponent(path.split('/')[5].split('?')[0]);
          state.memories = state.memories.filter((item) => item.name !== name); return { ok: true };
        }
        if (path.includes('/health/reports/') && method === 'DELETE') {
          const name = decodeURIComponent(path.split('/').at(-1)); state.reports = state.reports.filter((item) => item.name !== name);
          return { ok: true };
        }
        return { ok: true };
      }
    };
    repo.upload = async (path, bytes, admit) => {
      if (state.beforeUploadAdmit) await state.beforeUploadAdmit(state);
      if (admit && !admit()) throw failure(0, 'request_updated');
      const call = { path, method: 'UPLOAD', bytes: bytes.byteLength };
      shared.calls.push(call); state.calls.push(call);
      if (state.onCall) {
        try {
          const result = await state.onCall(call, state, shared);
          if (result !== undefined) return clone(result);
        } catch (error) {
          if (error.resultUnknown || error.resultPending) state.blocked.add(path);
          throw error;
        }
      }
      const name = decodeURIComponent(path.split('name=')[1].split('&')[0]);
      const extension = name.includes('.') ? name.slice(name.lastIndexOf('.')) : '';
      const stem = extension ? name.slice(0, -extension.length) : name;
      const remotePath = '/home/' + state.manager + '/.persona/health/reports/' + stem + '-20261111-140000' + extension;
      return { path: remotePath, name: remotePath.split('/').at(-1), size: bytes.byteLength };
    };
    const client = {
      connected: true, demo: false, profile: { id: 'hub-profile', manager: state.manager }, features: [],
      repo() { return repo; }, scope() { return state.scope; }, connectionGeneration() { return state.generation; },
      owns(generation, scope) { return state.connected && generation === state.generation && scope === state.scope; },
      operationBlocked(path) { return state.blocked.has(path); },
      async loadView(key) { return shared.views.get(state.scope + ':' + key) || ''; },
      viewRevision(){return state.epoch||0;},
      async saveView(key, value) { shared.views.set(state.scope + ':' + key, value); },
      async viewFor(row) {
        const next = shared.childClients.get(row.manager);
        if (!next) throw new Error('manager_unavailable');
        return next.client;
      }
    };
    state.repo = repo; state.client = client;
    return { client, state, repo };
  }

  shared.rootState = clientState(options.rootManager || 'work', 'hub:work');
  const root = makeClient(shared.rootState, true);
  for (const value of shared.managers) {
    const id = typeof value === 'string' ? value : value.id;
    const state = clientState(id, 'hub:' + id);
    const child = makeClient(state, false);
    shared.childClients.set(id, child);
  }
  return { root: root.client, rootState: root.state, shared, child: (id = shared.defaultManager) => shared.childClients.get(id) };
}

async function opened(options = {}) {
  const env = fixture(options), workspace = new HealthWorkspace();
  await workspace.open(env.root);
  return { ...env, workspace };
}

function pathMethod(env, method, prefix) {
  return env.shared.calls.filter((call) => call.method === method && call.path.startsWith(prefix));
}

test('Health resolves the roster default and keeps only health-space conversations from the health endpoint', async () => {
  const { workspace, root, rootState, shared, child } = await opened();
  assert.equal(root.profile.manager, 'work', 'opening Health does not change the selected manager');
  assert.equal(workspace.canStartConversation(), true);
  assert.equal(workspace.machine, 'health-machine');
  assert.deepEqual(workspace.conversations.map((row) => row.id), ['health-1']);
  assert.equal(workspace.conversations[0].manager, 'health-owner');
  assert.equal(workspace.conversations[0].data().space, 'health');
  assert.deepEqual(workspace.memoryEntries().map((file) => file.name), ['sleep.md']);
  assert.equal(workspace.indexMemory().name, 'MEMORY.md');
  assert.deepEqual(rootState.calls.map((call) => call.path), ['/api/v1/managers']);
  assert.ok(child().state.calls.some((call) => call.path === '/api/v1/assistant?space=health'));
  assert.ok(child().state.calls.some((call) => call.path === '/api/v1/assistant/memory?space=health'));
  assert.ok(child().state.calls.some((call) => call.path === '/api/v1/machines/health-machine/health/reports'));
  assert.equal(shared.calls.some((call) => call.path === '/api/v1/assistant'), false);
});

test('creating a Health chat uses the exact health space body and returns a default-manager route', async () => {
  const { workspace, child } = await opened();
  workspace.setDraft('How did I sleep this week?');
  const row = await workspace.startConversation();
  assert.deepEqual(pathMethod({ shared: { calls: child().state.calls } }, 'POST', '/api/v1/assistant/conversations').map((call) => [call.path, call.body]), [
    ['/api/v1/assistant/conversations', { prompt: 'How did I sleep this week?', space: 'health' }]
  ]);
  assert.ok(row);
  assert.equal(row.manager, 'health-owner');
  assert.equal(row.machine, 'health-machine');
  assert.equal(row.session, 'created-1');
  assert.equal(workspace.isHealthConversation(row), true);
  assert.equal(workspace.draft, '');
  assert.ok(child().state.calls.find((call) => call.method === 'POST').op);
});

test('memory index is read only; memory edits and deletion retain the health query and local draft scope', async () => {
  const { workspace, child, shared } = await opened();
  const index = workspace.indexMemory();
  workspace.openMemory(index);
  assert.equal(workspace.canEditMemory(), false);
  assert.equal(await workspace.deleteMemory(), false);
  assert.equal(pathMethod({ shared: { calls: child().state.calls } }, 'DELETE', '/api/v1/assistant/memory/').length, 0);

  const file = workspace.memoryEntries()[0];
  workspace.openMemory(file);
  await new Promise((resolve) => setImmediate(resolve));
  workspace.setMemoryDraft('# Sleep\nTry 8 hours.');
  await workspace.memoryWrite;
  const draftKey = 'hub:health-owner:health-memory-draft:["health-owner","sleep.md"]';
  assert.equal(JSON.parse(shared.views.get(draftKey)).content, '# Sleep\nTry 8 hours.');
  assert.equal(await workspace.saveMemory(), true);
  assert.deepEqual(pathMethod({ shared: { calls: child().state.calls } }, 'PUT', '/api/v1/assistant/memory/').map((call) => [call.path, call.body]), [
    ['/api/v1/assistant/memory/sleep.md?space=health', { content: '# Sleep\nTry 8 hours.' }]
  ]);
  workspace.openMemory(workspace.memoryEntries()[0]);
  assert.equal(await workspace.deleteMemory(), true);
  assert.deepEqual(pathMethod({ shared: { calls: child().state.calls } }, 'DELETE', '/api/v1/assistant/memory/').map((call) => call.path), [
    '/api/v1/assistant/memory/sleep.md?space=health'
  ]);
});

test('archive accepts only a listed Health conversation owned by the roster default', async () => {
  const { workspace, child } = await opened();
  const healthRow = workspace.conversations[0];
  const mainRow = ResourceRow.from(chat('other', 'main'), 'conversation'); mainRow.manager = 'health-owner'; mainRow.machine = 'health-machine';
  const foreign = ResourceRow.from(chat('foreign', 'health'), 'conversation'); foreign.manager = 'work'; foreign.machine = 'health-machine';
  assert.equal(await workspace.archive(mainRow), false);
  assert.equal(await workspace.archive(foreign), false);
  assert.equal(await workspace.archive(healthRow), true);
  assert.deepEqual(pathMethod({ shared: { calls: child().state.calls } }, 'POST', '/api/v1/assistant/conversations/').map((call) => call.path), [
    '/api/v1/assistant/conversations/health-1/archive'
  ]);
  assert.equal(workspace.canArchiveConversation(healthRow), false);
});

test('opening and deleting a report preserve its manager, machine, record and exact delete target', async () => {
  const { workspace, child } = await opened();
  const report = workspace.reports[0];
  assert.equal(workspace.openReport(report, true).path, '/health/reports/checkup.pdf.md');
  assert.equal(workspace.openReport(report, false).path, '/health/reports/checkup.pdf');
  assert.equal(workspace.openReport(report, true).manager, 'health-owner');
  assert.equal(workspace.openReport(report, true).machine, 'health-machine');
  assert.equal(await workspace.deleteReport(report), true);
  assert.deepEqual(pathMethod({ shared: { calls: child().state.calls } }, 'DELETE', '/api/v1/machines/').map((call) => call.path), [
    '/api/v1/machines/health-machine/health/reports/checkup.pdf'
  ]);
});

test('report upload targets the trusted default manager machine and returns the health reports path', async () => {
  const { workspace, shared, child } = await opened();
  assert.equal(workspace.canUploadReports(), true);
  const path = await workspace.uploadReport('checkup.pdf', new ArrayBuffer(12));
  assert.equal(path, '/home/health-owner/.persona/health/reports/checkup-20261111-140000.pdf');
  assert.deepEqual(shared.calls.filter((call) => call.method === 'UPLOAD').map((call) => [call.path, call.bytes]), [
    ['/api/v1/machines/health-machine/files?name=checkup.pdf&dest=health', 12]
  ]);
  assert.equal(workspace.appendReportPath(path, 'Review this checkup report item by item.'), true);
  await workspace.draftWrite;
  assert.equal(workspace.draft, 'Review this checkup report item by item.\n' + path);
  assert.equal(shared.calls.some((call) => call.method === 'POST' && call.path === '/api/v1/assistant/conversations'), false);
  assert.ok(child().state.calls.some((call) => call.path === '/api/v1/machines' && call.method === 'GET'));
  assert.equal(workspace.reportUploadState, 'done');
});

test('a retired owner is rejected after durable upload admission and before sending bytes', async () => {
  const { workspace, shared } = await opened({ beforeUploadAdmit: async (state) => { state.generation++; } });
  assert.equal(await workspace.uploadReport('checkup.pdf', new ArrayBuffer(12)), undefined);
  assert.equal(shared.calls.filter((call) => call.method === 'UPLOAD').length, 0);
});

test('a changed roster default cannot receive a report from the previous Health owner', async () => {
  const { workspace, shared } = await opened({ managers: ['health-owner', 'next-owner'] });
  shared.defaultManager = 'next-owner';
  assert.equal(await workspace.uploadReport('checkup.pdf', new ArrayBuffer(12)), undefined);
  assert.equal(shared.calls.filter((call) => call.method === 'UPLOAD').length, 0);
});

test('an unknown report upload cannot be retried for the same exact file target', async () => {
  const { workspace, shared } = await opened({ childOnCall: async (call) => {
    if (call.method === 'UPLOAD') throw failure(0, 'result_unknown', true);
    return undefined;
  } });
  assert.equal(await workspace.uploadReport('checkup.pdf', new ArrayBuffer(12)), undefined);
  assert.equal(workspace.reportUploadState, 'unknown');
  assert.equal(await workspace.uploadReport('checkup.pdf', new ArrayBuffer(12)), undefined);
  assert.equal(shared.calls.filter((call) => call.method === 'UPLOAD').length, 1);
});

test('report uploads stay unavailable when the default assistant machine is not trusted', async () => {
  for (const options of [{ machineTrust: 'unknown' }, { machineOnline: false }]) {
    const { workspace, shared } = await opened(options);
    assert.equal(workspace.canUploadReports(), false);
    assert.equal(workspace.canStartConversation(), false);
    assert.equal(await workspace.startConversation('An unsafe target?'), undefined);
    assert.equal(await workspace.uploadReport('checkup.pdf', new ArrayBuffer(12)), undefined);
    assert.equal(workspace.error, 'health_default_manager_unavailable');
    assert.equal(shared.calls.filter((call) => call.method === 'UPLOAD').length, 0);
  }
});

test('pending and unknown conversation writes are durable and cannot be sent twice', async () => {
  for (const resultFailure of [failure(202, 'result_unknown', true, true), failure(0, 'result_unknown', true, false)]) {
    const { workspace, child } = await opened({ childOnCall: async (call, state) => {
      if (call.method === 'POST' && call.path === '/api/v1/assistant/conversations') throw resultFailure;
      return undefined;
    } });
    workspace.setDraft('Log my weight today: 62.5 kg');
    await workspace.draftWrite;
    assert.equal(await workspace.startConversation(), undefined);
    const firstWrites = pathMethod({ shared: { calls: child().state.calls } }, 'POST', '/api/v1/assistant/conversations').length;
    assert.equal(firstWrites, 1);
    assert.ok(['pending', 'unknown'].includes(workspace.conversationWriteState));
    await workspace.open(workspace.client);
    assert.ok(['pending', 'unknown'].includes(workspace.conversationWriteState));
    assert.equal(await workspace.startConversation(), undefined);
    assert.equal(pathMethod({ shared: { calls: child().state.calls } }, 'POST', '/api/v1/assistant/conversations').length, 1);
  }
});

test('an absent or offline roster default reports Health unavailable without querying another space', async () => {
  const env = fixture();
  env.shared.defaultManager = '';
  const workspace = new HealthWorkspace();
  await workspace.open(env.root);
  assert.equal(workspace.error, 'health_default_manager_unavailable');
  assert.equal(env.shared.calls.some((call) => call.path === '/api/v1/assistant?space=health'), false);

  const offline = fixture({ managers: ['health-owner'] });
  offline.shared.managers = [{ id: 'health-owner', online: false }];
  offline.shared.defaultManager = 'health-owner';
  const blocked = new HealthWorkspace();
  await blocked.open(offline.root);
  assert.equal(blocked.error, 'health_default_manager_unavailable');
  assert.equal(offline.shared.calls.some((call) => call.path === '/api/v1/assistant?space=health'), false);
});

test('a changed default manager refuses writes until refresh binds Health to the new owner', async () => {
  const { workspace, shared, child } = await opened({ managers: ['health-owner', 'next-owner'] });
  shared.defaultManager = 'next-owner';
  workspace.setDraft('A question');
  assert.equal(await workspace.startConversation(), undefined);
  assert.equal(pathMethod({ shared: { calls: child('health-owner').state.calls } }, 'POST', '/api/v1/assistant/conversations').length, 0);
  await workspace.refresh();
  assert.equal(workspace.conversations[0].manager, 'next-owner');
  const row = await workspace.startConversation('A question');
  assert.equal(row.manager, 'next-owner');
  assert.equal(pathMethod({ shared: { calls: child('next-owner').state.calls } }, 'POST', '/api/v1/assistant/conversations').length, 1);
});

test('a delayed response from a retired health connection cannot change state, and the persisted intent blocks a duplicate', async () => {
  const response = deferred(), entered = deferred();
  const { workspace, child } = await opened({ childOnCall: async (call) => {
    if (call.method === 'POST' && call.path === '/api/v1/assistant/conversations') { entered.resolve(); return response.promise; }
    return undefined;
  } });
  workspace.setDraft('Will this remain private?'); await workspace.draftWrite;
  const pending = workspace.startConversation(); await entered.promise;
  const state = child().state, oldGeneration = state.generation;
  state.generation++;
  response.resolve({ session: chat('late', 'health') });
  assert.equal(await pending, undefined);
  assert.equal(workspace.conversations.some((row) => row.id === 'late'), false);
  assert.notEqual(state.generation, oldGeneration);
  assert.equal(workspace.conversationWriteState, 'sending');
  await workspace.open(workspace.client);
  assert.equal(workspace.conversationWriteState, 'unknown');
  assert.equal(await workspace.startConversation(), undefined);
  assert.equal(pathMethod({ shared: { calls: state.calls } }, 'POST', '/api/v1/assistant/conversations').length, 1);
});


test('Health local chat and memory drafts persist while the same owner is offline',async()=>{
 const {workspace,shared}=await opened(),child=shared.childClients.get('health-owner');
 await workspace.openMemory(workspace.memories.find(file=>file.name==='sleep.md'));child.client.connected=false;child.state.generation++;
 workspace.setDraft('Offline private health draft');workspace.setMemoryDraft('Offline sleep notes');await workspace.draftWrite;await workspace.memoryWrite;
 assert.ok([...shared.views.values()].some(raw=>raw.includes('Offline private health draft')));assert.ok([...shared.views.values()].some(raw=>raw.includes('Offline sleep notes')));
});
