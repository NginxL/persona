import assert from 'node:assert/strict'
import { appendFileSync, chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const RAW = '11111111-2222-4333-8444-555555555555'
const SESSION = `agy-${RAW}`
const FAKE = fileURLToPath(new URL('./fake-acp-rewind.mjs', import.meta.url))
const at = '2026-01-01T00:00:00Z'
const records = [
  { step_index: 0, type: 'USER_INPUT', content: 'Repeat' },
  { step_index: 1, type: 'PLANNER_RESPONSE', content: 'First answer' },
  { step_index: 2, type: 'USER_INPUT', content: 'Repeat' },
  { step_index: 3, type: 'PLANNER_RESPONSE', content: 'Second answer' },
].map((step) => ({ ...step, status: 'DONE', created_at: at }))
const nativeRecord = (step) => ({
  type: `CORTEX_STEP_TYPE_${step.type}`,
  metadata: { source: step.type === 'USER_INPUT' ? 'CORTEX_STEP_SOURCE_USER_EXPLICIT' : 'MODEL', createdAt: step.created_at },
  ...(step.type === 'USER_INPUT' ? { userInput: { items: [{ text: step.content }] } } : {}),
  ...(step.type === 'PLANNER_RESPONSE' ? { plannerResponse: { response: step.content } } : {}),
})

async function fixture(t, extra = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-rewind-'))
  const config = join(home, 'config')
  const logs = join(config, 'brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  const transcript = join(logs, 'transcript_full.jsonl')
  writeFileSync(transcript, records.map((r) => JSON.stringify(r)).join('\n') + '\n')
  writeFileSync(join(home, 'calls.jsonl'), '')
  const nativeFile = join(home, 'trajectory.json')
  const snapshot = {
    status: 'CASCADE_RUN_STATUS_IDLE', trajectory: { cascadeId: RAW, steps: records.map(nativeRecord) }, ...extra,
  }
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  const db = new DatabaseSync(join(config, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, 4, ?, ?, ?)').run(RAW, 'CASCADE_RUN_STATUS_IDLE', 'Rewind example', JSON.stringify([`file://${home}`]), new Date().toISOString())
  const binary = join(home, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${home}' "$@"\n`)
  chmodSync(binary, 0o700)
  const node = await startFakeNode(t)
  const options = { home, agyDir: config, agyBin: binary, socketPath: node.path, log: () => {} }
  let runtime = createAgyRuntime(options)
  t.after(async () => { await runtime.stop(); db.close(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  await until('rewind inventory', () => node.last('runtime.sessions')?.sessions.length)
  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const rewind = (extra = {}) => node.peer.call('session.rewind', { session_id: SESSION, prompt: 'Repeat', occurrence: 1, prompts_after: 0, restore: 'conversation', ...extra })
  const restart = async () => {
    await runtime.stop()
    const before = node.all('runtime.sessions').length
    runtime = createAgyRuntime(options)
    runtime.start()
    await until('restarted inventory', () => node.all('runtime.sessions').length > before)
  }
  return { node, runtime, home, transcript, db, nativeFile, calls, rewind, restart }
}

test('ACP updates during rewind cannot start autonomous work or restore the old tail', async (t) => {
  const { node, home, rewind, runtime } = await fixture(t, { holdRevert: true })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: records.length })
  const pending = rewind()
  try {
    await until('rewind updates delivered', () => node.last('runtime.sessions')?.sessions[0]?.title === 'Rewind in progress')
    assert.notEqual(node.last('runtime.sessions').sessions[0].status, 'working')
    assert.ok(!node.all('session.delta').some((delta) => delta.text === 'Stale reply.'))
  } finally {
    writeFileSync(join(home, 'release-revert'), '')
    await pending
  }
  assert.equal(runtime.idle(), true)
  const final = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(final.items.map((item) => item.text), ['Repeat', 'First answer'])
})

test('native rewind preserves the prefix, resets watched history and persists through replacement exports', async (t) => {
  const { node, transcript, rewind, calls, db } = await fixture(t)
  assert.equal(node.hello.caps.rewind, true)
  const initial = await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: initial.to_seq })
  assert.equal((await rewind()).input_cleared, true)
  const native = calls().find((c) => c.method === 'RevertToCascadeStep')
  assert.deepEqual(native.params, { cascadeId: RAW, stepIndex: 1, conversationOnly: true, suppressErrorStep: true })
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items.map((i) => i.text), ['Repeat', 'First answer'])
  assert.notEqual(page.history, initial.history)
  await until('history reset pushed', () => node.all('session.items').some((batch) => batch.history === page.history))
  const result = await rewind({ occurrence: 1, prompts_after: 0 })
  assert.equal(result.prompt, 'Repeat')
  const empty = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(empty.items, [])
  assert.equal(empty.to_seq, 0)
  assert.notEqual(empty.history, page.history)
  appendFileSync(transcript, [
    { step_index: 0, type: 'USER_INPUT', content: 'New branch', created_at: new Date(Date.now() + 10).toISOString(), status: 'DONE' },
    { step_index: 1, type: 'PLANNER_RESPONSE', content: 'New answer', created_at: new Date(Date.now() + 10).toISOString(), status: 'DONE' },
  ].map((s) => JSON.stringify(s)).join('\n') + '\n')
  db.exec('UPDATE conversation_summaries SET step_count = 2')
  const replacement = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(replacement.items.map((i) => i.text), ['New branch', 'New answer'])
  assert.notEqual(replacement.history, empty.history)
  assert.equal((await node.peer.call('session.items', { session_id: SESSION })).history, replacement.history)
})

test('rewound identical inputs and replacement replies cannot reuse earlier item identities', async (t) => {
  const { node, transcript, rewind, db } = await fixture(t)
  const initial = await node.peer.call('session.items', { session_id: SESSION })
  await rewind()
  // Native exports append the repeated input at its old index, potentially with the same timestamp.
  appendFileSync(transcript, [records[2], { ...records[3], content: 'Replacement answer' }]
    .map((step) => JSON.stringify(step)).join('\n') + '\n')
  db.exec('UPDATE conversation_summaries SET step_count = 4')
  const replacement = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(replacement.items.slice(0, 2), initial.items.slice(0, 2), 'retained prefix stays stable')
  assert.equal(replacement.items[2].text, initial.items[2].text)
  assert.equal(replacement.items[2].seq, initial.items[2].seq)
  assert.notEqual(replacement.items[2].id, initial.items[2].id, 'a pending send must not inherit the old echo binding')
  assert.notEqual(replacement.items[3].id, initial.items[3].id, 'the old expanded reply must not match the new reply')
  const full = await node.peer.call('session.item', { session_id: SESSION, item_id: replacement.items[3].id })
  assert.equal(full.text, 'Replacement answer')
  await assert.rejects(node.peer.call('session.item', { session_id: SESSION, item_id: initial.items[3].id }), /not found/)

  appendFileSync(transcript, JSON.stringify({ ...records[3], content: 'Completed replacement answer' }) + '\n')
  const completed = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(completed.items[3].id, replacement.items[3].id, 'same-step completion keeps its identity')
  assert.equal(completed.items[3].text, 'Completed replacement answer')

  appendFileSync(transcript, [records[2], { ...records[3], content: 'Completed replacement answer' }]
    .map((step) => JSON.stringify(step)).join('\n') + '\n')
  const repeated = await node.peer.call('session.items', { session_id: SESSION })
  assert.notEqual(repeated.items[2].id, completed.items[2].id, 'another identical replacement is another send')
  assert.notEqual(repeated.items[3].id, completed.items[3].id)
  assert.deepEqual(repeated.items.slice(0, 2), initial.items.slice(0, 2))
})

test('complete export replacement also distinguishes new steps at reused indices', async (t) => {
  const { node, transcript, db } = await fixture(t)
  const initial = await node.peer.call('session.items', { session_id: SESSION })
  const next = records.map((step, index) => index < 2 ? step : { ...step, created_at: '2026-01-02T00:00:00Z' })
  writeFileSync(transcript, next.map((step) => JSON.stringify(step)).join('\n') + '\n')
  db.exec('UPDATE conversation_summaries SET step_count = 4')
  const replaced = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(replaced.items.slice(0, 2), initial.items.slice(0, 2))
  assert.notEqual(replaced.items[2].id, initial.items[2].id)
  assert.notEqual(replaced.items[3].id, initial.items[3].id)
})

test('moved prompts and native busy state refuse before mutation', async (t) => {
  const { rewind, calls, nativeFile } = await fixture(t)
  await assert.rejects(rewind({ occurrence: 2, prompts_after: 0 }), (e) => e.code === 'rewind_mismatch')
  await assert.rejects(rewind({ restore: 'code_and_conversation' }), (e) => e.code === 'rewind_unsupported')
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, status: 'CASCADE_RUN_STATUS_RUNNING' }))
  await assert.rejects(rewind(), (e) => e.code === 'rewind_busy')
  assert.equal(calls().filter((c) => c.method === 'RevertToCascadeStep').length, 0)
})

test('a native failure preserves visible history and remains retryable', async (t) => {
  const { node, rewind, nativeFile } = await fixture(t, { fail: true })
  const initial = await node.peer.call('session.items', { session_id: SESSION })
  await assert.rejects(rewind(), /Native rewind refused/)
  const after = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(after, initial)
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, fail: false }))
  assert.equal((await rewind()).input_cleared, true)
})

test('plan-mode messages match their original native input', async (t) => {
  const { rewind, transcript } = await fixture(t)
  const plan = records.map((step) => step.step_index === 2 ? { ...step, content: '/plan Repeat' } : step)
  writeFileSync(transcript, plan.map((s) => JSON.stringify(s)).join('\n') + '\n')
  assert.equal((await rewind({ prompt: '/plan Repeat' })).input_cleared, true)
})


test('a companion restart verifies a native cut whose timestamp precedes the abandoned export', async (t) => {
  const { node, rewind, db, restart } = await fixture(t)
  await rewind()
  db.prepare('UPDATE conversation_summaries SET last_modified_time = ?').run('2025-01-01T00:00:00Z')
  await restart()
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items.map((i) => i.text), ['Repeat', 'First answer'])
  assert.ok(page.history.endsWith(':cut:2'))
})

test('a companion restart preserves an empty native trajectory with no steps field', async (t) => {
  const { node, rewind, db, restart } = await fixture(t)
  await rewind({ occurrence: 2, prompts_after: 1 })
  db.prepare('UPDATE conversation_summaries SET last_modified_time = ?').run('2025-01-01T00:00:00Z')
  await restart()
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items, [])
  assert.ok(page.history.endsWith(':cut:0'))
})

const later = '2026-01-01T00:01:00Z'
const texts = (page) => page.items.map((item) => item.text)

test('a sent turn appends to the history, its export changes it once and a rewind still replaces it (#12014)', async (t) => {
  const { node, transcript, db, nativeFile, rewind } = await fixture(t, { promptPlan: [{ reply: 'Third answer' }] })
  const idle = await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: idle.to_seq })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('the settled reply is pushed', () => node.last('session.items')?.items.some((item) => item.text === 'Third answer'))
  for (const batch of node.all('session.items')) {
    assert.equal(batch.history, idle.history, 'the send and its reply append')
    assert.ok(batch.from_seq >= idle.to_seq)
  }

  const turn = [{ step_index: 4, type: 'USER_INPUT', content: 'Third' }, { step_index: 5, type: 'PLANNER_RESPONSE', content: 'Third answer' }]
    .map((step) => ({ ...step, status: 'DONE', created_at: later }))
  appendFileSync(transcript, turn.map((step) => JSON.stringify(step)).join('\n') + '\n')
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  snapshot.trajectory.steps.push(
    { type: 'CORTEX_STEP_TYPE_USER_INPUT', metadata: { source: 'CORTEX_STEP_SOURCE_USER_EXPLICIT' }, userInput: { items: [{ text: 'Third' }] } },
    { type: 'CORTEX_STEP_TYPE_PLANNER_RESPONSE', metadata: { source: 'MODEL' } })
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  db.exec('UPDATE conversation_summaries SET step_count = 6')
  await until('the export replaces the projected turn', () => node.last('session.items')?.history !== idle.history)
  const exported = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(exported), [...records.map((step) => step.content), 'Third', 'Third answer'])
  assert.equal(node.last('session.items').history, exported.history)
  assert.equal(node.last('session.items').from_seq, 0)

  await rewind({ prompt: 'Third' })
  const rewound = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(rewound), records.map((step) => step.content))
  assert.notEqual(rewound.history, exported.history)
  await until('the rewind is pushed', () => node.last('session.items')?.history === rewound.history)
})

test('a restart changes the history only when projected steps were outstanding (#12014)', async (t) => {
  const { node, home, restart } = await fixture(t, { promptPlan: [{ reply: 'Third answer' }] })
  const items = () => node.peer.call('session.items', { session_id: SESSION })
  const idle = await items()
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('the reply waits for its export', async () => texts(await items()).includes('Third answer'))
  assert.equal((await items()).history, idle.history)
  const epoch = join(home, 'config/brain', RAW, '.persona-history-epoch')
  if (process.platform !== 'win32') assert.equal(statSync(epoch).mode & 0o777, 0o600)

  await restart()
  const restarted = await items()
  assert.notEqual(restarted.history, idle.history, 'clients drop the projected steps they hold')
  assert.deepEqual(texts(restarted), records.map((step) => step.content))
  await restart()
  assert.equal((await items()).history, restarted.history, 'nothing was outstanding')
})

test('a restart with an unreadable history epoch does not reuse the history of projected steps (#12014)', {
  skip: process.platform === 'win32' || process.getuid?.() === 0,
}, async (t) => {
  const { node, home, restart } = await fixture(t, { promptPlan: [{ reply: 'Third answer' }] })
  const items = () => node.peer.call('session.items', { session_id: SESSION })
  const idle = await items()
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('the reply waits for its export', async () => texts(await items()).includes('Third answer'))
  const sent = await items()
  assert.equal(sent.history, idle.history)
  const epoch = join(home, 'config/brain', RAW, '.persona-history-epoch')
  chmodSync(epoch, 0o000)
  try {
    await restart()
    assert.notEqual((await items()).history, sent.history, 'clients drop the projected steps they hold')
  } finally {
    chmodSync(epoch, 0o600)
  }
})

test('a send changes the history at once when its brain directory is read-only (#12014)', {
  skip: process.platform === 'win32' || process.getuid?.() === 0,
}, async (t) => {
  const { node, home, restart } = await fixture(t, { promptPlan: [{ reply: 'Third answer' }] })
  const items = () => node.peer.call('session.items', { session_id: SESSION })
  const brain = join(home, 'config/brain', RAW)
  const idle = await items()
  chmodSync(brain, 0o500)
  try {
    await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
    await until('the reply waits for its export', async () => texts(await items()).includes('Third answer'))
    const sent = await items()
    assert.notEqual(sent.history, idle.history, 'no restart can report the history of these projected steps')
    assert.equal(existsSync(join(brain, '.persona-history-epoch')), false)
    await restart()
    assert.notEqual((await items()).history, sent.history)
  } finally {
    chmodSync(brain, 0o700)
  }
})

test('an inventory refresh revalidates an existing cut after native work advances', async (t) => {
  const { node, transcript, rewind, db, nativeFile, calls } = await fixture(t)
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  const fullTrajectory = [...snapshot.trajectory.steps,
    nativeRecord({ ...records[2], step_index: 4, content: 'New question' }),
    nativeRecord({ ...records[3], step_index: 5, content: 'Recovered answer' })]
  await rewind()
  await until('rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')

  appendFileSync(transcript, [
    { ...records[2], step_index: 4, content: 'New question' },
    { ...records[3], step_index: 5, content: 'Recovered answer' },
  ].map((step) => JSON.stringify(step)).join('\n') + '\n')
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, trajectory: { ...snapshot.trajectory, steps: fullTrajectory } }))
  db.prepare('UPDATE conversation_summaries SET step_count = 6, last_modified_time = ?').run(new Date().toISOString())
  const checks = calls().filter((call) => call.method === 'GetCascadeTrajectory').length
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed' })
  await until('recovered last reply in the inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'Recovered answer')
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectory').length, checks + 1)
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.items.at(-1).text, 'Recovered answer')
})

test('a changed summary cannot expose the abandoned tail while the native trajectory is still rewound', async (t) => {
  const { node, transcript, rewind, db, nativeFile, calls } = await fixture(t)
  await rewind()
  await until('rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  appendFileSync(transcript, [
    { ...records[2], step_index: 4, content: 'Unexported question' },
    { ...records[3], step_index: 5, content: 'Abandoned answer' },
  ].map((step) => JSON.stringify(step)).join('\n') + '\n')
  db.prepare('UPDATE conversation_summaries SET step_count = 6, last_modified_time = ?').run(new Date().toISOString())
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  assert.equal(snapshot.trajectory.steps.length, 2)
  const checks = calls().filter((call) => call.method === 'GetCascadeTrajectory').length
  const before = node.all('runtime.sessions').length
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed' })
  await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items.map((item) => item.text), ['Repeat', 'First answer'])
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectory').length, checks + 1)
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'First answer')
})

test('native progress cannot clear a cut until the replacement export catches up', async (t) => {
  const { node, transcript, rewind, db, nativeFile } = await fixture(t)
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  const replacement = [
    records[0], records[1],
    { ...records[2], content: 'Replacement question', created_at: '2026-01-02T00:00:00Z' },
    { ...records[3], content: 'Replacement answer', created_at: '2026-01-02T00:00:01Z' },
  ]
  await rewind()
  await until('rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, trajectory: { ...snapshot.trajectory, steps: replacement.map(nativeRecord) } }))
  db.prepare('UPDATE conversation_summaries SET step_count = 4, last_modified_time = ?').run(new Date().toISOString())
  const before = node.all('runtime.sessions').length
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed' })
  await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
  const lagging = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(lagging.items.map((item) => item.text), ['Repeat', 'First answer'])
  assert.ok(lagging.history.endsWith(':cut:2'))
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'First answer')

  writeFileSync(transcript, replacement.map((step) => JSON.stringify(step)).join('\n') + '\n')
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed again' })
  await until('replacement export is visible', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'Replacement answer')
  const caughtUp = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(caughtUp.items.map((item) => item.text), replacement.map((step) => step.content))
})

test('a stale native check cannot erase a cut installed by a newer rewind', async (t) => {
  const { node, home, transcript, rewind, db, nativeFile } = await fixture(t)
  await rewind()
  await until('first rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  const watching = await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: watching.to_seq })
  const abandoned = [
    { ...records[2], step_index: 4, content: 'Abandoned question' },
    { ...records[3], step_index: 5, content: 'Abandoned answer' },
  ]
  appendFileSync(transcript, abandoned.map((step) => JSON.stringify(step)).join('\n') + '\n')
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, holdTrajectoryOnce: true,
    trajectory: { ...snapshot.trajectory, steps: [...records.map(nativeRecord), ...abandoned.map(nativeRecord)] } }))
  db.prepare('UPDATE conversation_summaries SET step_count = 6, last_modified_time = ?').run(new Date().toISOString())
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Start stale check' })
  await until('stale native check is held', () => existsSync(join(home, 'trajectory-held')))

  await rewind({ occurrence: 1, prompts_after: 1 })
  await until('second rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  writeFileSync(join(home, 'release-trajectory'), '')
  await until('stale native response is sent', () => existsSync(join(home, 'trajectory-response-sent')))
  await new Promise((resolve) => setTimeout(resolve, 100))
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(node.all('session.items').some((event) => event.items?.some((item) => item.text === 'Abandoned answer')), false)
  assert.equal(node.all('runtime.sessions').some((event) => event.sessions?.[0]?.last_text === 'Abandoned answer'), false)
  assert.deepEqual(page.items.map((item) => item.text), records.slice(0, 2).map((step) => step.content))
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'First answer')
})

test('a stale check cannot publish while a newer rewind is installing its cut', async (t) => {
  const { node, home, transcript, rewind, db, nativeFile } = await fixture(t)
  await rewind()
  await until('first rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  const watching = await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: watching.to_seq })
  const abandoned = [
    { ...records[2], step_index: 4, content: 'Abandoned question' },
    { ...records[3], step_index: 5, content: 'Abandoned answer' },
  ]
  appendFileSync(transcript, abandoned.map((step) => JSON.stringify(step)).join('\n') + '\n')
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, holdTrajectoryOnce: true,
    trajectory: { ...snapshot.trajectory, steps: [...records.map(nativeRecord), ...abandoned.map(nativeRecord)] } }))
  db.prepare('UPDATE conversation_summaries SET step_count = 6, last_modified_time = ?').run(new Date().toISOString())
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Start stale check' })
  await until('stale native check is held', () => existsSync(join(home, 'trajectory-held')))

  const armed = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...armed, holdAfterRevertOnce: true }))
  const second = rewind({ occurrence: 1, prompts_after: 1 })
  await until('second rewind is installing its cut', () => existsSync(join(home, 'after-trajectory-held')))
  writeFileSync(join(home, 'release-trajectory'), '')
  await until('stale native response is sent', () => existsSync(join(home, 'trajectory-response-sent')))
  await new Promise((resolve) => setTimeout(resolve, 100))
  writeFileSync(join(home, 'release-after-trajectory'), '')
  await second
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(node.all('session.items').some((event) => event.items?.some((item) => item.text === 'Abandoned answer')), false)
  assert.equal(node.all('runtime.sessions').some((event) => event.sessions?.[0]?.last_text === 'Abandoned answer'), false)
  assert.deepEqual(page.items.map((item) => item.text), records.slice(0, 2).map((step) => step.content))
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'First answer')
})
