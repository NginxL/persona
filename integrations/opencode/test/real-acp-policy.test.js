// T2: a pinned real engine, deterministic fake model, no provider account.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createOpencodeCompanion } from '../lib/companion.js'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { fakeModel, environment } from './real-acp-fixture.mjs'

const bin = process.env.OPENCODE_CONTRACT_BIN
test('pinned process instructions preserve project/AGENTS instructions, survive compaction and remain session private', { skip: !bin, timeout: 180_000 }, async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'opencode-real-policy-'))
  const cwd = join(home, '.persona', 'assistant')
  mkdirSync(cwd, { recursive: true })
  const agents = join(cwd, 'AGENTS.md'), project = join(cwd, 'project-policy.md')
  writeFileSync(agents, 'SHARED_AGENTS_SENTINEL')
  writeFileSync(project, 'PROJECT_INSTRUCTIONS_SENTINEL')
  const projectConfig = join(cwd, 'opencode.json')
  // Seed the normal schema field; this pinned CLI migrates schema-less files.
  const originalConfig = JSON.stringify({ $schema: 'https://opencode.ai/config.json', instructions: [project] })
  writeFileSync(projectConfig, originalConfig)
  const model = await fakeModel(t)
  const env = environment(home, model.url)
  delete env.OPENCODE_DISABLE_PROJECT_CONFIG
  const node = await startFakeNode(t, { features: ['assistant_pids', 'prefer_running'] })
  const logs = []
  const companion = createOpencodeCompanion({ home, bin, env, socketPath: node.path, retryMs: 20, pushDelayMs: 5,
    log: (line) => logs.push(line), openDb: async () => ({ all: () => [], close() {} }),
    discoverModels: async () => [{ providerID: 'test', id: 'test-model', name: 'Test Model' }],
  })
  t.after(async () => { await companion.stop(); rmSync(home, { recursive: true, force: true }) })
  try {
    await companion.start(); await node.ready
    assert.equal(node.hello.caps.supervision_policy, true)
    const start = async (policy) => (await node.peer.call('session.start', { cwd, assistant: true, supervision_instructions: policy })).session.session_id
    const first = await start('POLICY_A_SENTINEL'), second = await start('POLICY_B_SENTINEL')
    const send = async (id, text, policy) => {
      await node.peer.call('session.send', { session_id: id, text, supervision_instructions: policy })
      await until('real policy turn settled', () => !companion.live.get(id).running && !companion.live.get(id).queued, 40_000)
      assert.equal(companion.live.get(id).turnError, '')
    }
    const systemFor = (marker) => {
      const request = model.requests.findLast((request) => request.messages?.some((m) => m.role === 'user' && JSON.stringify(m.content).includes(marker)))
      assert.ok(request, 'the real engine must reach the fake model')
      return JSON.stringify(request.messages.filter((message) => message.role === 'system' || message.role === 'developer'))
    }
    await Promise.all([send(first, 'POLICY_FIRST_TURN', 'POLICY_A_SENTINEL'), send(second, 'POLICY_SECOND_TURN', 'POLICY_B_SENTINEL')])
    for (const [marker, own, other] of [['POLICY_FIRST_TURN', 'POLICY_A_SENTINEL', 'POLICY_B_SENTINEL'], ['POLICY_SECOND_TURN', 'POLICY_B_SENTINEL', 'POLICY_A_SENTINEL']]) {
      const system = systemFor(marker)
      for (const expected of [own, 'SHARED_AGENTS_SENTINEL', 'PROJECT_INSTRUCTIONS_SENTINEL']) assert.ok(system.includes(expected), `${expected} must have system-level precedence`)
      assert.equal(system.includes(other), false)
    }
    const beforeCompaction = model.requests.length
    await send(first, '/compact', 'POLICY_A_SENTINEL')
    assert.ok(model.requests.length > beforeCompaction, 'native compaction must actually call the fake summary model')
    await send(first, 'POLICY_AFTER_COMPACTION', 'POLICY_A_SENTINEL')
    const after = systemFor('POLICY_AFTER_COMPACTION')
    for (const expected of ['POLICY_A_SENTINEL', 'SHARED_AGENTS_SENTINEL', 'PROJECT_INSTRUCTIONS_SENTINEL']) assert.ok(after.includes(expected))
    assert.equal(after.includes('POLICY_B_SENTINEL'), false)
    const formerFile = companion.live.get(first).policyRecord.file
    await send(first, 'POLICY_DEMOTED_TURN', '')
    assert.equal(systemFor('POLICY_DEMOTED_TURN').includes('POLICY_A_SENTINEL'), false)
    assert.equal(existsSync(formerFile), false)
    const siblingFile = companion.live.get(second).policyRecord.file
    await node.peer.call('session.close', { session_id: second })
    assert.equal(existsSync(siblingFile), false)
    assert.equal(readFileSync(agents, 'utf8'), 'SHARED_AGENTS_SENTINEL')
    assert.equal(readFileSync(project, 'utf8'), 'PROJECT_INSTRUCTIONS_SENTINEL')
    assert.equal(readFileSync(projectConfig, 'utf8'), originalConfig)
    assert.deepEqual(model.requests.filter((request) => request.error), [], 'fake model accepts all native request shapes')
  } catch (error) { t.diagnostic(logs.slice(-20).join('\n')); throw error }
})
