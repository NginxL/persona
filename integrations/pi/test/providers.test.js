import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  applyPersonaDefault, argvPicksModel, askPersonaProviders, providerConfig, registerPersonaProviders, usePersonaProviders, userPiConfig,
} from '../lib/providers.js'

const KEY = 'a'.repeat(64)
const SECRET = 'sk-test-pi-provider-secret'

const answer = () => ({
  providers: [
    { id: 'persona-gw', name: 'Persona · gw', launcher: 'gw', api: 'anthropic-messages', base_url: 'https://gw.example.com', api_key: SECRET,
      model: 'claude-x', models: [{ id: 'claude-x', name: 'claude-x', context_window: 1000000, max_tokens: 32000, reasoning: true, input: ['text', 'image'] }] },
    { id: 'persona-cx', name: 'Persona · cx', launcher: 'cx', api: 'openai-responses', base_url: 'https://oa.example.com/v1', api_key: SECRET,
      auth_header: false, model: 'gpt-5-x', models: [{ id: 'gpt-5-x', context_window: 128000, max_tokens: 32000, reasoning: true, input: ['text'] }] },
  ],
  default: { provider: 'persona-gw', model: 'claude-x', effort: 'high' },
})

/** A node.sock that answers launcher.pi, and a launch key beside it (when key is given). */
async function fakeNode(t, { key = KEY, reply = answer() } = {}) {
  // A short path: a unix socket's path has a small limit.
  const dir = mkdtempSync(join(tmpdir(), 'pp-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  if (key) writeFileSync(join(dir, 'launch.key'), key + '\n', { mode: 0o600 })
  const asked = []
  const server = createServer((c) => {
    let buf = ''
    c.on('data', (d) => {
      buf += d
      if (!buf.includes('\n')) return
      const req = JSON.parse(buf.slice(0, buf.indexOf('\n')))
      asked.push(req)
      c.end(JSON.stringify(req.kind === 'launcher.pi' && req.launch_key === KEY ? reply : { error: 'refused' }) + '\n')
    })
  })
  const socket = join(dir, 'node.sock')
  await new Promise((r) => server.listen(socket, r))
  t.after(() => server.close())
  return { socket, asked }
}

/** pi's extension API as far as providers go, and a session context. */
function fakePi({ available = [], current, entries = [], ownAuth = false } = {}) {
  const pi = { providers: new Map(), handlers: new Map(), model: undefined, thinking: undefined }
  pi.registerProvider = (id, config) => pi.providers.set(id, config)
  pi.on = (name, fn) => pi.handlers.set(name, fn)
  pi.setModel = async (m) => { pi.model = m; return true }
  pi.setThinkingLevel = (l) => { pi.thinking = l }
  const ctx = {
    model: current,
    sessionManager: { getEntries: () => entries },
    modelRegistry: {
      find: (provider, id) => (pi.providers.get(provider)?.models.some((m) => m.id === id) || available.some((m) => m.provider === provider && m.id === id)
        ? { provider, id } : undefined),
      hasConfiguredAuth: () => ownAuth,
    },
  }
  return { pi, ctx }
}

const noUser = { providers: new Set(), defaultModel: false }

// A new computer with no models.json at all: the node's providers are registered with their keys, and a new session
// starts on Model Settings › Agents' model.
test('a computer without models.json gets Persona providers and can start a session', async (t) => {
  const node = await fakeNode(t)
  const agentDir = mkdtempSync(join(tmpdir(), 'pa-'))
  t.after(() => rmSync(agentDir, { recursive: true, force: true }))
  const user = userPiConfig({ agentDir, cwd: agentDir })
  assert.equal(user.providers.size, 0)
  assert.equal(user.defaultModel, false)
  const { pi, ctx } = fakePi()
  const registered = await usePersonaProviders(pi, { ask: () => askPersonaProviders({ socket: node.socket }), user, argv: ['node', 'pi'] })
  assert.deepEqual(registered, ['persona-gw', 'persona-cx'])
  assert.equal(node.asked[0].kind, 'launcher.pi')
  assert.equal(node.asked[0].launch_key, KEY)
  const gw = pi.providers.get('persona-gw')
  assert.equal(gw.apiKey, SECRET)
  assert.equal(gw.api, 'anthropic-messages')
  assert.equal(gw.baseUrl, 'https://gw.example.com')
  assert.equal(gw.models[0].contextWindow, 1000000)
  await pi.handlers.get('session_start')({ reason: 'startup' }, ctx)
  assert.deepEqual(pi.model, { provider: 'persona-gw', id: 'claude-x' })
  assert.equal(pi.thinking, 'high')
})

// Without the launch key (a sandbox that hides the data directory) nothing is asked, and nothing is registered.
test('no launch key, no providers', async (t) => {
  const node = await fakeNode(t, { key: '' })
  assert.equal(await askPersonaProviders({ socket: node.socket }), null)
  assert.equal(node.asked.length, 0)
  const wrong = await fakeNode(t, { key: 'b'.repeat(64) })
  assert.equal(await askPersonaProviders({ socket: wrong.socket }), null) // the node's refusal
  assert.equal(await askPersonaProviders({ socket: join(tmpdir(), 'no-such-dir', 'node.sock') }), null) // no node
})

// The node gave nothing (out of every provider's scope): pi is left exactly as it was.
test('a computer out of scope registers nothing', async () => {
  const { pi } = fakePi()
  const registered = await usePersonaProviders(pi, { ask: async () => ({ providers: [] }), user: noUser })
  assert.deepEqual(registered, [])
  assert.equal(pi.providers.size, 0)
  assert.equal(pi.handlers.size, 0)
})

// The user's own configuration wins: their models.json's provider of the same id, their default model, a model on the
// command line, a signed-in provider of their own already selected, a session that already has its model.
test("the user's own pi configuration wins", async (t) => {
  const agentDir = mkdtempSync(join(tmpdir(), 'pa-'))
  t.after(() => rmSync(agentDir, { recursive: true, force: true }))
  writeFileSync(join(agentDir, 'models.json'), JSON.stringify({ providers: { 'persona-gw': { baseUrl: 'https://mine.example.com', apiKey: 'x', models: [] } } }))
  writeFileSync(join(agentDir, 'settings.json'), JSON.stringify({ defaultProvider: 'anthropic', defaultModel: 'mine' }))
  const user = userPiConfig({ agentDir, cwd: agentDir })
  assert.deepEqual([...user.providers], ['persona-gw'])
  assert.equal(user.defaultModel, true)
  const { pi, ctx } = fakePi()
  assert.deepEqual(registerPersonaProviders(pi, answer(), user), ['persona-cx'])
  assert.equal(pi.providers.has('persona-gw'), false)

  const a = { ...answer(), default: { provider: 'persona-cx', model: 'gpt-5-x' } }
  const reg = ['persona-gw', 'persona-cx']
  assert.equal(await applyPersonaDefault(pi, ctx, a, reg, { user, argv: [] }), false, 'default model in settings')
  assert.equal(await applyPersonaDefault(pi, ctx, a, reg, { user: noUser, argv: ['pi', '--model', 'x'] }), false, '--model')
  const own = fakePi({ current: { provider: 'anthropic', id: 'mine' }, ownAuth: true })
  registerPersonaProviders(own.pi, a, noUser)
  assert.equal(await applyPersonaDefault(own.pi, own.ctx, a, reg, { user: noUser, argv: [] }), false, 'own signed-in provider')
  const resumed = fakePi({ entries: [{ type: 'message' }] })
  registerPersonaProviders(resumed.pi, a, noUser)
  assert.equal(await applyPersonaDefault(resumed.pi, resumed.ctx, a, reg, { user: noUser, argv: [] }), false, 'a session with history')
  // A project's own .pi/settings.json counts too.
  const proj = mkdtempSync(join(tmpdir(), 'pp-'))
  t.after(() => rmSync(proj, { recursive: true, force: true }))
  mkdirSync(join(proj, '.pi'))
  writeFileSync(join(proj, '.pi', 'settings.json'), JSON.stringify({ defaultModel: 'p' }))
  assert.equal(userPiConfig({ agentDir: proj, cwd: proj }).defaultModel, true)
})

test('argvPicksModel and providerConfig', () => {
  assert.equal(argvPicksModel(['node', 'pi', '--models=a,b']), true)
  assert.equal(argvPicksModel(['node', 'pi', '--provider', 'x']), true)
  assert.equal(argvPicksModel(['node', 'pi', '-m', 'claude-x']), true)
  assert.equal(argvPicksModel(['node', 'pi', '-m=claude-x']), true)
  assert.equal(argvPicksModel(['node', 'pi', '-p', 'hello']), false)
  assert.equal(argvPicksModel(['node', 'pi', '--', 'explain --model flag']), false)
  assert.equal(argvPicksModel(['node', 'pi', '--', '-m', 'not-a-flag']), false)
  const c = providerConfig({ ...answer().providers[0], auth_header: true, headers: { 'X-Team': 'blue' } })
  assert.equal(c.authHeader, true)
  assert.deepEqual(c.headers, { 'X-Team': 'blue' })
  assert.deepEqual(c.models[0].cost, { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 })
})

test('askPersonaProviders decodes multi-byte UTF-8 split across socket chunks', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'pp-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  writeFileSync(join(dir, 'launch.key'), KEY + '\n', { mode: 0o600 })
  const payload = Buffer.from(JSON.stringify({
    providers: [{ id: 'persona-gw', name: 'Persona · 网关', launcher: 'gw', api: 'anthropic-messages', api_key: SECRET, models: [{ id: 'm1' }] }],
  }) + '\n', 'utf8')
  const cut = payload.indexOf(Buffer.from('网', 'utf8')) + 1
  const server = createServer((c) => {
    c.once('data', () => {
      c.write(payload.subarray(0, cut))
      setTimeout(() => c.end(payload.subarray(cut)), 10)
    })
  })
  const socket = join(dir, 'node.sock')
  await new Promise((r) => server.listen(socket, r))
  t.after(() => server.close())

  const got = await askPersonaProviders({ socket })
  assert.equal(got.providers[0].name, 'Persona · 网关')
})
