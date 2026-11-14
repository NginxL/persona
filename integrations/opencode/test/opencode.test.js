import { test } from 'node:test'
import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createOpencodeHost } from '../lib/opencode.js'

const T0 = Date.now() - 60_000

/** opencode.db with the two tables the runtime reads (the columns it reads, as opencode creates them). */
function fakeDb({ busy = true } = {}) {
  const db = new DatabaseSync(':memory:')
  db.exec(`create table session_v2 (id text primary key, parent_id text, directory text not null, title text, model text,
    revert text, time_created integer not null, time_updated integer not null, time_idle integer,${busy ? ' time_busy integer,' : ''} time_archived integer);
    create table session_message (id text primary key, session_id text not null, type text not null, seq integer not null,
    time_created integer not null, time_updated integer not null, data text not null);`)
  const api = {
    db,
    session(id, o = {}) {
      db.prepare('insert into session_v2 (id, parent_id, directory, title, model, revert, time_created, time_updated, time_archived) values (?,?,?,?,?,?,?,?,?)')
        .run(id, o.parent ?? null, o.directory ?? '/w/app', o.title ?? null, o.model ?? null, o.revert ?? null, o.created ?? T0, o.updated ?? Date.now(), o.archived ?? null)
    },
    message(session, seq, type, data, updated = Date.now()) {
      const id = `msg_${session}_${seq}`
      db.prepare('insert or replace into session_message (id, session_id, type, seq, time_created, time_updated, data) values (?,?,?,?,?,?,?)')
        .run(id, session, type, seq, T0 + seq, updated, JSON.stringify({ time: { created: T0 + seq, ...(data.time ?? {}) }, ...data, time2: undefined }))
    },
    user: (s, seq, text) => api.message(s, seq, 'user', { text }),
    assistant: (s, seq, text, extra = {}) => api.message(s, seq, 'assistant', { content: text ? [{ type: 'text', text }] : [], finish: 'stop', time: { completed: T0 + seq + 1 },
      model: { id: 'model-x', providerID: 'prov' }, tokens: { input: 1000, output: 10, reasoning: 0, cache: { read: 3000, write: 0 } }, ...extra }),
    openDb: async () => ({ all: (sql, ...p) => db.prepare(sql).all(...p), close() {} }),
  }
  return api
}

/** The plugin context of one location: the session / permission API as calls it records, and an event stream the test feeds. */
function fakeCtx(directory = '/w/app') {
  const queue = []
  let wake
  const ctx = {
    location: { directory },
    calls: [],
    session: {},
    permission: {},
    model: { list: async () => [{ id: 'model-x', providerID: 'prov', limit: { context: 200000 } }] },
    event: {
      async *subscribe({ signal }) {
        while (!signal?.aborted) {
          if (queue.length) { yield queue.shift(); continue }
          await new Promise((r) => { wake = r; signal?.addEventListener('abort', r, { once: true }) })
        }
      },
    },
    emit(type, data, id = `evt_${Math.random()}`) { queue.push({ id, type, data }); wake?.() },
  }
  for (const [ns, names] of [['session', ['prompt', 'interrupt', 'update']], ['permission', ['reply']]]) {
    for (const name of names) ctx[ns][name] = async (args) => { ctx.calls.push([`${ns}.${name}`, args]); return {} }
  }
  return ctx
}

async function setup(t, opts = {}) {
  const n = await startFakeNode(t)
  const d = opts.db ?? fakeDb()
  const host = createOpencodeHost({ openDb: d.openDb, pid: 4242, log: opts.log, now: opts.now }, { socketPath: n.path, retryMs: 20, pushDelayMs: 5, answerFirst: opts.answerFirst, listDays: opts.listDays })
  t.after(() => host.stop())
  return { n, d, host }
}

test('one runtime per process lists the recent root sessions of the database', async (t) => {
  const d = fakeDb()
  d.session('ses_a', { title: 'Fix the parser', model: '{"id":"model-x"}' })
  d.user('ses_a', 4, 'fix the parser')
  d.assistant('ses_a', 5, 'Fixed it.\nSecond line')
  d.session('ses_new', { title: null, directory: '/w/lib' })
  d.user('ses_new', 2, 'what is in this repo?')
  d.session('ses_child', { parent: 'ses_a' })
  d.session('ses_archived', { archived: Date.now() })
  d.session('ses_ancient', { updated: Date.now() - 30 * 86400_000 })
  const { n, host } = await setup(t, { db: d })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  assert.equal(n.hello.runtime, 'opencode-4242')
  assert.equal(n.hello.agent, 'opencode')
  await until('listed', () => n.last('runtime.sessions')?.sessions.length === 2)
  const rows = Object.fromEntries(n.last('runtime.sessions').sessions.map((s) => [s.session_id, s]))
  assert.deepEqual(Object.keys(rows).sort(), ['ses_a', 'ses_new'])
  const a = rows.ses_a
  assert.deepEqual([a.title, a.cwd, a.status, a.last_text, a.model, a.rev, a.space_kind], ['Fix the parser', '/w/app', 'done', 'Fixed it. Second line', 'model-x', 5, 'opencode'])
  assert.equal(a.context_tokens, 4000)
  await until('window', async () => (await n.peer.call('session.items', { session_id: 'ses_a', turns: 1 })) && n.last('runtime.sessions').sessions.find((s) => s.session_id === 'ses_a').context_window === 200000)
  assert.equal(rows.ses_new.title, 'what is in this repo?', 'no title yet: the first message')
  assert.equal(rows.ses_new.status, 'idle')
  assert.equal(rows.ses_new.space_name, 'lib')
})

test('pages the conversation by turns, back and forward; a message still being written is not part of it', async (t) => {
  const d = fakeDb()
  d.session('ses_p')
  d.user('ses_p', 4, 'one')
  d.assistant('ses_p', 5, 'a1')
  d.user('ses_p', 9, 'two')
  d.message('ses_p', 10, 'assistant', { content: [{ type: 'text', text: 'a2' }, { type: 'tool', id: 'call_1', name: 'shell', state: { status: 'completed', input: { command: 'ls' }, content: [{ type: 'text', text: 'x' }] } }], finish: 'tool-calls', time: { completed: T0 + 99 } })
  d.message('ses_p', 14, 'assistant', { content: [{ type: 'text', text: 'writing…' }], time: {} }) // no completed: still running
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready
  const p = await n.peer.call('session.items', { session_id: 'ses_p', turns: 1 })
  assert.deepEqual(p.items.map((i) => i.id), ['9#0', '10#0', '10#2', '10#3'])
  assert.equal(p.to_seq, 10, 'the unfinished message is not there yet')
  assert.equal(p.has_more, true)
  assert.match(p.history, /^opencode:ses_p/)
  const before = await n.peer.call('session.items', { session_id: 'ses_p', before_seq: 9, limit: 10 })
  assert.deepEqual(before.items.map((i) => i.text), ['one', 'a1'])
  assert.equal(before.has_more, false)
  const after = await n.peer.call('session.items', { session_id: 'ses_p', after_seq: 5 })
  assert.deepEqual(after.items.map((i) => i.id), ['9#0', '10#0', '10#2', '10#3'])
  assert.equal(after.from_seq, 5)
  const full = await n.peer.call('session.item', { session_id: 'ses_p', item_id: '10#3' })
  assert.equal(full.text, 'x')
  assert.equal(await n.peer.call('session.cursor', { session_id: 'ses_p' }).catch(() => 10), 10)
})

test('catching up past more messages than one read takes: the page ends where the read did, and says more comes', async (t) => {
  const d = fakeDb()
  d.session('ses_long')
  // 300 plain turns: a user message, a reply and the idle marker (no item) each: 900 rows, 600 items.
  let seq = 0
  for (let i = 0; i < 300; i++) {
    d.user('ses_long', ++seq, `q${i}`)
    d.assistant('ses_long', ++seq, `a${i}`)
    d.message('ses_long', ++seq, 'idle', { outcome: 'completed' })
  }
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready
  const seen = []
  let after = 0
  for (let i = 0; i < 10; i++) {
    const p = await n.peer.call('session.items', { session_id: 'ses_long', after_seq: after })
    seen.push(...p.items.map((it) => it.seq))
    assert.ok(p.items.every((it) => it.seq <= p.to_seq), 'nothing past to_seq')
    after = p.to_seq
    if (!p.more_after) break
  }
  assert.equal(after, 900)
  assert.equal(seen.length, 600, 'every message once: none skipped between pages')
  assert.equal(new Set(seen).size, 600)
})

test('sends into the session (once per client_msg_id), interrupts and renames through the plugin API of the session\'s location', async (t) => {
  const d = fakeDb()
  d.session('ses_c', { directory: '/w/lib' })
  const { n, host } = await setup(t, { db: d })
  const app = fakeCtx('/w/app')
  const lib = fakeCtx('/w/lib')
  host.register(app)
  host.register(lib)
  await n.ready
  await n.peer.call('session.send', { session_id: 'ses_c', text: 'go on', client_msg_id: 'm1' })
  await n.peer.call('session.send', { session_id: 'ses_c', text: 'go on', client_msg_id: 'm1' }) // a retry
  assert.deepEqual(lib.calls, [['session.prompt', { sessionID: 'ses_c', text: 'go on' }]])
  assert.equal(app.calls.length, 0, 'the location the session lives in')
  await n.peer.call('session.send', { session_id: 'ses_c', text: 'look', images: [{ mime: 'image/png', data: 'AAAA' }], client_msg_id: 'm2' })
  assert.deepEqual(lib.calls.at(-1)[1].files, [{ uri: 'data:image/png;base64,AAAA', name: 'image-1.png' }])
  await n.peer.call('session.interrupt', { session_id: 'ses_c' })
  await n.peer.call('session.rename', { session_id: 'ses_c', title: 'Build fix' })
  assert.deepEqual(lib.calls.slice(-2), [['session.interrupt', { sessionID: 'ses_c' }], ['session.update', { sessionID: 'ses_c', title: 'Build fix' }]])
  await assert.rejects(n.peer.call('session.send', { session_id: 'ses_c', text: '   ', client_msg_id: 'm3' }), /消息是空的/)
})

test('a watched session gets each finished message once, as a continuous batch; its live text as deltas; the state follows the turn', async (t) => {
  const d = fakeDb()
  d.session('ses_w')
  d.user('ses_w', 4, 'one')
  d.assistant('ses_w', 5, 'a1')
  const { n, host } = await setup(t, { db: d })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await n.peer.call('session.watch', { session_id: 'ses_w', since_seq: 0 })
  ctx.emit('session.inbox.enqueued', { sessionID: 'ses_w', inboxID: 'msg_x', item: { type: 'user', payload: { text: 'two' } } })
  await until('working', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_w')?.status === 'working')
  ctx.emit('session.text.delta', { sessionID: 'ses_w', assistantMessageID: 'msg_a', ordinal: 0, delta: 'Hel' })
  ctx.emit('session.text.delta', { sessionID: 'ses_w', assistantMessageID: 'msg_a', ordinal: 0, delta: 'lo' })
  await until('live text', () => n.all('session.delta').some((x) => x.text === 'Hello'))
  d.user('ses_w', 9, 'two')
  d.assistant('ses_w', 10, 'Hello there')
  ctx.emit('session.step.ended', { sessionID: 'ses_w', assistantMessageID: 'msg_a', finish: 'stop' })
  ctx.emit('session.execution.succeeded', { sessionID: 'ses_w' })
  await until('pushed', () => n.all('session.items').flatMap((b) => b.items).some((i) => i.text === 'Hello there'))
  const batches = n.all('session.items')
  assert.deepEqual(batches.slice(1).filter((b, i) => b.from_seq !== batches[i].to_seq), [])
  assert.equal(batches.at(-1).to_seq, 10)
  assert.equal(n.all('session.items').flatMap((b) => b.items).filter((i) => i.text === 'Hello there').length, 1)
  await until('done again', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_w')?.status === 'done')
  assert.equal(n.last('runtime.sessions').sessions.find((s) => s.session_id === 'ses_w').rev, 10)
  assert.ok(n.all('session.delta').at(-1).text === '', 'the live text ends with the turn')
})

test('a shell that runs past STALE_MS still publishes its same-seq result', async (t) => {
  let now = Date.now()
  const d = fakeDb()
  d.session('ses_long_shell')
  const { n, host } = await setup(t, { db: d, now: () => now })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await n.peer.call('session.watch', { session_id: 'ses_long_shell', since_seq: 0 })
  await host.commit('ses_long_shell')
  await host.onEvent(ctx, { type: 'session.execution.started', data: { sessionID: 'ses_long_shell' } })

  d.message('ses_long_shell', 5, 'shell', { command: 'sleep 61; echo done', status: 'running', time: { created: now } }, now)
  await host.commit('ses_long_shell')
  assert.equal(host.sessions.get('ses_long_shell').lastSeq, 0, 'running shell is not final at start')

  now += 61_000 // the running turn's normal poll calls the same commit path
  await host.commit('ses_long_shell')

  d.message('ses_long_shell', 5, 'shell', { command: 'sleep 61; echo done', status: 'completed', exit: 0,
    output: { output: 'done' }, time: { created: now - 61_000, completed: now } }, now)
  await host.commit('ses_long_shell')
  await until('completed shell result pushed', () => n.all('session.items').flatMap((b) => b.items).some((i) => i.kind === 'tool_result' && i.text === 'done'), 500)
})

test('what a watched session writes is pushed cut to the page limits, as its pages are', async (t) => {
  const d = fakeDb()
  d.session('ses_big')
  d.user('ses_big', 4, 'one')
  d.assistant('ses_big', 5, 'a1')
  const { n, host } = await setup(t, { db: d })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await n.peer.call('session.watch', { session_id: 'ses_big', since_seq: 0 })
  ctx.emit('session.execution.started', { sessionID: 'ses_big' }) // the conversation so far is the baseline
  await until('working', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_big')?.status === 'working')
  const output = 'line of output\n'.repeat(2000) // 30 KB
  d.message('ses_big', 8, 'assistant', { content: [{ type: 'tool', id: 'call_1', name: 'shell', state: { status: 'completed', input: { command: 'cat big.log' }, content: [{ type: 'text', text: output }] } }],
    finish: 'tool-calls', time: { created: T0 + 8, completed: T0 + 9 } })
  ctx.emit('session.tool.success', { sessionID: 'ses_big' })
  const pushed = await until('pushed', () => n.all('session.items').flatMap((b) => b.items).find((i) => i.kind === 'tool_result'))
  const paged = (await n.peer.call('session.items', { session_id: 'ses_big', limit: 2 })).items.find((i) => i.kind === 'tool_result')
  assert.equal(pushed.full_size, Buffer.byteLength(output), 'cut, with the size of the whole')
  assert.ok(Buffer.byteLength(pushed.text) < 5 * 1024, 'at most the 4 KB a page carries')
  assert.deepEqual(pushed, paged, 'the push and the page say the same')
})

test('every location delivers the same events: each is handled once; the stream moves to another location when its reader goes away', async (t) => {
  const d = fakeDb()
  d.session('ses_e')
  d.user('ses_e', 4, 'one')
  const { n, host } = await setup(t, { db: d })
  const a = fakeCtx('/w/app')
  const b = fakeCtx('/w/lib')
  const offA = host.register(a)
  host.register(b)
  await n.ready
  a.emit('session.execution.started', { sessionID: 'ses_e' }, 'evt_1')
  b.emit('session.execution.started', { sessionID: 'ses_e' }, 'evt_1')
  await until('working', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_e')?.status === 'working')
  assert.equal(host.sessions.get('ses_e').running, true)
  offA() // the primary goes: b reads the stream now
  b.emit('session.execution.succeeded', { sessionID: 'ses_e' }, 'evt_2')
  await until('idle again', () => host.sessions.get('ses_e').running === false)
})

/** A session the test runs a turn on, and its row as last listed. */
async function missedEnd(t, dbOpts) {
  const d = fakeDb(dbOpts)
  d.session('ses_m')
  d.user('ses_m', 4, 'one')
  const { n, host } = await setup(t, { db: d })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  const row = () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_m')
  const set = (cols) => d.db.prepare(`update session_v2 set ${Object.keys(cols).map((k) => `${k} = ?`).join(', ')} where id = ?`).run(...Object.values(cols), 'ses_m')
  let renames = 0
  const relist = async () => {
    const title = `r${++renames}`
    ctx.emit('session.renamed', { sessionID: 'ses_m', title })
    await until(`listed as ${title}`, () => row()?.title === title)
  }
  ctx.emit('session.execution.started', { sessionID: 'ses_m' })
  await until('working', () => row()?.status === 'working')
  return { d, n, host, ctx, row, set, relist }
}

test('a turn that ended while the event stream was down: time_idle after time_busy ends it, even after a rename', async (t) => {
  const { d, host, ctx, row, set, relist } = await missedEnd(t)
  const start = Date.now()
  // The previous turn's stamps: idle before this turn's start, still working.
  set({ time_busy: T0, time_idle: T0 + 1, time_updated: T0 + 1 })
  await relist()
  assert.equal(row().status, 'working')
  // This turn started in opencode and is not over: busy after idle.
  set({ time_busy: start, time_idle: T0 + 1 })
  await relist()
  assert.equal(row().status, 'working')
  // It ends while the stream is down; then a title generated alongside it renames the session (time_updated moves on).
  d.assistant('ses_m', 5, 'done it')
  set({ time_idle: start + 5, time_updated: start + 800 })
  await relist()
  assert.equal(row().status, 'done', 'a rename after the end does not keep it working')
  assert.equal(host.sessions.get('ses_m').running, false)
  // The end event arrives late after all: nothing changes, nothing breaks.
  ctx.emit('session.execution.succeeded', { sessionID: 'ses_m' })
  await relist()
  assert.equal(row().status, 'done')
  // The next turn starts: working again, not ended by the old idle stamp.
  ctx.emit('session.execution.started', { sessionID: 'ses_m' })
  await until('working again', () => row()?.status === 'working')
  await relist()
  assert.equal(row().status, 'working')
})

test('an opencode without time_busy: the list still reads, and the events alone say when a turn ends', async (t) => {
  const { d, ctx, row, set, relist } = await missedEnd(t, { busy: false })
  d.assistant('ses_m', 5, 'done it')
  set({ time_idle: Date.now() + 5, time_updated: Date.now() + 5 })
  await relist()
  assert.equal(row().status, 'working', 'no start stamp to compare with: left to the events')
  ctx.emit('session.execution.succeeded', { sessionID: 'ses_m' })
  await until('done', () => row()?.status === 'done')
})

test('a permission: a card on the phone while the TUI dialog waits; allow, always, deny with a reason; an answer in the TUI withdraws it', async (t) => {
  const d = fakeDb()
  d.session('ses_perm')
  d.user('ses_perm', 4, 'build it')
  const { n, host } = await setup(t, { db: d, answerFirst: 'phone' })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.some((s) => s.session_id === 'ses_perm'))
  const ask = (id, extra = {}) => ctx.emit('permission.asked', { id, sessionID: 'ses_perm', action: 'shell', resources: ['rm -rf build'], save: ['rm *'], source: { type: 'tool', id: `call_${id}` }, ...extra })

  n.resolve = () => ({ allow: true })
  ask('per_1')
  await until('replied once', () => ctx.calls.some((c) => c[0] === 'permission.reply'))
  assert.deepEqual(n.asked[0].tool_name, 'Bash')
  assert.equal(n.asked[0].summary, 'rm -rf build')
  assert.equal(n.asked[0].tool_input.command, 'rm -rf build')
  assert.equal(n.asked[0].always_label, '总是允许 rm *')
  assert.deepEqual(ctx.calls.at(-1), ['permission.reply', { sessionID: 'ses_perm', requestID: 'per_1', decision: 'once' }])

  n.resolve = () => ({ allow: true, always: true })
  ask('per_2')
  await until('always', () => ctx.calls.filter((c) => c[0] === 'permission.reply').length === 2)
  assert.equal(ctx.calls.at(-1)[1].decision, 'always')

  n.resolve = () => ({ allow: false, message: 'not that one' })
  ask('per_3')
  await until('rejected', () => ctx.calls.filter((c) => c[0] === 'permission.reply').length === 3)
  assert.deepEqual(ctx.calls.at(-1)[1], { sessionID: 'ses_perm', requestID: 'per_3', decision: 'reject', message: 'not that one' })

  n.resolve = () => ({ dismiss: true }) // handed back to the TUI
  ask('per_4')
  await until('asked', () => n.asked.length === 4)
  await new Promise((r) => setTimeout(r, 100))
  assert.equal(ctx.calls.filter((c) => c[0] === 'permission.reply').length, 3, 'nothing answered for a dismissed card')
})

test('a permission asked by a subagent is the root session\'s; while it waits the row says "needs you"; the TUI answering clears it', async (t) => {
  const d = fakeDb()
  d.session('ses_root')
  d.user('ses_root', 4, 'go')
  d.session('ses_sub', { parent: 'ses_root' })
  const { n, host } = await setup(t, { db: d, answerFirst: 'local' })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.some((s) => s.session_id === 'ses_root'))
  ctx.emit('permission.asked', { id: 'per_9', sessionID: 'ses_sub', action: 'edit', resources: ['/w/a.js'] })
  await until('blocked', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_root')?.status === 'blocked')
  assert.equal(n.asked.length, 0, 'the TUI is first: no card')
  assert.equal(n.last('runtime.sessions').sessions.some((s) => s.session_id === 'ses_sub'), false)
  ctx.emit('permission.replied', { sessionID: 'ses_sub', requestID: 'per_9', reply: 'once' })
  await until('clear', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_root')?.status !== 'blocked')
})

test('a session opencode does not have is not_found, so the node does not take another runtime\'s session for this one', async (t) => {
  const d = fakeDb()
  d.session('ses_a')
  d.user('ses_a', 4, 'hello')
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready
  // The node asks every connected runtime session.items {limit: 1} about a session nobody listed, and gives it to
  // the first that answers (runtimeFor, handOver): an empty page here would take a DSH or pi session for opencode.
  await assert.rejects(n.peer.call('session.items', { session_id: 'session-0b1c', limit: 1 }), (err) => err.code === 'not_found')
  const p = await n.peer.call('session.items', { session_id: 'ses_a', limit: 1 })
  assert.deepEqual(p.items.map((i) => i.id), ['4#0'])
})

test('without the database the runtime still answers: an empty list, no pages, sends and the rest through the plugin', async (t) => {
  const n = await startFakeNode(t)
  const host = createOpencodeHost({ openDb: async () => { throw new Error('no such file') }, pid: 4243 }, { socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => host.stop())
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await until('empty list', () => n.last('runtime.sessions')?.sessions.length === 0)
  const p = await n.peer.call('session.items', { session_id: 'ses_none', turns: 1 })
  assert.deepEqual(p.items, [])
})

test('a deleted session: its card leaves the phone, its polling stops, and it is not brought back', async (t) => {
  const d = fakeDb()
  d.session('ses_del')
  d.user('ses_del', 4, 'go')
  const { n, host } = await setup(t, { db: d, answerFirst: 'phone' })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.some((s) => s.session_id === 'ses_del'))
  await n.peer.call('session.watch', { session_id: 'ses_del', since_seq: 0 })
  n.resolve = () => new Promise(() => {}) // the phone does not answer
  ctx.emit('session.execution.started', { sessionID: 'ses_del' })
  ctx.emit('permission.asked', { id: 'per_d', sessionID: 'ses_del', action: 'shell', resources: ['ls'] })
  await until('the card', () => n.asked.length === 1)
  assert.equal(host.runtime.busy(), true)

  ctx.emit('session.deleted', { sessionID: 'ses_del' })
  await until('the card withdrawn', () => n.all('interaction.cancel').some((c) => c.id === n.asked[0].id))
  assert.equal(host.runtime.busy(), false)
  await new Promise((r) => setTimeout(r, 2500)) // past a poll
  assert.equal(host.sessions.has('ses_del'), false, 'not brought back by its polling')
})
