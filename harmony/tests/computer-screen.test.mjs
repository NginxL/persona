import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.ArkTS') return { url: 'persona-screen:arkts', shortCircuit: true };
    if (specifier === '@kit.CryptoArchitectureKit') return { url: 'persona-screen:crypto', shortCircuit: true };
    if (specifier === '@kit.BasicServicesKit') return { url: 'persona-screen:zlib', shortCircuit: true };
    if (context.parentURL?.endsWith('.ts') && specifier.startsWith('.') && !specifier.endsWith('.ts'))
      return { url: new URL(specifier + '.ts', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-screen:arkts') return { format: 'module', shortCircuit: true, source: `
      class Encoder { encodeInto(value) { return new globalThis.TextEncoder().encode(value); } }
      class Decoder { constructor(options = {}) { this.decoder = new globalThis.TextDecoder('utf-8', options); }
        decodeToString(value) { return this.decoder.decode(value); } }
      export const util = { TextEncoder: Encoder, TextDecoder: { create(_encoding, options = {}) { return new Decoder(options); } } };
    ` };
    if (url === 'persona-screen:crypto') return { format: 'module', shortCircuit: true, source: 'export const cryptoFramework = {};' };
    if (url === 'persona-screen:zlib') return { format: 'module', shortCircuit: true, source: 'export const zlib = {};' };
    if (url.endsWith('.ts')) return { format: 'module', shortCircuit: true, source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8')) };
    return next(url, context);
  }
});

const { RFBClient } = await import('../entry/src/main/ets/core/RFBClient.ts');
const { RFBFramebuffer } = await import('../entry/src/main/ets/core/RFBFramebuffer.ts');
const { RFBOwnerLease } = await import('../entry/src/main/ets/core/RFBOwnerLease.ts');

class ScriptedTransport {
  sent = [];
  closed = false;
  sendBinary(bytes) { if (this.closed) return false; this.sent.push([...new Uint8Array(bytes)]); return true; }
  close() { this.closed = true; }
}

class NoopInflater {
  async inflate() { return new Uint8Array(0); }
  close() {}
}

function be32(value) { return [(value >>> 24) & 255, (value >>> 16) & 255, (value >>> 8) & 255, value & 255]; }

function serverHandshake(width = 2, height = 1, name = 'Mock Mac') {
  const nameBytes = [...new TextEncoder().encode(name)];
  return new Uint8Array([
    ...new TextEncoder().encode('RFB 003.008\n'),
    1, 1,
    0, 0, 0, 0,
    (width >>> 8) & 255, width & 255, (height >>> 8) & 255, height & 255,
    ...new Array(16).fill(0),
    ...be32(nameBytes.length), ...nameBytes
  ]);
}

function rawUpdate(x, y, width, height, pixels) {
  return new Uint8Array([
    0, 0, 0, 1,
    (x >>> 8) & 255, x & 255, (y >>> 8) & 255, y & 255,
    (width >>> 8) & 255, width & 255, (height >>> 8) & 255, height & 255,
    0, 0, 0, 0,
    ...pixels
  ]);
}

async function waitFor(predicate) {
  for (let index = 0; index < 100; index++) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  assert.fail('timed out waiting for the RFB event');
}

function clientHarness(control = false) {
  const transport = new ScriptedTransport();
  const state = [];
  let allowControl = control;
  const client = new RFBClient(transport, async () => undefined, () => allowControl, new NoopInflater());
  client.onState = (phase, code) => state.push([phase, code]);
  const work = client.start();
  return { client, transport, state, work, setControl(value) { allowControl = value; } };
}

test('RFB 3.8 handshake, raw BGRA frame, refresh request, and gated native input', async () => {
  const harness = clientHarness(false);
  harness.client.receive(serverHandshake());
  await waitFor(() => harness.state.some(([phase]) => phase === 'live'));
  assert.equal(harness.client.serverName, 'Mock Mac');
  assert.deepEqual(harness.transport.sent[0], [...new TextEncoder().encode('RFB 003.008\n')]);
  assert.deepEqual(harness.transport.sent[1], [1]);
  assert.equal(harness.transport.sent[2][0], 1);
  assert.deepEqual(harness.transport.sent[3].slice(0, 5), [0, 0, 0, 0, 32]);
  assert.deepEqual(harness.transport.sent[4].slice(0, 4), [2, 0, 0, 6]);
  assert.deepEqual(harness.transport.sent[5], [3, 0, 0, 0, 0, 0, 0, 2, 0, 1]);

  let frame;
  harness.client.onFrame = (next) => { frame = next; };
  harness.client.receive(rawUpdate(0, 0, 2, 1, [1, 2, 3, 0, 4, 5, 6, 0]));
  await waitFor(() => frame !== undefined);
  assert.deepEqual(frame.pixel(0, 0), [1, 2, 3, 255]);
  assert.deepEqual(frame.pixel(1, 0), [4, 5, 6, 255]);
  assert.deepEqual(harness.transport.sent.at(-1).slice(0, 2), [3, 1], 'a completed frame schedules the next incremental request');

  const beforeDeniedInput = harness.transport.sent.length;
  assert.equal(harness.client.pointer(1, 0, 1), false);
  assert.equal(harness.client.pressKey(0x41), false);
  assert.equal(harness.transport.sent.length, beforeDeniedInput);

  harness.setControl(true);
  assert.equal(harness.client.pointer(1, 0, 1), true);
  assert.deepEqual(harness.transport.sent.at(-1), [5, 1, 0, 1, 0, 0]);
  assert.equal(harness.client.pressKey(0x41), true);
  assert.deepEqual(harness.transport.sent.at(-1), [4, 1, 0, 0, 0, 0, 0, 65, 4, 0, 0, 0, 0, 0, 0, 65]);
  assert.equal(harness.client.typeText('A'), true);
  assert.equal(harness.transport.sent.at(-1).length, 16);

  harness.client.close();
  await harness.work;
});

test('invalid RFB dimensions and rectangles end in a bounded unsupported state', async () => {
  const dimensions = clientHarness();
  dimensions.client.receive(serverHandshake(16385, 1));
  await waitFor(() => dimensions.state.some(([phase, code]) => phase === 'failed' && code === 'unsupported'));
  await dimensions.work;

  const rectangle = clientHarness();
  rectangle.client.receive(serverHandshake(2, 1));
  await waitFor(() => rectangle.state.some(([phase]) => phase === 'live'));
  rectangle.client.receive(rawUpdate(1, 0, 2, 1, [0, 0, 0, 0, 0, 0, 0, 0]));
  await waitFor(() => rectangle.state.some(([phase, code]) => phase === 'failed' && code === 'unsupported'));
  await rectangle.work;

  assert.equal(RFBFramebuffer.validSize(0, 1), false);
  assert.equal(RFBFramebuffer.validSize(16385, 1), false);
  assert.throws(() => new RFBFramebuffer(16385, 1), /rfb_size_limit/);
});

test('control lease is off by default and permanently revoked when its owner generation changes', () => {
  let ownerGeneration = 8;
  const capturedGeneration = ownerGeneration;
  const lease = new RFBOwnerLease(() => ownerGeneration === capturedGeneration);
  assert.equal(lease.allowsInput(), false);
  assert.equal(lease.grant(), true);
  assert.equal(lease.allowsInput(), true);
  ownerGeneration++;
  assert.equal(lease.allowsInput(), false);
  assert.equal(lease.grant(), false);
  lease.revoke();
  ownerGeneration = capturedGeneration;
  assert.equal(lease.allowsInput(), false);
});

test('LastRect terminates an unknown-count framebuffer update within the rectangle budget', async () => {
  const h = clientHarness(); h.client.receive(serverHandshake());
  await waitFor(() => h.state.some(([phase]) => phase === 'live'));
  let frame; h.client.onFrame = value => frame = value;
  const raw = rawUpdate(0,0,2,1,[1,2,3,0,4,5,6,0]);
  h.client.receive(new Uint8Array([0,0,255,255,...raw.slice(4),0,0,0,0,0,0,0,0,...be32(-224)]));
  await waitFor(() => frame !== undefined); assert.deepEqual(frame.pixel(1,0),[4,5,6,255]);
  h.client.close(); await h.work;
});
test('unknown-count framebuffer updates cannot exceed the actual rectangle budget', async () => {
  const h = clientHarness(); h.client.receive(serverHandshake());
  await waitFor(() => h.state.some(([phase]) => phase === 'live'));
  const size = [0,0,0,0,0,2,0,1,...be32(-223)], bytes=[0,0,255,255];
  for(let index=0;index<4097;index++) bytes.push(...size);
  h.client.receive(new Uint8Array(bytes));
  await waitFor(()=>h.state.some(([phase,code])=>phase==='failed'&&code==='unsupported')); await h.work;
});
