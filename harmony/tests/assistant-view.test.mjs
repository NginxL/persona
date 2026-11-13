import assert from 'node:assert/strict';
import test from 'node:test';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync, existsSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.ArkTS') return { url: 'mock:arkts', shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      const base = new URL(specifier, context.parentURL);
      const ts = new URL(base.href + '.ts');
      return { url: existsSync(ts) ? ts.href : base.href + '.ets', shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'mock:arkts') return { format: 'module', source: `
      export const url = { URL: { parseURL: value => new URL(value) } };
      export const util = {
        TextEncoder: class { encodeInto(value) { return new TextEncoder().encode(value); } },
        generateRandomUUID: (() => { let id = 0; return () => 'assistant-view-' + (++id); })()
      };
    `, shortCircuit: true };
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8')
        .replace(/@ObservedV2\s*/g, '')
        .replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { AssistantViewModel, assistantViewOpenTarget, parseAssistantViewBlocks } =
  await import('../entry/src/main/ets/models/AssistantView.ets');
const assistantModelSource = readFileSync(new URL('../entry/src/main/ets/models/AssistantView.ets', import.meta.url), 'utf8');
const assistantCardSource = readFileSync(new URL('../entry/src/main/ets/components/AssistantViewCard.ets', import.meta.url), 'utf8');
const presentationSource = readFileSync(new URL('../entry/src/main/ets/core/ExtensionPresentation.ts', import.meta.url), 'utf8');

let fixtureID = 0;

function viewWith(action, extra = {}) {
  return {
    title: 'Choice',
    fallback: 'The card fallback.',
    sections: [{ items: [{ type: 'button', key: 'choose', title: 'Choose', action }] }],
    ...extra
  };
}

function setup(options = {}) {
  const n = ++fixtureID;
  const profileID = 'profile-' + n;
  const manager = 'manager-' + n;
  const conversation = 'conversation-' + n;
  const itemID = 'item-' + n;
  const key = itemID + '/0';
  const card = options.view || viewWith({ id: 'choose', reply: 'Use this option' });
  const item = options.item || { id: itemID, role: 'assistant', kind: 'text', text: '```persona-view\n' + JSON.stringify(card) + '\n```' };
  const state = { calls: [], retries: [], records: [], handler: options.handler, retryHandler: options.retryHandler,
    generation: 1, connected: true, blocked: new Set(), beginGate: undefined, beginStarted: undefined };
  const repository = {
    mutations: {
      blocked() { return state.records.filter((record) => ['pending', 'unknown'].includes(record.state)); },
      async finish(record, next) { record.state = next; }
    },
    async call(path, method, body, op, admit) {
      const record = { id: op, path, method, state: 'pending' };
      state.records.push(record);
      if (state.beginGate) {
        if (state.beginStarted) state.beginStarted();
        await state.beginGate;
      }
      if (admit && !admit()) {
        record.state = 'failed';
        const error = new Error('request_updated'); error.code = 'request_updated'; error.status = 0; throw error;
      }
      state.calls.push({ path, method, body, op });
      try {
        const result = state.handler ? await state.handler({ path, method, body, op }, state) : {};
        record.state = 'done';
        return result;
      } catch (error) {
        record.state = error.resultUnknown ? 'unknown' : 'failed';
        throw error;
      }
    },
    transport: {
      async request(path, method, body, op) {
        state.retries.push({ path, method, body, op });
        if (state.retryHandler) return JSON.stringify(await state.retryHandler({ path, method, body, op }, state));
        return JSON.stringify({});
      }
    }
  };
  const selected = { kind: 'conversation', id: conversation, machine: '' };
  const client = {
    profile: { id: profileID, manager },
    unresolvedWrites: [],
    get connected() { return state.connected; },
    scope() { return 'hub:' + profileID + ':' + manager; },
    connectionGeneration() { return state.generation; },
    owns(generation, scope) { return generation === state.generation && scope === this.scope(); },
    target(row = selected) { return JSON.stringify([this.scope(), row.kind, row.machine || '', row.id]); },
    operationBlocked(path) { return state.records.some((record) => record.path === path && ['pending', 'unknown'].includes(record.state)); },
    repo() { return repository; }
  };
  const feed = {
    owner: manager,
    conversationId: conversation,
    views: {},
    viewActions: {},
    viewBlocks: options.registered === false ? [] : [key],
    async refresh() { if (options.refresh) await options.refresh(feed); }
  };
  const model = AssistantViewModel.acquire(client, feed, item, key, true);
  return { model, client, feed, item, key, card, state, repository, conversation, manager, profileID };
}

function observe(promise) {
  return promise.then(() => ({ ok: true }), (error) => ({ ok: false, error }));
}

function takeConfirmation(model) {
  const snapshot = model.selectionSnapshot;
  const lease = model.confirmationLease(snapshot.token);
  const selection = model.takeConfirmation(snapshot.token);
  return { selection, lease };
}

function failure(code, status = 502, resultUnknown = true) {
  const error = new Error(code);
  error.code = code;
  error.status = status;
  error.resultUnknown = resultUnknown;
  return error;
}

function actionRecord(key, action, clientMsgID, values = {}, revision = 1, status = 'pending', version = 1) {
  return { key, action, client_msg_id: clientMsgID, values, revision, status, version, kind: 'action' };
}

test('assistant card satisfies the current shared renderer contract without extension actions', () => {
  assert.match(presentationSource, /error: string;[\s\S]*lineResultRevision: number;/);
  assert.match(assistantModelSource, /implements ExtensionPresentation/);
  assert.match(assistantModelSource, /@Trace error: string/);
  assert.match(assistantModelSource, /@Trace lineResultRevision: number/);
  assert.match(assistantModelSource, /getSnapshot\(\): undefined/);
  assert.match(assistantModelSource, /lineResultFor\(_key: string\): ExtensionViewRecord/);
  assert.match(assistantCardSource, /ExtensionView\(\{ model: this\.presentation, compact: true, active: this\.effectiveActive\(\), showTitle: false/);
  assert.match(assistantCardSource, /onVisibleAreaChange\(\[0, 1\]/);
  assert.match(assistantCardSource, /acquire\(this\.model, this\.feed, this\.item, this\.cardKey, this\.effectiveActive\(\)\)/);
  assert.match(assistantCardSource, /this\.presentation\.setVisible\(this\.effectiveActive\(\)\)/);
  assert.match(assistantCardSource, /if \(!presentation \|\| !this\.effectiveActive\(\)\) return/);
  assert.match(assistantCardSource, /cancel: \(\) => presentation\.declineConfirmation\(token\)/);
  assert.match(assistantCardSource, /takeConfirmation\(token\)/);
  assert.match(assistantCardSource, /confirmationLeaseIsCurrent\(lease\)/);
  assert.match(assistantCardSource, /hostVisit: number/);
  assert.match(assistantCardSource, /sourceChanged\(\): void \{\s*if \(this\.mounted\) this\.bindPresentation\(\);\s*\}/);
  assert.match(assistantCardSource, /feedChanged\(\): void \{\s*if \(this\.mounted\) this\.bindPresentation\(\);\s*\}/);
  assert.match(assistantModelSource, /takeConfirmation\(expectedToken: string\)/);
  assert.doesNotMatch(assistantModelSource, /\/api\/v1\/extensions\/[^\s'"`]*action/i);
});

test('assistant fence parser preserves indexes across invalid blocks and follows shared fence examples', () => {
  const invalid = '```persona-view\ninvalid\n```\n```persona-view\n{"fallback":"ok","sections":[]}\n```';
  const blocks = parseAssistantViewBlocks('trusted-item', invalid);
  assert.deepEqual(blocks.map((block) => [block.key, block.index, block.valid]), [
    ['trusted-item/0', 0, false], ['trusted-item/1', 1, true]
  ]);
  assert.equal(setup({ view: { sections: [] } }).model.valid, false, 'assistant views require a readable fallback');
  assert.equal(parseAssistantViewBlocks('trusted-item', '````markdown\n```persona-view\n{}\n```\n````').length, 0);
  const vectors = JSON.parse(readFileSync(new URL('../../schema/extension/examples/assistant-text/fences.json', import.meta.url)));
  for (const vector of vectors) {
    const parsed = parseAssistantViewBlocks('item', vector.text);
    assert.deepEqual(parsed.map((block) => block.index), vector.indexes, vector.name);
    assert.deepEqual(parsed.map((block) => block.complete), vector.complete, vector.name);
  }
});

test('assistant fence parser reports source spans for body interleaving', () => {
  const complete = '```persona-view\r\n{"fallback":"First","sections":[]}\r\n```\r\n';
  const incomplete = '```persona-view\n{"fallback":"Second","sections":[]}\n';
  const text = '😀 Assistant reply\r\n' + complete + 'Between blocks\n' + incomplete;
  const blocks = parseAssistantViewBlocks('body-item', text);
  assert.equal(blocks.length, 2);
  assert.equal(blocks[0].start, text.indexOf(complete));
  assert.equal(blocks[0].end, blocks[0].start + complete.length);
  assert.equal(text.slice(blocks[0].start, blocks[0].end), complete);
  assert.equal(blocks[1].start, text.indexOf(incomplete));
  assert.equal(blocks[1].end, text.length);
  assert.equal(text.slice(blocks[1].start, blocks[1].end), incomplete);
});

test('assistant fallback limit counts Unicode characters separately from the byte budget', () => {
  const allowed = '界😀'.repeat(1000);
  const rejected = allowed + '界';
  const parsedAllowed = parseAssistantViewBlocks('fallback-allowed', '```persona-view\n' +
    JSON.stringify({ fallback: allowed, sections: [] }) + '\n```')[0];
  const parsedRejected = parseAssistantViewBlocks('fallback-rejected', '```persona-view\n' +
    JSON.stringify({ fallback: rejected, sections: [] }) + '\n```')[0];
  assert.equal(Array.from(allowed).length, 2000);
  assert.equal(parsedAllowed.valid, true);
  assert.equal(parsedAllowed.fallback, allowed);
  assert.equal(parsedRejected.valid, false);
  assert.equal(parsedRejected.fallback, '');
});

test('body cards remain read-only until their exact key is registered', () => {
  const f = setup({ registered: false });
  assert.equal(f.model.valid, true);
  assert.equal(f.model.complete, true);
  assert.equal(f.model.canInteract(), false);
  f.feed.viewBlocks = [f.key];
  const bound = AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, true);
  assert.equal(bound, f.model);
  assert.equal(bound.canInteract(), true);
});

test('live snapshots and action records merge monotonically by version and revision', () => {
  const key = 'view_live-' + (++fixtureID);
  const f = setup({ item: { id: 'live-item-' + fixtureID, view: {} } });
  f.feed.views[key] = { id: key, conversation: f.conversation, version: 4, view: viewWith({ id: 'choose', reply: 'Version four' }) };
  let model = AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  assert.equal(model.viewVersion, 4);
  assert.equal(model.changedNeedsConfirmation, false, 'an initial version is not a change the user must reconfirm');
  f.feed.views[key] = { id: key, conversation: f.conversation, version: 3, view: viewWith({ id: 'choose', reply: 'Old' }) };
  model = AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  assert.equal(model.viewVersion, 4);
  f.feed.viewActions[key] = actionRecord(key, 'choose', 'msg', { note: 'first' }, 2, 'pending', 4);
  model = AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  assert.equal(model.actionRevision, 2);
  f.feed.viewActions[key] = actionRecord(key, 'choose', 'msg', { note: 'first' }, 1, 'sent', 4);
  model = AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  assert.equal(model.actionState, 'pending');
  assert.equal(model.actionRevision, 2);
  f.feed.viewActions[key] = actionRecord(key, 'other', 'different-msg', { note: 'changed' }, 3, 'sent', 4);
  model = AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  assert.equal(model.actionState, 'pending', 'a higher revision cannot replace a different immutable choice');
  assert.equal(model.actionRevision, 2);
});

test('unrelated feed updates and equivalent item snapshots preserve an open confirmation', async () => {
  const f = setup({ view: viewWith({ id: 'choose', reply: 'Use this option', confirm: 'Confirm this option' }) });
  const completion = observe(f.model.performAction('choose', 'choose'));
  const token = f.model.selectionSnapshot.token;
  const lease = f.model.confirmationLease(token);
  assert.ok(lease);
  const otherKey = 'view_other-' + fixtureID;
  f.feed.views[otherKey] = { id: otherKey, conversation: f.conversation, version: 2,
    view: viewWith({ id: 'other', reply: 'Another card' }) };
  f.feed.viewActions[otherKey] = actionRecord(otherKey, 'other', 'other-message', {}, 1, 'pending', 2);
  f.feed.viewBlocks = [f.key, otherKey];
  assert.equal(AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, true), f.model);
  assert.equal(f.model.selectionSnapshot.token, token);
  assert.equal(f.model.confirmationLeaseIsCurrent(lease), true);

  const parsedItem = JSON.parse(JSON.stringify(f.item));
  const equivalentItem = {};
  for (const key of Object.keys(parsedItem).reverse()) equivalentItem[key] = parsedItem[key];
  assert.equal(AssistantViewModel.acquire(f.client, f.feed, equivalentItem, f.key, true), f.model);
  assert.equal(f.model.selectionSnapshot.token, token);
  assert.equal(f.model.confirmationLeaseIsCurrent(lease), true);

  const selection = f.model.takeConfirmation(token);
  assert.ok(selection);
  await f.model.confirmSelection(selection, lease);
  assert.equal((await completion).ok, true);
  assert.equal(f.state.calls.length, 1);
});

test('ambiguous assistant submission retries the captured body and settles its original journal receipt', async () => {
  const f = setup({ view: viewWith({ id: 'choose', reply: 'Use this option', confirm: 'Confirm this option' }),
    handler: async () => { throw failure('transport_timeout'); }, retryHandler: async (request) => {
    const body = JSON.parse(request.body);
    return { action: actionRecord(f.key, body.action, body.client_msg_id, body.values, 1, 'pending', body.expected_version) };
  } });
  let settled = false;
  const completion = observe(f.model.performAction('choose', 'choose')).then((value) => { settled = true; return value; });
  await Promise.resolve();
  assert.equal(settled, false, 'the completion remains pending while native confirmation is open');
  assert.equal(f.state.calls.length, 0, 'opening confirmation does not submit the action');
  const confirmation = takeConfirmation(f.model);
  await assert.rejects(f.model.confirmSelection(confirmation.selection, confirmation.lease), /transport_timeout/);
  const outcome = await completion;
  assert.equal(outcome.ok, false);
  assert.match(outcome.error.message, /transport_timeout/);
  assert.equal(f.state.calls.length, 1);
  assert.match(f.state.calls[0].path, /views\/item-\d+%2F0\/actions\?manager=/);
  assert.equal(f.state.calls[0].body.expected_version, 1);
  assert.equal(f.model.canRetryOriginal, true);
  await f.model.retryOriginal();
  assert.equal(f.state.retries.length, 1);
  assert.equal(f.state.retries[0].body, JSON.stringify(f.state.calls[0].body));
  assert.equal(f.state.retries[0].op, f.state.calls[0].op);
  assert.equal(f.state.records[0].state, 'done');
  assert.equal(f.model.hasActionRecord, true);
  assert.equal(f.model.canRetryOriginal, false);
});

test('deferred journal admission rejects a confirmed attempt after hide and same-conversation show', async () => {
  const f = setup({ view: viewWith({ id: 'choose', reply: 'Use this option', confirm: 'Confirm this option' }) });
  let signalStarted;
  let releaseBegin;
  const started = new Promise((resolve) => { signalStarted = resolve; });
  const beginGate = new Promise((resolve) => { releaseBegin = resolve; });
  f.state.beginGate = beginGate;
  f.state.beginStarted = signalStarted;

  const completion = observe(f.model.performAction('choose', 'choose'));
  const confirmation = takeConfirmation(f.model);
  const confirmed = observe(f.model.confirmSelection(confirmation.selection, confirmation.lease));
  await started;

  f.model.hide();
  assert.equal(AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, true), f.model);
  releaseBegin();

  const confirmedOutcome = await confirmed;
  const actionOutcome = await completion;
  assert.equal(confirmedOutcome.error.message, 'cancelled');
  assert.equal(actionOutcome.error.message, 'cancelled');
  assert.equal(f.state.records[0].state, 'failed');
  assert.equal(f.state.calls.length, 0, 'the old attempt is refused before transport dispatch');
  assert.equal(f.state.retries.length, 0);
});

test('a confirmed action whose follow-up feed read fails still locks after acknowledgement', async () => {
  const card = {
    title: 'Choice', fallback: 'The card fallback.', sections: [{ items: [
      { type: 'button', key: 'choose', title: 'Send', action: { id: 'choose', reply: 'Send the reply', confirm: 'Confirm this exact reply' } },
      { type: 'button', key: 'details', title: 'Details', action: { id: 'details', push: { page: 'detail' } } }
    ] }],
    pages: { detail: { title: 'Details', sections: [{ items: [
      { type: 'button', key: 'back', title: 'Back', action: { id: 'back', pop: true } }
    ] }] } }
  };
  const f = setup({
    view: card,
    handler: async ({ body }) => ({ action: actionRecord(f.key, body.action, body.client_msg_id, body.values, 1, 'pending', body.expected_version) }),
    refresh: async () => { throw new Error('offline'); }
  });
  let settled = false;
  const completion = observe(f.model.performAction('choose', 'choose')).then((value) => { settled = true; return value; });
  await Promise.resolve();
  assert.equal(settled, false, 'native confirmation alone does not finish the action');
  assert.equal(f.state.calls.length, 0);
  const confirmation = takeConfirmation(f.model);
  await f.model.confirmSelection(confirmation.selection, confirmation.lease);
  assert.equal((await completion).ok, true, 'the action resolves after its acknowledgement');
  assert.equal(f.state.calls.length, 1);
  assert.equal(f.state.calls[0].body.confirmed, true);
  assert.equal(f.model.hasActionRecord, true);
  assert.equal(f.model.canRetryOriginal, false);
  assert.equal(f.model.canInteract(), true);
  await f.model.performAction('details', 'details');
  assert.equal(f.model.currentPage, 'detail', 'local page navigation remains available after the reply is acknowledged');
  await f.model.performAction('back', 'back');
  assert.equal(f.model.currentPage, '');
  const confirmationRevision = f.model.confirmationRevision;
  await assert.rejects(f.model.performAction('choose', 'choose'), /already_answered/);
  assert.equal(f.state.calls.length, 1, 'an acknowledged card cannot submit a second reply');
  assert.equal(f.model.confirmationRevision, confirmationRevision, 'a locked reply does not prompt for a confirmation it cannot use');
});

test('dismissing native confirmation rejects the action without sending or showing an error', async () => {
  const f = setup({ view: viewWith({ id: 'choose', reply: 'Use this option', confirm: 'Confirm this option' }) });
  const completion = observe(f.model.performAction('choose', 'choose'));
  const token = f.model.selectionSnapshot.token;
  assert.ok(token);
  f.model.declineConfirmation(token);
  const outcome = await completion;
  assert.equal(outcome.ok, false);
  assert.equal(outcome.error.message, 'cancelled');
  assert.equal(f.state.calls.length, 0);
  assert.equal(f.model.error, '');
  assert.deepEqual(f.model.selectionSnapshot, {});
});

test('a remote answer settles and dismisses an open assistant confirmation', async () => {
  const f = setup({ view: viewWith({ id: 'choose', reply: 'Use this option', confirm: 'Confirm this option' }) });
  const completion = observe(f.model.performAction('choose', 'choose'));
  const token = f.model.selectionSnapshot.token;
  f.feed.viewActions[f.key] = actionRecord(f.key, 'choose', 'remote-message', {}, 1, 'pending', 1);
  AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, true);
  const outcome = await completion;
  assert.equal(outcome.ok, false);
  assert.equal(outcome.error.message, 'already_answered');
  assert.equal(f.model.selectionSnapshot.token, undefined);
  assert.equal(f.model.confirmationLease(token), undefined);
  assert.equal(f.model.hasActionRecord, true);
  assert.equal(f.state.calls.length, 0);
});

test('hiding a card cancels an in-flight completion and preserves the exact retry', async () => {
  let finishRequest;
  const pending = new Promise((resolve) => { finishRequest = resolve; });
  const f = setup({
    handler: async () => pending,
    retryHandler: async (request) => {
      const body = JSON.parse(request.body);
      return { action: actionRecord(f.key, body.action, body.client_msg_id, body.values, 1, 'pending', body.expected_version) };
    }
  });
  const send = f.model.performAction('choose', 'choose');
  const completion = observe(send);
  assert.equal(f.state.calls.length, 1);
  const originalBody = JSON.stringify(f.state.calls[0].body);
  const originalOperation = f.state.calls[0].op;
  f.model.hide();
  finishRequest({ action: actionRecord(f.key, 'choose', originalOperation, {}, 1, 'pending', 1) });
  const outcome = await completion;
  assert.equal(outcome.ok, false);
  assert.equal(outcome.error.message, 'cancelled');
  assert.equal(f.model.hasActionRecord, false, 'a completion from the hidden interaction is ignored');
  assert.equal(f.model.canRetryOriginal, true);
  const restored = AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, true);
  assert.equal(restored, f.model);
  await restored.retryOriginal();
  assert.equal(f.state.retries.length, 1);
  assert.equal(f.state.retries[0].body, originalBody);
  assert.equal(f.state.retries[0].op, originalOperation);
  assert.equal(f.model.hasActionRecord, true);
});

test('profile forget clears inactive cached assistant state before it can be reacquired', async () => {
  const f = setup({ handler: async () => { throw failure('transport_timeout'); } });
  const failed = await observe(f.model.performAction('choose', 'choose'));
  assert.equal(failed.ok, false);
  assert.equal(f.model.canRetryOriginal, true);
  f.model.hide();
  const inactive = AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, false);
  assert.equal(inactive, f.model);
  assert.equal(inactive.active, false);

  f.item.text = '';
  f.feed.views = {};
  f.feed.viewActions = {};
  f.feed.viewBlocks = [];
  AssistantViewModel.forgetProfile(f.profileID);

  assert.equal(f.model.fallbackText, '');
  assert.deepEqual(f.model.view, {});
  assert.equal(f.model.canRetryOriginal, false);
  assert.deepEqual(f.model.selectionSnapshot, {});
  assert.equal(f.model.hasActionRecord, false);
  const reacquired = AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, false);
  assert.notEqual(reacquired, f.model);
  assert.equal(reacquired.fallbackText, '');
  assert.equal(reacquired.canInteract(), false);
});

test('a live view update invalidates an open confirmation without sending it', async () => {
  const key = 'view_live-' + (++fixtureID);
  const f = setup({ item: { id: 'live-item-' + fixtureID, view: {} }, view: viewWith({
    id: 'choose', reply: 'Use this option', confirm: 'Confirm this choice'
  }) });
  f.feed.views[key] = { id: key, conversation: f.conversation, version: 1, view: f.card };
  const model = AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  const completion = observe(model.performAction('choose', 'choose'));
  const revision = model.confirmationRevision;
  f.feed.views[key] = { id: key, conversation: f.conversation, version: 2,
    view: viewWith({ id: 'choose', reply: 'Use the updated option', confirm: 'Confirm this updated choice' }) };
  AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  assert.equal(model.viewVersion, 2);
  assert.equal(model.selectionSnapshot.token, undefined);
  assert.ok(model.confirmationRevision > revision);
  assert.equal(f.state.calls.length, 0);
  const outcome = await completion;
  assert.equal(outcome.ok, false);
  assert.equal(outcome.error.message, 'assistant_view_changed');
});

test('an old dialog token cannot consume a replacement after hide and live view update', async () => {
  const key = 'view_live-' + (++fixtureID);
  const f = setup({ item: { id: 'live-item-' + fixtureID, view: {} }, view: viewWith({
    id: 'choose', reply: 'Original choice', confirm: 'Confirm original'
  }) });
  f.feed.views[key] = { id: key, conversation: f.conversation, version: 1, view: f.card };
  const model = AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  const firstCompletion = observe(model.performAction('choose', 'choose'));
  const firstToken = model.selectionSnapshot.token;
  const firstLease = model.confirmationLease(firstToken);

  model.hide();
  f.feed.views[key] = { id: key, conversation: f.conversation, version: 2,
    view: viewWith({ id: 'choose', reply: 'Updated choice', confirm: 'Confirm updated' }) };
  AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  const secondCompletion = observe(model.performAction('choose', 'choose'));
  const secondToken = model.selectionSnapshot.token;
  assert.notEqual(secondToken, firstToken);
  assert.equal(model.confirmationLeaseIsCurrent(firstLease), false);
  assert.equal(model.takeConfirmation(firstToken), undefined);
  model.declineConfirmation(firstToken);
  assert.equal(model.selectionSnapshot.token, secondToken, 'a stale callback leaves the replacement confirmation intact');
  assert.equal(f.state.calls.length, 0);

  const confirmation = takeConfirmation(model);
  await model.confirmSelection(confirmation.selection, confirmation.lease);
  assert.equal((await firstCompletion).error.message, 'cancelled');
  assert.equal((await secondCompletion).ok, true);
  assert.equal(f.state.calls.length, 1);
  assert.equal(f.state.calls[0].body.expected_version, 2);
});

test('a confirmation token is invalid after reconnect to the same conversation', async () => {
  const key = 'view_live-' + (++fixtureID);
  const f = setup({ item: { id: 'reconnect-item-' + fixtureID, view: {} }, view: viewWith({
    id: 'choose', reply: 'Use this option', confirm: 'Confirm this option'
  }) });
  f.feed.views[key] = { id: key, conversation: f.conversation, version: 1, view: f.card };
  const model = AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  const completion = observe(model.performAction('choose', 'choose'));
  const token = model.selectionSnapshot.token;
  const lease = model.confirmationLease(token);
  f.state.generation++;
  AssistantViewModel.acquire(f.client, f.feed, f.item, key, true);
  assert.equal(model.takeConfirmation(token), undefined);
  assert.equal(model.confirmationLeaseIsCurrent(lease), false);
  assert.equal(f.state.calls.length, 0);
  const outcome = await completion;
  assert.equal(outcome.ok, false);
  assert.equal(outcome.error.message, 'assistant_view_changed');
});

test('submit collects current-page string values and validates typed fields before transport', async () => {
  const form = {
    title: 'Values', fallback: 'Submit the values.', sections: [{ items: [
      { type: 'field', key: 'count', name: 'count', kind: 'number', label: 'Count', value: '1' },
      { type: 'field', key: 'day', name: 'day', kind: 'date', label: 'Day', value: '2026-11-11' },
      { type: 'field', key: 'tags', name: 'tags', kind: 'multichoice', label: 'Tags', value: '[]', choices: [
        { value: 'red', title: 'Red' }, { value: 'blue', title: 'Blue' }
      ] },
      { type: 'button', key: 'choose', title: 'Submit', action: { id: 'choose', reply: 'Submit these fields', submit: true,
        confirm: 'Confirm these exact values' } }
    ] }]
  };
  const f = setup({ view: form, handler: async ({ body }) => ({ action: actionRecord(f.key, body.action, body.client_msg_id, body.values) }) });
  f.model.setFieldValue('count', '2.5');
  f.model.setFieldValue('day', '2026-11-12');
  f.model.toggleValue('tags', 'red');
  const completion = observe(f.model.performAction('choose', 'choose'));
  assert.equal(f.state.calls.length, 0);
  assert.match(f.model.selectionSnapshot.message, /Count: 2\.5/);
  f.model.setFieldValue('count', '9');
  assert.equal(f.model.fieldValue('count'), '2.5', 'the confirmed draft is immutable while confirmation is open');
  const confirmation = takeConfirmation(f.model);
  await f.model.confirmSelection(confirmation.selection, confirmation.lease);
  assert.equal((await completion).ok, true);
  assert.equal(f.state.calls.length, 1);
  assert.equal(f.state.calls[0].body.confirmed, true);
  assert.deepEqual(f.state.calls[0].body.values, { count: '2.5', day: '2026-11-12', tags: '["red"]' });

  const invalid = setup({ view: form });
  invalid.model.setFieldValue('count', '0x10');
  await assert.rejects(invalid.model.performAction('choose', 'choose'), /assistant_view_form_invalid/);
  assert.equal(invalid.state.calls.length, 0);
  assert.equal(invalid.model.error, 'assistant_view_form_invalid');
});

test('page form actions submit only the current page and preserve its identity', async () => {
  const detail = {
    title: 'Details', sections: [{ items: [
      { type: 'field', key: 'note', name: 'note', kind: 'multiline', label: 'Note', value: '' },
      { type: 'button', key: 'submit-detail', title: 'Send', action: { id: 'submit-detail', reply: 'Send the detail note', submit: true } }
    ] }]
  };
  const card = viewWith({ id: 'open-details', push: { page: 'detail' } }, { pages: { detail: detail } });
  const f = setup({ view: card, handler: async ({ body }) => ({
    action: actionRecord(f.key, body.action, body.client_msg_id, body.values, 1, 'pending', body.expected_version)
  }) });
  await f.model.performAction('choose', 'open-details');
  assert.equal(f.model.currentPage, 'detail');
  f.model.setFieldValue('note', 'Only this page');
  f.model.hide();
  const restored = AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, true);
  assert.equal(restored, f.model);
  assert.equal(restored.currentPage, 'detail', 'the page path survives a lazy row teardown');
  assert.equal(restored.fieldValue('note'), 'Only this page', 'the page draft survives a lazy row teardown');
  await f.model.performAction('submit-detail', 'submit-detail');
  assert.equal(f.state.calls.length, 1);
  assert.equal(f.state.calls[0].body.page, 'detail');
  assert.deepEqual(f.state.calls[0].body.values, { note: 'Only this page' });
});

test('view changes preserve the old selection draft and require a fresh native confirmation', async () => {
  const f = setup({
    handler: async () => { throw failure('view_changed', 409, false); },
    refresh: async (feed) => {
      feed.views[f.key] = { id: f.key, conversation: f.conversation, version: 2,
        view: viewWith({ id: 'choose', reply: 'Use the updated choice' }) };
    }
  });
  const first = await observe(f.model.performAction('choose', 'choose'));
  assert.equal(first.ok, false);
  assert.equal(first.error.message, 'view_changed');
  assert.equal(f.state.calls.length, 1);
  assert.equal(f.model.viewVersion, 2);
  assert.equal(f.model.changedNeedsConfirmation, true);
  const second = observe(f.model.performAction('choose', 'choose'));
  assert.equal(f.state.calls.length, 1, 'the refreshed action waits for another user confirmation');
  const selection = f.model.selectionSnapshot;
  assert.equal(selection.version, 2);
  f.model.declineConfirmation(selection.token);
  const secondOutcome = await second;
  assert.equal(secondOutcome.ok, false);
  assert.equal(secondOutcome.error.message, 'cancelled');
  const third = observe(f.model.performAction('choose', 'choose'));
  assert.equal(f.state.calls.length, 1, 'declining never sends or clears the changed-version guard');
  f.model.declineConfirmation(f.model.selectionSnapshot.token);
  const thirdOutcome = await third;
  assert.equal(thirdOutcome.ok, false);
  assert.equal(thirdOutcome.error.message, 'cancelled');
});

test('page navigation stays local and open targets are allowlisted', async () => {
  const f = setup({ view: viewWith({ id: 'details', push: { page: 'detail' } }, {
    pages: { detail: { title: 'Details', sections: [{ items: [{ type: 'text', text: 'Private detail' }] }] } }
  }) });
  await f.model.performAction('choose', 'details');
  assert.equal(f.model.currentPage, 'detail');
  assert.equal(f.model.pageTitle, 'Details');
  f.model.back();
  assert.equal(f.model.currentPage, '');
  assert.equal(f.state.calls.length, 0);
  assert.deepEqual(assistantViewOpenTarget('https://example.test/path'), { kind: 'link', address: 'https://example.test/path' });
  assert.equal(assistantViewOpenTarget('http://example.test'), undefined);
  assert.equal(assistantViewOpenTarget('https://user:pass@example.test'), undefined);
  assert.equal(assistantViewOpenTarget('persona://session/machine/session')?.kind, 'resource');
  assert.equal(assistantViewOpenTarget('persona://session/machine/session-id')?.kind, 'resource');
  assert.equal(assistantViewOpenTarget('persona://session/machine%2Fother/session'), undefined);
  assert.equal(assistantViewOpenTarget('persona://session/machine/%252Fother'), undefined);
  assert.equal(assistantViewOpenTarget('persona://kb/docs/a/%2e%2e/b'), undefined);
  assert.equal(assistantViewOpenTarget('persona://kb/docs/%252e%252e/secret'), undefined);
  assert.equal(assistantViewOpenTarget('persona://file/machine/%2Fhome%2Fuser%2Fnote')?.kind, 'resource');
  assert.equal(assistantViewOpenTarget('persona://file/machine/%252e%252e%252fsecret'), undefined);
});

test('retry journal settlement does not publish an old generation over newer client writes', async () => {
  for (const retryFails of [false, true]) {
    const f = setup({ handler: async () => { throw failure('transport_timeout'); } });
    const original = await observe(f.model.performAction('choose', 'choose'));
    assert.equal(original.ok, false);
    let finishState = '';
    let signalStarted;
    let releaseFinish;
    const started = new Promise((resolve) => { signalStarted = resolve; });
    const finishGate = new Promise((resolve) => { releaseFinish = resolve; });
    const finish = f.repository.mutations.finish;
    f.repository.mutations.finish = async (record, next) => {
      finishState = next;
      signalStarted();
      await finishGate;
      await finish(record, next);
    };
    f.state.retryHandler = async (request) => {
      if (retryFails) throw failure('bad_request', 400, false);
      const body = JSON.parse(request.body);
      return { action: actionRecord(f.key, body.action, body.client_msg_id, body.values, 1, 'pending', body.expected_version) };
    };
    const retry = observe(f.model.retryOriginal());
    await started;
    const newHubWrites = [{ id: 'new-hub-operation' }];
    f.client.unresolvedWrites = newHubWrites;
    f.state.generation++;
    AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, true);
    releaseFinish();
    const outcome = await retry;
    assert.equal(outcome.ok, false);
    assert.equal(f.state.records[0].state, retryFails ? 'failed' : 'done');
    assert.equal(f.client.unresolvedWrites, newHubWrites);
    assert.equal(finishState, retryFails ? 'failed' : 'done');
  }
});

test('a cache-full view update preserves a draft and the exact uncertain retry', async () => {
  const form = {
    title: 'Keep state', fallback: 'Submit the saved note.', sections: [{ items: [
      { type: 'field', key: 'note', name: 'note', kind: 'text', label: 'Note', value: '' },
      { type: 'button', key: 'choose', title: 'Send', action: { id: 'choose', reply: 'Submit the note', submit: true } }
    ] }]
  };
  const f = setup({ item: { id: 'cache-target-' + (++fixtureID), view: {} }, view: form,
    handler: async () => { throw failure('transport_timeout'); } });
  f.feed.views[f.key] = { id: f.key, conversation: f.conversation, version: 1, view: form };
  const model = AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, true);
  model.setFieldValue('note', 'Keep this draft');
  const failed = await observe(model.performAction('choose', 'choose'));
  assert.equal(failed.ok, false);
  assert.equal(model.canRetryOriginal, true);
  const originalBody = JSON.stringify(f.state.calls[0].body);

  const blocker = setup();
  blocker.model.drafted = true;
  blocker.model.cardSize = 5 * 1024 * 1024;
  const oversized = {
    title: 'New version', fallback: 'This source exceeds the remaining cache budget.', sections: [{ items: [
      { type: 'text', text: 'x'.repeat(60000) },
      { type: 'field', key: 'note', name: 'note', kind: 'text', label: 'Note', value: '' },
      { type: 'button', key: 'choose', title: 'Send', action: { id: 'choose', reply: 'Submit the note', submit: true } }
    ] }]
  };
  f.feed.views[f.key] = { id: f.key, conversation: f.conversation, version: 2, view: oversized };
  const rebound = AssistantViewModel.acquire(f.client, f.feed, f.item, f.key, true);
  assert.equal(rebound, model);
  assert.equal(model.viewVersion, 1);
  assert.equal(model.fieldValue('note'), 'Keep this draft');
  assert.equal(model.canRetryOriginal, true);

  f.state.retryHandler = async (request) => {
    const body = JSON.parse(request.body);
    return { action: actionRecord(f.key, body.action, body.client_msg_id, body.values, 1, 'pending', body.expected_version) };
  };
  await model.retryOriginal();
  assert.equal(f.state.retries[0].body, originalBody);
  assert.equal(model.hasActionRecord, true);
});
