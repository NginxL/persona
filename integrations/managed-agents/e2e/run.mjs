// End-to-end acceptance of the Managed Agents runtime (docs/integrations/runtimes.md "Acceptance") against a real
// `personad node --stdio`; the script plays the hub, and a fake Managed Agents server plays the API. Runs twice: the
// companion on the node's runtime.sock (self / local), then as a daemon the node dials from daemons.json (the shape a
// companion next to a remote deployment takes). No network beyond loopback, no real key.
//
//   PERSONAD=<personad binary> node integrations/managed-agents/e2e/run.mjs

import { randomUUID } from 'node:crypto'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { checks, startNode, until } from '../../runtime-sdk/testing/node-harness.mjs'
import { parseConfig } from '../lib/config.js'
import { createManagedAgentsRuntime } from '../lib/runtime.js'
import { startFakeMA } from '../testing/fake-ma.mjs'

const personad = process.env.PERSONAD
if (!personad) {
  console.error('set PERSONAD (a personad binary)')
  process.exit(2)
}
const { check, summary } = checks()
const root = mkdtempSync(join('/tmp', 'jma-e2e-'))
const MODEL_KEY = `model-${randomUUID()}`

const ma = await startFakeMA({
  agent: async (s, text, api) => {
    api.emit(s.id, { type: 'session.status_running' })
    if (text.includes('ask')) {
      const call = api.emit(s.id, { type: 'agent.tool_use', name: 'bash', input: { command: 'make deploy' }, evaluated_permission: 'ask' })
      api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'requires_action', event_ids: [call.id] } })
      return
    }
    if (text.includes('question')) {
      const call = api.emit(s.id, { type: 'agent.custom_tool_use', name: 'AskQuestion', input: { questions: [{ question: 'Which region?', options: [{ label: 'North' }, { label: 'South' }] }] } })
      api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'requires_action', event_ids: [call.id] } })
      return
    }
    api.emit(s.id, { type: 'agent.thinking', content: [{ type: 'text', text: 'thinking it over' }] })
    api.delta(s.id, 'live-1', 'echo')
    await new Promise((r) => setTimeout(r, 200))
    api.emit(s.id, { type: 'agent.message', content: [{ type: 'text', text: `echo: ${text}` }] })
    api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })
  },
  onAnswer: async (s, ev, api) => {
    api.emit(s.id, { type: 'session.status_running' })
    if (ev.type === 'user.tool_confirmation') api.emit(s.id, { type: 'agent.tool_result', tool_use_id: ev.tool_use_id, content: [{ type: 'text', text: ev.result === 'allow' ? 'deployed' : 'skipped' }] })
    api.emit(s.id, { type: 'agent.message', content: [{ type: 'text', text: `answered: ${ev.type}` }] })
    api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })
  },
})

const existing = ma.create({ title: 'Existing report', agent: 'agent-a' })
ma.emit(existing.id, { type: 'user.message', content: [{ type: 'text', text: 'summarize Q3' }] })
ma.emit(existing.id, { type: 'session.status_running' })
ma.emit(existing.id, { type: 'agent.message', content: [{ type: 'text', text: 'Q3 grew 12%.' }] })
ma.emit(existing.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })

const config = parseConfig({ profiles: [{ id: 'self', name: 'Lab', mode: 'self', base_url: ma.url, api_key: 'env:MA_E2E_KEY',
  headers: { 'X-Ark-Beta': 'agentic-2026-06-01' }, model_headers: { Authorization: 'env:MA_E2E_MODEL' },
  start: { agent: 'agent-a', environment_id: 'env-1', label: 'Helper' }, poll_seconds: 1 }] })
const env = { MA_E2E_KEY: ma.key, MA_E2E_MODEL: `Bearer ${MODEL_KEY}` }

/** The session's conversation as a client reads it (a page through the node), polled until ok. */
async function readUntil(node, sid, what, ok, ms = 15_000) {
  return until(what, async () => {
    const page = await node.call('session.items', { session_id: sid, limit: 200 })
    return ok(page.items) ? page.items : undefined
  }, ms)
}

async function scenario(label, { daemon }) {
  const home = join(root, label, 'home')
  mkdirSync(join(home, '.persona'), { recursive: true })
  const logs = []
  const token = `t-${randomUUID()}`
  const runtimeOptions = daemon ? { listen: { port: 0, token }, answerFirst: 'phone' } : { retryMs: 100, answerFirst: 'phone' }
  let node
  let ma1
  const prefix = daemon ? 'mad.' : ''
  try {
    if (daemon) {
      ma1 = createManagedAgentsRuntime({ config, env, statePath: join(root, label, 'state.json'), log: (m) => logs.push(m), runtimeOptions })
      const addr = await ma1.start()
      writeFileSync(join(home, '.persona', 'daemons.json'), JSON.stringify({ daemons: [{ id: 'mad', name: 'Managed Agents', url: `ws://127.0.0.1:${addr.port}/persona/runtime`, token }] }), { mode: 0o600 })
      node = await startNode({ personad, home })
    } else {
      node = await startNode({ personad, home })
      ma1 = createManagedAgentsRuntime({ config, env, statePath: join(root, label, 'state.json'), log: (m) => logs.push(m), runtimeOptions: { ...runtimeOptions, socketPath: node.runtimeSocket } })
      await ma1.start()
    }
    const launcherName = daemon ? 'mad' : 'ma-self'
    const ls = await until('the launcher', async () => {
      const { launchers } = await node.call('launchers.list')
      return launchers.find((l) => l.name === launcherName) ? launchers : undefined
    }, 20_000)
    check(`${label}: connects and offers its launcher`, ls.some((l) => l.name === launcherName))

    // Agents list: an existing session, grouped by its Managed Agents agent.
    const exId = `${prefix}self.${existing.id}`
    const row = await until('the existing session listed', async () => ((await node.call('sessions.list')).sessions ?? []).find((s) => s.session_id === exId), 20_000)
    check(`${label}: an existing session is in the Agents list`, row.title === 'Existing report' && row.source_kind === 'runtime' && !row.pane_id && row.status === 'done', JSON.stringify({ title: row.title, status: row.status }))
    check(`${label}: it is grouped by its agent and labeled`, row.space_name === 'Helper' && row.agent_label === 'Managed Agents', `${row.space_name} / ${row.agent_label}`)
    check(`${label}: it can be messaged and stopped, not driven as a terminal`, row.capabilities?.send === true && row.capabilities?.interrupt === true && !row.capabilities?.terminal)

    // Reading: the conversation pages through the node.
    const page = await node.call('session.items', { session_id: exId })
    check(`${label}: its conversation reads through the node`, page.items.slice(0, 2).map((i) => `${i.role}:${i.text}`).join('|') === 'user:summarize Q3|assistant:Q3 grew 12%.' && page.history === `ma:self.${existing.id}`, JSON.stringify(page.items.map((i) => i.text)))

    // Sending: one message per client_msg_id, the reply arrives live and in the transcript.
    await node.call('session.watch', { session_id: exId, since_seq: 0 })
    const id = randomUUID()
    const before = ma.requests.filter((r) => r.method === 'POST' && r.path.endsWith(`/${existing.id}/events`)).length
    await node.call('session.send', { session_id: exId, text: `hello from ${label}`, client_msg_id: id })
    await node.call('session.send', { session_id: exId, text: `hello from ${label}`, client_msg_id: id })
    await until('the reply', () => node.items(exId).some((i) => i.text === `echo: hello from ${label}`), 10_000)
    const posts = ma.requests.filter((r) => r.method === 'POST' && r.path.endsWith(`/${existing.id}/events`))
    check(`${label}: a retried send reaches Managed Agents once`, posts.length - before === 1, String(posts.length - before))
    check(`${label}: the thinking shows too`, node.items(exId).some((i) => i.kind === 'thinking'))
    check(`${label}: live text arrives as deltas`, node.deltas(exId).some((t) => t === 'echo'), JSON.stringify(node.deltas(exId)))
    check(`${label}: the model key goes only with the send`, posts.at(-1).headers['x-user-model-header-authorization'] === `Bearer ${MODEL_KEY}` &&
      ma.requests.filter((r) => r.method === 'GET').every((r) => !r.headers['x-user-model-header-authorization']))
    check(`${label}: pushed batches are continuous`, node.gaps(exId).length === 0, JSON.stringify(node.gaps(exId)))

    // Starting: a new session from the launcher, with a first message that asks for permission.
    node.resolve = (ix) => (ix.kind === 'permission' ? { allow: true } : ix.kind === 'question' ? { answers: { 'Which region?': 'South' } } : { dismiss: true })
    const { session } = await node.call('session.start', { launcher: launcherName, cwd: '/tmp', prompt: 'ask before you deploy', client_msg_id: randomUUID() })
    const sid = session.session_id
    check(`${label}: a session started from the phone is a Managed Agents session`, sid.startsWith(`${prefix}self.sesn-`), sid)
    await node.call('session.watch', { session_id: sid, since_seq: 0 })
    const afterAllow = await readUntil(node, sid, 'the permission answered', (items) => items.some((i) => i.text === 'answered: user.tool_confirmation'))
    const card = node.asked.find((i) => i.kind === 'permission' && i.session_id === sid)
    check(`${label}: the tool confirmation reached the phone as a permission card`, card?.tool_name === 'Bash' && card?.summary === 'make deploy' && card?.agent_label === 'Managed Agents', JSON.stringify(card && { t: card.tool_name, s: card.summary, l: card.agent_label }))
    check(`${label}: the tool ran after the phone allowed it`, afterAllow.some((i) => i.kind === 'tool_result' && i.text === 'deployed'))

    // A question card.
    await node.call('session.send', { session_id: sid, text: 'question please', client_msg_id: randomUUID() })
    await readUntil(node, sid, 'the question answered', (items) => items.some((i) => i.text === 'answered: user.custom_tool_result'))
    const q = node.asked.find((i) => i.kind === 'question' && i.session_id === sid)
    const answer = ma.sessions.get(sid.split('.').at(-1)).events.find((e) => e.type === 'user.custom_tool_result')
    check(`${label}: AskQuestion reached the phone and its answer went back`, q?.questions?.[0]?.question === 'Which region?' && JSON.parse(answer.content[0].text).answers['Which region?'] === 'South')

    // Terminal calls are not this runtime's.
    const keys = await node.call('session.keys', { session_id: sid, keys: ['enter'] }).then(() => 'ok', (e) => e.message)
    check(`${label}: terminal calls are refused as unsupported`, /^unsupported/.test(keys), keys)

    // Keys never reached the node's or the runtime's logs.
    const all = node.log.join('') + logs.join('\n')
    check(`${label}: no key in any log`, !all.includes(ma.key) && !all.includes(MODEL_KEY))

    await ma1.stop()
    await until('its sessions leave with it', async () => !((await node.call('sessions.list')).sessions ?? []).some((s) => s.session_id === exId), 15_000)
    check(`${label}: its sessions leave with it`, true)
  } catch (err) {
    check(`${label}: run`, false, err.stack)
    console.log('--- node log ---\n' + (node?.log.join('').slice(-3000) ?? ''))
    console.log('--- runtime log ---\n' + logs.join('\n').slice(-2000))
  } finally {
    await ma1?.stop()
    node?.stop()
  }
}

try {
  await scenario('socket', { daemon: false })
  await scenario('daemon', { daemon: true })
} finally {
  ma.close()
}
process.exit(summary() ? 1 : 0)
