import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { toAgyId } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const fake = fileURLToPath(new URL('./fake-acp-policy.mjs', import.meta.url))

async function fixture(t, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-isolation-'))
  const binary = join(home, 'agy')
  const record = join(home, 'requests.jsonl')
  const assistantDir = join(home, '.persona', 'assistant')
  mkdirSync(assistantDir, { recursive: true })
  writeFileSync(record, '')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${fake}' --record '${record}' "$@"\n`, { mode: 0o700 })
  const node = await startFakeNode(t, { features: ['assistant_pids'] })
  const runtime = createAgyRuntime({ home, agyBin: binary, personadBin: binary, socketPath: node.path, log: () => {}, ...options })
  t.after(async () => { await runtime.stop(); rmSync(home, { recursive: true, force: true }) })
  await runtime.start()
  await node.ready
  const calls = () => readFileSync(record, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  const pids = () => node.last('runtime.assistant_pids')?.pids ?? {}
  const start = async (params) => (await node.peer.call('session.start', { cwd: home, ...params })).session.session_id
  const call = (method, session_id, params = {}) => node.peer.call(`session.${method}`, { session_id, ...params })
  const request = (method, session) => calls().findLast((entry) => entry.method === method && entry.params?.sessionId === toAgyId(session))
  return { node, runtime, assistantDir, calls, pids, start, call, request }
}

test('Agents share their own worker; a stall, recycle, or crash there does not interrupt an Assistant', async (t) => {
  const { node, runtime, assistantDir, calls, pids, start, call, request } = await fixture(t, { promptInactivityMs: 200 })
  const assistant = await start({ cwd: assistantDir })
  const otherAssistant = await start({ assistant: true })
  const agent = await start()
  const otherAgent = await start()
  await until('both assistant workers reported', () => pids()[assistant] && pids()[otherAssistant])
  const workers = { ...pids() }
  assert.notEqual(workers[assistant], workers[otherAssistant])
  const shared = request('session/resume', agent).worker
  assert.equal(request('session/resume', otherAgent).worker, shared)
  assert.ok(!Object.values(workers).includes(shared))
  assert.equal(calls().filter((entry) => entry.started).length, 3)

  const permission = Promise.withResolvers()
  node.resolve = () => permission.promise
  await call('send', assistant, { text: 'ask' })
  await until('Assistant permission', () => node.asked.some((card) => card.session_id === assistant))
  await call('send', agent, { text: 'hold' })
  await until('Agent inactivity reported', () => node.last('runtime.sessions')?.sessions.some((row) => row.session_id === agent && row.turn_error))
  await call('modes', otherAgent, { model: 'test-model' })
  const recycled = request('session/set_model', otherAgent).worker
  assert.notEqual(recycled, shared)
  assert.throws(() => process.kill(shared, 0), { code: 'ESRCH' })
  process.kill(recycled, 'SIGKILL')
  await until('crashed Agent worker replaced', async () => {
    try {
      await call('modes', agent, { model: 'test-model' })
      return request('session/set_model', agent).worker !== recycled
    } catch (error) {
      if (!/agy acp (exited|process|stdin)|write EPIPE/.test(error.message)) throw error
      return false
    }
  })

  assert.deepEqual(pids(), workers)
  await call('modes', otherAssistant, { model: 'test-model' })
  assert.equal(request('session/set_model', otherAssistant).worker, workers[otherAssistant])
  assert.equal(request('session/prompt', assistant).worker, workers[assistant])
  assert.equal((await call('interrupt', assistant)).settled, true)
  assert.equal(request('session/cancel', assistant).worker, workers[assistant])
  permission.resolve({ allow: true })
  await runtime.stop()
  await until('all stop-time PID reports cleared', () => Object.keys(pids()).length === 0)
  for (const entry of calls().filter((entry) => entry.started)) assert.throws(() => process.kill(entry.started, 0), { code: 'ESRCH' })
})

test('idle reaping spares active prompts and even permissions requested outside a turn', async (t) => {
  const { node, pids, start, call, request, assistantDir } = await fixture(t, { assistantAcpIdleMs: 350, promptInactivityMs: 0 })
  const pending = Promise.withResolvers()
  node.resolve = () => pending.promise
  const busy = await start({ cwd: assistantDir, prompt: 'hold' })
  const waiting = await start({ cwd: assistantDir })
  await call('modes', waiting, { effort: 'ask-idle' })
  await until('permission outside a prompt', () => node.asked.some((card) => card.session_id === waiting))
  assert.equal(request('session/prompt', waiting), undefined)
  const idle = await start({ cwd: assistantDir })
  const oldPID = await until('idle PID reported', () => pids()[idle])
  const busyPID = pids()[busy]
  const waitingPID = pids()[waiting]
  await until('only the idle worker reaped', () => !pids()[idle])
  await until('idle process gone', () => {
    try { process.kill(oldPID, 0); return false } catch (error) { assert.equal(error.code, 'ESRCH'); return true }
  })
  assert.deepEqual(pids(), { [busy]: busyPID, [waiting]: waitingPID })
  assert.equal((await call('interrupt', busy)).settled, true)
  pending.resolve({ allow: true })
  await call('modes', idle, { model: 'test-model' })
  const resumedPID = await until('reloaded worker reported', () => pids()[idle])
  assert.notEqual(resumedPID, oldPID)
  const resumed = request('session/resume', idle)
  assert.equal(resumed.worker, resumedPID)
  assert.ok(resumed.params.mcpServers[0].env.some(({ name, value }) => name === 'PERSONA_ASSISTANT' && value === '1'))
  await until('interrupted and approved workers also reaped', () => !pids()[busy] && !pids()[waiting])
})

test('an unexpected Assistant worker exit clears just its PID; the next send resumes on a fresh worker', async (t) => {
  const { pids, start, call, request } = await fixture(t)
  const crashed = await start({ assistant: true })
  const stable = await start({ assistant: true })
  const stablePID = await until('stable worker reported', () => pids()[stable])
  const crashedPID = pids()[crashed]
  process.kill(crashedPID, 'SIGKILL')
  await until('only crashed worker cleared', () => !pids()[crashed])
  assert.deepEqual(pids(), { [stable]: stablePID })
  await call('send', crashed, { text: 'continue' })
  const restarted = await until('replacement reported', () => pids()[crashed])
  assert.notEqual(restarted, crashedPID)
  assert.equal((await until('prompt reaches the replacement', () => request('session/prompt', crashed))).worker, restarted)
  await call('close', crashed)
  await until('closed worker removed', () => !pids()[crashed])
  assert.deepEqual(pids(), { [stable]: stablePID })
  assert.throws(() => process.kill(restarted, 0), { code: 'ESRCH' })
})

test('a send during idle retirement waits for the old worker to exit before starting a replacement', async (t) => {
  const { pids, start, call, calls, request } = await fixture(t, {
    agyArgs: ['--ignore-term'], assistantAcpIdleMs: 350,
  })
  const session = await start({ assistant: true })
  const oldPID = await until('original worker reported', () => pids()[session])
  await until('old worker is retiring', () => !pids()[session])
  process.kill(oldPID, 0)
  await call('send', session, { text: 'resume' })
  assert.throws(() => process.kill(oldPID, 0), { code: 'ESRCH' })
  const resumed = await until('replacement worker receives prompt', () => request('session/prompt', session))
  assert.notEqual(resumed.worker, oldPID)
  assert.deepEqual(calls().filter((entry) => entry.started).map((entry) => entry.started), [oldPID, resumed.worker])
  process.kill(resumed.worker, 'SIGKILL')
})
