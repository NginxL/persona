import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { transcriptStatus } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const ID = '11111111-2222-3333-4444-555555555555'
const SESSION = `agy-${ID}`
const at = '2026-01-02T11:59:00Z'
const step = (type, fields = {}) => ({ step_index: 0, type, status: 'DONE', created_at: at, ...fields })
const idle = { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 1 }

test('native transcript stages distinguish external work from completed replies', () => {
  const cases = [
    ['user message alone', [step('USER_INPUT')], 'unknown'],
    ['unfinished response alone', [step('PLANNER_RESPONSE', { status: 'RUNNING', content: 'Reading' })], 'unknown'],
    ['tool request alone', [step('PLANNER_RESPONSE', { content: 'Running tests', tool_calls: [{ name: 'run_command' }] })], 'unknown'],
    ['completed tool alone', [step('GENERIC', { content: 'Tests passed' })], 'unknown'],
    ['final reply', [step('PLANNER_RESPONSE', { content: 'Finished' })], 'done', idle],
    ['metadata after final reply', [step('PLANNER_RESPONSE', { content: 'Finished' }), step('CHECKPOINT')], 'done', idle],
    ['progress narration while running', [step('PLANNER_RESPONSE', { content: 'Running the experiment; results will follow.' })], 'working', { status: 'CASCADE_RUN_STATUS_RUNNING' }],
    ['background work while idle', [step('PLANNER_RESPONSE', { content: 'Running the experiment' })], 'working', { ...idle, not_fully_idle: 1 }],
    ['killed conversation', [step('PLANNER_RESPONSE', { content: 'Running the experiment' })], 'idle', { status: 'CASCADE_RUN_STATUS_RUNNING', killed: 1 }],
    ['reply without native state', [step('PLANNER_RESPONSE', { content: 'Running the experiment' })], 'unknown'],
    ['unrecognized native state', [step('PLANNER_RESPONSE', { content: 'Running the experiment' })], 'unknown', { status: 'FUTURE_STATUS' }],
    ['idle summary predates narration', [step('PLANNER_RESPONSE', { step_index: 1, content: 'Running the experiment' })], 'unknown', idle],
    ['idle summary predates user input', [step('USER_INPUT', { step_index: 1 })], 'unknown', idle],
    ['stopped after a tool result', [step('GENERIC')], 'idle', idle],
    ['error', [step('ERROR_MESSAGE')], 'idle'],
    ['interrupted step', [step('GENERIC', { status: 'CANCELLED' })], 'idle'],
    ['empty history', [], 'unknown'],
    ['unfinished history without a time', [step('GENERIC', { created_at: undefined })], 'unknown'],
    ['stale unfinished history', [step('USER_INPUT', { created_at: '2025-12-01T00:00:00Z' })], 'unknown'],
    ['old completed reply', [step('PLANNER_RESPONSE', { content: 'Finished', created_at: '2025-12-01T00:00:00Z' })], 'done', idle],
  ]
  for (const [name, steps, expected, summary] of cases) {
    assert.equal(transcriptStatus(steps, summary), expected, name)
  }
})

async function fixture(t, steps, summary, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'persona-agy-status-'))
  const agyDir = join(home, 'agy')
  const logs = join(agyDir, 'brain', ID, '.system_generated', 'logs')
  mkdirSync(agyDir, { recursive: true })
  const transcript = join(logs, 'transcript.jsonl')
  const write = (values) => {
    mkdirSync(logs, { recursive: true })
    writeFileSync(transcript, values.map((value, index) => JSON.stringify({ ...value, step_index: index })).join('\n') + '\n')
  }
  if (!options.noTranscript) write(steps)
  if (options.oldTranscript) {
    const old = new Date(Date.now() - 30 * 86400_000)
    utimesSync(transcript, old, old)
  }
  if (options.internal) {
    mkdirSync(join(agyDir, 'cache'), { recursive: true })
    writeFileSync(join(agyDir, 'cache', 'conversation_metadata.json'), JSON.stringify({ conversations: { [ID]: { is_internal: true } } }))
  }
  const summaryFile = join(agyDir, 'conversation_summaries.db')
  let db
  const state = (value) => {
    if (!db) {
      db = new DatabaseSync(summaryFile)
      db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle NUMERIC, killed NUMERIC, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    }
    db.prepare('INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, ?, ?, ?, ?, ?, ?)')
      .run(ID, value.status, value.not_fully_idle ?? 0, value.killed ?? 0, value.step_count ?? steps.length,
        value.title ?? '', value.workspace_uris ?? '[]', value.last_modified_time ?? '')
  }
  if (summary) state(summary)
  const binary = join(home, 'fake-agy.mjs')
  writeFileSync(binary, `#!/usr/bin/env node
if (process.argv.includes('models')) { console.log('test-model\\tTest Model'); process.exit(0) }
import { createInterface } from 'node:readline'
createInterface({ input: process.stdin }).on('line', (line) => {
  const request = JSON.parse(line)
  if (request.id === undefined || request.method === 'session/prompt') return
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result: {} }) + '\\n')
})
`, { mode: 0o700 })
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ home, agyDir, agyBin: binary, socketPath: node.path, listDays: 1, maxSessions: 1, log: () => {} })
  t.after(async () => {
    await runtime.stop()
    db?.close()
    rmSync(home, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready
  await until('native conversation listed', () => options.expectEmpty ? node.last('runtime.sessions')?.sessions.length === 0 : node.last('runtime.sessions')?.sessions.length)
  const refresh = async () => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed' })
    await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
    return node.last('runtime.sessions').sessions[0]?.status
  }
  return { node, write, state, refresh, summaryFile, database: () => db }
}

test('runtime inventory follows an external tool turn through its final reply', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node, write, state, refresh } = await fixture(t, [step('GENERIC', { created_at, content: 'Tool output' })], { status: 'CASCADE_RUN_STATUS_RUNNING' })
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'working')
  write([step('PLANNER_RESPONSE', { created_at, content: 'Finished' })])
  state(idle)
  assert.equal(await refresh(), 'done')
})

test('runtime inventory never presents a user question as an unanswered reply', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const previous = step('PLANNER_RESPONSE', { created_at, content: 'Which configuration should I use?' })
  const { node, write, state, refresh } = await fixture(t, [previous], idle)
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, previous.content)
  const turn = [
    previous,
    step('USER_INPUT', { created_at, source: 'USER_EXPLICIT', content: 'What were the results?' }),
    step('PLANNER_RESPONSE', { created_at, thinking: 'Inspect the logs.', tool_calls: [{ name: 'run_command' }] }),
    step('GENERIC', { created_at, content: 'Retry the command?' }),
  ]
  write(turn)
  state({ ...idle, step_count: turn.length })
  assert.equal(await refresh(), 'idle')
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, '')

  const reply = step('PLANNER_RESPONSE', { created_at, content: 'Should I retry with the other configuration?' })
  write([...turn, reply])
  state({ ...idle, step_count: turn.length + 1 })
  assert.equal(await refresh(), 'done')
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, reply.content)
})

test('owned ACP turn stays working while the native transcript has the previous reply', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node } = await fixture(t, [step('PLANNER_RESPONSE', { created_at, content: 'Previous reply' })], idle)
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'done')
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue', client_msg_id: 'owned-turn' })
  await until('owned ACP turn running', () => node.last('runtime.sessions')?.sessions[0]?.status === 'working')
})

test('native running state keeps narration working until the conversation becomes idle', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const narration = step('PLANNER_RESPONSE', { created_at, content: 'Running the experiment; results will follow.' })
  const { node, write, state, refresh, summaryFile } = await fixture(t, [narration], { status: 'CASCADE_RUN_STATUS_RUNNING' })
  const original = readFileSync(summaryFile)
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'working')
  assert.equal(await refresh(), 'working', 'the narration alone does not complete the turn')
  assert.deepEqual(readFileSync(summaryFile), original, 'inventory leaves the native database unchanged')
  write([narration, step('GENERIC', { created_at, content: 'Tool output' })])
  assert.equal(await refresh(), 'working')
  write([narration, step('GENERIC', { created_at }), step('PLANNER_RESPONSE', { created_at, content: 'Finished' })])
  state({ ...idle, step_count: 3 })
  assert.equal(await refresh(), 'done')
  write([narration, step('GENERIC', { created_at }), step('PLANNER_RESPONSE', { created_at, content: 'Finished' }), step('USER_INPUT', { created_at })])
  assert.equal(await refresh(), 'unknown', 'a stale idle summary does not establish a new execution')
})

test('old completed user input with no running state stays unknown', { timeout: 15_000 }, async (t) => {
  const created_at = new Date(Date.now() - 8 * 3600_000).toISOString()
  const { node, state, refresh } = await fixture(t, [step('USER_INPUT', { created_at })], { status: '' })
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'unknown')
  assert.equal(await refresh(), 'unknown')
  state({ status: 'CASCADE_RUN_STATUS_RUNNING' })
  assert.equal(await refresh(), 'working')
  state({ status: '' })
  assert.equal(await refresh(), 'unknown')
})

test('native running conversations are discovered before transcript export', { timeout: 15_000 }, async (t) => {
  const { node, state, refresh } = await fixture(t, [], {
    status: 'CASCADE_RUN_STATUS_RUNNING', title: 'External experiment', workspace_uris: '["file:///work/example"]',
  }, { noTranscript: true })
  const row = node.last('runtime.sessions').sessions[0]
  assert.equal(row.session_id, SESSION)
  assert.equal(row.status, 'working')
  assert.equal(row.title, 'External experiment')
  assert.equal(row.cwd, '/work/example')
  state(idle)
  assert.equal(await refresh(), undefined, 'an inactive conversation without a transcript leaves the live inventory')
})

for (const internal of [false, true]) {
  test(`native running state survives history limits with internal metadata ${internal}`, { timeout: 15_000 }, async (t) => {
    const old = new Date(Date.now() - 30 * 86400_000).toISOString()
    const { node } = await fixture(t, [step('GENERIC', { created_at: old })], {
      status: 'CASCADE_RUN_STATUS_RUNNING', last_modified_time: old, workspace_uris: 'invalid JSON',
    }, { oldTranscript: true, internal })
    assert.equal(node.last('runtime.sessions').sessions[0].status, 'working')
  })
}

test('unavailable native summary stores cannot turn narration into completion', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node, state, refresh, summaryFile, database } = await fixture(t, [step('PLANNER_RESPONSE', { created_at, content: 'Continuing the experiment' })])
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'unknown', 'missing database')
  writeFileSync(summaryFile, 'invalid database')
  assert.equal(await refresh(), 'unknown', 'unreadable database')
  rmSync(summaryFile)
  state(idle)
  const db = database()
  db.exec('BEGIN EXCLUSIVE')
  try {
    assert.equal(await refresh(), 'unknown', 'locked database')
  } finally {
    db.exec('ROLLBACK')
  }
  db.exec('DROP TABLE conversation_summaries; CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT)')
  assert.equal(await refresh(), 'unknown', 'unsupported older schema')
})


test('completed availability workspaces stay hidden after a companion restart', async (t) => {
  const missing = join(tmpdir(), 'persona-check-availability-missing')
  const { node } = await fixture(t, [step('PLANNER_RESPONSE', { content: 'OK' })], {
    ...idle, workspace_uris: JSON.stringify([`file://${missing}`]),
  }, { expectEmpty: true })
  assert.equal(node.last('runtime.sessions').sessions.length, 0)
})

test('a real workspace sharing the check prefix remains visible', async (t) => {
  const real = mkdtempSync(join(tmpdir(), 'persona-check-'))
  t.after(() => rmSync(real, { recursive: true, force: true }))
  const { node } = await fixture(t, [step('PLANNER_RESPONSE', { content: 'Answer' })], {
    ...idle, workspace_uris: JSON.stringify([`file://${real}`]),
  })
  assert.equal(node.last('runtime.sessions').sessions.length, 1)
})

test('lagging summaries never truncate freshly exported steps by timestamp alone', async (t) => {
  const steps = [step('USER_INPUT', { source: 'USER_EXPLICIT', content: 'Question' }), step('PLANNER_RESPONSE', { content: 'Answer' })]
  const { node, state, refresh } = await fixture(t, steps, { ...idle, last_modified_time: '2026-01-03T00:00:00Z' })
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'Answer')
  const history = node.last('runtime.sessions').sessions[0].rev
  state({ ...idle, step_count: 2, last_modified_time: '2026-01-03T00:00:01Z' })
  await refresh()
  assert.equal(node.last('runtime.sessions').sessions[0].rev, history)
})
