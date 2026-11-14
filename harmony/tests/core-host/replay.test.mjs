// The generated ArkTS codec against the real core, on the host (#7450). Every action is encoded by the generated
// codec (Wire.ts), applied by the real runtime through persona-core-ohos's registry, the code under the NAPI entry
// points, and every effect and patch is decoded by the generated codec again. The registry runs in a helper process:
// native/app-core/crates/persona-core-ohos/examples/host_replay.rs, whose header documents the frames used here.
//
// This is host evidence only. It does not load the NAPI library, build a HAP or run on a device; the on-device
// `coreReplay` (entry/src/ohosTest/ets/test/NativeCore.test.ets) is still the only test of those.
//
// It needs the helper, so it is not one of the portable tests/*.test.mjs files:
//
//   (cd ../native/app-core && cargo build --locked -p persona-core-ohos --example host_replay)
//   PERSONA_CORE_HOST_REPLAY=../native/app-core/target/debug/examples/host_replay node --test tests/core-host/replay.test.mjs
import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import nodeModule from 'node:module';
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { isDeepStrictEqual } from 'node:util';

// A missing prerequisite fails this file. It never skips: a skipped replay would read as a passing one.
if (typeof nodeModule.registerHooks !== 'function' || typeof nodeModule.stripTypeScriptTypes !== 'function') {
  throw new Error('The ArkTS core tests load the generated codec with module.registerHooks and ' +
    'module.stripTypeScriptTypes, which need Node.js 22.15 or newer (23.5 or newer on the 23 line). This is ' + process.version + '.');
}
const helperPath = process.env.PERSONA_CORE_HOST_REPLAY;
if (!helperPath) {
  throw new Error('PERSONA_CORE_HOST_REPLAY is not set. Build the helper with `cargo build --locked -p persona-core-ohos ' +
    '--example host_replay` in native/app-core and set the variable to target/debug/examples/host_replay there.');
}
if (!existsSync(helperPath)) {
  throw new Error('PERSONA_CORE_HOST_REPLAY names a file that does not exist: ' + helperPath);
}

const wireUrl = new URL('../../entry/src/main/ets/core/rustcore/Wire.ts', import.meta.url);
const wireSource = readFileSync(wireUrl, 'utf8');
nodeModule.registerHooks({
  load(url, context, next) {
    if (url === wireUrl.href) return {format: 'module', shortCircuit: true, source: nodeModule.stripTypeScriptTypes(wireSource)};
    return next(url, context);
  },
});
const wire = await import(wireUrl.href);

// The helper's frames: a u32 little-endian length, a tag byte, then the fields.
const MAX_FRAME = 16 * 1024 * 1024;
const Command = {config: 1, create: 2, dispatch: 3, flush: 4, drain: 5, close: 6, reencode: 7, samples: 8};
const Record = {ok: 1, error: 2, changed: 3, effect: 4, sample: 5, end: 6};
const WireType = {Action: 1, ChatEvent: 2, ChatPatch: 3, ChatSnapshot: 4, Config: 5, Effect: 6, ListOp: 7, Request: 8, WireBatch: 9};

function frame(tag, ...fields) {
  const body = Buffer.concat([Buffer.of(tag), ...fields]);
  const prefix = Buffer.alloc(4);
  prefix.writeUInt32LE(body.length);
  return Buffer.concat([prefix, body]);
}
function handleField(handle) {
  const field = Buffer.alloc(8);
  field.writeBigInt64LE(BigInt(handle));
  return field;
}
function hex(bytes) {
  return Buffer.from(bytes).toString('hex');
}

// The NAPI entry points' shape (coreConfig, coreCreate, coreDispatch, coreFlush, coreDrain, coreClose), answered by
// the helper. The callbacks the real binding would deliver arrive as records and are kept in the order they happened.
class HostCore {
  #child;
  #pending = Buffer.alloc(0);
  #records = [];
  #wake = null;
  #failure = null;
  #exit = null;
  #stderr = '';
  #callbacks = [];
  #samples = new Map();

  constructor(path) {
    this.#child = spawn(path, [], {stdio: ['pipe', 'pipe', 'pipe']});
    this.#child.stdout.on('data', (chunk) => this.#received(chunk));
    this.#child.stderr.on('data', (chunk) => { this.#stderr += chunk; });
    // A helper that has gone away is reported by its exit, with its stderr, not by the write that noticed.
    this.#child.stdin.on('error', () => {});
    this.#child.on('error', (error) => { this.#failure ??= error; this.#notify(); });
    this.#child.on('close', (code, signal) => { this.#exit = {code, signal}; this.#notify(); });
  }

  #notify() {
    const wake = this.#wake;
    this.#wake = null;
    if (wake) wake();
  }

  #received(chunk) {
    this.#pending = Buffer.concat([this.#pending, chunk]);
    while (this.#failure === null && this.#pending.length >= 4) {
      const length = this.#pending.readUInt32LE(0);
      if (length === 0 || length > MAX_FRAME) {
        this.#failure = new Error('the helper wrote a frame of ' + length + ' bytes');
        break;
      }
      if (this.#pending.length < 4 + length) break;
      this.#records.push(this.#pending.subarray(4, 4 + length));
      this.#pending = this.#pending.subarray(4 + length);
    }
    this.#notify();
  }

  async #record() {
    for (;;) {
      if (this.#failure !== null) throw this.#failure;
      const record = this.#records.shift();
      if (record !== undefined) return record;
      if (this.#exit !== null) {
        throw new Error('the helper exited (' + (this.#exit.signal ?? this.#exit.code) + ') before answering: ' + this.#stderr.trim());
      }
      await new Promise((resolve) => { this.#wake = resolve; });
    }
  }

  // Sends one command and reads up to its result, keeping the callback and sample records written before it.
  async #call(tag, ...fields) {
    this.#child.stdin.write(frame(tag, ...fields));
    for (;;) {
      const record = await this.#record();
      const body = record.subarray(1);
      switch (record[0]) {
        case Record.ok: return new Uint8Array(body);
        case Record.error: throw new Error('the core refused: ' + body.toString('utf8'));
        case Record.changed:
          assert.equal(body.length, 16, 'a changed record is a handle and a version');
          this.#callbacks.push({kind: 'changed', handle: Number(body.readBigInt64LE(0)), version: body.readBigUInt64LE(8)});
          break;
        case Record.effect:
          this.#callbacks.push({kind: 'effect', handle: Number(body.readBigInt64LE(0)), bytes: new Uint8Array(body.subarray(8))});
          break;
        case Record.sample: {
          const name = body.subarray(1, 1 + body[0]).toString('utf8');
          assert.ok(!this.#samples.has(name), 'the helper names each sample once: ' + name);
          this.#samples.set(name, new Uint8Array(body.subarray(1 + body[0])));
          break;
        }
        default: throw new Error('the helper wrote an unknown record: ' + record[0]);
      }
    }
  }

  async coreConfig() { return this.#call(Command.config); }
  async coreCreate(config) {
    const handle = Number(Buffer.from(await this.#call(Command.create, config)).readBigInt64LE(0));
    assert.ok(Number.isSafeInteger(handle) && handle > 0, 'a handle is a positive integer');
    return handle;
  }
  async coreDispatch(handle, action) { return this.#flag(await this.#call(Command.dispatch, handleField(handle), action)); }
  async coreFlush(handle, timeoutMillis) {
    const timeout = Buffer.alloc(4);
    timeout.writeUInt32LE(timeoutMillis);
    return this.#flag(await this.#call(Command.flush, handleField(handle), timeout));
  }
  async coreDrain(handle, chat) { return this.#call(Command.drain, handleField(handle), Buffer.from(chat, 'utf8')); }
  async coreClose(handle) { assert.equal((await this.#call(Command.close, handleField(handle))).length, 0); }

  // Rust's encoding of the value Rust decoded from `bytes`.
  async reencode(type, bytes) { return this.#call(Command.reencode, Buffer.of(WireType[type]), bytes); }
  // The helper's Rust-built values, by name, as Rust encoded them.
  async samples() {
    await this.#call(Command.samples);
    return this.#samples;
  }

  #flag(value) {
    assert.ok(value.length === 1 && value[0] <= 1, 'a flag is one byte, 0 or 1');
    return value[0] === 1;
  }

  // The callbacks since the last call, in order. Every one must belong to `handle`.
  takeCallbacks(handle) {
    const callbacks = this.#callbacks;
    this.#callbacks = [];
    for (const callback of callbacks) assert.equal(callback.handle, handle, 'a callback for another handle');
    return callbacks;
  }

  // Ends the script: the helper writes its `end` record and exits 0, with nothing left unread.
  async end() {
    this.#child.stdin.end();
    assert.equal((await this.#record())[0], Record.end, 'the helper ends with its end record');
    while (this.#exit === null && this.#failure === null) await new Promise((resolve) => { this.#wake = resolve; });
    if (this.#failure !== null) throw this.#failure;
    assert.deepEqual(this.#exit, {code: 0, signal: null}, this.#stderr.trim());
    assert.equal(this.#records.length + this.#pending.length + this.#callbacks.length, 0, 'nothing follows the end record');
  }

  kill() { this.#child.kill('SIGKILL'); }
}

const cores = [];
function startCore() {
  const core = new HostCore(helperPath);
  cores.push(core);
  return core;
}
after(() => { for (const core of cores) core.kill(); });

// Prefixes a failure with where it happened, keeping the assertion's own diff.
function at(label, check) {
  try {
    return check();
  } catch (error) {
    error.message = label + ': ' + error.message;
    throw error;
  }
}

// The same values as `samples()` in the helper, written by hand here and built in Rust there. Neither side is
// generated from the other, and neither comes from the codec under test.
const sampleSnapshot = {items_json: '[{"id":"s#0","text":"snapshot é ✓ 🌍"}]', history: 'snapshot-history', covered: 4294967303, has_more: true, loaded_once: false};
const sampleBatch = {items_json: '[{"id":"b#0"}]', history: 'batch-history', from_seq: -3, to_seq: 8589934597, backlog: true, has_more: false, more_after: false};
const sampleBareBatch = {items_json: '[]', history: null, from_seq: null, to_seq: null, backlog: false, has_more: null, more_after: true};
const sampleInsert = {kind: 'insert', index: 258, id: '7#1', json: '{"id":"7#1","kind":"text"}'};
const sampleUpdate = {kind: 'update', index: 65537, id: '7#2', json: '{"id":"7#2","text":"new"}'};
const sampleRange = {kind: 'fetch_range', from_seq: 2, through_seq: 8589934600};
const expectedSamples = {
  'Action.open_chat': {kind: 'open_chat', chat: 'chat/open', snapshot: sampleSnapshot},
  'Action.close_chat': {kind: 'close_chat', chat: 'chat/close'},
  'Action.chat': {kind: 'chat', chat: 'chat/event', event: {kind: 'open_fold', fold_id: 'fold:3#0'}},
  'Action.continue_fill': {kind: 'continue_fill', chat: 'chat/fill'},
  'ChatEvent.latest': {kind: 'latest', batch: sampleBatch},
  'ChatEvent.pushed': {kind: 'pushed', batch: sampleBareBatch},
  'ChatEvent.after': {kind: 'after', batch: sampleBatch},
  'ChatEvent.reloaded': {kind: 'reloaded', items_json: '[{"id":"r#0"}]', history: 'reloaded-history', to_seq: 77, has_more: true},
  'ChatEvent.reloaded_bare': {kind: 'reloaded', items_json: '[]', history: null, to_seq: null, has_more: false},
  'ChatEvent.older': {kind: 'older', items_json: '[{"id":"o#0"}]', has_more: true},
  'ChatEvent.range': {kind: 'range', items_json: '[{"id":"g#0"}]', more: false},
  'ChatEvent.reconnected': {kind: 'reconnected'},
  'ChatEvent.load_older': {kind: 'load_older'},
  'ChatEvent.open_fold': {kind: 'open_fold', fold_id: 'fold:12#0'},
  'ChatEvent.fill_failed': {kind: 'fill_failed'},
  'ChatEvent.older_failed': {kind: 'older_failed'},
  'ChatEvent.range_failed': {kind: 'range_failed'},
  'ChatPatch.full': {version: 4294967298, covered: -9, history: 'patch-history', has_more: true, loaded_once: false,
    ops: [{kind: 'remove', index: 3}, sampleInsert, sampleUpdate]},
  'ChatPatch.bare': {version: 0, covered: 0, history: null, has_more: null, loaded_once: true, ops: []},
  'ChatSnapshot.full': sampleSnapshot,
  'ChatSnapshot.bare': {items_json: '', history: null, covered: -2, has_more: null, loaded_once: true},
  'Config.default': {},
  'Effect.fetch': {kind: 'fetch', chat: 'chat/effect', request: sampleRange},
  'Effect.rejected': {kind: 'rejected', reason: 'rejected ✗'},
  'ListOp.remove': {kind: 'remove', index: 4294967295},
  'ListOp.insert': sampleInsert,
  'ListOp.update': sampleUpdate,
  'Request.fetch_after': {kind: 'fetch_after', after_seq: 4294967301},
  'Request.fetch_latest': {kind: 'fetch_latest', fold_work: true},
  'Request.subscribe': {kind: 'subscribe', since_seq: 41, fold_work: false},
  'Request.subscribe_from_start': {kind: 'subscribe', since_seq: null, fold_work: true},
  'Request.fetch_older': {kind: 'fetch_older', before_seq: 12, fold_work: false},
  'Request.fetch_range': sampleRange,
  'WireBatch.full': sampleBatch,
  'WireBatch.bare': sampleBareBatch,
};

// Every type the generated codec declares, with the variants of each union: what the samples have to cover.
function declaredShapes() {
  const shapes = new Map();
  for (const match of wireSource.matchAll(/^export interface (\w+) \{/gm)) shapes.set(match[1], null);
  for (const match of wireSource.matchAll(/^export type (\w+) =\n((?: {2}\| .*\n)+)/gm)) {
    const kinds = [...match[2].matchAll(/readonly kind: "(\w+)"/g)].map((kind) => kind[1]);
    assert.equal(kinds.length, match[2].trimEnd().split('\n').length, match[1] + ': every variant declares its kind');
    shapes.set(match[1], kinds);
  }
  return shapes;
}

test('ArkTS and Rust write the same bytes for every wire shape, and read them as the same values', {timeout: 60_000}, async () => {
  const core = startCore();
  const rustSamples = await core.samples();
  assert.deepEqual([...rustSamples.keys()].sort(), Object.keys(expectedSamples).sort(), 'the helper and this test name the same samples');

  const shapes = declaredShapes();
  assert.deepEqual([...shapes.keys()].sort(), Object.keys(WireType).sort(), 'every declared wire type has a helper type number');
  for (const [type, kinds] of shapes) {
    const values = Object.entries(expectedSamples).filter(([name]) => name.startsWith(type + '.')).map(([, value]) => value);
    assert.ok(values.length > 0, type + ' has no sample');
    for (const kind of kinds ?? []) assert.ok(values.some((value) => value.kind === kind), type + '.' + kind + ' has no sample');
  }

  // Every sample is checked before any failure is reported, so one run names all the shapes that disagree.
  const disagreements = [];
  for (const [name, value] of Object.entries(expectedSamples)) {
    const type = name.slice(0, name.indexOf('.'));
    const rust = rustSamples.get(name);
    // Rust to ArkTS: the bytes Rust wrote for its own value decode to the value written here.
    try {
      const read = wire['decode' + type](rust);
      if (!isDeepStrictEqual(read, value)) disagreements.push(name + ': ArkTS reads Rust\'s ' + hex(rust) + ' as ' + JSON.stringify(read));
    } catch (error) {
      disagreements.push(name + ': ArkTS cannot read Rust\'s ' + hex(rust) + ': ' + error.message);
    }
    // ArkTS to Rust: the value written here encodes to exactly the bytes Rust wrote, and Rust reads them back.
    try {
      const arkts = wire['encode' + type](value);
      if (hex(arkts) !== hex(rust)) disagreements.push(name + ': ArkTS writes ' + hex(arkts) + ', Rust writes ' + hex(rust));
      else assert.equal(hex(await core.reencode(type, arkts)), hex(rust));
    } catch (error) {
      disagreements.push(name + ': ' + error.message);
    }
  }
  assert.equal(disagreements.length, 0, 'ArkTS and Rust disagree on ' + disagreements.length + ' of ' +
    rustSamples.size * 2 + ' checks:\n' + disagreements.join('\n'));
  await core.end();
});

// The fixture event -> Action mapping of NativeCore.test.ets, which this replay mirrors on the host.
function batch(event) {
  return {items_json: JSON.stringify(event.items ?? []), history: event.history ?? null, from_seq: event.from_seq ?? null,
    to_seq: event.to_seq ?? null, backlog: event.backlog === true, has_more: event.has_more ?? null, more_after: event.more_after === true};
}
function eventAction(event, chat) {
  const items_json = JSON.stringify(event.items ?? []);
  switch (event.kind) {
    case 'continue_fill': return {kind: 'continue_fill', chat};
    case 'http_latest': return {kind: 'chat', chat, event: {kind: 'latest', batch: batch(event)}};
    case 'ws_items': return {kind: 'chat', chat, event: {kind: 'pushed', batch: batch(event)}};
    case 'http_after': return {kind: 'chat', chat, event: {kind: 'after', batch: batch(event)}};
    case 'http_reload': return {kind: 'chat', chat, event: {kind: 'reloaded', items_json, history: event.history ?? null,
      to_seq: event.to_seq ?? null, has_more: event.has_more === true}};
    case 'http_older': return {kind: 'chat', chat, event: {kind: 'older', items_json, has_more: event.has_more === true}};
    case 'http_range': return {kind: 'chat', chat, event: {kind: 'range', items_json, more: event.more === true}};
    case 'reconnect': return {kind: 'chat', chat, event: {kind: 'reconnected'}};
    case 'load_older': return {kind: 'chat', chat, event: {kind: 'load_older'}};
    case 'open_fold': return {kind: 'chat', chat, event: {kind: 'open_fold', fold_id: event.fold_id ?? ''}};
    default: throw new Error('unknown fixture event: ' + event.kind);
  }
}
function requestObject(request) {
  switch (request.kind) {
    case 'fetch_after': return {kind: request.kind, after_seq: request.after_seq};
    case 'fetch_latest': return {kind: request.kind, fold_work: request.fold_work};
    case 'subscribe': return request.since_seq === null ? {kind: request.kind, fold_work: request.fold_work}
      : {kind: request.kind, fold_work: request.fold_work, since_seq: request.since_seq};
    case 'fetch_older': return {kind: request.kind, before_seq: request.before_seq, fold_work: request.fold_work};
    case 'fetch_range': return {kind: request.kind, from_seq: request.from_seq, through_seq: request.through_seq};
    default: throw new Error('unknown request: ' + request.kind);
  }
}

// One open chat on one handle: dispatches ArkTS-encoded actions, and after each flush decodes what the core sent
// back, applying the patch's operations to the list a client would hold.
class ReplayedChat {
  list = [];
  #version = 0n;

  constructor(core, handle, chat, label) {
    this.core = core;
    this.handle = handle;
    this.chat = chat;
    this.label = label;
  }

  async dispatch(action) {
    const bytes = wire.encodeAction(action);
    // Rust reads these bytes as an Action and writes the same bytes back: nothing was dropped or left over.
    assert.equal(hex(await this.core.reencode('Action', bytes)), hex(bytes), this.label + ': Rust re-encodes the action differently');
    assert.equal(await this.core.coreDispatch(this.handle, bytes), true, this.label + ': dispatch');
  }

  // Returns the drained patch and the requests the core made since the last settle.
  async settle() {
    assert.equal(await this.core.coreFlush(this.handle, 5000), true, this.label + ': the worker did not finish');
    const requests = [];
    for (const callback of this.core.takeCallbacks(this.handle)) {
      if (callback.kind === 'changed') {
        assert.ok(callback.version > this.#version, this.label + ': wake-up versions grow');
        this.#version = callback.version;
        continue;
      }
      const effect = at(this.label + ' effect', () => wire.decodeEffect(callback.bytes));
      if (effect.kind === 'rejected') throw new Error(this.label + ': rejected: ' + effect.reason);
      assert.equal(effect.chat, this.chat, this.label + ': the effect names the chat');
      requests.push(requestObject(effect.request));
    }
    const bytes = await this.core.coreDrain(this.handle, this.chat);
    const patch = at(this.label + ' patch', () => wire.decodeChatPatch(bytes));
    // The wake-up's version reaches a client as a plain number and the patch's inside its bytes: they are one value.
    assert.equal(BigInt(patch.version), this.#version, this.label + ': the patch carries the last wake-up\'s version');
    for (const op of patch.ops) {
      if (op.kind === 'remove') this.list.splice(op.index, 1);
      else if (op.kind === 'insert') this.list.splice(op.index, 0, op.json);
      else this.list[op.index] = op.json;
    }
    return {patch, requests};
  }
}

test('every continuity case replays through the ArkTS codec and the real runtime', {timeout: 120_000}, async () => {
  const directory = new URL('../../../spec/fixtures/continuity/', import.meta.url);
  const files = readdirSync(directory).filter((name) => name.endsWith('.json')).sort();
  const core = startCore();
  const config = await core.coreConfig();
  assert.equal(hex(config), hex(wire.encodeConfig({})), 'the core\'s default config is the ArkTS codec\'s empty config');
  const chat = 'fixture/continuity';
  let cases = 0, steps = 0;
  for (const file of files) {
    const document = JSON.parse(readFileSync(new URL(file, directory), 'utf8'));
    for (const fixture of document.cases) {
      cases++;
      const handle = await core.coreCreate(config);
      const replay = new ReplayedChat(core, handle, chat, fixture.name + ' open');
      const initial = fixture.input.initial;
      await replay.dispatch({kind: 'open_chat', chat, snapshot: {items_json: JSON.stringify(initial.items ?? []),
        history: initial.history ?? null, covered: initial.covered ?? 0, has_more: initial.has_more ?? null,
        loaded_once: initial.loaded_once === true}});
      await replay.settle();
      assert.equal(fixture.input.events.length, fixture.expected.length, fixture.name + ': one expectation per event');
      for (let index = 0; index < fixture.input.events.length; index++) {
        const label = fixture.name + ' step ' + index;
        const expected = fixture.expected[index];
        replay.label = label;
        await replay.dispatch(eventAction(fixture.input.events[index], chat));
        const {patch, requests} = await replay.settle();
        // Compared as values, as the Rust host replay does. The core writes an item it changes (a promoted
        // fold) with its keys in another order, so comparing the text would fail on steps that are right.
        at(label + ' items', () => assert.deepStrictEqual(replay.list.map((json) => JSON.parse(json)), expected.items));
        assert.equal(patch.covered, expected.covered, label + ' covered');
        assert.equal(patch.history, expected.history, label + ' history');
        assert.equal(patch.has_more, expected.has_more, label + ' has_more');
        assert.equal(patch.loaded_once, expected.loaded_once, label + ' loaded_once');
        at(label + ' requests', () => assert.deepStrictEqual(requests, expected.actions));
        steps++;
      }
      await core.coreClose(handle);
      assert.deepEqual(core.takeCallbacks(handle), [], fixture.name + ': no callback after the last flush');
    }
  }
  await core.end();
  assert.equal(cases, 38, 'every continuity case replays');
  assert.equal(steps, 94, 'every continuity step replays');
});

test('a repeated item is one Update, and a closed handle stays closed', {timeout: 60_000}, async () => {
  const core = startCore();
  const chat = 'update';
  const item = (text) => '[{"id":"1#0","seq":1,"role":"assistant","kind":"text","text":"' + text + '"}]';
  const handle = await core.coreCreate(await core.coreConfig());
  const replay = new ReplayedChat(core, handle, chat, 'update');
  await replay.dispatch({kind: 'open_chat', chat, snapshot: {items_json: item('old'), history: 'h', covered: 1, has_more: false, loaded_once: true}});
  await replay.settle();
  await replay.dispatch({kind: 'chat', chat, event: {kind: 'pushed', batch: {items_json: item('newest'), history: 'h', from_seq: 0,
    to_seq: 1, backlog: false, has_more: null, more_after: false}}});
  const {patch} = await replay.settle();
  assert.equal(patch.ops.length, 1);
  assert.equal(patch.ops[0].kind, 'update');
  assert.equal(patch.ops[0].index, 0);
  assert.deepEqual(replay.list, [item('newest').slice(1, -1)]);
  await core.coreClose(handle);
  await core.coreClose(handle);
  await assert.rejects(core.coreDispatch(handle, wire.encodeAction({kind: 'continue_fill', chat})), /core is closed/);
  await assert.rejects(core.coreFlush(handle, 5000), /core is closed/);
  await assert.rejects(core.coreDrain(handle, chat), /core is closed/);
  await core.end();
});

test('the core rejects what it cannot use, and the helper ends on a malformed script', {timeout: 60_000}, async () => {
  // Refusals of a well-formed command come back in band, the way the NAPI entry points throw.
  const core = startCore();
  await assert.rejects(core.coreCreate(Uint8Array.of(255)), /the core refused/);
  await assert.rejects(core.reencode('Action', Uint8Array.of(9, 0, 0, 0)), /the core refused/);
  const handle = await core.coreCreate(await core.coreConfig());
  // Bytes that are no Action are still queued; the worker answers with a `rejected` effect and no wake-up.
  assert.equal(await core.coreDispatch(handle, Uint8Array.of(9, 0, 0, 0)), true);
  assert.equal(await core.coreFlush(handle, 5000), true);
  const callbacks = core.takeCallbacks(handle);
  assert.deepEqual(callbacks.map((callback) => callback.kind), ['effect']);
  const effect = wire.decodeEffect(callbacks[0].bytes);
  assert.equal(effect.kind, 'rejected');
  assert.match(effect.reason, /^undecodable action/);
  // The handle is left open: the end of the script closes it.
  await core.end();

  // A script that is not well formed ends the helper with status 2 and one line, never a panic.
  const handleBytes = handleField(1);
  const malformed = {
    'a length over the limit': Buffer.from('ffffffff', 'hex'),
    'an empty frame': Buffer.alloc(4),
    'a length cut short': Buffer.from('0100', 'hex'),
    'a frame cut short': frame(Command.dispatch, handleBytes).subarray(0, 8),
    'an unknown command': frame(99),
    'bytes after config': frame(Command.config, Buffer.of(0)),
    'a short handle': frame(Command.dispatch, handleBytes.subarray(0, 7)),
    'a flush without a timeout': frame(Command.flush, handleBytes),
    'a timeout over the limit': frame(Command.flush, handleBytes, Buffer.from('61ea0000', 'hex')),
    'a chat that is not UTF-8': frame(Command.drain, handleBytes, Buffer.of(0xff)),
    'bytes after close': frame(Command.close, handleBytes, Buffer.of(0)),
    'an unknown wire type': frame(Command.reencode, Buffer.of(0)),
    'a reencode without a type': frame(Command.reencode),
    'bytes after samples': frame(Command.samples, Buffer.of(0)),
  };
  for (const [name, script] of Object.entries(malformed)) {
    const result = spawnSync(helperPath, [], {input: script, timeout: 30_000});
    assert.equal(result.status, 2, name + ': exit status, stderr: ' + result.stderr);
    assert.match(result.stderr.toString(), /^host_replay: [^\n]+\n$/, name);
    assert.equal(result.stdout.length, 0, name + ': nothing is written for a script that never was valid');
  }
  // What was answered before the malformed frame is still written.
  const partial = spawnSync(helperPath, [], {input: Buffer.concat([frame(Command.config), frame(99)]), timeout: 30_000});
  assert.equal(partial.status, 2);
  assert.equal(hex(partial.stdout), hex(frame(Record.ok)));
});
