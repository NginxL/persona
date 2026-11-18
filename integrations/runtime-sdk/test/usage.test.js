import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createRuntime } from '../index.js'
import { startFakeNode, until } from '../testing/fake-node.js'

const driver = { listSessions: async () => [{ session_id: 's1', status: 'idle' }], items: async () => ({ items: [], to_seq: 0 }) }
const report = () => ({ session_id: 's1', ts: new Date().toISOString(), model: 'toy', request_id: 'r1', input: 10, output: 20, cache_read: 30, cache_write: 40, reasoning: 7 })

test('usage reports require negotiated support and send per-request counts without adding reasoning twice', async t => {
  const n = await startFakeNode(t, { features: ['usage'] })
  const rt = createRuntime(driver, { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  const p = report()
  assert.equal(rt.usage(p), false, 'offline reports are not replayed')
  rt.start()
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.nodeHas('usage'), true)
  assert.equal(n.last('usage.report'), undefined)
  assert.equal(rt.usage({ ...p, agent: 'spoofed', launcher: 'spoofed' }), true)
  await until('usage', () => n.last('usage.report'))
  assert.deepEqual(n.last('usage.report'), p)
  for (const invalid of [{ input: -1 }, { output: 1.5 }, { reasoning: 21 }, { input: Number.MAX_SAFE_INTEGER }, { ts: 'invalid' }, { ts: '2026' }, { model: '🚀'.repeat(100) }, { session_id: '' }]) {
    assert.throws(() => rt.usage({ ...p, ...invalid }), TypeError)
  }
})

test('an older node receives no usage events', async t => {
  const n = await startFakeNode(t, { features: [] })
  const rt = createRuntime(driver, { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  rt.start()
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.usage(report()), false)
  assert.equal(n.last('usage.report'), undefined)
})
