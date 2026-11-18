// An agy acp process can keep the model list it had when the account's plan lapsed or the CLI signed out (#10368).
import assert from 'node:assert/strict'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const model = (id, name) => ({ modelId: id, name })
const LAPSED = [model('gemini-flash-medium', 'Gemini Flash (Medium)'), model('gemini-flash-high', 'Gemini Flash (High)')]
const FULL = [...LAPSED, model('gemini-carbon-medium', 'Gemini Carbon (Medium)'), model('gemini-carbon-high', 'Gemini Carbon (High)')]
const CARBON = 'gemini-carbon-high'

async function fixture(t, models, configured = 'Gemini Carbon (High)') {
  const home = mkdtempSync(join(tmpdir(), 'agy-stale-'))
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const account = (value) => writeFileSync(join(home, 'account.json'), JSON.stringify(value))
  account({ models })
  const agyDir = join(home, 'settings')
  mkdirSync(agyDir)
  writeFileSync(join(agyDir, 'settings.json'), JSON.stringify({ model: configured }))
  const bin = join(home, 'agy')
  const fake = fileURLToPath(new URL('./fake-acp-stale.mjs', import.meta.url))
  writeFileSync(bin, `#!/bin/sh\nexec '${process.execPath}' '${fake}' '${home}' "$@"\n`)
  chmodSync(bin, 0o755)
  const n = await startFakeNode(t)
  const logs = []
  const runtime = createAgyRuntime({ home, agyDir, agyBin: bin, personadBin: '/missing', socketPath: n.path, log: (m) => logs.push(m) })
  t.after(() => runtime.stop())
  runtime.start()
  await n.ready
  const calls = () => existsSync(join(home, 'calls.jsonl'))
    ? readFileSync(join(home, 'calls.jsonl'), 'utf8').trim().split('\n').map((line) => JSON.parse(line)) : []
  const processes = () => [...new Set(calls().map((call) => call.pid))]
  const published = () => n.last('runtime.launchers')?.launchers[0].models.map((entry) => entry.id) || []
  const hold = (on) => on ? writeFileSync(join(home, 'hold'), '') : rmSync(join(home, 'hold'), { force: true })
  const check = () => n.peer.call('launchers.check', { cwd: home, prompt: 'Reply with OK' })
  const holdDiscovery = (on) => on ? writeFileSync(join(home, 'models-hold'), '') : rmSync(join(home, 'models-hold'), { force: true })
  const discoveries = () => existsSync(join(home, 'discoveries')) ? readFileSync(join(home, 'discoveries'), 'utf8').trim().split('\n').length : 0
  await until('the first discovery', () => published().length === models.length)
  return { n, home, agyDir, account, calls, processes, published, hold, check, logs, holdDiscovery, discoveries }
}

/** Start a session whose turn stays running until `end()`, which checks that nothing cancelled or failed it. */
async function runningTurn({ n, home, calls, hold, logs }) {
  hold(true)
  const { session } = await n.peer.call('session.start', { cwd: home, model: 'gemini-flash-high', prompt: 'Work' })
  await until('the running turn', () => calls().some((call) => call.method === 'session/prompt'))
  const row = () => n.last('runtime.sessions')?.sessions.find((entry) => entry.session_id === session.session_id)
  return async () => {
    hold(false)
    await until('the turn to end', () => row() && row().status !== 'working')
    const raw = session.session_id.replace(/^agy-/, '')
    assert.deepEqual(calls().filter((call) => call.method === 'session/cancel' && call.params.sessionId === raw), [])
    assert.deepEqual(logs.filter((line) => /prompt error|replacing/.test(line)), [])
  }
}

test('after the plan is renewed, a model agy models lists again starts on a replaced agy acp process', async (t) => {
  const f = await fixture(t, LAPSED)
  // Not on the lapsed plan: it fails once, as before, and the process stays.
  await assert.rejects(f.n.peer.call('session.start', { cwd: f.home }), /Unknown model ID: Gemini Carbon \(High\)/)
  assert.equal(f.processes().length, 1)

  f.account({ models: FULL }) // renewed: the CLI lists Carbon again, the running agy acp keeps the lapsed list
  const { session } = await f.n.peer.call('session.start', { cwd: f.home })
  assert.equal(session.model, CARBON)
  assert.equal(f.processes().length, 2)
  const [, fresh] = f.processes()
  assert.deepEqual(f.calls().filter((call) => call.method === 'session/set_model').map((call) => [call.pid === fresh, call.params.modelId]),
    [[false, 'Gemini Carbon (High)'], [false, 'Gemini Carbon (High)'], [true, CARBON]])
  // The session the old process refused is closed, not left behind.
  assert.equal(f.calls().filter((call) => call.method === 'session/close' && call.pid !== fresh).length, 2)
  assert.deepEqual(f.published(), FULL.map((entry) => entry.modelId))

  const result = await f.check()
  assert.equal(result.ok, true)
  assert.equal(result.model, CARBON)
  assert.equal(f.processes().length, 2)
})

test('a running turn keeps its agy acp process: the start fails as before until the turn ends', async (t) => {
  const f = await fixture(t, LAPSED)
  const end = await runningTurn(f)
  f.account({ models: FULL })
  await assert.rejects(f.n.peer.call('session.start', { cwd: f.home }), /Unknown model ID/)
  await assert.rejects(f.check(), /Unknown model ID/)
  assert.equal(f.processes().length, 1)

  await end()
  const { session } = await f.n.peer.call('session.start', { cwd: f.home })
  assert.equal(session.model, CARBON)
  assert.equal(f.processes().length, 2)
})

test('a model a fresh agy acp process also refuses fails as before, after one replacement', async (t) => {
  const f = await fixture(t, FULL)
  f.account({ models: FULL, rejects: [CARBON] })
  await assert.rejects(f.n.peer.call('session.start', { cwd: f.home }), new RegExp(`Unknown model ID: ${CARBON}`))
  assert.equal(f.processes().length, 2)
  assert.equal(f.calls().filter((call) => call.method === 'session/set_model').length, 2)

  // The same catalog and the same process: no replacement loop.
  await assert.rejects(f.n.peer.call('session.start', { cwd: f.home }), /Unknown model ID/)
  await assert.rejects(f.check(), /Unknown model ID/)
  assert.equal(f.processes().length, 2)
  assert.equal(f.calls().filter((call) => call.method === 'session/set_model').length, 4)
})

test('a session result from an agy acp process older than the discovery does not shrink the catalog', async (t) => {
  const f = await fixture(t, LAPSED, 'Gemini Flash (High)')
  await f.n.peer.call('session.start', { cwd: f.home })
  f.account({ models: FULL })
  const refreshed = await f.n.peer.call('launchers.models', { refresh: true })
  assert.equal(refreshed.models.length, FULL.length)
  const before = f.n.all('runtime.launchers').length

  // Both run on the process that still has the lapsed list, and both succeed: Flash is on either plan.
  await f.n.peer.call('session.start', { cwd: f.home, model: 'gemini-flash-medium' })
  assert.equal((await f.check()).ok, true)
  assert.equal(f.processes().length, 1)
  for (const launchers of f.n.all('runtime.launchers').slice(before)) assert.equal(launchers.launchers[0].models.length, FULL.length)
  assert.equal((await f.n.peer.call('launchers.models', {})).models.length, FULL.length)
})

test('after the CLI signs in again, its signed-out agy acp process is replaced', async (t) => {
  const f = await fixture(t, FULL)
  assert.equal((await f.check()).ok, true)
  f.account({ error: 'Please sign in' })
  await assert.rejects(f.n.peer.call('launchers.models', { refresh: true }), /Please sign in/)
  await assert.rejects(f.check(), /Please sign in/)

  f.account({ models: FULL })
  await f.n.peer.call('launchers.models', { refresh: true })
  await until('the replacement', () => f.logs.some((line) => /replacing the process \(the Antigravity CLI signed in again\)/.test(line)))
  assert.equal((await f.check()).ok, true)
  assert.equal(f.processes().length, 2)
})

test('a sign-in seen during a running turn replaces the process only after the turn ends', async (t) => {
  const f = await fixture(t, FULL)
  const end = await runningTurn(f)
  f.account({ error: 'Please sign in' })
  await assert.rejects(f.n.peer.call('launchers.models', { refresh: true }), /Please sign in/)
  await assert.rejects(f.check(), /Please sign in/)
  f.account({ models: FULL })
  await f.n.peer.call('launchers.models', { refresh: true })
  await assert.rejects(f.check(), /Please sign in/)
  assert.equal(f.processes().length, 1)

  await end()
  assert.equal((await f.check()).ok, true)
  assert.equal(f.processes().length, 2)
})

test('a model change on an existing session resumes it on the replaced agy acp process', async (t) => {
  const f = await fixture(t, LAPSED, 'Gemini Flash (High)')
  const { session } = await f.n.peer.call('session.start', { cwd: f.home })
  f.account({ models: FULL })
  await f.n.peer.call('session.modes', { session_id: session.session_id, model: CARBON })
  const [old, fresh] = f.processes()
  const raw = session.session_id.replace(/^agy-/, '')
  assert.deepEqual(f.calls().filter((call) => call.params?.sessionId === raw && /set_model|resume/.test(call.method))
    .map((call) => [call.pid === fresh ? 'fresh' : call.pid === old ? 'old' : '?', call.method, call.params.modelId]),
  [['old', 'session/set_model', 'gemini-flash-high'], ['old', 'session/set_model', CARBON],
    ['fresh', 'session/resume', undefined], ['fresh', 'session/set_model', CARBON]])
  await until('the new model', () => f.n.last('runtime.sessions')?.sessions.find((entry) => entry.session_id === session.session_id)?.model === CARBON)
})

test('a turn that starts during the discovery for a refused model keeps its agy acp process', async (t) => {
  const f = await fixture(t, LAPSED)
  await f.n.peer.call('session.start', { cwd: f.home, model: 'gemini-flash-high' })
  f.account({ models: FULL })
  f.holdDiscovery(true)
  const refused = f.n.peer.call('session.start', { cwd: f.home })
  await until('the forced discovery', () => f.discoveries() === 2)
  const end = await runningTurn(f)
  f.holdDiscovery(false)
  await assert.rejects(refused, /Unknown model ID/)
  assert.equal(f.processes().length, 1)

  await end()
  const { session } = await f.n.peer.call('session.start', { cwd: f.home })
  assert.equal(session.model, CARBON)
  assert.equal(f.processes().length, 2)
})

test('a call between two agy acp requests keeps its process when an overlapping call would replace it', async (t) => {
  const f = await fixture(t, LAPSED)
  await f.n.peer.call('session.start', { cwd: f.home, model: 'gemini-flash-high' })
  f.account({ models: FULL })
  // An idle conversation whose failed last step the export omits: reading it asks the native service, no ACP request.
  const raw = '22222222-3333-4444-8555-666666666666'
  const logs = join(f.agyDir, 'brain', raw, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  writeFileSync(join(logs, 'transcript_full.jsonl'), `${JSON.stringify({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT',
    status: 'DONE', created_at: '2026-01-02T12:00:00Z', content: 'Check the result.' })}\n`)
  writeFileSync(join(f.home, 'native.json'), JSON.stringify([{ type: 'CORTEX_STEP_TYPE_USER_INPUT' }, { type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE',
    metadata: { createdAt: '2026-01-02T12:00:01Z' }, errorMessage: { error: { userErrorMessage: 'Provider unavailable', errorCode: 503 } } }]))
  writeFileSync(join(f.home, 'native-hold'), '')
  const db = new DatabaseSync(join(f.agyDir, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, 2, ?, ?, ?)')
    .run(raw, 'CASCADE_RUN_STATUS_IDLE', 'Native read', JSON.stringify([`file://${f.home}`]), new Date().toISOString())
  db.close()
  const page = f.n.peer.call('session.items', { session_id: `agy-${raw}` })
  await until('the native read', () => existsSync(join(f.home, 'native.jsonl')))

  await assert.rejects(f.n.peer.call('session.start', { cwd: f.home }), /Unknown model ID/)
  assert.equal(f.processes().length, 1)
  rmSync(join(f.home, 'native-hold'))
  assert.equal((await page).items.at(-1).text, 'API Error: Provider unavailable')
  assert.deepEqual(f.logs.filter((line) => /native turn error|replacing/.test(line)), [])

  const { session } = await f.n.peer.call('session.start', { cwd: f.home })
  assert.equal(session.model, CARBON)
  assert.equal(f.processes().length, 2)
})

test('a call that arrives while agy acp is being replaced waits for the old process to exit', async (t) => {
  const f = await fixture(t, LAPSED)
  f.account({ models: LAPSED, slowExit: true })
  await f.n.peer.call('session.start', { cwd: f.home, model: 'gemini-flash-high' })
  f.account({ models: FULL })
  f.holdDiscovery(true)
  const renewed = f.n.peer.call('session.start', { cwd: f.home })
  await until('the forced discovery', () => f.discoveries() === 2)
  f.holdDiscovery(false)
  await until('the replacement', () => f.logs.some((line) => /replacing the process/.test(line)))
  assert.equal((await f.check()).ok, true)
  assert.equal((await renewed).session.model, CARBON)
  assert.equal(f.processes().length, 2)
  assert.equal(existsSync(join(f.home, 'overlaps')), false)
})
