import assert from 'node:assert/strict';
import test from 'node:test';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { existsSync, readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.ArkTS') return { url: 'mock:arkts', shortCircuit: true };
    if (context.parentURL?.endsWith('/models/ExtensionUI.ets')) {
      if (specifier === './ClientModel') return { url: 'mock:client', shortCircuit: true };
      if (specifier === './HubRepository') return { url: 'mock:repository', shortCircuit: true };
      if (specifier === '../design/Strings') return { url: 'mock:strings', shortCircuit: true };
    }
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      const base = new URL(specifier, context.parentURL);
      return { url: base.href + (existsSync(new URL(base.href + '.ts')) ? '.ts' : '.ets'), shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'mock:arkts') return { format: 'module', source: "export const url = { URL: { parseURL: value => new URL(value) } }; export const util = { TextEncoder: { create: () => ({ encodeInto: value => new TextEncoder().encode(value) }) }, generateRandomUUID: () => 'id-' + Math.random().toString(16).slice(2) };", shortCircuit: true };
    if (url === 'mock:client') return { format: 'module', source: 'export class ClientModel {}', shortCircuit: true };
    if (url === 'mock:repository') return { format: 'module', source: 'export function segment(value) { return encodeURIComponent(value); }', shortCircuit: true };
    if (url === 'mock:strings') return { format: 'module', source: 'export function tr(value) { return value; }', shortCircuit: true };
    if (url.endsWith('/ExtensionReviewPayload.ts')) {
      return { format: 'module', source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8')), shortCircuit: true };
    }
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const payload = await import('../entry/src/main/ets/core/ExtensionReviewPayload.ts');
const { ExtensionUIModel } = await import('../entry/src/main/ets/models/ExtensionUI.ets');
const { Wire } = await import('../entry/src/main/ets/models/Wire.ets');
const renderer = readFileSync(new URL('../entry/src/main/ets/components/ExtensionView.ets', import.meta.url), 'utf8');

function rendererMethod(name) {
  const start=renderer.indexOf('  private '+name+'(');assert.ok(start>=0,name);const body=renderer.indexOf('{',renderer.indexOf('):',start));let depth=1,quote='',escape=false,end=body+1;
  for(;end<renderer.length&&depth;end++){const ch=renderer[end];if(quote){if(escape)escape=false;else if(ch==='\\')escape=true;else if(ch===quote)quote='';}else if(ch==='"'||ch==="'"||ch==='`')quote=ch;else if(ch==='{')depth++;else if(ch==='}')depth--;}
  assert.equal(depth,0,name);return renderer.slice(start,end);
}
const lineStateSource = renderer.slice(renderer.indexOf('class ExtensionLineState {'), renderer.indexOf('/** A picture'));
let serializedComponents = 0;
const rendererJSON = {parse: JSON.parse, stringify(value) { if (value?.type === 'code' || value?.type === 'diff') serializedComponents++; return JSON.stringify(value); }};
const NativeLineRenderer = new Function('JSON','Wire','lineWindowOffset','objectValues','records',stripTypeScriptTypes(lineStateSource)+';return '+stripTypeScriptTypes('class NativeLineRenderer {'+['renderView','sections','sectionItems','runAction','rememberLineRequest','lineComponentIdentity','lineState','matchingLineResult','reuseLineResult','lineResultValues','lineResultOffset','moveLineWindow','hasEarlierLineWindow','hasMoreLineWindows'].map(rendererMethod).join('\n')+'}'))(rendererJSON,Wire,payload.lineWindowOffset,value=>Array.isArray(value)?value:[],value=>Array.isArray(value)?value:[]);
function nativeLines(model) {
  const native = new NativeLineRenderer();
  native.model = model; native.rootKey = () => '@root'; native.lineResultsRevision = 0;
  native.lineIdentityView = undefined; native.lineIdentityRevision = -1; native.lineIdentities = new Map();
  native.linePagingRevision = 0; native.lineStates = new Map(); native.lineOwner = undefined; native.lineRoot = '';
  return native;
}

function fixtureClient() {
  const state = { calls: [] };
  const repository = {
    transport: { send() { return true; }, async request() { return '{}'; }, async requestBytes() { return { data: new ArrayBuffer(0), contentType: '' }; } },
    async call(path, method, body, op, admit) { state.calls.push({ path, method, body, op, admitted: admit ? admit() : true }); return {}; }
  };
  const client = {
    profile: { id: 'profile', manager: 'manager' }, connected: true, features: [],
    connectionGeneration() { return 1; }, scope() { return 'hub:manager'; }, owns(generation, scope) { return generation === 1 && scope === 'hub:manager'; },
    observeEvents() { return () => {}; }, repo() { return repository; }
  };
  return { client, repository, state };
}

function viewWith(item) { return { sections: [{ key: 'section', items: [item] }] }; }

test('diff context requests only exact hidden ranges on valid file sides', () => {
  const hunks = [
    { old_start: 10, new_start: 20, lines: ['+a', '+b', '+c', '+d', '+e'] },
    { old_start: 30, new_start: 50, lines: [' unchanged'] }
  ];
  const ranges = payload.diffContextRanges('modified', hunks);
  const beforeNew = ranges.find((range) => range.side === 'new' && range.boundary === 'top');
  const betweenNew = ranges.find((range) => range.side === 'new' && range.boundary === 'between');
  const afterNew = ranges.find((range) => range.side === 'new' && range.boundary === 'bottom');
  assert.deepEqual([beforeNew.from, beforeNew.to], [1, 19]);
  assert.deepEqual([betweenNew.from, betweenNew.to], [25, 49]);
  assert.equal(afterNew.from, 51);
  assert.equal(afterNew.to, 'end');
  assert.deepEqual(payload.diffContextValues('/repo/file.ts', betweenNew), {
    path: '/repo/file.ts', side: 'new', from: '25', to: '49'
  });
  assert.ok(payload.diffContextRanges('added', hunks).every((range) => range.side === 'new'));
  assert.ok(payload.diffContextRanges('deleted', hunks).every((range) => range.side === 'old'));
  const adjacent = [
    { old_start: 1, new_start: 1, lines: [' unchanged'] },
    { old_start: 2, new_start: 2, lines: [' unchanged'] }
  ];
  assert.equal(payload.diffContextRanges('modified', adjacent).filter((range) => range.boundary === 'between').length, 0);
});

test('legacy diff expansion uses only up/down and context actions take precedence', () => {
  const requests = payload.diffExpandRequests('/repo/file.ts', 'modified', [
    { old_start: 10, new_start: 20, lines: [' x'] },
    { old_start: 40, new_start: 50, lines: [' y'] }
  ]);
  assert.ok(requests.length > 0);
  assert.ok(requests.every((request) => request.path === '/repo/file.ts' && ['up', 'down'].includes(request.direction)));
  assert.doesNotMatch(renderer, /direction:\s*['"]all['"]/);
  assert.match(renderer, /if \(Wire\.has\(item, 'context'\)\) this\.diffContextButtons/);
  assert.match(renderer, /Wire\.has\(item, 'expand'\) && !Wire\.has\(item, 'context'\)/);
});

test('thread replies retain their original thread ID', () => {
  assert.deepEqual(payload.threadReplyValues('thread-7', 'reply'), { body: 'reply', thread: 'thread-7' });
  assert.match(renderer, /threadReplyValues\(Wire\.text\(thread, 'id'\)/);
});

test('returned line replies are path-correlated and bounded in the renderer', () => {
  assert.match(renderer, /private matchingLineResult\(key: string, path: string\)/);
  assert.match(renderer, /const resultMatches = Wire\.text\(result, 'path'\) === path && Array\.isArray\(result\['lines'\]\)/);
  assert.match(renderer, /lines\.slice\(state\.offset, state\.offset \+ state\.limit\)/);
  assert.match(renderer, /this\.returnedLines\(key, UIUtils\.makeBinding\(\(\): string => Wire\.text\(item, 'path'\)\)\)/);
});

test('multichoice display and toggling retain the initial selection', () => {
  const item = { value: ['kept', 'remove'] };
  const initial = payload.multiChoiceValue(item);
  assert.deepEqual(JSON.parse(initial), ['kept', 'remove']);
  assert.deepEqual(JSON.parse(payload.toggleMultiChoice(initial, 'remove')), ['kept']);
  assert.match(renderer, /multiChoiceValue\(item\)/);
});

test('charts expose raw negative values and candlestick OHLC in text rows', () => {
  const lineRows = payload.chartRows({ kind: 'line', unit: ' kWh', series: [{ name: 'Load', points: [
    { label: 'night', value: -2.5, display: '−2.5' }, { label: 'day', value: 3 }
  ] }] });
  assert.equal(lineRows[0].value, '−2.5 · -2.5 kWh');
  const candle = payload.chartRows({ kind: 'candlestick', unit: ' USD', series: [{ name: 'A', points: [
    { label: 'Mon', value: 12, open: 10, high: 15, low: 9, close: 12, volume: 40 }
  ] }] })[0];
  assert.deepEqual([candle.open, candle.high, candle.low, candle.close, candle.volume], ['10 USD', '15 USD', '9 USD', '12 USD', '40']);
  assert.doesNotMatch(renderer, /Progress\(\{ value: Math\.max\(0, Math\.min\(100, Math\.abs\(Wire\.number\(point, 'value'\)/);
  assert.match(renderer, /point\.open \+ '   H ' \+ point\.high \+ '   L ' \+ point\.low \+ '   C ' \+ point\.close/);
});

test('slider previews while moving, then commits at End or Click', () => {
  assert.equal(payload.sliderShouldCommit(0), false); // Begin
  assert.equal(payload.sliderShouldCommit(1), false); // Moving
  assert.equal(payload.sliderShouldCommit(2), true); // End
  assert.equal(payload.sliderShouldCommit(3), true); // Click
  assert.match(renderer, /\.onChange\(\(value: number, mode: SliderChangeMode\)/);
  assert.match(renderer, /if \(sliderShouldCommit\(mode\)\) this\.releaseSlider/);
  assert.match(renderer, /this\.queueControlValue\(control, key, String\(value\), -1\)/);
  assert.match(renderer, /this\.queueControlValue\(control, key, String\(next\), 600\)/);
  assert.doesNotMatch(renderer, /Button\(tr\('apply'\)\)/);
});

test('renderer allows same-flight edits and reverts a value when its real confirmation is canceled', async () => {
  assert.equal(payload.controlInputAllowed(false, true, false, true, true, true, true), true);
  assert.equal(payload.controlInputAllowed(true, true, false, true, true, true, true), false);
  assert.equal(payload.controlInputAllowed(false, false, false, true, true, true, true), false);
  assert.equal(payload.controlInputAllowed(false, true, false, false, true, false, true), false);
  assert.equal(payload.controlInputAllowed(false, true, false, true, true, true, false), false);
  assert.match(renderer, /\.enabled\(this\.controlCanEdit\(control, key\)\)/);
  assert.match(renderer, /private controlCanEdit\(control: ExtensionViewRecord, key: string\): boolean/);
  assert.match(renderer, /if \(this\.model\.busy\) for \(const state of this\.controlFlights\.values\(\)\) if \(state\.queue\.inFlight\(\)\) state\.observedBusy = true/);
  assert.match(renderer, /\.catch\(\(\): void => this\.failControl\(identity, state\)\)/);

  const queue = new payload.LatestControlValue('5');
  queue.queue('8', true);
  const submitted = queue.take();
  assert.equal(submitted, '8');
  queue.queue('12', true);
  assert.equal(queue.queuedValue(), '12');

  const liveFixture = fixtureClient(), liveModel = new ExtensionUIModel();
  let finishRequest;
  liveFixture.repository.call = async () => new Promise((resolve) => { finishRequest = resolve; });
  const liveControl = { kind: 'slider', name: 'level', value: 5, action: { id: 'volume' } };
  liveModel.bind(liveFixture.client, 'extension', 'home', {}, viewWith({ type: 'row', key: 'volume', control: liveControl }));
  const liveAction = liveModel.performAction('volume', 'volume', 'change', { level: submitted });
  assert.equal(liveModel.busy, true);
  assert.equal(liveModel.canInteract(), false);
  assert.equal(payload.controlInputAllowed(false, true, liveModel.canInteract(), liveModel.busy,
    queue.inFlight(), true, liveModel.error === ''), true);
  finishRequest({ lines: { path: 'file.ts', start: 1, lines: [] } });
  await liveAction;
  assert.equal(queue.complete('8'), '12');
  queue.fail('8');
  liveModel.dispose();

  const fixture = fixtureClient(), model = new ExtensionUIModel();
  const control = { kind: 'slider', name: 'level', value: 5, action: { id: 'volume', confirm: 'Change volume?' } };
  model.bind(fixture.client, 'extension', 'home', {}, viewWith({ type: 'row', key: 'volume', control }));
  const cancelQueue = new payload.LatestControlValue('5');
  cancelQueue.queue('8', true);
  const cancelSubmitted = cancelQueue.take();
  cancelQueue.queue('12', true);
  const request = model.performAction('volume', 'volume', 'change', { level: cancelSubmitted }).catch((error) => {
    cancelQueue.fail('5');
    throw error;
  });
  await Promise.resolve();
  assert.equal(model.canInteract(), false);
  assert.equal(payload.controlInputAllowed(false, true, model.canInteract(), model.busy, cancelQueue.inFlight(), false, model.error === ''), false);
  model.confirmAction(model.confirmationToken, false);
  await assert.rejects(request, /cancelled/);
  assert.equal(cancelQueue.value(), '5');
  assert.equal(fixture.state.calls.length, 0);
  model.dispose();
});

test('latest control edits survive a view update until the first request settles', () => {
  const queue = new payload.LatestControlValue('5');
  queue.queue('10', true);
  assert.equal(queue.take(), '10');
  queue.queue('15', true);
  queue.queue('20', true);
  assert.equal(queue.queuedValue(), '20');
  assert.equal(queue.complete('12'), '20');
  assert.equal(queue.inFlight(), true);
  queue.fail('12');
  assert.equal(queue.value(), '12');
  assert.equal(queue.hasQueued(), false);
  assert.match(renderer, /if \(state\.queue\.inFlight\(\)\) \{ if \(viewChanged\) state\.authoritativeView = true; continue; \}\s*if \(changed \|\| viewChanged && state\.queue\.hasQueued\(\)\)/);
  assert.match(renderer, /state\.authoritativeView \|\| serverValue !== state\.previous \? serverValue : state\.submitted/);
});

test('visible images obey decoded-pixel, concurrency and retention budgets', async () => {
  const size = payload.boundedImageSize(2400, 1600);
  assert.ok(size.pixels <= 1000000);
  assert.ok(Math.max(size.width, size.height) <= 1600);
  const budget = new payload.ExtensionImageBudget(1, 8, 5000);
  const first = budget.request(1296), second = budget.request(1296), cancelled = budget.request(1296);
  const firstLease = await first.promise;
  assert.deepEqual(budget.counts(), { activeLoads: 1, retainedImages: 1, retainedPixels: 1296, waiting: 2 });
  budget.cancel(cancelled);
  assert.equal(await cancelled.promise, undefined);
  firstLease.finishLoad();
  const secondLease = await second.promise;
  assert.deepEqual(budget.counts(), { activeLoads: 1, retainedImages: 2, retainedPixels: 2592, waiting: 0 });
  firstLease.release(); secondLease.finishLoad(); secondLease.release();
  assert.deepEqual(budget.counts(), { activeLoads: 0, retainedImages: 0, retainedPixels: 0, waiting: 0 });
  assert.equal((renderer.match(/ExtensionImage\(\{ ui: this\.model, budget: this\.imageBudget, active: this\.active/g) || []).length, 3);
  assert.match(renderer, /this\.budget\.request\(this\.displayPixelBudget\(\)\)/);
  assert.match(renderer, /boundedImageSize\(info\.size\.width, info\.size\.height, this\.displayPixelBudget\(\)\)/);
});

test('six small visible images can all settle and pixel overbudget requests fall back immediately', async () => {
  const budget = new payload.ExtensionImageBudget(2, 64, 4000000);
  const tickets = Array.from({ length: 6 }, () => budget.request(1296));
  const leases = [];
  for (let index = 0; index < 2; index++) leases[index] = await tickets[index].promise;
  for (let index = 2; index < tickets.length; index++) {
    leases[index - 2].finishLoad();
    leases[index] = await tickets[index].promise;
  }
  for (const lease of leases) lease.finishLoad();
  assert.equal(budget.counts().retainedImages, 6);
  assert.equal(budget.counts().retainedPixels, 6 * 1296);
  const full = new payload.ExtensionImageBudget(1, 4, 2000);
  const first = await full.request(1800).promise;
  assert.equal(await full.request(1800).promise, undefined);
  first.release();
  assert.equal(full.counts().waiting, 0);
  for (const lease of leases) lease.release();
});

test('whole-file line replies open at the requested interval and expose bounded adjacent pages', async () => {
  assert.equal(payload.lineWindowOffset(2000, 1, 3000), 1999);
  assert.equal(payload.lineWindowOffset(2000, 1, 500), 0);
  assert.equal(payload.lineWindowOffset(1, 2000, 200), 0);
  assert.match(renderer, /this\.rememberLineRequest\(key, payload\)/);

  assert.match(renderer, /lines\.slice\(state\.offset, state\.offset \+ state\.limit\)/);
  assert.match(renderer, /Button\(tr\('back'\)\)\.onClick\(\(\) => this\.moveLineWindow\(key, path\.value, false\)\)/);
  assert.match(renderer, /Button\(tr\('more'\)\)\.onClick\(\(\) => this\.moveLineWindow\(key, path\.value, true\)\)/);
  assert.doesNotMatch(renderer, /more lines/);

  const fixture = fixtureClient(), model = new ExtensionUIModel();
  const wholeFile = Array.from({ length: 3000 }, (_value, index) => 'line ' + String(index + 1));
  fixture.repository.call = async () => ({ lines: { path: 'file.ts', side: 'new', start: 1, lines: wholeFile, total: wholeFile.length } });
  model.bind(fixture.client, 'extension', 'home', {}, viewWith({ type: 'diff', key: 'file', path: 'file.ts', context: { id: 'context' } }));
  await model.performAction('file', 'context', 'press', { path: 'file.ts', side: 'new', from: '2000', to: '2050' });
  const native = nativeLines(model);
  native.rememberLineRequest('file',{path:'file.ts',side:'new',from:'2000',to:'2050'});native.lineResultsRevision=model.lineResultRevision;
  const firstPage=native.lineResultValues('file','file.ts');assert.deepEqual([firstPage[0],firstPage.at(-1)],['line 2000','line 2050']);assert.equal(native.hasMoreLineWindows('file','file.ts'),true);
  native.moveLineWindow('file','file.ts',true);assert.deepEqual(native.lineResultValues('other','other.ts'),[]);let page=native.lineResultValues('file','file.ts');assert.deepEqual([page[0],page.at(-1)],['line 2051','line 2550']);
  native.moveLineWindow('file','file.ts',false);assert.deepEqual(native.lineResultValues('file','file.ts'),firstPage);
  native.moveLineWindow('file','file.ts',false);page=native.lineResultValues('file','file.ts');assert.deepEqual([page[0],page.at(-1)],['line 1500','line 1999']);
  assert.equal(model.lineResultFor('file').side, 'new');
  model.dispose();
});

test('component line caches survive other replies and cached ranges avoid another dispatch', () => {
  let latest = 'a', calls = 0;
  const replies = {
    a: { path: 'a.ts', side: 'new', start: 1, total: 2000, lines: Array.from({length:2000}, (_,i) => 'a'+(i+1)) },
    b: { path: 'b.ts', side: 'old', start: 10, total: 200, lines: Array.from({length:191}, (_,i) => 'b'+(i+10)) }
  };
  const model = { revision: 1, view: {sections:[{items:[{type:'diff',key:'a',path:'a.ts',revision:'commit-1'},{type:'diff',key:'b',path:'b.ts'}]}]}, getSnapshot: () => undefined, canInteract: () => true,
    lineResultFor: key => key === latest ? replies[key] : {},
    performAction: async () => { calls++; } };
  const native = nativeLines(model);
  native.rememberLineRequest('a',{path:'a.ts',side:'new',from:'1000',to:'1050'}); native.lineResultsRevision = 1;
  assert.equal(native.lineResultValues('a','a.ts')[0], 'a1000');
  native.moveLineWindow('a','a.ts',true);
  assert.deepEqual(native.lineResultValues('b','b.ts'),[]);
  assert.equal(native.lineResultValues('a','a.ts')[0], 'a1051');
  native.rememberLineRequest('b',{path:'b.ts',side:'old',from:'40',to:'70'}); latest = 'b'; native.lineResultsRevision = 2;
  assert.equal(native.lineResultValues('b','b.ts')[0], 'b40');
  assert.equal(native.lineResultValues('a','a.ts')[0], 'a1051');
  native.moveLineWindow('a','a.ts',false);
  assert.deepEqual([native.lineResultValues('a','a.ts')[0],native.lineResultValues('a','a.ts').at(-1)], ['a1000','a1050']);
  native.runAction({id:'context'},'a','press',{path:'a.ts',side:'new',from:'1800',to:'1850'});
  assert.equal(calls,0);assert.equal(native.lineResultValues('a','a.ts')[0],'a1800');
  const beforeRender = serializedComponents;
  for(let index=0;index<500;index++) { native.matchingLineResult('a','a.ts'); native.lineResultOffset('a','a.ts'); }
  assert.equal(serializedComponents, beforeRender); // Full components are serialized once per published View, never per row.
  native.runAction({id:'context'},'a','press',{path:'a.ts',side:'new',from:'1950',to:'end'});
  assert.equal(calls,0);assert.equal(native.lineResultValues('a','a.ts').at(-1),'a2000');
  native.runAction({id:'context'},'a','press',{path:'a.ts',side:'old',from:'10',to:'20'});
  assert.equal(calls,1); // Different side is never a cache hit.
  native.runAction({id:'context'},'b','press',{path:'b.ts',side:'old',from:'1',to:'20'});
  assert.equal(calls,2); // The requested interval must be covered in full.
  model.view = {sections:[{items:[{...model.view.sections[0].items[0],revision:'commit-2'},model.view.sections[0].items[1]]}]}; model.revision++;
  latest = 'a'; // Even the model's most recent result is from the old commit.
  native.runAction({id:'context'},'a','press',{path:'a.ts',side:'new',from:'1800',to:'1850'});
  assert.equal(calls,3);assert.deepEqual(native.lineResultValues('a','a.ts'),[]);
  model.getSnapshot = () => ({session:'new-session',epoch:'new-epoch'});
  latest = ''; assert.deepEqual(native.lineResultValues('a','a.ts'),[]);
});

test('full-page renderer can omit the title already shown by its host', () => {
  assert.match(renderer, /if \(this\.showTitle && Wire\.text\(this\.renderView\(\), 'title'\)\)/);
});
