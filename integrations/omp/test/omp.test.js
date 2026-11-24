import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { join } from 'node:path'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { agentDir, ompConfigPath, oneShot } from '../lib/compat.js'
import { host, options } from '../lib/host.js'

// ompDir is an agent directory with Persona's settings in it.
function ompDir(t, config) {
  const dir = mkdtempSync(join(tmpdir(), 'omp-agent-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  if (config) writeFileSync(ompConfigPath(dir), JSON.stringify(config))
  return dir
}

function clearSlots() {
  delete globalThis.__personaOmp
  delete globalThis.__personaOmpAskUser
  delete globalThis.__personaPi
}

function deps() {
  return { SessionManager: { listAll: async () => [], open: () => ({}) }, pid: 4242, log: () => {} }
}

test('the agent directory is omp\'s own unless PI_CODING_AGENT_DIR names one', () => {
  assert.equal(agentDir({}), join(homedir(), '.omp', 'agent'))
  assert.equal(agentDir({ PI_CODING_AGENT_DIR: '/somewhere/agent' }), '/somewhere/agent')
  assert.equal(agentDir({ PI_CODING_AGENT_DIR: '  ' }), join(homedir(), '.omp', 'agent'))
  assert.equal(ompConfigPath('/a'), join('/a', 'persona.json'))
})

test('a one-shot run is left alone', () => {
  for (const argv of [
    ['omp', '-p', 'hi'],
    ['omp', '--print'],
    ['omp', '--print=true', 'hi'],
    ['omp', '--print=false', 'hi'],
    ['omp', '--mode', 'json'],
    ['omp', '--mode=json'],
  ]) {
    assert.equal(oneShot(argv), true, argv.join(' '))
  }
  for (const argv of [['omp'], ['omp', 'hi'], ['omp', 'explain this json']]) assert.equal(oneShot(argv), false, argv.join(' '))
})

test('settings are the pi extension\'s, read from omp\'s own directory', (t) => {
  const dir = ompDir(t, { approve: ['bash', 7, 'write'], answerFirst: 'phone', askUser: false, socket: '/r.sock' })
  const o = options({ PI_CODING_AGENT_DIR: dir }, 4242)
  assert.deepEqual(o.approve, ['bash', 'write'])
  assert.equal(o.answerFirst, 'phone')
  assert.equal(o.askUser, false)
  assert.equal(o.socketPath, '/r.sock')
  assert.equal(o.agent, 'omp')
  assert.equal(o.name, 'omp')
  assert.equal(o.instance, 'omp-4242')
  assert.equal(o.interactionPrefix, 'omp')
  assert.deepEqual(o.resumeCommand, ['omp', '--resume'])
  assert.deepEqual(o.settleEvents, ['agent_end'], 'omp dispatches no agent_settled; a turn ends with agent_end')

  const d = options({ PI_CODING_AGENT_DIR: ompDir(t, null) }, 1)
  assert.deepEqual(d.approve, [])
  assert.equal(d.answerFirst, 'auto')
  assert.equal(d.askUser, true)
  assert.equal(d.socketPath, undefined)
})

test('the process connects to the node as omp, in the omp slot', async (t) => {
  clearSlots()
  t.after(clearSlots)
  const n = await startFakeNode(t)
  const dir = ompDir(t, { socket: n.path })
  const env = { PI_CODING_AGENT_DIR: dir }
  const a = host(deps(), env)
  t.after(() => a.stop())
  a.start() // omp calls register per session; a process with no session open still connects
  await n.ready
  assert.equal(n.hello.runtime, 'omp-4242')
  assert.equal(n.hello.agent, 'omp')
  assert.equal(n.hello.name, 'omp')
  assert.equal(n.hello.instance, 'omp-4242')
  assert.equal(n.hello.launcher, undefined, 'a terminal omp starts no sessions')

  const piSlot = { host: { stop() {} } }
  globalThis.__personaPi = piSlot
  const b = host(deps(), env)
  assert.equal(b, a, 'one host per process')
  assert.equal(globalThis.__personaPi, piSlot, 'the pi slot is untouched')
})

test('a session the host serves stops listing as working when its turn ends (omp dispatches no agent_settled)', async (t) => {
  clearSlots()
  t.after(clearSlots)
  const n = await startFakeNode(t)
  const a = host(deps(), { PI_CODING_AGENT_DIR: ompDir(t, { socket: n.path }) })
  t.after(() => a.stop())
  const handlers = new Map()
  const entries = [
    { type: 'message', id: 'u1', parentId: null, message: { role: 'user', content: 'one' } },
    { type: 'message', id: 'a1', parentId: 'u1', message: { role: 'assistant', content: [{ type: 'text', text: 'done' }] } },
  ]
  const sm = {
    entries, leaf: 'a1',
    getSessionId: () => 's-end', getCwd: () => '/w/app', getSessionName: () => '', getEntries: () => entries,
    getBranch: () => entries,
  }
  const ctx = { sessionManager: sm, mode: 'tui' }
  const pi = { on: (e, fn) => (handlers.get(e) ?? handlers.set(e, []).get(e)).push(fn) }
  a.register(pi)
  const fire = (e) => { for (const fn of handlers.get(e) ?? []) fn({}, ctx) }
  fire('session_start')
  await n.ready
  const status = () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 's-end')?.status
  await until('listed done', () => status() === 'done')
  fire('agent_start')
  await until('working', () => status() === 'working')
  fire('agent_end')
  await until('back to done', () => status() === 'done')
})
