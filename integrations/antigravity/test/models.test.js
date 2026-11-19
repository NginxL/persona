import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'
import { mapModels } from '../lib/models.js'

const entries = ['medium', 'high', 'low'].map((level) => ({ modelId: `gemini-future-${level}`, name: `Gemini Future (${level})` }))

test('native catalog preserves new IDs, defaults and only advertised thinking variants', () => {
  const models = mapModels([...entries, entries[0], { modelId: 'single', name: 'Single' }], entries[1].name)
  assert.equal(models.length, 4)
  assert.equal(models[1].default, true)
  assert.deepEqual(models[0].efforts, ['low', 'medium', 'high'])
  assert.deepEqual(models[3].efforts, [])
  assert.equal(models[0].default_effort, 'medium')
})

async function fixture(t, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-models-'))
  t.after(() => rmSync(home, { recursive: true, force: true }))
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
  t.after(() => runtime.stop())
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
  await assert.rejects(n.peer.call('session.modes', { session_id: sessionId, model: entries[1].modelId, effort: 'max' }), /Thinking level unavailable/)
  await until('native configuration', () => row()?.model === entries[1].modelId && row()?.effort === 'high')
  await n.peer.call('session.modes', { session_id: sessionId, permission_mode: 'bypassPermissions' })
  await until('native configuration', () => row()?.permission_mode === 'bypassPermissions')
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
