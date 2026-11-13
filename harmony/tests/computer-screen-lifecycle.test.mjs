import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.ArkTS') return { url: 'persona-screen-lifecycle:arkts', shortCircuit: true };
    if (specifier === '@kit.CryptoArchitectureKit') return { url: 'persona-screen-lifecycle:crypto', shortCircuit: true };
    if (specifier === '@kit.BasicServicesKit') return { url: 'persona-screen-lifecycle:zlib', shortCircuit: true };
    if (context.parentURL?.endsWith('/ComputerScreen.ets')) {
      if (specifier === './ClientModel') return { url: 'persona-screen-lifecycle:client', shortCircuit: true };
      if (specifier === './HubRepository') return { url: 'persona-screen-lifecycle:repository', shortCircuit: true };
      if (specifier === '../platform/HubTransport') return { url: 'persona-screen-lifecycle:transport', shortCircuit: true };
    }
    if (context.parentURL?.endsWith('.ts') && specifier.startsWith('.') && !specifier.endsWith('.ts'))
      return { url: new URL(specifier + '.ts', context.parentURL).href, shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      const base = new URL(specifier, context.parentURL);
      for (const extension of ['.ets', '.ts']) {
        const candidate = new URL(base.href + extension);
        if (existsSync(candidate)) return { url: candidate.href, shortCircuit: true };
      }
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-screen-lifecycle:arkts') return { format: 'module', shortCircuit: true, source: `
      class Encoder { encodeInto(value) { return new globalThis.TextEncoder().encode(value); }
        static create() { return new Encoder(); } }
      class Decoder { constructor(options = {}) { this.decoder = new globalThis.TextDecoder('utf-8', options); }
        decodeToString(value) { return this.decoder.decode(value); } }
      export const util = { TextEncoder: Encoder, TextDecoder: { create(_encoding, options = {}) { return new Decoder(options); } } };
    ` };
    if (url === 'persona-screen-lifecycle:crypto') return { format: 'module', shortCircuit: true, source: 'export const cryptoFramework = {};' };
    if (url === 'persona-screen-lifecycle:zlib') return { format: 'module', shortCircuit: true, source: `
      export const zlib = { createZipSync() { return { getZStream: async () => ({}), inflateInit2: async () => 0,
        inflate: async () => 0, inflateEnd: async () => 0 }; }, ReturnStatus: { OK: 0, BUF_ERROR: -5 }, CompressFlushMode: { NO_FLUSH: 0 } };
    ` };
    if (url === 'persona-screen-lifecycle:client') return { format: 'module', shortCircuit: true, source: 'export class ClientModel {};' };
    if (url === 'persona-screen-lifecycle:repository') return { format: 'module', shortCircuit: true, source: 'export function segment(value) { return encodeURIComponent(value); } export class HubRepository {};' };
    if (url === 'persona-screen-lifecycle:transport') return { format: 'module', shortCircuit: true, source: `
      export class HubTransport {}
      export class HttpFailure extends Error { constructor(status, code) { super(code); this.status = status; this.code = code; } }
    ` };
    if (url.endsWith('.ets') || url.endsWith('.ts')) {
      let source = readFileSync(new URL(url), 'utf8');
      if (url.endsWith('.ets')) source = source.replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { ComputerScreen } = await import('../entry/src/main/ets/models/ComputerScreen.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');

function deferred() {
  let resolve;
  const promise = new Promise((yes) => { resolve = yes; });
  return { promise, resolve };
}

async function waitFor(predicate) {
  for (let index = 0; index < 500; index++) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  assert.fail('timed out waiting for the screen lifecycle event');
}

class TestStream {
  closed = false;
  onState;
  onBinary;
  connectGate;
  failBeforeConnectResolves = false;

  async connect(onMessage, onState, onBinary) {
    this.onState = onState;
    this.onBinary = onBinary;
    if (this.failBeforeConnectResolves) {
      onMessage('unexpected text frame');
      await this.connectGate.promise;
    }
  }

  sendBinary(_bytes) { return !this.closed; }
  close() { this.closed = true; }
  receive(bytes) {
    const copy = new Uint8Array(bytes);
    this.onBinary?.(copy.buffer);
  }
  state(value) { this.onState?.(value); }
}

function screenFixture(stream) {
  const machine = ResourceRow.from({ id: 'mac-1', manager: 'work', name: 'Mac', online: true, trust: 'confirmed', os: 'mac' }, 'machine');
  const row = ResourceRow.from({ id: 'mac-1', manager: 'work', name: 'Mac', online: true, trust: 'confirmed', os: 'mac' }, 'machine');
  const repository = {
    transport: { stream: () => stream, request: async () => ({}) },
    async call() { return { screen_sharing: true }; }
  };
  const parent = {
    demo: false,
    connected: true,
    profile: { manager: 'work' },
    machines: [machine],
    async viewFor() { return this; },
    connectionGeneration() { return 7; },
    scope() { return 'hub:work'; },
    owns(generation, scope) { return this.connected && generation === 7 && scope === 'hub:work'; },
    repo() { return repository; }
  };
  return { screen: new ComputerScreen(), parent, row, repository };
}

function be32(value) { return [(value >>> 24) & 255, (value >>> 16) & 255, (value >>> 8) & 255, value & 255]; }

function serverHandshake(width = 2, height = 1) {
  const name = [...new TextEncoder().encode('Mac')];
  return new Uint8Array([...new TextEncoder().encode('RFB 003.008\n'), 1, 1, 0, 0, 0, 0,
    (width >>> 8) & 255, width & 255, (height >>> 8) & 255, height & 255,
    ...new Array(16).fill(0), ...be32(name.length), ...name]);
}

function rawUpdate() {
  return new Uint8Array([0, 0, 0, 1, 0, 0, 0, 0, 0, 2, 0, 1, 0, 0, 0, 0, 1, 2, 3, 0, 4, 5, 6, 0]);
}

test('a port-stream failure while connect is pending invalidates the attempt before RFB can start', async () => {
  const stream = new TestStream();
  stream.failBeforeConnectResolves = true;
  stream.connectGate = deferred();
  const { screen, parent, row } = screenFixture(stream);
  const opening = screen.open(parent, row);

  await waitFor(() => screen.phase === 'failed');
  assert.equal(screen.socketReady, false);
  assert.equal(screen.client, undefined);
  stream.connectGate.resolve();
  await opening;

  assert.equal(screen.phase, 'failed');
  assert.equal(screen.socketReady, false);
  assert.equal(screen.client, undefined);
  assert.equal(stream.closed, true);
  screen.close();
});

test('an established stream failure clears the frame snapshot and advances its revision', async () => {
  const stream = new TestStream();
  const { screen, parent, row } = screenFixture(stream);
  const opening = screen.open(parent, row);
  const initialRevision = screen.frameRevision;

  await waitFor(() => stream.onBinary !== undefined);
  stream.receive(serverHandshake());
  await waitFor(() => screen.phase === 'live');
  stream.receive(rawUpdate());
  await waitFor(() => screen.frameRevision > initialRevision);
  const revision = screen.frameRevision;

  stream.state('closed');
  await waitFor(() => screen.phase === 'failed');
  await opening;

  assert.ok(screen.frameRevision > revision);
  assert.equal(screen.remoteWidth, 0);
  assert.equal(screen.remoteHeight, 0);
  assert.equal(screen.frameSnapshot().pixels.length, 0);
  assert.equal(screen.controlAllowed(), false);
  screen.close();
});


const pageSource = readFileSync(new URL('../entry/src/main/ets/components/ComputerScreenPage.ets', import.meta.url), 'utf8').replace(/@Monitor\([^)]*\)\s*/g, '');
function pageMethod(name) {
  const start = pageSource.search(new RegExp('^  (?:private )?(?:async )?' + name + '\\(', 'm'));
  assert.ok(start >= 0, name); const body = pageSource.indexOf('{',start); let depth=1,quote='',escaped=false,at=body+1;
  for(; at<pageSource.length && depth; at++){const c=pageSource[at]; if(quote){if(escaped)escaped=false;else if(c==='\\')escaped=true;else if(c===quote)quote='';}else if(c==="'"||c==='"'||c==='`')quote=c;else if(c==='{')depth++;else if(c==='}')depth--;}
  return pageSource.slice(start,at);
}
const PageBehavior = new Function('tr','return '+stripTypeScriptTypes('class PageBehavior {'+
 ['phaseChanged','clearFrame','clearFields','confirmControl','isVisible','sendKey','submitText'].map(pageMethod).join('\n')+'}'))(key=>key);
function pageFixture(){
 const page=new PageBehavior();let revision=1;let grants=0;let keys=0;let dialog;
 page.active=true;page.mounted=true;page.row={id:'mac',title:'Mac',key:()=> 'mac'};
 page.screen={phase:'live',isLive:()=>page.screen.phase==='live',controlAllowed:()=>false,connectionRevision:()=>revision,releasePointer:()=>{page.remoteReleaseCount=(page.remoteReleaseCount||0)+1;},grantControl:()=>grants++,pressKey:()=>keys++};
 page.getUIContext=()=>({showAlertDialog:value=>dialog=value});
 return {page,bump:()=>revision++,grants:()=>grants,keys:()=>keys,confirm:()=>dialog.secondaryButton.action()};
}
test('a failed connection immediately releases its native frame and credential drafts',()=>{
 const {page}=pageFixture();let released=0;page.framePixels={release:()=>released++};page.accountDraft='synthetic';page.passwordDraft='synthetic-secret';page.textSending=true;
 page.screen.phase='credentials';page.phaseChanged();assert.equal(page.passwordDraft,'synthetic-secret');assert.equal(released,1);
 page.screen.phase='failed';page.phaseChanged();assert.equal(page.accountDraft,'');assert.equal(page.passwordDraft,'');assert.equal(page.textDraft,'');assert.equal(page.textSending,false);assert.equal(page.framePixels,undefined);
});
test('a delayed control confirmation cannot authorize a replacement or hidden session',()=>{
 const f=pageFixture();f.page.confirmControl();f.bump();f.confirm();assert.equal(f.grants(),0);
 f.page.confirmControl();f.page.active=false;f.confirm();assert.equal(f.grants(),0);
 f.page.active=true;f.page.confirmControl();f.confirm();assert.equal(f.grants(),1);
});
test('keyboard input cannot overtake an in-flight Unicode paste or clear a replacement editor',async()=>{
 const f=pageFixture(),gate=deferred();f.page.textDraft='中文';f.page.screen.sendText=()=>{assert.equal(f.page.remoteReleaseCount,1);return gate.promise;};
 f.page.pointerDown=true;const sending=f.page.submitText();assert.equal(f.page.pointerDown,false);f.page.sendKey(0xff0d);assert.equal(f.keys(),0);
 gate.resolve(true);await sending;f.page.sendKey(0xff0d);assert.equal(f.keys(),1);assert.equal(f.page.textDraft,'');
 const second=deferred();f.page.textDraft='中文';f.page.screen.sendText=()=>second.promise;const old=f.page.submitText();f.bump();f.page.clearFields();f.page.textDraft='中文';second.resolve(true);await old;assert.equal(f.page.textDraft,'中文');
});
