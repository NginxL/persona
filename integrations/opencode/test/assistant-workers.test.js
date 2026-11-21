import assert from 'node:assert/strict'
import { test } from 'node:test'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createOpencodeCompanion } from '../lib/companion.js'
import { createAssistantWorkers } from '../lib/assistant-workers.js'

async function fixture(t, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'opencode-workers-'))
  const cwd = join(home, 'shared-project')
  mkdirSync(cwd)
  const record = join(home, 'requests.jsonl')
  writeFileSync(record, '')
  const node = await startFakeNode(t, { features: ['assistant_pids', 'prefer_running'], tools: [{ name: 'probe', description: 'Fake probe', input_schema: { type: 'object', properties: {} } }] })
  const companion = createOpencodeCompanion({
    home, socketPath: node.path, bin: process.execPath,
    args: [fileURLToPath(new URL('./fake-acp-worker.mjs', import.meta.url))],
    env: { ...process.env, FAKE_OPENCODE_RECORD: record, ...(options.workerEnv ?? {}) },
    retryMs: 20, pushDelayMs: 5, assistantReapMs: 10,
    openDb: async () => ({ all: () => [], close() {} }),
    discoverModels: async () => [{ providerID: 'mock', id: 'one', name: 'One', variants: { low: {}, high: {} } }, { providerID: 'mock', id: 'two', name: 'Two', variants: { low: {}, high: {} } }],
    ...options,
  })
  t.after(async () => { await companion.stop(); rmSync(home, { recursive: true, force: true }) })
  await companion.start()
  await node.ready
  const calls = () => readFileSync(record, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  const pids = () => node.last('runtime.assistant_pids')?.pids ?? {}
  const start = async (params = {}) => (await node.peer.call('session.start', { cwd, ...params })).session.session_id
  const call = (method, id, params = {}) => node.peer.call('session.' + method, { session_id: id, ...params })
  const row = (id) => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === id)
  return { node, companion, calls, pids, start, call, row }
}

test('same-directory assistants have independent processes/MCP identities; ordinary Agents retain the shared process', async (t) => {
  const f = await fixture(t)
  const a = await f.start({ assistant: true })
  const b = await f.start({ assistant: true })
  const ordinary = await f.start()
  const sibling = await f.start()
  await until('dedicated worker PIDs', () => f.pids()[a] && f.pids()[b])
  assert.notEqual(f.pids()[a], f.pids()[b])
  const created = f.calls().filter((r) => r.method === 'session/new')
  assert.equal(created.length, 4)
  assert.equal(created[2].worker, created[3].worker)
  assert.ok(!Object.values(f.pids()).includes(created[2].worker))
  const spawned = f.calls().filter((r) => r.started)
  for (const worker of spawned.slice(0, 2)) {
    assert.equal(worker.args[worker.args.indexOf('--port') + 1], '0')
    assert.equal(worker.args[worker.args.indexOf('--hostname') + 1], '127.0.0.1')
    assert.ok(worker.args.includes('--no-mdns'))
  }
  assert.equal(spawned[2].args.includes('--port'), false, 'ordinary shared-process arguments retain their prior behavior')
  assert.equal(f.node.hello.caps.supervision_policy, undefined)
  await Promise.all([f.call('send', a, { text: 'probe' }), f.call('send', b, { text: 'probe' })])
  await until('both MCP calls', () => f.node.toolCalls.length === 2)
  assert.deepEqual(f.node.toolCalls.map((r) => r.session_id).sort(), [a, b].sort())
  const before = f.calls().filter((r) => r.ended === b).length
  await f.call('send', ordinary, { text: 'foreign:' + a })
  await f.call('send', b, { text: 'foreign:' + a })
  await until('ordinary and sibling turns settled', () => f.calls().some((r) => r.ended === ordinary) && f.calls().filter((r) => r.ended === b).length > before)
  const items = await f.node.peer.call('session.items', { session_id: a })
  assert.equal(items.items.some((r) => r.text === 'must not cross sessions'), false)
  assert.equal(f.companion.live.get(sibling).assistant, false)
})

test('crashing a worker fails its active turn, withdraws its card and PID, and spares another assistant', async (t) => {
  const f = await fixture(t)
  const a = await f.start({ assistant: true })
  const b = await f.start({ assistant: true })
  await until('PIDs', () => f.pids()[a] && f.pids()[b])
  const pid = f.pids()[a], stable = f.pids()[b]
  const answer = Promise.withResolvers()
  f.node.resolve = () => answer.promise
  await f.call('send', a, { text: 'ask' })
  await until('permission card', () => f.node.asked.some((r) => r.session_id === a))
  process.kill(pid, 'SIGKILL')
  await until('failed turn and cleared PID', () => f.row(a)?.turn_error && !f.pids()[a] && f.row(a)?.status !== 'working')
  assert.equal(f.pids()[b], stable)
  await until('permission withdrawn', () => f.node.all('interaction.cancel').some((r) => r.id === f.node.asked[0].id))
  await f.call('send', a, { text: 'resume' })
  await until('replacement resumed', () => f.pids()[a] && f.pids()[a] !== pid && f.calls().some((r) => r.method === 'session/resume' && r.params.sessionId === a && r.worker === f.pids()[a]))
  const resumed = f.calls().findLast((r) => r.method === 'session/resume' && r.params.sessionId === a)
  assert.equal(resumed.worker, f.pids()[a])
  assert.equal(f.calls().some((r) => r.method === 'session/load' && r.params.sessionId === a), false)
  answer.resolve({ allow: true })
})

test('idle reaping preserves active, queued and permission-held workers; resume replays acknowledged execution options', async (t) => {
  const f = await fixture(t, { assistantIdleMs: 250 })
  const busy = await f.start({ assistant: true, prompt: 'hold' })
  await f.call('send', busy, { text: 'queued' })
  const pending = Promise.withResolvers()
  f.node.resolve = () => pending.promise
  const asking = await f.start({ assistant: true, prompt: 'ask' })
  await until('permission', () => f.node.asked.some((r) => r.session_id === asking))
  const idle = await f.start({ assistant: true, model: 'mock/two', effort: 'high', mode: 'plan' })
  await until('idle PID', () => f.pids()[idle])
  const first = f.pids()[idle]
  const route = f.companion.live.get(idle).mcpRoute
  await until('only idle reaped', () => !f.pids()[idle] && f.pids()[busy] && f.pids()[asking])
  await until('idle process exited', () => { try { process.kill(first, 0); return false } catch (err) { assert.equal(err.code, 'ESRCH'); return true } })
  const invokeOldRoute = () => fetch(route.url, { method: 'POST', headers: { 'Content-Type': 'application/json', ...route.headersObject }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'probe', arguments: {} } }) })
  assert.equal((await invokeOldRoute()).status, 404)
  await f.call('send', idle, { text: 'resume' })
  await until('replayed options and prompt', () => f.calls().some((r) => r.ended === idle))
  const sequence = f.calls().filter((r) => r.params?.sessionId === idle && r.worker !== first)
  assert.equal(sequence[0].method, 'session/resume')
  const prompt = sequence.findIndex((r) => r.method === 'session/prompt')
  assert.notEqual(f.companion.live.get(idle).mcpRoute.url, route.url)
  assert.equal((await invokeOldRoute()).status, 404, 'retired token never regains authority after resume')
  assert.ok(sequence.slice(0, prompt).some((r) => r.method === 'session/set_config_option' && r.params.configId === 'model' && r.params.value === 'mock/two'))
  assert.ok(sequence.slice(0, prompt).some((r) => r.params.configId === 'variant' && r.params.value === 'high'))
  assert.ok(sequence.slice(0, prompt).some((r) => r.params.configId === 'mode' && r.params.value === 'plan'))
  await f.call('interrupt', busy)
  pending.resolve({ allow: true })
})

test('worker capacity returns busy when all are in use and otherwise retires only an idle worker', async (t) => {
  const f = await fixture(t, { maxAssistantWorkers: 2 })
  const a = await f.start({ assistant: true, prompt: 'hold' })
  const b = await f.start({ assistant: true, prompt: 'hold' })
  await until('both prompts active', () => f.calls().filter((r) => r.method === 'session/prompt').length === 2)
  await assert.rejects(f.start({ assistant: true }), (r) => r.code === 'busy')
  assert.equal(f.calls().filter((r) => r.started).length, 2)
  await f.call('interrupt', a)
  const old = f.pids()[a], other = f.pids()[b]
  const c = await f.start({ assistant: true })
  await until('replacement admitted', () => f.pids()[c])
  assert.equal(f.pids()[a], undefined)
  assert.equal(f.pids()[b], other)
  assert.throws(() => process.kill(old, 0), { code: 'ESRCH' })
  assert.notEqual(f.pids()[c], other)
})

test('admission reserves a slot before a native session id exists and an in-flight control/fork is never idle', async () => {
  let count = 0, timestamp = Date.now()
  const pool = createAssistantWorkers({ limit: 1, idleMs: 1, now: () => timestamp, create: () => ({ stop: async () => count-- }), busy: (s) => s.forking, retiring: () => {} })
  const first = await pool.reserve(); count++
  await assert.rejects(pool.reserve(), (r) => r.code === 'busy')
  pool.bind(first, { id: 'one', forking: true }); pool.release(first); timestamp += 100
  await pool.reap(); assert.equal(count, 1)
  await assert.rejects(pool.reserve(), (r) => r.code === 'busy')
  first.session.forking = false
  const leased = await pool.acquire(first.session)
  await pool.reap(); assert.equal(count, 1)
  pool.release(leased)
  timestamp += 2 // idle time starts when the final lease is released
  await pool.reap(); assert.equal(count, 0)
  await pool.stop()
})

test('policy rejection starts no worker and shutdown removes every owned execution process', async (t) => {
  const f = await fixture(t)
  await assert.rejects(f.start({ assistant: true, supervision_instructions: 'test policy' }), (err) => err.code === 'assistant_supervision_runtime')
  assert.equal(f.calls().length, 0)
  const a = await f.start({ assistant: true }), b = await f.start({ assistant: true })
  await until('worker PID publication', () => f.pids()[a] && f.pids()[b])
  const owned = [f.pids()[a], f.pids()[b]]
  await f.companion.stop()
  for (const pid of owned) assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' })
  assert.equal(f.companion.assistantWorkers.workers.size, 0)
})

test('one retirement failure does not strand other workers during shutdown', async () => {
  const stopped = []
  const pool = createAssistantWorkers({ create: () => ({ stop: async () => stopped.push(true) }), busy: () => false, retiring: (worker) => {
    if (worker.session.id === 'one') throw new Error('cleanup failed')
  } })
  for (const id of ['one', 'two']) {
    const worker = await pool.reserve(); pool.bind(worker, { id }); pool.release(worker)
  }
  await assert.rejects(pool.stop(), /cleanup failed/)
  assert.equal(stopped.length, 2)
  assert.equal(pool.workers.size, 0)
})

test('a crashed assistant group leader cannot leave its native server descendant alive', { skip: process.platform !== 'linux' }, async (t) => {
  const f = await fixture(t, { workerEnv: { FAKE_OPENCODE_CHILD: '1' } })
  const id = await f.start({ assistant: true })
  await until('owned process tree', () => f.pids()[id] && f.calls().some((r) => r.descendant))
  const child = f.calls().find((r) => r.descendant).descendant
  t.after(() => { try { process.kill(child, 'SIGKILL') } catch {} })
  process.kill(f.pids()[id], 'SIGKILL')
  await until('native descendant terminated', () => {
    try { return /^State:\s+Z/m.test(readFileSync(`/proc/${child}/status`, 'utf8')) }
    catch (err) { if (err.code === 'ENOENT') return true; throw err }
  })
})
