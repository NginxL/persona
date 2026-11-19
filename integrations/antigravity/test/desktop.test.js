import assert from 'node:assert/strict'
import { appendFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { startFakeNode } from '../../runtime-sdk/testing/fake-node.js'
import { createDesktopRuntime } from '../lib/desktop.js'

test('desktop attaches to existing sessions, follows transcripts, and routes only their controls', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const key = '11111111-2222-4333-8444-555555555555'
  const session_id = 'desktop-agy-' + key
  const folder = join(dir, 'brain', key, '.system_generated', 'logs')
  mkdirSync(folder, { recursive: true })
  const file = join(folder, 'transcript.jsonl')
  writeFileSync(file, JSON.stringify({ step_index: 0, type: 'USER_INPUT', content: 'hi' }) + '\n')
  const node = await startFakeNode(t)
  const calls = []
  const cascadeConfig = { plannerConfig: { planModel: "test-model" } }
  const runtime = createDesktopRuntime({ dir, socketPath: node.path, discover: async () => ({}),
    call: async (_, method, params) => {
      if (method === 'GetAllCascadeTrajectories') return { trajectorySummaries: { [key]: {
        summary: 'test session', lastModifiedTime: new Date().toISOString(), stepCount: 1,
      } } }
      if (method === 'GetCascadeTrajectorySteps') return { steps: [{ userInput: { userConfig: cascadeConfig } }] }
      calls.push({ method, params })
      return {}
    } })
  t.after(() => runtime.stop())
  await runtime.start()
  await node.ready
  const until = async fn => {
    for (let i = 0; i < 100 && !fn(); i++) await delay(25)
    assert.ok(fn())
  }
  await until(() => node.last('runtime.sessions'))
  assert.equal(node.last('runtime.sessions').sessions[0].session_id, session_id)
  const read = await node.peer.call('session.items', { session_id })
  assert.equal(read.items[0].text, 'hi')
  await node.peer.call('session.watch', { session_id })
  appendFileSync(file, JSON.stringify({ step_index: 1, type: 'PLANNER_RESPONSE', content: 'hello' }) + '\n')
  await until(() => node.last('session.items'))
  assert.equal(node.last('session.items').items[0].text, 'hello')
  await node.peer.call('session.send', { session_id, text: 'next', client_msg_id: 'one' })
  await node.peer.call('session.send', { session_id, text: 'next', client_msg_id: 'one' })
  await node.peer.call('session.interrupt', { session_id })
  await assert.rejects(node.peer.call('session.send', { session_id: 'agy-' + key, text: 'wrong' }), { code: 'not_found' })
  assert.deepEqual(calls, [
    { method: 'SendUserCascadeMessage', params: { cascadeId: key, items: [{ text: 'next' }], cascadeConfig, clientMessageId: 'one' } },
    { method: 'CancelCascadeInvocation', params: { cascadeId: key } },
  ])
})
