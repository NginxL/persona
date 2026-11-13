import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/FileTransfer.ets')) {
      if (specifier === './ClientModel') return { url: 'persona-file-transfer:client', shortCircuit: true };
      if (specifier === '../platform/HubTransport') return { url: 'persona-file-transfer:transport', shortCircuit: true };
      if (specifier === '@kit.ArkTS') return { url: 'persona-file-transfer:arkts', shortCircuit: true };
    }
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-file-transfer:client') return { format: 'module', source: 'export class ClientModel {}', shortCircuit: true };
    if (url === 'persona-file-transfer:arkts') return { format: 'module', source: `
      let sequence = 0;
      export const util = { generateRandomUUID: () => 'synthetic-' + String(++sequence) };`, shortCircuit: true };
    if (url === 'persona-file-transfer:transport') return { format: 'module', source: `
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

const { FileTransfer } = await import('../entry/src/main/ets/models/FileTransfer.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');
const { HttpFailure } = await import('persona-file-transfer:transport');
let fixtureSerial = 0;

function row(id, manager = 'manager-a', extras = {}) {
  return ResourceRow.from({ id, name: id, manager, ...extras }, 'machine');
}

function fixture(options = {}) {
  const fixtureId = ++fixtureSerial;
  const state = {
    calls: [], listeners: new Set(), journal: '', journalRevision: 0, generation: 1, transfers: new Map(),
    settings: new Map([
      ['source', { enabled: false, inbox: '~/Downloads/Persona' }],
      ['target', { enabled: false, inbox: '~/Downloads/Persona' }],
      ['offline', { enabled: true, inbox: '~/Downloads/Persona' }],
      ['pending', { enabled: true, inbox: '~/Downloads/Persona' }],
      ['other', { enabled: true, inbox: '~/Downloads/Persona' }]
    ]),
    post: undefined,
    readJournal: undefined,
    settingResponse: undefined,
    getTransfer: undefined,
    deleteTransfer: undefined,
    putSetting: undefined,
    failGetSettings: new Set()
  };
  const machines = [
    row('source', 'manager-a', { online: true, trust: 'personal' }),
    row('target', 'manager-a', { online: true, trust: 'confirmed' }),
    row('offline', 'manager-a', { online: false, trust: 'personal' }),
    row('pending', 'manager-a', { online: true, trust: 'pending' }),
    row('other', 'manager-b', { online: true, trust: 'work' })
  ];
  const profile = { id: `profile-${fixtureId}`, hubId: `hub-${fixtureId}`, manager: 'manager-a' };
  const selected = ResourceRow.from({ id: 'session-a', manager: 'manager-a', machine_id: 'source', cwd: '/work' }, 'session');
  const transport = {
    async request(path, method = 'GET', body = '') {
      state.calls.push({ path, method, body, channel: 'request' });
      if (path.endsWith('/file-transfer/settings')) {
        const machineId = path.split('/').at(-3);
        if (method === 'GET' && state.failGetSettings.has(machineId)) throw new HttpFailure(0, 'offline');
        if (method === 'GET') return JSON.stringify(state.settings.get(machineId) || { enabled: false });
        if (state.putSetting) await state.putSetting(machineId, JSON.parse(body), state);
        state.settings.set(machineId, { ...(state.settings.get(machineId) || {}), ...JSON.parse(body) });
        if (state.settingResponse) return state.settingResponse(machineId, state.settings.get(machineId));
        return JSON.stringify(state.settings.get(machineId));
      }
      const id = path.split('/').at(-1);
      if (method === 'GET') {
        if (state.getTransfer) return state.getTransfer(id, state);
        const value = state.transfers.get(id);
        if (!value) throw new HttpFailure(404, 'unknown_transfer');
        return JSON.stringify(value);
      }
      if (method === 'DELETE') {
        if (state.deleteTransfer) return state.deleteTransfer(id, state);
        const value = state.transfers.get(id);
        if (!value) throw new HttpFailure(404, 'unknown_transfer');
        value.state = 'cancelled';
        return JSON.stringify(value);
      }
      throw new Error('unexpected_request');
    },
    async requestAccepted(path, method, body) {
      const payload = JSON.parse(body);
      state.calls.push({ path, method, body, payload, channel: 'requestAccepted' });
      state.journalAtPost = state.journal;
      if (state.post) return state.post(payload, state);
      const value = {
        id: 'transfer-1', state: 'running', from: payload.from.machine, to: payload.to.machine,
        name: 'report.pdf', bytes: 0, total: 100, updated_at: '2026-11-12T00:00:00Z'
      };
      state.transfers.set(value.id, value);
      return JSON.stringify(value);
    }
  };
  const makeClient = () => ({
    profile, selected, machines, connected: true, demo: false, features: ['file_transfer'],
    target() { return JSON.stringify([this.scope(), this.selected.kind, this.selected.machine, this.selected.id]); },
    scope() { return JSON.stringify([profile.id, profile.manager]); },
    machineName(id) { return id; },
    connectionGeneration() { return state.generation; },
    owns(generation, scope) { return generation === state.generation && scope === this.scope(); },
    repo() { return { transport }; },
    observeEvents(listener) { state.listeners.add(listener); return () => state.listeners.delete(listener); },
    viewRevision() { return state.journalRevision; },
    captureView() {
      const epoch = state.journalRevision;
      const valid = () => epoch === state.journalRevision;
      return {
        epoch, valid,
        async read() { if (!valid()) throw new Error('request_updated'); return state.readJournal ? state.readJournal() : state.journal; },
        async write(value) {
          if (!valid()) throw new Error('request_updated');
          if (state.writeJournal) await state.writeJournal(value);
          if (!valid()) throw new Error('request_updated');
          state.journal = value;
        }
      };
    },
    async loadView() { return state.journal; },
    async saveView(_key, value, expectedRevision) {
      if (expectedRevision !== state.journalRevision) throw new Error('view_revision_changed');
      // LocalStore revisions are purge epochs; ordinary writes do not advance them.
      state.journal = value;
    }
  });
  const client = makeClient();
  const transfer = new FileTransfer();
  transfer.bind(client, 'source', 'manager-a');
  return { state, client, makeClient, transfer };
}

async function openReadySource(transfer) {
  await transfer.show();
  await transfer.openSource('/work/report.pdf', 'report.pdf', '/work');
  await transfer.selectTarget('target');
}

async function enableTransfer(transfer) {
  await openReadySource(transfer);
  await transfer.setEnabled('source', true);
  await transfer.setEnabled('target', true);
}

test('only trusted online computers in the captured manager can receive a direct transfer', async () => {
  const { state, transfer } = fixture();
  await openReadySource(transfer);
  assert.deepEqual(transfer.targets.map((machine) => machine.id), ['source', 'target']);
  assert.equal(transfer.canStart('target'), false);
  await transfer.setEnabled('source', true);
  await transfer.setEnabled('target', true);
  assert.equal(transfer.canStart('target'), true);

  const created = await transfer.start('target');
  const post = state.calls.find((call) => call.channel === 'requestAccepted');
  assert.deepEqual(post.payload, {
    from: { machine: 'source', path: '/work/report.pdf', cwd: '/work' },
    to: { machine: 'target', dest: 'inbox' }
  });
  assert.equal(JSON.parse(state.journalAtPost).records[0].state, 'creating', 'the pending create is durable before POST');
  assert.equal(created.state, 'running', 'a 202 body is only an accepted/running transfer');
  assert.equal(state.calls.some((call) => call.channel === 'request' && call.method === 'POST'), false);
  assert.equal(state.calls.some((call) => call.path === '/api/v1/files/transfer' && call.method === 'GET'), false);
  transfer.hide();
});

test('a definite switch rejection restores its known value while an uncertain PUT re-reads without compensation', async () => {
  const { state, transfer } = fixture();
  await openReadySource(transfer);
  state.putSetting = async (_machineId, _body) => { throw new HttpFailure(403, 'read_only'); };
  assert.equal(await transfer.setEnabled('source', true), false);
  assert.equal(transfer.sourceSetting.state, 'ready');
  assert.equal(transfer.sourceSetting.enabled, false);
  assert.equal(transfer.sourceSetting.error, 'read_only');
  assert.equal(state.calls.filter((call) => call.method === 'GET' && call.path.endsWith('/file-transfer/settings')).length, 2);

  state.putSetting = async (_machineId, _body) => { throw new HttpFailure(0, 'network_lost', true); };
  state.failGetSettings.add('source');
  assert.equal(await transfer.setEnabled('source', true), false);
  assert.equal(transfer.sourceSetting.state, 'unknown');
  assert.equal(transfer.sourceSetting.enabled, false, 'a failed status re-read restores the last confirmed switch position');
  assert.equal(state.calls.filter((call) => call.method === 'PUT' && call.path.endsWith('/file-transfer/settings')).length, 2);
  assert.equal(state.calls.filter((call) => call.method === 'GET' && call.path.endsWith('/file-transfer/settings')).length, 3);
  transfer.hide();
});

test('an idless create remains unknown across rebind and acknowledgement is local only', async () => {
  const { state, client, transfer } = fixture();
  await openReadySource(transfer);
  await transfer.setEnabled('source', true);
  await transfer.setEnabled('target', true);
  state.post = async () => { throw new HttpFailure(0, 'network_lost', true); };
  const record = await transfer.start('target');
  assert.equal(record.state, 'unknown');
  assert.equal(record.id, '');
  const postCount = state.calls.filter((call) => call.channel === 'requestAccepted').length;
  const transferRequestCount = state.calls.filter((call) => call.path.includes('/files/transfer')).length;
  transfer.hide();

  const restored = new FileTransfer();
  restored.bind(client, 'source', 'manager-a');
  await restored.show();
  assert.equal(restored.records[0].state, 'unknown');
  assert.equal(restored.records[0].id, '');
  assert.equal(state.calls.filter((call) => call.channel === 'requestAccepted').length, postCount);
  assert.equal(state.calls.filter((call) => call.path.includes('/files/transfer')).length, transferRequestCount);
  assert.equal(await restored.acknowledgeUnknown(restored.records[0].key), true);
  assert.equal(state.calls.filter((call) => call.path.includes('/files/transfer')).length, transferRequestCount);
  assert.equal(state.calls.some((call) => call.path === '/api/v1/files/transfer' && call.method === 'GET'), false);
  assert.equal(state.calls.some((call) => call.path === '/api/v1/files/transfer' && call.method === 'DELETE'), false);
  await openReadySource(restored);
  await assert.rejects(() => restored.start('target'), /duplicate_confirmation_required/);
  assert.equal(state.calls.filter((call) => call.channel === 'requestAccepted').length, postCount);
  assert.equal(state.calls.filter((call) => call.path.includes('/files/transfer')).length, transferRequestCount);
  restored.hide();
});

test('known ids reconcile by GET and events must match manager, id, source, and target', async () => {
  const { state, client, transfer } = fixture();
  await openReadySource(transfer);
  await transfer.setEnabled('source', true);
  await transfer.setEnabled('target', true);
  const record = await transfer.start('target');
  const restored = new FileTransfer();
  restored.bind(client, 'source', 'manager-a');
  await restored.show();
  assert.equal(state.calls.some((call) => call.method === 'GET' && call.path.endsWith('/transfer/transfer-1')), true);

  const event = (manager, changes = {}) => JSON.stringify({
    type: 'file_transfer', manager,
    transfer: { id: 'transfer-1', state: 'done', from: 'source', to: 'target', bytes: 100, total: 100,
      result_path: '/Users/test/Downloads/Persona/report.pdf', updated_at: '2026-11-12T00:00:02Z', ...changes }
  });
  const before = restored.records.find((item) => item.key === record.key).state;
  for (const listener of state.listeners) listener(event('manager-b'));
  for (const listener of state.listeners) listener(event('manager-a', { id: 'another-id' }));
  for (const listener of state.listeners) listener(event('manager-a', { from: 'other' }));
  for (const listener of state.listeners) listener(event('manager-a', { to: 'other' }));
  assert.equal(restored.records.find((item) => item.key === record.key).state, before);
  for (const listener of state.listeners) listener(event('manager-a'));
  assert.equal(restored.records.find((item) => item.key === record.key).state, 'done');
  for (const listener of state.listeners) listener(event('manager-a', { state: 'running', bytes: 40, updated_at: '2026-11-12T00:00:01Z' }));
  assert.equal(restored.records.find((item) => item.key === record.key).state, 'done', 'a late progress event cannot undo a terminal result');
  transfer.hide(); restored.hide();
});

for (const code of ['file_changed', 'file_transfer_disabled']) test(`a running transfer keeps ${code} failure across rebind without automatic creation`, async () => {
  const { state, client, transfer } = fixture();
  await enableTransfer(transfer);
  const record = await transfer.start('target');
  assert.equal(record.state, 'running');
  const failed = { ...state.transfers.get(record.id), state: 'failed', code,
    error: code === 'file_changed' ? 'Source computer: the source file changed' : 'Target computer: file transfer is off',
    bytes: 40, updated_at: '2026-11-12T00:00:02Z' };
  state.transfers.set(record.id, failed);
  for (const listener of state.listeners) listener(JSON.stringify({ type: 'file_transfer', manager: 'manager-a', transfer: failed }));
  assert.equal(record.state, 'failed');
  assert.equal(record.code, code);
  assert.equal(record.error, failed.error);
  assert.equal(record.bytes, 40);
  transfer.hide();
  state.generation++;
  const restored = new FileTransfer();
  restored.bind(client, 'source', 'manager-a');
  await openReadySource(restored);
  const retained = restored.records.find(item => item.key === record.key);
  assert.equal(retained.state, 'failed');
  assert.equal(retained.code, code);
  assert.equal(retained.error, failed.error);
  assert.equal(state.calls.filter(call => call.channel === 'requestAccepted').length, 1);
  restored.hide();
});

test('an uncertain cancel is reconciled and is never shown cancelled before server confirmation', async () => {
  const { state, transfer } = fixture();
  await openReadySource(transfer);
  await transfer.setEnabled('source', true);
  await transfer.setEnabled('target', true);
  const record = await transfer.start('target');
  state.deleteTransfer = async () => { throw new HttpFailure(0, 'network_lost', true); };
  state.getTransfer = async () => JSON.stringify(state.transfers.get('transfer-1'));
  assert.equal(await transfer.cancel(record.key), false);
  assert.equal(transfer.records[0].state, 'running');
  assert.equal(transfer.records[0].cancelUnknown, true);
  state.getTransfer = async () => {
    const current = state.transfers.get('transfer-1');
    return JSON.stringify({ ...current, state: 'cancelled', updated_at: '2026-11-12T00:00:03Z' });
  };
  await transfer.refresh(record.key);
  assert.equal(transfer.records[0].state, 'cancelled');
  transfer.hide();
});

test('an unknown transfer cannot be cancelled and a 404 remains unknown', async () => {
  const { state, transfer } = fixture();
  await openReadySource(transfer);
  await transfer.setEnabled('source', true);
  await transfer.setEnabled('target', true);
  state.post = async () => { throw new HttpFailure(0, 'network_lost', true); };
  const record = await transfer.start('target');
  const deleteCount = state.calls.filter((call) => call.method === 'DELETE').length;
  assert.equal(await transfer.cancel(record.key), false);
  assert.equal(state.calls.filter((call) => call.method === 'DELETE').length, deleteCount);
  assert.equal(transfer.records[0].state, 'unknown');
  transfer.hide();

  const known = fixture();
  await openReadySource(known.transfer);
  await known.transfer.setEnabled('source', true);
  await known.transfer.setEnabled('target', true);
  await known.transfer.start('target');
  known.transfer.hide();
  known.state.getTransfer = async () => { throw new HttpFailure(404, 'unknown_transfer'); };
  const restored = new FileTransfer();
  restored.bind(known.client, 'source', 'manager-a');
  await restored.show();
  assert.equal(restored.records[0].state, 'unknown');
  assert.equal(restored.records[0].code, 'unknown_transfer');
  assert.equal(known.state.calls.filter((call) => call.method === 'DELETE').length, 0);
  restored.hide();
});

test('a second file-transfer view shares the pending create and cannot POST a duplicate', async () => {
  const { state, client, transfer } = fixture();
  await openReadySource(transfer);
  await transfer.setEnabled('source', true);
  await transfer.setEnabled('target', true);

  let releasePost;
  let enteredPost;
  const postEntered = new Promise((resolve) => { enteredPost = resolve; });
  state.post = async (payload) => {
    enteredPost();
    return new Promise((resolve) => {
      releasePost = () => {
        const accepted = { id: 'shared-transfer', state: 'running', from: payload.from.machine,
          to: payload.to.machine, bytes: 0, total: 100, updated_at: '2026-11-12T00:00:00Z' };
        state.transfers.set(accepted.id, accepted);
        resolve(JSON.stringify(accepted));
      };
    });
  };
  const firstStart = transfer.start('target');
  await postEntered;

  const second = new FileTransfer();
  second.bind(client, 'source', 'manager-a');
  await openReadySource(second);
  assert.equal(second.records[0].state, 'creating');
  assert.equal(second.canStart('target'), false);
  await assert.rejects(() => second.start('target'), /transfer_create_pending/);
  assert.equal(state.calls.filter((call) => call.channel === 'requestAccepted').length, 1);

  releasePost();
  await firstStart;
  assert.equal(second.records[0].state, 'running');
  assert.equal(second.records[0].id, 'shared-transfer');
  assert.equal(state.calls.filter((call) => call.channel === 'requestAccepted').length, 1);
  transfer.hide(); second.hide();
});

test('same-scope file-transfer clients share the journal while the original POST is pending', async () => {
  const { state, client, makeClient, transfer } = fixture();
  await openReadySource(transfer);
  await transfer.setEnabled('source', true);
  await transfer.setEnabled('target', true);

  let releasePost;
  let enteredPost;
  const postEntered = new Promise((resolve) => { enteredPost = resolve; });
  state.post = async (payload) => {
    enteredPost();
    return new Promise((resolve) => {
      releasePost = () => {
        const accepted = { id: 'cross-client-transfer', state: 'running', from: payload.from.machine,
          to: payload.to.machine, bytes: 0, total: 100, updated_at: '2026-11-12T00:00:00Z' };
        state.transfers.set(accepted.id, accepted);
        resolve(JSON.stringify(accepted));
      };
    });
  };
  const firstStart = transfer.start('target');
  await postEntered;
  transfer.hide();

  const otherClient = makeClient();
  assert.notEqual(otherClient, client);
  const second = new FileTransfer();
  second.bind(otherClient, 'source', 'manager-a');
  await openReadySource(second);
  assert.equal(second.records[0].state, 'creating');
  assert.equal(second.canStart('target'), false);
  await assert.rejects(() => second.start('target'), /transfer_create_pending/);
  assert.equal(state.calls.filter((call) => call.channel === 'requestAccepted').length, 1);

  releasePost();
  await firstStart;
  assert.equal(second.records[0].state, 'running');
  assert.equal(second.records[0].id, 'cross-client-transfer');
  assert.equal(state.calls.filter((call) => call.channel === 'requestAccepted').length, 1);
  second.hide();
});

test('a successful malformed PUT is reconciled as unknown and a malformed POST stays idless unknown', async () => {
  const { state, transfer } = fixture();
  await openReadySource(transfer);
  state.settingResponse = () => '{malformed';
  state.failGetSettings.add('source');
  assert.equal(await transfer.setEnabled('source', true), false);
  assert.equal(transfer.sourceSetting.state, 'unknown');
  assert.equal(state.calls.filter((call) => call.method === 'PUT' && call.path.endsWith('/file-transfer/settings')).length, 1);
  state.failGetSettings.delete('source');
  state.settingResponse = undefined;
  await transfer.openSource('/work/report.pdf', 'report.pdf', '/work');
  await transfer.selectTarget('target');
  await transfer.setEnabled('target', true);
  state.post = async () => '{malformed';
  const record = await transfer.start('target');
  assert.equal(record.state, 'unknown');
  assert.equal(record.id, '');
  assert.equal(state.calls.filter((call) => call.channel === 'requestAccepted').length, 1);
  transfer.hide();
});

test('a 404 for a known transfer keeps its id and requires acknowledgement plus a new duplicate confirmation', async () => {
  const { state, transfer } = fixture();
  await enableTransfer(transfer);
  let creates = 0;
  state.post = async (payload) => {
    const id = `known-${++creates}`;
    const accepted = { id, state: 'running', from: payload.from.machine, to: payload.to.machine,
      bytes: 0, total: 100, updated_at: '2026-11-12T00:00:00Z' };
    state.transfers.set(id, accepted);
    return JSON.stringify(accepted);
  };
  state.getTransfer = async (id) => {
    if (id === 'known-1') throw new HttpFailure(404, 'unknown_transfer');
    return JSON.stringify(state.transfers.get(id));
  };
  const first = await transfer.start('target');
  assert.equal(first.state, 'unknown');
  assert.equal(first.id, 'known-1');
  assert.equal(await transfer.acknowledgeUnknown(first.key), true);
  assert.equal(transfer.requiresDuplicateConfirmation('target'), true);
  await assert.rejects(() => transfer.start('target'), /duplicate_confirmation_required/);
  assert.equal(creates, 1);
  const second = await transfer.start('target', true);
  assert.equal(second.id, 'known-2');
  assert.equal(creates, 2);
  transfer.hide();
});

test('a setting save freezes target selection until its result is applied', async () => {
  const { state, transfer } = fixture();
  await openReadySource(transfer);
  await transfer.setEnabled('source', true);
  let releasePut;
  let enteredPut;
  const putEntered = new Promise((resolve) => { enteredPut = resolve; });
  state.putSetting = async (machineId) => {
    if (machineId !== 'target') return;
    enteredPut();
    await new Promise((resolve) => { releasePut = resolve; });
  };
  const saving = transfer.setEnabled('target', true);
  await putEntered;
  await transfer.selectTarget('source');
  assert.equal(transfer.targetId, 'target');
  assert.equal(transfer.targetSelectionFrozen(), true);
  releasePut();
  assert.equal(await saving, true);
  assert.equal(transfer.targetId, 'target');
  assert.equal(transfer.targetSetting.state, 'ready');
  assert.equal(transfer.targetSetting.enabled, true);
  transfer.hide();
});

test('an active source file keeps its transfer id visible and blocks another target or POST until terminal', async () => {
  const { state, transfer } = fixture();
  await enableTransfer(transfer);
  let creates = 0;
  state.post = async (payload) => {
    const id = `active-${++creates}`;
    const accepted = { id, state: 'running', from: payload.from.machine, to: payload.to.machine,
      bytes: 40, total: 100, updated_at: '2026-11-12T00:00:00Z' };
    state.transfers.set(id, accepted);
    return JSON.stringify(accepted);
  };
  const first = await transfer.start('target');
  assert.equal(transfer.latestForSource()?.id, first.id);
  assert.equal(transfer.targetSelectionFrozen(), true);
  assert.equal(transfer.canStart('target'), false);
  await transfer.selectTarget('source');
  assert.equal(transfer.targetId, 'target');
  await assert.rejects(() => transfer.start('target'), /transfer_active/);
  assert.equal(creates, 1);
  state.getTransfer = async (id) => JSON.stringify({ ...state.transfers.get(id), state: 'done',
    bytes: 100, updated_at: '2026-11-12T00:00:02Z' });
  await transfer.refresh(first.key);
  assert.equal(transfer.records[0].state, 'done');
  assert.equal(transfer.targetSelectionFrozen(), false);
  assert.equal(transfer.canStart('target'), true);
  await transfer.start('target');
  assert.equal(creates, 2);
  transfer.hide();
});

for (const interruption of ['selection', 'reconnect', 'hide']) {
  test(`a pending POST settles through its captured journal after ${interruption}`, async () => {
    const { state, client, transfer } = fixture();
    await enableTransfer(transfer);
    let releasePost;
    let enteredPost;
    const postEntered = new Promise((resolve) => { enteredPost = resolve; });
    state.post = async (payload) => {
      enteredPost();
      return new Promise((resolve) => {
        releasePost = () => {
          const accepted = { id: `captured-${interruption}`, state: 'running', from: payload.from.machine,
            to: payload.to.machine, bytes: 0, total: 100, updated_at: '2026-11-12T00:00:00Z' };
          state.transfers.set(accepted.id, accepted);
          resolve(JSON.stringify(accepted));
        };
      });
    };
    const firstStart = transfer.start('target');
    await postEntered;
    const original = client.selected;
    if (interruption === 'selection') client.selected = ResourceRow.from({ id: 'other-session', manager: 'manager-a', machine_id: 'source', cwd: '/other' }, 'session');
    if (interruption === 'reconnect') state.generation++;
    if (interruption === 'hide') transfer.hide();
    releasePost();
    const record = await firstStart;
    assert.equal(record.state, 'running');
    assert.equal(JSON.parse(state.journal).records[0].id, `captured-${interruption}`);
    if (interruption === 'selection') client.selected = original;
    const restored = new FileTransfer();
    restored.bind(client, 'source', 'manager-a');
    await restored.show();
    assert.equal(restored.records[0].id, `captured-${interruption}`);
    assert.equal(restored.records[0].state, 'running');
    transfer.hide(); restored.hide();
  });
}

for (const interruption of ['selection', 'reconnect', 'hide']) {
  test(`a pending DELETE clears cancel state and persists its result after ${interruption}`, async () => {
    const { state, client, transfer } = fixture();
    await enableTransfer(transfer);
    const record = await transfer.start('target');
    assert.equal(record.state, 'running');
    let releaseDelete;
    let enteredDelete;
    const deleteEntered = new Promise((resolve) => { enteredDelete = resolve; });
    state.deleteTransfer = async (id) => {
      enteredDelete();
      return new Promise((resolve) => {
        releaseDelete = () => {
          const cancelled = { ...state.transfers.get(id), state: 'cancelled', updated_at: '2026-11-12T00:00:02Z' };
          state.transfers.set(id, cancelled);
          resolve(JSON.stringify(cancelled));
        };
      });
    };
    const cancellation = transfer.cancel(record.key);
    await deleteEntered;
    const original = client.selected;
    if (interruption === 'selection') client.selected = ResourceRow.from({ id: 'other-session', manager: 'manager-a', machine_id: 'source', cwd: '/other' }, 'session');
    if (interruption === 'reconnect') state.generation++;
    if (interruption === 'hide') transfer.hide();
    releaseDelete();
    assert.equal(await cancellation, true);
    assert.equal(record.cancelBusy, false);
    assert.equal(record.state, 'cancelled');
    assert.equal(JSON.parse(state.journal).records[0].state, 'cancelled');
    if (interruption === 'selection') client.selected = original;
    transfer.hide();
  });
}

test('captured view purge epochs prevent a late POST response from repopulating a forgotten journal', async () => {
  const { state, client, transfer } = fixture();
  await enableTransfer(transfer);
  let releasePost;
  let enteredPost;
  const postEntered = new Promise((resolve) => { enteredPost = resolve; });
  state.post = async (payload) => {
    enteredPost();
    return new Promise((resolve) => {
      releasePost = () => resolve(JSON.stringify({ id: 'forgotten-transfer', state: 'running',
        from: payload.from.machine, to: payload.to.machine, bytes: 0, total: 1 }));
    });
  };
  const firstStart = transfer.start('target');
  await postEntered;
  state.journalRevision++;
  state.journal = '';
  releasePost();
  await firstStart;
  const restored = new FileTransfer();
  restored.bind(client, 'source', 'manager-a');
  await restored.show();
  assert.equal(restored.records.length, 0);
  assert.equal(state.journal, '');
  transfer.hide(); restored.hide();
});

test('hiding during the initial journal read does not resubscribe or reconcile after it finishes', async () => {
  const { state, transfer } = fixture();
  const raw = JSON.stringify({ version: 1, records: [{ key: 'persisted-running', id: 'pending-transfer',
    manager: 'manager-a', sourceMachine: 'source', sourceName: 'report.pdf', sourcePath: '/work/report.pdf',
    targetMachine: 'target', state: 'running', bytes: 0, total: 10 }] });
  let finishRead;
  let enteredRead;
  const readEntered = new Promise((resolve) => { enteredRead = resolve; });
  state.readJournal = () => {
    enteredRead();
    return new Promise((resolve) => { finishRead = () => resolve(raw); });
  };
  const opening = transfer.show();
  await readEntered;
  transfer.hide();
  finishRead();
  await opening;
  assert.equal(transfer.sharedJournal.subscribers.length, 0);
  assert.equal(state.calls.some((call) => call.path.endsWith('/transfer/pending-transfer') && call.method === 'GET'), false);
  assert.equal(transfer.pollTimer, -1);
});

test('random local keys stay distinct with a fixed clock and concurrent different files', async () => {
  const { state, client, transfer } = fixture();
  await enableTransfer(transfer);
  const second = new FileTransfer();
  second.bind(client, 'source', 'manager-a');
  await openReadySource(second);
  await second.openSource('/work/another.pdf', 'another.pdf', '/work');
  await second.selectTarget('target');
  let creates = 0;
  state.post = async (payload) => {
    const id = `parallel-${++creates}`;
    const accepted = { id, state: 'running', from: payload.from.machine, to: payload.to.machine,
      bytes: 0, total: 10, updated_at: '2026-11-12T00:00:00Z' };
    state.transfers.set(id, accepted);
    return JSON.stringify(accepted);
  };
  const originalNow = Date.now;
  Date.now = () => 42;
  try {
    const results = await Promise.all([transfer.start('target'), second.start('target')]);
    assert.notEqual(results[0].key, results[1].key);
    assert.equal(creates, 2);
  } finally { Date.now = originalNow; }
  transfer.hide(); second.hide();
});

test('a retained native target binding receives renamed computers and loses revoked membership',async()=>{
  const {client,transfer}=fixture();
  client.machineName=(id)=>client.machines.find(machine=>machine.id===id)?.title||id;
  try {
    await openReadySource(transfer);await enableTransfer(transfer);
    assert.equal(transfer.canStart('target'),true);
    transfer.refreshTargets();
    const rendered=transfer.targets.find(machine=>machine.id==='target');
    client.machines=client.machines.map(machine=>machine.id==='target'?row('target','manager-a',{name:'Renamed computer',online:true,trust:'confirmed'}):machine);
    transfer.refreshTargets();
    assert.equal(rendered.title,'Renamed computer');
    assert.ok(transfer.targets.includes(rendered));
    client.machines=client.machines.map(machine=>machine.id==='target'?row('target','manager-a',{online:false,trust:'confirmed'}):machine);
    transfer.refreshTargets();assert.equal(transfer.targets.some(machine=>machine.id==='target'),false);
    assert.equal(transfer.canStart('target'),false);
  } finally {transfer.hide();}
});

for (const machine of ['source', 'target']) test(`reopening waits for the ${machine} switch write and cannot authorize an old GET`, async () => {
  const { state, client, transfer } = fixture();
  await enableTransfer(transfer);
  const second = new FileTransfer();
  second.bind(client, 'source', 'manager-a');
  await openReadySource(second);
  assert.equal(second.canStart('target'), true);
  let releasePut, enteredPut;
  const entered = new Promise(resolve => { enteredPut = resolve; });
  state.putSetting = async id => {
    if (id !== machine) return;
    enteredPut();
    await new Promise(resolve => { releasePut = resolve; });
    state.settings.set(id, { ...state.settings.get(id), enabled: false });
    state.failGetSettings.add(id);
    throw new HttpFailure(0, 'lost_response', true);
  };
  const saving = transfer.setEnabled(machine, false);
  await entered;
  assert.equal(second.canStart('target'), false, 'shared in-flight write blocks the other retained view');
  const reads = state.calls.filter(call => call.method === 'GET').length;
  transfer.hide();
  let reopened = false;
  const reopening = transfer.openSource('/work/report.pdf', 'report.pdf', '/work').then(() => { reopened = true; });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(reopened, false);
  assert.equal(state.calls.filter(call => call.method === 'GET').length, reads, 'no stale settings read races the PUT');
  releasePut();
  assert.equal(await saving, false);
  await reopening;
  await transfer.selectTarget('target');
  assert.equal(transfer.canStart('target'), false);
  assert.equal(second.canStart('target'), false, 'an old ready setting is not revived when the write settles');
  assert.equal(state.calls.filter(call => call.channel === 'requestAccepted').length, 0);
  transfer.hide(); second.hide();
});

test('a late failed status read cannot overwrite an acknowledged terminal event', async () => {
  const { state, transfer } = fixture();
  await enableTransfer(transfer);
  const record = await transfer.start('target');
  let rejectRead, enteredRead;
  const entered = new Promise(resolve => { enteredRead = resolve; });
  state.getTransfer = async () => {
    enteredRead();
    return new Promise((resolve, reject) => { rejectRead = () => reject(new HttpFailure(404, 'unknown_transfer')); });
  };
  const reading = transfer.refresh(record.key);
  await entered;
  const done = { ...state.transfers.get(record.id), state: 'done', bytes: 100,
    result_path: '/synthetic/inbox/report.pdf', updated_at: '2026-11-12T00:01:00Z' };
  for (const callback of state.listeners) callback(JSON.stringify({ type: 'file_transfer', manager: 'manager-a', transfer: done }));
  assert.equal(record.state, 'done');
  rejectRead();
  assert.equal(await reading, false);
  assert.equal(record.state, 'done');
  assert.equal(record.resultPath, '/synthetic/inbox/report.pdf');
  assert.equal(record.code, '');
  transfer.hide();
});

for (const machine of ['source', 'target']) test(`a ${machine} setting write during journal staging revokes dispatch`, async () => {
  const { state, client, transfer } = fixture();
  await enableTransfer(transfer);
  const second = new FileTransfer(); second.bind(client, 'source', 'manager-a'); await openReadySource(second);
  let releaseJournal, enteredJournal, releasePut, enteredPut;
  const journalEntered = new Promise(resolve => { enteredJournal = resolve; });
  const putEntered = new Promise(resolve => { enteredPut = resolve; });
  let first = true;
  state.writeJournal = async () => {
    if (!first) return;
    first = false; enteredJournal(); await new Promise(resolve => { releaseJournal = resolve; });
  };
  state.putSetting = async id => {
    if (id !== machine) return;
    enteredPut(); await new Promise(resolve => { releasePut = resolve; });
  };
  const starting = transfer.start('target'); await journalEntered;
  const disabling = second.setEnabled(machine, false); await putEntered;
  releaseJournal();
  const record = await starting;
  assert.equal(record.state, 'failed');
  assert.equal(record.code, 'request_updated');
  assert.equal(state.calls.filter(call => call.channel === 'requestAccepted').length, 0);
  releasePut(); assert.equal(await disabling, true);
  transfer.hide(); second.hide();
});

for (const change of ['manager', 'trust', 'online']) test(`a target ${change} change during journal staging revokes dispatch`, async () => {
  const { state, client, transfer } = fixture(); await enableTransfer(transfer);
  let releaseJournal, enteredJournal;
  const entered = new Promise(resolve => { enteredJournal = resolve; }); let first = true;
  state.writeJournal = async () => {
    if (!first) return;
    first = false; enteredJournal(); await new Promise(resolve => { releaseJournal = resolve; });
  };
  const starting = transfer.start('target'); await entered;
  client.machines = client.machines.map(machine => machine.id === 'target'
    ? row('target', change === 'manager' ? 'manager-b' : 'manager-a', { online: change !== 'online', trust: change === 'trust' ? 'pending' : 'confirmed' }) : machine);
  releaseJournal(); const record = await starting;
  assert.equal(record.state, 'failed');
  assert.equal(state.calls.filter(call => call.channel === 'requestAccepted').length, 0);
  transfer.hide();
});

for (const machine of ['source', 'target']) test(`a hidden staged request cannot outlive a settled ${machine} setting mutation`, async () => {
  const { state, client, transfer } = fixture(); await enableTransfer(transfer);
  const second = new FileTransfer(); second.bind(client, 'source', 'manager-a'); await openReadySource(second);
  let releaseJournal, enteredJournal; const entered = new Promise(resolve => { enteredJournal = resolve; }); let first = true;
  state.writeJournal = async () => {
    if (!first) return;
    first = false; enteredJournal(); await new Promise(resolve => { releaseJournal = resolve; });
  };
  const starting = transfer.start('target'); await entered; transfer.hide();
  assert.equal(await second.setEnabled(machine, false), true);
  assert.equal(transfer.sharedJournal.settingWrites.size, 0, 'the write has fully settled before the journal resumes');
  releaseJournal(); const record = await starting;
  assert.equal(record.state, 'failed');
  assert.equal(state.calls.filter(call => call.channel === 'requestAccepted').length, 0);
  second.hide();
});
