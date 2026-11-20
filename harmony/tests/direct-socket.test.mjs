import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

const kits = {
  '@kit.AbilityKit': ['bundleManager'], '@kit.BasicServicesKit': ['deviceInfo'],
  '@kit.LocalizationKit': ['i18n'], '@kit.PerformanceAnalysisKit': ['hilog'],
  '@kit.CryptoArchitectureKit': ['cryptoFramework'], '@kit.DeviceCertificateKit': ['cert'],
  '@kit.NetworkKit': ['connection', 'socket'], '@kit.RemoteCommunicationKit': ['rcp'], '@kit.ArkTS': ['url', 'util']
};
registerHooks({
  resolve(specifier, context, next) {
    if (Object.hasOwn(kits, specifier)) return { url: 'socket-kit:' + specifier, shortCircuit: true };
    if (context.parentURL?.endsWith('/HubTransport.ets') && specifier.startsWith('../core/'))
      return { url: new URL(specifier + '.ts', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.startsWith('socket-kit:')) return { format: 'module', shortCircuit: true,
      source: kits[url.slice(11)].map(name => `export const ${name} = globalThis.socketTestKit.${name};`).join('\n') };
    if (url.endsWith('.ets')) return { format: 'module', shortCircuit: true,
      source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8'), { mode: 'transform' }) };
    return next(url, context);
  }
});

// Stop at the peer certificate. No fake upgrade or credentials can make a dial look successful.
let records, lookup, refused, dials, tlsCalls, closed, writes;
class FakeSocket {
  on() {}
  off() {}
  async close() { closed.add(this); }
  async send() { writes++; }
}
function route(address) {
  const candidates = address.address === 'hub.test' ? records : [address];
  const candidate = candidates.find(candidate => candidate.family === address.family);
  if (!candidate || refused.has(candidate.address)) throw Error('unreachable_address');
  return candidate;
}
class TCP extends FakeSocket {
  async connect({ address }) { dials.push(address); this.peer = route(address); }
}
class TLS extends FakeSocket {
  constructor(tcp) { super(); this.tcp = tcp; }
  async bind(address) { this.local = address; }
  async connect(options) {
    tlsCalls.push(options);
    if (!options.proxy && !this.tcp) {
      assert.equal(this.local.family, options.address.family);
      route(options.address);
    }
    if (this.tcp) assert.equal(this.tcp.peer.family, options.address.family);
  }
  async getRemoteCertificate() { return { encodingFormat: -1, data: new Uint8Array() }; }
}
globalThis.socketTestKit = {
  rcp: {}, i18n: {getSystemLanguage: () => 'en-US'},
  util: {TextEncoder: class { encodeInto(value) { return new TextEncoder().encode(value); } },
    TextDecoder: {create: (encoding, options) => ({decodeToString: bytes => new TextDecoder(encoding,options).decode(bytes)})}},
  hilog: { error() {} }, url: { URL: { parseURL: input => new URL(input) } },
  cert: { EncodingFormat: { FORMAT_DER: 0, FORMAT_PEM: 1 } },
  connection: { getAddressesByName: name => lookup(name) },
  socket: { constructTCPSocketInstance: () => new TCP(), constructTLSSocketInstance: tcp => new TLS(tcp),
    Protocol: { TLSv13: 'TLSv1.3', TLSv12: 'TLSv1.2' }, ProxyTypes: { SOCKS5: 1 } }
};
const { EndpointConfig, HubTransport } = await import('../entry/src/main/ets/platform/HubTransport.ets');

test('held Agent HTTP 202 and client hello use the actual transport contract', async () => {
  const requests = [], envelopes = [], rcp = globalThis.socketTestKit.rcp;
  let reply, status = 202;
  rcp.Request = class { constructor(target,method,headers) { Object.assign(this,{target,method,headers}); } };
  rcp.createSession = () => ({close(){}, async fetch(request) {
    requests.push(request); request.destination(new TextEncoder().encode(JSON.stringify(reply)).buffer);
    return {statusCode:status, headers:{}};
  }});
  const client = new HubTransport(new EndpointConfig('https://desk.test/','fixture','','','owner'));
  client.clientMetadata = () => ({platform:'harmony',version:'1',build:'1',os:{}});
  client.send = message => {envelopes.push(JSON.parse(message));return true;};
  assert.ok(client.hello(['elicitation']));
  assert.deepEqual(envelopes[0].features, ['elicitation','agent_message_hold']);
  const root = '/api/v1/sessions/my%20laptop/chat%2Fone', path = root + '/messages';
  reply = {delivered:'later',undelivered:{id:'own-id',state:'waiting',computer:'Desk'}};
  assert.deepEqual(JSON.parse(await client.request(path,'POST','{}','own-id')), reply);
  assert.equal(requests.at(-1).target,'https://desk.test'+path);
  assert.equal(requests.at(-1).headers['X-Persona-Features'],'agent_message_hold');
  assert.equal(requests.at(-1).headers['X-Persona-Manager'],'owner');
  for(const [response,pending] of [[{state:'unknown'},false],[{state:'pending'},true],[{},true],
    [{...reply,undelivered:{...reply.undelivered,id:'foreign'}},true]]) {
    reply = response;
    await assert.rejects(client.request(path,'POST','{}','own-id'), error => error.status===202 && error.resultUnknown && error.resultPending===pending);
  }
  reply = {ok:true}; status = 200;
  const route = root + '/held/op%2F1/retry';
  await client.request(route,'POST','{}','action'); assert.equal(requests.at(-1).target,'https://desk.test'+route);
  assert.equal(requests.at(-1).headers['X-Persona-Features'],undefined);
  await assert.rejects(client.request('/api/v1/files/a%2Fb','GET'), /invalid_path/);
});
const v4 = { address: '192.0.2.20', family: 1 }, v6 = { address: '2001:db8::20', family: 2 };
function reset(answers) {
  records = answers; refused = new Set(); dials = []; tlsCalls = []; closed = new Set(); writes = 0;
  lookup = async name => { assert.equal(name, 'hub.test'); return records; };
}
function transport(host = 'hub.test', proxy) {
  return new HubTransport(new EndpointConfig('https://' + host + ':8443/', '', 'fixture', 'A'.repeat(43), '', [], proxy));
}
async function rejectsPin(client) {
  await assert.rejects(client.connect(() => {}), /hub_pin_mismatch/);
  assert.equal(writes, 0);
}

test('an AAAA-only domain reaches pin verification with its original TLS hostname', async () => {
  reset([v6]);
  await rejectsPin(transport());
  assert.deepEqual(tlsCalls.map(call => call.address), [{ address: 'hub.test', port: 8443, family: 2 }]);
});
test('an unreachable family can fall back before TLS and pin failure ends the dial', async () => {
  reset([v6, v4]); refused.add(v6.address);
  await rejectsPin(transport());
  assert.deepEqual(dials.map(peer => peer.family), [2, 1]);
  assert.equal(closed.size, 2);
  assert.equal(tlsCalls.at(-1).address.address, 'hub.test');
  assert.equal(tlsCalls.at(-1).address.family, 1);
  reset([v6, v4]);
  await rejectsPin(transport());
  assert.equal(tlsCalls.length, 1);
  assert.equal(tlsCalls[0].address.family, 2);
});
test('numeric peers and the SSH proxy do not start a direct Hub DNS lookup', async () => {
  reset([]); lookup = async () => { throw Error('unexpected_lookup'); };
  await rejectsPin(transport('[::1]')); await rejectsPin(transport('127.0.0.1'));
  const proxy = { host: '127.0.0.1', port: 1234, username: 'fixture', password: 'fixture', isCurrent: () => true };
  await rejectsPin(transport('hub.test', proxy));
  assert.equal(tlsCalls[2].proxy.address.address, proxy.host);
  assert.equal(tlsCalls[2].address.address, 'hub.test');
});
test('a second connect or a retired DNS lookup cannot open a socket', async () => {
  reset([v6]);
  const pending = Promise.withResolvers(), client = transport();
  lookup = () => pending.promise;
  const first = client.connect(() => {});
  const error = assert.rejects(first, /connection_closed/);
  await assert.rejects(client.connect(() => {}), /already_connecting/);
  client.close(); await error;
  pending.resolve(records); await Promise.resolve();
  assert.equal(tlsCalls.length, 0); assert.equal(dials.length, 0); assert.equal(writes, 0);
});
