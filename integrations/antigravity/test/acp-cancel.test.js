import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { DatabaseSync } from 'node:sqlite'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { AgyAcpClient } from '../lib/acp.js'
import { toPersonaId } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE = fileURLToPath(new URL('./fake-acp-cancel.mjs', import.meta.url))
const SESSION = '0c7d2c4e-5b1a-4f3e-9a6d-2f8b1c3d4e5f' // the session the fake agent opens
const SLOW = 15_000 // spawning a node process can be slow on a busy machine

/** A temp dir with the fake's record file; afterwards every fake it started is killed and the dir removed. */
function scratch(t) {
  const dir = mkdtempSync(join(tmpdir(), 'agy-cancel-'))
  const record = join(dir, 'record.jsonl')
  writeFileSync(record, '')
  t.after(() => {
    for (const m of received(record)) {
      if (m.started) {
        try {
          process.kill(m.started, 'SIGKILL')
        } catch {}
      }
    }
    rmSync(dir, { recursive: true, force: true })
  })
  return { dir, record }
}

/** What the fake agent received, in order. */
function received(record) {
  return readFileSync(record, 'utf8').split('\n').filter(Boolean).map((line) => JSON.parse(line))
}

/** The answers to the fake's session/request_permission requests. */
function permissionAnswers(record) {
  return received(record).filter((m) => typeof m.id === 'string' && m.id.startsWith('perm-'))
}

const cancelled = { outcome: { outcome: 'cancelled' } }

test('cancel is an ACP notification: the running prompt ends with stopReason cancelled', async (t) => {
  const { record } = scratch(t)
  const acp = new AgyAcpClient({ bin: process.execPath, args: [FAKE, '--record', record, 'acp'] })
  t.after(() => acp.stop())
  await acp.ensureStarted()
  const prompt = acp.prompt(SESSION, [{ type: 'text', text: 'hold' }])
  await until('the prompt reaches the agent', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await acp.cancel(SESSION)
  assert.deepEqual(await prompt, { stopReason: 'cancelled' })
  const cancels = received(record).filter((m) => m.method === 'session/cancel')
  assert.equal(cancels.length, 1)
  assert.equal('id' in cancels[0], false, 'session/cancel carries no id')
  assert.deepEqual(cancels[0].params, { sessionId: SESSION })
})

test('cancel with no agy running starts none and resolves', async (t) => {
  const { record } = scratch(t)
  const acp = new AgyAcpClient({ bin: process.execPath, args: [FAKE, '--record', record, 'acp'] })
  t.after(() => acp.stop())
  await acp.cancel(SESSION)
  assert.equal(acp.proc, null)
  assert.deepEqual(received(record), [], 'no agy process was started')
})

/** The agy runtime on a fake node, with an executable fake agy; nobody answers a card on the phone. */
async function agyRuntime(t, options = {}) {
  const { dir, record } = scratch(t)
  const agyBin = join(dir, 'agy')
  writeFileSync(agyBin, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' --record '${record}' "$@"\n`)
  chmodSync(agyBin, 0o755)
  // agy keeps each conversation in brain/<id>: the runtime lists the sessions it finds there.
  const agyDir = join(dir, 'config')
  mkdirSync(join(agyDir, 'brain', SESSION), { recursive: true })
  const n = await startFakeNode(t)
  n.resolve = () => new Promise(() => {})
  options.beforeStart?.(agyDir)
  const runtime = createAgyRuntime({ agyBin, agyDir, home: dir, socketPath: n.path, log: () => {}, ...options.runtime })
  t.after(() => runtime.stop())
  runtime.start()
  await n.ready
  const start = async (prompt) => {
    const { session } = await n.peer.call('session.start', { cwd: dir, prompt })
    assert.equal(session.session_id, toPersonaId(SESSION))
    return session.session_id
  }
  const status = (pid) => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)?.status
  // A rename pushes the inventory again: the status as the list reads it now.
  const refresh = async (pid) => {
    const before = n.all('runtime.sessions').length
    await n.peer.call('session.rename', { session_id: pid, title: 'Renamed' })
    await until('inventory refreshed', () => n.all('runtime.sessions').length > before, SLOW)
    return status(pid)
  }
  return { n, record, start, status, refresh }
}

/** agy's own conversation summary for SESSION in agyDir: what nativeWorking reads. */
function nativeSummary(agyDir, status, stepCount) {
  const db = new DatabaseSync(join(agyDir, 'conversation_summaries.db'))
  try {
    db.exec('CREATE TABLE IF NOT EXISTS conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    db.prepare('INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)')
      .run(SESSION, status, stepCount, '', '[]', new Date().toISOString())
  } finally {
    db.close()
  }
}

// #11108: interrupt answers settled once the running turn has ended, so agent_interrupt need not poll for it.
test('interrupt answers settled once agy ends the cancelled turn', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('hold')
  await until('the prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await until('the row is working', () => status(pid) === 'working', SLOW)
  const res = await n.peer.call('session.interrupt', { session_id: pid })
  assert.equal(res?.settled, true)
  assert.ok(received(record).some((m) => m.ended === SESSION), 'agy ended the prompt before settled was answered')
  await until('the row is no longer working', () => status(pid) !== 'working', SLOW)
})

test('interrupt does not answer settled when agy never ends the cancelled turn', async (t) => {
  const { n, record, start, status } = await agyRuntime(t, { runtime: { interruptSettleMs: 300 } })
  const pid = await start('ignore-cancel')
  await until('the prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  const res = await n.peer.call('session.interrupt', { session_id: pid })
  assert.notEqual(res?.settled, true)
  assert.ok(received(record).some((m) => m.method === 'session/cancel'), 'the cancel was sent')
  await until('the row is still working', () => status(pid) === 'working', SLOW)
})

// #11108: agy's summary can still say running right after a confirmed cancel; that alone is not work. A new native
// step is.
test('a summary still running after a confirmed cancel does not keep the row working until a new step', async (t) => {
  let agyDir
  const { n, record, start, status, refresh } = await agyRuntime(t, {
    beforeStart: (dir) => { agyDir = dir; nativeSummary(dir, 'CASCADE_RUN_STATUS_RUNNING', 1) },
  })
  const pid = await start('hold')
  await until('the prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  assert.equal((await n.peer.call('session.interrupt', { session_id: pid }))?.settled, true)
  await until('the row is no longer working', () => status(pid) !== undefined && status(pid) !== 'working', SLOW)
  assert.notEqual(await refresh(pid), 'working', 'the stale summary alone does not make it working again')
  nativeSummary(agyDir, 'CASCADE_RUN_STATUS_RUNNING', 2)
  assert.equal(await refresh(pid), 'working', 'a new native step is work again')
})

test('interrupt withdraws the permission card, answers agy cancelled and ends the turn', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask')
  await until('the card is on the phone', () => n.asked.length === 1, SLOW)
  assert.equal(n.asked[0].kind, 'permission')
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('the card is withdrawn', () => n.last('interaction.cancel')?.id === n.asked[0].id, SLOW)
  await until('agy hears cancelled', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  const cancels = received(record).filter((m) => m.method === 'session/cancel')
  assert.equal(cancels.length, 1)
  assert.equal('id' in cancels[0], false, 'session/cancel carries no id')
  await until('the turn ends', () => status(pid) === 'done', SLOW)
})

test('close cancels the active turn and withdraws its permission card even without native session/close', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask')
  await until('the card is on the phone', () => n.asked.length === 1, SLOW)
  await n.peer.call('session.close', { session_id: pid })
  await until('the card is withdrawn', () => n.last('interaction.cancel')?.id === n.asked[0].id, SLOW)
  await until('the native prompt ended', () => received(record).some((m) => m.ended === SESSION), SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  await until('the session is no longer live', () => status(pid) === undefined, SLOW)
  await assert.rejects(n.peer.call('session.send', { session_id: pid, text: 'Continue' }), /session is closed/)
  const hellos = n.hellos
  const inventories = n.all('runtime.sessions').length
  n.peer.close()
  await until('the runtime reconnects', () => n.hellos > hellos, SLOW)
  await until('the closed inventory is republished', () => n.all('runtime.sessions').length > inventories && n.last('runtime.sessions')?.sessions.length === 0, SLOW)
})

test('close prevents queued prompts from starting after cancellation', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('hold')
  await until('the first prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await n.peer.call('session.send', { session_id: pid, text: 'Queued' })
  await n.peer.call('session.close', { session_id: pid })
  await until('the native prompt ended', () => received(record).some((m) => m.ended === SESSION), SLOW)
  await until('the session is no longer live', () => status(pid) === undefined, SLOW)
  assert.equal(received(record).filter((m) => m.method === 'session/prompt').length, 1)
})

test('a permission request crossing close is cancelled without a new card', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask-on-cancel')
  await until('the prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await n.peer.call('session.close', { session_id: pid })
  await until('the native prompt ended', () => received(record).some((m) => m.ended === SESSION), SLOW)
  await until('the session is no longer live', () => status(pid) === undefined, SLOW)
  assert.equal(n.asked.length, 0)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
})

test('a permission request that crosses the interrupt is answered cancelled without a card', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask-on-cancel')
  await until('the prompt reaches the agent', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('agy hears cancelled', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  await until('the turn ends', () => status(pid) === 'done', SLOW)
  assert.equal(n.asked.length, 0, 'no card went to the phone')
})

test('a card still out when the turn ends is withdrawn and agy hears cancelled', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask-then-end')
  await until('agy hears cancelled', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  await until('the card is withdrawn', () => n.last('interaction.cancel'), SLOW)
  for (const ix of n.asked) assert.ok(n.all('interaction.cancel').some((c) => c.id === ix.id), 'every card that went out is taken off')
  await until('the turn ends', () => status(pid) === 'done', SLOW)
})

test('an answered card still selects the matching agy option', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  n.resolve = () => ({ allow: true })
  const pid = await start('ask')
  await until('agy hears the answer', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'allow' } })
  assert.equal(n.all('interaction.cancel').length, 0, 'an answered card is not withdrawn')
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('the turn ends', () => status(pid) === 'done', SLOW)
})

test('interrupt cancels every queued follow-up without applying its options', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('hold')
  await until('first prompt running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await n.peer.call('session.send', { session_id: pid, text: 'Queued one', model: 'queued-model' })
  await n.peer.call('session.send', { session_id: pid, text: 'Queued two' })
  assert.deepEqual(await n.peer.call('session.interrupt', { session_id: pid }), { ok: true, settled: true })
  await until('queue no longer working', () => status(pid) !== 'working', SLOW)
  assert.equal(received(record).filter((m) => m.method === 'session/prompt').length, 1)
  assert.equal(received(record).some((m) => m.params?.modelId === 'queued-model'), false)
})

test('permission cards use the runtime, session, kind and tool call key', async (t) => {
  const { n, start } = await agyRuntime(t)
  const pid = await start('ask')
  await until('permission card', () => n.asked.length > 0, SLOW)
  const { interactionId } = await import('../../runtime-sdk/index.js')
  assert.equal(n.asked[0].id, interactionId('agy', pid, 'permission', 'call-1'))
})


test('cancelling before a prompt starts does not poison the next permission request', async (t) => {
  const { n, record, start } = await agyRuntime(t)
  const pid = await start()
  n.resolve = () => ({ allow: true })
  const [, interrupted] = await Promise.all([
    n.peer.call('session.send', { session_id: pid, text: 'hold' }),
    n.peer.call('session.interrupt', { session_id: pid }),
  ])
  assert.equal(interrupted.settled, true)
  assert.equal(received(record).some((call) => call.method === 'session/prompt'), false)
  await n.peer.call('session.send', { session_id: pid, text: 'ask' })
  await until('next permission answered', () => permissionAnswers(record).length > 0, SLOW)
  assert.equal(n.asked.length, 1)
  assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'allow' } })
  await n.peer.call('session.interrupt', { session_id: pid })
})

test('interrupt does not report settled while a later send is running', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('hold')
  await until('first prompt running', () => received(record).some((call) => call.method === 'session/prompt'), SLOW)
  let release, entered = false
  const gate = new Promise((resolve) => { release = resolve })
  const original = AgyAcpClient.prototype.cancel
  const mocked = t.mock.method(AgyAcpClient.prototype, 'cancel', async function (...args) {
    await original.apply(this, args)
    entered = true
    await gate
  })
  const interrupted = n.peer.call('session.interrupt', { session_id: pid })
  try {
    await until('old turn ended during cancel', () => entered && received(record).some((call) => call.ended === SESSION) && status(pid) !== 'working', SLOW)
    await n.peer.call('session.send', { session_id: pid, text: 'hold' })
    await until('later prompt running', () => received(record).filter((call) => call.method === 'session/prompt').length === 2, SLOW)
  } finally {
    release()
    mocked.mock.restore()
  }
  assert.notEqual((await interrupted).settled, true)
  await n.peer.call('session.interrupt', { session_id: pid })
})

test('an uncancelled successor clears cancellation left by a preparing turn', async (t) => {
  const { n, record, start } = await agyRuntime(t)
  const pid = await start()
  n.resolve = () => ({ allow: true })
  let release, entered = false, hold = true
  const gate = new Promise((resolve) => { release = resolve })
  const original = AgyAcpClient.prototype.ensureSessionLoaded
  const mocked = t.mock.method(AgyAcpClient.prototype, 'ensureSessionLoaded', async function (...args) {
    if (hold) {
      hold = false
      entered = true
      await gate
    }
    return original.apply(this, args)
  })
  const first = n.peer.call('session.send', { session_id: pid, text: 'hold' })
  let interrupted
  try {
    await until('first turn preparing', () => entered, SLOW)
    interrupted = n.peer.call('session.interrupt', { session_id: pid })
    await until('preparing turn cancelled', () => received(record).some((call) => call.method === 'session/cancel'), SLOW)
    assert.equal((await n.peer.call('session.send', { session_id: pid, text: 'ask' })).queued, true)
    assert.equal(received(record).some((call) => call.method === 'session/prompt'), false)
  } finally {
    release()
    mocked.mock.restore()
    await first
    await interrupted
  }
  await until('successor permission answered', () => permissionAnswers(record).length === 1, SLOW)
  assert.equal(received(record).filter((call) => call.method === 'session/prompt').length, 1)
  assert.equal(n.asked.length, 1)
  assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'allow' } })
  await n.peer.call('session.interrupt', { session_id: pid })
})
