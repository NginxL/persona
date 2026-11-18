// Run with PERSONAD=<personad> node integrations/runtime-sdk/e2e/usage.mjs.
// Real SDK -> runtime socket -> node -> hub notification, using synthetic counts only.
import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { createRuntime } from '../index.js'
import { startNode, until } from '../testing/node-harness.mjs'

const node = await startNode({ personad: process.env.PERSONAD })
const rt = createRuntime({
  listSessions: async () => [{ session_id: 'usage-own', agent: 'grok', status: 'idle' }, { session_id: 'usage-file', agent: 'claude', status: 'idle' }],
  items: async () => ({ items: [], to_seq: 0 }),
}, { runtime: 'usage-toy', agent: 'grok', socketPath: node.runtimeSocket, refreshMs: 0 })
try {
  rt.start()
  await until('owned session at the hub', () => node.sessions().some(s => s.session_id === 'usage-own'))
  assert.equal(rt.nodeHas('usage'), true)
  const p = { session_id: 'usage-own', ts: new Date().toISOString(), model: 'toy-model', request_id: 'same', input: 10, output: 20, cache_read: 30, cache_write: 40, reasoning: 5 }
  rt.usage(p)
  rt.usage(p)
  rt.usage({ ...p, session_id: 'usage-file' })
  rt.usage({ ...p, session_id: 'foreign' })
  rt.usage({ ...p, request_id: 'barrier' })
  const events = await until('two admitted requests', () => {
    const a = node.events.filter(e => e.event === 'usage' && e.params.add)
    return a.length ? a : undefined
  }, 15_000)
  assert.equal(events.length, 1)
  const { params } = events[0]
  assert.equal(params.full, undefined)
  assert.deepEqual(params.buckets, [{ minute: Math.floor(Date.parse(p.ts) / 60000) * 60, model: 'toy-model', agent: 'grok', in: 20, out: 40, cache_read: 60, cache_write: 80, req: 2 }])
  console.log('PASS runtime usage: real SDK/node transport, owned agent attribution, dedupe, file-agent exclusion and foreign-session refusal')
} finally {
  rt.stop()
  await node.stopAndWait()
}

const root = mkdtempSync('/tmp/runtime-usage-')
let daemon, remoteNode
try {
  daemon = createRuntime({
    listSessions: async () => [{ session_id: 'own', agent: 'grok', status: 'idle' }, { session_id: 'file', agent: 'claude', status: 'idle' }],
    items: async () => ({ items: [], to_seq: 0 }),
  }, { runtime: 'usage-daemon', agent: 'grok', listen: { port: 0, token: 'synthetic-test-token' }, refreshMs: 0 })
  const address = await daemon.start()
  const home = join(root, 'home')
  mkdirSync(join(home, '.persona'), { recursive: true })
  writeFileSync(join(home, '.persona', 'daemons.json'), JSON.stringify({ daemons: [{ id: 'remote', url: `ws://127.0.0.1:${address.port}/persona/runtime`, token: 'synthetic-test-token' }] }), { mode: 0o600 })
  remoteNode = await startNode({ personad: process.env.PERSONAD, home })
  await until('namespaced daemon session', () => remoteNode.sessions().some(s => s.session_id === 'remote.own'))
  assert.equal(daemon.nodeHas('usage'), true)
  const p = { session_id: 'own', ts: new Date().toISOString(), model: 'toy-model', request_id: 'r1', output: 12 }
  daemon.usage(p)
  daemon.usage(p)
  daemon.usage({ ...p, session_id: 'file' })
  daemon.usage({ ...p, session_id: 'foreign' })
  daemon.usage({ ...p, request_id: 'r2' })
  const events = await until('daemon usage at the hub', () => {
    const a = remoteNode.events.filter(e => e.event === 'usage' && e.params.add)
    return a.length ? a : undefined
  }, 15_000)
  assert.equal(events.length, 1)
  assert.deepEqual(events[0].params.buckets, [{ minute: Math.floor(Date.parse(p.ts) / 60000) * 60, model: 'toy-model', agent: 'grok', out: 24, req: 2 }])
  console.log('PASS daemon runtime usage: real WebSocket link, namespaced ownership, file-agent exclusion and dedupe')
} finally {
  daemon?.stop()
  await remoteNode?.stopAndWait()
  rmSync(root, { recursive: true, force: true })
}
