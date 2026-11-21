import assert from 'node:assert/strict'
import { test } from 'node:test'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync, existsSync, chmodSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createOpencodeCompanion } from '../lib/companion.js'
import { createSupervisionStore } from '../lib/supervision.js'

async function fixture(t, { append = false, probeFails = false } = {}) {
  const home = mkdtempSync(join(tmpdir(), 'opencode-runtime-policy-'))
  const cwd = join(home, '.persona', 'assistant')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'AGENTS.md'), 'shared original')
  const record = join(home, 'record.jsonl')
  writeFileSync(record, '')
  const env = { ...process.env, OPENCODE_CONFIG_CONTENT: '{}', FAKE_OPENCODE_RECORD: record, FAKE_OPENCODE_VERSION: '1.19.6', ...(append ? { FAKE_OPENCODE_SYSTEM_PROMPT: '1' } : {}) }
  const node = await startFakeNode(t, { features: ['assistant_pids', 'prefer_running'] })
  let refusedText
  const store = createSupervisionStore({ home, env, bin: 'fake-probe', directory: join(home, 'policies'),
    run: async (_, args, { env }) => {
      if (args[0] === '--version') return { stdout: '1.19.6' }
      const config = JSON.parse(env.OPENCODE_CONFIG_CONTENT)
      if (refusedText && config.instructions.some((file) => readFileSync(file, 'utf8') === refusedText)) throw new Error('fake rebuild failure')
      return { stdout: JSON.stringify({ ...config, ...(probeFails ? { instructions: [] } : {}) }) }
    },
    acpFactory: () => ({ ensureStarted: async () => ({ capabilities: { sessionCapabilities: append ? { systemPrompt: {} } : {} }, agentInfo: { version: '1.19.6' } }), stop: async () => {} }),
  })
  const companion = createOpencodeCompanion({ home, env, bin: process.execPath, args: [fileURLToPath(new URL('./fake-acp-worker.mjs', import.meta.url))],
    socketPath: node.path, retryMs: 20, pushDelayMs: 5, policyStore: store,
    openDb: async () => ({ all: () => [], close() {} }),
    discoverModels: async () => [{ providerID: 'mock', id: 'one', name: 'One' }, { providerID: 'mock', id: 'two', name: 'Two', variants: { high: {} } }],
  })
  t.after(async () => { await companion.stop(); rmSync(home, { recursive: true, force: true }) })
  await companion.start(); await node.ready
  const calls = () => readFileSync(record, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  const start = async (params = {}) => (await node.peer.call('session.start', { cwd, ...params })).session.session_id
  const call = (method, id, params = {}) => node.peer.call('session.' + method, { session_id: id, ...params })
  const settled = (id) => until('turn settled', () => !companion.live.get(id).running && !companion.live.get(id).queued)
  return { home, cwd, companion, node, store, calls, start, call, settled, refuse(text) { refusedText = text } }
}

test('startup effectiveness failure advertises no supervision capability and starts or sends nothing for policy deliveries', async (t) => {
  const f = await fixture(t, { probeFails: true })
  assert.equal(f.node.hello.caps.supervision_policy, undefined)
  await assert.rejects(f.start({ assistant: true, supervision_instructions: 'policy' }), (error) => error.code === 'assistant_supervision_runtime')
  assert.equal(f.calls().length, 0)
})

test('concurrent policies stay in dedicated process configuration; ordinary Agents and shared workspace bytes stay unchanged', async (t) => {
  const f = await fixture(t)
  assert.equal(f.node.hello.caps.supervision_policy, true)
  const a = await f.start({ assistant: true, supervision_instructions: 'policy A' })
  const b = await f.start({ assistant: true, supervision_instructions: 'policy B' })
  const agent = await f.start({ cwd: join(f.home, 'ordinary') })
  const workers = f.calls().filter((call) => call.started)
  assert.equal(workers.length, 3)
  assert.deepEqual(workers.map((call) => call.instructions.map((file) => file.text)), [['policy A'], ['policy B'], []])
  await Promise.all([f.call('send', a, { text: 'hello A', supervision_instructions: 'policy A' }), f.call('send', b, { text: 'hello B', supervision_instructions: 'policy B' }), f.call('send', agent, { text: 'hello Agent' })])
  await Promise.all([f.settled(a), f.settled(b), f.settled(agent)])
  for (const call of f.calls().filter((call) => call.method === 'session/prompt')) assert.equal(JSON.stringify(call.params.prompt).includes('policy '), false)
  assert.equal(readFileSync(join(f.cwd, 'AGENTS.md'), 'utf8'), 'shared original')
})

test('idle policy changes resume without transcript replay, restore options, clear on demotion and roll back a failed rebuild without sending', async (t) => {
  const f = await fixture(t)
  const id = await f.start({ assistant: true, supervision_instructions: 'policy A', model: 'mock/two', effort: 'high', mode: 'plan' })
  const original = f.companion.live.get(id).policyRecord
  const firstPID = f.companion.live.get(id).worker.acp.pid
  await f.call('send', id, { text: 'hold', supervision_instructions: 'policy A' })
  await until('active turn', () => f.calls().some((call) => call.method === 'session/prompt'))
  await assert.rejects(f.call('send', id, { text: 'must not run', supervision_instructions: 'policy B' }), (error) => error.code === 'busy')
  await f.call('interrupt', id)
  await f.call('send', id, { text: 'new policy input', supervision_instructions: 'policy B' })
  await f.settled(id)
  const state = f.companion.live.get(id)
  assert.notEqual(state.worker.acp.pid, firstPID)
  assert.equal(state.policy, 'policy B')
  assert.equal(existsSync(original.file), false)
  assert.equal(state.model, 'mock/two'); assert.equal(state.effort, 'high'); assert.equal(state.mode, 'plan')
  assert.equal(f.calls().some((call) => call.method === 'session/load'), false)
  assert.ok(f.calls().some((call) => call.method === 'session/resume' && call.params.sessionId === id))
  const previous = state.policyRecord
  await assert.rejects(f.call('send', id, { text: 'invalid input', model: 'unknown-model', supervision_instructions: 'must not commit' }), (error) => error.code === 'bad_request')
  assert.equal(state.policyRecord, previous)
  assert.equal(state.policy, 'policy B')
  f.refuse('failing policy')
  await assert.rejects(f.call('send', id, { text: 'failed input', supervision_instructions: 'failing policy' }), (error) => error.code === 'assistant_supervision_runtime')
  assert.equal(state.policy, 'policy B'); assert.equal(readFileSync(previous.file, 'utf8'), 'policy B')
  assert.equal(f.calls().some((call) => call.method === 'session/prompt' && JSON.stringify(call.params).includes('failed input')), false)
  await f.call('send', id, { text: 'ordinary input', supervision_instructions: '' })
  await f.settled(id)
  assert.equal(state.policyRecord, null); assert.equal(existsSync(previous.file), false)
  const last = f.calls().findLast((call) => call.started)
  assert.deepEqual(last.instructions, [])
  await f.call('close', id)
})

test('ACP systemPrompt append takes precedence over process instructions and is replayed on resume', async (t) => {
  const f = await fixture(t, { append: true })
  const id = await f.start({ assistant: true, supervision_instructions: 'append policy' })
  assert.deepEqual(f.calls().find((call) => call.method === 'session/new').params.systemPrompt, { mode: 'append', content: [{ type: 'text', text: 'append policy' }] })
  assert.deepEqual(f.calls().find((call) => call.started).instructions, [])
  await f.companion.assistantWorkers.discard(f.companion.live.get(id).worker)
  await f.call('send', id, { text: 'resume', supervision_instructions: 'append policy' })
  await f.settled(id)
  assert.equal(f.calls().find((call) => call.method === 'session/resume').params.systemPrompt.content[0].text, 'append policy')
  await f.call('send', id, { text: 'clear', supervision_instructions: '' })
  await f.settled(id)
  assert.equal(f.calls().findLast((call) => call.method === 'session/resume').params.systemPrompt, null)
})

test('a queued/in-flight mode change keeps its worker and prevents a concurrent policy rebuild', async (t) => {
  const f = await fixture(t)
  const id = await f.start({ assistant: true, supervision_instructions: 'original' })
  const worker = f.companion.live.get(id).worker
  const entered = Promise.withResolvers(), release = Promise.withResolvers()
  const set = worker.acp.setConfigOption.bind(worker.acp)
  worker.acp.setConfigOption = async (...args) => { entered.resolve(); await release.promise; return set(...args) }
  const control = f.call('modes', id, { effort: 'high' })
  await entered.promise
  try {
    await assert.rejects(f.call('send', id, { text: 'blocked input', supervision_instructions: 'new policy' }), (error) => error.code === 'busy')
    assert.equal(f.companion.live.get(id).worker, worker)
    assert.equal(f.companion.live.get(id).policy, 'original')
  } finally { release.resolve() }
  await control
})

test('failure to prepare an unwritable policy preserves the existing idle worker and sends nothing', { skip: process.platform === 'win32' || process.getuid?.() === 0 }, async (t) => {
  const f = await fixture(t)
  const id = await f.start({ assistant: true, supervision_instructions: 'original' })
  const state = f.companion.live.get(id), worker = state.worker
  const root = join(f.home, 'policies')
  chmodSync(root, 0o500)
  try {
    await assert.rejects(f.call('send', id, { text: 'unwritable input', supervision_instructions: 'new policy' }), (error) => error.code === 'assistant_supervision_runtime')
    assert.equal(state.worker, worker)
    assert.equal(state.policy, 'original')
    assert.equal(f.calls().some((call) => call.method === 'session/prompt'), false)
  } finally { chmodSync(root, 0o700) }
})

test('interrupting policy admission restores the previous role even after its old file was removed', async (t) => {
  const f = await fixture(t)
  const id = await f.start({ assistant: true, supervision_instructions: 'original' })
  const state = f.companion.live.get(id), previous = state.policyRecord
  const entered = Promise.withResolvers(), release = Promise.withResolvers()
  const remove = f.store.remove.bind(f.store)
  f.store.remove = async (record) => {
    if (record === previous) { entered.resolve(); await release.promise }
    return remove(record)
  }
  const sending = f.call('send', id, { text: 'cancelled input', supervision_instructions: 'new policy' })
  const refused = assert.rejects(sending, (error) => error.code === 'busy' && error.data?.delivered === 'none')
  await entered.promise
  await f.call('interrupt', id)
  release.resolve()
  await refused
  assert.equal(state.policy, 'original')
  assert.equal(readFileSync(state.policyRecord.file, 'utf8'), 'original')
  assert.equal(state.policyFlight, false)
  assert.equal(f.calls().some((call) => call.method === 'session/prompt'), false)
  await f.call('send', id, { text: 'valid retry', supervision_instructions: 'original' })
  await f.settled(id)
  assert.equal(state.turnError, '')
})
