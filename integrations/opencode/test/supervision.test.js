import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createSupervisionStore, policyEnvironment, supportsSystemPrompt } from '../lib/supervision.js'

async function fixture(t, options = {}) {
  const home = await mkdtemp(join(tmpdir(), 'opencode-policy-'))
  const root = join(home, 'policy')
  const calls = []
  let version = '1.19.6', configured = true, acpVersion = version, promptCapability
  const env = { OPENCODE_CONFIG_CONTENT: JSON.stringify({ instructions: ['existing-policy.md'], mcp: { unrelated: { type: 'local', command: ['fake'] } } }) }
  const store = createSupervisionStore({ home, directory: root, bin: 'fake-opencode', env,
    run: async (_, args, opts) => {
      calls.push({ args, env: opts.env })
      if (args[0] === '--version') return { stdout: version }
      assert.deepEqual(args, ['debug', 'config'])
      const config = JSON.parse(opts.env.OPENCODE_CONFIG_CONTENT)
      return { stdout: JSON.stringify({ ...config, ...(!configured ? { instructions: [] } : {}) }) }
    },
    acpFactory: () => ({
      ensureStarted: async () => ({ capabilities: { sessionCapabilities: { systemPrompt: promptCapability } }, agentInfo: { version: acpVersion } }),
      stop: async () => calls.push({ stopped: true }),
    }), ...options,
  })
  t.after(async () => { await store.stop(); await rm(home, { recursive: true, force: true }) })
  return { home, root, store, calls, env,
    setVersion(value) { version = value }, setAcpVersion(value) { acpVersion = value },
    setConfigured(value) { configured = value }, setPromptCapability(value) { promptCapability = value },
  }
}

test('capability remains absent unless pinned version, resolved instructions and ACP version all pass startup checks', async (t) => {
  const f = await fixture(t)
  assert.equal(f.store.ready, null)
  f.setVersion('1.19.7')
  assert.equal(await f.store.probe(), null)
  assert.equal(f.calls.length, 1, 'an unverified engine version starts no ACP process')
  f.setVersion('1.19.6'); f.setConfigured(false)
  assert.equal(await f.store.probe(), null)
  assert.deepEqual(await readdir(f.root), [], 'failed self-check removes its probe file')
  f.setConfigured(true); f.setAcpVersion('different-binary')
  assert.equal(await f.store.probe(), null)
  f.setAcpVersion('1.19.6')
  assert.deepEqual(await f.store.probe(), { version: '1.19.6', channel: 'config' })
  assert.deepEqual(await readdir(f.root), [], 'successful startup leaves no sentinel policy')
  f.setPromptCapability({})
  assert.deepEqual(await f.store.probe(), { version: '1.19.6', channel: 'append' })
})

test('policy and MCP configuration are private per worker and preserve user instructions; closing never deletes a sibling', async (t) => {
  const f = await fixture(t)
  const assistant = join(f.home, '.persona', 'assistant')
  await mkdir(assistant, { recursive: true })
  const agents = join(assistant, 'AGENTS.md')
  await writeFile(agents, 'shared workspace instructions')
  const first = await f.store.create('policy A'), second = await f.store.create('policy B')
  assert.notEqual(first.file, second.file)
  if (process.platform !== 'win32') {
    assert.equal((await stat(first.file)).mode & 0o777, 0o600)
    assert.equal((await stat(first.dir)).mode & 0o777, 0o700)
  }
  const route = { remoteConfig: { type: 'remote', url: 'http://127.0.0.1:12345/private', headers: { Authorization: 'Bearer fake-only' } } }
  const firstEnv = await f.store.verify(first, route)
  const firstConfig = JSON.parse(firstEnv.OPENCODE_CONFIG_CONTENT)
  assert.deepEqual(firstConfig.instructions, ['existing-policy.md', first.file])
  assert.deepEqual(firstConfig.mcp.persona, route.remoteConfig)
  assert.deepEqual(firstConfig.mcp.unrelated, JSON.parse(f.env.OPENCODE_CONFIG_CONTENT).mcp.unrelated)
  assert.equal(JSON.stringify(firstConfig).includes(second.file), false)
  assert.equal(await readFile(agents, 'utf8'), 'shared workspace instructions')
  await f.store.remove(first)
  await assert.rejects(readFile(first.file), { code: 'ENOENT' })
  assert.equal(await readFile(second.file, 'utf8'), 'policy B')
  await f.store.remove({ dir: assistant })
  assert.equal(await readFile(agents, 'utf8'), 'shared workspace instructions')
  await f.store.stop()
  await assert.rejects(readFile(second.file), { code: 'ENOENT' })
})

test('missing/replaced or oversized policies fail closed; no shared Persona policy directory is allowed', async (t) => {
  const f = await fixture(t)
  await assert.rejects(f.store.create('x'.repeat(128 * 1024 + 1)), (error) => error.code === 'assistant_supervision_runtime')
  const record = await f.store.create('original policy')
  await writeFile(record.file, 'changed policy')
  await assert.rejects(f.store.verify(record), (error) => error.code === 'assistant_supervision_runtime')
  await rm(record.file)
  await assert.rejects(f.store.verify(record))
  const unsafe = createSupervisionStore({ home: f.home, directory: join(f.home, '.persona', 'assistant', 'policies'), bin: 'fake', env: {} })
  await assert.rejects(unsafe.create('policy'), (error) => error.code === 'assistant_supervision_runtime')
})

test('ACP append is advertised by an object, never by unknown/boolean capabilities or override alone', () => {
  for (const value of [undefined, null, false, true, [], 'yes']) assert.equal(supportsSystemPrompt({ sessionCapabilities: { systemPrompt: value } }), false)
  for (const value of [{}, { mode: { override: {} } }]) assert.equal(supportsSystemPrompt({ sessionCapabilities: { systemPrompt: value } }), true)
  const original = { OPENCODE_CONFIG_CONTENT: '{ // operator comment\n"instructions": ["project.md",], "model":"test/one" }' }
  assert.deepEqual(JSON.parse(policyEnvironment(original, 'worker.md').OPENCODE_CONFIG_CONTENT).instructions, ['project.md', 'worker.md'])
  assert.equal(original.OPENCODE_CONFIG_CONTENT.includes('worker.md'), false)
  assert.throws(() => policyEnvironment({ OPENCODE_CONFIG_CONTENT: '{"instructions":"invalid"}' }, 'worker.md'))
})

test('closing after runtime recovery deletes only that native session; startup reclaims records of dead companions', async (t) => {
  const f = await fixture(t)
  const a = await f.store.create('policy A'), b = await f.store.create('policy B')
  await f.store.bind(a, 'native-a'); await f.store.bind(b, 'native-b')
  const recovered = createSupervisionStore({ home: f.home, directory: f.root, bin: 'unused', env: {} })
  await recovered.removeForSession('native-a')
  await assert.rejects(readFile(a.file), { code: 'ENOENT' })
  assert.equal(await readFile(b.file, 'utf8'), 'policy B')
  await recovered.collectStale()
  assert.equal(await readFile(b.file, 'utf8'), 'policy B', 'a live companion is never reclaimed by startup GC')
  const orphan = await f.store.create('crashed before the native id was returned')
  const marker = join(orphan.dir, 'owner.json')
  const owner = JSON.parse(await readFile(marker, 'utf8'))
  await writeFile(marker, JSON.stringify({ ...owner, pid: 2147483647 }))
  await recovered.collectStale()
  await assert.rejects(readFile(orphan.file), { code: 'ENOENT' })
  assert.equal(await readFile(b.file, 'utf8'), 'policy B')
  await recovered.removeForSession('native-b')
  await assert.rejects(readFile(b.file), { code: 'ENOENT' })
})
