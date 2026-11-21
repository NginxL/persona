import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE = fileURLToPath(new URL('./fake-acp-usage.mjs', import.meta.url))

async function fixture(t, features) {
  const home = mkdtempSync(join(tmpdir(), 'agy-usage-'))
  const agyDir = join(home, 'agy')
  mkdirSync(agyDir, { recursive: true })
  const quotaFile = join(home, 'quota.json')
  writeFileSync(quotaFile, JSON.stringify({ buckets: [
    { bucketId: '3p-5h', window: '18000s', remainingFraction: 0.75, resetTime: '2099-01-01T00:00:00Z' },
  ] }))
  writeFileSync(join(home, 'native-calls.jsonl'), '')
  const binary = join(home, 'fake-agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${home}' "$@"\n`)
  chmodSync(binary, 0o700)
  const node = await startFakeNode(t, { features })
  const runtime = createAgyRuntime({ home, agyDir, agyBin: binary, socketPath: node.path, usageWindowsEveryMs: 20, log: () => {} })
  t.after(async () => { await runtime.stop(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  const calls = () => readFileSync(join(home, 'native-calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  return { node, runtime, quotaFile, calls }
}

test('agy polls native quota summaries and publishes normalized usage windows', async (t) => {
  const { node, runtime, quotaFile, calls } = await fixture(t, ['usage_windows'])
  const params = await until('first usage window report', () => node.last('usage.windows'))
  assert.equal(params.provider, 'agy')
  assert.equal(params.name, 'Antigravity')
  assert.ok(Number.isFinite(Date.parse(params.sampled_at)))
  assert.deepEqual(params.windows, [{ label: '5h', window_minutes: 300, used_percent: 25, resets_at: '2099-01-01T00:00:00.000Z' }])
  assert.deepEqual(calls()[0], { method: 'RetrieveUserQuotaSummary', params: { request: { forceRefresh: false } } })

  writeFileSync(quotaFile, JSON.stringify({ buckets: [] }))
  await until('empty native snapshot clears the account', () => node.last('usage.windows')?.windows?.length === 0)
  const count = calls().length
  await runtime.stop()
  await new Promise((resolve) => setTimeout(resolve, 60))
  assert.equal(calls().length, count, 'a stopped runtime does not poll again')
})

test('agy does not start a native quota reader against an older node', async (t) => {
  const { node, calls } = await fixture(t, [])
  await new Promise((resolve) => setTimeout(resolve, 80))
  assert.equal(node.last('usage.windows'), undefined)
  assert.deepEqual(calls(), [])
})
