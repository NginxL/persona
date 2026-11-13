import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/AgentWorkspace.ets') && specifier === './Wire')
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

const { AgentWorkspace } = await import('../entry/src/main/ets/models/AgentWorkspace.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');

function agent(id, fields = {}) {
  const data = {
    session_id: id, machine_id: 'm1', manager: 'work', title: 'Agent ' + id, cwd: '/repo', repo_root: '/repo',
    pane_id: 'herdr:pane-' + id, worktree: '/repo', branch: 'main', status: 'working', state: 'working', updated_at: '2026-11-11T10:00:00Z',
    ...fields
  };
  return ResourceRow.from(data, 'session');
}

function machine(id = 'm1', trust = 'personal') {
  return ResourceRow.from({ id, manager: 'work', name: 'Computer ' + id, online: true, trust }, 'machine');
}

function clone(value) { return JSON.parse(JSON.stringify(value)); }

function clientFixture(options = {}) {
  const state = {
    scope: 'hub:work', generation: 3, manager: 'work', calls: [], blocked: new Set(),
    prefs: {
      pins: { spaces: ['m1/keep-space'], sessions: ['m1/keep-session'] },
      mutes: { spaces: ['m1/space'], sessions: ['m1/keep-muted'], allowed: ['m1/allowed', 'keep/allowed'] }
    }, history: options.history || [], views: new Map(), request: undefined
  };
  const repo = {
    async call(path, method = 'GET', body = {}) {
      const call = { path, method, body: clone(body) };
      state.calls.push(call);
      if (state.request) {
        const result = await state.request(call, state);
        if (result !== undefined) return clone(result);
      }
      if (path === '/api/v1/pins' && method === 'GET') return clone(state.prefs.pins);
      if (path === '/api/v1/mutes' && method === 'GET') return clone(state.prefs.mutes);
      if (path === '/api/v1/pins' && method === 'PUT') { state.prefs.pins = clone(body); return clone(body); }
      if (path === '/api/v1/mutes' && method === 'PUT') { state.prefs.mutes = clone(body); return clone(body); }
      if (path.startsWith('/api/v1/sessions/archived')) return { sessions: state.history };
      return { ok: true };
    }
  };
  const client = {
    connected: true, demo: false, sessionsReady: true, profile: { manager: state.manager }, features: [],
    sessions: options.sessions || [agent('s1')], machines: options.machines || [machine()],
    selected: new ResourceRow(),
    repo() { return repo; },
    scope() { return state.scope; },
    connectionGeneration() { return state.generation; },
    owns(generation, scope) { return generation === state.generation && scope === state.scope; },
    target(row) { return JSON.stringify([this.scope(), row.kind, row.machine, row.id]); },
    machineName(id) { return this.machines.find((row) => row.id === id)?.title || id; },
    shortPath(path) { return path.replace(/\\/g, '/').split('/').filter(Boolean).slice(-2).join('/'); },
    operationBlocked(path) { return state.blocked.has(path); },
    async loadView(key) { return state.views.get(state.scope + ':' + key) || ''; },
    async saveView(key, value) { state.views.set(state.scope + ':' + key, value); },
    directoryOwners(){return[this];}, groupName(){return 'Hub';}, directoryOwnerFor(row){return row.manager===this.profile.manager ? this : undefined;}, async refreshDirectory(){},
    async refresh() {}
  };
  return { state, client, repo };
}

async function opened(options = {}) {
  const fixture = clientFixture(options), workspace = new AgentWorkspace();
  await workspace.open(fixture.client);
  return { ...fixture, workspace };
}

test('pins fetch the current document and preserve unrelated space and session keys', async () => {
  const { workspace, state, client } = await opened();
  const row = client.sessions[0];
  state.prefs.pins = { spaces: ['m1/space-a', 'm1/space-b'], sessions: ['m1/s1', 'm2/other'] };
  state.calls.length = 0;
  assert.equal(await workspace.togglePin(row), true);
  assert.deepEqual(state.calls.map((call) => [call.method, call.path]), [
    ['GET', '/api/v1/pins'], ['PUT', '/api/v1/pins']
  ]);
  assert.deepEqual(state.calls[1].body, { spaces: ['m1/space-a', 'm1/space-b'], sessions: ['m2/other'] });
});

test('unmuting an agent inherited from a muted space writes an allowed exception', async () => {
  const { workspace, state, client } = await opened();
  const row = client.sessions[0]; row.raw = JSON.stringify({ ...row.data(), space_name: 'space' });
  state.prefs.mutes = { spaces: ['m1/space', 'm2/keep'], sessions: ['m2/session'], allowed: ['m1/existing', 'keep/allowed'] };
  state.calls.length = 0;
  assert.equal(workspace.isMuted(row), true);
  assert.equal(await workspace.toggleMute(row), true);
  assert.deepEqual(state.calls.map((call) => [call.method, call.path]), [
    ['GET', '/api/v1/mutes'], ['PUT', '/api/v1/mutes']
  ]);
  assert.deepEqual(state.calls[1].body, {
    spaces: ['m1/space', 'm2/keep'], sessions: ['m2/session'], allowed: ['m1/existing', 'keep/allowed', 'm1/s1']
  });
  assert.equal(workspace.isMuted(row), false);
});

test('muting an agent opts it back into a mute beneath an allowed exception', async () => {
  const { workspace, state, client } = await opened();
  const row = client.sessions[0]; row.raw = JSON.stringify({ ...row.data(), space_name: 'space' });
  state.prefs.mutes = { spaces: ['m1/space', 'm2/keep'], sessions: ['m2/session'], allowed: ['m1/s1', 'keep/allowed'] };
  await workspace.refresh(client);
  state.calls.length = 0;
  assert.equal(workspace.isMuted(row), false);
  assert.equal(await workspace.toggleMute(row), true);
  assert.deepEqual(state.calls[1].body, {
    spaces: ['m1/space', 'm2/keep'], sessions: ['m2/session', 'm1/s1'], allowed: ['keep/allowed']
  });
  assert.equal(workspace.isMuted(row), true);
});

test('rename, close, delete, and seen use the existing routes and exact request bodies', async () => {
  const s1 = agent('s1');
  const done = agent('done', { state: 'unread', status: 'done' });
  const { workspace, state } = await opened({ sessions: [s1, done] });
  state.calls.length = 0;
  assert.equal(await workspace.rename(s1, 'Renamed', 'review'), true);
  assert.equal(await workspace.close(s1), true);
  assert.equal(await workspace.delete(done), true);
  assert.equal(await workspace.markSeen(done), true);
  assert.deepEqual(state.calls.filter((call) => call.path.includes('/sessions/')).map((call) => [call.method, call.path, call.body]), [
    ['POST', '/api/v1/sessions/m1/s1/rename', { title: 'Renamed', tab_name: 'review' }],
    ['POST', '/api/v1/sessions/m1/s1/close', {}],
    ['DELETE', '/api/v1/sessions/m1/done', {}],
    ['POST', '/api/v1/sessions/m1/done/seen', {}]
  ]);
});

test('history loads only when the hub advertises the archive capability and honors machine selection', async () => {
  const archived = agent('archive-1', { archived: true, state: 'offline', status: 'offline' });
  const { workspace, state, client } = await opened({ history: [archived] });
  client.features = ['agent_session_archives'];
  state.calls.length = 0;
  await workspace.setMachineFilter('m1');
  await workspace.setHistory(true);
  assert.ok(state.calls.some((call) => call.method === 'GET' && call.path === '/api/v1/sessions/archived?machine_id=m1&manager=work'));
  assert.deepEqual(workspace.groups.flatMap((group) => group.rows).map((row) => row.id), ['archive-1']);
});

test('the flat list shows friendly identity and state while retaining complete inspection metadata', async () => {
  const row = agent('s1', { cwd: '/work/persona', repo_root: '/work/persona', worktree: '/work/persona', last_text: 'Checking the build' });
  const { workspace } = await opened({ sessions: [row], machines: [machine('m1')] });
  assert.equal(workspace.groupingMode, 'flat');
  assert.deepEqual(workspace.groups.map((group) => group.id), ['flat']);
  assert.equal(workspace.groups[0].showMachine, false);
  assert.equal(workspace.groups[0].showRepository, false);
  assert.equal(workspace.groups[0].showWorktree, false);
  assert.deepEqual(workspace.rowMetadata(row), ['Hub · work', 'Computer m1', 'work/persona', 'working', 'Checking the build']);
  assert.deepEqual(workspace.listMetadata(row), ['Hub', 'Computer m1', 'working', 'Checking the build']);
});

test('native grouping choices show only their selected levels and grouping changes release the membership freeze', async () => {
  const first = agent('s1', { repo_root: '/work/persona', worktree: '/work/persona' });
  const second = agent('s2', { repo_root: '/work/persona', worktree: '/work/persona/feature', branch: 'feature' });
  const third = agent('s3', { machine_id: 'm2', repo_root: '/other/repo', cwd: '/other/repo', worktree: '/other/repo' });
  const { workspace, client } = await opened({ sessions: [first, second, third], machines: [machine('m1'), machine('m2')] });
  assert.equal(workspace.visibleRows().length, 3);
  workspace.setGroupingMode('machine');
  assert.equal(workspace.groups.length, 2);
  assert.ok(workspace.groups.every((group) => group.showMachine && !group.showRepository && !group.showWorktree));
  workspace.setGroupingMode('repository');
  assert.equal(workspace.groups.length, 2);
  assert.ok(workspace.groups.every((group) => !group.showMachine && group.showRepository && !group.showWorktree));
  workspace.setGroupingMode('worktree');
  assert.equal(workspace.groups.length, 3);
  assert.ok(workspace.groups.every((group) => !group.showMachine && !group.showRepository && group.showWorktree));

  workspace.setGroupingMode('flat');
  workspace.setReadingDetail(true);
  client.sessions = [first];
  workspace.update(client, true);
  assert.deepEqual(workspace.visibleRows().map((row) => row.id), ['s1', 's2', 's3']);
  client.sessions = [first, second, third, agent('s4')];
  workspace.update(client, true);
  assert.deepEqual(workspace.visibleRows().map((row) => row.id), ['s1', 's2', 's3']);
  workspace.setGroupingMode('machine');
  const groupedOrder = workspace.visibleRows().map((row) => row.id);
  assert.deepEqual(groupedOrder, ['s1', 's2', 's4', 's3']);
  client.sessions = [first, second, third, agent('s4'), agent('s5', { state: 'needs_you' })];
  workspace.update(client, true);
  assert.deepEqual(workspace.visibleRows().map((row) => row.id), groupedOrder);
});

test('external session selection updates and clears the left row selection', async () => {
  const first = agent('s1'), second = agent('s2');
  const { workspace, client } = await opened({ sessions: [first, second] });
  client.selected = agent('s2');
  workspace.update(client);
  assert.equal(workspace.isSelected(second), true);
  client.selected = ResourceRow.from({ id: 'conversation-1', manager: 'work' }, 'conversation');
  workspace.update(client);
  assert.equal(workspace.selectedKey, '');
  assert.equal(workspace.isSelected(second), false);
  client.selected = agent('s1');
  await workspace.open(client);
  assert.equal(workspace.isSelected(first), true);
});

test('a detail reading freezes membership and refreezes after explicit filter changes', async () => {
  const first = agent('s1', { state: 'working', updated_at: '2026-11-11T10:00:00Z' });
  const second = agent('s2', { state: 'idle', status: 'idle', updated_at: '2026-11-11T09:00:00Z' });
  const { workspace, client } = await opened({ sessions: [first, second] });
  workspace.setReadingDetail(true);
  const latest = agent('s1', { state: 'idle', status: 'idle', updated_at: '2026-11-11T11:00:00Z' });
  const added = agent('s3', { state: 'working', updated_at: '2026-11-11T12:00:00Z' });
  client.sessions = [latest, added];
  workspace.update(client, true);
  const frozen = workspace.groups.flatMap((group) => group.rows);
  assert.deepEqual(frozen.map((row) => row.id), ['s1', 's2']);
  assert.equal(frozen[0].state, 'idle');
  workspace.setQuickFilter('working');
  assert.deepEqual(workspace.groups.flatMap((group) => group.rows).map((row) => row.id), ['s3']);
  const updated = agent('s3', { state: 'idle', status: 'idle', updated_at: '2026-11-11T13:00:00Z' });
  const next = agent('s4', { state: 'working', updated_at: '2026-11-11T14:00:00Z' });
  client.sessions = [updated, next];
  workspace.update(client, true);
  assert.deepEqual(workspace.groups.flatMap((group) => group.rows).map((row) => row.id), ['s3']);
  assert.equal(workspace.visibleRows()[0].state, 'idle');

  workspace.setQuickFilter('all');
  const refrozen = workspace.visibleRows().map((row) => row.id);
  assert.deepEqual(refrozen, ['s4', 's3']);
  client.sessions = [updated, next, agent('s5', { state: 'needs_you', updated_at: '2026-11-11T15:00:00Z' })];
  workspace.update(client, true);
  assert.deepEqual(workspace.visibleRows().map((row) => row.id), refrozen);
});

test('an empty list also stays frozen until an explicit filter recomputes it', async () => {
  const { workspace, client } = await opened({ sessions: [] });
  workspace.setReadingDetail(true);
  client.sessions = [agent('arrived')];
  workspace.update(client, true);
  assert.deepEqual(workspace.visibleRows(), []);
  workspace.setQuickFilter('all');
  assert.deepEqual(workspace.visibleRows().map((row) => row.id), ['arrived']);
});

test('session actions refuse read-only, untrusted, and already unresolved paths', async () => {
  const readOnly = agent('readonly', { source_kind: 'codex_history', read_only_reason: 'History' });
  const pending = agent('pending');
  const { workspace, state, client } = await opened({ sessions: [readOnly, pending], machines: [machine('m1', 'pending')] });
  assert.equal(workspace.canRename(readOnly), false);
  assert.equal(workspace.canClose(readOnly), false);
  assert.equal(await workspace.rename(readOnly, 'No', ''), false);
  client.machines = [machine()]; workspace.update(client);
  const path = '/api/v1/sessions/m1/pending/rename';
  state.blocked.add(path);
  assert.equal(await workspace.rename(pending, 'No', ''), false);
  assert.equal(workspace.error, 'pending_action');
  assert.equal(state.calls.some((call) => call.path === path), false);
  client.profile.manager = 'personal'; state.manager = 'personal'; state.scope = 'hub:personal'; state.generation++;
  assert.equal(await workspace.close(pending), false);
});

test('a scope change while reading pins prevents the old pairing from writing', async () => {
  const { workspace, state, client } = await opened();
  const entered = new Promise((resolve) => { state.entered = resolve; });
  let release;
  const waiting = new Promise((resolve) => { release = resolve; });
  state.request = async (call) => {
    if (call.path === '/api/v1/pins' && call.method === 'GET') {
      state.entered();
      await waiting;
      return { spaces: ['m1/space'], sessions: ['m1/s1'] };
    }
    return undefined;
  };
  const pending = workspace.togglePin(client.sessions[0]);
  await entered;
  client.profile.manager = 'personal'; state.manager = 'personal'; state.scope = 'hub:personal'; state.generation++;
  release();
  assert.equal(await pending, false);
  assert.equal(state.calls.some((call) => call.method === 'PUT' && call.path === '/api/v1/pins'), false);
});

test('list filters and selection restore within their manager scope', async () => {
  const { workspace, state, client } = await opened();
  workspace.setQuickFilter('working');
  workspace.setGroupingMode('repository');
  await new Promise((resolve) => setTimeout(resolve, 220));
  assert.equal(JSON.parse(state.views.get('hub:work:agent-workspace')).quick, 'working');
  assert.equal(JSON.parse(state.views.get('hub:work:agent-workspace')).grouping, 'repository');
  state.scope = 'hub:personal'; state.manager = 'personal'; client.profile.manager = 'personal'; state.generation++;
  state.views.set('hub:personal:agent-workspace', JSON.stringify({ quick: 'needs_you', grouping: 'worktree', machine: 'p1', search: 'review', selected: '["session","personal","p1","p1-s1"]' }));
  await workspace.open(client);
  assert.equal(workspace.quickFilter, 'needs_you');
  assert.equal(workspace.groupingMode, 'worktree');
  assert.equal(workspace.machineFilter, 'p1');
  assert.equal(workspace.search, 'review');
  assert.equal(workspace.selectedKey, '["session","personal","p1","p1-s1"]');
});

test('the first loaded live catalog populates a cold Agents list before membership is frozen',async()=>{
 const {client}=clientFixture({sessions:[]});client.sessionsReady=false;const workspace=new AgentWorkspace();await workspace.open(client,true);assert.equal(workspace.groups.length,0);
 client.sessions=[agent('first')];client.sessionsReady=true;workspace.update(client,true);assert.deepEqual(workspace.groups.flatMap(g=>g.rows.map(r=>r.id)),['first']);
 client.sessions=[agent('later'),agent('first',{state:'needs_you'})];workspace.update(client,true);assert.deepEqual(workspace.groups.flatMap(g=>g.rows.map(r=>r.id)),['first']);
});

test('merged rows with identical runtime ids isolate pin, mute and mutation ownership',async()=>{
 const a=clientFixture(),b=clientFixture();a.client.profile.id='hub-a';b.client.profile.id='hub-b';b.state.scope='hub-b:work';
 const first=agent('same'),second=agent('same');first.group='hub-a';second.group='hub-b';a.client.sessions=[first];b.client.sessions=[second];
 a.client.directoryReady=true;a.client.directorySessions=[first,second];a.client.directoryOwners=()=>[a.client,b.client];a.client.directoryOwnerFor=row=>row.group==='hub-b'?b.client:a.client;
 a.client.groupName=row=>row.group==='hub-b'?'Second Hub':'First Hub';
 const w=new AgentWorkspace();await w.open(a.client);assert.equal(w.visibleRows().length,2);
 b.state.calls=[];a.state.calls=[];assert.equal(await w.togglePin(second),true);
 assert.equal(b.state.calls.some(c=>c.method==='PUT'&&c.path==='/api/v1/pins'),true);assert.equal(a.state.calls.length,0);
 assert.equal(w.isPinned(second),true);assert.equal(w.isPinned(first),false);
 b.state.calls=[];assert.equal(await w.rename(second,'Renamed',''),true);assert.equal(b.state.calls[0].path,'/api/v1/sessions/m1/same/rename');assert.equal(a.state.calls.length,0);
 w.selectExternal(second);w.update(a.client);assert.equal(w.isSelected(second),true);assert.equal(w.isSelected(first),false);
 assert.ok(w.rowMetadata(second)[0].includes('Second Hub'));
});
test('a mutation journal admission is revoked when its captured Hub goes away',async()=>{
 const f=clientFixture(),w=new AgentWorkspace();await w.open(f.client);let dispatched=0;
 f.repo.call=async(path,method,body,op,admit)=>{f.state.generation++;if(admit&&!admit())throw Error('request_updated');dispatched++;return{};};
 assert.equal(await w.rename(f.client.sessions[0],'changed',''),false);assert.equal(dispatched,0);
});

test('a background directory load cannot bypass established reading membership',async()=>{const first=agent('frozen-a'),second=agent('frozen-b'),{workspace,client}=await opened({sessions:[first,second]});workspace.setReadingDetail(true);client.directoryReady=true;client.directoryLoading=true;client.directorySessions=[agent('new-member'),agent('frozen-a',{state:'idle'})];workspace.update(client,true);assert.deepEqual(workspace.visibleRows().map(row=>row.id),['frozen-a','frozen-b']);assert.equal(workspace.visibleRows()[0].state,'idle');});
test('forgetting a foreign Hub removes frozen rows, history, selection and action authority',async()=>{const a=clientFixture(),b=clientFixture();a.client.profile.id='hub-a';b.client.profile.id='hub-b';b.state.scope='hub-b:work';const first=agent('same'),second=agent('same');first.group='hub-a';second.group='hub-b';a.client.directoryReady=true;a.client.directorySessions=[first,second];a.client.directoryOwners=()=>[a.client,b.client];a.client.directoryOwnerFor=row=>row.group==='hub-b'?b.client:a.client;const w=new AgentWorkspace();await w.open(a.client,true);w.selectExternal(second);w.historyRows=[second];const ticket=w.actionTicket(second);assert.equal(ticket(),true);w.forgetHub('hub-b');assert.deepEqual(w.visibleRows().map(row=>row.group),['hub-a']);assert.equal(w.selectedKey,'');assert.equal(w.historyRows.length,0);assert.equal(ticket(),false);});
test('an archive-only foreign owner is included and its open confirmation cannot survive reconnect',async()=>{const a=clientFixture(),b=clientFixture({history:[agent('archived',{archived:true,state:'offline'})]});a.client.profile.id='hub-a';b.client.profile.id='hub-b';b.state.scope='hub-b:work';b.client.features=['agent_session_archives'];b.client.sessions=[];a.client.directoryOwners=()=>[a.client,b.client];a.client.directoryOwnerFor=row=>row.group==='hub-b'?b.client:a.client;const w=new AgentWorkspace();await w.open(a.client);assert.equal(w.historySupported(),true);await w.setHistory(true);assert.ok(b.state.calls.some(call=>call.path.startsWith('/api/v1/sessions/archived')));assert.equal(w.visibleRows()[0].group,'hub-b');await w.setHistory(false);const row=a.client.sessions[0],ticket=w.actionTicket(row);assert.equal(ticket(),true);a.state.generation++;assert.equal(ticket(),false);});

const nativePageSource=readFileSync(new URL('../entry/src/main/ets/components/AgentListPage.ets',import.meta.url),'utf8');
function nativePageMethod(name){const start=nativePageSource.search(new RegExp('^  private (?:async )?'+name+'\\(','m'));assert.ok(start>=0);const body=nativePageSource.indexOf('{',start);let depth=1,quote='',escape=false,at=body+1;for(;at<nativePageSource.length&&depth;at++){const c=nativePageSource[at];if(quote){if(escape)escape=false;else if(c==='\\')escape=true;else if(c===quote)quote='';}else if(c==='"'||c==="'"||c==='`')quote=c;else if(c==='{')depth++;else if(c==='}')depth--;}return nativePageSource.slice(start,at);}
const AgentNativeActions=new Function('tr','Wire','return '+stripTypeScriptTypes('class AgentNativeActions{'+['openRename','saveRename','confirmClose','confirmDelete'].map(nativePageMethod).join('\n')+'}'))(key=>key,(await import('../entry/src/main/ets/models/Wire.ets')).Wire);
test('native rename, close and delete retain the owner generation captured when the editor or confirmation opened',async()=>{
 for(const action of ['rename','close','delete']){
  const a=clientFixture(),b=clientFixture();a.client.profile.id='hub-a';b.client.profile.id='hub-b';b.state.scope='hub-b:work';const row=agent('foreign',{state:'offline',status:'offline'});row.group='hub-b';a.client.directoryReady=true;a.client.directorySessions=[row];a.client.directoryOwners=()=>[a.client,b.client];a.client.directoryOwnerFor=()=>b.client;
  const workspace=new AgentWorkspace();await workspace.open(a.client);b.state.calls=[];const page=new AgentNativeActions();page.workspace=workspace;page.model=a.client;page.active=true;let dialog;page.getUIContext=()=>({showAlertDialog:value=>dialog=value});
  if(action==='rename'){page.openRename(row);page.renameTitle='Changed';b.state.generation++;await page.saveRename();assert.equal(page.editorOpen,false);}
  else{page[action==='close'?'confirmClose':'confirmDelete'](row);b.state.generation++;dialog.secondaryButton.action();await new Promise(resolve=>setImmediate(resolve));}
  assert.equal(b.state.calls.some(call=>call.method!=='GET'),false);
 }
});

test('forget cancels an aggregate history pass even after that Hub rows were collected',async()=>{const a=clientFixture(),b=clientFixture({history:[agent('b-history')]}),c=clientFixture();a.client.profile.id='hub-a';b.client.profile.id='hub-b';c.client.profile.id='hub-c';b.state.scope='hub-b:work';c.state.scope='hub-c:work';b.client.features=c.client.features=['agent_session_archives'];a.client.directoryOwners=()=>[a.client,b.client,c.client];a.client.directoryOwnerFor=row=>row.group==='hub-b'?b.client:row.group==='hub-c'?c.client:a.client;let release,started;const gate=new Promise(r=>release=r),entered=new Promise(r=>started=r);c.state.request=async call=>{if(call.path.startsWith('/api/v1/sessions/archived')){started();return gate;}};const w=new AgentWorkspace();await w.open(a.client);const history=w.setHistory(true);await entered;w.forgetHub('hub-b');release({sessions:[agent('c-history').data()]});await history;assert.equal(w.loading,false);assert.equal(w.historyRows.some(row=>row.group==='hub-b'),false);assert.equal(w.visibleRows().some(row=>row.group==='hub-b'),false);});
test('turning history off invalidates its pending response before restoring the live list',async()=>{const f=clientFixture();f.client.features=['agent_session_archives'];let release,started;const gate=new Promise(r=>release=r),entered=new Promise(r=>started=r);f.state.request=async call=>{if(call.path.startsWith('/api/v1/sessions/archived')){started();return gate;}};const w=new AgentWorkspace();await w.open(f.client);const old=w.setHistory(true);await entered;await w.setHistory(false);release({sessions:[agent('stale-archive').data()]});await old;assert.equal(w.history,false);assert.deepEqual(w.visibleRows().map(row=>row.id),['s1']);assert.equal(w.loading,false);});

test('detail activation while opening cannot freeze an empty list before its saved view loads', async () => {
  const { client } = clientFixture(), workspace = new AgentWorkspace();
  const opening = workspace.open(client, true);
  workspace.setReadingDetail(true);
  await opening;
  assert.deepEqual(workspace.visibleRows().map(row => row.id), ['s1']);
  client.sessions = [agent('s1', { state: 'idle' }), agent('new-later')];
  workspace.update(client, true);
  assert.deepEqual(workspace.visibleRows().map(row => row.id), ['s1']);
  assert.equal(workspace.visibleRows()[0].state, 'idle');
  workspace.hide();
});
