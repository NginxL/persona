import assert from 'node:assert/strict';
import test from 'node:test';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync, existsSync } from 'node:fs';

const files = new Map();
const directories = new Set();
const opened = [];
let uuid = 0;
let canPreview = async () => true;
globalThis.canIUse = () => true;

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/MessageImageGallery.ets') && specifier === './MessageImage')
      return { url: 'gallery-boundary:image-loader', shortCircuit: true };
    if (context.parentURL?.endsWith('/NativePreview.ets')) {
      if (specifier === '../core/AttachmentTypes' || specifier === '../core/MessageImageGallery') {
        const source = new URL(specifier + '.ts', context.parentURL);
        if (existsSync(source)) return { url: source.href, shortCircuit: true };
      }
      return { url: 'gallery-boundary:' + specifier, shortCircuit: true };
    }
    if (context.parentURL?.endsWith('/MessageImageGallery.ets') && specifier === '../core/MessageImageGallery')
      return { url: new URL('../core/MessageImageGallery.ts', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'gallery-boundary:image-loader') return {
      format: 'module', shortCircuit: true,
      source: 'export async function loadMessageImage(model, itemId, remotePath) { return globalThis.galleryBoundary.load(model, itemId, remotePath); }'
    };
    if (url.startsWith('gallery-boundary:')) {
      const specifier = url.slice('gallery-boundary:'.length);
      const sources = {
        '../design/Strings': 'export const tr = key => key;',
        '@kit.BasicServicesKit': 'export class BusinessError extends Error {}',
        '@kit.AbilityKit': 'export const common = {};',
        '@kit.ArkTS': 'export const util = { generateRandomUUID: () => globalThis.galleryBoundary.uuid() };',
        '@kit.CoreFileKit': 'export const fileIo = globalThis.galleryBoundary.io; export const fileUri = { getUriFromPath: path => "file://" + path };',
        '@kit.PreviewKit': 'export const filePreview = globalThis.galleryBoundary.preview;'
      };
      return { format: 'module', shortCircuit: true, source: sources[specifier] };
    }
    if (url.endsWith('.ets') || url.endsWith('.ts'))
      return { format: 'module', shortCircuit: true, source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8')) };
    return next(url, context);
  }
});

globalThis.galleryBoundary = {
  uuid: () => `11111111-1111-4111-8111-${String(++uuid).padStart(12, '0')}`,
  load: async () => ({ data: new Uint8Array([1, 2, 3]).buffer, mime: 'image/jpeg' }),
  io: {
    OpenMode: { CREATE: 1, WRITE_ONLY: 2, TRUNC: 4 },
    statSync(path) { if (!directories.has(path)) throw new Error('missing'); return {}; },
    mkdirSync(path) { directories.add(path); },
    listFileSync(path) {
      if (!directories.has(path)) throw Object.assign(new Error('missing'), { code: 13900002 });
      return [...files.keys()].filter((key) => key.startsWith(path + '/')).map((key) => key.slice(path.length + 1));
    },
    openSync(path) { return { fd: path }; },
    writeSync(path, data) { files.set(path, new Uint8Array(data.slice(0))); return data.byteLength; },
    closeSync() {},
    unlinkSync(path) { files.delete(path); },
    rmdirSync(path) { directories.delete(path); }
  },
  preview: {
    async canPreview() { return canPreview(); },
    async openPreview(_context, previews, index) { opened.push({ previews: previews.slice(), index }); },
    async closePreview() { opened.push({ closed: true }); }
  }
};

const { conversationImageEntries, conversationImageKey, messageImageGalleryWindow } = await import('../entry/src/main/ets/core/MessageImageGallery.ts');
const { loadMessageImageGallery } = await import('../entry/src/main/ets/models/MessageImageGallery.ets');
const { previewMessageImageGallery, clearImagePreviews } = await import('../entry/src/main/ets/platform/NativePreview.ets');

function entries(count) {
  return Array.from({ length: count }, (_value, index) => ({
    key: 'image-' + index, itemId: 'item-' + index, remotePath: '', target: 'conversation:1'
  }));
}

function modelFixture() {
  const state = { generation: 3, scope: 'hub:manager', target: 'conversation:1', connected: true };
  const repository = {};
  return {
    state,
    repository,
    model: {
      profile: { id: 'hub-id', hubId: 'hub-id' },
      get connected() { return state.connected; },
      isChat() { return true; },
      repo() { return repository; },
      connectionGeneration() { return state.generation; },
      scope() { return state.scope; },
      target() { return state.target; },
      owns(generation, scope) { return state.generation === generation && state.scope === scope; }
    }
  };
}

function resetPreview() {
  files.clear(); directories.clear(); opened.length = 0; canPreview = async () => true;
  globalThis.galleryBoundary.preview.openPreview = async (_context, previews, index) => opened.push({ previews: previews.slice(), index });
}

test('gallery windows cap entries and keep the selected image in range', () => {
  const window = messageImageGalleryWindow(entries(20), 19);
  assert.equal(window.entries.length, 16);
  assert.equal(window.entries[0].key, 'image-4');
  assert.equal(window.entries[15].key, 'image-19');
  assert.equal(window.selectedIndex, 15);
  assert.equal(messageImageGalleryWindow(entries(2), 2), undefined);
  const duplicate = entries(2); duplicate[1].key = duplicate[0].key;
  assert.equal(messageImageGalleryWindow(duplicate, 0), undefined);
});

test('conversation gallery entries preserve transcript order, exclude foreign owners, and key exact sources', () => {
  const messages = [
    { id: 'first', ownerTarget: 'conversation:1', imagePaths: ['/a.jpg', '/a.jpg'], imageId: 'item-a' },
    { id: 'foreign', ownerTarget: 'conversation:old', imagePaths: ['/private.jpg'], imageId: 'private-item' },
    { id: 'second', ownerTarget: 'conversation:1', imagePaths: ['/b.png'], imageId: '' }
  ];
  const images = conversationImageEntries(messages, 'conversation:1');
  assert.deepEqual(images.map((entry) => [entry.remotePath, entry.itemId]), [
    ['/a.jpg', ''], ['/a.jpg', ''], ['', 'item-a'], ['/b.png', '']
  ]);
  assert.deepEqual(images.map((entry) => entry.key), [
    conversationImageKey('conversation:1', 'first', 'path', '/a.jpg', 0),
    conversationImageKey('conversation:1', 'first', 'path', '/a.jpg', 1),
    conversationImageKey('conversation:1', 'first', 'item', 'item-a'),
    conversationImageKey('conversation:1', 'second', 'path', '/b.png', 0)
  ]);
  assert.equal(new Set(images.map((entry) => entry.key)).size, images.length);
  assert.notEqual(conversationImageKey('conversation:2', 'first', 'path', '/a.jpg', 0), images[0].key);
  assert.notEqual(conversationImageKey('conversation:1', 'second', 'path', '/a.jpg', 0), images[0].key);
});

test('gallery loads use one captured owner and reuse the selected authenticated image', async () => {
  const f = modelFixture();
  const list = entries(3);
  const window = messageImageGalleryWindow(list, 1);
  const calls = [];
  globalThis.galleryBoundary.load = async (_model, itemId, remotePath) => {
    calls.push([itemId, remotePath]);
    return { data: new Uint8Array([4, 5, 6]).buffer, mime: 'image/jpeg' };
  };
  const selected = { source: list[1], target: 'conversation:1', scope: 'hub:manager', generation: 3,
    hub: 'hub-id', data: new Uint8Array([7, 8]).buffer, mime: 'image/jpeg' };
  const gallery = await loadMessageImageGallery(f.model, window, () => true, selected);
  assert.deepEqual(calls, [['item-0', ''], ['item-2', '']]);
  assert.equal(gallery.target, 'conversation:1');
  assert.equal(gallery.entries[1].data.byteLength, 2);
  assert.equal(gallery.entries.every((image) => image.scope === 'hub:manager' && image.generation === 3), true);
});

test('gallery loading stops before an image can be requested for a replacement target', async () => {
  const f = modelFixture();
  const calls = [];
  globalThis.galleryBoundary.load = async () => {
    calls.push('loaded');
    f.state.target = 'conversation:2';
    return { data: new Uint8Array([1]).buffer, mime: 'image/jpeg' };
  };
  await assert.rejects(loadMessageImageGallery(f.model, messageImageGalleryWindow(entries(2), 0), () => true), /request_updated/);
  assert.deepEqual(calls, ['loaded']);
});

test('gallery aggregate bytes stay within 64 MiB', async () => {
  const f = modelFixture();
  const refs = entries(3);
  globalThis.galleryBoundary.load = async (_model, itemId) => ({
    data: new ArrayBuffer(itemId === 'item-2' ? 1 : 32 * 1024 * 1024), mime: 'image/jpeg'
  });
  await assert.rejects(loadMessageImageGallery(f.model, messageImageGalleryWindow(refs, 0), () => true), /preview_unavailable/);
});

test('native gallery opens the selected Hub-scoped set with the OS multi-image preview', async () => {
  resetPreview();
  const image = (key, mime) => ({ source: { key, itemId: key, remotePath: '', target: 'conversation:1' }, target: 'conversation:1', scope: 'hub:manager',
    generation: 3, hub: 'hub-id', data: new Uint8Array([1, 2, 3]).buffer, mime });
  await previewMessageImageGallery({ cacheDir: '/cache' }, 'hub/one', [image('a', 'image/jpeg'), image('b', 'image/png')], 1, () => true);
  assert.equal(opened.length, 1);
  assert.equal(opened[0].previews.length, 2);
  assert.equal(opened[0].index, 1);
  assert.match(opened[0].previews[0].uri, /message-images\/hub%2Fone\/.*\.jpg$/);
  assert.match(opened[0].previews[1].uri, /message-images\/hub%2Fone\/.*\.png$/);
  await previewMessageImageGallery({ cacheDir: '/cache' }, 'hub/two', [image('c', 'image/jpeg')], 0, () => true);
  clearImagePreviews({ cacheDir: '/cache' }, 'hub/one');
  assert.equal([...files.keys()].some((path) => path.includes('hub%2Fone')), false);
  assert.equal([...files.keys()].some((path) => path.includes('hub%2Ftwo')), true);
});

test('native gallery removes staged copies when scope changes during OS admission', async () => {
  resetPreview();
  let current = true;
  canPreview = async () => { current = false; return true; };
  const image = { source: { key: 'a', itemId: 'a', remotePath: '', target: 'conversation:1' }, target: 'conversation:1', scope: 'hub:manager',
    generation: 3, hub: 'hub-id', data: new Uint8Array([1]).buffer, mime: 'image/jpeg' };
  await assert.rejects(previewMessageImageGallery({ cacheDir: '/cache' }, 'hub/one', [image], 0, () => current), /preview_unavailable/);
  assert.equal(opened.length, 0);
  assert.equal(files.size, 0);
});

test('native preview modal host pause does not close the presented gallery', async () => {
  resetPreview();
  let hostActive = true;
  let ownerCurrent = true;
  const image = { source: { key: 'a', itemId: 'a', remotePath: '', target: 'conversation:1' }, target: 'conversation:1', scope: 'hub:manager',
    generation: 3, hub: 'hub-id', data: new Uint8Array([1]).buffer, mime: 'image/jpeg' };
  globalThis.galleryBoundary.preview.openPreview = async (_context, previews, index) => {
    opened.push({ previews: previews.slice(), index });
    hostActive = false;
  };
  await previewMessageImageGallery({ cacheDir: '/cache' }, 'hub/one', [image], 0,
    () => hostActive && ownerCurrent, () => ownerCurrent);
  assert.equal(opened.length, 1);
  assert.equal(opened[0].index, 0);
  assert.equal(files.size, 1);
});

test('a presented gallery closes and removes its cache when the owner changes', async () => {
  resetPreview();
  let ownerCurrent = true;
  const image = { source: { key: 'a', itemId: 'a', remotePath: '', target: 'conversation:1' }, target: 'conversation:1', scope: 'hub:manager',
    generation: 3, hub: 'hub-id', data: new Uint8Array([1]).buffer, mime: 'image/jpeg' };
  globalThis.galleryBoundary.preview.openPreview = async (_context, previews, index) => {
    opened.push({ previews: previews.slice(), index });
    ownerCurrent = false;
  };
  await assert.rejects(previewMessageImageGallery({ cacheDir: '/cache' }, 'hub/one', [image], 0,
    () => ownerCurrent, () => ownerCurrent), /request_updated/);
  assert.equal(opened.length, 2);
  assert.equal(opened[1].closed, true);
  assert.equal(files.size, 0);
});
