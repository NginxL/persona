import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync, existsSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier.startsWith('@kit.')) return {url:'client-boundary:'+specifier,shortCircuit:true};
    if (context.parentURL?.includes('/entry/src/main/ets/') && specifier.startsWith('.')) {
      const base = new URL(specifier + (specifier.endsWith('.ts') || specifier.endsWith('.ets') ? '' : '.ets'), context.parentURL);
      const url = existsSync(base) ? base : new URL(base.href.replace(/\.ets$/, '.ts'));
      return {url: url.href, shortCircuit: true};
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.startsWith('client-boundary:')) return {format:'module',shortCircuit:true,source:`
      export const common = {};
      export const bundleManager = {getBundleInfoForSelf(){return {name:'com.example.persona'};}};
      export const cert = {};
      export const cryptoFramework = {};
      export const deviceInfo = {};
      export const hilog = {debug(){},info(){},warn(){},error(){}};
      export const i18n = {getSystemLanguage(){return 'zh-CN';}};
      export const rcp = {};
      export const socket = {};
      export const connection = {};
      export const relationalStore = {};
      export const util = {TextEncoder:class {static create(){return new this();}encodeInto(value){return new TextEncoder().encode(value);}},generateRandomUUID:()=> 'synthetic-id'};
      export const tr = key => key;
      export const url = {URL:{parseURL:value=>new URL(value)}};
      export const segment = encodeURIComponent;
      export const withSourceContext = text => text;
      export const messageDisplay = text => text;
      export const nativeNotifications = {async clear(){},features(){return ['elicitation'];}};
    `};
    if (url.endsWith('.ets')) return {format:'module', shortCircuit:true, source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,'').replace(/TranscriptItem, TranscriptBatch, TranscriptAction, TranscriptSnapshot/, '').replace(/, TranscriptBatch, TranscriptSnapshot/, ''))};
    return next(url, context);
  },
});

const {TranscriptModel} = await import('../entry/src/main/ets/models/TranscriptModel.ets');

function fixture() {
  const calls = [];
  const sent = [];
  const gate = {};
  gate.promise = new Promise((resolve, reject) => { gate.resolve = resolve; gate.reject = reject; });
  const repository = {
    async call(path) {
      calls.push(path);
      if (path.includes('after_seq=')) {
        const after = Number(new URLSearchParams(path.split('?')[1]).get('after_seq'));
        return {items:[{id:`${after + 1}#0`,seq:after + 1,role:'assistant',kind:'text',text:'filled'}],history:'h',from_seq:after,to_seq:after + 1};
      }
      return gate.promise;
    },
    transport: {send(text) { sent.push(JSON.parse(text)); }},
  };
  const model = new TranscriptModel({machine:'machine-one',session:'session-one',id:'session-one'}, repository,
    {async get(){return JSON.stringify({items:[{id:'1#0',seq:1,role:'assistant',kind:'text',text:'cached'}],covered:1,history:'h',hasMore:false,loadedOnce:true});},async put(){return true;}}, 'test', () => {}, error => { throw error; });
  return {model,calls,sent,gate};
}

test('a gap heard during a pending latest page is fetched after it completes', async () => {
  const {model,calls,gate} = fixture();
  model.reducer.restore({items:[{id:'1#0',seq:1,role:'assistant',kind:'text',text:'cached'}],covered:1,history:'h',hasMore:false,loadedOnce:true});
  const initial = model.latest();
  model.receive({type:'items',machine_id:'machine-one',session_id:'session-one',items:[{id:'4#0',seq:4,role:'assistant',kind:'text',text:'later'}],history:'h',from_seq:3,to_seq:4});
  gate.resolve({items:[{id:'1#0',seq:1,role:'assistant',kind:'text',text:'first'}],history:'h',to_seq:1,has_more:false});
  await initial;
  await new Promise(resolve => setImmediate(resolve));
  assert.ok(calls.some(path => path.includes('after_seq=')));
});

test('a failed latest page releases the fill gate for a later gap', async () => {
  const {model,calls,gate} = fixture();
  model.reducer.restore({items:[{id:'1#0',seq:1,role:'assistant',kind:'text',text:'cached'}],covered:1,history:'h',hasMore:false,loadedOnce:true});
  const initial = model.latest();
  gate.reject(new Error('synthetic offline'));
  await assert.rejects(initial);
  model.receive({type:'items',machine_id:'machine-one',session_id:'session-one',items:[{id:'4#0',seq:4,role:'assistant',kind:'text',text:'later'}],history:'h',from_seq:3,to_seq:4});
  await new Promise(resolve => setImmediate(resolve));
  assert.ok(calls.some(path => path.includes('after_seq=')));
});

function historyPage(history, sequences, hasMore = false) {
  return {
    items: sequences.map(seq => ({id: `${seq}#0`, seq, role: 'user', kind: 'text', text: `${history} ${seq}`})),
    history, to_seq: sequences.at(-1), has_more: hasMore,
  };
}

function historyFixture(call) {
  const visible = [], snapshots = [], errors = [];
  const model = new TranscriptModel({machine:'machine-one', session:'session-one'},
    {call, transport:{send(){}}},
    {async get(){return '';}, async put(_key, value){snapshots.push(JSON.parse(value));}},
    'history-test', (items, hasMore, ready) => visible.push({items, hasMore, ready}), error => errors.push(error.message));
  return {model, visible, snapshots, errors};
}

test('an older page from a rewritten transcript discards the superseded cache even if the latest read fails', async () => {
  const old = historyPage('old', [5, 6], true), replacement = historyPage('new', [1, 2, 3]);
  let reads = 0;
  const {model, visible, snapshots, errors} = historyFixture(async path => {
    if (path.includes('before_seq=5')) return historyPage('new', [1, 2]);
    if (++reads === 1) return old;
    if (reads === 2) throw new Error('replacement temporarily unavailable');
    return replacement;
  });
  await model.open();
  await model.older();
  assert.deepEqual(visible.at(-1), {items: [], hasMore: false, ready: false});
  assert.equal(snapshots.at(-1).history, 'new');
  assert.deepEqual(snapshots.at(-1).items, []);
  assert.equal(snapshots.at(-1).covered, 0);
  assert.deepEqual(errors, ['replacement temporarily unavailable']);
  await model.latest();
  assert.deepEqual(visible.at(-1).items, replacement.items);
  assert.equal(snapshots.at(-1).covered, 3);
});

for (const read of ['latest', 'older', 'fill', 'fold']) {
  test(`a pending ${read} response cannot restore the old transcript after a replacement push`, async () => {
    const old = historyPage('old', [5, 6], true), replacement = historyPage('new', [1, 2, 3]);
    const fold = {id:'fold:2#0', seq:2, kind:'fold', fold:{through_seq:4, through_id:'4#0', tools:1, thoughts:0}};
    if (read === 'fold') {
      old.items.unshift(fold);
      replacement.items.splice(1, 1, fold);
    }
    let release, reads = 0;
    const pending = new Promise(resolve => {release = resolve;});
    const {model, visible, snapshots} = historyFixture(async () => {
      if (++reads === 1) return old;
      return reads === 2 ? pending : replacement;
    });
    await model.open();
    const target = {type:'items', machine_id:'machine-one', session_id:'session-one'};
    const start = {
      latest: () => model.latest(), older: () => model.older(),
      fill: () => model.receive({...target, ...historyPage('old', [9]), from_seq:8}),
      fold: () => model.unfold(fold.id),
    };
    const late = {
      latest: old, older: historyPage('old', [1, 2]), fill: {...historyPage('old', [7, 8]), from_seq:6},
      fold: {items:[{id:'2#0', seq:2, kind:'tool_use', tool_use_id:'old-call'}], more:false},
    };
    const request = start[read]();
    assert.equal(reads, 2);
    model.receive({...target, ...replacement, from_seq:0});
    const firstReplacement = visible.length - 1;
    release(late[read]);
    await request;
    await new Promise(resolve => setImmediate(resolve));
    for (const state of visible.slice(firstReplacement)) assert.deepEqual(state.items, replacement.items);
    for (const snapshot of snapshots.slice(firstReplacement)) {
      assert.equal(snapshot.history, 'new');
      assert.equal(snapshot.covered, 3);
      assert.deepEqual(snapshot.items, replacement.items);
    }
  });
}

test('an older page without a history identifier can still extend a legacy transcript', async () => {
  const recent = historyPage('legacy', [3, 4], true), older = historyPage('legacy', [1, 2]);
  delete recent.history; delete older.history;
  const {model, snapshots} = historyFixture(async path => path.includes('before_seq=3') ? older : recent);
  await model.open();
  await model.older();
  assert.deepEqual(snapshots.at(-1).items, [...older.items, ...recent.items]);
  assert.equal(snapshots.at(-1).hasMore, false);
});

test('a replacement that needs catch-up can fetch its tail while the old fill is still in flight', async () => {
  let release;
  const oldFill = new Promise(resolve => {release = resolve;});
  const {model, snapshots} = historyFixture(async path => {
    if (path.includes('after_seq=6')) return oldFill;
    if (path.includes('after_seq=1')) return {...historyPage('new', [2, 3]), from_seq:1};
    return historyPage('old', [5, 6], true);
  });
  await model.open();
  const target = {type:'items', machine_id:'machine-one', session_id:'session-one'};
  model.receive({...target, ...historyPage('old', [9]), from_seq:8});
  model.receive({...target, ...historyPage('new', [1]), from_seq:0, more_after:true});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(snapshots.at(-1).covered, 3);
  release({...historyPage('old', [7, 8]), from_seq:6});
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(snapshots.at(-1).items, historyPage('new', [1, 2, 3]).items);
});

test('an in-flight latest page matching the replacement push can finish the open without another read', async () => {
  const replacement = historyPage('current', [1, 2]);
  let allowPage, started, reads = 0;
  const pending = new Promise(resolve => {allowPage = resolve;});
  const requested = new Promise(resolve => {started = resolve;});
  const {model, snapshots} = historyFixture(async () => {reads++; started(); return pending;});
  const opening = model.open();
  await requested;
  model.receive({...replacement, type:'items', machine_id:'machine-one', session_id:'session-one', from_seq:0});
  allowPage(replacement);
  await opening;
  assert.equal(reads, 1);
  assert.deepEqual(snapshots.at(-1).items, replacement.items);
});
