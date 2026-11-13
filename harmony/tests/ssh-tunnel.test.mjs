import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { randomUUID } from 'node:crypto';

const tag = { ALIAS: 1, SECRET: 2, DATA_LABEL_CRITICAL_1: 3, DATA_LABEL_CRITICAL_2: 4, RETURN_TYPE: 5, ACCESSIBILITY: 6, AUTH_TYPE: 7, SYNC_TYPE: 8, DATA_LABEL_CRITICAL_3: 9, RETURN_LIMIT: 10 };
const rows = new Map();
const calls = { add: [], query: [], open: [], close: [] };
let addGate, queryGate, openGate, failAdd = false;
let dnsGate, failDns = false;
let dnsAnswers = [{ address: '192.0.2.10' }, { address: '2001:db8::10' }, { address: '192.0.2.10' }, { address: 'hub.test' }];
const dnsCalls = [];
const active = new Set();
const alias = (map) => Buffer.from(map.get(tag.ALIAS) || []).toString();
const copy = (map) => new Map([...map].map(([key, value]) => [key, value instanceof Uint8Array ? new Uint8Array(value) : value]));
const matches = (row, query) => [...query].every(([key, value]) => key === tag.RETURN_TYPE || key === tag.RETURN_LIMIT ||
  value instanceof Uint8Array && row.get(key) instanceof Uint8Array && Buffer.from(value).equals(Buffer.from(row.get(key))));
globalThis.__sshFixture = {
  util: { generateRandomUUID: randomUUID, TextEncoder: class { encodeInto(value) { return new TextEncoder().encode(value); } } },
  url: { URL: { parseURL: (value) => new URL(value) } },
  netConnection: { async getAddressesByName(host) { dnsCalls.push(host); if (dnsGate) await dnsGate.promise; if (failDns) throw Error('device DNS failed'); return dnsAnswers; } },
  asset: {
    Tag: tag, Accessibility: { DEVICE_UNLOCKED: 2 }, AuthType: { NONE: 0 }, SyncType: { NEVER: 0 }, ReturnType: { ALL: 0, ATTRIBUTES: 1 },
    async add(map) { assert.ok(map.get(tag.SECRET).length <= 1024); calls.add.push(copy(map)); if (addGate) await addGate.promise; if (failAdd) throw Error('platform secret diagnostic'); rows.set(alias(map), copy(map)); },
    async query(query) { calls.query.push(copy(query)); if (queryGate) await queryGate.promise; return [...rows.values()].filter((row) => matches(row, query)).slice(0,query.get(tag.RETURN_LIMIT)||Infinity).map(row=>{const result=copy(row);if(query.get(tag.RETURN_TYPE)===1)result.delete(tag.SECRET);return result;}); },
    async remove(query) { assert.deepEqual([...query.keys()],[tag.ALIAS],'native remove admits alias only');for (const [key, row] of rows) if (matches(row, query)) rows.delete(key); }
  },
  bridge: {
    async open(request) {
      calls.open.push({ ...request, privateKey: new Uint8Array(request.privateKey), passphrase: new Uint8Array(request.passphrase) });
      if (openGate) await openGate.promise;
      active.add(request.id);
      return { id: request.id, host: '127.0.0.1', port: 34567, username: 'persona', password: 'ephemeral' };
    },
    async close(id) { calls.close.push(id); active.delete(id); },
    isOpen(id) { return active.has(id); }
  }
};
registerHooks({
  resolve(specifier, context, next) {
    const stubs = { '@kit.ArkTS': 'arkts', '@kit.AbilityKit': 'ability', '@kit.NetworkKit': 'network', '@kit.AssetStoreKit': 'asset', '@kit.PerformanceAnalysisKit': 'log', 'libpersona_ssh.so': 'bridge' };
    if (stubs[specifier]) return { url: 'persona-ssh:' + stubs[specifier], shortCircuit: true };
    if (specifier === './ClientModel' && context.parentURL?.endsWith('/SshTunnel.ets')) return { url: 'persona-ssh:client', shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      for (const extension of ['.ets', '.ts']) {
        const candidate = new URL(specifier + extension, context.parentURL);
        if (existsSync(candidate)) return { url: candidate.href, shortCircuit: true };
      }
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    const source = {
      'persona-ssh:arkts': 'export const util = globalThis.__sshFixture.util; export const url = globalThis.__sshFixture.url;',
      'persona-ssh:ability': 'export const common = {};',
      'persona-ssh:network': 'export const connection = globalThis.__sshFixture.netConnection;',
      'persona-ssh:asset': 'export const asset = globalThis.__sshFixture.asset;',
      'persona-ssh:log': 'export const hilog = {error(){}};',
      'persona-ssh:bridge': 'export default globalThis.__sshFixture.bridge;',
      'persona-ssh:client': 'export class ClientModel {}'
    }[url];
    if (source) return { format: 'module', source, shortCircuit: true };
    if (url.endsWith('.ets') || url.endsWith('.ts')) {
      const code = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(code), shortCircuit: true };
    }
    return next(url, context);
  }
});
const { SshTunnel, SshConfiguration } = await import('../entry/src/main/ets/models/SshTunnel.ets');
const { SshCredentialVault } = await import('../entry/src/main/ets/platform/SshCredentialVault.ets');

const KEY = '-----BEGIN OPENSSH PRIVATE KEY-----\nfixture-key\n-----END OPENSSH PRIVATE KEY-----\n';
const PIN = 'SHA256:' + Buffer.alloc(32, 1).toString('base64').replace(/=+$/, '');
function gate() { let resolve; const promise = new Promise((done) => { resolve = done; }); return { promise, resolve }; }
async function until(predicate) { for (let i = 0; i < 100; i++) { if (predicate()) return; await new Promise((done) => setTimeout(done, 0)); } assert.fail('fixture event not reached'); }
function reset() { rows.clear(); calls.add.length = 0; calls.query.length = 0; calls.open.length = 0; calls.close.length = 0; active.clear(); addGate = undefined; queryGate = undefined; openGate = undefined; failAdd = false; dnsGate = undefined; failDns = false; dnsAnswers = [{ address: '192.0.2.10' }, { address: '2001:db8::10' }, { address: '192.0.2.10' }, { address: 'hub.test' }]; dnsCalls.length = 0; }
function owner() {
  return { profile: { id: randomUUID(), baseUrl: 'https://hub.test:8443/prefix/', pin: 'pinned-spki' }, demo: false, generation: 7, epoch: 0, pairingEpoch: 0, retirements: [],
    pairingRevision() { return this.pairingEpoch; }, retireHubConnections(id) { this.retirements.push(id); },
    saved: '', writes: [], scope() { return this.profile.id + ':manager'; }, connectionGeneration() { return this.generation; },
    owns(generation, scope) { return generation === this.generation && scope === this.scope(); }, viewRevision() { return this.epoch; },
    async loadView() { return this.saved; }, async saveView(key, value, epoch) { assert.equal(epoch, this.epoch); this.writes.push([key, value]); this.saved = value; }
  };
}
function configuration() { const config = new SshConfiguration(); config.enabled = true; config.sshHost = 'ssh.test'; config.username = 'user'; config.hostKey = PIN; return config; }
async function fixture() { const client = owner(), tunnel = new SshTunnel(); tunnel.initialize({}); await tunnel.bind(client); return { client, tunnel }; }
async function configured() { const value = await fixture(); await value.tunnel.save(configuration(), KEY, 'phrase', true); return value; }

test('multiline SSH secrets use separate device-only aliases; DB retains metadata references only', async () => {
  reset(); const { client, tunnel } = await configured();
  assert.equal(calls.add.length, 3);
  for (const entry of calls.add) { assert.equal(entry.get(tag.ACCESSIBILITY), 2); assert.equal(entry.get(tag.SYNC_TYPE), 0); }
  assert.notEqual(alias(calls.add[0]), alias(calls.add[1]));
  assert.ok(!client.saved.includes('fixture-key')); assert.ok(!client.saved.includes('phrase'));
  const lease = await tunnel.acquire(); assert.equal(lease.isCurrent(), true);
  assert.equal(calls.open[0].targetHost, 'hub.test'); assert.equal(calls.open[0].targetPort, 8443);
  assert.deepEqual(calls.open[0].resolvedHubIps, ['192.0.2.10', '2001:db8::10']); assert.deepEqual(dnsCalls, ['hub.test']);
  assert.equal(Buffer.from(calls.open[0].privateKey).toString(), KEY);
  assert.equal('token' in calls.open[0], false); tunnel.stop(); assert.equal(lease.isCurrent(), false);
});
test('save requires explicit host-fingerprint confirmation before touching secrets', async () => {
  reset(); const { tunnel } = await fixture();
  await assert.rejects(tunnel.save(configuration(), KEY, '', false), /ssh_invalid_config/);
  assert.equal(calls.add.length, 0); assert.equal(calls.open.length, 0);
});
test('disabled configuration never opens native SSH', async () => {
  reset(); const { tunnel } = await fixture(); assert.equal(await tunnel.acquire(), undefined); assert.equal(calls.open.length, 0);
});
test('device DNS failure rejects before native SSH opens', async () => {
  reset(); failDns = true; const { tunnel } = await configured();
  await assert.rejects(tunnel.acquire(), /ssh_dns_failed/); assert.deepEqual(dnsCalls, ['hub.test']); assert.equal(calls.query.length, 0); assert.equal(calls.open.length, 0);
});
test('a pending DNS lookup cancels immediately and a late answer cannot open native SSH', async () => {
  reset(); const { tunnel } = await configured(); dnsGate = gate();
  const pending = tunnel.acquire(); await until(() => dnsCalls.length === 1); tunnel.stop();
  await assert.rejects(pending, /ssh_cancelled/); dnsGate.resolve();
  await new Promise((done) => setTimeout(done, 0)); assert.equal(calls.open.length, 0); assert.equal(tunnel.phase, 'idle');
});
test('captured DNS hints are deduplicated and capped at sixteen IP literals', async () => {
  reset(); dnsAnswers = Array.from({ length: 22 }, (_unused, index) => ({ address: `192.0.2.${index + 1}` }));
  const { tunnel } = await configured(); await tunnel.acquire();
  assert.equal(calls.open[0].resolvedHubIps.length, 16); assert.equal(new Set(calls.open[0].resolvedHubIps).size, 16);
});
test('literal Hub IP skips device DNS and carries no resolved hostname hints', async () => {
  reset(); const client = owner(); client.profile.baseUrl = 'https://192.0.2.10:8443/prefix/';
  const tunnel = new SshTunnel(); tunnel.initialize({}); await tunnel.bind(client); await tunnel.save(configuration(), KEY, '', true);
  await tunnel.acquire(); assert.deepEqual(dnsCalls, []); assert.deepEqual(calls.open[0].resolvedHubIps, []);
});
test('simultaneous acquisition shares one startup and stop cancels its preallocated ID', async () => {
  reset(); const { tunnel } = await configured(); openGate = gate();
  const first = tunnel.acquire(), second = tunnel.acquire();
  await until(() => calls.open.length === 1); const id = calls.open[0].id;
  tunnel.stop(); assert.ok(calls.close.includes(id)); openGate.resolve();
  await assert.rejects(first, /ssh_cancelled/); await assert.rejects(second, /ssh_cancelled/);
  assert.equal(active.has(id), false); assert.equal(tunnel.phase, 'idle');
});
test('old native completion cannot attach to another owner or close its new lease', async () => {
  reset(); const { tunnel } = await configured(); const pending = gate(); openGate = pending;
  const old = tunnel.acquire(); await until(() => calls.open.length === 1); const oldId = calls.open[0].id;
  const next = owner(); await tunnel.bind(next); await tunnel.save(configuration(), KEY, '', true);
  openGate = undefined; const fresh = await tunnel.acquire(); pending.resolve(); await assert.rejects(old, /ssh_cancelled/);
  assert.equal(active.has(oldId), false); assert.equal(fresh.isCurrent(), true); tunnel.stop();
});
test('generation, endpoint, SPKI and foreground changes revoke captured leases', async () => {
  for (const mutate of [(client) => client.generation++, (client) => client.profile.baseUrl = 'https://other.test/', (client) => client.profile.pin = 'new-pin']) {
    reset(); const { client, tunnel } = await configured(); const lease = await tunnel.acquire(); mutate(client); assert.equal(lease.isCurrent(), false); tunnel.stop();
  }
  reset(); const { client, tunnel } = await configured(); let foreground = true;
  await tunnel.bind(client, () => foreground); const lease = await tunnel.acquire(); foreground = false;
  assert.equal(lease.isCurrent(), false); tunnel.stop();
});
test('retired vault writes cannot reappear after same-profile pairing is admitted again', async () => {
  reset(); const vault = new SshCredentialVault(), profile = randomUUID(); vault.allow(profile); addGate = gate();
  const old = vault.put(profile, KEY, 'old', () => true); await until(() => calls.add.length === 1);
  const forgotten = vault.forget(profile); vault.allow(profile); const fresh = vault.put(profile, KEY, 'fresh', () => true);
  addGate.resolve(); addGate = undefined; await assert.rejects(old, /ssh_cancelled/); await forgotten;
  const reference = await fresh; const secrets = await vault.get(profile, reference, () => true);
  assert.equal(Buffer.from(secrets.passphrase).toString(), 'fresh'); secrets.clear(); assert.equal(rows.size, 3);
});
test('forget during secret read rejects before any native authentication', async () => {
  reset(); const { client, tunnel } = await configured(); queryGate = gate(); const pending = tunnel.acquire();
  await Promise.resolve(); const forget = tunnel.forget(client.profile.id); queryGate.resolve(); queryGate = undefined;
  await forget; await assert.rejects(pending, /ssh_cancelled/); assert.equal(calls.open.length, 0); assert.equal(rows.size, 0);
});
test('forget deletes multiple bounded attribute pages by alias and retains another profile',async()=>{
 reset();const vault=new SshCredentialVault(),profile=randomUUID(),other=randomUUID();vault.allow(profile);vault.allow(other);
 const retained=await vault.put(other,KEY,'',()=>true);
 for(let index=0;index<35;index++)await vault.put(profile,KEY,'phrase',()=>true);
 const before=calls.query.length;await vault.forget(profile);assert.equal(rows.size,2);
 const removalQueries=calls.query.slice(before);assert.equal(removalQueries.length,3);for(const query of removalQueries){assert.equal(query.get(tag.RETURN_TYPE),1);assert.equal(query.get(tag.RETURN_LIMIT),64);}
 const secrets=await vault.get(other,retained,()=>true);assert.equal(secrets.passphrase.length,0);assert.equal(Buffer.from(secrets.privateKey).toString(),KEY);secrets.clear();await vault.forget(other);assert.equal(rows.size,0);
});
test('UTF-8 secret limits and storage failures stay bounded and sanitized', async () => {
  reset(); const vault = new SshCredentialVault(), profile = randomUUID(); vault.allow(profile);
  await assert.rejects(vault.put(profile, '界'.repeat(12000), '', () => true), /ssh_invalid_key/);
  await assert.rejects(vault.put(profile, KEY, '界'.repeat(2000), () => true), /ssh_invalid_key/);
  assert.equal(calls.add.length, 0); failAdd = true;
  await assert.rejects(vault.put(profile, KEY, '', () => true), /^Error: ssh_storage_failed$/); assert.equal(rows.size, 0);
});
test('metadata write failure removes the newly created private-key aliases', async () => {
  reset(); const { client, tunnel } = await fixture(); client.saveView = async () => { throw Error('storage_unavailable'); };
  await assert.rejects(tunnel.save(configuration(), KEY, 'phrase', true)); assert.equal(rows.size, 0); assert.equal(tunnel.configuration.keyRef, '');
});
test('concurrent settings writes and connection startup cannot race credential replacement', async () => {
  reset(); const { tunnel } = await configured(); addGate = gate();
  const save = tunnel.save(configuration(), KEY + 'replacement', '', true); await until(() => calls.add.length > 3);
  await assert.rejects(tunnel.save(configuration(), '', '', true), /ssh_busy/);
  await assert.rejects(tunnel.disable(), /ssh_busy/); await assert.rejects(tunnel.acquire(), /ssh_busy/);
  addGate.resolve(); addGate = undefined; await save;
});
test('multikilobyte secrets are reassembled from at most 1 KiB protected assets', async () => {
  reset(); const vault = new SshCredentialVault(), profile = randomUUID(); vault.allow(profile);
  const key = KEY + 'x'.repeat(8000), phrase = '界'.repeat(1000);
  const reference = await vault.put(profile, key, phrase, () => true);
  assert.ok(calls.add.length > 10); for (const entry of calls.add) assert.ok(entry.get(tag.SECRET).length <= 1024);
  const secrets = await vault.get(profile, reference, () => true);
  assert.equal(Buffer.from(secrets.privateKey).toString(), key); assert.equal(Buffer.from(secrets.passphrase).toString(), phrase); secrets.clear();
  const keyPart = [...rows.keys()].find((value) => value.includes(':key.1:')); rows.delete(keyPart);
  await assert.rejects(vault.get(profile, reference, () => true), /ssh_invalid_key/);
});
test('settings clear secret fields on hide and route changes', () => {
  const source = readFileSync(new URL('../entry/src/main/ets/components/SshSettingsPage.ets', import.meta.url), 'utf8');
  assert.match(source, /aboutToDisappear\(\).*clearSecrets\(\)/);
  assert.match(source, /visibilityChanged\(\)[\s\S]*?clearSecrets\(\)/);
  assert.match(source, /ssh_verify_message/);
});

test('same-Hub re-pairing revokes active and pending native leases before authentication can attach', async () => {
 reset();const {client,tunnel}=await configured();const lease=await tunnel.acquire();client.pairingEpoch++;assert.equal(lease.isCurrent(),false);tunnel.stop();
 await tunnel.bind(client);openGate=gate();const pending=tunnel.acquire();await until(()=>calls.open.length===2);const id=calls.open[1].id;client.pairingEpoch++;openGate.resolve();await assert.rejects(pending,/ssh_cancelled/);assert.equal(active.has(id),false);tunnel.stop();
});

test('Hub-wide settings commit retires connections and excludes concurrent writers from other manager models',async()=>{
 reset();const {client,tunnel}=await configured();assert.deepEqual(client.retirements,[client.profile.id]);
 const second=new SshTunnel();second.initialize({});await second.bind(client);addGate=gate();const replacing=tunnel.save(configuration(),KEY+'new','',true);await until(()=>calls.add.length>3);
 await assert.rejects(second.disable(),/ssh_busy/);await assert.rejects(second.acquire(),/ssh_busy/);await assert.rejects(second.save(configuration(),KEY,'',true),/ssh_busy/);
 addGate.resolve();addGate=undefined;await replacing;assert.equal(client.retirements.length,2);await second.disable();assert.equal(client.retirements.length,3);tunnel.stop();second.stop();
});
