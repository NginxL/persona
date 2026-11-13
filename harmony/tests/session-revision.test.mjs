import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === 'persona-revision:transport') return { url: specifier, shortCircuit: true };
    if (context.parentURL?.endsWith('/SessionRevision.ets')) {
      if (specifier === './ClientModel') return { url: 'persona-revision:client', shortCircuit: true };
      if (specifier === './HubRepository') return { url: 'persona-revision:repository', shortCircuit: true };
      if (specifier === '../platform/HubTransport') return { url: 'persona-revision:transport', shortCircuit: true };
      if (specifier === '@kit.ArkTS') return { url: 'persona-revision:arkts', shortCircuit: true };
      if (specifier === '../core/SessionRevision')
        return { url: new URL('../core/SessionRevision.ts', context.parentURL).href, shortCircuit: true };
    }
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-revision:client') return { format: 'module', source: 'export class ClientModel {}', shortCircuit: true };
    if (url === 'persona-revision:repository') return {
      format: 'module', source: 'export class HubRepository {}\nexport const segment = encodeURIComponent;', shortCircuit: true
    };
    if (url === 'persona-revision:transport') return {
      format: 'module', source: `export class HttpFailure extends Error {
        constructor(status, code, resultUnknown = false, resultPending = false) {
          super(code); this.status = status; this.code = code; this.resultUnknown = resultUnknown; this.resultPending = resultPending;
        }
      }`, shortCircuit: true
    };
    if (url === 'persona-revision:arkts') return {
      format: 'module', source: `let operation = 0; export const util = { generateRandomUUID() { operation++; return 'op-' + operation; } };`, shortCircuit: true
    };
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    if (url.endsWith('.ts')) {
      return { format: 'module', source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8')), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { SessionRevision } = await import('../entry/src/main/ets/models/SessionRevision.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');
const { sessionRevisionPrompts, sessionRevisionTarget } = await import('../entry/src/main/ets/core/SessionRevision.ts');
const { HttpFailure } = await import('persona-revision:transport');

function row(fields = {}) {
  return ResourceRow.from({ id: 's1', session_id: 's1', machine_id: 'm1', manager: 'worker', title: 'Agent', cwd: '/repo',
    state: 'idle', status: 'idle', agent: 'claude', ...fields }, 'session');
}

function item(id, seq, role, kind, text = '', fields = {}) {
  return { id, seq, role, kind, text, ...fields };
}

function fixture(options = {}) {
  const state = {
    scope: 'hub:worker', generation: 4, attempts: [], journal: [], dispatched: [], blocked: new Set(),
    beforeAdmission: undefined, handle: options.handle, full: options.full || { text: '' }, result: options.result,
    connected: true, selected: options.selected || row(options.session), draft: options.draft || '',
    draftRevision: options.draftRevision || 'draft-1', decision: options.decision
  };
  const repository = {
    async call(path, method = 'GET', body = {}, op = '', admit) {
      const call = { path, method, body: structuredClone(body), op };
      state.attempts.push(call);
      state.journal.push({ path, method, state: 'pending' });
      if (state.beforeAdmission && (!state.beforeAdmission.path || path.endsWith(state.beforeAdmission.path))) {
        const before = state.beforeAdmission.callback;
        state.beforeAdmission = undefined;
        await before(call);
      }
      if (admit && !admit()) {
        state.journal[state.journal.length - 1].state = 'failed';
        throw new HttpFailure(0, 'request_updated');
      }
      state.dispatched.push(call);
      try {
        if (state.handle) {
          const result = await state.handle(call, state);
          if (result !== undefined) return structuredClone(result);
        }
        if (path.endsWith('/full')) return structuredClone(state.full);
        if (path.endsWith('/fork')) return { session_id: 'fork-2' };
        if (path.endsWith('/rewind')) return structuredClone(state.result || {
          prompt: body.prompt, restore: body.restore, input_cleared: false, code_restored: false
        });
        return {};
      } catch (error) {
        if (error.resultUnknown || error.resultPending) state.blocked.add(path);
        throw error;
      }
    }
  };
  const model = {
    selected: state.selected,
    profile: { manager: 'worker' },
    connected: true,
    demo: false,
    draft: state.draft,
    draftRevision: state.draftRevision,
    decision: state.decision,
    scope: () => state.scope,
    target(targetRow = model.selected) { return JSON.stringify([state.scope, targetRow.kind, targetRow.machine, targetRow.id]); },
    connectionGeneration: () => state.generation,
    owns(generation, scope) { return generation === state.generation && scope === state.scope; },
    repo: () => repository,
    operationBlocked(path) { return state.blocked.has(path); }
  };
  const revision = new SessionRevision();
  const items = options.items || [item('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa#0', 1, 'user', 'text', 'hello')];
  revision.open(model, state.selected, items);
  return { state, model, repository, revision, items };
}

const firstPromptId = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa#0';
const secondPromptId = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb#0';
const queuedPromptId = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc#0';
const toolImageId = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd#0';

test('raw prompts group split transcript lines, ignore queued/tool images, retain tags, and count matching prompts from the end', () => {
  const items = [
    item(firstPromptId, 1, 'user', 'text', '<command-name>help</command-name>', { full_size: 70 }),
    item('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa#1', 1, 'user', 'text', 'show status'),
    item('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa#2', 1, 'user', 'image'),
    item(queuedPromptId, 2, 'user', 'text', 'later but queued', { queued: true }),
    item(toolImageId, 2, 'user', 'image', '', { tool_use_id: 'tool-1' }),
    item(secondPromptId, 3, 'user', 'text', '<command-name>help</command-name> show\nstatus')
  ];
  const prompts = sessionRevisionPrompts(items);
  assert.equal(prompts.length, 2);
  assert.equal(prompts[0].raw, '<command-name>help</command-name>\nshow status');
  assert.equal(prompts[0].cut, true);
  assert.equal(prompts[1].raw, '<command-name>help</command-name> show\nstatus');
  const target = sessionRevisionTarget(items, firstPromptId, '<command-name>help</command-name>\nshow status');
  assert.equal(target.prompt, '<command-name>help</command-name>\nshow status');
  assert.equal(target.occurrence, 2);
  assert.equal(target.promptsAfter, 1);
  assert.equal(target.cutItemId, firstPromptId);
});

test('fork uses only can_fork metadata, the registered route, and the returned offline session identity', async () => {
  const replyId = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee#0';
  const { state, revision } = fixture({
    session: { can_fork: true, agent: 'codex', source_kind: 'another_source', state: 'working', starting: true },
    items: [item(replyId, 1, 'assistant', 'text', 'answer')]
  });
  const fingerprint = revision.fingerprint(replyId);
  assert.equal(revision.canFork(replyId), true);
  const branch = await revision.fork(replyId, fingerprint);
  assert.deepEqual(state.dispatched[0], {
    path: '/api/v1/sessions/m1/s1/fork', method: 'POST', body: { at_item_id: replyId }, op: 'op-1'
  });
  assert.equal(branch.id, 'fork-2');
  assert.equal(branch.session, 'fork-2');
  assert.equal(branch.manager, 'worker');
  assert.equal(branch.machine, 'm1');
  assert.equal(branch.kind, 'session');
  assert.equal(branch.state, 'offline');

  const olderHub = fixture({ items: [item(replyId, 1, 'assistant', 'text', 'answer')] });
  assert.equal(olderHub.revision.canFork(replyId), false);
  assert.equal(await olderHub.revision.fork(replyId, olderHub.revision.fingerprint(replyId)), undefined);
  assert.equal(olderHub.state.attempts.length, 0);
});

test('a truncated rewind hydrates the full raw item, sends exact protocol fields, and does not read after ACK', async () => {
  const targetId = 'ffffffff-ffff-4fff-8fff-ffffffffffff#0';
  const repeatedId = '11111111-1111-4111-8111-111111111111#0';
  const text = '<command-name>help</command-name>\nshow status';
  let liveRevision;
  const setup = fixture({
    items: [
      item(targetId, 1, 'user', 'text', '<command-name>help</command-name> …', { full_size: 120 }),
      item(repeatedId, 2, 'user', 'text', '<command-name>help</command-name> show\nstatus')
    ],
    full: { id: targetId, role: 'user', kind: 'text', text },
    result: { prompt: text, restore: 'code_and_conversation', input_cleared: true, code_restored: false },
    handle(call) {
      if (call.path.endsWith('/full')) {
        liveRevision.updateItems([
          item(targetId, 1, 'user', 'text', text),
          item(repeatedId, 2, 'user', 'text', '<command-name>help</command-name> show\nstatus')
        ]);
        return { id: targetId, role: 'user', kind: 'text', text };
      }
      return undefined;
    }
  });
  const { state, revision } = setup;
  liveRevision = revision;
  const fingerprint = revision.fingerprint(targetId);
  const outcome = await revision.rewind(targetId, 'code_and_conversation', fingerprint);
  assert.equal(state.dispatched.length, 2);
  assert.equal(state.dispatched[0].method, 'GET');
  assert.equal(state.dispatched[0].path, '/api/v1/sessions/m1/s1/items/' + encodeURIComponent(targetId) + '/full');
  assert.deepEqual(state.dispatched[1].body, {
    prompt: text, occurrence: 2, prompts_after: 1, restore: 'code_and_conversation'
  });
  assert.equal(state.dispatched[1].path, '/api/v1/sessions/m1/s1/rewind');
  assert.equal(outcome.state, 'done');
  assert.equal(outcome.prompt, text);
  assert.equal(outcome.inputCleared, true);
  assert.equal(outcome.codeRestored, false);
  assert.equal(outcome.canFillDraft, true);
  assert.equal(state.dispatched.some((call) => call.method === 'GET' && call.path !== state.dispatched[0].path), false);
});

test('rewind refuses sourced, assistant, pane, non-Claude, starting, offline, working, needs-you, and open-request sessions', () => {
  const userId = '22222222-2222-4222-8222-222222222222#0';
  const items = [item(userId, 1, 'user', 'text', 'hello')];
  const cases = [
    { source_kind: 'codex' }, { assistant: true }, { session_id: 's1@pane' }, { agent: 'codex' },
    { starting: true }, { state: 'offline' }, { state: 'working' }, { state: 'needs_you' }, { pending_interactions: 1 }
  ];
  for (const session of cases) {
    const { revision } = fixture({ session, items });
    assert.equal(revision.canRewind(userId), false);
  }
  const openRequest = fixture({ items, decision: { machine: 'm1', session: 's1', status: 'open' } });
  assert.equal(openRequest.revision.rewindBlock(userId), 'session_revision_open_request');
});

test('queued and tool-use image rows cannot rewind, while a normal image-only message reports no text locally', async () => {
  const queued = fixture({ items: [item(queuedPromptId, 1, 'user', 'text', 'queued', { queued: true })] });
  assert.equal(queued.revision.canRewind(queuedPromptId), false);
  const toolImage = fixture({ items: [item(toolImageId, 1, 'user', 'image', '', { tool_use_id: 'tool-2' })] });
  assert.equal(toolImage.revision.canRewind(toolImageId), false);
  const imageId = '33333333-3333-4333-8333-333333333333#0';
  const imageOnly = fixture({ items: [item(imageId, 1, 'user', 'image')] });
  assert.equal(imageOnly.revision.canRewind(imageId), true);
  const outcome = await imageOnly.revision.rewind(imageId, 'conversation', imageOnly.revision.fingerprint(imageId));
  assert.equal(outcome.error, 'session_revision_no_text');
  assert.equal(imageOnly.state.attempts.length, 0);
});

test('owner and target changes after durable journal begin prevent dispatch', async () => {
  const targetId = '44444444-4444-4444-8444-444444444444#0';
  const items = [item(targetId, 1, 'user', 'text', 'hello')];
  const changedOwner = fixture({ items });
  changedOwner.state.beforeAdmission = { path: '/rewind', callback: async () => { changedOwner.state.scope = 'hub:other'; } };
  const ownerOutcome = await changedOwner.revision.rewind(targetId, 'conversation', changedOwner.revision.fingerprint(targetId));
  assert.equal(ownerOutcome.error, 'request_updated');
  assert.equal(changedOwner.state.journal.length, 1);
  assert.equal(changedOwner.state.dispatched.length, 0);

  const changedTarget = fixture({ items });
  changedTarget.state.beforeAdmission = { path: '/rewind', callback: async () => {
    changedTarget.revision.updateItems([item(targetId, 1, 'user', 'text', 'different text')]);
  } };
  const targetOutcome = await changedTarget.revision.rewind(targetId, 'conversation', changedTarget.revision.fingerprint(targetId));
  assert.equal(targetOutcome.error, 'request_updated');
  assert.equal(changedTarget.state.journal.length, 1);
  assert.equal(changedTarget.state.dispatched.length, 0);

  const hidden = fixture({ items });
  hidden.state.beforeAdmission = { path: '/rewind', callback: async () => { hidden.revision.hide(); } };
  const hiddenOutcome = await hidden.revision.rewind(targetId, 'conversation', hidden.revision.fingerprint(targetId));
  assert.equal(hiddenOutcome.error, 'request_updated');
  assert.equal(hidden.state.dispatched.length, 0);
});

test('pending and unknown writes remain blocked without a retry', async () => {
  const replyId = '55555555-5555-4555-8555-555555555555#0';
  const pending = fixture({
    session: { can_fork: true }, items: [item(replyId, 1, 'assistant', 'text', 'answer')],
    handle() { throw new HttpFailure(202, 'result_unknown', true, true); }
  });
  const pendingFingerprint = pending.revision.fingerprint(replyId);
  assert.equal(await pending.revision.fork(replyId, pendingFingerprint), undefined);
  assert.equal(pending.revision.forkState, 'pending');
  assert.equal(pending.revision.canFork(replyId), false);
  await pending.revision.fork(replyId, pendingFingerprint);
  assert.equal(pending.state.attempts.length, 1);

  const unknown = fixture({
    session: { can_fork: true }, items: [item(replyId, 1, 'assistant', 'text', 'answer')],
    handle() { throw new HttpFailure(0, 'result_unknown', true, false); }
  });
  const unknownFingerprint = unknown.revision.fingerprint(replyId);
  assert.equal(await unknown.revision.fork(replyId, unknownFingerprint), undefined);
  assert.equal(unknown.revision.forkState, 'unknown');
  await unknown.revision.fork(replyId, unknownFingerprint);
  assert.equal(unknown.state.attempts.length, 1);
});

test('rewind reports draft conflicts instead of overwriting a changed or nonempty composer', async () => {
  const targetId = '66666666-6666-4666-8666-666666666666#0';
  const existing = fixture({
    items: [item(targetId, 1, 'user', 'text', 'rewind me')], draft: 'keep this draft', draftRevision: 'revision-7',
    result: { prompt: 'rewind me', restore: 'conversation', input_cleared: true, code_restored: false }
  });
  const existingOutcome = await existing.revision.rewind(targetId, 'conversation', existing.revision.fingerprint(targetId));
  assert.equal(existingOutcome.draftUnchanged, true);
  assert.equal(existingOutcome.canFillDraft, false);
  assert.equal(existingOutcome.capturedDraft, 'keep this draft');
  assert.equal(existingOutcome.capturedDraftRevision, 'revision-7');

  const changed = fixture({
    items: [item(targetId, 1, 'user', 'text', 'rewind me')],
    result: { prompt: 'rewind me', restore: 'conversation', input_cleared: true, code_restored: false },
    handle(call, state) {
      if (call.path.endsWith('/rewind')) {
        state.model.draft = 'typed after confirmation';
        state.model.draftRevision = 'revision-8';
        return { prompt: 'rewind me', restore: 'conversation', input_cleared: true, code_restored: false };
      }
      return undefined;
    }
  });
  changed.state.model = changed.model;
  const changedOutcome = await changed.revision.rewind(targetId, 'conversation', changed.revision.fingerprint(targetId));
  assert.equal(changedOutcome.draftUnchanged, false);
  assert.equal(changedOutcome.canFillDraft, false);
});

test('a transcript snapshot is immutable until updateItems supplies a new raw target', () => {
  const targetId = '77777777-7777-4777-8777-777777777777#0';
  const original = item(targetId, 1, 'user', 'text', 'canonical raw text');
  const { revision } = fixture({ items: [original] });
  const fingerprint = revision.fingerprint(targetId);
  original.text = 'mutated outside the model';
  assert.equal(revision.fingerprint(targetId), fingerprint);
  revision.updateItems([item(targetId, 1, 'user', 'text', 'updated raw text')]);
  assert.notEqual(revision.fingerprint(targetId), fingerprint);
});
