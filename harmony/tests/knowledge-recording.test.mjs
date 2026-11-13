import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

const recordingSdkFiles = new Map();
const recordingSdkDirectories = new Set();
const recordingFileIo = {
  statSync(path) {
    if (recordingSdkDirectories.has(path) || recordingSdkFiles.has(path)) return {};
    throw new Error('not_found');
  },
  mkdirSync(path) { recordingSdkDirectories.add(path); },
  listFileSync(directory) {
    const prefix = directory + '/';
    return [...recordingSdkFiles.keys()].filter((path) => path.startsWith(prefix)).map((path) => path.slice(prefix.length));
  },
  readTextSync(path) {
    if (!recordingSdkFiles.has(path)) throw new Error('not_found');
    return recordingSdkFiles.get(path);
  },
  unlinkSync(path) {
    if (!recordingSdkFiles.delete(path)) throw new Error('not_found');
  }
};
globalThis.__recordingNativeTest = { fileIo: recordingFileIo };

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/KnowledgeRecording.ets') && specifier === './Wire')
      return { url: new URL('./Wire.ets', context.parentURL).href, shortCircuit: true };
    if (context.parentURL?.endsWith('/KnowledgeRecording.ets') && specifier === '../platform/NativeRecording')
      return { url: new URL('../platform/NativeRecording.ets', context.parentURL).href, shortCircuit: true };
    if (context.parentURL?.endsWith('/NativeRecording.ets') && specifier.startsWith('@kit.'))
      return { url: 'recording-sdk:' + specifier, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.startsWith('recording-sdk:')) {
      const specifier = url.slice('recording-sdk:'.length);
      const sources = {
        '@kit.AbilityKit': 'export const abilityAccessCtrl = {}; export const common = {}; export const Permissions = {};',
        '@kit.AudioKit': 'export const audio = {};',
        '@kit.CoreSpeechKit': 'export const speechRecognizer = {};',
        '@kit.CoreFileKit': 'export const fileIo = globalThis.__recordingNativeTest.fileIo;',
        '@kit.ArkTS': 'export const util = {};'
      };
      return { format: 'module', source: sources[specifier], shortCircuit: true };
    }
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { KnowledgeRecording } = await import('../entry/src/main/ets/models/KnowledgeRecording.ets');

function clone(value) { return structuredClone(value); }
const models = new Set();

function newModel(platform) {
  const model = new KnowledgeRecording(platform);
  models.add(model);
  return model;
}

async function disposeModels() {
  for (const model of models) await model.dispose();
  models.clear();
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

class RecordingPlatformStub {
  records = new Map();
  activeId = '';
  bytes = 64000;
  transcribeCalls = [];
  removed = [];
  cancelCalls = 0;
  transcribeResult = '你好';
  transcribeGate = undefined;
  nextSave = undefined;

  async initialize() {}
  async ensureMicrophonePermission() {}
  async start(_context, id) { this.activeId = id; return '/private/' + id + '.pcm'; }
  async stop() {
    const id = this.activeId; this.activeId = '';
    return { path: id ? '/private/' + id + '.pcm' : '', bytes: id ? this.bytes : 0, duration: id ? this.bytes / 32000 : 0 };
  }
  async save(entry) {
    if (this.nextSave && this.nextSave.matches(entry)) {
      const pending = this.nextSave; this.nextSave = undefined;
      pending.started.resolve(); await pending.release.promise;
    }
    this.records.set(entry.id, clone(entry));
  }
  async load(profileId, hubId, managerId) {
    return [...this.records.values()].filter((entry) => entry.profileId === profileId && entry.hubId === hubId && entry.managerId === managerId).map(clone);
  }
  async transcribe(id, language) {
    this.transcribeCalls.push({ id, language });
    if (this.transcribeGate) return this.transcribeGate.promise;
    if (this.transcribeResult instanceof Error) throw this.transcribeResult;
    return this.transcribeResult;
  }
  async cancelTranscription() {
    this.cancelCalls++;
    this.transcribeGate?.reject(new Error('transcription_cancelled'));
    this.transcribeGate = undefined;
  }
  async remove(id) { this.removed.push(id); this.records.delete(id); }
  blockNextUploadSave() {
    return this.blockNextSave((entry) => entry.uploadState === 'uploading');
  }
  blockNextTranscriptionSave() {
    return this.blockNextSave((entry) => entry.transcriptionState === 'transcribing');
  }
  blockNextSave(matches) {
    const pending = { matches, started: deferred(), release: deferred() };
    this.nextSave = pending;
    return pending;
  }
  async forgetProfile(profileId) {
    for (const entry of [...this.records.values()]) if (entry.profileId === profileId) await this.remove(entry.id);
  }
}

function makeClient(overrides = {}) {
  const profile = {
    id: overrides.id || 'profile-1', hubId: overrides.hubId || 'hub-1', manager: overrides.manager || 'manager-1',
    baseUrl: 'https://hub.invalid', pin: 'pin-1'
  };
  const state = { connected: overrides.connected ?? true, generation: 3, children: new Map(), calls: [] };
  const repository = { transport: { async request(path, method, body) {
    const call = { path, method, body };
    state.calls.push(call);
    if (overrides.request) return overrides.request(call);
    const data = JSON.parse(body);
    return JSON.stringify({ id: path.slice('/api/v1/recordings/'.length), revision: data.revision, status: 'stored' });
  } } };
  const client = {
    connected: state.connected, demo: false, profile,
    scope() { return JSON.stringify([profile.id, profile.hubId, profile.manager]); },
    connectionGeneration() { return state.generation; },
    owns(generation, scope) { return this.connected && generation === state.generation && scope === this.scope(); },
    async viewFor(row) { return state.children.get(row.manager) || client; },
    repo() { return repository; }
  };
  state.client = client; state.repository = repository;
  return state;
}

async function opened(options = {}) {
  const platform = new RecordingPlatformStub();
  const state = makeClient(options);
  const model = newModel(platform);
  await model.open(state.client, options.ownerManager || state.client.profile.manager, { filesDir: '/private' });
  return { model, platform, state };
}

async function startAndStop(model) {
  assert.equal(await model.start(), true);
  const id = model.entry.id;
  assert.equal(model.captureState, 'recording');
  assert.equal(await model.stop(), true);
  assert.equal(model.captureState, 'stopped');
  return id;
}

test('stop closes the capture into a durable local recording without uploading audio', async () => {
  const { model, platform, state } = await opened();
  const id = await startAndStop(model);
  assert.equal(model.entry.audioBytes, 64000);
  assert.equal(model.entry.duration, 2);
  assert.equal(platform.records.get(id).captureState, 'stopped');
  assert.deepEqual(state.calls, []);
});

test('transcription failure keeps local audio and an explicit retry can transcribe and sync text', async () => {
  const { model, platform, state } = await opened();
  const id = await startAndStop(model);
  platform.transcribeResult = new Error('speech_engine_failed');
  assert.equal(await model.transcribeAndUpload(), false);
  assert.equal(model.transcriptionState, 'failed');
  assert.equal(platform.records.get(id).audioBytes, 64000);
  assert.deepEqual(platform.removed, []);

  platform.transcribeResult = '你好，世界。';
  assert.equal(await model.transcribeAndUpload(), true);
  assert.equal(model.transcriptionState, 'complete');
  assert.equal(model.uploadState, 'stored');
  assert.equal(state.calls.length, 1);
  const call = state.calls[0], body = JSON.parse(call.body);
  assert.equal(call.path, '/api/v1/recordings/' + id);
  assert.equal(call.method, 'PUT');
  assert.equal(body.device, 'phone');
  assert.equal(body.complete, true);
  assert.deepEqual(body.segments, [{ start: 0, end: 2, text: '你好，世界。' }]);
  assert.equal(Object.hasOwn(body, 'audio'), false);
});

test('an unknown PUT is journaled and restart retries the identical id, revision and body', async () => {
  const timeout = new Error('connection_lost'); timeout.status = 0; timeout.resultUnknown = true;
  const first = await opened({ request: async () => { throw timeout; } });
  const id = await startAndStop(first.model);
  assert.equal(await first.model.transcribeAndUpload(), false);
  assert.equal(first.model.uploadState, 'unknown');
  const originalCall = first.state.calls[0];
  const originalBody = JSON.parse(originalCall.body);

  const reopened = newModel(first.platform);
  const online = makeClient();
  await reopened.open(online.client, online.client.profile.manager, { filesDir: '/private' });
  assert.equal(reopened.entry.id, id);
  assert.equal(reopened.uploadState, 'unknown');
  assert.equal(await reopened.retryUpload(), true);
  assert.equal(online.calls.length, 1);
  assert.equal(online.calls[0].path, originalCall.path);
  assert.equal(online.calls[0].method, originalCall.method);
  assert.deepEqual(JSON.parse(online.calls[0].body), originalBody);
  assert.equal(reopened.uploadState, 'stored');
});

test('restart turns an interrupted upload into a retryable unknown without sending automatically', async () => {
  const first = await opened();
  const id = await startAndStop(first.model);
  assert.equal(await first.model.transcribeAndUpload(), true);
  const saved = clone(first.platform.records.get(id));
  const body = clone(saved.uploadBody);
  saved.uploadState = 'uploading'; saved.uploadError = ''; saved.serverStatus = ''; saved.serverRevision = 0;
  await first.platform.save(saved);

  const online = makeClient();
  const reopened = newModel(first.platform);
  await reopened.open(online.client, online.client.profile.manager, { filesDir: '/private' });
  assert.equal(reopened.uploadState, 'unknown');
  assert.equal(online.calls.length, 0);
  assert.equal(await reopened.retryUpload(), true);
  assert.equal(online.calls.length, 1);
  assert.deepEqual(JSON.parse(online.calls[0].body), body);
});

test('a deleted server recording keeps its local transcript and does not offer futile retries', async () => {
  const gone = new Error('recording_deleted'); gone.status = 410;
  const { model, platform, state } = await opened({ request: async () => { throw gone; } });
  const id = await startAndStop(model);
  assert.equal(await model.transcribeAndUpload(), false);
  assert.equal(model.transcriptionState, 'complete');
  assert.equal(model.uploadState, 'deleted');
  assert.equal(model.entry.uploadError, 'knowledge_recording_deleted');
  assert.equal(model.canRetryUpload(), false);
  assert.equal(platform.records.get(id).transcript, '你好');
  assert.equal(state.calls.length, 1);
});

test('local deletion serializes behind a pending journal save and blocks late resurrection', async () => {
  const { model, platform, state } = await opened();
  const id = await startAndStop(model);
  platform.transcribeResult = '删除竞态';
  const save = platform.blockNextUploadSave();
  const sync = model.transcribeAndUpload();
  await save.started.promise;
  assert.equal(model.canDeleteLocal(), true);
  const deletion = model.deleteLocal();
  save.release.resolve();
  assert.equal(await deletion, true);
  assert.equal(await sync, false);
  assert.equal(platform.records.has(id), false);
  assert.equal(model.entry, undefined);
  assert.equal(state.calls.length, 0);
});

test('a forgotten scope cannot retry its journal into another profile', async () => {
  const timeout = new Error('connection_lost'); timeout.status = 0; timeout.resultUnknown = true;
  const { model, platform, state } = await opened({ request: async () => { throw timeout; } });
  const id = await startAndStop(model);
  assert.equal(await model.transcribeAndUpload(), false);
  assert.equal(state.calls.length, 1);

  state.client.connected = false;
  state.client.profile.id = '';
  state.client.profile.hubId = '';
  state.client.profile.manager = '';
  state.generation++;
  assert.equal(await model.retryUpload(), false);
  assert.equal(state.calls.length, 1);

  const replacement = makeClient({ id: 'profile-2', hubId: 'hub-2', manager: 'manager-2' });
  await model.open(replacement.client, replacement.client.profile.manager, { filesDir: '/private' });
  assert.equal(model.entries.length, 0);
  assert.ok(platform.records.has(id));
  assert.deepEqual(replacement.calls, []);
});

test('previous recordings can be selected while idle, but selection and capture stay locked during upload', async () => {
  const { model, state } = await opened();
  const earlierId = await startAndStop(model);
  const latestId = await startAndStop(model);
  assert.equal(model.entries.length, 2);
  assert.equal(model.entry.id, latestId);
  assert.equal(model.canSelectEntry(), true);
  model.selectEntry(earlierId);
  assert.equal(model.entry.id, earlierId);
  model.selectEntry(latestId);

  const requestStarted = deferred(), requestGate = deferred();
  state.repository.transport.request = async (path, method, body) => {
    requestStarted.resolve();
    await requestGate.promise;
    const data = JSON.parse(body);
    return JSON.stringify({ id: path.slice('/api/v1/recordings/'.length), revision: data.revision, status: 'stored' });
  };
  const upload = model.transcribeAndUpload();
  await requestStarted.promise;
  assert.equal(model.uploadState, 'uploading');
  assert.equal(model.canStart(), false);
  assert.equal(model.canSelectEntry(), false);
  model.selectEntry(earlierId);
  assert.equal(model.entry.id, latestId);
  requestGate.resolve();
  assert.equal(await upload, true);
  assert.equal(model.canSelectEntry(), true);
  model.selectEntry(earlierId);
  assert.equal(model.entry.id, earlierId);
});

test('backgrounding stops capture and cancels an active transcription without syncing', async () => {
  const { model, platform, state } = await opened();
  assert.equal(await model.start(), true);
  const id = model.entry.id;
  await model.stopForBackground();
  assert.equal(model.captureState, 'stopped');
  assert.equal(platform.records.get(id).captureState, 'stopped');

  const gate = deferred(); platform.transcribeGate = gate;
  const transcription = model.transcribeAndUpload();
  for (let turn = 0; turn < 100 && platform.transcribeCalls.length === 0; turn++) await Promise.resolve();
  assert.equal(platform.transcribeCalls.length, 1);
  assert.equal(model.transcriptionState, 'transcribing');
  await model.stopForBackground();
  assert.equal(platform.cancelCalls, 1);
  assert.equal(model.transcriptionState, 'idle');
  assert.equal(platform.records.get(id).captureState, 'stopped');
  assert.deepEqual(state.calls, []);
  assert.equal(await transcription, false);
});

test('backgrounding during the transcription journal write does not start ASR afterward', async () => {
  const { model, platform, state } = await opened();
  const id = await startAndStop(model);
  const save = platform.blockNextTranscriptionSave();
  const transcription = model.transcribeAndUpload();
  await save.started.promise;
  await model.stopForBackground();
  save.release.resolve();
  assert.equal(await transcription, false);
  assert.equal(platform.transcribeCalls.length, 0);
  assert.equal(platform.records.get(id).transcriptionState, 'idle');
  assert.deepEqual(state.calls, []);
});

test('global background hook stops every registered capture while preserving its local file', async () => {
  const first = await opened(), second = await opened({ id: 'profile-2', hubId: 'hub-2', manager: 'manager-2' });
  assert.equal(await first.model.start(), true);
  assert.equal(await second.model.start(), true);
  const firstId = first.model.entry.id, secondId = second.model.entry.id;
  await KnowledgeRecording.backgroundAll();
  assert.equal(first.model.captureState, 'stopped');
  assert.equal(second.model.captureState, 'stopped');
  assert.equal(first.platform.records.get(firstId).captureState, 'stopped');
  assert.equal(second.platform.records.get(secondId).captureState, 'stopped');
  assert.deepEqual(first.state.calls, []);
  assert.deepEqual(second.state.calls, []);
});

test('forget retires matching models before pending saves finish and preserves other profiles', async () => {
  const forgotten = await opened(), other = await opened({ id: 'profile-2', hubId: 'hub-2', manager: 'manager-2' });
  await forgotten.model.start();
  const forgottenId = forgotten.model.entry.id;
  await forgotten.model.stop();
  await other.model.start();
  const otherId = other.model.entry.id;

  const save = forgotten.platform.blockNextUploadSave();
  const sync = forgotten.model.transcribeAndUpload();
  await save.started.promise;
  const forgetting = KnowledgeRecording.forgetProfile('profile-1', { filesDir: '/private' });
  assert.equal(forgotten.model.entry, undefined);
  assert.equal(other.model.entry.id, otherId);
  save.release.resolve();
  await forgetting;
  assert.equal(await sync, false);
  assert.equal(forgotten.platform.records.has(forgottenId), false);
  assert.equal(forgotten.state.calls.length, 0);
  assert.equal(other.platform.records.get(otherId).profileId, 'profile-2');
  assert.equal(other.model.captureState, 'recording');

  const repaired = newModel(forgotten.platform);
  await repaired.open(forgotten.state.client, forgotten.state.client.profile.manager, { filesDir: '/private' });
  assert.equal(repaired.entries.length, 0);
  await KnowledgeRecording.backgroundAll();
});

test('forget purges persisted files when no recording model was opened this launch', async () => {
  await disposeModels();
  recordingSdkFiles.clear(); recordingSdkDirectories.clear();
  const context = { filesDir: '/prior-session' }, directory = '/prior-session/knowledge-recordings';
  recordingSdkDirectories.add(directory);
  const forgottenId = '00000000-0000-4000-8000-000000000001';
  const otherId = '00000000-0000-4000-8000-000000000002';
  const manifest = (id, profileId) => JSON.stringify({ id, profileId, hubId: 'hub-' + profileId, managerId: 'manager-1' });
  recordingSdkFiles.set(directory + '/' + forgottenId + '.json', manifest(forgottenId, 'profile-1'));
  recordingSdkFiles.set(directory + '/' + forgottenId + '.pcm', 'profile 1 audio');
  recordingSdkFiles.set(directory + '/' + otherId + '.json', manifest(otherId, 'profile-2'));
  recordingSdkFiles.set(directory + '/' + otherId + '.pcm', 'profile 2 audio');

  await KnowledgeRecording.forgetProfile('profile-1', context);
  assert.equal(recordingSdkFiles.has(directory + '/' + forgottenId + '.json'), false);
  assert.equal(recordingSdkFiles.has(directory + '/' + forgottenId + '.pcm'), false);
  assert.equal(recordingSdkFiles.has(directory + '/' + otherId + '.json'), true);
  assert.equal(recordingSdkFiles.has(directory + '/' + otherId + '.pcm'), true);
});
