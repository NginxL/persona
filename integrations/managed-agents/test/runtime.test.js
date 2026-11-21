import assert from 'node:assert/strict'
import { mkdtemp } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { Peer, SUBPROTOCOL } from '../../runtime-sdk/index.js'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { parseConfig } from '../lib/config.js'
import { createManagedAgentsRuntime } from '../lib/runtime.js'
import { startFakeMA } from '../testing/fake-ma.mjs'

const MODEL_KEY = 'model-secret-abcdef'

async function fixture(t, { ma: maOpts = {}, profile = {}, node: nodeOpts = {} } = {}) {
  const ma = await startFakeMA(maOpts)
  t.after(() => ma.close())
  const node = await startFakeNode(t, { strict: true, ...nodeOpts })
  const config = parseConfig({ profiles: [{ id: 'cloud', name: 'Ark', mode: 'self', base_url: ma.url, api_key: 'env:MA_KEY',
    headers: { 'X-Ark-Beta': 'agentic-2026-06-01' }, model_headers: { Authorization: 'env:MODEL_AUTH' },
    start: { agent: 'agent-a', environment_id: 'env-1', label: 'Helper' }, poll_seconds: 1, ...profile }] })
  assert.deepEqual(config.problems, [])
  const logs = []
  const dir = await mkdtemp(join(tmpdir(), 'ma-rt-'))
  const rt = createManagedAgentsRuntime({ config, statePath: join(dir, 'state.json'), env: { MA_KEY: ma.key, MODEL_AUTH: `Bearer ${MODEL_KEY}` },
    log: (m) => logs.push(m), pollScale: 0.05, runtimeOptions: { socketPath: node.path, retryMs: 50, pushDelayMs: 10, answerFirst: 'phone' } })
  await rt.start()
  await node.ready
  t.after(() => rt.stop())
  const rows = () => node.last('runtime.sessions')?.sessions ?? []
  const items = (sid) => node.all('session.items').filter((b) => b.session_id === sid).flatMap((b) => b.items ?? [])
  return { ma, node, rt, logs, rows, items }
}

async function launcherProfiles(t) {
  const mas = await Promise.all([startFakeMA({ list: false }), startFakeMA({ list: false })])
  for (const ma of mas) t.after(() => ma.close())
  assert.notEqual(mas[0].url, mas[1].url)
  const config = parseConfig({ profiles: mas.map((ma, i) => ({
    id: `profile-${i + 1}`, mode: 'self', base_url: ma.url, api_key: `env:MA_${i + 1}_KEY`,
    start: { agent: `agent-${i + 1}`, environment_id: `env-${i + 1}` }, poll_seconds: 1,
  })) })
  assert.deepEqual(config.problems, [])
  const env = Object.fromEntries(mas.map((ma, i) => [`MA_${i + 1}_KEY`, ma.key]))
  const statePath = join(await mkdtemp(join(tmpdir(), 'ma-launcher-')), 'state.json')
  return { mas, config, env, statePath }
}

// The node's end of a daemon connection, with its launcher rewritten by daemons.json.
function dialDaemonNode(url, token) {
  const ws = new WebSocket(url, { protocols: [SUBPROTOCOL], headers: { authorization: `Bearer ${token}` } })
  const handlers = { data: [], close: [], error: [] }
  const socket = {
    setEncoding() {},
    on(ev, fn) { (handlers[ev] ??= []).push(fn) },
    write(s) { ws.send(s.endsWith('\n') ? s.slice(0, -1) : s) },
    destroy() { ws.close() },
  }
  const node = { ws, hello: undefined }
  ws.onmessage = (e) => handlers.data.forEach((fn) => fn(String(e.data) + '\n'))
  ws.onclose = () => handlers.close.forEach((fn) => fn())
  ws.onerror = () => {}
  node.peer = new Peer(socket, {
    prefix: 'n',
    onRequest: async (method, params) => {
      if (method === 'runtime.hello') {
        node.hello = params
        return { protocol: 1, node_version: 'test', hub: true, features: ['prefer_running'] }
      }
      throw new Error(`unexpected ${method}`)
    },
    onEvent: () => {},
  })
  return node
}

test('hello names the runtime and its launcher; sessions are listed grouped by agent', async (t) => {
  const { ma, node, rt, rows } = await fixture(t)
  assert.equal(node.hello.runtime, 'managed-agents')
  assert.equal(node.hello.name, 'Managed Agents')
  assert.deepEqual(node.hello.caps, { ...node.hello.caps, start: true, send: true, interrupt: true })
  assert.deepEqual(node.hello.launchers, [{ name: 'ma-cloud', display_name: 'Helper', agent: 'managed-agents', model: '' }])
  const s = ma.create({ title: 'Quarterly report', agent: 'agent-a' })
  ma.emit(s.id, { type: 'user.message', content: [{ type: 'text', text: 'hello' }] })
  ma.emit(s.id, { type: 'agent.message', content: [{ type: 'text', text: 'Hi there' }] })
  ma.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })
  await rt.refresh()
  const row = await until('listed', () => rows().find((r) => r.session_id === `cloud.${s.id}`))
  assert.equal(row.title, 'Quarterly report')
  assert.equal(row.space_id, 'cloud:agent-a')
  assert.equal(row.space_name, 'Helper')
  assert.equal(row.model, 'model-x')
})

test('reads a conversation page by page, with the key on every request and the model key nowhere but sends', async (t) => {
  const { ma, node, rt } = await fixture(t, { ma: { pageSize: 2 } })
  const s = ma.create({ title: 'Read me' })
  ma.emit(s.id, { type: 'user.message', content: [{ type: 'text', text: 'list files' }] })
  ma.emit(s.id, { type: 'session.status_running' })
  ma.emit(s.id, { type: 'agent.tool_use', name: 'bash', input: { command: 'ls' }, evaluated_permission: 'allow' })
  ma.emit(s.id, { type: 'agent.tool_result', tool_use_id: s.events[2].id, content: [{ type: 'text', text: 'a.txt' }] })
  ma.emit(s.id, { type: 'agent.message', content: [{ type: 'text', text: 'One file: a.txt' }] })
  ma.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })
  await rt.refresh()
  const sid = `cloud.${s.id}`
  const page = await node.peer.call('session.items', { session_id: sid })
  assert.deepEqual(page.items.map((i) => i.kind), ['text', 'tool_use', 'tool_result', 'text'])
  assert.equal(page.history, `ma:${sid}`)
  assert.equal(page.to_seq, 6)
  assert.equal(page.items[1].tool_name, 'Bash')
  const last = await node.peer.call('session.items', { session_id: sid, turns: 1, after_seq: 5 })
  assert.equal(last.to_seq, 6)
  // Three pages of two events each were followed.
  assert.ok(ma.requests.filter((r) => r.path.endsWith('/events') && r.method === 'GET').length >= 3)
  for (const r of ma.requests) {
    assert.equal(r.headers.authorization, `Bearer ${ma.key}`)
    assert.equal(r.headers['x-ark-beta'], 'agentic-2026-06-01')
    assert.equal(r.headers['x-user-model-header-authorization'], undefined, `${r.method} ${r.path} carried the model key`)
  }
})

test('sends a message once per client_msg_id and pushes the reply as it lands', async (t) => {
  const { ma, node, rt, rows, items } = await fixture(t)
  const s = ma.create({ title: 'Chat' })
  await rt.refresh()
  const sid = `cloud.${s.id}`
  await until('listed', () => rows().find((r) => r.session_id === sid))
  await node.peer.call('session.watch', { session_id: sid, since_seq: 0 })
  await node.peer.call('session.send', { session_id: sid, text: 'ping', client_msg_id: 'm1' })
  await node.peer.call('session.send', { session_id: sid, text: 'ping', client_msg_id: 'm1' }) // the app retries
  await until('the reply', () => items(sid).some((i) => i.text === 'echo: ping'), 5000)
  const posts = ma.requests.filter((r) => r.method === 'POST' && r.path.endsWith('/events'))
  assert.equal(posts.length, 1)
  assert.deepEqual(JSON.parse(posts[0].body), { events: [{ type: 'user.message', content: [{ type: 'text', text: 'ping' }] }] })
  // BYOK: the model credential rides on the send, under the configured prefix.
  assert.equal(posts[0].headers['x-user-model-header-authorization'], `Bearer ${MODEL_KEY}`)
  await until('done', () => rows().find((r) => r.session_id === sid)?.status === 'done', 5000)
  assert.equal(rows().find((r) => r.session_id === sid).last_text, 'echo: ping')
  // Pushed batches continue each other.
  const batches = node.all('session.items').filter((b) => b.session_id === sid)
  for (let i = 1; i < batches.length; i++) assert.equal(batches[i].from_seq, batches[i - 1].to_seq)
})

test('streams live text while watched', async (t) => {
  let release
  const { ma, node, rt, rows } = await fixture(t, { ma: { agent: async (s, text, api) => {
    api.emit(s.id, { type: 'session.status_running' })
    await new Promise((r) => { release = r })
    api.delta(s.id, 'evt-live', 'Hel')
    api.delta(s.id, 'evt-live', 'lo')
    await new Promise((r) => setTimeout(r, 300))
    api.emit(s.id, { id: 'evt-live', type: 'agent.message', content: [{ type: 'text', text: 'Hello' }] })
    api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })
  } } })
  const s = ma.create()
  await rt.refresh()
  const sid = `cloud.${s.id}`
  await until('listed', () => rows().find((r) => r.session_id === sid))
  await node.peer.call('session.watch', { session_id: sid, since_seq: 0 })
  await until('the stream open', () => ma.streamCount() > 0, 3000)
  await node.peer.call('session.send', { session_id: sid, text: 'hi', client_msg_id: 'm' })
  await until('the agent runs', () => release, 3000)
  release()
  await until('live text', () => node.all('session.delta').some((d) => d.session_id === sid && d.text === 'Hello'), 3000)
  await until('it ends', () => node.all('session.delta').some((d) => d.session_id === sid && d.text === ''), 3000)
})

test('a tool confirmation becomes a permission card; allow and deny go back as tool confirmations', async (t) => {
  let answered = []
  const { ma, node, rt, rows, items } = await fixture(t, { ma: {
    agent: async (s, text, api) => {
      api.emit(s.id, { type: 'session.status_running' })
      const call = api.emit(s.id, { type: 'agent.tool_use', name: 'bash', input: { command: `rm ${text}` }, evaluated_permission: 'ask' })
      api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'requires_action', event_ids: [call.id] } })
    },
    onAnswer: async (s, ev, api) => {
      answered.push(ev)
      api.emit(s.id, { type: 'session.status_running' })
      api.emit(s.id, { type: 'agent.tool_result', tool_use_id: ev.tool_use_id, content: [{ type: 'text', text: ev.result === 'allow' ? 'removed' : 'denied' }], is_error: ev.result !== 'allow' })
      api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })
    },
  } })
  node.resolve = (ix) => (ix.tool_input.command === 'rm a' ? { allow: true } : { allow: false, message: 'keep it' })
  const s = ma.create()
  await rt.refresh()
  const sid = `cloud.${s.id}`
  await until('listed', () => rows().find((r) => r.session_id === sid))
  await node.peer.call('session.watch', { session_id: sid, since_seq: 0 })
  await node.peer.call('session.send', { session_id: sid, text: 'a', client_msg_id: 'a' })
  await until('allowed', () => answered.length === 1, 5000)
  const card = node.asked[0]
  assert.equal(card.kind, 'permission')
  assert.equal(card.session_id, sid)
  assert.equal(card.tool_name, 'Bash')
  assert.equal(card.summary, 'rm a')
  assert.deepEqual({ ...answered[0], session_thread_id: undefined }, { type: 'user.tool_confirmation', tool_use_id: card.tool_use_id, result: 'allow', session_thread_id: undefined })
  await until('done', () => rows().find((r) => r.session_id === sid)?.status === 'done', 5000)
  await node.peer.call('session.send', { session_id: sid, text: 'b', client_msg_id: 'b' })
  await until('denied', () => answered.length === 2, 5000)
  assert.equal(answered[1].result, 'deny')
  assert.equal(answered[1].deny_message, 'keep it')
  await until('the result', () => items(sid).some((i) => i.kind === 'tool_result' && i.text === 'denied'), 5000)
  assert.equal(node.asked.length, 2, 'each call is asked once')
})

test('AskQuestion and ExitPlanMode become question and plan cards answered with custom tool results', async (t) => {
  const results = []
  const { ma, node, rt, rows } = await fixture(t, { ma: {
    agent: async (s, text, api) => {
      api.emit(s.id, { type: 'session.status_running' })
      const call = text === 'q'
        ? api.emit(s.id, { type: 'agent.custom_tool_use', name: 'AskQuestion', input: { questions: [{ question: 'Which city?', header: 'City', options: [{ label: 'Paris' }, { label: 'Rome' }] }] } })
        : api.emit(s.id, { type: 'agent.custom_tool_use', name: 'ExitPlanMode', input: { title: 'Trip', plan: '1. Book\n2. Go' } })
      api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'requires_action', event_ids: [call.id] } })
    },
    onAnswer: async (s, ev, api) => {
      results.push(ev)
      api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })
    },
  } })
  node.resolve = (ix) => (ix.kind === 'question' ? { answers: { 'Which city?': 'Rome' } } : { approve: true, mode: 'acceptEdits' })
  const s = ma.create()
  await rt.refresh()
  const sid = `cloud.${s.id}`
  await until('listed', () => rows().find((r) => r.session_id === sid))
  await node.peer.call('session.send', { session_id: sid, text: 'q', client_msg_id: 'q' })
  await until('answered', () => results.length === 1, 5000)
  assert.equal(node.asked[0].kind, 'question')
  assert.equal(node.asked[0].questions[0].question, 'Which city?')
  assert.deepEqual(JSON.parse(results[0].content[0].text), { answers: { 'Which city?': 'Rome' } })
  await until('done', () => rows().find((r) => r.session_id === sid)?.status === 'done', 5000)
  await node.peer.call('session.send', { session_id: sid, text: 'p', client_msg_id: 'p' })
  await until('approved', () => results.length === 2, 5000)
  assert.equal(node.asked[1].kind, 'plan')
  assert.match(node.asked[1].plan, /1\. Book/)
  assert.equal(JSON.parse(results[1].content[0].text).decision, 'approved')
})

test('a card answered elsewhere is withdrawn from the phone', async (t) => {
  const { ma, node, rt, rows } = await fixture(t)
  let asked
  node.resolve = () => new Promise((r) => { asked = r })
  const s = ma.create()
  const call = ma.emit(s.id, { type: 'agent.tool_use', name: 'bash', input: { command: 'deploy' }, evaluated_permission: 'ask' })
  ma.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'requires_action', event_ids: [call.id] } })
  await rt.refresh()
  const sid = `cloud.${s.id}`
  await until('blocked', () => rows().find((r) => r.session_id === sid)?.status === 'blocked', 5000)
  await until('asked', () => asked, 3000)
  ma.emit(s.id, { type: 'user.tool_confirmation', tool_use_id: call.id, result: 'allow' })
  ma.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })
  await until('withdrawn', () => node.events.some((e) => e.event === 'interaction.cancel'), 5000)
  asked({ dismiss: true })
})

test('starts a session from the launcher, with its first message, and keeps listing it without a list route', async (t) => {
  const { ma, node, rt, rows, items } = await fixture(t, { ma: { list: false } })
  const { session } = await node.peer.call('session.start', { launcher: 'ma-cloud', cwd: '/tmp', prompt: 'plan a trip', client_msg_id: 'first' })
  assert.match(session.session_id, /^cloud\.sesn-/)
  const created = ma.requests.find((r) => r.method === 'POST' && r.path === '/api/v3/sessions')
  assert.deepEqual(JSON.parse(created.body), { agent: 'agent-a', environment_id: 'env-1', title: 'plan a trip' })
  await node.peer.call('session.watch', { session_id: session.session_id, since_seq: 0 })
  await until('the reply', () => items(session.session_id).some((i) => i.text === 'echo: plan a trip'), 5000)
  await rt.refresh()
  await until('still listed', () => rows().some((r) => r.session_id === session.session_id && r.started_by_persona), 3000)
})

test('an unknown launcher creates no session and sends no prompt to either endpoint', async (t) => {
  const { mas, config, env, statePath } = await launcherProfiles(t)
  const node = await startFakeNode(t, { strict: true })
  const rt = createManagedAgentsRuntime({ config, env, statePath, pollScale: 0.05,
    runtimeOptions: { socketPath: node.path, retryMs: 50, pushDelayMs: 10 } })
  t.after(() => rt.stop())
  await rt.start()
  await node.ready
  const err = await node.peer.call('session.start', { launcher: 'stale-launcher', cwd: '/tmp', prompt: 'must not be routed', client_msg_id: 'unknown' })
    .then(() => undefined, (e) => e)
  assert.deepEqual(mas.map((ma) => ma.requests.filter((r) => r.method === 'POST').map((r) => r.path)), [[], []])
  assert.equal(err?.code, 'launcher_missing')
  for (const ma of mas) assert.equal(ma.sessions.size, 0)
})

test('a daemon alias still starts the first profile and sends its prompt only there', async (t) => {
  const { mas, config, env, statePath } = await launcherProfiles(t)
  const token = 'test-daemon-token'
  const rt = createManagedAgentsRuntime({ config, env, statePath, pollScale: 0.05,
    runtimeOptions: { listen: { port: 0, token }, pushDelayMs: 10 } })
  t.after(() => rt.stop())
  const addr = await rt.start()
  const node = dialDaemonNode(`ws://127.0.0.1:${addr.port}/persona/runtime`, token)
  t.after(() => node.ws.close())
  await until('daemon hello', () => node.hello)
  const { session } = await node.peer.call('session.start', { launcher: 'daemon-alias', cwd: '/tmp', prompt: 'daemon prompt', client_msg_id: 'daemon' })
  assert.match(session.session_id, /^profile-1\.sesn-/)
  assert.equal(mas[0].sessions.size, 1)
  const created = mas[0].requests.find((r) => r.method === 'POST' && r.path === '/api/v3/sessions')
  assert.deepEqual(JSON.parse(created.body), { agent: 'agent-1', environment_id: 'env-1', title: 'daemon prompt' })
  const sent = mas[0].requests.find((r) => r.method === 'POST' && r.path.endsWith('/events'))
  assert.deepEqual(JSON.parse(sent.body), { events: [{ type: 'user.message', content: [{ type: 'text', text: 'daemon prompt' }] }] })
  assert.equal(mas[1].sessions.size, 0)
  assert.deepEqual(mas[1].requests.filter((r) => r.method === 'POST'), [])
})

test('interrupt sends user.interrupt and settles once the turn ended', async (t) => {
  const { ma, node, rt, rows } = await fixture(t, { ma: { agent: async (s, text, api) => api.emit(s.id, { type: 'session.status_running' }) } })
  const s = ma.create()
  await rt.refresh()
  const sid = `cloud.${s.id}`
  await until('listed', () => rows().find((r) => r.session_id === sid))
  await node.peer.call('session.send', { session_id: sid, text: 'long job', client_msg_id: 'x' })
  await until('working', () => rows().find((r) => r.session_id === sid)?.status === 'working', 5000)
  const res = await node.peer.call('session.interrupt', { session_id: sid })
  assert.deepEqual(res, { ok: true, settled: true })
  assert.ok(ma.requests.some((r) => r.method === 'POST' && JSON.parse(r.body || '{}').events?.[0]?.type === 'user.interrupt'))
})

test('a session the server does not have is not_found and leaves nothing behind', async (t) => {
  const { node, rt } = await fixture(t)
  const err = await node.peer.call('session.items', { session_id: 'cloud.sesn-missing' }).then(() => undefined, (e) => e)
  assert.equal(err?.code, 'not_found')
  assert.equal(rt.sessions.has('cloud.sesn-missing'), false)
})

test('a wrong key is reported without the key, and nothing is listed', async (t) => {
  const ma = await startFakeMA({ key: 'right-key-123456' })
  t.after(() => ma.close())
  const node = await startFakeNode(t)
  const config = parseConfig({ profiles: [{ id: 'cloud', mode: 'self', base_url: ma.url, api_key: 'env:MA_KEY' }] })
  const logs = []
  const rt = createManagedAgentsRuntime({ config, statePath: join(await mkdtemp(join(tmpdir(), 'ma-')), 's.json'), env: { MA_KEY: 'wrong-key-abcdef' },
    log: (m) => logs.push(m), pollScale: 0.05, runtimeOptions: { socketPath: node.path, retryMs: 50, pushDelayMs: 10 } })
  ma.create()
  await rt.start()
  t.after(() => rt.stop())
  await node.ready
  await until('a warning', () => logs.some((l) => /401/.test(l)), 3000)
  assert.ok(logs.every((l) => !l.includes('wrong-key-abcdef') && !l.includes('right-key-123456')), logs.join('\n'))
  assert.deepEqual(node.last('runtime.sessions')?.sessions ?? [], [])
})
