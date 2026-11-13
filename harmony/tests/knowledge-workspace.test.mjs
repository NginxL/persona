import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/KnowledgeWorkspace.ets') && specifier === './Wire')
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

const { KnowledgeWorkspace } = await import('../entry/src/main/ets/models/KnowledgeWorkspace.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');

function clone(value) { return structuredClone(value); }

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function flush() { return new Promise((resolve) => setTimeout(resolve, 0)); }

function failure(status, code, resultUnknown = false, resultPending = false) {
  const error = new Error(code);
  error.status = status; error.resultUnknown = resultUnknown; error.resultPending = resultPending;
  return error;
}

function machine(id, manager = 'owner', trust = 'personal', online = true) {
  return ResourceRow.from({ id, manager, name: id, trust, online }, 'machine');
}

function entry(kind, id, manager = 'work', fields = {}) {
  const row = ResourceRow.from({ id, kind, name: id, manager, one_line: 'A short description', ...fields }, 'knowledge');
  row.path = kind; row.manager = manager;
  return row;
}

function fixture(options = {}) {
  const shared = { calls: [], views: new Map(), request: options.request, handoffs: [], createdAgents: [], blocked: new Set() };
  const owners = new Map();

  function childState(manager) {
    return { manager, scope: 'hub:' + manager, generation: 4, connected: true, blocked: shared.blocked,
      machines: [machine('personal-box', manager), machine('untrusted-box', manager, 'pending'), machine('offline-box', manager, 'personal', false)],
      request: options.request, updatedMetadata: undefined, lastMinutes: undefined };
  }

  function detailData(kind, id) {
    const base = { id, kind, meta: { name: id, importance: 'P2', relevance: 25, tags: ['old'] },
      summary: '# ' + id + '\nA short description.\n\nMore context.', log: ['Earlier event', 'Latest event'],
      links: [{ id: 'related-doc', kind: 'docs', title: 'Related document' }], path: '/persona/' + kind + '/' + id, recent: 'Recent messages.' };
    if (kind === 'projects') {
      base.meta = { ...base.meta, kind: 'short', status: 'active', dirs: ['personal-box:/repo', 'untrusted-box:/unsafe'] };
      base.moments = [{ id: 'moment-1', author: 'A coworker', text: 'Decision moved forward' }];
    }
    if (kind === 'recordings') {
      base.segments = [{ text: 'Transcript segment' }];
      base.minutes = { version: 7, markdown: 'Minutes', actions: [{ id: 'todo-1', what: 'Follow up' }] };
    }
    return base;
  }

  function repoFor(state) {
    return {
      async call(path, method = 'GET', body = {}, op = '') {
        const call = { path, method, body: clone(body), op, manager: state.manager };
        shared.calls.push(call);
        try {
          if (options.request) {
            const result = await options.request(call, state, shared);
            if (result !== undefined) return clone(result);
          }
        } catch (error) {
          if (error.resultUnknown || error.resultPending) shared.blocked.add(path);
          throw error;
        }
        if (method === 'GET' && path.startsWith('/api/v1/kb/moments/')) return { markdown: '# Moment\nDetails and comments.' };
        if (method === 'GET' && path.startsWith('/api/v1/kb/')) {
          const rest = path.slice('/api/v1/kb/'.length).split('?')[0].split('/');
          if (rest.length === 1) return { items: [{ id: 'project-1', kind: rest[0], name: 'Project One', manager: state.manager, one_line: 'A short description.' }], tags: ['focus', 'later'] };
          const [, id] = rest;
          const q = new URLSearchParams(path.split('?')[1] || '').get('q');
          const detail = detailData(rest[0], decodeURIComponent(id));
          return { detail, matches: q ? [{ line: 'Found “' + q + '” in this entry', day: '2026-11-10' }] : [],
            agents: [{ id: 'agent-1', session_id: 'agent-1', machine_id: 'personal-box', title: 'Build project', state: 'working' }] };
        }
        if (method === 'GET' && path.startsWith('/api/v1/kb-search?')) {
          const q = new URLSearchParams(path.split('?')[1] || '').get('q') || '';
          return { results: [{ count: 2, in_name: true,
            entry: { id: 'project-1', kind: 'projects', title: 'Project One', manager: state.manager },
            hits: [{ line: 'Found ' + q }, { line: 'A second match' }] }] };
        }
        if (method === 'PUT' && path.startsWith('/api/v1/kb/')) { state.updatedMetadata = clone(body); return { ok: true }; }
        if (method === 'POST' && path.startsWith('/api/v1/recordings/')) { state.lastMinutes = { path, body: clone(body) }; return { ok: true }; }
        if (method === 'DELETE' && path.startsWith('/api/v1/recordings/')) return { ok: true };
        return { ok: true };
      }
    };
  }

  function makeClient(manager, rootState) {
    const state = rootState || childState(manager);
    const repo = repoFor(state);
    const client = {
      connected: true, demo: false, profile: { id: 'hub-profile', manager }, machines: state.machines,
      repo() { return repo; }, scope() { return state.scope; }, connectionGeneration() { return state.generation; },
      owns(generation, scope) { return state.connected && generation === state.generation && scope === state.scope; },
      operationBlocked(path) { return shared.blocked.has(path); },
      async loadView(key) { return shared.views.get(state.scope + ':' + key) || ''; },
      viewRevision(){return state.epoch||0;},
      async saveView(key, value) { shared.views.set(state.scope + ':' + key, value); },
      async viewFor(row) { return owners.get(row.manager)?.client || client; },
      async handoff(row) {
        shared.handoffs.push(row.key());
        const target = new ResourceRow('main-' + manager, 'Main chat', 'conversation'); target.manager = manager; return target;
      },
      async createAgent(prompt, targetMachine, directory, launcher) {
        shared.createdAgents.push({ prompt, machine: targetMachine.id, directory, launcher, manager });
        const result = new ResourceRow('agent-created', 'Started agent', 'session');
        result.manager = manager; result.machine = targetMachine.id; result.session = result.id; return result;
      }
    };
    state.repo = repo; state.client = client;
    owners.set(manager, { client, state });
    return { client, state, repo };
  }

  const rootState = childState('work');
  const root = makeClient('work', rootState);
  const owner = makeClient('owner');
  return { client: root.client, rootState: root.state, owner, shared };
}

async function opened(options = {}, row) {
  const env = fixture(options), workspace = new KnowledgeWorkspace();
  await workspace.open(env.client, row);
  return { ...env, workspace };
}

function calls(env, method, prefix) {
  return env.shared.calls.filter((call) => call.method === method && call.path.startsWith(prefix));
}

test('Knowledge list, tags and global search use the existing routes and retain the owning manager', async () => {
  const { workspace, client, shared } = await opened();
  assert.equal(workspace.entries[0].id, 'project-1');
  assert.equal(workspace.entries[0].manager, 'work');
  assert.deepEqual(workspace.tags, ['focus', 'later']);
  assert.equal(shared.calls[0].path, '/api/v1/kb/projects');

  workspace.setTag('focus');
  await workspace.refresh();
  assert.ok(shared.calls.some((call) => call.path === '/api/v1/kb/projects?tag=focus'));
  workspace.kind = 'projects'; workspace.tag = ''; workspace.query = 'calendar';
  await workspace.refresh();
  assert.ok(shared.calls.some((call) => call.path === '/api/v1/kb-search?q=calendar&results=1'));
  assert.equal(workspace.results[0].entry.manager, client.profile.manager);
  assert.deepEqual(workspace.results[0].hits, ['Found calendar', 'A second match']);
});

test('detail reads preserve owner, query matches, timeline, related entries and Moments route', async () => {
  const row = entry('projects', 'a/b', 'owner', { opened_from_query: 'calendar' });
  const { workspace, shared, owner } = await opened({}, row);
  assert.equal(workspace.selected.manager, 'owner');
  assert.equal(workspace.matches[0].line, 'Found “calendar” in this entry');
  assert.equal(workspace.agents[0].manager, 'owner');
  assert.ok(shared.calls.some((call) => call.manager === 'owner' && call.path === '/api/v1/kb/projects/a%2Fb?q=calendar'));
  assert.equal(await workspace.openMoment('moment-1'), '# Moment\nDetails and comments.');
  assert.ok(owner.state.repo && shared.calls.some((call) => call.path === '/api/v1/kb/moments/moment-1'));
});

test('metadata editing is available to every Knowledge kind and sends only allowed fields', async () => {
  const row = entry('people', 'person-1', 'owner');
  const { workspace, owner, shared } = await opened({}, row);
  workspace.beginEdit(); workspace.setEditImportance('P1'); workspace.setEditRelevance('55'); workspace.setEditTags('#family, doctor');
  await workspace.draftWrite;
  const editorKey = 'hub:owner:knowledge-draft:["owner","people","person-1"]';
  assert.equal(JSON.parse(shared.views.get(editorKey)).importance, 'P1');
  assert.equal(await workspace.saveEdit(), true);
  assert.deepEqual(owner.state.updatedMetadata, { importance: 'P1', relevance: 55, tags: ['family', 'doctor'] });
  assert.ok(shared.calls.some((call) => call.path === '/api/v1/kb/people/person-1' && call.method === 'PUT'));
});

test('project metadata includes project-only status and kind, and agent drafts remain owner scoped', async () => {
  const row = entry('projects', 'project-1', 'owner');
  const { workspace, owner, shared } = await opened({}, row);
  workspace.beginEdit(); workspace.setEditImportance('P0'); workspace.setEditRelevance('88'); workspace.setEditTags('focus, mobile');
  workspace.setEditProjectKind('long'); workspace.setEditStatus('paused');
  workspace.setAgentDraft('Review the latest plan'); workspace.setAgentLauncher('claude');
  await workspace.draftWrite;
  const editorKey = 'hub:owner:knowledge-draft:["owner","projects","project-1"]';
  assert.deepEqual(JSON.parse(shared.views.get(editorKey)), { importance: 'P0', relevance: '88', tags: 'focus, mobile',
    project_kind: 'long', status: 'paused', agent_prompt: 'Review the latest plan', agent_launcher: 'claude', metadata_dirty: true });
  assert.equal(await workspace.saveEdit(), true);
  assert.deepEqual(owner.state.updatedMetadata, { importance: 'P0', relevance: 88, tags: ['focus', 'mobile'], status: 'paused', project_kind: 'long' });
});

test('Persona handoff stores a structured manager-owned reference and dispatch uses only trusted online directories', async () => {
  const row = entry('projects', 'project-1', 'owner');
  const { workspace, owner, shared } = await opened({}, row);
  assert.deepEqual(workspace.directoryTargets.map((target) => [target.machine, target.path]), [['personal-box', '/repo']]);
  const target = await workspace.askPersona();
  assert.equal(target.manager, 'owner');
  assert.deepEqual(shared.handoffs, [row.key()]);
  assert.equal(shared.calls.some((call) => call.path.includes('/messages')), false);

  workspace.setAgentDraft('Inspect this project'); workspace.setAgentLauncher('claude');
  const created = await workspace.dispatchAgent(workspace.machineForDirectory(workspace.targetMachines), workspace.agentDraft, workspace.agentLauncher);
  assert.equal(created.manager, 'owner');
  assert.equal(shared.createdAgents.length, 1);
  assert.deepEqual([shared.createdAgents[0].machine, shared.createdAgents[0].directory, shared.createdAgents[0].launcher], ['personal-box', '/repo', 'claude']);
  assert.ok(shared.createdAgents[0].prompt.startsWith('<persona-context>'));
  assert.equal(workspace.canUseMachine(machine('untrusted-box', 'owner', 'pending')), false);
  assert.equal(workspace.canUseMachine(machine('offline-box', 'owner', 'personal', false)), false);
  assert.ok(shared.calls.some(call => call.manager === 'owner'));
});

test('recording minutes, todos and forgetting use the exact recording endpoints and payloads', async () => {
  const row = entry('recordings', 'recording-1', 'owner');
  const { workspace, owner, shared } = await opened({}, row);
  assert.equal(workspace.detail.segments[0].text, 'Transcript segment');
  assert.equal(workspace.detail.minutes.version, 7);
  assert.equal(await workspace.makeMinutes(), true);
  assert.equal(await workspace.addMinutesTodos(['todo-1', 'todo-2']), true);
  assert.deepEqual(owner.state.lastMinutes, { path: '/api/v1/recordings/recording-1/minutes/todos', body: { version: 7, actions: ['todo-1', 'todo-2'] } });
  assert.equal(await workspace.deleteRecording(), true);
  assert.deepEqual(calls({ shared }, 'POST', '/api/v1/recordings/').map((call) => [call.path, call.body]), [
    ['/api/v1/recordings/recording-1/minutes', {}],
    ['/api/v1/recordings/recording-1/minutes/todos', { version: 7, actions: ['todo-1', 'todo-2'] }]
  ]);
  assert.deepEqual(calls({ shared }, 'DELETE', '/api/v1/recordings/').map((call) => call.path), ['/api/v1/recordings/recording-1']);
});

test('unknown and pending metadata mutations block a second write on the same path', async () => {
  for (const writeFailure of [failure(202, 'result_unknown', true, true), failure(0, 'result_unknown', true, false)]) {
    const { workspace, shared } = await opened({ request: async (call) => {
      if (call.method === 'PUT' && call.path.startsWith('/api/v1/kb/')) throw writeFailure;
      return undefined;
    } }, entry('projects', 'project-1'));
    workspace.beginEdit(); workspace.setEditRelevance('30');
    assert.equal(await workspace.saveEdit(), false);
    assert.ok(['pending', 'unknown'].includes(workspace.writeState));
    assert.equal(await workspace.saveEdit(), false);
    assert.equal(calls({ shared }, 'PUT', '/api/v1/kb/projects/project-1').length, 1);
  }
});

test('late metadata responses from an old connection do not apply to the selected Knowledge entry', async () => {
  const response = deferred(), entered = deferred();
  const row = entry('projects', 'project-1', 'owner');
  const { workspace, owner, shared } = await opened({ request: async (call) => {
    if (call.method === 'PUT') { entered.resolve(); return response.promise; }
    return undefined;
  } }, row);
  workspace.beginEdit(); workspace.setEditStatus('done');
  const saving = workspace.saveEdit(); await entered.promise;
  owner.state.generation++;
  response.resolve({ ok: true });
  assert.equal(await saving, false);
  assert.equal(workspace.writeState, 'sending');
  assert.equal(calls({ shared }, 'PUT', '/api/v1/kb/projects/project-1').length, 1);
});


test('Knowledge editor drafts survive offline edits, reconnect refresh and reopening',async()=>{
 const row=entry('projects','project-1','owner'),{workspace,owner}=await opened({},row);workspace.beginEdit();owner.client.connected=false;owner.state.generation++;workspace.setEditTags('offline-tag');await workspace.draftWrite;owner.client.connected=true;
 await workspace.refresh();assert.equal(workspace.editTags,'offline-tag');workspace.hide();await workspace.open(owner.client,row);workspace.beginEdit();assert.equal(workspace.editTags,'offline-tag');
});

test('a successful metadata save cannot clear edits typed while its acknowledgement is pending',async()=>{
 const gate=deferred(),entered=deferred(),row=entry('projects','project-1','owner'),{workspace,shared}=await opened({request:async call=>{if(call.method==='PUT'){entered.resolve();await gate.promise;return{ok:true};}}},row);
 workspace.beginEdit();workspace.setEditTags('sent-tags');const saving=workspace.saveEdit();await entered.promise;workspace.setEditTags('newer-tags');gate.resolve();assert.equal(await saving,true);await workspace.draftWrite;
 assert.equal(workspace.editOpen,true);assert.equal(workspace.editTags,'newer-tags');assert.ok([...shared.views.values()].some(raw=>raw.includes('newer-tags')));
});

test('a child metadata save is reflected after the independent parent list refreshes', async () => {
  const staleList = deferred(), staleListEntered = deferred();
  let listReads = 0;
  const row = entry('projects', 'project-1', 'work'), env = fixture({ request: async (call, state) => {
    if (call.method === 'GET' && call.path === '/api/v1/kb/projects') {
      listReads++;
      if (listReads === 1) { staleListEntered.resolve(); return staleList.promise; }
      return { items: [{ id: 'project-1', kind: 'projects', name: state.updatedMetadata ? 'Project Updated' : 'Project One',
        manager: state.manager, tags: state.updatedMetadata?.tags || ['old'] }], tags: ['focus'] };
    }
    if (call.method === 'PUT' && call.path === '/api/v1/kb/projects/project-1') {
      state.updatedMetadata = clone(call.body); return { ok: true };
    }
    return undefined;
  } });
  const parent = new KnowledgeWorkspace(), child = new KnowledgeWorkspace();
  const openingParent = parent.open(env.client);
  await staleListEntered.promise;
  await child.open(env.client, row);
  child.beginEdit(); child.setEditTags('new-label');
  assert.equal(await child.saveEdit(), true);

  await parent.refresh();
  assert.equal(parent.entries[0].title, 'Project Updated');
  assert.deepEqual(parent.entries[0].data().tags, ['new-label']);
  staleList.resolve({ items: [{ id: 'project-1', kind: 'projects', name: 'Stale Project', manager: 'work', tags: ['old'] }], tags: ['old'] });
  await openingParent;
  assert.equal(parent.entries[0].title, 'Project Updated');
  assert.deepEqual(parent.entries[0].data().tags, ['new-label']);
});

test('a child recording delete disappears after the independent parent list refreshes', async () => {
  let deleted = false;
  const listRequested = deferred(), row = entry('recordings', 'recording-1', 'work');
  const env = await opened({ request: async (call, state) => {
    if (call.method === 'GET' && call.path === '/api/v1/kb/recordings') {
      listRequested.resolve();
      return { items: deleted ? [] : [{ id: 'recording-1', kind: 'recordings', name: 'Recording One', manager: state.manager }], tags: [] };
    }
    if (call.method === 'DELETE' && call.path === '/api/v1/recordings/recording-1') { deleted = true; return { ok: true }; }
    return undefined;
  } });
  const parent = env.workspace;
  parent.setKind('recordings'); await listRequested.promise; await parent.refresh();
  assert.equal(parent.entries.length, 1);
  const child = new KnowledgeWorkspace(); await child.open(env.client, row);
  assert.equal(await child.deleteRecording(), true);

  await parent.refresh();
  assert.equal(parent.entries.length, 0);
});

test('an acknowledged recording delete invalidates a detail read already in flight', async () => {
  const oldDetail = deferred(), oldDetailEntered = deferred();
  let detailReads = 0;
  const row = entry('recordings', 'recording-1', 'owner');
  const { workspace } = await opened({ request: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/kb/recordings/recording-1' && ++detailReads === 2) {
      oldDetailEntered.resolve(); return oldDetail.promise;
    }
    if (call.method === 'DELETE' && call.path === '/api/v1/recordings/recording-1') return { ok: true };
    return undefined;
  } }, row);
  const originalSummary = workspace.detail.summary;
  const refreshing = workspace.refresh(); await oldDetailEntered.promise;
  assert.equal(workspace.loading, true);
  assert.equal(await workspace.deleteRecording(), true);
  assert.equal(workspace.loading, false);

  oldDetail.resolve({ detail: { id: 'recording-1', kind: 'recordings', summary: 'Stale pre-delete detail', meta: {} }, matches: [], agents: [] });
  await refreshing;
  assert.equal(workspace.writeState, 'deleted');
  assert.equal(workspace.detail.summary, originalSummary);
  assert.equal(workspace.loading, false);
});

test('old kind failures and query responses cannot replace current rows, results, errors or loading', async () => {
  const oldKind = deferred(), currentPeople = deferred(), oldListBeforeSearch = deferred(), firstSearch = deferred(), secondSearch = deferred();
  const oldKindEntered = deferred(), peopleEntered = deferred(), beforeSearchEntered = deferred(), firstSearchEntered = deferred(), secondSearchEntered = deferred();
  const env = await opened({ request: async (call) => {
    if (call.method !== 'GET') return undefined;
    if (call.path === '/api/v1/kb/projects?tag=slow-kind') { oldKindEntered.resolve(); return oldKind.promise; }
    if (call.path === '/api/v1/kb/people') { peopleEntered.resolve(); return currentPeople.promise; }
    if (call.path === '/api/v1/kb/people?tag=slow-query') { beforeSearchEntered.resolve(); return oldListBeforeSearch.promise; }
    if (call.path === '/api/v1/kb-search?q=first&results=1') { firstSearchEntered.resolve(); return firstSearch.promise; }
    if (call.path === '/api/v1/kb-search?q=second&results=1') { secondSearchEntered.resolve(); return secondSearch.promise; }
    return undefined;
  } });
  const { workspace } = env;

  workspace.setTag('slow-kind'); await oldKindEntered.promise;
  workspace.setKind('people'); await peopleEntered.promise;
  assert.equal(workspace.loading, true);
  oldKind.reject(failure(503, 'old_kind_failed'));
  await flush();
  assert.equal(workspace.loading, true);
  assert.equal(workspace.error, '');
  currentPeople.resolve({ items: [{ id: 'person-current', kind: 'people', name: 'Current Person', manager: 'work' }], tags: ['people-tag'] });
  await flush();
  assert.equal(workspace.kind, 'people');
  assert.equal(workspace.entries[0].path, 'people');
  assert.equal(workspace.entries[0].id, 'person-current');
  assert.deepEqual(workspace.tags, ['people-tag']);

  workspace.setTag('slow-query'); await beforeSearchEntered.promise;
  workspace.setQuery('first');
  oldListBeforeSearch.reject(failure(503, 'old_list_failed'));
  await flush();
  assert.equal(workspace.loading, false);
  assert.equal(workspace.error, '');
  assert.equal(workspace.entries[0].id, 'person-current');

  await firstSearchEntered.promise;
  workspace.setQuery('second');
  await secondSearchEntered.promise;
  assert.equal(workspace.loading, true);
  secondSearch.resolve({ results: [{ count: 1, in_name: true,
    entry: { id: 'second-hit', kind: 'people', title: 'Second result', manager: 'work' }, hits: [{ line: 'second query hit' }] }] });
  await flush();
  assert.equal(workspace.results[0].entry.id, 'second-hit');
  assert.equal(workspace.loading, false);
  firstSearch.resolve({ results: [{ count: 1, in_name: true,
    entry: { id: 'first-hit', kind: 'people', title: 'First result', manager: 'work' }, hits: [{ line: 'first query hit' }] }] });
  await flush();
  assert.equal(workspace.results[0].entry.id, 'second-hit');
  assert.equal(workspace.error, '');
  workspace.hide();
});

test('a late detail refresh cannot replace the post-save reload or overwrite a dirty editor', async () => {
  const writeGate = deferred(), writeEntered = deferred(), oldDetail = deferred(), oldDetailEntered = deferred();
  let detailReads = 0;
  const row = entry('projects', 'project-1', 'owner');
  const { workspace } = await opened({ request: async (call) => {
    if (call.method === 'PUT') { writeEntered.resolve(); await writeGate.promise; return { ok: true }; }
    if (call.method === 'GET' && call.path === '/api/v1/kb/projects/project-1') {
      detailReads++;
      if (detailReads === 2) { oldDetailEntered.resolve(); return oldDetail.promise; }
      if (detailReads > 2) return { detail: { id: 'project-1', kind: 'projects', summary: 'Fresh post-save detail',
        meta: { importance: 'P1', relevance: 60, tags: ['saved-tags'], kind: 'short', status: 'active' } }, matches: [], agents: [] };
    }
    return undefined;
  } }, row);

  workspace.beginEdit(); workspace.setEditTags('saved-tags');
  const saving = workspace.saveEdit(); await writeEntered.promise;
  const refreshing = workspace.refresh(); await oldDetailEntered.promise;
  workspace.setEditTags('newer-draft');
  writeGate.resolve();
  assert.equal(await saving, true);
  assert.equal(workspace.detail.summary, 'Fresh post-save detail');
  assert.equal(workspace.editOpen, true);
  assert.equal(workspace.editTags, 'newer-draft');

  oldDetail.resolve({ detail: { id: 'project-1', kind: 'projects', summary: 'Old background detail',
    meta: { importance: 'P2', relevance: 25, tags: ['old'], kind: 'short', status: 'active' } }, matches: [], agents: [] });
  await refreshing;
  assert.equal(workspace.detail.summary, 'Fresh post-save detail');
  assert.equal(workspace.editTags, 'newer-draft');
});

test('a detail open interrupted by reconnect resumes draft and project targets on the new owner generation', async () => {
  const oldDetail = deferred(), retryDetail = deferred(), oldEntered = deferred(), retryEntered = deferred();
  let detailReads = 0;
  const env = fixture({ request: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/kb/projects/project-1') {
      detailReads++;
      if (detailReads === 1) { oldEntered.resolve(); return oldDetail.promise; }
      if (detailReads === 2) { retryEntered.resolve(); return retryDetail.promise; }
    }
    return undefined;
  } });
  const row = entry('projects', 'project-1', 'owner'), workspace = new KnowledgeWorkspace();
  const draftKey = 'knowledge-draft:' + JSON.stringify(['owner', 'projects', 'project-1']);
  env.shared.views.set(env.owner.state.scope + ':' + draftKey, JSON.stringify({ importance: 'P1', relevance: '41', tags: 'saved across reconnect',
    project_kind: 'long', status: 'paused', agent_prompt: 'draft prompt', agent_launcher: 'claude', metadata_dirty: true }));

  const opening = workspace.open(env.client, row);
  await oldEntered.promise;
  env.owner.state.generation++;
  const reconnectRead = workspace.refresh();
  await retryEntered.promise;
  oldDetail.reject(failure(503, 'old_owner_failed'));
  await opening;
  assert.equal(workspace.error, '');
  assert.equal(workspace.loading, true);

  retryDetail.resolve({ detail: { id: 'project-1', kind: 'projects', summary: 'Loaded after reconnect',
    meta: { importance: 'P2', relevance: 25, tags: ['server'], kind: 'short', status: 'active', dirs: ['personal-box:/repo', 'untrusted-box:/unsafe'] } },
    matches: [], agents: [] });
  await reconnectRead;
  assert.equal(workspace.detail.summary, 'Loaded after reconnect');
  assert.equal(workspace.editTags, 'saved across reconnect');
  assert.equal(workspace.agentDraft, 'draft prompt');
  assert.deepEqual(workspace.targetMachines.map((item) => item.id), ['personal-box']);
  assert.deepEqual(workspace.directoryTargets.map((item) => [item.machine, item.path]), [['personal-box', '/repo']]);
  assert.equal(workspace.loading, false);
});

test('detail edits made after the reconnect read starts survive its saved draft restore', async () => {
  const oldDetail = deferred(), retryDetail = deferred(), oldEntered = deferred(), retryEntered = deferred();
  let detailReads = 0;
  const env = fixture({ request: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/kb/projects/project-1') {
      detailReads++;
      if (detailReads === 1) { oldEntered.resolve(); return oldDetail.promise; }
      if (detailReads === 2) { retryEntered.resolve(); return retryDetail.promise; }
    }
    return undefined;
  } });
  const row = entry('projects', 'project-1', 'owner'), workspace = new KnowledgeWorkspace();
  const opening = workspace.open(env.client, row);
  await oldEntered.promise;
  env.owner.state.generation++;
  const reconnectRead = workspace.refresh();
  await retryEntered.promise;

  workspace.beginEdit(); workspace.setEditImportance('P0'); workspace.setEditRelevance('73'); workspace.setEditTags('typed after retry');
  workspace.setAgentDraft('prompt after retry'); workspace.setAgentLauncher('launcher after retry');
  oldDetail.reject(failure(503, 'old_owner_failed'));
  await opening;
  assert.equal(workspace.loading, true);

  retryDetail.resolve({ detail: { id: 'project-1', kind: 'projects', summary: 'Retry detail',
    meta: { importance: 'P2', relevance: 25, tags: ['server'], kind: 'short', status: 'active', dirs: ['personal-box:/repo'] } }, matches: [], agents: [] });
  await reconnectRead;
  assert.equal(workspace.editImportance, 'P0');
  assert.equal(workspace.editRelevance, '73');
  assert.equal(workspace.editTags, 'typed after retry');
  assert.equal(workspace.agentDraft, 'prompt after retry');
  assert.equal(workspace.agentLauncher, 'launcher after retry');
  assert.equal(workspace.detail.summary, 'Retry detail');
  assert.equal(workspace.directoryTargets.length, 1);
});

test('detail edits made during the first read survive reconnect even when their draft write is pending', async () => {
  const oldDetail = deferred(), retryDetail = deferred(), oldEntered = deferred(), retryEntered = deferred(), saveGate = deferred(), saveEntered = deferred();
  let detailReads = 0;
  const env = fixture({ request: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/kb/projects/project-1') {
      detailReads++;
      if (detailReads === 1) { oldEntered.resolve(); return oldDetail.promise; }
      if (detailReads === 2) { retryEntered.resolve(); return retryDetail.promise; }
    }
    return undefined;
  } });
  const row = entry('projects', 'project-1', 'owner'), workspace = new KnowledgeWorkspace();
  const draftKey = 'knowledge-draft:' + JSON.stringify(['owner', 'projects', 'project-1']);
  const saveView = env.owner.client.saveView.bind(env.owner.client);
  env.owner.client.saveView = async (key, value, revision) => {
    if (key === draftKey) { saveEntered.resolve(); await saveGate.promise; }
    await saveView(key, value, revision);
  };
  const opening = workspace.open(env.client, row);
  await oldEntered.promise;
  workspace.beginEdit(); workspace.setEditImportance('P1'); workspace.setEditRelevance('64'); workspace.setEditTags('typed before reconnect');
  workspace.setAgentDraft('prompt before reconnect'); workspace.setAgentLauncher('launcher before reconnect');
  await saveEntered.promise;
  env.owner.state.generation++;
  const reconnectRead = workspace.refresh();
  await retryEntered.promise;

  oldDetail.reject(failure(503, 'old_owner_failed'));
  await opening;
  assert.equal(workspace.loading, true);
  retryDetail.resolve({ detail: { id: 'project-1', kind: 'projects', summary: 'Recovered detail',
    meta: { importance: 'P2', relevance: 25, tags: ['server'], kind: 'short', status: 'active', dirs: ['personal-box:/repo'] } }, matches: [], agents: [] });
  await reconnectRead;
  assert.equal(workspace.editImportance, 'P1');
  assert.equal(workspace.editRelevance, '64');
  assert.equal(workspace.editTags, 'typed before reconnect');
  assert.equal(workspace.agentDraft, 'prompt before reconnect');
  assert.equal(workspace.agentLauncher, 'launcher before reconnect');
  assert.equal(workspace.detail.summary, 'Recovered detail');
  assert.equal(workspace.directoryTargets.length, 1);
  saveGate.resolve();
  await workspace.draftWrite;
});

test('Knowledge detail immutable leaves key by full content while minutes selection stays logical-id based', () => {
  const page = readFileSync(new URL('../entry/src/main/ets/components/KnowledgeWorkspacePage.ets', import.meta.url), 'utf8');
  for (const key of [
    "JSON.stringify([index, Wire.text(match, 'id'), JSON.stringify(match)])",
    'JSON.stringify([index, line])',
    "JSON.stringify([index, Wire.text(moment, 'id'), JSON.stringify(moment)])",
    "JSON.stringify([index, Wire.text(item, 'kind') + '/' + Wire.text(item, 'id'), JSON.stringify(item)])",
    "JSON.stringify([index, Wire.text(action, 'id'), JSON.stringify(action)])",
    'JSON.stringify([index, JSON.stringify(line)])'
  ]) assert.ok(page.includes(key), 'missing content-sensitive native ForEach key: ' + key);
  assert.ok(page.includes("this.selectedActions.includes(Wire.text(action, 'id'))"));
  assert.equal(page.split('JSON.stringify([index, line])').length - 1, 2, 'search snippets and timeline lines keep full-line identity');
});
