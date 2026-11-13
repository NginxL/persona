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

const {
  decodeInteractionDecision, interactionCanSubmit, interactionElicitationDefaults, interactionElicitationMessage,
  interactionElicitationMode, interactionElicitationSupported, interactionElicitationUrl, interactionHistoryResolution,
  interactionPermissionFieldPlaceholder, interactionQuestionOptions, interactionResolution, interactionScreenOptions,
  validateInteractionElicitation
} = await import('../entry/src/main/ets/models/InteractionDecision.ets');

function decision(interaction, extra = {}) { return decodeInteractionDecision({ interaction, ...extra }); }

test('question answers keep exact labels, custom text takes precedence, and cancel is gated', () => {
  const request = decision({ id: 'question-1', kind: 'question', can_cancel: true, questions: [
    { question: 'Which?', multi_select: true, options: [{ label: 'Alpha', description: 'First' }, { label: 'Beta' }] }
  ] });
  assert.equal(request.fields[0].kind, 'multi_choice');
  assert.equal(interactionQuestionOptions(request, 'Which?')[0].detail, 'First');
  assert.deepEqual(interactionResolution(request, 'answer', { 'Which?': 'Alpha, Beta' }), { answers: { 'Which?': 'Alpha, Beta' } });
  assert.deepEqual(interactionResolution(request, 'answer', { 'Which?': 'custom answer' }), { answers: { 'Which?': 'custom answer' } });
  assert.deepEqual(interactionResolution(request, 'cancel', {}), { cancel: true });
  request.raw = JSON.stringify({ interaction: { id: 'question-1', kind: 'question', questions: [{ question: 'Which?', options: [] }] } });
  assert.throws(() => interactionResolution(request, 'cancel', {}), /unsupported/);
});

test('plan and permission bodies match proto.Resolution and never send denied secret values', () => {
  const plan = decision({ id: 'plan-1', kind: 'plan', plan: 'Proceed?' });
  assert.deepEqual(interactionResolution(plan, 'approve', {}), { approve: true });
  assert.deepEqual(interactionResolution(plan, 'deny', { feedback: 'Use another path' }), { approve: false, feedback: 'Use another path' });

  const permission = decision({ id: 'permission-1', kind: 'permission', fields: [
    { key: 'token', label: 'Token', placeholder: 'Paste token', secret: true },
    { key: 'note', label: 'Note', optional: true }
  ] });
  assert.equal(interactionPermissionFieldPlaceholder(permission, 'token'), 'Paste token');
  assert.deepEqual(interactionResolution(permission, 'approve', { token: 'one-time', note: 'safe' }), {
    allow: true, values: { token: 'one-time', note: 'safe' }
  });
  assert.deepEqual(interactionResolution(permission, 'deny', { token: 'one-time' }), { allow: false });
  assert.throws(() => interactionResolution(permission, 'approve', {}), /required/);
});

test('screen form displays labels, resolves stable keys, handles multi-select, and rejects a changed revision', () => {
  const request = decision({ id: 'screen-1', kind: 'screen', dialog: {
    question: 'Continue?', rev: 'rev-7', multi: false, options: [{ key: 'continue_key', label: 'Continue' }, { key: 'cancel_key', label: 'Cancel' }]
  } });
  assert.equal(request.options[0], 'Continue');
  assert.equal(interactionScreenOptions(request)[0].key, 'continue_key');
  assert.deepEqual(interactionResolution(request, 'continue_key', {}, 'rev-7'), { option: 'continue_key' });
  assert.throws(() => interactionResolution(request, 'continue_key', {}, 'rev-6'), /request_updated/);

  const multi = decision({ id: 'screen-multi', kind: 'screen', dialog: {
    rev: 'rev-8', multi: true, submit: 'Save choices', options: [{ key: 'x', label: 'X' }, { key: 'y', label: 'Y' }]
  } });
  assert.deepEqual(interactionResolution(multi, 'submit', { options: ['y'], text: 'Other' }, 'rev-8'), {
    options: ['y'], text: 'Other'
  });
});

test('elicitation form validates typed schema values and excludes secret defaults', () => {
  const request = decision({ id: 'form-1', kind: 'elicitation', elicitation: {
    mode: 'form', message: 'Fill in the request', requested_schema: {
      type: 'object', required: ['email', 'count', 'notify'], properties: {
        email: { type: 'string', format: 'email' }, count: { type: 'integer', minimum: 1 },
        notify: { type: 'boolean' }, color: { type: 'string', enum: ['red', 'blue'], enumNames: ['Red', 'Blue'] },
        tags: { type: 'array', items: { type: 'string', enum: ['one', 'two'] }, maxItems: 2 },
        secret: { type: 'string', format: 'password', default: 'do-not-fill' }
      }
    }
  } });
  assert.equal(request.prompt, 'Fill in the request');
  assert.equal(interactionElicitationMessage(request), 'Fill in the request');
  assert.equal(interactionElicitationSupported(request), true);
  assert.equal(interactionElicitationMode(request), 'form');
  assert.equal(interactionElicitationDefaults(request).secret, undefined);
  assert.equal(request.fields.find((field) => field.key === 'secret').kind, 'secret');

  const invalid = validateInteractionElicitation(request, { email: 'bad', count: '', notify: false, color: 'red', tags: ['one'] });
  assert.equal(invalid.valid, false);
  assert.equal(invalid.errors.email, 'invalid');
  assert.equal(invalid.errors.count, 'invalid', 'blank numeric input is not silently coerced to zero');
  const valid = validateInteractionElicitation(request, { email: 'a@example.test', count: '2', notify: false, color: 'red', tags: ['one', 'two'] });
  assert.equal(valid.valid, true);
  assert.deepEqual(valid.content, { email: 'a@example.test', count: 2, notify: false, color: 'red', tags: ['one', 'two'] });
  assert.deepEqual(interactionResolution(request, 'accept', {
    email: 'a@example.test', count: '2', notify: false, color: 'red', tags: ['one'], secret: 'one-time'
  }), { action: 'accept', content: { email: 'a@example.test', count: 2, notify: false, color: 'red', tags: ['one'], secret: 'one-time' } });
});

test('elicitation URLs require an exact safe HTTP(S) open and preserve separate cancel/decline actions', () => {
  const request = decision({ id: 'url-1', kind: 'elicitation', elicitation: { mode: 'url', message: 'Open this page', url: 'https://example.test/path?x=1' } });
  const url = interactionElicitationUrl(request);
  assert.equal(url, 'https://example.test/path?x=1');
  assert.throws(() => interactionResolution(request, 'accept', {}), /required/);
  assert.deepEqual(interactionResolution(request, 'accept', { opened_url: url }), { action: 'accept' });
  assert.deepEqual(interactionResolution(request, 'decline', {}), { action: 'decline' });
  assert.deepEqual(interactionResolution(request, 'cancel', {}), { action: 'cancel' });
  assert.equal(interactionElicitationUrl(decision({ id: 'bad-url', kind: 'elicitation', elicitation: { mode: 'url', url: 'https://user@example.test/' } })), '');
  assert.equal(interactionElicitationUrl(decision({ id: 'other-mode', kind: 'elicitation', elicitation: { mode: 'form', url: 'https://example.test/' } })), '');
});

test('history entries expose safe outcomes and cannot be submitted', () => {
  const resolved = decision({ id: 'history-1', kind: 'permission', fields: [{ key: 'token', label: 'Token', secret: true }] }, {
    status: 'resolved', resolution: { allow: true, values: { token: 'secret-value' } }
  });
  assert.equal(interactionCanSubmit(resolved), false);
  assert.equal(interactionCanSubmit(decision({ id: 'open-history', kind: 'plan' }), true), false);
  assert.deepEqual(interactionHistoryResolution(resolved), { allow: true });
  assert.equal(JSON.stringify(interactionHistoryResolution(resolved)).includes('secret-value'), false);
});
