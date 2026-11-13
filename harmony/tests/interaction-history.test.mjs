import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { InteractionHistoryModel } = await import('../entry/src/main/ets/models/InteractionHistory.ets');
const { interactionCanSubmit } = await import('../entry/src/main/ets/models/InteractionDecision.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');

function session(id = 's1', manager = 'work', machine = 'm1') {
  const row = new ResourceRow(id, 'Session ' + id, 'session');
  row.manager = manager; row.machine = machine; row.session = id;
  return row;
}

function fixture(handler, options = {}) {
  const state = {
    generation: 3, scope: 'hub:work', manager: 'work', selected: options.selected || session(),
    connected: options.connected !== false, demo: false, calls: [], repository: undefined
  };
  const repository = {
    async call(path, method = 'GET', body = {}) {
      state.calls.push({ path, method, body, repository: this });
      return handler(path, state);
    }
  };
  state.repository = repository;
  const client = {
    profile: { manager: state.manager },
    get selected() { return state.selected; },
    get connected() { return state.connected; },
    get demo() { return state.demo; },
    connectionGeneration() { return state.generation; },
    scope() { return state.scope; },
    target(row = state.selected) { return JSON.stringify([this.scope(), row.kind, row.machine, row.id]); },
    owns(generation, scope) { return generation === state.generation && scope === state.scope; },
    repo() { return state.repository; }
  };
  return { state, repository, client, history: new InteractionHistoryModel() };
}

function historyRow(id, kind = 'permission', resolution = { allow: true }) {
  return {
    interaction: { id, machine_id: 'm1', session_id: 's1', manager: 'work', kind,
      summary: 'Review the request', fields: [{ key: 'token', label: 'Token', secret: true }] },
    status: 'resolved', resolution, resolved_at: '2026-11-11T10:00:00Z'
  };
}

test('history uses the bounded, manager-qualified session route and redacts stored answers', async () => {
  const secretPermission = historyRow('permission-1', 'permission', { allow: true, values: { token: 'permission-secret' } });
  const secretForm = { interaction: { id: 'form-1', machine_id: 'm1', session_id: 's1', manager: 'work', kind: 'elicitation',
    elicitation: { mode: 'form', message: 'Fill in the form', requested_schema: { type: 'object', required: ['password'], properties: {
      password: { type: 'string', format: 'password' }
    } } } }, status: 'resolved', resolution: { action: 'accept', content: { password: 'form-secret' } } };
  const question = { interaction: { id: 'question-1', machine_id: 'm1', session_id: 's1', manager: 'work', kind: 'question',
    questions: [{ question: 'Which library?', options: [{ label: 'pgx' }] }] }, status: 'resolved', resolution: { answers: { 'Which library?': 'pgx' } } };
  const { state, client, repository, history } = fixture(async () => ({ interactions: [secretPermission, secretForm, question] }));

  await history.open(client);

  assert.equal(state.calls.length, 1);
  assert.equal(state.calls[0].path, '/api/v1/interactions/history?machine_id=m1&session_id=s1&limit=100&manager=work');
  assert.equal(state.calls[0].method, 'GET');
  assert.deepEqual(history.rows.map((entry) => entry.request.id), ['permission-1', 'form-1', 'question-1']);
  assert.equal(history.rows[0].request.status, 'resolved');
  assert.equal(interactionCanSubmit(history.rows[0].request), false);
  assert.equal(JSON.stringify(history.rows[0].request.raw).includes('permission-secret'), false);
  assert.equal(JSON.stringify(history.rows[0].outcome).includes('permission-secret'), false);
  assert.equal(JSON.stringify(history.rows[1].request.raw).includes('form-secret'), false);
  assert.equal(JSON.stringify(history.rows[1].outcome).includes('form-secret'), false);
  assert.equal(history.rows[0].request.raw.includes('"allow":true'), true);
  assert.equal(history.rows[2].request.raw.includes('"answers":{"Which library?":"pgx"}'), true);
  assert.equal(state.calls.length, 1, 'opening history only reads; it never resolves or reanswers');
});

test('history bounds oversized server pages and drops rows outside the selected target', async () => {
  const rows = [{ ...historyRow('foreign-session'), interaction: { ...historyRow('foreign-session').interaction, session_id: 'other' } }];
  rows.push(...Array.from({ length: 101 }, (_, index) => historyRow('entry-' + index)));
  const { client, history } = fixture(async () => ({ interactions: rows }));

  await history.open(client);

  assert.equal(history.rows.length, 99, 'the response is capped at 100 before its foreign row is filtered');
  assert.equal(history.rows.some((entry) => entry.request.id === 'foreign-session'), false);
  assert.equal(history.rows.some((entry) => entry.request.id === 'entry-99'), false);
});

test('a late history reply cannot replace another selected session', async () => {
  let completeOld;
  let callCount = 0;
  const { state, client, history } = fixture((path) => {
    callCount++;
    if (callCount === 1) return new Promise((resolve) => { completeOld = resolve; });
    return Promise.resolve({ interactions: [{ ...historyRow('current-session'), interaction: {
      ...historyRow('current-session').interaction, session_id: 's2'
    } }] });
  });
  const old = history.open(client);
  state.selected = session('s2'); state.generation++;
  await history.open(client);
  completeOld({ interactions: [historyRow('stale-session')] });
  await old;

  assert.deepEqual(history.rows.map((entry) => entry.request.id), ['current-session']);
  assert.equal(history.loading, false);
});

test('a late history reply cannot cross a manager owner change', async () => {
  let completeOld;
  let callCount = 0;
  const { state, client, history } = fixture(() => {
    callCount++;
    if (callCount === 1) return new Promise((resolve) => { completeOld = resolve; });
    const next = historyRow('new-owner');
    next.interaction.manager = 'personal';
    return Promise.resolve({ interactions: [next] });
  });
  const old = history.open(client);
  state.manager = 'personal'; state.scope = 'hub:personal'; state.generation++;
  client.profile.manager = 'personal'; state.selected = session('s1', 'personal');
  await history.open(client);
  completeOld({ interactions: [historyRow('stale-owner')] });
  await old;

  assert.deepEqual(history.rows.map((entry) => entry.request.id), ['new-owner']);
  assert.equal(history.rows[0].request.raw.includes('stale-owner'), false);
});

test('pending history entries remain read-only and cannot be submitted', async () => {
  const pending = historyRow('pending-1'); pending.status = 'pending'; delete pending.resolution;
  const { client, history, state } = fixture(async () => ({ interactions: [pending] }));
  await history.open(client);

  assert.equal(history.rows[0].request.status, 'pending');
  assert.equal(interactionCanSubmit(history.rows[0].request, true), false);
  assert.deepEqual(state.calls.map((call) => call.method), ['GET']);
});

test('hidden destinations stop requests and ignore replies already in flight', async () => {
  let complete;
  const { client, history } = fixture(() => new Promise((resolve) => { complete = resolve; }));
  const loading = history.open(client);
  await history.open(client, false);
  complete({ interactions: [historyRow('hidden-reply')] });
  await loading;

  assert.deepEqual(history.rows, []);
  assert.equal(history.loading, false);
  assert.equal(history.selectedId, '');
});

test('request failures and malformed history envelopes become safe page errors', async () => {
  const failed = fixture(async () => { throw new Error('internal response detail'); });
  await failed.history.open(failed.client);
  assert.equal(failed.history.error, 'failed');
  assert.equal(failed.history.error.includes('internal'), false);
  assert.equal(failed.history.loading, false);

  const malformed = fixture(async () => ({ interactions: 'not-an-array' }));
  await malformed.history.open(malformed.client);
  assert.equal(malformed.history.error, 'failed');
  assert.deepEqual(malformed.history.rows, []);
});
