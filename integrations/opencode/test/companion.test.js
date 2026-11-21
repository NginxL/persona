import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { DatabaseSync } from 'node:sqlite'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { parseCompanionArgs } from '../companion.mjs'
import { buildAcpEnv, createOpencodeCompanion } from '../lib/companion.js'

const T0 = Date.now() - 60_000

/** In-memory opencode 1.x SQLite database (`session`, `message`, `part`). */
function fakeV1Db() {
  const db = new DatabaseSync(':memory:')
  db.exec(`
    create table session (
      id text primary key,
      parent_id text,
      directory text not null,
      title text,
      model text,
      revert text,
      time_created integer not null,
      time_updated integer not null,
      time_archived integer
    );
    create table message (
      id text primary key,
      session_id text not null,
      time_created integer not null,
      time_updated integer not null,
      data text not null
    );
    create table part (
      id text primary key,
      message_id text not null,
      session_id text not null,
      time_created integer not null,
      time_updated integer not null,
      data text not null
    );
  `)
  return {
    db,
    session(id, o = {}) {
      db.prepare('insert into session (id, parent_id, directory, title, model, revert, time_created, time_updated, time_archived) values (?,?,?,?,?,?,?,?,?)')
        .run(id, o.parent ?? null, o.directory ?? '/w/app', o.title ?? 'Existing session', o.model ?? null, o.revert ?? null, o.created ?? T0, o.updated ?? Date.now(), o.archived ?? null)
    },
    message(sessionId, msgId, role, parts, extra = {}) {
      const ts = extra.created ?? Date.now()
      db.prepare('insert into message (id, session_id, time_created, time_updated, data) values (?,?,?,?,?)')
        .run(msgId, sessionId, ts, ts, JSON.stringify({ role, time: { created: ts, ...(role === 'assistant' ? { completed: ts + 1 } : {}) }, ...extra }))
      parts.forEach((p, idx) => {
        db.prepare('insert into part (id, message_id, session_id, time_created, time_updated, data) values (?,?,?,?,?,?)')
          .run(`${msgId}_p${idx}`, msgId, sessionId, ts + idx, ts + idx, JSON.stringify(p))
      })
    },
    openDb: async () => ({ all: (sql, ...p) => db.prepare(sql).all(...p), close() {} }),
  }
}

/** Fake OpencodeAcpClient driving companion tests deterministically without spawning opencode. */
class FakeAcpClient extends EventEmitter {
  constructor() {
    super()
    this.generation = 1
    this.pid = 0
    this.busy = false
    this.loadedSessions = new Set()
    this.calls = []
    this.nextId = 1
    this.onPermissionRequest = null
    this.onNewSession = null
    this.onPrompt = null
    this.failModeSet = null
    this.sessionState = new Map()
    this.configModels = [
      { value: 'opencode-go/claude-sonnet-4-6', name: 'opencode-go/Claude Sonnet 4.6' },
      { value: 'openai/gpt-5', name: 'openai/GPT-5' },
    ]
  }
  async ensureStarted() {
    this.calls.push(['ensureStarted'])
    if (!this.pid) {
      this.pid = 40000 + this.generation
      this.generation++
    }
    return { agentCapabilities: { loadSession: true, mcpCapabilities: { http: true } } }
  }
  async stopChild() {
    this.calls.push(['stopChild'])
    this.pid = 0
    this.loadedSessions.clear()
  }
  async newSession({ cwd, mcpServers }) {
    this.calls.push(['newSession', { cwd, mcpServers }])
    const sessionId = `ses_acp_${this.nextId++}`
    this.loadedSessions.add(sessionId)
    if (this.onNewSession) await this.onNewSession({ sessionId, cwd, mcpServers })
    return {
      sessionId,
      configOptions: [
        { id: 'mode', category: 'mode', currentValue: 'build', options: [{ value: 'build', name: 'Build' }, { value: 'plan', name: 'Plan' }] },
        { id: 'model', category: 'model', currentValue: 'opencode-go/claude-sonnet-4-6', options: this.configModels },
      ],
    }
  }
  async loadSession({ sessionId, cwd, mcpServers }) {
    this.calls.push(['loadSession', { sessionId, cwd, mcpServers }])
    this.loadedSessions.add(sessionId)
    return {
      configOptions: [
        { id: 'mode', category: 'mode', currentValue: 'build', options: [{ value: 'build', name: 'Build' }, { value: 'plan', name: 'Plan' }] },
        { id: 'model', category: 'model', currentValue: 'opencode-go/claude-sonnet-4-6', options: this.configModels },
      ],
    }
  }
  async resumeSession(params) {
    this.calls.push(['resumeSession', params])
    this.loadedSessions.add(params.sessionId)
    return { configOptions: [
      { id: 'mode', category: 'mode', currentValue: 'build', options: [{ value: 'build' }, { value: 'plan' }] },
      { id: 'model', category: 'model', currentValue: 'opencode-go/claude-sonnet-4-6', options: this.configModels },
    ] }
  }
  async setConfigOption(sessionId, configId, value) {
    this.calls.push(['setConfigOption', { sessionId, configId, value }])
    if (this.failModeSet && configId === 'mode' && value === this.failModeSet) {
      throw new Error('plan mode unavailable')
    }
    const st = this.sessionState.get(sessionId) || { mode: 'build', model: 'opencode-go/claude-sonnet-4-6', variant: 'low' }
    st[configId] = value
    this.sessionState.set(sessionId, st)
    return {
      configOptions: [
        { id: 'mode', category: 'mode', currentValue: st.mode, options: [{ value: 'build', name: 'Build' }, { value: 'plan', name: 'Plan' }] },
        { id: 'model', category: 'model', currentValue: st.model, options: this.configModels },
        { id: 'variant', category: 'reasoning', currentValue: st.variant, options: [{ value: 'low' }, { value: 'high' }] },
      ],
    }
  }
  async prompt(sessionId, promptBlocks) {
    this.calls.push(['prompt', { sessionId, prompt: promptBlocks }])
    if (this.onPrompt) return this.onPrompt(sessionId, promptBlocks, this)
    this.emit('update', { sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Hel' } } })
    this.emit('update', { sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'lo!' } } })
    return { stopReason: 'end_turn' }
  }
  cancel(sessionId) {
    this.calls.push(['cancel', { sessionId }])
  }
  async closeSession(sessionId) {
    this.calls.push(['closeSession', { sessionId }])
    this.loadedSessions.delete(sessionId)
  }
  async stop() {
    this.loadedSessions.clear()
  }
}

// Legacy feature tests share their expectation log and input controls, but
// each assistant still receives a different ACP object and loaded-session set.
function createTestCompanion(options) {
  const parent = options.acp
  return createOpencodeCompanion({ ...options, assistantAcpFactory: () => {
    const child = new FakeAcpClient()
    child.calls = parent.calls
    for (const key of ['nextId', 'onPrompt', 'onNewSession', 'failModeSet', 'configModels']) {
      Object.defineProperty(child, key, { get: () => parent[key], set: (value) => { parent[key] = value } })
    }
    child.newSession = async (params) => {
      const result = await parent.newSession.call(child, params)
      child.loadedSessions.add(result.sessionId)
      return result
    }
    return child
  } })
}

test('companion advertises launchers/models/assistant without supervision_policy, starts Agent session, streams, handles permissions, modes, rename, close', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  db.session('ses_disk_1', { directory: '/w/old', title: 'Old TUI session' })
  db.message('ses_disk_1', 'm1', 'user', [{ type: 'text', text: 'from tui' }])
  db.message('ses_disk_1', 'm2', 'assistant', [{ type: 'text', text: 'reply from tui' }], { modelID: 'gpt-5', providerID: 'openai' })

  const acp = new FakeAcpClient()
  const companion = createTestCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    model: 'opencode-go/claude-sonnet-4-6',
    openDb: db.openDb,
    acp,
    discoverModels: async () => [
      { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6', limit: { context: 200000 }, variants: { low: {}, high: {} } },
      { providerID: 'openai', id: 'gpt-5', name: 'GPT-5', limit: { context: 128000 } },
    ],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  assert.equal(n.hello.runtime, 'opencode')
  assert.equal(n.hello.agent, 'opencode')
  assert.equal(n.hello.launcher, 'opencode')
  assert.equal(n.hello.caps.start, true)
  assert.equal(n.hello.caps.models, true)
  assert.equal(n.hello.caps.assistant, true)
  assert.equal(n.hello.caps.readonly, true)
  assert.equal(n.hello.caps.model, true)
  assert.ok(!n.hello.caps.supervision_policy, 'must never declare supervision_policy (#11220)')

  // Disk-only sessions are listed as offline so a running opencode serve plugin wins via prefer_running.
  await until('disk session listed', () => n.last('runtime.sessions')?.sessions?.some((s) => s.session_id === 'ses_disk_1'))
  const diskRow = n.last('runtime.sessions').sessions.find((s) => s.session_id === 'ses_disk_1')
  assert.equal(diskRow.status, 'offline')
  assert.equal(diskRow.title, 'Old TUI session')

  // Reading a foreign session returns not_found.
  await assert.rejects(n.peer.call('session.items', { session_id: 'ses_unknown', limit: 1 }), (err) => err.code === 'not_found')

  // Reading a v1 DB session works cleanly.
  const diskItems = await n.peer.call('session.items', { session_id: 'ses_disk_1', limit: 10 })
  assert.deepEqual(diskItems.items.map((i) => i.text), ['from tui', 'reply from tui'])

  // Models RPC returns normalized catalog.
  const cat = await n.peer.call('launchers.models', { refresh: true })
  assert.deepEqual(cat.models.map((m) => m.id), ['opencode-go/claude-sonnet-4-6', 'openai/gpt-5'])
  assert.deepEqual(cat.models[0].efforts, ['low', 'high'])

  // Start a new Agent session with model, prompt, and image; verify permission card + streaming + tool_call.
  n.resolve = () => ({ allow: true })
  acp.onPrompt = async (sessionId, blocks, client) => {
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'plan' } } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'tool_call', toolCallId: 'tc_1', kind: 'execute', title: 'bash', rawInput: { command: 'ls' } } })
    const perm = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_1', kind: 'execute', title: 'bash', rawInput: { command: 'ls' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
        { optionId: 'always', kind: 'allow_always', name: 'Always allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(perm, { outcome: { outcome: 'selected', optionId: 'once' } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'tool_call_update', toolCallId: 'tc_1', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'file.txt' } }] } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'All good.' } } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'usage_update', used: 1500, size: 200000 } })
    return { stopReason: 'end_turn' }
  }

  const started = await n.peer.call('session.start', {
    cwd: '/w/app',
    model: 'openai/gpt-5',
    prompt: 'inspect dir',
    images: [{ mime: 'image/png', data: 'QUFB' }],
    client_msg_id: 'cm_1',
  })
  const sid = started.session.session_id
  assert.equal(started.session.started_by_persona, true)
  assert.equal(started.session.model, 'openai/gpt-5')
  await n.peer.call('session.watch', { session_id: sid, since_seq: 0 })

  await until('turn finished', () => n.last('runtime.sessions')?.sessions?.find((s) => s.session_id === sid)?.status === 'done')
  assert.equal(n.asked.length, 1)
  assert.equal(n.asked[0].tool_name, 'Bash')
  assert.equal(n.asked[0].summary, 'ls')

  const pg = await n.peer.call('session.items', { session_id: sid, limit: 20 })
  assert.deepEqual(pg.items.map((i) => i.kind), ['text', 'image', 'thinking', 'tool_use', 'tool_result', 'text'])
  assert.equal(pg.items[0].client_msg_id, 'cm_1')
  assert.equal(pg.items[5].text, 'All good.')

  // Lazy image lookup works.
  const img = await n.peer.call('session.image', { session_id: sid, item_id: pg.items[1].id })
  assert.deepEqual(img, { mime: 'image/png', data: 'QUFB' })

  // Switch mode/model via session.modes, rename, and close.
  await n.peer.call('session.modes', { session_id: sid, permission_mode: 'plan', model: 'opencode-go/claude-sonnet-4-6' })
  assert.equal(companion.live.get(sid).mode, 'plan')
  assert.equal(companion.live.get(sid).model, 'opencode-go/claude-sonnet-4-6')

  await n.peer.call('session.rename', { session_id: sid, title: 'Renamed chat' })
  assert.equal(companion.live.get(sid).title, 'Renamed chat')

  await n.peer.call('session.close', { session_id: sid })
  assert.equal(companion.live.has(sid), false)
  assert.ok(acp.calls.some((c) => c[0] === 'closeSession' && c[1].sessionId === sid))
})

test('the ACP companion exposes A2A tools and forwards calls from its own Agent session', async (t) => {
  const n = await startFakeNode(t, { tools: [{ name: 'a2a_report', description: 'Report a task result' }] })
  const acp = new FakeAcpClient()
  let endpoint
  acp.onNewSession = ({ mcpServers }) => { [endpoint] = mcpServers }
  const companion = createTestCompanion({ socketPath: n.path, openDb: fakeV1Db().openDb, acp })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready
  const { session } = await n.peer.call('session.start', { cwd: '/w/app' })
  const headers = { 'Content-Type': 'application/json', ...Object.fromEntries(endpoint.headers.map(({ name, value }) => [name, value])) }
  const mcp = (method, params) => fetch(endpoint.url, {
    method: 'POST', headers, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }),
  }).then((res) => res.json())
  assert.deepEqual((await mcp('tools/list', {})).result.tools.map(({ name }) => name), ['a2a_report'])
  const args = { task_id: 'task', message_id: 'report', state: 'completed', text: 'Done' }
  assert.equal((await mcp('tools/call', { name: 'a2a_report', arguments: args })).result.content[0].text, 'a2a_report ran')
  assert.deepEqual(n.toolCalls, [{ name: 'a2a_report', args, session_id: session.session_id }])
})

test('companion assistant mode wires HTTP MCP end-to-end, hides context, enforces memory_dir, plan readonly_unsupported, command gates, and rejects supervision_instructions', async (t) => {
  const n = await startFakeNode(t, {
    tools: [
      { name: 'kb_search', description: 'Search KB', input_schema: { type: 'object', properties: {} } },
      { name: 'mail_send', description: 'Send mail', input_schema: { type: 'object', properties: {} }, shell: ['send-mail'] },
    ],
  })
  const db = fakeV1Db()
  const acp = new FakeAcpClient()
  let mcpEndpoint
  acp.onNewSession = async ({ mcpServers }) => {
    mcpEndpoint = mcpServers[0]
    const hdrs = Object.fromEntries(mcpEndpoint.headers.map((h) => [h.name, h.value]))
    // Simulate opencode acp connecting to our HTTP MCP server during session/new (before session.start returns).
    const listRes = await fetch(mcpEndpoint.url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...hdrs },
      body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list', params: {} }),
    }).then((r) => r.json())
    assert.deepEqual(listRes.result.tools.map((x) => x.name), ['kb_search', 'mail_send'])
  }

  const companion = createTestCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    openDb: db.openDb,
    acp,
    discoverModels: async () => [{ providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' }],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  // Supervision instructions must fail closed (#11220).
  await assert.rejects(
    n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true, supervision_instructions: 'supervision' }),
    (err) => err.code === 'assistant_supervision_runtime',
  )

  // Start assistant session.
  const started = await n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true })
  const sid = started.session.session_id
  assert.ok(mcpEndpoint)

  // Now call a Persona MCP tool over the bound route during a turn.
  const hdrs = Object.fromEntries(mcpEndpoint.headers.map((h) => [h.name, h.value]))
  acp.onPrompt = async (sessionId, blocks, client) => {
    assert.match(blocks[0].text, /^<persona-context>\nnow=2026\n<\/persona-context>\n\n帮我查笔记$/)
    const callRes = await fetch(mcpEndpoint.url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...hdrs },
      body: JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'kb_search', arguments: { q: 'plan' } } }),
    }).then((r) => r.json())
    assert.equal(callRes.result.content[0].text, 'kb_search ran')

    // Command gate must be rejected even under bypassPermissions.
    const gated = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_gate', kind: 'execute', title: 'bash', rawInput: { command: 'send-mail --to alice' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(gated, { outcome: { outcome: 'selected', optionId: 'reject' } })
    n.setHub(false)
    await until('hub disconnected during native permission', () => !companion.runtime.hubConnected)
    const disconnected = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_offline', kind: 'execute', title: 'bash', rawInput: { command: 'pwd' } },
      options: [{ optionId: 'once', kind: 'allow_once', name: 'Allow' }, { optionId: 'reject', kind: 'reject_once', name: 'Reject' }],
    })
    assert.deepEqual(disconnected, { outcome: { outcome: 'selected', optionId: 'reject' } })
    n.setHub(true)
    await until('hub reconnected during native permission', () => companion.runtime.hubConnected)

    // Memory write auto-allows without asking phone.
    const mem = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_mem', kind: 'edit', title: 'write', rawInput: { filePath: '/home/u/.persona/assistant/memory/a.md' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(mem, { outcome: { outcome: 'selected', optionId: 'once' } })

    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: '查到了' } } })
    return { stopReason: 'end_turn' }
  }

  await n.peer.call('session.send', {
    session_id: sid,
    text: '帮我查笔记',
    context: 'now=2026',
    assistant: true,
    mode: 'bypassPermissions',
    memory_dir: '/home/u/.persona/assistant/memory',
    client_msg_id: 'cm_asst_1',
  })
  await until('assistant done', () => n.last('runtime.sessions')?.sessions?.find((s) => s.session_id === sid)?.status === 'done')
  assert.equal(n.toolCalls.length, 1)
  assert.equal(n.toolCalls[0].assistant, true)
  assert.equal(n.toolCalls[0].session_id, sid)
  assert.equal(n.asked.length, 0)

  // Context snapshot is hidden from visible items.
  const itemsPage = await n.peer.call('session.items', { session_id: sid, limit: 10 })
  assert.equal(itemsPage.items[0].text, '帮我查笔记')
  assert.equal(itemsPage.items[1].text, '查到了')

  // Failure to enter plan mode surfaces readonly_unsupported.
  acp.failModeSet = 'plan'
  await assert.rejects(
    n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true, mode: 'plan' }),
    (err) => err.code === 'readonly_unsupported',
  )
})

test('companion resumes a disk-only session via ACP session/load and supports interrupt', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  db.session('ses_resume_1', { directory: '/w/app', title: 'Resume me' })
  db.message('ses_resume_1', 'm1', 'user', [{ type: 'text', text: 'first' }])

  const acp = new FakeAcpClient()
  let resolvePrompt
  acp.onPrompt = () => new Promise((r) => { resolvePrompt = r })

  const companion = createTestCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    openDb: db.openDb,
    acp,
    discoverModels: async () => [],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  await n.peer.call('session.send', { session_id: 'ses_resume_1', text: 'continue', client_msg_id: 'cm_r1' })
  await until('loadSession called', () => acp.calls.some((c) => c[0] === 'loadSession' && c[1].sessionId === 'ses_resume_1'))
  await until('prompt started', () => resolvePrompt !== undefined)

  const intrPromise = n.peer.call('session.interrupt', { session_id: 'ses_resume_1' })
  await until('cancel sent', () => acp.calls.some((c) => c[0] === 'cancel' && c[1].sessionId === 'ses_resume_1'))
  resolvePrompt({ stopReason: 'cancelled' })
  const intr = await intrPromise
  assert.equal(intr.settled, true)

  const pg = await n.peer.call('session.items', { session_id: 'ses_resume_1', limit: 10 })
  assert.equal(pg.items.at(-1).text, '已中断')
})

test('parseCompanionArgs honors --bin, --opencode-bin, --name, --display-name, PERSONA_OPENCODE_BIN, OPENCODE_BIN, and rejects unknown flags', () => {
  const fromDocFlags = parseCompanionArgs(
    ['--bin', '/opt/oc', '--name', 'My OC', '--model', 'openai/gpt-5', '--no-assistant'],
    {},
  )
  assert.equal(fromDocFlags.bin, '/opt/oc')
  assert.equal(fromDocFlags.name, 'My OC')
  assert.equal(fromDocFlags.displayName, 'My OC')
  assert.equal(fromDocFlags.model, 'openai/gpt-5')
  assert.equal(fromDocFlags.assistant, false)

  const fromAliases = parseCompanionArgs(['--opencode-bin', '/usr/bin/oc', '--display-name', 'Alias OC'], {})
  assert.equal(fromAliases.bin, '/usr/bin/oc')
  assert.equal(fromAliases.name, 'Alias OC')
  assert.equal(fromAliases.displayName, 'Alias OC')

  const fromEnv = parseCompanionArgs([], { PERSONA_OPENCODE_BIN: '/env/persona-oc', OPENCODE_BIN: '/env/old-oc' })
  assert.equal(fromEnv.bin, '/env/persona-oc')
  const fromLegacyEnv = parseCompanionArgs([], { OPENCODE_BIN: '/env/old-oc' })
  assert.equal(fromLegacyEnv.bin, '/env/old-oc')

  assert.throws(() => parseCompanionArgs(['--unknown-flag', '1'], {}))
  const bounded = parseCompanionArgs(['--assistant-workers', '3', '--assistant-idle-ms', '2500'], {})
  assert.equal(bounded.maxAssistantWorkers, 3)
  assert.equal(bounded.assistantIdleMs, 2500)
  for (const value of ['0', '65', '1.5', 'NaN']) assert.throws(() => parseCompanionArgs(['--assistant-workers', value], {}))
  for (const value of ['0', '-1', 'Infinity']) assert.throws(() => parseCompanionArgs(['--assistant-idle-ms', value], {}))

  const acpEnv = buildAcpEnv({ OPENCODE_PERMISSION: JSON.stringify({ read: 'allow', bash: 'allow' }) })
  assert.deepEqual(JSON.parse(acpEnv.OPENCODE_PERMISSION), { read: 'allow', bash: 'ask', edit: 'ask', write: 'ask' })
})

test('companion enforces raw unclipped command gates, relative AGENTS.md rejection, start model switch without configOptions, default preservation, plan mode across ACP restart, effort switching, acceptEdits, live thinking, and v2 blob + client_msg_id', async (t) => {
  const n = await startFakeNode(t, {
    tools: [
      { name: 'mail_send', description: 'Send mail', input_schema: { type: 'object', properties: {} }, shell: ['send-mail'] },
    ],
  })

  // Build a v2 DB with a historical session, a blob-backed image, and 2 prior user messages.
  const v2db = new DatabaseSync(':memory:')
  v2db.exec(`
    create table session_v2 (
      id text primary key,
      parent_id text,
      directory text not null,
      title text,
      model text,
      revert text,
      time_created integer not null,
      time_updated integer not null,
      time_archived integer
    );
    create table session_message (
      id text primary key,
      session_id text not null,
      seq integer not null,
      type text not null,
      data text not null,
      time_updated integer not null
    );
    create table blob (
      hash text primary key,
      data blob not null
    );
    create table blob_ref (
      hash text not null,
      session_id text not null,
      message_id text not null,
      seq integer not null
    );
  `)
  const imgBytes = Buffer.from('fake-png-bytes')
  const imgHash = createHash('sha256').update(imgBytes).digest('hex')
  v2db.prepare('insert into session_v2 (id, parent_id, directory, title, model, revert, time_created, time_updated, time_archived) values (?,?,?,?,?,?,?,?,?)')
    .run('ses_v2_hist', null, '/w/v2', 'V2 Hist', null, null, T0, Date.now(), null)
  v2db.prepare('insert into session_message (id, session_id, seq, type, data, time_updated) values (?,?,?,?,?,?)')
    .run('u1', 'ses_v2_hist', 1, 'user', JSON.stringify({ text: 'first msg', files: [{ mime: 'image/png', uri: `media:${imgHash}` }] }), Date.now())
  v2db.prepare('insert into session_message (id, session_id, seq, type, data, time_updated) values (?,?,?,?,?,?)')
    .run('a1', 'ses_v2_hist', 2, 'assistant', JSON.stringify({ content: [{ type: 'text', text: 'first reply' }] }), Date.now())
  v2db.prepare('insert into blob (hash, data) values (?,?)').run(imgHash, imgBytes)
  v2db.prepare('insert into blob_ref (hash, session_id, message_id, seq) values (?,?,?,?)').run(imgHash, 'ses_v2_hist', 'u1', 1)

  const acp = new FakeAcpClient()
  let returnEmptyConfigOnNew = false
  const origNew = acp.newSession.bind(acp)
  acp.newSession = async (args) => {
    if (returnEmptyConfigOnNew) {
      acp.calls.push(['newSession', args])
      const sessionId = `ses_acp_${acp.nextId++}`
      acp.loadedSessions.add(sessionId)
      return { sessionId } // No configOptions returned!
    }
    const res = await origNew(args)
    res.configOptions.push({
      id: 'variant',
      category: 'reasoning',
      currentValue: 'low',
      options: [{ value: 'low' }, { value: 'high' }],
    })
    return res
  }

  const companion = createTestCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    // No explicit default model! CLI catalog's first model differs from ACP's currentValue.
    openDb: async () => ({ all: (sql, ...p) => v2db.prepare(sql).all(...p), close() {} }),
    acp,
    discoverModels: async () => [
      { providerID: 'openai', id: 'gpt-5', name: 'GPT-5', variants: { low: {}, high: {} } },
      { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6', variants: { low: {}, high: {} } },
    ],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready
  await n.peer.call('launchers.models', { refresh: true })

  // 1. Blob-backed 2.x image loads in companion.
  const histItems = await n.peer.call('session.items', { session_id: 'ses_v2_hist', limit: 10 })
  const imgItem = histItems.items.find((i) => i.kind === 'image')
  assert.ok(imgItem)
  const blobImg = await n.peer.call('session.image', { session_id: 'ses_v2_hist', item_id: imgItem.id })
  assert.deepEqual(blobImg, { mime: 'image/png', data: imgBytes.toString('base64') })

  // 2. Resumed v2 session initializes userInputCount from DB so client_msg_id attaches to the new message (ordinal 2).
  acp.onPrompt = async (sessionId) => {
    v2db.prepare('insert into session_message (id, session_id, seq, type, data, time_updated) values (?,?,?,?,?,?)')
      .run('u2', sessionId, 3, 'user', JSON.stringify({ text: 'second msg' }), Date.now())
    v2db.prepare('insert into session_message (id, session_id, seq, type, data, time_updated) values (?,?,?,?,?,?)')
      .run('a2', sessionId, 4, 'assistant', JSON.stringify({ content: [{ type: 'text', text: 'second reply' }] }), Date.now())
    return { stopReason: 'end_turn' }
  }
  await n.peer.call('session.send', { session_id: 'ses_v2_hist', text: 'second msg', client_msg_id: 'cm_v2_2' })
  await until('v2 turn done', () => !companion.live.get('ses_v2_hist')?.running)
  const v2After = await n.peer.call('session.items', { session_id: 'ses_v2_hist', limit: 10 })
  const secondUser = v2After.items.find((i) => i.text === 'second msg')
  assert.equal(secondUser?.client_msg_id, 'cm_v2_2')

  // 3. Starting without an explicit default model must NOT override ACP session/new's currentValue with models[0].
  acp.calls.length = 0
  const defStart = await n.peer.call('session.start', { cwd: '/w/app' })
  assert.equal(defStart.session.model, 'opencode-go/claude-sonnet-4-6')
  assert.equal(acp.calls.filter((c) => c[0] === 'setConfigOption' && c[1].configId === 'model').length, 0)

  // 4. Starting with a requested model when session/new returns NO configOptions MUST still call setConfigOption('model', ...).
  returnEmptyConfigOnNew = true
  acp.calls.length = 0
  const explicitStart = await n.peer.call('session.start', { cwd: '/w/app', model: 'openai/gpt-5', effort: 'high' })
  assert.equal(explicitStart.session.model, 'openai/gpt-5')
  assert.equal(explicitStart.session.effort, 'high')
  assert.ok(acp.calls.some((c) => c[0] === 'setConfigOption' && c[1].configId === 'model' && c[1].value === 'openai/gpt-5'))
  assert.ok(acp.calls.some((c) => c[0] === 'setConfigOption' && c[1].configId === 'variant' && c[1].value === 'high'))
  returnEmptyConfigOnNew = false

  // 5. Plan mode survives an ACP child restart (ensureLoaded re-applies mode=plan).
  const planStart = await n.peer.call('session.start', { cwd: '/w/app', permission_mode: 'plan' })
  const planSid = planStart.session.session_id
  assert.equal(companion.live.get(planSid).mode, 'plan')
  acp.generation++
  acp.loadedSessions.clear()
  acp.calls.length = 0
  acp.onPrompt = async () => ({ stopReason: 'end_turn' })
  await n.peer.call('session.send', { session_id: planSid, text: 'check plan' })
  await until('plan turn done', () => !companion.live.get(planSid)?.running)
  assert.equal(companion.live.get(planSid).mode, 'plan')
  assert.ok(acp.calls.some((c) => c[0] === 'setConfigOption' && c[1].sessionId === planSid && c[1].configId === 'mode' && c[1].value === 'plan'))

  // 6. Long 26KB padded command + shell syntax (`send-mail; echo`) + relative AGENTS.md write + acceptEdits + live thinking.
  const asstStart = await n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true })
  const asstSid = asstStart.session.session_id
  await n.peer.call('session.watch', { session_id: asstSid, since_seq: 0 })
  n.asked.length = 0
  n.resolve = () => ({ allow: false })

  acp.onPrompt = async (sessionId, _blocks, client) => {
    // Live thinking streams incrementally.
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'step 1 ' } } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'step 2' } } })

    // 26KB padded command ending in `; send-mail` (clipInput would truncate to 12KB) must STILL be rejected under bypassPermissions!
    const longCmd = `echo ${'a'.repeat(26_000)}; "send-mail" --to alice`
    const gated = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_long', kind: 'execute', title: 'bash', rawInput: { command: longCmd } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(gated, { outcome: { outcome: 'selected', optionId: 'reject' } })

    // Switch to default mode and check relative `AGENTS.md` is NOT auto-approved as memory write.
    companion.live.get(sessionId).mode = 'default'
    const relAgents = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_agents', kind: 'edit', title: 'write', rawInput: { filePath: 'AGENTS.md' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.equal(n.asked.length, 1, 'relative AGENTS.md must ask phone')
    assert.deepEqual(relAgents, { outcome: { outcome: 'selected', optionId: 'reject' } })

    // acceptEdits auto-approves write/edit without asking phone.
    companion.live.get(sessionId).mode = 'acceptEdits'
    const editOk = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_edit_ok', kind: 'edit', title: 'edit', rawInput: { filePath: '/w/code.js' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(editOk, { outcome: { outcome: 'selected', optionId: 'once' } })
    assert.equal(n.asked.length, 1)

    return { stopReason: 'end_turn' }
  }

  await n.peer.call('session.send', {
    session_id: asstSid,
    text: 'run checks',
    assistant: true,
    mode: 'bypassPermissions',
    memory_dir: '/home/u/.persona/assistant/memory',
  })
  await until('asst checks done', () => !companion.live.get(asstSid)?.running)
  const thoughtItems = (await n.peer.call('session.items', { session_id: asstSid, limit: 10 })).items.filter((i) => i.kind === 'thinking')
  assert.equal(thoughtItems.length, 1)
  assert.equal(thoughtItems[0].text, 'step 1 step 2')
})

test('parseCompanionArgs supports --instance, --no-update, and --parent-stdin with strict validation', () => {
  const parsed = parseCompanionArgs([
    '--socket', '/tmp/runtime.sock',
    '--bin', '/usr/local/bin/opencode',
    '--instance', 'builtin-opencode-42-1',
    '--no-update',
    '--parent-stdin',
  ], {})
  assert.equal(parsed.socketPath, '/tmp/runtime.sock')
  assert.equal(parsed.bin, '/usr/local/bin/opencode')
  assert.equal(parsed.instance, 'builtin-opencode-42-1')
  assert.equal(parsed.noUpdate, true)
  assert.equal(parsed.parentStdin, true)
  assert.equal(parsed.assistant, true)
  assert.throws(() => parseCompanionArgs(['--unknown-flag'], {}))
})

test('companion.mjs with --parent-stdin exits cleanly when parent closes stdin', async () => {
  const script = fileURLToPath(new URL('../companion.mjs', import.meta.url))
  const child = spawn(process.execPath, [
    script,
    '--socket', '/tmp/persona-opencode-nonexistent-test.sock',
    '--bin', '/nonexistent/opencode-test-bin',
    '--instance', 'builtin-opencode-test-1',
    '--no-update',
    '--parent-stdin',
  ], {
    stdio: ['pipe', 'pipe', 'pipe'],
  })
  let stderr = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (chunk) => { stderr += chunk })

  await until('companion started', () => stderr.includes('started opencode ACP companion for Persona'), 5000)
  child.stdin.end()

  const exit = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      try { child.kill('SIGKILL') } catch {}
      reject(new Error(`companion did not exit after stdin closed; stderr=${stderr}`))
    }, 5000)
    child.once('exit', (code, signal) => {
      clearTimeout(timer)
      resolve({ code, signal })
    })
  })
  assert.equal(exit.code, 0)
  assert.equal(exit.signal, null)
})

test('companion does not spawn opencode acp on startup, stops idle acp child, and reloads on next turn', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  const acp = new FakeAcpClient()
  const companion = createOpencodeCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    instance: 'builtin-opencode-idle-1',
    acpIdleMs: 40,
    openDb: db.openDb,
    acp,
    discoverModels: async () => [{ providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' }],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  // Hello carries the configured --instance and never declares supervision_policy.
  assert.equal(n.hello.instance, 'builtin-opencode-idle-1')
  assert.equal(n.hello.caps?.supervision_policy, undefined)

  // Idle startup never spawns opencode acp.
  assert.equal(acp.pid, 0)
  assert.equal(acp.calls.filter((c) => c[0] === 'ensureStarted').length, 0)

  // Starting a session spawns acp; once idle, acp.stopChild() reclaims it.
  const started = await n.peer.call('session.start', { cwd: '/w/app', prompt: 'hello' })
  const sid = started.session.session_id
  await until('first turn done', () => !companion.live.get(sid)?.running)
  await until('idle acp child stopped', () => acp.pid === 0 && acp.calls.some((c) => c[0] === 'stopChild'), 2000)

  // Next send transparently restarts acp and reloads the session via session/load.
  acp.calls.length = 0
  await n.peer.call('session.send', { session_id: sid, text: 'follow up' })
  await until('second turn done', () => !companion.live.get(sid)?.running)
  assert.ok(acp.calls.some((c) => c[0] === 'ensureStarted'))
  assert.ok(acp.calls.some((c) => c[0] === 'loadSession' && c[1].sessionId === sid))
  await until('idle acp child stopped again', () => acp.pid === 0 && acp.calls.some((c) => c[0] === 'stopChild'), 2000)
})
