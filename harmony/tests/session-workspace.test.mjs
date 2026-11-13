import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/SessionWorkspace.ets') || context.parentURL?.endsWith('/FileTransfer.ets')) {
      if (specifier === '@kit.ArkTS') return { url: 'persona-session:arkts', shortCircuit: true };
      if (specifier === './ClientModel') return { url: 'persona-session:client', shortCircuit: true };
      if (specifier === './HubRepository') return { url: 'persona-session:repository', shortCircuit: true };
      if (specifier === '../platform/HubTransport') return { url: 'persona-session:transport', shortCircuit: true };
    }
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-session:arkts') return { format: 'module', source: 'export const util={generateRandomUUID:()=>crypto.randomUUID()};', shortCircuit: true };
    if (url === 'persona-session:client') return { format: 'module', source: 'export class ClientModel {}', shortCircuit: true };
    if (url === 'persona-session:repository') return { format: 'module', source: 'export const segment = encodeURIComponent;', shortCircuit: true };
    if (url === 'persona-session:transport') return { format: 'module', source: `
      export class HubTransport {}
      export class HttpFailure extends Error {
        constructor(status, code, resultUnknown = false, resultPending = false) {
          super(code); this.status = status; this.code = code; this.resultUnknown = resultUnknown; this.resultPending = resultPending;
        }
      }`, shortCircuit: true };
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { SessionWorkspace } = await import('../entry/src/main/ets/models/SessionWorkspace.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');
const { HttpFailure } = await import('persona-session:transport');

function clone(value) { return structuredClone(value); }
function waitForTurn() { return new Promise((resolve) => setTimeout(resolve, 0)); }

function session(id = 's1', fields = {}) {
  return ResourceRow.from({ id, session_id: id, machine_id: 'm1', manager: 'work', cwd: '/repo', workspace_id: 'w1', ...fields }, 'session');
}

function machine(fields = {}) {
  return ResourceRow.from({ id: 'm1', manager: 'work', name: 'MacBook', online: true, trust: '', ...fields }, 'machine');
}

function fixture(options = {}) {
  const state = { scope: 'hub:work', generation: 3, calls: [], attempts: [], uploads: [], blocked: [], beforeDispatch: undefined,
    handle: options.handle, features: options.features || ['file_edit', 'upload_dir', 'file_download', 'git_write', 'terminal_control', 'preview'] };
  const repository = {
    mutations: { blocked: () => state.blocked },
    transport: {
      sent: [],
      send(value) { this.sent.push(value); },
      stream(path) {
        const child = { path, closed: false, sent: [], callbacks: undefined,
          async connect(text, status, binary) { this.callbacks = { text, status, binary }; status('ready'); },
          send(value) { this.sent.push(value); return true; }, close() { this.closed = true; } };
        state.previewStream = child; return child;
      }
    },
    async call(path, method = 'GET', body = {}, _op = '', admit) {
      const call = { path, method, body: clone(body) };
      state.attempts.push(call);
      if (state.beforeDispatch) { const before = state.beforeDispatch; state.beforeDispatch = undefined; await before(call); }
      if (admit && !admit()) throw new HttpFailure(0, 'request_updated');
      state.calls.push(call);
      try {
        if (state.handle) {
          const result = await state.handle(call, state);
          if (result !== undefined) return clone(result);
        }
      } catch (error) {
        if (error.resultUnknown || error.resultPending) state.blocked.push({ path, method });
        throw error;
      }
      if (path.includes('/fs?')) return { path: '/repo', entries: [] };
      if (path.includes('/git/status')) return { is_repo: true, branch: 'main', upstream: 'origin/main', files: [], stashes: [] };
      if (path.includes('/git/branches')) return { branches: [{ name: 'main', current: true }, { name: 'topic', current: false }] };
      if (path.includes('/screen?')) return { text: 'terminal screen' };
      if (path.includes('/preview/servers')) return { servers: [{ name: 'web', port: 5173, url: 'http://localhost:5173/', command: ['npm', 'run', 'dev'], cwd: '/repo', listening: true }] };
      if (path.includes('/files/trash')) return { trashed: [{ path: '/repo/a.txt', trash_id: 'opaque-trash-id' }] };
      return { ok: true };
    },
    async upload(path, bytes, admit) {
      const attempt = { path, bytes };
      state.attempts.push(attempt);
      if (state.beforeDispatch) { const before = state.beforeDispatch; state.beforeDispatch = undefined; await before(attempt); }
      if (admit && !admit()) throw new HttpFailure(0, 'request_updated');
      state.uploads.push(attempt);
      return { path: '/repo/upload.bin' };
    }
  };
  const client = {
    selected: options.selected || session('s1', options.session || {}),
    machines: options.machines || [machine(options.machine || {})],
    profile: { manager: 'work' }, connected: options.connected !== false, demo: false,
    features: state.features,
    scope: () => state.scope,
    connectionGeneration: () => state.generation,
    owns: (generation, scope) => generation === state.generation && scope === state.scope,
    target(row = client.selected) { return JSON.stringify([state.scope, row.key()]); },
    repo: () => repository,
    viewRevision: () => 0,
    captureView: () => ({epoch:0,valid:()=>true,read:async()=>'',write:async()=>{}}),
    loadView: async () => '',
    saveView: async () => {},
    observeEvents: () => () => {},
    machineName: () => 'MacBook'
  };
  const workspace = new SessionWorkspace();
  workspace.bind(client); workspace.show();
  return { state, client, repository, workspace };
}

test('file changes use the registered rename, move, trash and restore routes with exact bodies', async () => {
  const { state, workspace } = fixture();
  await waitForTurn();
  assert.equal(workspace.canEditFiles(), true);

  assert.equal(await workspace.renameFile('/repo/a.txt', 'renamed.txt'), true);
  assert.deepEqual(state.calls.find((call) => call.method === 'POST'), {
    path: '/api/v1/machines/m1/files/rename', method: 'POST', body: { path: '/repo/a.txt', cwd: '/repo', name: 'renamed.txt' }
  });
  assert.equal(await workspace.moveFiles(['/repo/renamed.txt'], '/repo/archive', 'keep'), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/files/move')).body,
    { paths: ['/repo/renamed.txt'], cwd: '/repo', dir: '/repo/archive', conflict: 'keep' });
  assert.equal(await workspace.trashFiles(['/repo/renamed.txt']), true);
  assert.deepEqual(workspace.trashUndo.map((item) => [item.path, item.trashId]), [['/repo/a.txt', 'opaque-trash-id']]);
  assert.equal(await workspace.restoreTrash(), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/files/restore')).body, { trash_ids: ['opaque-trash-id'] });
  workspace.hide();
});

test('file conflict is recoverable, while an uncertain write blocks further file edits', async () => {
  let uncertain = false;
  const { state, workspace } = fixture({ handle(call) {
    if (call.method === 'POST' && call.path.endsWith('/files/move')) throw new HttpFailure(409, 'exists');
    if (uncertain && call.method === 'POST' && call.path.endsWith('/files/rename')) throw new HttpFailure(0, 'result_unknown', true);
    return undefined;
  } });
  await waitForTurn();
  assert.equal(await workspace.moveFiles(['/repo/a.txt'], '/repo/target'), false);
  assert.equal(workspace.fileWriteState, 'conflict');
  uncertain = true;
  assert.equal(await workspace.renameFile('/repo/a.txt', 'next.txt'), false);
  assert.equal(workspace.fileWriteState, 'unknown');
  assert.equal(workspace.canEditFiles(), false);
  workspace.hide();
});

test('a target switch between journal admission and dispatch prevents stale file and upload writes', async () => {
  const { state, client, workspace } = fixture();
  await waitForTurn();
  state.beforeDispatch = async () => { client.selected = session('s2'); };
  assert.equal(await workspace.renameFile('/repo/a.txt', 'stale.txt'), false);
  assert.equal(state.calls.some((call) => call.path.endsWith('/files/rename')), false);
  client.selected = session('s1');
  state.generation++;
  workspace.bind(client); workspace.show();
  await waitForTurn();
  state.beforeDispatch = async () => { client.selected = session('s2'); };
  await assert.rejects(workspace.uploadFile('upload.bin', new Uint8Array([1]).buffer), /request_updated/);
  assert.equal(state.uploads.length, 0);
  workspace.hide();
});

test('git writes use registered bodies and stay acknowledged when the follow-up status read fails', async () => {
  let failStatus = false;
  const { state, workspace } = fixture({ handle(call) {
    if (failStatus && call.method === 'GET' && call.path.includes('/git/status')) throw new Error('status_offline');
    return undefined;
  } });
  await waitForTurn();
  workspace.selectTab(1); await workspace.loadGit();
  assert.equal(workspace.canWriteGit(), true);

  const file = Object.assign({ path: '/repo/a.txt' }, { staged: false, unstaged: true, untracked: false });
  assert.equal(await workspace.stageGitFile(file), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/stage')).body, { cwd: '/repo', paths: ['/repo/a.txt'] });
  assert.equal(await workspace.commitGit('ship it'), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/commit')).body, { cwd: '/repo', message: 'ship it' });
  assert.equal(await workspace.pushGit(true), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/push')).body, { cwd: '/repo', set_upstream: true });
  assert.equal(await workspace.switchGit('topic'), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/switch')).body, { cwd: '/repo', branch: 'topic' });
  assert.equal(await workspace.stashGit('push', '', 'save point', true), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/stash')).body,
    { cwd: '/repo', op: 'push', message: 'save point', untracked: true });
  failStatus = true;
  assert.equal(await workspace.commitGit('ack is durable'), true);
  await waitForTurn();
  assert.equal(workspace.gitWriteState, 'acknowledged');
  workspace.hide();
});

test('unknown git and terminal key outcomes remain blocked without a replay', async () => {
  const { state, workspace } = fixture({ handle(call) {
    if (call.method === 'POST' && (call.path.endsWith('/git/commit') || call.path.endsWith('/keys')))
      throw new HttpFailure(0, 'result_unknown', true);
    return undefined;
  } });
  await waitForTurn();
  workspace.selectTab(1); await workspace.loadGit();
  assert.equal(await workspace.commitGit('may have committed'), false);
  assert.equal(workspace.gitWriteState, 'unknown');
  assert.equal(workspace.canWriteGit(), false);

  workspace.selectTab(3); await waitForTurn();
  assert.equal(workspace.screenControlRequested, false);
  assert.equal(workspace.enableScreenControl(), true);
  assert.equal(await workspace.sendScreenKeys(['ctrl+c'], 'typed text', workspace.screenRevision), false);
  assert.equal(workspace.screenControlRequested, false);
  assert.equal(state.calls.filter((call) => call.path.endsWith('/keys')).length, 1);
  assert.equal(await workspace.sendScreenKeys(['ctrl+c'], 'typed text', workspace.screenRevision), false);
  assert.equal(state.calls.filter((call) => call.path.endsWith('/keys')).length, 1);
  workspace.hide();
});

test('workspace mutations are disabled for an unconfirmed, offline, or read-only session', async () => {
  const pending = fixture({ machine: { trust: 'pending' } });
  await waitForTurn();
  assert.equal(pending.workspace.canEditFiles(), false);
  assert.equal(pending.workspace.canWriteGit(), false);
  pending.workspace.hide();

  const offline = fixture({ machine: { online: false } });
  await waitForTurn();
  assert.equal(offline.workspace.canEditFiles(), false);
  offline.workspace.hide();

  const readOnly = fixture({ session: { read_only: true } });
  await waitForTurn();
  assert.equal(readOnly.workspace.canEditFiles(), false);
  readOnly.workspace.hide();
});

test('the terminal screen is read-only by default and sends keys only after explicit enablement', async () => {
  const { state, workspace } = fixture();
  await waitForTurn();
  workspace.selectTab(3); await waitForTurn();
  assert.equal(workspace.screenText, 'terminal screen');
  assert.equal(workspace.screenControlRequested, false);
  assert.equal(workspace.canControlScreen(), true);
  assert.equal(workspace.enableScreenControl(), true);
  const revision = workspace.screenRevision;
  assert.equal(await workspace.sendScreenKeys(['enter'], 'pwd', revision), true);
  const keys = state.calls.find((call) => call.path.endsWith('/keys'));
  assert.deepEqual(keys.body, { keys: ['enter'], text: 'pwd' });
  assert.equal(await workspace.sendScreenKeys(['not-a-key'], '', revision), false);
  workspace.hide();
});

test('dev preview opens the authorized native stream without putting the Hub token in its URL', async () => {
  const { state, workspace } = fixture();
  await waitForTurn();
  workspace.selectTab(4); await waitForTurn();
  assert.equal(await workspace.openPreview(5173, '/guide', 'web'), true);
  assert.equal(state.previewStream.path, '/api/v1/machines/m1/preview/connect?port=5173&path=%2Fguide&w=390&h=844&dpr=1&mobile=1');
  assert.equal(state.previewStream.path.includes('token'), false);
  workspace.hide();
});

test('file transfer follows late capabilities of the captured connection and immediately revokes them',async()=>{
  const {client,state,workspace}=fixture({features:[]});
  const entry={name:'sample.txt',path:'/repo/sample.txt',directory:false,symlink:false,size:20,ignored:false};
  try {
    assert.equal(workspace.canSendFile(entry),false);
    client.features=['file_transfer'];
    assert.equal(workspace.canSendFile(entry),true);
    client.features=[];assert.equal(workspace.canSendFile(entry),false);
    client.features=['file_transfer'];state.generation++;
    assert.equal(workspace.canSendFile(entry),false);
  } finally {workspace.hide();}
});
