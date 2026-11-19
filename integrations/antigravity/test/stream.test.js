import assert from 'node:assert/strict'
import { appendFileSync, chmodSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { packageHash } from '../../runtime-sdk/lib/package.js'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { PACKAGE_NAME } from '../install.mjs'
import { createAgyRuntime } from '../lib/runtime.js'
import { AgyAcpClient } from '../lib/acp.js'

const FAKE = fileURLToPath(new URL('./fake-acp-stream.mjs', import.meta.url))
const RAW = '11111111-2222-4333-8444-555555555555'
const SESSION = `agy-${RAW}`

async function fixture(t, options = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'agy-stream-'))
  writeFileSync(join(directory, 'updates.jsonl'), '')
  writeFileSync(join(directory, 'calls.jsonl'), '')
  const binary = join(directory, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${directory}' "$@"\n`)
  chmodSync(binary, 0o700)
  mkdirSync(join(directory, 'config/brain', RAW), { recursive: true })
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ agyBin: binary, agyDir: join(directory, 'config'), home: directory, socketPath: node.path, log: () => {}, ...options })
  t.after(async () => { await runtime.stop(); rmSync(directory, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  await node.peer.call('session.start', { cwd: directory })
  await node.peer.call('session.watch', { session_id: SESSION })
  await until('initial session is listed', () => sessionStatus(node))
  const calls = () => readFileSync(join(directory, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const update = (value) => appendFileSync(join(directory, 'updates.jsonl'), `${JSON.stringify(value)}\n`)
  const chunk = async (text, expected = text) => {
    update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } })
    await until(`live text ${expected}`, () => node.last('session.delta')?.text === expected, 15_000)
  }
  const promptCount = (count) => until(`${count} prompts started`, () => calls().filter((r) => r.method === 'session/prompt').length === count, 15_000)
  return { node, directory, calls, update, chunk, promptCount, runtime }
}

test('AGY streams one assistant message at a time across tool calls', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Make the change.' })
  await promptCount(1)
  await chunk('Preparing ')
  await chunk('the change.', 'Preparing the change.')
  update({ sessionUpdate: 'tool_call', toolCallId: 'tool-1', title: 'Read' })
  update({ sessionUpdate: 'tool_call_update', toolCallId: 'tool-1', status: 'completed' })
  await chunk('The change is ')
  await chunk('complete.', 'The change is complete.')
  update({ end: true })
  await until('live text cleared at completion', () => node.last('session.delta')?.text === '', 15_000)
})

test('a queued prompt does not receive the executing prompt text and starts with an empty stream', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First request.' })
  await promptCount(1)
  await chunk('First reply')
  await node.peer.call('session.send', { session_id: SESSION, text: 'Second request.' })
  await chunk(' complete.', 'First reply complete.')
  update({ end: true })
  await promptCount(2)
  await until('queued prompt clears the previous text', () => node.last('session.delta')?.text === '', 15_000)
  await chunk('Second reply.')
  update({ end: true })
  await until('second prompt completes', () => node.last('session.delta')?.text === '', 15_000)
})

test('each prompt clears any live text before its first response', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First request.' })
  await promptCount(1)
  await chunk('First reply.')
  update({ end: true })
  await until('first prompt finishes', () => node.last('session.delta')?.text === '', 15_000)
  const before = node.all('session.delta').length
  await node.peer.call('session.send', { session_id: SESSION, text: 'Second request.' })
  await promptCount(2)
  await until('new prompt publishes its empty boundary', () => node.all('session.delta').length > before, 15_000)
  assert.equal(node.last('session.delta').text, '')
  await chunk('First reply.')
  assert.equal(node.last('session.delta').text, 'First reply.', 'a new turn may legitimately repeat earlier words')
  update({ end: true })
})

test('AGY completion retires the cached reply after the viewer leaves', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First request.' })
  await promptCount(1)
  await chunk('Previous reply.')
  await node.peer.call('session.unwatch', { session_id: SESSION })
  update({ end: true })
  await until('unwatched completion clears the reply', () => node.last('session.delta')?.text === '', 2000)
  await node.peer.call('session.watch', { session_id: SESSION })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Second request.' })
  await promptCount(2)
  assert.equal(node.last('session.delta').text, '')
  await chunk('New reply.')
  update({ end: true })
})

function transcripts(directory, compact, complete) {
  const logs = join(directory, 'config/brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  for (const [file, rows] of [['transcript.jsonl', compact], ['transcript_full.jsonl', complete]]) {
    writeFileSync(join(logs, file), rows.map((row) => JSON.stringify(row)).join('\n') + '\n')
  }
}

const userStep = (index, text) => ({ step_index: index, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', content: text })
const replyStep = (index, text) => ({ step_index: index, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', content: text })

function nativeState(directory, status, stepCount) {
  const db = new DatabaseSync(join(directory, 'config/conversation_summaries.db'))
  try {
    db.exec('CREATE TABLE IF NOT EXISTS conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    db.prepare("INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, 0, 0, ?, '', '[]', '')")
      .run(RAW, `CASCADE_RUN_STATUS_${status}`, stepCount)
  } finally {
    db.close()
  }
}

const sessionStatus = (node) => node.last('runtime.sessions')?.sessions.find((session) => session.session_id === SESSION)?.status

test('autonomous wakes stream work and settle only once native work is idle and exported', async (t) => {
  const { node, directory, calls, update, chunk, runtime } = await fixture(t, { exportPollMs: 20 })
  const prior = [userStep(0, 'Run the workflow.'), replyStep(1, 'Workflow started.')]
  transcripts(directory, prior, prior)
  nativeState(directory, 'IDLE', 2)

  update({ sessionUpdate: 'agent_thought_chunk', content: { text: 'Inspect the result.' } })
  await until('autonomous working status before native activity', () => sessionStatus(node) === 'working', 1000)
  await until('autonomous thought', () => node.last('session.items')?.items.some((item) => item.kind === 'thinking' && item.text === 'Inspect the result.'))
  await chunk('Reading the result.')
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await until('autonomous tool and pre-tool text', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
  assert.ok(node.last('session.items').items.some((item) => item.text === 'Reading the result.'))
  update({ sessionUpdate: 'tool_call_update', toolCallId: 'read', status: 'completed', rawInput: { Path: 'result.txt' }, rawOutput: 'Found it.' })
  await until('autonomous tool result', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_result'))
  const live = node.last('session.items').items
  assert.equal(live.find((item) => item.kind === 'tool_use').tool_use_id, live.find((item) => item.kind === 'tool_result').tool_use_id)
  assert.match(JSON.stringify(live.find((item) => item.kind === 'tool_use').tool_input), /result\.txt/)
  await chunk('Workflow finished.')
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 0)

  const wake = { step_index: 2, type: 'SYSTEM_MESSAGE', content: 'The workflow finished.' }
  const partial = [...prior, wake, replyStep(3, 'Reading the result.')]
  transcripts(directory, partial, partial)
  await new Promise((resolve) => setTimeout(resolve, 80))
  assert.equal(node.last('session.delta').text, 'Workflow finished.', 'an idle summary and a partial export do not settle the turn')

  const complete = [...partial, { ...replyStep(4, ''), thinking: 'Inspect the result.', tool_calls: [{ name: 'read_file', args: { Path: 'result.txt' } }] },
    { step_index: 5, type: 'GENERIC', status: 'DONE', content: 'Found it.' }, replyStep(6, 'Workflow finished.')]
  nativeState(directory, 'RUNNING', 7)
  transcripts(directory, complete, complete)
  await new Promise((resolve) => setTimeout(resolve, 80))
  assert.equal(node.last('session.delta').text, 'Workflow finished.', 'a running summary does not settle even with the reply exported')
  nativeState(directory, 'IDLE', 7)
  await until('native export replaces autonomous work', () => sessionStatus(node) === 'done' && node.last('session.delta')?.text === '' && runtime.idle())
  const final = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(final.items.filter((item) => item.kind === 'tool_use').length, 1)
  assert.equal(final.items.filter((item) => item.text === 'Workflow finished.').length, 1)
})

test('autonomous wakes retire after inactivity without native export', async (t) => {
  const { node, directory, update, chunk, runtime } = await fixture(t, { autonomousIdleMs: 600, exportPollMs: 20, exportSettleTimeoutMs: 100 })
  nativeState(directory, 'IDLE', 0)
  update({ sessionUpdate: 'tool_call_update', toolCallId: 'read', name: 'read_file', status: 'completed', rawOutput: 'Found it.' })
  await until('a tool update can start the wake', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_result'), 1000)
  await chunk('Still awake.')
  await until('inactivity retires the stream and working status', () => node.last('session.delta')?.text === '' && sessionStatus(node) !== 'working' && runtime.idle(), 1500)
  assert.deepEqual((await node.peer.call('session.items', { session_id: SESSION })).items, [])
  await new Promise((resolve) => setTimeout(resolve, 150))
  await chunk('Awake again.')
  const complete = [{ step_index: 0, type: 'SYSTEM_MESSAGE' }, replyStep(1, 'Awake again.')]
  transcripts(directory, complete, complete)
  nativeState(directory, 'IDLE', 2)
  await until('first normal export settles after the ephemeral wake expires', () => runtime.idle() && node.last('session.delta')?.text === '', 300)
})

for (const status of ['RUNNING', 'IDLE']) {
  test(`a wake resumes after a long tool pause with native status ${status}`, async (t) => {
    const { node, directory, update, chunk, runtime } = await fixture(t, { autonomousIdleMs: 600, exportPollMs: 20 })
    nativeState(directory, status, 0)
    update({ sessionUpdate: 'tool_call', toolCallId: 'slow-tool', name: 'run_command' })
    await until('slow tool is live', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
    await until('stream retires during the tool pause', () => runtime.idle() && sessionStatus(node) !== 'working')
    if (status === 'IDLE') {
      update({ sessionUpdate: 'tool_call_update', toolCallId: 'slow-tool', status: 'completed', rawOutput: 'Finished.' })
      await until('resumed tool is complete', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_result'))
    }
    await chunk('Command finished.')

    const complete = [{ step_index: 0, type: 'SYSTEM_MESSAGE' },
      { ...replyStep(1, ''), tool_calls: [{ name: 'run_command' }] },
      { step_index: 2, type: 'GENERIC', status: 'DONE', content: 'Finished.' }, replyStep(3, 'Command finished.')]
    transcripts(directory, complete, complete)
    nativeState(directory, 'IDLE', 4)
    await until('the single native wake export settles the resumed stream', () => runtime.idle() && node.last('session.delta')?.text === '', 300)
  })
}

test('an autonomous wake stays live while its permission card awaits an answer', async (t) => {
  const { node, calls, update, chunk, runtime } = await fixture(t, { autonomousIdleMs: 300, exportPollMs: 20 })
  const permissionId = 'wake-permission'
  const answer = () => calls().find((call) => call.id === permissionId)?.result?.outcome
  let answerCard
  node.resolve = () => new Promise((resolve) => { answerCard = resolve })
  await chunk('Waiting for permission.')
  update({ permission: permissionId })
  await until('permission card reaches the phone', () => answerCard)
  await new Promise((resolve) => setTimeout(resolve, 400))
  assert.equal(answer(), undefined, 'the wake must not cancel the card after its inactivity deadline')
  assert.equal(runtime.idle(), false)

  answerCard({ allow: true })
  await until('ACP permission answer', () => answer())
  assert.deepEqual(answer(), { outcome: 'selected', optionId: 'allow' })
  await new Promise((resolve) => setTimeout(resolve, 60))
  assert.equal(runtime.idle(), false, 'answering the card gives the wake a fresh inactivity window')
  await chunk('Continuing.', 'Waiting for permission.Continuing.')
  await until('wake settles after the answer and subsequent inactivity', () => runtime.idle() && node.last('session.delta')?.text === '')
})

test('empty updates cannot start an autonomous wake', async (t) => {
  const { node, update, runtime } = await fixture(t)
  const before = node.all('session.delta').length
  update({ sessionUpdate: 'agent_message_chunk', content: { text: ' \n ' } })
  update({ sessionUpdate: 'agent_thought_chunk', content: { text: ' \t ' } })
  update({ sessionUpdate: 'tool_call_update', status: 'completed' })
  update({ sessionUpdate: 'session_info_update', title: 'Updates delivered' })
  await until('ACP updates delivered', () => node.last('runtime.sessions')?.sessions[0]?.title === 'Updates delivered')
  assert.notEqual(sessionStatus(node), 'working')
  assert.equal(node.all('session.delta').length, before)
  assert.equal(runtime.idle(), true)
})

test('an installed update exits as soon as the autonomous wake retires', async (t) => {
  const store = mkdtempSync(join(tmpdir(), 'agy-wake-update-'))
  t.after(() => rmSync(store, { recursive: true, force: true }))
  const exits = []
  const { node, directory, chunk } = await fixture(t, {
    idleCheckMs: 10_000, retryMs: 20, exportPollMs: 20,
    update: { name: PACKAGE_NAME, hash: 'running', store, exit: (hash) => exits.push(hash) },
  })
  await chunk('Wake reply.')
  const files = [{ path: 'antigravity/companion.mjs', data: Buffer.from('export {}\n') }]
  const hash = packageHash(files)
  node.offer = { name: PACKAGE_NAME, hash, files: files.map((file) => ({ ...file, data: file.data.toString('base64') })) }
  node.peer.close(new Error('node upgraded'))
  await until('package installed while wake is active', () => {
    try { return readlinkSync(join(store, 'current')) === hash } catch { return false }
  })
  assert.deepEqual(exits, [])
  const steps = [replyStep(0, 'Wake reply.')]
  transcripts(directory, steps, steps)
  nativeState(directory, 'IDLE', 1)
  await until('wake retirement triggers exit without waiting for the idle poll', () => exits.length, 1500)
  assert.deepEqual(exits, [hash])
})

test('a user send takes over an autonomous wake without waiting for it', async (t) => {
  const { node, update, chunk, promptCount, runtime } = await fixture(t)
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await chunk('Background progress.')
  const sent = await node.peer.call('session.send', { session_id: SESSION, text: 'Next request.' })
  assert.notEqual(sent?.queued, true)
  await promptCount(1)
  await chunk('User reply.')
  update({ end: true })
  await until('user turn settles independently', () => node.last('session.delta')?.text === '' && sessionStatus(node) !== 'working' && runtime.idle())
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items.map((item) => item.text), ['Next request.', 'User reply.'])
})

test('a policy-changing send cannot resume AGY during an autonomous wake', async (t) => {
  const { node, calls, chunk } = await fixture(t)
  await chunk('Background progress.')
  const resumes = calls().filter((call) => call.method === 'session/resume').length
  await assert.rejects(node.peer.call('session.send', {
    session_id: SESSION, text: 'Next request.', supervision_instructions: 'Ask before editing files.',
  }), /Wait for the current turn to finish before changing supervision policy/)
  assert.equal(calls().filter((call) => call.method === 'session/resume').length, resumes)
  assert.equal(calls().some((call) => call.method === 'session/prompt'), false)
  assert.equal(node.last('session.delta').text, 'Background progress.')
})

test('interrupt settles an autonomous wake even when the native summary still says running', async (t) => {
  const { node, directory, calls, update, chunk, runtime } = await fixture(t)
  nativeState(directory, 'RUNNING', 0)
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await chunk('Background progress.')
  await node.peer.call('session.interrupt', { session_id: SESSION })
  await until('native cancel requested', () => calls().some((call) => call.method === 'session/cancel'))
  await until('interrupt retires autonomous work', () => node.last('session.delta')?.text === '' && sessionStatus(node) !== 'working' && runtime.idle())
  assert.deepEqual((await node.peer.call('session.items', { session_id: SESSION })).items, [])
})

test('an autonomous wake preserves a previous unexported reply through interrupt and export', async (t) => {
  const { node, directory, update, chunk, promptCount, runtime } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Run the workflow.' })
  await promptCount(1)
  await chunk('Workflow started.')
  update({ end: true })
  await until('prompt reply held until export', () => runtime.idle() && node.last('session.items')?.items.some((item) => item.text === 'Workflow started.'))
  const prior = node.last('session.items').history

  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await until('autonomous tool over the pending reply', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
  assert.notEqual(node.last('session.items').history, prior)
  await node.peer.call('session.interrupt', { session_id: SESSION })
  await until('prior transcript restored and autonomous tool removed', () => node.last('session.items')?.history === prior)
  assert.deepEqual(node.last('session.items').items.map((item) => item.text), ['Run the workflow.', 'Workflow started.'])

  await chunk('Workflow finished.')
  const complete = [userStep(0, 'Run the workflow.'), { step_index: 1, type: 'SYSTEM_MESSAGE', content: 'Progress update.' },
    replyStep(2, 'Workflow started.'), { step_index: 3, type: 'SYSTEM_MESSAGE', content: 'The workflow finished.' }, replyStep(4, 'Workflow finished.')]
  transcripts(directory, complete, complete)
  nativeState(directory, 'IDLE', 5)
  await until('both replies are exported despite a mid-prompt system message', () => runtime.idle() && node.last('session.items')?.to_seq === 5)
  const final = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(final.items.map((item) => item.text), ['Run the workflow.', 'Workflow started.', 'Workflow finished.'])
})

test('a text-only wake does not rewrite the unexported prompt history', async (t) => {
  const { node, update, chunk, promptCount, runtime } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Run the workflow.' })
  await promptCount(1)
  await chunk('Workflow started.')
  update({ end: true })
  await until('prompt reply held until export', () => runtime.idle() && node.last('session.items')?.items.some((item) => item.text === 'Workflow started.'))
  const prior = await node.peer.call('session.items', { session_id: SESSION })
  await chunk('Workflow finished.')
  assert.deepEqual(await node.peer.call('session.items', { session_id: SESSION }), prior)
})

test('a late export of one wake cannot settle the next wake', async (t) => {
  const { node, directory, update, chunk, runtime } = await fixture(t, { autonomousIdleMs: 1300, exportPollMs: 20, exportSettleTimeoutMs: 500 })
  const announce = (toolCallId) => update({ sessionUpdate: 'tool_call', toolCallId, name: 'read_file' })
  announce('first-read')
  await chunk('Workflow finished.')
  await until('first wake times out unexported', () => runtime.idle() && node.last('session.delta')?.text === '')

  announce('second-read')
  await chunk('Workflow finished.')
  const first = [{ step_index: 0, type: 'SYSTEM_MESSAGE' },
    { ...replyStep(1, ''), tool_calls: [{ name: 'read_file' }] }, replyStep(2, 'Workflow finished.')]
  transcripts(directory, first, first)
  nativeState(directory, 'IDLE', 3)
  await new Promise((resolve) => setTimeout(resolve, 550))
  assert.equal(runtime.idle(), false, 'expiry does not reassign the earlier wake with the same tool and reply')
  assert.equal(node.last('session.delta').text, 'Workflow finished.')

  const next = [...first, { step_index: 3, type: 'SYSTEM_MESSAGE' }, replyStep(4, 'Workflow finished.')]
  transcripts(directory, next, next)
  nativeState(directory, 'IDLE', 5)
  await new Promise((resolve) => setTimeout(resolve, 100))
  assert.equal(runtime.idle(), false, 'the second wake cannot borrow the first wake\'s tool call')

  const complete = [...next.slice(0, -1), { ...replyStep(4, ''), tool_calls: [{ name: 'read_file' }] }, replyStep(5, 'Workflow finished.')]
  transcripts(directory, complete, complete)
  nativeState(directory, 'IDLE', 6)
  await until('second wake settles with its own export', () => runtime.idle() && node.last('session.delta')?.text === '', 400)
  const final = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(final.items.filter((item) => item.kind === 'tool_use').length, 2)
})

for (const exportSettleTimeoutMs of [450, 1100]) {
  test(`a chained timeout protects the next wake with a ${exportSettleTimeoutMs} ms export window`, async (t) => {
    const { node, directory, chunk, runtime } = await fixture(t, { autonomousIdleMs: 700, exportPollMs: 20, exportSettleTimeoutMs })
    const reply = 'Workflow finished.'
    const steps = []
    const exportWake = () => {
      steps.push({ step_index: steps.length, type: 'SYSTEM_MESSAGE' }, replyStep(steps.length + 1, reply))
      transcripts(directory, steps, steps)
      nativeState(directory, 'IDLE', steps.length)
    }
    const retired = () => runtime.idle() && node.last('session.delta')?.text === ''

    await chunk(reply)
    await until('the first wake times out', retired)
    const firstExpiry = Date.now() + exportSettleTimeoutMs
    await chunk(reply)
    exportWake()
    await until('the second wake also times out', retired)
    await new Promise((resolve) => setTimeout(resolve, Math.max(0, firstExpiry - Date.now() + 30)))

    await chunk(reply)
    exportWake()
    await new Promise((resolve) => setTimeout(resolve, 100))
    assert.equal(runtime.idle(), false, 'the second wake export cannot settle the third wake')
    assert.equal(node.last('session.delta').text, reply)

    exportWake()
    await until('the third wake settles on its own export', retired, 350)
  })
}

test('a live wake discards only unobserved missed boundaries on expiry', async (t) => {
  const exportSettleTimeoutMs = 1100
  const { node, directory, chunk, runtime } = await fixture(t, { autonomousIdleMs: 800, exportPollMs: 20, exportSettleTimeoutMs })
  const reply = 'Workflow finished.'
  const retired = () => runtime.idle() && node.last('session.delta')?.text === ''

  await chunk(reply)
  await until('the first wake times out', retired)
  await chunk(reply)
  await until('the ephemeral second wake times out', retired)
  const expiresAt = Date.now() + exportSettleTimeoutMs

  await chunk(reply)
  const first = [{ step_index: 0, type: 'SYSTEM_MESSAGE' }, replyStep(1, reply)]
  transcripts(directory, first, first)
  nativeState(directory, 'IDLE', first.length)
  await new Promise((resolve) => setTimeout(resolve, 550))
  await chunk(' ', `${reply} `)
  await new Promise((resolve) => setTimeout(resolve, Math.max(0, expiresAt - Date.now() + 50)))
  assert.equal(runtime.idle(), false, 'expiry cannot reassign the first wake export')
  await chunk(' ', `${reply}  `)

  const complete = [...first, { step_index: 2, type: 'SYSTEM_MESSAGE' }, replyStep(3, reply)]
  transcripts(directory, complete, complete)
  nativeState(directory, 'IDLE', complete.length)
  await until('the third wake settles without an export for the second wake', retired, 350)
})

test('unknown and archived sessions cannot start autonomous work', async (t) => {
  const { node, update, chunk, runtime } = await fixture(t)
  const unknown = '66666666-2222-4333-8444-555555555555'
  await node.peer.call('session.watch', { session_id: `agy-${unknown}` })
  update({ sessionId: unknown, sessionUpdate: 'agent_message_chunk', content: { text: 'Unknown session.' } })
  await chunk('Known session.')
  assert.equal(node.all('session.delta').some((delta) => delta.text === 'Unknown session.'), false)
  await node.peer.call('session.close', { session_id: SESSION })
  await until('close retires autonomous work', () => node.last('session.delta')?.text === '' && runtime.idle() && !sessionStatus(node))

  update({ sessionUpdate: 'agent_message_chunk', content: { text: 'Archived session.' } })
  await new Promise((resolve) => setTimeout(resolve, 100))
  assert.equal(node.all('session.delta').some((delta) => delta.text === 'Archived session.'), false)
  assert.equal(runtime.idle(), true)
})

test('watched native turns push new content after an older quota error without ACP updates', async (t) => {
  const { node, directory, calls } = await fixture(t)
  const old = [userStep(0, 'First request.'), { step_index: 1, type: 'ERROR_MESSAGE', content: 'Quota exhausted.' }]
  transcripts(directory, old, old)
  await until('old quota is pushed', () => node.last('session.items')?.to_seq === 2)
  const next = [...old, userStep(2, 'Second request.'), replyStep(3, 'Current progress.')]
  transcripts(directory, next, next)
  await until('external turn is pushed', () => node.last('session.items')?.to_seq === 4)
  assert.equal(node.last('session.items').from_seq, 2)
  assert.deepEqual(node.last('session.items').items.map((item) => item.text), ['Second request.', 'Current progress.'])
  assert.equal(calls().some((call) => call.method === 'session/prompt'), false)

  await node.peer.call('session.unwatch', { session_id: SESSION })
  const before = node.all('session.items').length
  const final = [...next, replyStep(4, 'Current result.')]
  transcripts(directory, final, final)
  await new Promise((resolve) => setTimeout(resolve, 600))
  assert.equal(node.all('session.items').length, before)
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: 4 })
  await until('rewatching catches up', () => node.last('session.items')?.to_seq === 5)
  assert.equal(node.last('session.items').items.at(-1).text, 'Current result.')
})

test('a native planner completion at the same index replaces watched content', async (t) => {
  const { node, directory } = await fixture(t)
  const pending = { ...replyStep(1, ''), thinking: 'Reviewing the change.', created_at: '2026-01-02T12:00:01Z' }
  const old = [userStep(0, 'Review the change.'), pending]
  transcripts(directory, old, old)
  await until('initial work is pushed', () => node.last('session.items')?.to_seq === 2)
  const history = node.last('session.items').history
  const complete = [old[0], { ...pending, content: 'Review complete.' }]
  transcripts(directory, complete, complete)
  await until('same-index completion is pushed', () => node.last('session.items')?.items.some((item) => item.text === 'Review complete.'))
  assert.equal(node.last('session.items').to_seq, 2)
  assert.notEqual(node.last('session.items').history, history)
})

test('normal AGY pages recover the complete native reply through the same source as full items', async (t) => {
  const { node, directory } = await fixture(t)
  const complete = '## Report\n' + 'A complete result with Unicode 字段.\n'.repeat(80) + 'End of report.'
  transcripts(directory,
    [userStep(0, 'Read the report.'), { ...replyStep(1, '## Report\n<truncated 4840 bytes>\nEnd of report.'), truncated_fields: ['content'] }],
    [userStep(0, 'Read the report.'), replyStep(1, complete)])
  const page = await node.peer.call('session.items', { session_id: SESSION })
  const reply = page.items.find((item) => item.role === 'assistant' && item.kind === 'text')
  assert.equal(reply.text, complete)
  assert.equal(reply.full_size, undefined)
  assert.equal(page.to_seq, 2)
  assert.notEqual(page.history, `agy:${RAW}`, 'compact transcript caches use a different history')
  const full = await node.peer.call('session.item', { session_id: SESSION, item_id: reply.id })
  assert.equal(full.text, complete)
})

test('live AGY items apply Persona page limits to the complete native reply', async (t) => {
  const { node, directory } = await fixture(t)
  const complete = '## Report\n' + 'A detailed report row.\n'.repeat(2000) + 'Literal example: <truncated 7 bytes>.'
  transcripts(directory,
    [userStep(0, 'Read the report.'), { ...replyStep(1, '## Report\n<truncated 40000 bytes>\nEnd.'), truncated_fields: ['content'] }],
    [userStep(0, 'Read the report.'), replyStep(1, complete)])
  await until('the reply is pushed', () => node.last('session.items')?.to_seq === 2)
  const reply = node.last('session.items').items.find((item) => item.role === 'assistant' && item.kind === 'text')
  assert.equal(reply.full_size, Buffer.byteLength(complete))
  assert.ok(reply.text.startsWith('## Report\nA detailed report row.'))
  const full = await node.peer.call('session.item', { session_id: SESSION, item_id: reply.id })
  assert.equal(full.text, complete)
  assert.ok(full.text.includes('Literal example: <truncated 7 bytes>.'))
})

test('AGY watch cursors follow the complete export while its compact export is ahead', async (t) => {
  const { node, directory } = await fixture(t)
  const old = [userStep(0, 'First request.'), replyStep(1, 'First reply.')]
  const nextUser = userStep(2, 'Second request.')
  transcripts(directory, [...old, nextUser], old)
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.to_seq, 2)
  await until('the initial complete export is pushed', () => node.last('session.items')?.to_seq === 2)
  await node.peer.call('session.unwatch', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION })
  const complete = [...old, nextUser, replyStep(3, 'Second reply.')]
  transcripts(directory, complete, complete)
  await until('the complete export catches up', () => node.last('session.items')?.to_seq === 4)
  assert.deepEqual(node.last('session.items').items.map((item) => item.seq), [3, 4])
})

test('owned ACP turns publish the user prompt, working state and live tool steps before native transcript export', { timeout: 20_000 }, async (t) => {
  const { node, directory, update, promptCount } = await fixture(t)
  const started = await node.peer.call('session.start', { cwd: directory, assistant: true })
  assert.equal(started.session.status, 'idle')

  await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect the repository.', client_msg_id: 'msg-1', assistant: true })
  await promptCount(1)
  await until('working session published', () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)?.status === 'working')

  const liveInitial = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(liveInitial.items.length, 1)
  assert.equal(liveInitial.items[0].role, 'user')
  assert.equal(liveInitial.items[0].text, 'Inspect the repository.')
  assert.equal(liveInitial.items[0].client_msg_id, 'msg-1')

  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'Checking files first.' } })
  update({ sessionUpdate: 'tool_call', toolCallId: 'tool-live-1', name: 'run_command', rawInput: { CommandLine: 'ls', toolSummary: 'List files' } })
  await until('live thought and tool call pushed', () => {
    const latest = node.last('session.items')
    return latest?.items?.some((it) => it.kind === 'thinking') && latest?.items?.some((it) => it.kind === 'tool_use' && it.tool_name === 'Bash')
  })

  const exported = [userStep(0, 'Inspect the repository.'), replyStep(1, 'Inspection complete.')]
  transcripts(directory, exported, exported)
  update({ end: true })
  await until('native export replaces live steps', () => node.last('session.items')?.items?.some((it) => it.role === 'assistant' && it.text === 'Inspection complete.'))
  const finalPage = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(finalPage.items[0].client_msg_id, 'msg-1')
  assert.equal(finalPage.items.at(-1).text, 'Inspection complete.')
})

test('live thinking chunks, parallel tool updates and settling keep history stable without rewriting prior turns (#11129)', { timeout: 25_000 }, async (t) => {
  const { node, directory, update, promptCount } = await fixture(t)
  const prior = [
    { ...userStep(0, 'Old prompt 1'), created_at: '2026-01-01T00:00:00Z' },
    { ...replyStep(1, 'Old answer 1'), created_at: '2026-01-01T00:00:01Z' },
    { ...userStep(2, 'Old prompt 2'), created_at: '2026-01-01T00:00:02Z' },
    { ...replyStep(3, 'Old answer 2'), created_at: '2026-01-01T00:00:03Z' },
  ]
  transcripts(directory, prior, prior)
  await node.peer.call('session.unwatch', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION })

  await node.peer.call('session.send', { session_id: SESSION, text: 'Investigate the jump.', client_msg_id: 'msg-jump' })
  await promptCount(1)
  await until('live turn user prompt pushed', () => node.last('session.items')?.to_seq === 5)

  const liveHistory = node.last('session.items').history
  const startBatchCount = node.all('session.items').length
  // Seed simulated client state from the initial live page before incremental updates begin.
  const mergedById = new Map((await node.peer.call('session.items', { session_id: SESSION })).items.map((it) => [it.id, it]))
  let covered = 5

  // 1. Multiple thinking chunks separated beyond the 150ms throttle update the same thinking item in place.
  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'Step 1: ' } })
  await until('first thinking chunk pushed', () => node.last('session.items')?.items?.some((it) => it.kind === 'thinking' && it.text === 'Step 1:'))
  const firstThought = node.last('session.items').items.find((it) => it.kind === 'thinking')
  assert.equal(node.last('session.items').history, liveHistory)

  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'reading runtime.js.' } })
  await until('second thinking chunk updates same item', () =>
    node.last('session.items')?.items?.some((it) => it.id === firstThought.id && it.text === 'Step 1: reading runtime.js.'),
  )
  const secondThoughtBatch = node.last('session.items')
  assert.equal(secondThoughtBatch.history, liveHistory)
  assert.equal(secondThoughtBatch.from_seq, firstThought.seq)
  assert.equal(secondThoughtBatch.to_seq, firstThought.seq)
  assert.deepEqual(secondThoughtBatch.items.map((it) => it.id), [firstThought.id])

  // 2. Parallel tool calls, an in-place argument update, and out-of-order completion stay incremental and paired.
  update({ sessionUpdate: 'tool_call', toolCallId: 'tc-1', name: 'read_file', rawInput: {} })
  update({ sessionUpdate: 'tool_call', toolCallId: 'tc-2', name: 'run_command', rawInput: { CommandLine: 'ls' } })
  await until('both parallel tool calls pushed', () => {
    const allLive = node.all('session.items').slice(startBatchCount).flatMap((b) => b.items)
    return allLive.some((it) => it.kind === 'tool_use' && it.tool_name === 'Read') &&
      allLive.some((it) => it.kind === 'tool_use' && it.tool_name === 'Bash')
  })

  update({ sessionUpdate: 'tool_call_update', toolCallId: 'tc-1', status: 'in_progress', rawInput: { Path: '/tmp/a.js' } })
  await until('tool call input updated in place', () =>
    node.all('session.items').slice(startBatchCount).flatMap((b) => b.items)
      .some((it) => it.kind === 'tool_use' && it.tool_name === 'Read' && it.tool_input?.file_path === '/tmp/a.js'),
  )

  update({ sessionUpdate: 'tool_call_update', toolCallId: 'tc-2', status: 'completed', rawOutput: 'ok-2' })
  update({ sessionUpdate: 'tool_call_update', toolCallId: 'tc-1', status: 'failed', rawOutput: 'err-1' })
  await until('both out-of-order tool results pushed', () => {
    const allLive = node.all('session.items').slice(startBatchCount).flatMap((b) => b.items)
    return allLive.some((it) => it.kind === 'tool_result' && it.text === 'ok-2') &&
      allLive.some((it) => it.kind === 'tool_result' && it.text === 'err-1' && it.is_error === true)
  })

  // 3. A second thinking phase after tools appends chronologically without mutating the first thinking item.
  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'Step 2: wrapping up.' } })
  await until('second thinking block pushed', () =>
    node.all('session.items').slice(startBatchCount).flatMap((b) => b.items)
      .some((it) => it.kind === 'thinking' && it.text === 'Step 2: wrapping up.'),
  )

  // 4. Streaming final text and ending the turn before disk export settles incrementally under the same history.
  update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Fixed cleanly.' } })
  await until('final reply streams', () => node.last('session.delta')?.text === 'Fixed cleanly.')
  update({ end: true })
  await until('settled reply pushed incrementally', () =>
    node.last('session.items')?.items?.some((it) => it.role === 'assistant' && it.kind === 'text' && it.text === 'Fixed cleanly.'),
  )

  const liveBatches = node.all('session.items').slice(startBatchCount)
  assert.ok(liveBatches.length >= 4, `expected multiple incremental batches, got ${liveBatches.length}`)
  for (const batch of liveBatches) {
    assert.equal(batch.history, liveHistory, 'live progress and settling must not mutate history')
    assert.ok(batch.from_seq > 0, `expected incremental continuation batch, got from_seq=${batch.from_seq}`)
    assert.ok(batch.from_seq <= covered, `batch must remain continuous (from_seq=${batch.from_seq} > covered=${covered})`)
    assert.equal(batch.items.some((it) => it.seq <= 4), false, 'prior turns must not be re-transmitted during live updates')
    for (const it of batch.items) mergedById.set(it.id, it)
    covered = Math.max(covered, batch.to_seq)
  }

  const merged = [...mergedById.values()].sort((a, b) => a.seq - b.seq)
  assert.deepEqual(merged.slice(0, 4).map((it) => it.text), ['Old prompt 1', 'Old answer 1', 'Old prompt 2', 'Old answer 2'])
  const thoughts = merged.filter((it) => it.kind === 'thinking')
  assert.deepEqual(thoughts.map((it) => it.text), ['Step 1: reading runtime.js.', 'Step 2: wrapping up.'])
  const readUse = merged.find((it) => it.kind === 'tool_use' && it.tool_name === 'Read')
  const bashUse = merged.find((it) => it.kind === 'tool_use' && it.tool_name === 'Bash')
  const resultsByCall = new Map(merged.filter((it) => it.kind === 'tool_result').map((it) => [it.tool_use_id, it]))
  assert.equal(readUse.tool_input.file_path, '/tmp/a.js')
  assert.equal(resultsByCall.get(readUse.tool_use_id)?.text, 'err-1')
  assert.equal(resultsByCall.get(readUse.tool_use_id)?.is_error, true)
  assert.equal(resultsByCall.get(bashUse.tool_use_id)?.text, 'ok-2')
  assert.equal(resultsByCall.get(bashUse.tool_use_id)?.is_error, false)
  assert.equal(merged.at(-1).text, 'Fixed cleanly.')

  // 5. Only once native transcript_full.jsonl actually flushes does history transition back to canonical disk history.
  const flushed = [
    ...prior,
    { ...userStep(4, 'Investigate the jump.'), created_at: '2026-01-01T00:00:04Z' },
    { ...replyStep(5, 'Fixed cleanly.'), thinking: 'Step 1: reading runtime.js.\nStep 2: wrapping up.', created_at: '2026-01-01T00:00:05Z' },
  ]
  transcripts(directory, flushed, flushed)
  await until('native disk export replaces synthetic settled history once', () => {
    const latest = node.last('session.items')
    return latest && latest.history !== liveHistory && !latest.history.includes(':settled:') && latest.to_seq === 6
  })
  assert.equal(node.last('session.items').from_seq, 0)
  assert.equal(node.last('session.items').items.at(-1).text, 'Fixed cleanly.')
})

test('trailing thinking chunks arriving right before turn completion are not dropped on settle (#11129)', { timeout: 20_000 }, async (t) => {
  const { node, update, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Quick check.' })
  await promptCount(1)
  await until('user prompt pushed', () => node.last('session.items')?.to_seq === 1)
  const liveHistory = node.last('session.items').history

  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'First half' } })
  await until('first thinking chunk pushed', () => node.last('session.items')?.items?.some((it) => it.kind === 'thinking' && it.text === 'First half'))
  const thoughtItem = node.last('session.items').items.find((it) => it.kind === 'thinking')
  const afterFirstThought = node.all('session.items').length

  // Immediately append a second thinking chunk and end the turn before the 150ms thought throttle fires.
  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: ' + second half.' } })
  update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'All good.' } })
  update({ end: true })

  await until('settled batch includes final reply', () =>
    node.last('session.items')?.items?.some((it) => it.role === 'assistant' && it.kind === 'text' && it.text === 'All good.'),
  )
  const followBatches = node.all('session.items').slice(afterFirstThought)
  for (const batch of followBatches) assert.equal(batch.history, liveHistory)
  const updatedThought = followBatches.flatMap((b) => b.items).findLast((it) => it.id === thoughtItem.id)
  assert.equal(updatedThought?.text, 'First half + second half.')
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.history, liveHistory)
  assert.equal(page.items.find((it) => it.id === thoughtItem.id)?.text, 'First half + second half.')
})

for (const partialTools of [false, true]) {
  test(`partial export preserves the final reply (tools: ${partialTools})`, async (t) => {
    const { node, directory, update, chunk, promptCount } = await fixture(t)
    await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect.' })
    await promptCount(1)
    await chunk('Checking first.')
    update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
    update({ sessionUpdate: 'tool_call_update', toolCallId: 'read', status: 'completed', rawOutput: 'Found it.' })
    await chunk('Final answer.')
    const partial = [userStep(0, 'Inspect.')]
    if (partialTools) partial.push(replyStep(1, 'Checking first.'),
      { ...replyStep(2, 'Checking a file.'), tool_calls: [{ name: 'read_file', args: {} }] },
      { step_index: 3, type: 'GENERIC', status: 'DONE', content: 'Found it.' })
    transcripts(directory, partial, partial)
    update({ end: true })
    await until('turn settled', () => node.last('session.delta')?.text === '')
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.equal(page.items.at(-1).text, 'Final answer.')
    assert.ok(page.items.at(-1).seq > partial.at(-1).step_index + 1)
    assert.ok(page.items.some((item) => item.text === 'Checking first.'))
    const full = [...partial, replyStep(partial.at(-1).step_index + 1, 'Final answer.')]
    transcripts(directory, full, full)
    await until('final export replaces pending history', () => node.last('session.items') && !node.last('session.items').history.includes(':settled:'))
    assert.equal((await node.peer.call('session.items', { session_id: SESSION })).items.at(-1).text, 'Final answer.')
  })
}

test('pre-tool text is committed and partial user export keeps live history stable', async (t) => {
  const { node, directory, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect.' })
  await promptCount(1)
  const before = await node.peer.call('session.items', { session_id: SESSION })
  await chunk('Checking first.')
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await until('tool pushed', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items.filter((item) => item.kind === 'text').map((item) => item.text), ['Inspect.', 'Checking first.'])
  assert.equal(node.last('session.delta').text, '')
  transcripts(directory, [userStep(0, 'Inspect.')], [userStep(0, 'Inspect.')])
  const partial = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(partial.history, before.history)
  assert.deepEqual(partial.items, page.items)
  update({ end: true })
})

test('queued sends acknowledge the queue and defer options until execution', async (t) => {
  const { node, calls, update, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
  await promptCount(1)
  const count = calls().length
  const result = await node.peer.call('session.send', { session_id: SESSION, text: 'Second.', model: 'next-model', mode: 'plan' })
  assert.equal(result.queued, true)
  assert.equal(calls().slice(count).some((call) => call.method === 'session/set_model' || call.method === 'session/set_mode'), false)
  update({ end: true })
  await promptCount(2)
  const nextCalls = calls().slice(count)
  assert.ok(nextCalls.some((call) => call.method === 'session/set_model'))
  assert.ok(nextCalls.some((call) => call.method === 'session/set_mode'))
  update({ end: true })
})

test('plan-mode starts advertise readonly support', async (t) => {
  const { node, directory, calls } = await fixture(t)
  assert.equal(node.hello.caps.readonly, true)
  const result = await node.peer.call('session.start', { cwd: directory, permission_mode: 'plan', readonly: true })
  assert.equal(result.session.permission_mode, 'plan')
  assert.ok(calls().some((call) => call.method === 'session/set_mode' && call.params.modeId === 'plan'))
})


for (const text of ['invalid-once', 'invalid-always']) {
  test(`stale sessions get one clean resume retry: ${text}`, async (t) => {
    const { node, calls, update, promptCount } = await fixture(t)
    const before = calls().length
    await node.peer.call('session.send', { session_id: SESSION, text })
    await promptCount(2)
    const recovery = calls().slice(before).filter((call) => ['session/prompt', 'session/resume'].includes(call.method))
    assert.deepEqual(recovery.map((call) => call.method), ['session/prompt', 'session/resume', 'session/prompt'])
    if (text === 'invalid-always') {
      await until('retry failure published', () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)?.turn_error === 'Invalid session')
      assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 2)
    } else {
      update({ end: true })
      await until('retried turn ends', () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)?.status !== 'working')
    }
  })
}

for (const secondReply of ['Second answer.', 'First answer.']) {
  test(`a previous turn exported during its successor cannot replace ${secondReply}`, async (t) => {
    const { node, directory, update, chunk, promptCount } = await fixture(t)
    await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
    await promptCount(1)
    await chunk('First answer.')
    await node.peer.call('session.send', { session_id: SESSION, text: 'Second.' })
    update({ end: true })
    await promptCount(2)
    await chunk(secondReply)
    const first = [userStep(0, 'First.'), replyStep(1, 'First answer.')]
    transcripts(directory, first, first)
    update({ end: true })
    await until('second turn settled', () => node.last('session.delta')?.text === '')
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.deepEqual(page.items.filter((item) => item.kind === 'text').map((item) => item.text),
      ['First.', 'First answer.', 'Second.', secondReply])
    const full = [...first, userStep(2, 'Second.'), replyStep(3, secondReply)]
    transcripts(directory, full, full)
    await until('second export replaces pending', () => node.last('session.items') && !node.last('session.items').history.includes(':settled:'))
    assert.equal(node.last('session.items').items.at(-1).text, secondReply)
  })
}


for (const externalReply of [false, true]) {
  test(`a native successor supersedes pending live history (reply: ${externalReply})`, async (t) => {
    const { node, directory, update, chunk, promptCount } = await fixture(t)
    await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
    await promptCount(1)
    await chunk('First answer.')
    update({ end: true })
    await until('first reply settled', () => node.last('session.delta')?.text === '')
    const steps = [userStep(0, 'First.'), replyStep(1, 'First answer.'), userStep(2, 'External question.')]
    if (externalReply) steps.push(replyStep(3, 'External answer.'))
    transcripts(directory, steps, steps)
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.equal(page.history.includes(':settled:'), false)
    assert.deepEqual(page.items.map((item) => item.text), steps.map((step) => step.content))
  })
}

test('compact truncated replies eventually release pending history without losing wholly unexported replies', async (t) => {
  const { node, directory, update, chunk, promptCount } = await fixture(t, { exportSettleTimeoutMs: 150, exportPollMs: 20 })
  await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
  await promptCount(1)
  await chunk('First answer with complete details.')
  update({ end: true })
  await until('reply settled', () => node.last('session.delta')?.text === '')
  await new Promise((resolve) => setTimeout(resolve, 200))
  assert.equal((await node.peer.call('session.items', { session_id: SESSION })).items.at(-1).text, 'First answer with complete details.')
  const logs = join(directory, 'config/brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  writeFileSync(join(logs, 'transcript.jsonl'), JSON.stringify(userStep(0, 'First.')) + '\n')
  assert.equal((await node.peer.call('session.items', { session_id: SESSION })).items.at(-1).text, 'First answer with complete details.')
  writeFileSync(join(logs, 'transcript.jsonl'), [userStep(0, 'First.'),
    { ...replyStep(1, 'First answer <truncated 20 bytes>'), truncated_fields: ['content'] }].map(JSON.stringify).join('\n') + '\n')
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.history.includes(':settled:'), false)
  assert.equal(page.items.at(-1).text, 'First answer <truncated 20 bytes>')
})

test('a rejected initial preparation leaves idle history and error state unchanged', async (t) => {
  const { node, calls } = await fixture(t)
  const before = await node.peer.call('session.items', { session_id: SESSION })
  const batches = node.all('session.items').length
  await assert.rejects(node.peer.call('session.send', { session_id: SESSION, text: 'Do not publish this.', model: 'invalid-model' }), /Unknown model/)
  const after = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(after, before)
  const inventories = node.all('runtime.sessions').length
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Checked' })
  await until('inventory refreshed', () => node.all('runtime.sessions').length > inventories)
  const row = node.last('runtime.sessions').sessions.find((row) => row.session_id === SESSION)
  assert.equal(row.status, 'idle')
  assert.equal(row.turn_error, undefined)
  assert.equal(node.all('session.items').slice(batches).flatMap((batch) => batch.items).some((item) => item.text === 'Do not publish this.'), false)
  assert.equal(calls().some((call) => call.method === 'session/prompt'), false)
})


test('preparation does not expose a user message before accepting the send', async (t) => {
  const { node } = await fixture(t)
  const before = await node.peer.call('session.items', { session_id: SESSION })
  let release, entered = false
  const gate = new Promise((resolve) => { release = resolve })
  const original = AgyAcpClient.prototype.setModel
  t.mock.method(AgyAcpClient.prototype, 'setModel', async function (...args) {
    entered = true
    await gate
    return original.apply(this, args)
  })
  const rejected = assert.rejects(node.peer.call('session.send', { session_id: SESSION, text: 'Not accepted.', model: 'invalid-model' }), /Unknown model/)
  try {
    await until('model preparation started', () => entered)
    assert.deepEqual(await node.peer.call('session.items', { session_id: SESSION }), before)
  } finally {
    release()
    await rejected
  }
})

test('whitespace before a tool call does not allocate a text step', async (t) => {
  const { node, update, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect.' })
  await promptCount(1)
  update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: '  \n' } })
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await until('tool pushed', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.items.find((item) => item.kind === 'tool_use').seq, 2)
  assert.equal(page.to_seq, 2)
  update({ end: true })
})

test('a queued preparation failure remains visible after its queued acknowledgement', async (t) => {
  const { node, update, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
  await promptCount(1)
  assert.equal((await node.peer.call('session.send', { session_id: SESSION, text: 'Queued.', model: 'invalid-model' })).queued, true)
  update({ end: true })
  await until('queued failure published', () => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === SESSION)?.turn_error?.includes('Unknown model'))
  const row = node.last('runtime.sessions').sessions.find((row) => row.session_id === SESSION)
  assert.notEqual(row.status, 'working')
})

for (const truncated of [false, true]) {
  test(`expiry preserves final replies over pre-tool narration (truncated: ${truncated})`, async (t) => {
    const { node, directory, update, chunk, promptCount } = await fixture(t, { exportSettleTimeoutMs: 150, exportPollMs: 20 })
    await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect.' })
    await promptCount(1)
    await chunk('Preparing.')
    update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
    await chunk('Final answer.')
    const narration = truncated ? { ...replyStep(1, 'Preparing <truncated 8 bytes>'), truncated_fields: ['content'] } : replyStep(1, 'Preparing.')
    const partial = [userStep(0, 'Inspect.'), narration]
    transcripts(directory, partial, partial)
    update({ end: true })
    await until('reply settled', () => node.last('session.delta')?.text === '')
    await new Promise((resolve) => setTimeout(resolve, 200))
    assert.equal((await node.peer.call('session.items', { session_id: SESSION })).items.at(-1).text, 'Final answer.')
  })
}

test('runtime session.items refreshes history token and omits broken tool-media image items (#11224)', async (t) => {
  const { node, directory } = await fixture(t)
  const steps = [
    { ...userStep(0, 'Check the screenshot.'), created_at: '2026-01-01T00:00:00Z' },
    {
      step_index: 1,
      source: 'MODEL',
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-01-01T00:00:01Z',
      tool_calls: [{ name: 'read_file', args: { Path: '/work/shots/ui.png' } }],
    },
    {
      step_index: 2,
      source: 'MODEL',
      type: 'GENERIC',
      status: 'DONE',
      created_at: '2026-01-01T00:00:02Z',
      content: 'Read image /work/shots/ui.png',
      media: [{ mime_type: 'image/png', uri: '/work/shots/ui.png' }],
    },
    { ...replyStep(3, 'Looks clean.'), created_at: '2026-01-01T00:00:03Z' },
  ]
  transcripts(directory, steps, steps)
  const paged = await node.peer.call('session.items', { session_id: SESSION, turns: 1 })
  assert.match(paged.history, /:no-tool-media(?:$|:)/)
  assert.equal(paged.has_more, false)
  assert.equal(paged.items.some((it) => it.kind === 'image'), false)
  const readUse = paged.items.find((it) => it.kind === 'tool_use')
  const readResult = paged.items.find((it) => it.kind === 'tool_result')
  assert.equal(readUse?.tool_name, 'Read')
  assert.deepEqual(readUse?.tool_input, { file_path: '/work/shots/ui.png' })
  assert.equal(readResult?.tool_use_id, readUse?.tool_use_id)
  assert.equal(readResult?.is_error, false)
})
