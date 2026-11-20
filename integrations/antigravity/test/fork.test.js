import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const source = '11111111-2222-4333-8444-555555555555'
const branch = 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee'
const sessionId = `agy-${source}`
const branched = `agy-${branch}`
const fake = fileURLToPath(new URL('./fake-acp-rewind.mjs', import.meta.url))
const records = [
  { type: 'USER_INPUT', content: 'First question' },
  { type: 'PLANNER_RESPONSE', thinking: 'Reasoning', content: 'First draft', tool_calls: [{ name: 'view_file' }] },
  { type: 'GENERIC', content: 'Tool result' },
  { type: 'PLANNER_RESPONSE', content: 'First answer' },
  { type: 'USER_INPUT', content: 'Second question' },
  { type: 'PLANNER_RESPONSE', content: 'Second answer' },
].map((step, step_index) => ({ ...step, step_index, status: 'DONE', created_at: '2026-01-01T00:00:00Z' }))

async function fixture(t, { assistant = false, status = 'IDLE', pending = false, requireLoad = false, holdFork = false } = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-fork-'))
  const cwd = join(home, assistant ? '.persona/health' : 'Example workspace')
  const config = join(home, 'config')
  const logs = join(config, 'brain', source, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  mkdirSync(cwd, { recursive: true })
  const jsonl = (steps) => steps.map((step) => JSON.stringify(step)).join('\n') + '\n'
  const transcript = jsonl(records)
  const transcriptPath = join(logs, 'transcript_full.jsonl')
  writeFileSync(transcriptPath, transcript)
  writeFileSync(join(home, 'calls.jsonl'), '')
  const snapshot = (steps) => writeFileSync(join(home, 'trajectory.json'), JSON.stringify({
    forkId: branch, delayForkExport: assistant || pending, requireLoad, holdFork, status: `CASCADE_RUN_STATUS_${status}`,
    promptReply: pending ? 'Latest answer' : undefined,
    trajectory: { cascadeId: source, steps: steps.map((step) => ({ type: `CORTEX_STEP_TYPE_${step.type}` })) },
  }))
  snapshot(records)
  const db = new DatabaseSync(join(config, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)').run(
    source, 'CASCADE_RUN_STATUS_IDLE', records.length, 'Example chat', JSON.stringify([pathToFileURL(cwd).href]), new Date().toISOString())
  const binary = join(home, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${fake}' '${home}' "$@"\n`)
  chmodSync(binary, 0o700)
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ home, agyDir: config, agyBin: binary, personadBin: binary,
    socketPath: node.path, exportPollMs: 20, exportSettleTimeoutMs: 500, log: () => {} })
  t.after(async () => { await runtime.stop(); db.close(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  await until('source listed', () => node.last('runtime.sessions')?.sessions.some((row) => row.session_id === sessionId))
  const fork = (at_item_id, session_id = sessionId) => node.peer.call('session.fork', { session_id, at_item_id, assistant })
  const items = async (id = sessionId) => (await node.peer.call('session.items', { session_id: id })).items
  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  const latest = [...records,
    { type: 'USER_INPUT', content: 'Latest question' }, { type: 'PLANNER_RESPONSE', thinking: 'Native reasoning' },
    { type: 'PLANNER_RESPONSE', content: 'Latest answer' },
  ].map((step, step_index) => ({ status: 'DONE', created_at: '2026-01-01T00:00:00Z', ...step, step_index }))
  return { node, cwd, fork, items, calls, checkSource: () => assert.equal(readFileSync(transcriptPath, 'utf8'), transcript),
    finishTurn: async () => {
      await node.peer.call('session.send', { session_id: sessionId, text: 'Latest question' })
      await until('prompt finished', () => runtime.idle())
      snapshot(latest)
      return (await items()).find((item) => item.text === 'Latest answer')
    },
    exportLatest: () => writeFileSync(transcriptPath, jsonl(latest)),
    releaseFork: () => writeFileSync(join(home, 'release-fork'), ''),
  }
}

test('native fork keeps the entire conversation in the same workspace', async (t) => {
  const { node, cwd, fork, items, calls, checkSource } = await fixture(t, { requireLoad: true })
  assert.equal(node.hello.caps.fork, true)
  assert.equal((await fork()).session_id, branched)
  assert.deepEqual(calls().find((call) => call.method === 'ForkConversation')?.params, {
    sourceCascadeId: source, forkAtStepIndex: -1,
    targetForkWorkspace: 'FORK_TARGET_CURRENT_WORKSPACE', sourceWorkspaceUris: [pathToFileURL(cwd).href],
  })
  assert.deepEqual(await items(branched), await items())
  checkSource()
})

test('forking at a reply keeps its tool results and stops before the next user message', async (t) => {
  const { fork, items, calls, checkSource } = await fixture(t)
  const reply = (await items()).find((item) => item.text === 'First draft')
  assert.match(reply.id, /^2@[\da-f]+#1$/)
  assert.equal((await fork(reply.id)).session_id, branched)
  assert.equal(calls().find((call) => call.method === 'ForkConversation')?.params.forkAtStepIndex, 3)
  assert.deepEqual((await items(branched)).filter((item) => item.kind === 'text').map((item) => item.text),
    ['First question', 'First draft', 'First answer'])
  checkSource()
})

test('an assistant fork can be opened and continued before the native export arrives', async (t) => {
  const { node, cwd, fork, items, calls } = await fixture(t, { assistant: true, requireLoad: true })
  const result = await fork('4#0')
  assert.equal(result.session_id, branched)
  assert.equal(result.session.cwd, cwd)
  assert.equal(calls().find((call) => call.method === 'ForkConversation')?.params.forkAtStepIndex, 3)
  const listed = await until('assistant fork listed', () => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === branched))
  assert.equal(listed.cwd, cwd)
  assert.equal(listed.status, 'idle')
  assert.equal((await items(branched)).at(-1).text, 'First answer')
  await node.peer.call('session.send', { session_id: branched, text: 'Continue the branch' })
  const sent = await until('branch continued', () => calls().find((call) => call.method === 'session/prompt'))
  assert.equal(sent.params.sessionId, branch)
  for (const id of [source, branch]) {
    const resume = calls().find((call) => call.method === 'session/resume' && call.params.sessionId === id)
    assert.equal(resume?.params.cwd, cwd)
    assert.ok(resume.params.mcpServers[0].env.some(({ name, value }) => name === 'PERSONA_ASSISTANT' && value === '1'))
  }
  await node.peer.call('session.modes', { session_id: sessionId, permission_mode: 'default' })
  assert.equal(calls().filter((call) => call.method === 'session/resume' && call.params.sessionId === source).length, 1)
})

for (const whole of [false, true]) {
  test(`fork ${whole ? 'at the end' : 'at the latest reply'} waits for the completed turn export`, async (t) => {
    const { fork, items, calls, checkSource, finishTurn, exportLatest } = await fixture(t, { pending: true })
    const reply = await finishTurn()
    assert.equal(reply.seq, 8)
    checkSource()
    const result = fork(whole ? '' : reply.id)
    const exported = delay(80).then(() => exportLatest())
    const [forked] = await Promise.all([result, exported])
    assert.equal(forked.session_id, branched)
    assert.equal(calls().find((call) => call.method === 'ForkConversation')?.params.forkAtStepIndex, whole ? -1 : 8)
    assert.notEqual((await items()).at(-1).id, reply.id)
    assert.deepEqual((await items(branched)).slice(-3).map((item) => item.text),
      ['Latest question', 'Native reasoning', 'Latest answer'])
  })
}

test('fork refuses when the completed turn export never arrives', async (t) => {
  const { fork, calls, finishTurn } = await fixture(t, { pending: true })
  await finishTurn()
  await assert.rejects(fork(), (error) => error.code === 'fork_turn_running')
  assert.ok(!calls().some((call) => call.method === 'ForkConversation'))
})

test('a mode change cannot race a native fork', async (t) => {
  const { node, fork, calls, releaseFork } = await fixture(t, { holdFork: true })
  const result = fork()
  try {
    await until('native fork started', () => calls().some((call) => call.method === 'ForkConversation'))
    await assert.rejects(node.peer.call('session.modes', { session_id: sessionId, permission_mode: 'plan' }), /busy/)
  } finally {
    releaseFork()
    await result
  }
})

test('a native running turn refuses to fork', async (t) => {
  const { fork, calls } = await fixture(t, { status: 'RUNNING' })
  await assert.rejects(fork(), (error) => error.code === 'fork_turn_running')
  assert.ok(!calls().some((call) => call.method === 'ForkConversation'))
})

test('unknown, archived and stale targets do not fork', async (t) => {
  const { node, fork, calls } = await fixture(t)
  await assert.rejects(fork('', 'agy-11111111-eeee-4333-8444-555555555555'), (error) => error.code === 'not_found')
  await assert.rejects(fork('4@old#0'), (error) => error.code === 'fork_mismatch')
  await assert.rejects(fork('4#9'), (error) => error.code === 'fork_mismatch')
  await node.peer.call('session.close', { session_id: sessionId })
  await assert.rejects(fork(), (error) => error.code === 'not_found')
  assert.ok(!calls().some((call) => call.method === 'ForkConversation'))
})
