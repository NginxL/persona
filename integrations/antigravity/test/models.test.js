import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'
import { mapModels } from '../lib/models.js'

const entries = ['medium', 'high', 'low', 'xhigh', 'max'].map((level) => ({ modelId: `gemini-future-${level}`, name: `Gemini Future (${level})` }))
const AUTO_REFUSAL_NOTICE = 'agy 拒绝了 Auto 模式，已改由 Persona 自动批准权限请求'

test('native catalog preserves new IDs, defaults and only advertised thinking variants', () => {
  const models = mapModels([...entries, entries[0], { modelId: 'single', name: 'Single' }], entries[1].name)
  assert.equal(models.length, 6)
  assert.equal(models[1].default, true)
  for (const model of models.slice(0, -1)) {
    assert.deepEqual(model.efforts, ['low', 'medium', 'high', 'xhigh', 'max'])
    assert.equal(model.default_effort, model.id.split('-').at(-1))
  }
  assert.deepEqual(models.at(-1).efforts, [])
  assert.equal(models.at(-1).default_effort, undefined)
})

async function fixture(t, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-models-'))
  const catalog = (value) => writeFileSync(join(home, 'catalog.json'), JSON.stringify(value))
  catalog({ models: entries })
  const agyDir = join(home, 'settings')
  mkdirSync(agyDir)
  const settings = (value) => writeFileSync(join(agyDir, 'settings.json'), JSON.stringify(value))
  settings({ model: entries[1].name, agentMode: 'auto' })
  const bin = join(home, 'agy')
  const fake = fileURLToPath(new URL('./fake-acp-models.mjs', import.meta.url))
  writeFileSync(bin, `#!/bin/sh\nexec '${process.execPath}' '${fake}' '${home}' "$@"\n`)
  chmodSync(bin, 0o755)
  const n = await startFakeNode(t)
  const runtime = createAgyRuntime({ home, agyDir, agyBin: bin, checkTimeoutMs: options.checkTimeoutMs, personadBin: '/missing', socketPath: n.path, log: () => {},
    modelsTimeoutMs: options.modelsTimeoutMs, catalogEveryMs: options.catalogEveryMs, catalogMaxMs: options.catalogMaxMs })
  t.after(async () => { await runtime.stop(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await n.ready
  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').trim().split('\n').map(JSON.parse)
  const modelsCalls = () => {
    try {
      return readFileSync(join(home, 'models-calls.jsonl'), 'utf8').trim().split('\n').filter(Boolean).map((line) => JSON.parse(line).at)
    } catch {
      return []
    }
  }
  if (options.noStart) return { n, home, catalog, calls, settings, modelsCalls }
  const { session } = await n.peer.call('session.start', { cwd: home })
  const sessionId = session.session_id
  const row = () => n.last('runtime.sessions')?.sessions.find((entry) => entry.session_id === sessionId)
  return { n, home, catalog, settings, sessionId, row, session, calls }
}

async function modePermissionFixture(t, controls = {}, startParams = {}) {
  const f = await fixture(t, { noStart: true })
  f.catalog({ models: entries, refuseAuto: controls.refuseAuto, settingsModeUpdates: controls.settingsModeUpdates })
  f.settings({ model: entries[1].name, agentMode: controls.agentMode ?? 'default' })
  const { session } = await f.n.peer.call('session.start', { cwd: f.home, ...startParams })
  const sessionId = session.session_id
  const row = () => f.n.last('runtime.sessions')?.sessions.find((entry) => entry.session_id === sessionId)
  const answers = () => f.calls().filter((call) => typeof call.id === 'string' && call.id.startsWith('read-permission-') && call.result)
  const items = () => f.n.peer.call('session.items', { session_id: sessionId })
  return { ...f, session, sessionId, row, answers, items }
}

const selected = (optionId) => ({ outcome: { outcome: 'selected', optionId } })

test('catalog discovery and refresh use native CLI data and current settings', async (t) => {
  const { n, catalog, settings } = await fixture(t)
  let result = await n.peer.call('launchers.models', {})
  assert.equal(result.models.find((model) => model.default)?.id, entries[1].modelId)
  catalog({ models: [...entries, { modelId: 'new-native-model', name: 'New Native Model' }] })
  settings({ model: 'new-native-model' })
  result = await n.peer.call('launchers.models', { refresh: true })
  assert.equal(result.models.at(-1).default, true)
  await until('native configuration', () => n.last('runtime.launchers')?.launchers[0].model === 'New Native Model')
  catalog({ error: 'Please sign in' })
  await assert.rejects(n.peer.call('launchers.models', { refresh: true }), /Please sign in/)
})

test('session controls confirm native state and preserve acknowledged fields on failure', async (t) => {
  const { n, sessionId, row, session } = await fixture(t)
  assert.equal(n.hello.caps.model, true)
  assert.equal(n.hello.caps.models, true)
  assert.equal(session.model, entries[1].modelId)
  assert.equal(session.permission_mode, 'auto')
  assert.equal(session.effort, 'high')
  await until('empty owned session remains listed', () => row()?.status === 'idle')
  await n.peer.call('session.modes', { session_id: sessionId, model: entries[1].modelId, permission_mode: 'auto' })
  await until('native configuration', () => row()?.model === entries[1].modelId && row()?.permission_mode === 'auto')
  await assert.rejects(n.peer.call('session.modes', { session_id: sessionId, model: 'missing' }), /Model unavailable/)
  assert.equal(row().model, entries[1].modelId)
  await n.peer.call('session.modes', { session_id: sessionId, effort: 'low' })
  await until('native configuration', () => row()?.model === entries[2].modelId && row()?.effort === 'low')
  await assert.rejects(n.peer.call('session.modes', { session_id: sessionId, model: entries[1].modelId, effort: 'ultra' }), /Thinking level unavailable/)
  await until('native configuration', () => row()?.model === entries[1].modelId && row()?.effort === 'high')
  await n.peer.call('session.modes', { session_id: sessionId, permission_mode: 'bypassPermissions' })
  await until('native configuration', () => row()?.permission_mode === 'bypassPermissions')
})

test('a model pick or a thought-level switch applies the advertised native variant', async (t) => {
  const { n, sessionId, row, calls } = await fixture(t)
  const advertised = await n.peer.call('launchers.models', {})
  assert.deepEqual(advertised.models.find((model) => model.id === 'gemini-future-xhigh')?.efforts, ['low', 'medium', 'high', 'xhigh', 'max'])
  for (const effort of ['low', 'medium', 'high', 'xhigh', 'max']) {
    const model = `gemini-future-${effort}`
    await n.peer.call('session.modes', { session_id: sessionId, model })
    await until(`ACP selected ${model}`, () => row()?.model === model && row()?.effort === effort)
    assert.equal(calls().filter((call) => call.method === 'session/set_model').at(-1)?.params.modelId, model)
  }
  for (const effort of ['xhigh', 'max', 'medium']) {
    await n.peer.call('session.modes', { session_id: sessionId, effort })
    await until(`ACP selected ${effort}`, () => row()?.model === `gemini-future-${effort}` && row()?.effort === effort)
  }
})

test('uses the ACP session window until the transcript reports its own', async (t) => {
  const { n, home, catalog } = await fixture(t, { noStart: true })
  catalog({ models: entries, windows: { [entries[0].modelId]: 524288, [entries[2].modelId]: 131072 } })
  const { session } = await n.peer.call('session.start', { cwd: home })
  const row = () => n.last('runtime.sessions')?.sessions.find((entry) => entry.session_id === session.session_id)
  await until('initial ACP window', () => row()?.context_window === 524288)
  await n.peer.call('session.modes', { session_id: session.session_id, model: entries[2].modelId })
  await until('new model window', () => row()?.context_window === 131072)
  const logs = join(home, 'settings', 'brain', session.session_id.slice(4), '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  writeFileSync(join(logs, 'transcript.jsonl'), JSON.stringify({ step_index: 0, type: 'PLANNER_RESPONSE',
    model: entries[2].modelId.toUpperCase(), content: 'Done', input_tokens: 8000, context_window: 64000 }) + '\n')
  await n.peer.call('session.rename', { session_id: session.session_id, title: 'Recorded usage' })
  await until('transcript window overrides ACP', () => row()?.context_tokens === 8000 && row()?.context_window === 64000)
})

test('a rejected startup configuration closes the newly created native session', async (t) => {
  const { n, calls } = await fixture(t)
  await assert.rejects(n.peer.call('session.start', { model: 'missing' }), /Model unavailable/)
  assert.equal(calls().filter((call) => call.method === 'session/close').length, 1)
})


test('availability checks use native ACP and deny tools without phone cards', async (t) => {
  const { n, home, catalog, calls } = await fixture(t, { noStart: true })
  catalog({ models: entries, askPermission: true })
  const result = await n.peer.call('launchers.check', { cwd: home, prompt: 'Reply with OK' })
  assert.equal(n.hello.caps.check, true)
  assert.equal(result.ok, true)
  assert.equal(result.answer, 'OK')
  assert.equal(result.model, entries[1].modelId)
  assert.equal(n.asked.length, 0)
  assert.equal(calls().find((call) => call.method === 'session/set_mode')?.params?.modeId, 'default')
  assert.equal(calls().find((call) => call.id === 'check-permission')?.result?.outcome?.outcome, 'cancelled')
  assert.equal(calls().filter((call) => call.method === 'session/close').length, 1)
  assert.equal(n.last('runtime.sessions')?.sessions.length ?? 0, 0)
})

test('failed availability inference closes the check session and propagates the native error', async (t) => {
  const { n, home, catalog, calls } = await fixture(t, { noStart: true })
  catalog({ models: entries, promptError: 'Provider unavailable' })
  await assert.rejects(n.peer.call('launchers.check', { cwd: home, prompt: 'Reply with OK' }), /Provider unavailable/)
  assert.equal(calls().filter((call) => call.method === 'session/close').length, 1)
})

test('an availability timeout cancels its prompt and closes the session', async (t) => {
  // Warm the ACP process before measuring the prompt timeout.
  const { n, home, catalog, calls } = await fixture(t, { checkTimeoutMs: 200 })
  catalog({ models: entries, holdPrompt: true })
  await assert.rejects(n.peer.call('launchers.check', { cwd: home, prompt: 'Reply with OK' }), /timed out/)
  assert.equal(calls().filter((call) => call.method === 'session/cancel').length, 1)
  assert.equal(calls().filter((call) => call.method === 'session/close').length, 1)
})


for (const model of [entries[1].name, entries[1].modelId]) {
  test(`new sessions apply the configured default ${model}`, async (t) => {
    const { n, home, settings, calls } = await fixture(t, { noStart: true })
    settings({ model })
    const result = await n.peer.call('session.start', { cwd: home })
    assert.equal(result.session.model, entries[1].modelId)
    assert.equal(calls().find((call) => call.method === 'session/set_model')?.params?.modelId, entries[1].modelId)
  })
}

test('an explicit caller model overrides the configured default', async (t) => {
  const { n, home } = await fixture(t, { noStart: true })
  const result = await n.peer.call('session.start', { cwd: home, model: entries[2].modelId })
  assert.equal(result.session.model, entries[2].modelId)
  assert.equal(result.session.effort, 'low')
})

test('resuming a session does not replace its native model with the configured default', async (t) => {
  const { n, calls } = await fixture(t, { noStart: true })
  await n.peer.call('session.modes', { session_id: 'agy-11111111-2222-3333-4444-555555555555', permission_mode: 'default' })
  assert.equal(calls().filter((call) => call.method === 'session/set_model').length, 0)
})

test('refused auto automatically approves git status, rg and an MCP read without phone cards', async (t) => {
  const f = await modePermissionFixture(t, { refuseAuto: true }, { assistant: true, permission_mode: 'auto', prompt: 'ask-read-permissions' })
  await until('all three read permissions are answered', () => f.answers().length === 3)
  await until('the task turn ends', () => f.row()?.status === 'done')
  assert.deepEqual({
    mode: f.row()?.permission_mode ?? 'default',
    cards: f.n.asked.map((interaction) => interaction.tool_name),
    answers: f.answers().map((answer) => ({ id: answer.id, result: answer.result })).sort((a, b) => a.id.localeCompare(b.id)),
  }, {
    mode: 'bypassPermissions',
    cards: [],
    answers: [
      { id: 'read-permission-git-status', result: selected('always') },
      { id: 'read-permission-mcp', result: selected('always') },
      { id: 'read-permission-rg', result: selected('always') },
    ],
  })
})

test('refused auto publishes the applied Persona mode and exactly one fallback notice', async (t) => {
  const f = await modePermissionFixture(t, { refuseAuto: true }, { assistant: true, permission_mode: 'auto' })
  // Starting a task can apply its requested mode again; the session still gets only one notice.
  await f.n.peer.call('session.modes', { session_id: f.sessionId, permission_mode: 'auto' })
  const page = await f.items()
  const notices = page.items.filter((item) => item.kind === 'notice')
  assert.deepEqual({ mode: f.session.permission_mode, notices: notices.map((item) => item.text) }, {
    mode: 'bypassPermissions',
    notices: [AUTO_REFUSAL_NOTICE],
  })
  assert.equal(notices[0]?.role, 'system')
})

test('a settings-derived session follows agentMode changes without a companion restart', async (t) => {
  const f = await modePermissionFixture(t, { agentMode: 'default', refuseAuto: true, settingsModeUpdates: true })
  f.settings({ model: entries[1].name, agentMode: 'auto' })
  await f.n.peer.call('session.send', { session_id: f.sessionId, text: 'ask-read-permission' })
  await until('the permission after the settings change is answered', () => f.answers().length === 1)
  await until('the turn after the settings change ends', () => f.row()?.status === 'done')
  assert.deepEqual({
    mode: f.row()?.permission_mode ?? 'default',
    cards: f.n.asked.length,
    answer: f.answers()[0].result,
  }, { mode: 'bypassPermissions', cards: 0, answer: selected('always') })
})

test('an explicit session mode remains authoritative over agy settings changes', async (t) => {
  const f = await modePermissionFixture(t, { agentMode: 'default', refuseAuto: true, settingsModeUpdates: true }, { permission_mode: 'plan' })
  f.n.resolve = () => ({ allow: true })
  f.settings({ model: entries[1].name, agentMode: 'auto' })
  await f.n.peer.call('session.send', { session_id: f.sessionId, text: 'ask-read-permission' })
  await until('the explicitly planned permission is answered', () => f.answers().length === 1)
  await until('the explicit-mode turn ends', () => f.row()?.status === 'done')
  assert.deepEqual({
    mode: f.row()?.permission_mode ?? 'default',
    cards: f.n.asked.length,
    answer: f.answers()[0].result,
  }, { mode: 'plan', cards: 1, answer: selected('allow') })
})

for (const [permissionMode, nativeMode] of [['default', 'default'], ['plan', 'plan'], ['acceptEdits', 'accept-edits']]) {
  test(`explicit ${permissionMode} keeps forwarding permission requests as phone cards`, async (t) => {
    const f = await modePermissionFixture(t, { refuseAuto: true }, { permission_mode: permissionMode })
    f.n.resolve = () => ({ allow: true })
    await f.n.peer.call('session.send', { session_id: f.sessionId, text: 'ask-read-permission' })
    await until('the phone answer reaches agy', () => f.answers().length === 1)
    await until('the asking-mode turn ends', () => f.row()?.status === 'done')
    assert.equal(f.n.asked.length, 1)
    assert.equal(f.n.asked[0].kind, 'permission')
    assert.deepEqual(f.answers()[0].result, selected('allow'))
    assert.equal(f.calls().filter((call) => call.method === 'session/set_mode').at(-1)?.params?.modeId, nativeMode)
  })
}

test('accepted native auto remains auto and is not broadened to bypass', async (t) => {
  const f = await modePermissionFixture(t, { refuseAuto: false }, { permission_mode: 'auto' })
  assert.equal(f.session.permission_mode, 'auto')
  f.n.resolve = () => ({ allow: true })
  await f.n.peer.call('session.send', { session_id: f.sessionId, text: 'ask-read-permission' })
  await until('native auto forwards its permission request', () => f.answers().length === 1)
  await until('the native-auto turn ends', () => f.row()?.status === 'done')
  assert.equal(f.row()?.permission_mode, 'auto')
  assert.equal(f.n.asked.length, 1)
  assert.deepEqual(f.answers()[0].result, selected('allow'))
  assert.deepEqual(f.calls().filter((call) => call.method === 'session/set_mode').map((call) => call.params.modeId), ['auto'])
  assert.equal((await f.items()).items.some((item) => item.kind === 'notice' && item.text === AUTO_REFUSAL_NOTICE), false)
})

test('a newer explicit asking mode wins over an in-flight auto fallback', async (t) => {
  const f = await modePermissionFixture(t, { refuseAuto: false }, { permission_mode: 'auto' })
  f.catalog({ models: entries, refuseAuto: true, holdFallback: true })
  const autoChange = f.n.peer.call('session.modes', { session_id: f.sessionId, permission_mode: 'auto' })
  await until('the refused-auto fallback starts', () => f.calls().some((call) => call.method === 'session/set_mode' && call.params?.modeId === 'accept-edits'))
  const planChange = f.n.peer.call('session.modes', { session_id: f.sessionId, permission_mode: 'plan' })
  const deadline = Date.now() + 250
  await until('the explicit plan is sent or the fallback can finish', () => f.calls().some((call) => call.method === 'session/set_mode' && call.params?.modeId === 'plan') || Date.now() > deadline, 500)
  f.catalog({ models: entries, refuseAuto: true, holdFallback: true, releaseFallback: true })
  await Promise.all([autoChange, planChange])
  f.n.resolve = () => ({ allow: true })
  await f.n.peer.call('session.send', { session_id: f.sessionId, text: 'ask-read-permission' })
  await until('the permission after the explicit change is answered', () => f.answers().length === 1)
  await until('the explicit-mode turn ends', () => f.row()?.status === 'done')
  assert.equal(f.row()?.permission_mode, 'plan')
  assert.equal(f.n.asked.length, 1)
  assert.deepEqual(f.answers()[0].result, selected('allow'))
  assert.equal(f.calls().filter((call) => call.method === 'session/set_mode').at(-1)?.params?.modeId, 'plan')
})

test('interrupt cancels a permission that is waiting for the auto fallback', async (t) => {
  const f = await modePermissionFixture(t, { refuseAuto: false }, { permission_mode: 'auto' })
  f.catalog({ models: entries, refuseAutoOnPrompt: true, holdFallback: true })
  const sending = f.n.peer.call('session.send', { session_id: f.sessionId, text: 'ask-read-permission' })
  await until('the refused-auto fallback starts', () => f.calls().some((call) => call.method === 'session/set_mode' && call.params?.modeId === 'accept-edits'), 10_000)
  await until('the permission reaches the runtime', () => f.calls().some((call) => call.method === 'fake/permission'), 10_000)
  await new Promise((resolve) => setImmediate(resolve))
  const interrupting = f.n.peer.call('session.interrupt', { session_id: f.sessionId })
  await until('interrupt reaches agy', () => f.calls().some((call) => call.method === 'session/cancel'))
  f.catalog({ models: entries, refuseAutoOnPrompt: true, holdFallback: true, releaseFallback: true })
  await Promise.all([sending, interrupting])
  await until('the fallback settles the interrupted permission', () => f.answers().length === 1)
  assert.deepEqual(f.answers()[0].result, { outcome: { outcome: 'cancelled' } })
})

test('an explicit asking mode is reapplied after native settings change it', async (t) => {
  const f = await modePermissionFixture(t, { agentMode: 'default', refuseAuto: false, settingsModeUpdates: true }, { permission_mode: 'plan' })
  f.n.resolve = () => ({ allow: true })
  f.settings({ model: entries[1].name, agentMode: 'auto' })
  await f.n.peer.call('session.send', { session_id: f.sessionId, text: 'ask-read-permission' })
  await until('the native settings update applies', () => f.answers().length === 1)
  await f.n.peer.call('session.send', { session_id: f.sessionId, text: 'ask-read-permission' })
  await until('the next turn restores the explicit mode', () => f.answers().length === 2)
  assert.equal(f.calls().filter((call) => call.method === 'session/set_mode' && call.params?.modeId === 'plan').length, 2)
  assert.equal(f.row()?.permission_mode, 'plan')
  assert.equal(f.n.asked.length, 2)
})

test('a delayed default after auto acknowledgement still uses the fallback', async (t) => {
  const f = await modePermissionFixture(t, { refuseAuto: true, autoThenDefault: true }, { permission_mode: 'auto' })
  await until('the delayed refusal finishes', () => f.row()?.permission_mode === 'bypassPermissions')
  await f.n.peer.call('session.send', { session_id: f.sessionId, text: 'ask-read-permission' })
  await until('the delayed-fallback permission is answered', () => f.answers().length === 1)
  assert.equal(f.n.asked.length, 0)
  assert.deepEqual(f.answers()[0].result, selected('always'))
  const notices = (await f.items()).items.filter((item) => item.kind === 'notice' && item.text === AUTO_REFUSAL_NOTICE)
  assert.equal(notices.length, 1)
})

test('auto-mode refusal notice shifts projected seq without misaligning item usage or model', async (t) => {
  const f = await modePermissionFixture(t, { refuseAuto: true }, { assistant: true, permission_mode: 'auto' })
  const rawId = f.sessionId.replace(/^agy-/, '')
  const logs = join(f.home, 'settings', 'brain', rawId, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const at = '2026-11-21T10:00:00Z'
  const rawSteps = [
    { step_index: 0, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', created_at: at, content: 'Run check' },
    {
      step_index: 1, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', created_at: at,
      model: 'claude-opus-5-5-high', thinking: 'Checking', content: 'Running tool',
      input_tokens: 1200, cache_read_tokens: 48800, cache_write_tokens: 300, output_tokens: 180,
      tool_calls: [{ name: 'run_command', args: { CommandLine: 'echo ok' } }],
    },
    { step_index: 2, source: 'MODEL', type: 'GENERIC', status: 'DONE', created_at: at, content: 'ok' },
    {
      step_index: 3, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', created_at: at,
      model: 'claude-opus-5-5-high', content: 'Done.',
      input_tokens: 500, cache_read_tokens: 51000, output_tokens: 70,
    },
  ]
  writeFileSync(join(logs, 'transcript_full.jsonl'), rawSteps.map((s) => JSON.stringify(s)).join('\n') + '\n')

  const page = await f.items()
  assert.equal(page.items[0].kind, 'notice')
  assert.equal(page.items[0].text, AUTO_REFUSAL_NOTICE)
  assert.equal(page.items[0].seq, 1)
  assert.equal(page.items[0].usage, undefined)
  assert.equal(page.items[0].model, undefined)

  // Raw step 1 shifted to projected seq 3; raw step 3 shifted to projected seq 5.
  const step1Items = page.items.filter((item) => item.seq === 3)
  assert.equal(step1Items.length, 3)
  const wantStep1Usage = [{ model: 'claude-opus-5-5-high', in: 1200, out: 180, cache_read: 48800, cache_write: 300 }]
  assert.deepEqual(step1Items[0].usage, wantStep1Usage)
  assert.equal(step1Items[1].usage, undefined)
  assert.equal(step1Items[2].usage, undefined)
  assert.deepEqual(step1Items.map((item) => item.model), ['claude-opus-5-5-high', 'claude-opus-5-5-high', 'claude-opus-5-5-high'])

  const step3Item = page.items.find((item) => item.seq === 5)
  assert.equal(step3Item.text, 'Done.')
  assert.equal(step3Item.model, 'claude-opus-5-5-high')
  assert.deepEqual(step3Item.usage, [{ model: 'claude-opus-5-5-high', in: 500, out: 70, cache_read: 51000 }])

  // session.item returns exact projected usage on #0 and omits it on secondary #1.
  const primary = await f.n.peer.call('session.item', { session_id: f.sessionId, item_id: step1Items[0].id })
  assert.deepEqual(primary.usage, wantStep1Usage)
  assert.equal(primary.model, 'claude-opus-5-5-high')
  const secondary = await f.n.peer.call('session.item', { session_id: f.sessionId, item_id: step1Items[1].id })
  assert.equal(secondary.usage, undefined)
  assert.equal(secondary.model, 'claude-opus-5-5-high')
})

// #11106: an expired sign-in makes agy models hang rather than fail. The timeout says what to do about it.
test('a discovery that times out reports an expired sign-in', async (t) => {
  const { n, catalog } = await fixture(t, { noStart: true, modelsTimeoutMs: 300 })
  catalog({ hang: true })
  await assert.rejects(n.peer.call('launchers.models', { refresh: true }), /sign-in may have expired\. Run agy once/)
  catalog({ models: entries })
  const result = await n.peer.call('launchers.models', { refresh: true })
  assert.equal(result.models.length, entries.length)
})

// #11106: discoveries that keep timing out back off, doubling the wait; one that answers brings the usual pace back.
test('periodic discovery backs off while it keeps timing out', { timeout: 30_000 }, async (t) => {
  const { catalog, modelsCalls } = await fixture(t, { noStart: true, modelsTimeoutMs: 100, catalogEveryMs: 250, catalogMaxMs: 10_000 })
  catalog({ hang: true })
  await until('four periodic discoveries', () => modelsCalls().length >= 5, 20_000)
  const at = modelsCalls()
  // at[0] is the start's own discovery; each periodic one that times out doubles the next wait (250, 500, 1000, 2000 ms).
  const gaps = [at[2] - at[1], at[3] - at[2], at[4] - at[3]]
  assert.ok(gaps[1] > gaps[0] * 1.4 && gaps[2] > gaps[1] * 1.4, `gaps grow: ${gaps.join(', ')}`)
  catalog({ models: entries })
  const before = modelsCalls().length
  await until('a discovery answers', () => modelsCalls().length > before, 20_000)
  await until('the usual pace again', () => modelsCalls().length >= before + 2, 20_000)
  const after = modelsCalls()
  assert.ok(after.at(-1) - after.at(-2) < 1000, `back to every 250 ms: ${after.at(-1) - after.at(-2)} ms`)
})
