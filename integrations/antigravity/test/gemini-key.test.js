import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { AgyAcpClient } from '../lib/acp.js'
import { gatewayModelNames, geminiKeyMode, redactLog, served } from '../lib/provider.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE = fileURLToPath(new URL('./fake-acp-gemini-key.mjs', import.meta.url))
const KEY = 'test-key-0123456789'

// A fake Gemini-compatible gateway: GET /v1beta/models, paged, key in x-goog-api-key.
async function gateway(t) {
  const requests = []
  const server = createServer((req, res) => {
    const url = new URL(req.url, 'http://gateway.test')
    requests.push({ path: url.pathname, key: req.headers['x-goog-api-key'], token: url.searchParams.get('pageToken') })
    if (req.headers['x-goog-api-key'] !== KEY) { res.writeHead(401).end('{}'); return }
    const first = !url.searchParams.get('pageToken')
    const generate = ['generateContent', 'streamGenerateContent']
    res.setHeader('content-type', 'application/json')
    res.end(JSON.stringify(first
      ? { models: [
        { name: 'models/gemini-4-neon', baseModelId: 'gemini-4-neon', displayName: 'gemini-4-neon', supportedGenerationMethods: generate },
        { name: 'models/gemini-3.8-flash', baseModelId: 'gemini-3.8-flash', displayName: 'gemini-3.8-flash', supportedGenerationMethods: generate },
        { name: 'models/text-embedding', supportedGenerationMethods: ['embedContent'] },
      ], nextPageToken: 'more' }
      : { models: [{ name: 'models/gateway-only-model', displayName: 'Gateway only' }] }))
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  t.after(() => server.close())
  return { base: `http://127.0.0.1:${server.address().port}`, requests }
}

test('provider mode: PERSONA_AGY_MODEL_PROVIDER wins over agy settings.json', (t) => {
  const agyDir = mkdtempSync(join(tmpdir(), 'agy-provider-'))
  t.after(() => rmSync(agyDir, { recursive: true, force: true }))
  assert.equal(geminiKeyMode({ env: {}, agyDir }), false)
  writeFileSync(join(agyDir, 'settings.json'), '{"modelProvider":"gemini"}')
  assert.equal(geminiKeyMode({ env: {}, agyDir }), true)
  assert.equal(geminiKeyMode({ env: { PERSONA_AGY_MODEL_PROVIDER: 'oauth' }, agyDir }), false)
  writeFileSync(join(agyDir, 'settings.json'), '{"modelProvider":"oauth"}')
  assert.equal(geminiKeyMode({ env: { PERSONA_AGY_MODEL_PROVIDER: 'gemini' }, agyDir }), true)
  writeFileSync(join(agyDir, 'settings.json'), 'not json')
  assert.equal(geminiKeyMode({ env: {}, agyDir }), false)
})

test('the provider names are paged, filtered to what generates, and read with the key header', async (t) => {
  const g = await gateway(t)
  const names = await gatewayModelNames({ env: { GOOGLE_GEMINI_BASE_URL: `${g.base}/`, GEMINI_API_KEY: KEY } })
  assert.deepEqual([...names].sort(), ['gateway-only-model', 'gemini-3.8-flash', 'gemini-4-neon'])
  assert.deepEqual(g.requests.map((r) => [r.path, r.key, r.token]), [['/v1beta/models', KEY, null], ['/v1beta/models', KEY, 'more']])
  assert.equal(served(names, 'gemini-4-neon-high'), true)
  assert.equal(served(names, 'gemini-3.8-flash'), true)
  assert.equal(served(names, 'built-in-only-high'), false)
})

test('gateway errors carry neither the key nor the address', async (t) => {
  const g = await gateway(t)
  await assert.rejects(gatewayModelNames({ env: { GOOGLE_GEMINI_BASE_URL: g.base, GEMINI_API_KEY: 'wrong-key' } }), (err) => {
    assert.match(err.message, /HTTP 401/)
    assert.ok(!err.message.includes('wrong-key') && !err.message.includes(g.base))
    return true
  })
  await assert.rejects(gatewayModelNames({ env: { GOOGLE_GEMINI_BASE_URL: 'http://127.0.0.1:1', GEMINI_API_KEY: KEY } }), (err) => {
    assert.match(err.message, /model list failed/)
    assert.ok(!err.message.includes('127.0.0.1'))
    return true
  })
  await assert.rejects(gatewayModelNames({ env: {} }), /GEMINI_API_KEY is not set/)
})

test('redactLog removes the key from log lines', () => {
  const lines = []
  redactLog((m) => lines.push(m), { GEMINI_API_KEY: KEY })(`agy acp stderr: bad ${KEY} twice ${KEY}`)
  assert.deepEqual(lines, ['agy acp stderr: bad *** twice ***'])
})

async function fakeAgy(t) {
  const home = mkdtempSync(join(tmpdir(), 'agy-key-'))
  mkdirSync(join(home, 'agy'), { recursive: true })
  writeFileSync(join(home, 'calls.jsonl'), '')
  const bin = join(home, 'fake-agy')
  writeFileSync(bin, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${home}' "$@"\n`)
  chmodSync(bin, 0o700)
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  return { home, bin, calls }
}

test('the ACP client authenticates with the API key method only when the provider is set and agy offers it', async (t) => {
  const f = await fakeAgy(t)
  const keyed = new AgyAcpClient({ bin: f.bin, authMethod: 'gemini-api-key' })
  await keyed.ensureStarted()
  await keyed.stop()
  const plain = new AgyAcpClient({ bin: f.bin })
  await plain.ensureStarted()
  await plain.stop()
  assert.deepEqual(f.calls().filter((c) => c.method).map((c) => [c.method, c.params?.methodId]), [
    ['initialize', undefined], ['authenticate', 'gemini-api-key'], ['initialize', undefined]])
})

test('in API key mode the runtime lists the agy models the provider serves and skips quota windows', async (t) => {
  const g = await gateway(t)
  const f = await fakeAgy(t)
  const node = await startFakeNode(t, { features: ['usage_windows'] })
  const logs = []
  const runtime = createAgyRuntime({
    home: f.home, agyDir: join(f.home, 'agy'), agyBin: f.bin, socketPath: node.path, usageWindowsEveryMs: 20,
    env: { ...process.env, PERSONA_AGY_MODEL_PROVIDER: 'gemini', GOOGLE_GEMINI_BASE_URL: g.base, GEMINI_API_KEY: KEY },
    log: (m) => logs.push(m),
  })
  t.after(() => runtime.stop())
  runtime.start()
  await node.ready
  await until('the provider models', () => node.last('runtime.launchers')?.launchers[0].models.length === 3)
  const models = node.last('runtime.launchers').launchers[0].models
  assert.deepEqual(models.map((m) => m.id), ['gemini-4-neon-high', 'gemini-4-neon-low', 'gemini-3.8-flash-medium'], 'agy ids the provider serves, not the built-in catalog')
  await new Promise((resolve) => setTimeout(resolve, 120))
  assert.equal(node.last('usage.windows'), undefined)
  const starts = f.calls().filter((c) => c.argv)
  assert.deepEqual(starts.map((c) => c.argv), [['--settings', '{"modelProvider":"gemini"}', 'models']], 'only agy models, with the provider setting')
  assert.equal(f.calls().filter((c) => c.method).length, 0, 'no agy acp process is needed for quota')
  assert.equal(logs.filter((l) => l.includes('usage windows')).length, 1, 'the missing quota is logged once')
  assert.ok(!logs.some((l) => /model discovery error/.test(l)), logs.join('\n'))
  assert.ok(!logs.join('\n').includes(KEY))
})
