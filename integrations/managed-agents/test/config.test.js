import assert from 'node:assert/strict'
import { chmod, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { parseConfig } from '../lib/config.js'
import { createSecrets, parseRef } from '../lib/secrets.js'

const KEY = 'ark-0123456789abcdef'

test('a key in the config is refused, and the refusal never repeats it', () => {
  const { profiles, problems } = parseConfig({ profiles: [{ id: 'cloud', mode: 'cloud', api_key: KEY }] })
  assert.equal(profiles.length, 0)
  assert.equal(problems.length, 1)
  assert.match(problems[0], /never the key itself/)
  assert.ok(!problems[0].includes(KEY))
  assert.throws(() => parseRef(KEY), (e) => !e.message.includes(KEY))
})

test('cloud defaults to Ark: region host, /api/v3, bearer, beta header; self needs its own address', () => {
  const { profiles, problems } = parseConfig({ profiles: [
    { id: 'cloud', mode: 'cloud', region: 'cn-shanghai', api_key: 'env:ARK_KEY' },
    { id: 'self', mode: 'self', api_key: 'file:~/k' },
    { id: 'lab', mode: 'self', base_url: 'http://127.0.0.1:8080/', api_prefix: 'v1', auth: 'x-api-key', api_key: 'keychain:lab' },
  ] })
  assert.deepEqual(problems.map((p) => p.slice(0, 12)), ['profiles[1]:'])
  const [cloud, lab] = profiles
  assert.equal(cloud.base, 'https://ark.cn-shanghai.volces.com')
  assert.equal(cloud.prefix, '/api/v3')
  assert.equal(cloud.auth, 'bearer')
  assert.equal(cloud.headers['X-Ark-Beta'], 'agentic-2026-06-01')
  assert.equal(cloud.modelHeaderPrefix, 'X-User-Model-Header-')
  assert.equal(lab.base, 'http://127.0.0.1:8080')
  assert.equal(lab.prefix, '/v1')
  assert.equal(lab.auth, 'x-api-key')
})

test('the claude API family has its own defaults', () => {
  const { profiles } = parseConfig({ profiles: [{ id: 'c', mode: 'cloud', api: 'claude', api_key: 'env:K' }] })
  assert.equal(profiles[0].base, 'https://api.anthropic.com')
  assert.equal(profiles[0].auth, 'x-api-key')
  assert.equal(profiles[0].headers['anthropic-beta'], 'managed-agents-2026-04-01')
})

test('plain http off loopback, credentials in the URL or in headers are refused', () => {
  const bad = (p) => parseConfig({ profiles: [{ id: 'x', mode: 'self', api_key: 'env:K', ...p }] }).problems[0]
  assert.match(bad({ base_url: 'http://ma.example.com' }), /in the clear/)
  assert.equal(bad({ base_url: 'http://ma.example.com', allow_http: true }), undefined)
  assert.match(bad({ base_url: 'https://u:p@ma.example.com' }), /no user name/)
  assert.match(bad({ base_url: 'https://ma.example.com', headers: { Authorization: 'Bearer x' } }), /never in headers/)
  assert.match(bad({ base_url: 'https://ma.example.com', model_headers: { Authorization: 'sk-raw' } }), /not a reference/)
})

test('references resolve from the environment, a file, the keychain and extension secrets', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'ma-secrets-'))
  const file = join(dir, 'key')
  await writeFile(file, `${KEY}\n`)
  await chmod(file, 0o644)
  const logs = []
  const calls = []
  const s = createSecrets({
    env: { ARK_KEY: KEY, PERSONA_EXTENSION_SECRETS: JSON.stringify({ api_key: 'from-ext-123' }) },
    platform: 'darwin',
    run: async (bin, args) => { calls.push([bin, ...args]); return 'from-keychain-123\n' },
    log: (m) => logs.push(m),
  })
  assert.equal(await s.resolve('env:ARK_KEY'), KEY)
  assert.equal(await s.resolve(`file:${file}`), KEY)
  assert.match(logs[0], /chmod 600/)
  assert.equal(await s.resolve('keychain:work/ma'), 'from-keychain-123')
  assert.deepEqual(calls[0], ['/usr/bin/security', 'find-generic-password', '-s', 'work', '-a', 'ma', '-w'])
  assert.equal(await s.resolve('extension:api_key'), 'from-ext-123')
  await assert.rejects(s.resolve('env:MISSING'), /not set/)
  assert.ok(logs.every((l) => !l.includes(KEY)))
})

test('redact takes every resolved value and bearer tokens out of text', async () => {
  const s = createSecrets({ env: { K: KEY } })
  await s.resolve('env:K')
  assert.equal(s.redact(`failed with ${KEY} and Authorization: Bearer abcdefghijkl`), 'failed with <redacted> and Authorization: Bearer <redacted>')
})

test('a value that would break a header is refused', async () => {
  const s = createSecrets({ env: { K: 'a\nb-cdef' } })
  await assert.rejects(s.resolve('env:K'), /line break/)
})
