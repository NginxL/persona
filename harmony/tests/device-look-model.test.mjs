import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';
import { deflateSync, crc32 } from 'node:zlib';

const files = new Map();
const directories = new Set(['/']);
const saved = new Map();
const calls = [];
let imageGate;

function missing() { return Object.assign(new Error('not_found'), { code: 13900002 }); }
function parent(path) { return path.slice(0, path.lastIndexOf('/')) || '/'; }
function ensureDirectory(path) {
  if (!path || directories.has(path)) return;
  ensureDirectory(parent(path));
  directories.add(path);
}
function children(path) {
  const names = new Set();
  for (const directory of directories) if (directory !== path && parent(directory) === path) names.add(directory.slice(path.length + 1));
  for (const file of files.keys()) if (parent(file) === path) names.add(file.slice(path.length + 1));
  return [...names];
}

globalThis.deviceLookBoundary = {
  files,
  directories,
  saved,
  calls,
  putError: false,
  putGate: undefined,
  getGate: undefined,
  decodeCalls: 0,
  io: {
    OpenMode: { CREATE: 1, WRITE_ONLY: 2, TRUNC: 4 },
    statSync(path) {
      if (files.has(path)) return { size: files.get(path).byteLength, isDirectory: () => false };
      if (directories.has(path)) return { size: 0, isDirectory: () => true };
      throw missing();
    },
    mkdirSync(path, recursive = false) {
      if (!recursive && !directories.has(parent(path))) throw missing();
      ensureDirectory(path);
    },
    listFileSync(path) {
      if (!directories.has(path)) throw missing();
      return children(path);
    },
    openSync(path) { return { fd: path }; },
    writeSync(fd, bytes) { files.set(fd, new Uint8Array(bytes.slice(0))); return bytes.byteLength; },
    fsyncSync() {},
    closeSync() {},
    renameSync(from, to) {
      const bytes = files.get(from);
      if (!bytes) throw missing();
      files.set(to, bytes); files.delete(from);
    },
    unlinkSync(path) { if (!files.delete(path)) throw missing(); },
    rmdirSync(path) { if (children(path).length) throw new Error('directory_not_empty'); directories.delete(path); }
  },
  async requestBytes(path) {
    calls.push(['bytes', path]);
    if (imageGate) return imageGate.promise;
    return { data: png().buffer, contentType: 'image/png' };
  },
  reset() { files.clear(); directories.clear(); directories.add('/'); saved.clear(); calls.length = 0; imageGate = undefined; this.putError = false; this.putGate = undefined; this.getGate = undefined; this.decodeCalls = 0; },
  gateImages(gate) { imageGate = gate; }
};

registerHooks({
  resolve(specifier, context, next) {
    const parentURL = context.parentURL || '';
    if (parentURL.endsWith('/DeviceLook.ets')) {
      if (specifier === '@kit.ImageKit' || specifier === '@kit.AbilityKit' || specifier === '@kit.BasicServicesKit' || specifier === '@kit.CoreFileKit' ||
        specifier === '../platform/HubTransport' || specifier === '../platform/LocalStore')
        return { url: 'device-look-boundary:' + specifier, shortCircuit: true };
      if (specifier === './Wire') return { url: new URL('./Wire.ets', parentURL).href, shortCircuit: true };
      if (specifier === '../core/DeviceLook') return { url: new URL('../core/DeviceLook.ts', parentURL).href, shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.startsWith('device-look-boundary:')) {
      const specifier = url.slice('device-look-boundary:'.length);
      const source = {
        '@kit.AbilityKit': 'export const common = {};',
        '@kit.ImageKit': 'export const image = {createImageSource(data){const bytes=new Uint8Array(data);return {async getImageInfo(){return {size:{width:new DataView(data).getUint32(16),height:new DataView(data).getUint32(20)}};},async createPixelMap(){globalThis.deviceLookBoundary.decodeCalls++;if(bytes.length<67||!bytes.slice(-8,-4).every((v,i)=>v===[73,69,78,68][i]))throw Error("decode_failed");return {async release(){}};},async release(){}};}};',
        '@kit.BasicServicesKit': 'export class BusinessError extends Error {}',
        '@kit.CoreFileKit': 'export const fileIo = globalThis.deviceLookBoundary.io;export const fileUri={getUriFromPath:path=>"file://app"+path};',
        '../platform/HubTransport': 'export class HttpFailure extends Error { constructor(status, code = "") { super(code); this.status = status; this.code = code; } }; globalThis.deviceLookBoundary.HttpFailure = HttpFailure;',
        '../platform/LocalStore': 'export class LocalStore { async open(_context) {} async get(key) { const b=globalThis.deviceLookBoundary;if(b.getGate)await b.getGate();return b.saved.get(key) || ""; } async close() {} async put(key, value) { const b=globalThis.deviceLookBoundary; if(b.putError)throw Error("disk unavailable"); if(b.putGate)await b.putGate(key,value); b.saved.set(key,value); } }'
      }[specifier];
      return { format: 'module', shortCircuit: true, source };
    }
    if (url.endsWith('.ets') || url.endsWith('.ts')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', shortCircuit: true, source: stripTypeScriptTypes(source) };
    }
    return next(url, context);
  }
});

const { DeviceLook } = await import('../entry/src/main/ets/models/DeviceLook.ets');

function png(width = 64, height = 64) {
  const chunk=(type,data)=>{const name=Buffer.from(type),length=Buffer.alloc(4),checksum=Buffer.alloc(4);length.writeUInt32BE(data.length);checksum.writeUInt32BE(crc32(Buffer.concat([name,data])));return Buffer.concat([length,name,data,checksum]);};
  const header=Buffer.alloc(13);header.writeUInt32BE(width,0);header.writeUInt32BE(height,4);header[8]=8;header[9]=6;
  const rows=Buffer.alloc((width*4+1)*height,255);for(let row=0;row<height;row++)rows[row*(width*4+1)]=0;
  const data=Buffer.concat([Buffer.from([137,80,78,71,13,10,26,10]),chunk('IHDR',header),chunk('IDAT',deflateSync(rows)),chunk('IEND',Buffer.alloc(0))]);
  return new Uint8Array(data);
}

function packageInfo(id, accent = '#7A6A52') {
  return {
    id, rev: 'a'.repeat(64), bytes: 4096,
    manifest: {
      format: 1, id, name: id === 'nwverse' ? 'Milk Frog' : 'Aurora',
      avatar: { sizes: { '64': 'avatar/64.png', '128': 'avatar/128.png' } },
      dot: { states: { idle: { file: 'dot/idle.png' } } },
      colors: { light: { accent, bubble: '#996920', bubble_text: '#FFFFFF' }, dark: { accent: '#958469', bubble: '#9A6B21', bubble_text: '#FFFFFF' } }
    }
  };
}

function makeClient(options = {}) {
  const client = {
    connected: true,
    demo: false,
    profile: { id: options.profileId || 'profile-1', hubId: options.hubId || 'hub-1', manager: 'manager-1', name: options.name || 'Hub A' },
    generation: 3, pairingEpoch: 0,
    pairingRevision() { return this.pairingEpoch; },
    scopeValue: (options.profileId || 'profile-1')+':'+(options.hubId || 'hub-1')+':manager-1',
    repository: undefined,
    connectionGeneration() { return this.generation; },
    scope() { return this.scopeValue; },
    owns(generation, scope) { return this.connected && this.generation === generation && this.scopeValue === scope; },
    repo() { return this.repository; }
  };
  const transport = {
    async request(path) {
      calls.push(['json', path]);
      if (path === '/api/v1/looks' && options.oldHub) throw new globalThis.deviceLookBoundary.HttpFailure(404, 'not_found');
      if (path === '/api/v1/looks' || path === '/api/v1/personas') {
        const key = path === '/api/v1/looks' ? 'looks' : 'personas';
        return JSON.stringify({ [key]: options.packages || [packageInfo('nwverse')] });
      }
      throw new Error('unexpected_route');
    },
    async requestBytes(path) { return globalThis.deviceLookBoundary.requestBytes(path); }
  };
  client.repository = { transport };
  return client;
}

async function openModel(client) {
  const model = new DeviceLook();
  await model.initialize({ filesDir: '/private/app' });
  await model.bind(client);
  return model;
}

function deferred() {
  let resolve;
  const promise = new Promise((yes) => { resolve = yes; });
  return { promise, resolve };
}

test('device-wide avatar and accent preferences restore independently from Hub ownership', async () => {
  deviceLookBoundary.reset();
  const model = await openModel(makeClient());
  assert.equal(await model.setAvatar(model.avatars.find(item => item.id === 'nwverse').optionKey()), true);
  assert.equal(await model.setAccent('avatar'), true);
  assert.equal(deviceLookBoundary.saved.has('device-look'), true);
  const record = JSON.parse(deviceLookBoundary.saved.get('device-look'));
  assert.equal(record.avatarId, 'nwverse');
  assert.equal(record.accentId, 'avatar');
  assert.equal(record.infoJSON.length < 128 * 1024, true);

  const restored = new DeviceLook();
  await restored.initialize({ filesDir: '/private/app' });
  assert.equal(restored.avatarId, 'nwverse');
  assert.equal(restored.accentId, 'avatar');
});

test('an older Hub uses only the documented personas fallback and stores the real image privately', async () => {
  deviceLookBoundary.reset();
  const client = makeClient({ oldHub: true });
  const model = await openModel(client);
  assert.deepEqual(calls.filter((call) => call[0] === 'json').map((call) => call[1]), ['/api/v1/looks', '/api/v1/personas']);
  assert.equal(model.avatars.some((avatar) => avatar.id === 'nwverse'), true);
  assert.equal(model.avatarPath, '');
  const avatar = model.avatars.find((item) => item.id === 'nwverse');
  assert.ok(avatar?.imagePath.startsWith('/private/app/device-look/avatars/profile-1/hub-1/nwverse/'));
});

test('forget purges unselected Hub copies, retains the selected image and device-wide preference', async () => {
  deviceLookBoundary.reset();
  const model = await openModel(makeClient({ packages: [packageInfo('nwverse'), packageInfo('aurora')] }));
  const selected = model.avatars.find((item) => item.id === 'nwverse');
  const unselected = model.avatars.find((item) => item.id === 'aurora');
  assert.ok(selected && unselected);
  assert.equal(await model.setAvatar(model.avatars.find(item => item.id === 'nwverse').optionKey()), true);
  await model.forgetHub('profile-1', 'hub-1');
  assert.equal(deviceLookBoundary.files.has(selected.imagePath), true);
  assert.equal(deviceLookBoundary.files.has(unselected.imagePath), false);
  assert.equal(model.avatarId, 'nwverse');
  assert.equal(model.avatars.map((item) => item.id).join(','), 'persona,nwverse');
  assert.equal(JSON.parse(deviceLookBoundary.saved.get('device-look')).avatarId, 'nwverse');
});

test('forget invalidates an image response already in flight before it can recreate a cache copy', async () => {
  deviceLookBoundary.reset();
  const gate = deferred();
  deviceLookBoundary.gateImages(gate);
  const client = makeClient();
  const model = new DeviceLook();
  await model.initialize({ filesDir: '/private/app' });
  const pending = model.bind(client);
  while (!calls.some((call) => call[0] === 'bytes')) await new Promise((resolve) => setImmediate(resolve));
  await model.forgetHub('profile-1', 'hub-1');
  gate.resolve({ data: png().buffer, contentType: 'image/png' });
  await pending;
  assert.equal([...deviceLookBoundary.files.keys()].some((path) => path.includes('/device-look/avatars/profile-1/hub-1/')), false);
});

test('failed preference storage preserves the visible avatar and accent and reports failure',async()=>{
 deviceLookBoundary.reset();const model=await openModel(makeClient()),choice=model.avatars.find(item=>item.id==='nwverse');deviceLookBoundary.putError=true;
 assert.equal(await model.setAvatar(choice.optionKey()),false);assert.equal(model.avatarId,'persona');assert.equal(model.error,'device_look_storage_failed');
 assert.equal(await model.setAccent('coral'),false);assert.equal(model.accentId,'style');assert.equal(deviceLookBoundary.saved.size,0);
});

test('rapid independent selections serialize before publication and survive reopening',async()=>{
 deviceLookBoundary.reset();const model=await openModel(makeClient()),gate=deferred(),entered=deferred();let writes=0;
 deviceLookBoundary.putGate=async()=>{if(++writes===1){entered.resolve();await gate.promise;}};
 const choice=model.avatars.find(item=>item.id==='nwverse'),avatar=model.setAvatar(choice.optionKey());await entered.promise;const accent=model.setAccent('coral');
 assert.equal(model.avatarId,'persona');gate.resolve();assert.deepEqual(await Promise.all([avatar,accent]),[true,true]);assert.equal(writes,2);
 const restored=new DeviceLook();await restored.initialize({filesDir:'/private/app'});assert.equal(restored.avatarKey,choice.optionKey());assert.equal(restored.accentId,'coral');
 await model.setAvatar(choice.optionKey());assert.equal(writes,2,'a duplicate Radio/row action must not write twice');
});

test('identical package IDs and revisions from different Hubs remain independently selectable',async()=>{
 deviceLookBoundary.reset();const model=await openModel(makeClient()),first=model.avatars.find(item=>item.id==='nwverse');await model.setAvatar(first.optionKey());await model.setAccent('avatar');const firstColor=model.palette('glass').light.brand;
 await model.bind(makeClient({profileId:'profile-2',hubId:'hub-2',name:'Hub B',packages:[packageInfo('nwverse','#005500')]}));
 const choices=model.avatars.filter(item=>item.id==='nwverse');assert.equal(choices.length,2);assert.notEqual(choices[0].optionKey(),choices[1].optionKey());assert.equal(model.avatarKey,first.optionKey());
 const second=choices.find(item=>item.profileId==='profile-2');assert.equal(await model.setAvatar(second.optionKey()),true);assert.equal(model.avatarKey,second.optionKey());assert.notEqual(model.avatarPath,first.imagePath);assert.notEqual(model.palette('glass').light.brand,firstColor);
});

test('a late image response from a replaced repository cannot install a private copy',async()=>{
 deviceLookBoundary.reset();const gate=deferred();deviceLookBoundary.gateImages(gate);const client=makeClient(),model=new DeviceLook();await model.initialize({filesDir:'/private/app'});const pending=model.bind(client);
 while(!calls.some(call=>call[0]==='bytes'))await new Promise(resolve=>setImmediate(resolve));client.generation++;client.repository={transport:{}};gate.resolve({data:png().buffer,contentType:'image/png'});await pending;
 assert.equal(model.avatars.length,1);assert.equal([...files.keys()].some(path=>path.endsWith('.png')),false);assert.equal(model.avatarId,'persona');
});

test('Forget fences a queued selection before pruning its uncommitted image',async()=>{
 deviceLookBoundary.reset();const model=await openModel(makeClient()),choice=model.avatars.find(item=>item.id==='nwverse'),gate=deferred(),entered=deferred();let writes=0;
 deviceLookBoundary.putGate=async()=>{if(++writes===1){entered.resolve();await gate.promise;}};
 const selecting=model.setAvatar(choice.optionKey());await entered.promise;const forgetting=model.forgetHub('profile-1','hub-1');gate.resolve();assert.equal(await selecting,false);await forgetting;
 assert.equal(model.avatarId,'persona');assert.equal(JSON.parse(saved.get('device-look')).avatarId,'persona');assert.equal(files.has(choice.imagePath),false);
});

test('rebinding prunes obsolete avatar revisions while retaining the chosen device image',async()=>{
 deviceLookBoundary.reset();const client=makeClient(),model=await openModel(client),first=model.avatars.find(item=>item.id==='nwverse');await model.setAvatar(first.optionKey());
 for(const rev of ['b','c','d']){const pack=packageInfo('nwverse');pack.rev=rev.repeat(64);await model.bind(makeClient({packages:[pack]}));assert.equal(files.size,2);assert.equal(files.has(first.imagePath),true);}
});

test('selection is rejected during initial restore and cannot erase the saved avatar',async()=>{
 deviceLookBoundary.reset();const original=await openModel(makeClient()),choice=original.avatars.find(item=>item.id==='nwverse');await original.setAvatar(choice.optionKey());await original.setAccent('avatar');
 const gate=deferred(),entered=deferred();deviceLookBoundary.getGate=async()=>{entered.resolve();await gate.promise;};const restored=new DeviceLook(),opening=restored.initialize({filesDir:'/private/app'});await entered.promise;
 assert.equal(restored.ready,false);assert.equal(await restored.setAccent('blue'),false);assert.equal(await restored.setAvatar('builtin:persona'),false);
 gate.resolve();await opening;deviceLookBoundary.getGate=undefined;assert.equal(restored.avatarKey,choice.optionKey());assert.equal(restored.accentId,'avatar');assert.equal(await restored.setAccent('blue'),true);
 const reopened=new DeviceLook();await reopened.initialize({filesDir:'/private/app'});assert.equal(reopened.avatarKey,choice.optionKey());assert.equal(reopened.accentId,'blue');
});

test('a header-only PNG cannot become a selectable or persistent avatar',async()=>{
 deviceLookBoundary.reset();deviceLookBoundary.gateImages({promise:Promise.resolve({data:png().slice(0,24).buffer,contentType:'image/png'})});const model=await openModel(makeClient());
 assert.equal(deviceLookBoundary.decodeCalls,1);assert.equal(model.avatars.length,1);assert.equal(model.avatarId,'persona');assert.equal(model.error,'device_look_avatar_unavailable');assert.equal(files.size,0);
});

test('a failed initialization keeps selection closed throughout a gated retry',async()=>{
 deviceLookBoundary.reset();const original=await openModel(makeClient()),choice=original.avatars.find(item=>item.id==='nwverse');await original.setAvatar(choice.optionKey());await original.setAccent('avatar');
 const restored=new DeviceLook();deviceLookBoundary.getGate=async()=>{throw Error('temporary storage failure');};await assert.rejects(restored.initialize({filesDir:'/private/app'}));
 assert.equal(restored.ready,false);assert.equal(await restored.setAccent('blue'),false);
 const gate=deferred(),entered=deferred();deviceLookBoundary.getGate=async()=>{entered.resolve();await gate.promise;};const retry=restored.initialize({filesDir:'/private/app'});await entered.promise;
 assert.equal(restored.ready,false);assert.equal(await restored.setAvatar('builtin:persona'),false);assert.equal(await restored.setAccent('blue'),false);
 gate.resolve();await retry;deviceLookBoundary.getGate=undefined;assert.equal(restored.ready,true);assert.equal(restored.avatarKey,choice.optionKey());assert.equal(restored.accentId,'avatar');
 const reopened=new DeviceLook();await reopened.initialize({filesDir:'/private/app'});assert.equal(reopened.avatarKey,choice.optionKey());assert.equal(reopened.accentId,'avatar');
});
