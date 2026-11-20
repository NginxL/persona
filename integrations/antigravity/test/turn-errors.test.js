import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { nativeErrorStep } from '../lib/errors.js'
import { stepsToItems } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const RAW = '11111111-2222-4333-8444-555555555555'
const SESSION = `agy-${RAW}`
const FAKE = fileURLToPath(new URL('./fake-acp-rewind.mjs', import.meta.url))
const AT = '2026-01-02T12:00:00Z'
const RESET = '2026-01-03T18:00:00Z'
const MESSAGE = 'Individual quota reached. Resets in 30h.'
const TRANSIENT_401 = 'UNAUTHENTICATED (code 401): Request is missing required authentication credential'
const input = { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: AT, content: 'Check the result.' }
const error = {
  type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE', metadata: { createdAt: AT },
  errorMessage: { error: {
    userErrorMessage: MESSAGE, errorCode: 429,
    details: JSON.stringify({ error: { details: [{ reason: 'QUOTA_EXHAUSTED', metadata: { quotaResetTimeStamp: RESET } }] } }),
  } },
}

test('native errors preserve structured subscription limits and their actual timestamp', () => {
  const step = nativeErrorStep(error, 1)
  const parsed = stepsToItems([input, step])
  assert.equal(parsed.lastText, '')
  assert.deepEqual(parsed.turnError, {
    turn_error: MESSAGE, turn_error_kind: 'usage_limit', turn_error_resets_at: '2026-01-03T18:00:00.000Z',
    turn_error_code: 'rate_limited', turn_error_after_work: false,
  })
  assert.equal(parsed.items.at(-1).role, 'assistant')
  assert.equal(parsed.items.at(-1).kind, 'text')
  assert.equal(parsed.items.at(-1).text, `API Error: ${MESSAGE}`)
  const existing = stepsToItems([{ ...step, content: 'API Error: 503 unavailable' }])
  assert.equal(existing.items[0].text, 'API Error: 503 unavailable')
  const work = { step_index: 1, type: 'PLANNER_RESPONSE', tool_calls: [{ name: 'run_command' }] }
  assert.equal(stepsToItems([input, work, { ...step, step_index: 2 }]).turnError.turn_error_after_work, true)
  assert.equal(stepsToItems([input, step, { ...input, step_index: 2 }]).turnError, undefined)
})

test('ordinary rate limits and malformed metadata keep their native error without inventing a reset', () => {
  for (const details of [undefined, 'invalid JSON', JSON.stringify({ error: { details: {} } }),
    JSON.stringify({ error: { details: [{ reason: 'QUOTA_EXHAUSTED', metadata: { quotaResetTimeStamp: 'invalid' } }] } })]) {
    const step = nativeErrorStep({ ...error, errorMessage: { error: { userErrorMessage: 'Too many requests', errorCode: 429, details } } }, 0)
    assert.deepEqual(step.turn_error, { turn_error: 'Too many requests', turn_error_code: 'rate_limited' })
  }
  assert.equal(nativeErrorStep({ type: 'CORTEX_STEP_TYPE_USER_INPUT' }, 0), null)
  assert.equal(nativeErrorStep({ type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE' }, 0), null)
  assert.equal(nativeErrorStep({ ...error, status: 'CORTEX_STEP_STATUS_CANCELLED' }, 0), null)
})

async function fixture(t, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-turn-error-'))
  const config = join(home, 'config')
  const logs = join(config, 'brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  const transcript = join(logs, 'transcript_full.jsonl')
  const write = (steps) => writeFileSync(transcript, steps.map((step) => JSON.stringify(step)).join('\n') + '\n')
  write([input])
  writeFileSync(join(home, 'calls.jsonl'), '')
  const nativeFile = join(home, 'trajectory.json')
  const snapshot = {
    lazyService: true,
    holdErrors: options.holdErrors,
    status: 'CASCADE_RUN_STATUS_IDLE',
    trajectory: { cascadeId: RAW, steps: [
      { type: 'CORTEX_STEP_TYPE_USER_INPUT', metadata: { source: 'CORTEX_STEP_SOURCE_USER_EXPLICIT' }, userInput: { items: [{ text: input.content }] } },
      ...Array.from({ length: options.padding || 0 }, () => ({ type: 'CORTEX_STEP_TYPE_SYSTEM_MESSAGE' })),
      ...(options.rpcError ? [] : [error]),
    ] },
    promptError: options.rpcError,
    promptPlan: options.promptPlan,
  }
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  const db = new DatabaseSync(join(config, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)')
    .run(RAW, 'CASCADE_RUN_STATUS_IDLE', options.initialCount ?? snapshot.trajectory.steps.length, 'Error example', JSON.stringify([`file://${home}`]), new Date().toISOString())
  const state = (count) => db.prepare('UPDATE conversation_summaries SET step_count = ?, last_modified_time = ? WHERE conversation_id = ?').run(count, new Date().toISOString(), RAW)
  const binary = join(home, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${home}' "$@"\n`)
  chmodSync(binary, 0o700)
  const node = await startFakeNode(t)
  const runtimeOptions = { home, agyDir: config, agyBin: binary, personadBin: binary, socketPath: node.path, log: () => {},
    transient401RetryDelayMs: options.retryDelay ?? 10, exportPollMs: 20 }
  let runtime = createAgyRuntime(runtimeOptions)
  t.after(async () => { await runtime.stop(); db.close(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  const row = () => node.last('runtime.sessions')?.sessions.find((entry) => entry.session_id === SESSION)
  await until('session listed', row)
  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const items = () => node.peer.call('session.items', { session_id: SESSION })
  const refresh = async () => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: SESSION, title: 'Refreshed' })
    await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
  }
  const restart = async () => {
    await runtime.stop()
    const before = node.all('runtime.sessions').length
    runtime = createAgyRuntime(runtimeOptions)
    runtime.start()
    await until('restarted inventory', () => node.all('runtime.sessions').length > before)
  }
  return { node, row, calls, items, write, state, refresh, restart, transcript, nativeFile, idle: () => runtime.idle() }
}

for (const exported of [false, true]) {
  test(`retries one transient 401 ${exported ? 'recorded in the transcript' : 'returned by ACP'} without duplicating the user message`, async (t) => {
    const { node, row, calls, items, idle } = await fixture(t, { initialCount: 1, promptPlan: [
      { error: TRANSIENT_401, export: exported }, { reply: 'Finished.', export: exported },
    ] })
    const send = (text, client_msg_id) => node.peer.call('session.send', { session_id: SESSION, text, client_msg_id })
    await send('Continue.', 'request-1')
    await until('retry succeeds', () => row()?.last_text === 'Finished.' && idle())
    assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 2)
    assert.equal(row().turn_error, undefined)
    const page = await items()
    const users = page.items.filter((item) => item.role === 'user')
    assert.deepEqual(users.map((item) => item.text), [input.content, 'Continue.'])
    assert.equal(users.at(-1).client_msg_id, 'request-1')
    assert.equal(page.items.some((item) => item.text?.includes('UNAUTHENTICATED')), false)
    if (exported) {
      await send('Continue.', 'request-2')
      await until('next send settles', idle)
      assert.equal((await items()).items.filter((item) => item.text === 'Continue.').length, 2)
    }
  })
}

for (const [reason, plan, attempts, retryDelay] of [
  ['revoked login', [{ error: `${TRANSIENT_401}; invalid_grant` }], 1],
  ['assistant text', [{ error: TRANSIENT_401, reply: 'Partial answer' }], 1],
  ['tool work', [{ error: TRANSIENT_401, tool: true }], 1],
  ['tool work received during the delay', [{ error: TRANSIENT_401, lateTool: 30 }], 1, 250],
  ['a second transient failure', [{ error: TRANSIENT_401 }, { error: TRANSIENT_401 }], 2],
]) {
  test(`does not retry after ${reason}`, async (t) => {
    const { node, row, calls } = await fixture(t, { initialCount: 1, promptPlan: plan, retryDelay })
    await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.' })
    await until('failure published', () => row()?.turn_error?.startsWith(TRANSIENT_401))
    assert.equal(calls().filter((call) => call.method === 'session/prompt').length, attempts)
  })
}

test('interrupt aborts the transient 401 delay and settles the turn', async (t) => {
  const { node, row, calls, idle } = await fixture(t, { initialCount: 1, retryDelay: 60_000,
    promptPlan: [{ error: TRANSIENT_401 }, { reply: 'Next.' }] })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.' })
  await until('first prompt sent', () => calls().some((call) => call.method === 'session/prompt'))
  assert.equal((await node.peer.call('session.interrupt', { session_id: SESSION })).settled, true)
  assert.equal(idle(), true)
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 1)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Try again.' })
  await until('next send completes', () => row()?.last_text === 'Next.')
})

for (const assistant of [false, true]) {
  test(`assistant sends use assistant MCP after ${assistant ? 'assistant' : 'ordinary'} modes`, async (t) => {
    const { node, calls } = await fixture(t, { initialCount: 1 })
    const modes = (flags = {}) => node.peer.call('session.modes', { session_id: SESSION, permission_mode: 'default', ...flags })
    const resumes = () => calls().filter((call) => call.method === 'session/resume').map((call) => call.params.mcpServers[0])
    const assistantMcp = (server) => server.env.some(({ name, value }) => name === 'PERSONA_ASSISTANT' && value === '1')
    await modes({ assistant })
    await modes()
    assert.deepEqual(resumes().map(assistantMcp), [assistant])
    await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.', assistant: true })
    await modes()
    assert.deepEqual(resumes().map(assistantMcp), assistant ? [true] : [false, true])
    assert.match(resumes().at(-1).args[0], /mcp-proxy\.mjs$/)
  })
}

test('runtime recovers an omitted native error through a cached bounded tail without resuming the session', async (t) => {
  const { row, calls, items, refresh, restart, transcript } = await fixture(t)
  const original = readFileSync(transcript)
  const page = await items()
  assert.match(page.history, /:full:api-errors(?:$|:)/)
  assert.equal(page.to_seq, 2)
  assert.equal(page.items.at(-1).text, `API Error: ${MESSAGE}`)
  await until('quota fields published', () => row()?.turn_error_kind === 'usage_limit')
  assert.equal(row().turn_error_resets_at, '2026-01-03T18:00:00.000Z')
  await Promise.all([items(), items(), items()])
  await refresh()
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 1)
  assert.deepEqual(calls().find((call) => call.method === 'GetCascadeTrajectorySteps').params, { cascadeId: RAW, stepOffset: 1, disableRehydration: true })
  assert.equal(calls().filter((call) => call.method === 'session/list').length, 1)
  assert.equal(calls().some((call) => ['session/resume', 'session/prompt'].includes(call.method)), false)
  assert.deepEqual(readFileSync(transcript), original)
  await restart()
  assert.equal((await items()).items.at(-1).text, `API Error: ${MESSAGE}`)
  await until('quota recovered after restart', () => row()?.turn_error === MESSAGE)
})

test('new user input and successful response clear the old native failure', async (t) => {
  const { row, items, write, state, refresh } = await fixture(t)
  await items()
  await until('initial error', () => row()?.turn_error === MESSAGE)
  write([input, { ...input, step_index: 2, created_at: '2026-01-02T12:01:00Z', content: 'Continue.' },
    { step_index: 3, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-01-02T12:01:01Z', content: 'Finished.' }])
  state(4)
  await refresh()
  assert.equal(row().turn_error, undefined)
  assert.equal(row().last_text, 'Finished.')
  assert.equal((await items()).items.some((item) => item.kind === 'notice'), false)
})

test('native recovery reads only the final eight steps of a large export gap', async (t) => {
  const { calls, items } = await fixture(t, { padding: 20 })
  const page = await items()
  assert.equal(page.to_seq, 22)
  assert.equal(page.items.at(-1).text, `API Error: ${MESSAGE}`)
  assert.equal(calls().find((call) => call.method === 'GetCascadeTrajectorySteps').params.stepOffset, 14)
})

test('rewinding a failed turn removes its recovered error', async (t) => {
  const { node, row, items } = await fixture(t)
  await items()
  await until('initial error', () => row()?.turn_error === MESSAGE)
  await node.peer.call('session.rewind', { session_id: SESSION, prompt: input.content, occurrence: 1, prompts_after: 0, restore: 'conversation' })
  assert.deepEqual((await items()).items, [])
  await until('rewound inventory', () => row()?.turn_error === undefined)
})

test('a rejected ACP prompt publishes its failure through the normal session row', async (t) => {
  const { node, row, write, state, refresh } = await fixture(t, { rpcError: 'Upstream connection closed' })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.' })
  await until('RPC failure published', () => row()?.turn_error === 'Upstream connection closed')
  assert.notEqual(row().status, 'working')
  write([input, { ...input, step_index: 1, created_at: '2026-01-02T12:01:00Z', content: 'Another request.' }])
  state(2)
  await refresh()
  assert.equal(row().turn_error, undefined)
})


test('listing idle sessions does not start native error probes', async (t) => {
  const { calls, refresh } = await fixture(t)
  await refresh()
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 0)
})

test('a pending native error probe does not hold a completed prompt active', async (t) => {
  const { node, row, calls, items, state, refresh, nativeFile } = await fixture(t, { holdErrors: true, initialCount: 1 })
  const release = () => {
    const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
    snapshot.holdErrors = false
    writeFileSync(nativeFile, JSON.stringify(snapshot))
  }
  state(2)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.' })
  await until('completion probes the native tail', () => calls().some((call) => call.method === 'GetCascadeTrajectorySteps'))
  try {
    await refresh()
    assert.notEqual(row().status, 'working', 'native error reads must not hold a completed prompt active')
  } finally {
    release()
  }
  await items()
})
