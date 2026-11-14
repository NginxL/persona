// The opencode runtime (docs/integrations/opencode.md): the sessions of one opencode server process in Persona through the runtime
// SDK (docs/integrations/runtimes.md). opencode loads a plugin once per location (a project directory) in the one server process
// that every TUI attaches to, so there are as many calls of `register` as directories in use, but one runtime
// connection per process: index.js hands each location's plugin context to the host made here, once per process.
// The server-wide event stream is read through one of them (the primary; another takes over when it goes away).
//
// Where things come from:
//  - the list and the conversation: opencode.db, read-only (db.js). The plugin API has no list of sessions and gives a
//    session's context only (what a compaction left), while the database holds all of it with a stable seq per message.
//  - what happens now (a turn starting and ending, text arriving, a permission asked): the plugin's event stream.
//  - control (send, interrupt, rename, permission replies): the plugin's own session / permission API.
// The state a herdr pane shows (working, idle) is herdr's; this runtime adds the title, the last words, the model, the
// context use, the conversation, and "needs you" with a card while a permission waits. Everything opencode-specific
// comes in through `deps`, so the tests drive it with fakes.

import { createHash } from 'node:crypto'
import { basename } from 'node:path'
import { createRuntime, interactionId, light, page, RpcError, snippet } from '../../runtime-sdk/index.js'
import { contextTokens, fromRow, imageOf, lastText, liveText, messageItems, permissionCard } from './map.js'

export const RUNTIME_VERSION = '0.1.0'
const ROOTS = 200 // sessions looked at for the list (the newest roots)
const TAIL = 500 // messages read for the newest page of a conversation
const AFTER = 600 // messages read for one catch-up page
const LIST_CACHE = 3000 // ms: the list of sessions is read from the database at most this often
const POLL_EVERY = 2000 // ms: while a turn runs, look for messages the events did not announce
const STALE_MS = 60_000 // an unfinished message of a session that is running is final after this (events missed)
const SETTLE_MS = 1500 // ...and of one that is not running, after this (an interrupted step never says it completed)
const MODELS_EVERY = 10 * 60_000 // ms: the models' context windows are asked again

/**
 * @typedef {object} OpencodeDeps
 * @property {() => Promise<{ all(sql: string, ...params: any[]): any[] }>} openDb - opencode.db, read-only.
 * @property {(msg: string) => void} [log]
 * @property {(path: string) => import('node:net').Socket} [connect] - tests.
 * @property {number} [pid]
 * @property {() => number} [now]
 */

/**
 * @param {OpencodeDeps} deps
 * @param {{ socketPath?: string, answerFirst?: 'auto'|'phone'|'local', listDays?: number, maxSessions?: number,
 *   retryMs?: number, pushDelayMs?: number, runtime?: string, instance?: string }} [options]
 */
export function createOpencodeHost(deps, options = {}) {
  const log = deps.log ?? (() => {})
  const now = deps.now ?? Date.now
  const listDays = options.listDays ?? 7
  const maxSessions = options.maxSessions ?? 80
  const sessions = new Map() // session id -> { running, blocked: Map(request id -> AbortController), lastSeq, texts, textMessage, chain, poll }
  const locations = new Set() // { ctx, directory, abort? } of every registered location, in registration order
  const roots = new Map() // session id -> { root, directory } (a child session answers for its root)
  const windows = new Map() // "provider\0model" and "model" -> context window
  const seen = new Set() // ids of events handled (every location's subscription delivers the same ones)
  const derived = new Map() // session id -> { rev, last, model, tokens, hasAssistant } (per max seq)
  let primary // the location whose context reads the event stream
  let started = false
  let stopped = false
  let db
  let dbFailedAt = 0
  let listCache = { at: 0, rows: [] }
  let modelsAt = 0

  const rt = createRuntime({
    listSessions,
    items,
    cursor: async (id) => {
      const v = await view(id)
      return { to_seq: v.toSeq, history: v.history }
    },
    image,
    item,
    send,
    interrupt,
    rename,
    hasLocalClient: () => true, // the opencode TUI (it cannot be counted from here)
  }, {
    runtime: options.runtime ?? `opencode-${deps.pid ?? process.pid}`,
    name: 'opencode',
    agent: 'opencode',
    version: RUNTIME_VERSION,
    socketPath: options.socketPath,
    answerFirst: options.answerFirst ?? 'auto',
    log,
    connect: deps.connect,
    instance: options.instance,
    retryMs: options.retryMs,
    pushDelayMs: options.pushDelayMs,
  })

  // ---- the database ----

  /** The database, opened on first use; null for a minute after a failure to open it. */
  async function database() {
    if (db) return db
    if (dbFailedAt && now() - dbFailedAt < 60_000) return null
    try {
      db = await deps.openDb()
      dbFailedAt = 0
    } catch (err) {
      dbFailedAt = now()
      log(`persona: opencode database: ${err.message}`)
    }
    return db ?? null
  }

  /**
   * "time_busy," when session_v2 has it (opencode stamps it when a turn starts; older versions have no such column),
   * else "": selecting a column that is not there would fail the query, and rows() gives [] for that, an empty list.
   * Asked once per database opened.
   */
  let busyFor
  let busyCol = ''
  async function busyColumn() {
    const d = await database()
    if (!d || busyFor === d) return busyCol
    const cols = await rows('pragma table_info(session_v2)')
    if (!cols.length) return '' // not read: ask again next time
    busyFor = d
    busyCol = cols.some((c) => c.name === 'time_busy') ? 'time_busy,' : ''
    return busyCol
  }

  /** Rows of a query; [] when the database is not there or the query failed (logged). */
  async function rows(sql, ...params) {
    const d = await database()
    if (!d) return []
    try {
      return d.all(sql, ...params)
    } catch (err) {
      log(`persona: opencode query: ${err.message}`)
      if (/not an? (database|open)|closed/i.test(String(err.message))) db = undefined
      return []
    }
  }

  /** The root of a session: a subagent's session answers for the session that started it. */
  async function rootOf(id) {
    const c = roots.get(id)
    if (c) return c
    let cur = id
    for (let i = 0; i < 6; i++) {
      const [r] = await rows('select parent_id from session_v2 where id = ?', cur)
      if (!r) return { root: id } // not in the database (yet): its own root, and not remembered
      if (!r.parent_id) {
        const out = { root: cur }
        roots.set(id, out)
        return out
      }
      cur = r.parent_id
    }
    return { root: id }
  }

  // ---- the sessions ----

  const st = (id) => {
    let s = sessions.get(id)
    if (!s) {
      s = { running: false, runningSince: 0, blocked: new Map(), lastSeq: undefined, texts: new Map(), textMessage: '', chain: Promise.resolve(), poll: undefined, title: undefined }
      sessions.set(id, s)
    }
    return s
  }

  const isPinned = (id) => {
    const s = sessions.get(id)
    return !!s && (s.running || s.blocked.size > 0)
  }

  async function derive(id, rev) {
    const c = derived.get(id)
    if (c && c.rev === rev) return c
    const recent = await rows("select seq, type, data from session_message where session_id = ? and type in ('assistant', 'user') order by seq desc limit 8", id)
    const msgs = recent.map((r) => ({ ...fromRow(r), seq: r.seq })).reverse()
    const lastAssistant = [...msgs].reverse().find((m) => m.type === 'assistant')
    const out = {
      rev,
      last: lastText(msgs),
      model: lastAssistant?.model?.id ?? '',
      provider: lastAssistant?.model?.providerID ?? '',
      tokens: contextTokens(lastAssistant),
      hasAssistant: !!lastAssistant,
    }
    derived.set(id, out)
    if (derived.size > 400) derived.delete(derived.keys().next().value)
    return out
  }

  /** The newest root sessions in the database, kept for LIST_CACHE ms. */
  async function recentRoots() {
    if (now() - listCache.at < LIST_CACHE) return listCache.rows
    const list = await rows(
      `select id, title, directory, time_updated, time_idle, ${await busyColumn()} model from session_v2 where parent_id is null and time_archived is null order by time_updated desc limit ?`, ROOTS)
    listCache = { at: now(), rows: list }
    return list
  }

  async function sessionRow(r) {
    const [m] = await rows('select max(seq) as seq from session_message where session_id = ?', r.id)
    const rev = m?.seq ?? 0
    const d = await derive(r.id, rev)
    const s = sessions.get(r.id)
    let title = s?.title || r.title || ''
    if (!title) {
      const [u] = await rows("select data from session_message where session_id = ? and type = 'user' order by seq limit 1", r.id)
      if (u) title = snippet(fromRow({ data: u.data }).text ?? '')
    }
    const directory = r.directory ?? ''
    // A turn that ended while the event stream was down never sent its end. opencode stamps time_busy when a turn
    // starts and time_idle when it ends: an idle stamp newer than both its own start and the start we saw is that
    // end. (time_updated is no guide: a rename, a model or agent change moves it with no turn.) A turn interrupted by
    // opencode shutting down writes no time_idle, so it is not caught here; the next start's events are.
    // Note: this calls ended(), which has side effects (stream end, cards withdrawn, a commit).
    if (s?.running && typeof r.time_idle === 'number' && typeof r.time_busy === 'number' &&
      r.time_idle > r.time_busy && r.time_idle > s.runningSince) {
      log(`persona: opencode session ${r.id} went idle without an event; marking its turn over`)
      ended(r.id)
    }
    const status = s?.blocked.size ? 'blocked' : s?.running ? 'working' : d.hasAssistant ? 'done' : 'idle'
    const row = {
      session_id: r.id,
      title: title || basename(directory),
      cwd: directory,
      status,
      updated_at: new Date(r.time_updated ?? now()).toISOString(),
      rev,
      space_id: directory,
      space_name: basename(directory),
      space_kind: 'opencode',
    }
    if (d.last) row.last_text = d.last
    const model = d.model || modelOf(r.model)
    if (model) row.model = model
    if (d.tokens > 0) {
      row.context_tokens = d.tokens
      const w = windows.get(`${d.provider}\0${d.model}`) ?? windows.get(d.model)
      if (w) row.context_window = w
    }
    return row
  }

  function modelOf(json) {
    try {
      return json ? JSON.parse(json).id ?? '' : ''
    } catch {
      return ''
    }
  }

  async function listSessions() {
    const recent = await recentRoots()
    const ids = new Set(recent.map((r) => r.id))
    // Running and waiting ones are listed whatever their age; the phone's, and the recent ones, by the SDK's rule.
    const extra = []
    for (const [id] of sessions) {
      if (!ids.has(id) && isPinned(id)) {
        const [r] = await rows(`select id, title, directory, time_updated, time_idle, ${await busyColumn()} model from session_v2 where id = ? and parent_id is null`, id)
        if (r) extra.push(r)
      }
    }
    const picked = rt.recent([...recent, ...extra], { days: listDays, max: maxSessions, pinned: (r) => isPinned(r.id), id: (r) => r.id, updatedAt: (r) => r.time_updated })
    const out = []
    for (const r of picked) out.push(await sessionRow(r))
    return out
  }

  // ---- the conversation ----

  /** A message is final once opencode says it completed; one still running is not pushed (it is rewritten in place). */
  function settled(r, s) {
    if (r.type !== 'assistant' && r.type !== 'shell' && r.type !== 'compaction') return true
    let t
    try {
      t = JSON.parse(r.data)
    } catch {
      return true
    }
    if (r.type === 'compaction') return t.status !== 'pending'
    if (typeof t.time?.completed === 'number' || t.finish) return true
    if (r.type === 'shell' && t.status === 'running') return false
    if (r.type === 'shell' && t.status) return true
    return now() - (r.time_updated ?? 0) > (s?.running ? STALE_MS : SETTLE_MS)
  }

  /** The rows up to the first message still being written: the conversation as far as it is final. */
  function settledPrefix(list, id) {
    const s = sessions.get(id)
    const out = []
    for (const r of list) {
      if (!settled(r, s)) break
      out.push(r)
    }
    return out
  }

  async function view(id) {
    const [r] = await rows('select revert from session_v2 where id = ?', id)
    // Not opencode's (the database is open and has no such session): the node asks every runtime about a session
    // nobody listed and gives it to the first that answers, so an empty page would take another runtime's for ours.
    if (!r && db) throw new RpcError(`opencode 里没有这个会话（${id}）`, 'not_found')
    return { toSeq: await maxSettledBefore(id), history: historyOf(id, r?.revert) }
  }

  /** The largest seq below the first unfinished message of a session (all of them when none is). */
  async function maxSettledBefore(id) {
    const open = await rows("select seq, type, data, time_updated from session_message where session_id = ? and type in ('assistant', 'shell', 'compaction') order by seq desc limit 3", id)
    const s = sessions.get(id)
    let first
    for (const r of open.reverse()) if (!settled(r, s)) { first = r.seq; break }
    const [m] = first === undefined
      ? await rows('select max(seq) as seq from session_message where session_id = ?', id)
      : await rows('select max(seq) as seq from session_message where session_id = ? and seq < ?', id, first)
    return m?.seq ?? 0
  }

  /** The conversation's identity: another one when opencode reverted messages (they are hidden, not rewritten). */
  const historyOf = (id, revert) => `opencode:${id}${revert ? `:r${createHash('sha256').update(String(revert)).digest('hex').slice(0, 8)}` : ''}`

  async function items(params) {
    const id = params.session_id
    const v = await view(id)
    const upto = v.toSeq
    const q = (extra, ...p) => rows(`select id, seq, type, data, time_updated from session_message where session_id = ? and seq <= ? ${extra}`, id, upto, ...p)
    let list
    let windowFull = false
    let afterFull = false
    if (typeof params.through_seq === 'number' && params.through_seq > 0) {
      list = await q('and seq >= ? and seq <= ? order by seq', params.from_seq ?? 1, params.through_seq)
    } else if (params.after_seq !== undefined && params.after_seq !== null) {
      list = await q('and seq > ? order by seq limit ?', params.after_seq, AFTER)
      afterFull = list.length === AFTER
    } else {
      const before = params.before_seq > 0 ? params.before_seq : Number.MAX_SAFE_INTEGER
      list = (await q('and seq < ? order by seq desc limit ?', before, TAIL)).reverse()
      windowFull = list.length === TAIL
    }
    const all = list.flatMap((r) => messageItems(fromRow(r), r.seq))
    const pg = page(all, params, { history: v.history, toSeq: v.toSeq })
    if (windowFull && pg.has_more === false) pg.has_more = true
    if (afterFull && !pg.more_after) {
      // The read stopped short of the end (messages without items, like the idle markers, fill it): the page covers
      // what was read, not the whole session, or the phone takes the rest as caught up and never fetches it.
      pg.to_seq = list.at(-1).seq
      pg.more_after = true
    }
    return pg
  }

  function splitItemID(itemID) {
    const [seqText, idxText] = String(itemID).split('#')
    const seq = Number(seqText)
    const idx = Number(idxText)
    if (!Number.isSafeInteger(seq) || !Number.isSafeInteger(idx) || seq < 1) throw new RpcError(`bad item id ${itemID}`, 'bad_request')
    return { seq, idx }
  }

  async function messageAt(id, seq) {
    const [r] = await rows('select id, seq, type, data from session_message where session_id = ? and seq = ?', id, seq)
    return r ? fromRow(r) : undefined
  }

  async function item(params) {
    const { seq } = splitItemID(params.item_id)
    const msg = await messageAt(params.session_id, seq)
    const it = msg && messageItems(msg, seq).find((x) => x.id === params.item_id)
    if (!it) throw new RpcError(`no item ${params.item_id}`, 'not_found')
    return it
  }

  async function image(params) {
    const { seq, idx } = splitItemID(params.item_id)
    const b = imageOf(await messageAt(params.session_id, seq), idx)
    if (!b) throw new RpcError(`no image ${params.item_id}`, 'not_found')
    return b
  }

  /** Push what became final since the last push (the items only when the phone watches the session). */
  function commit(id) {
    const s = st(id)
    s.chain = s.chain.then(async () => {
      if (stopped) return
      if (s.lastSeq === undefined) {
        s.lastSeq = await maxSettledBefore(id) // first sight: the conversation so far is what the phone fetches
        return
      }
      const list = settledPrefix(await rows('select id, seq, type, data, time_updated from session_message where session_id = ? and seq > ? order by seq limit ?', id, s.lastSeq, AFTER), id)
      if (list.length === 0) return
      const toSeq = list.at(-1).seq
      const [r] = await rows('select revert from session_v2 where id = ?', id)
      if (sessions.get(id) !== s) return // deleted meanwhile (forget): nothing more of it goes out
      const fresh = rt.watching(id) ? list.flatMap((m) => messageItems(fromRow(m), m.seq)).map(light) : [] // cut as pages are
      rt.append(id, { items: fresh, fromSeq: s.lastSeq, toSeq, history: historyOf(id, r?.revert) })
      s.lastSeq = toSeq
      s.updatedAt = now()
      listCache.at = 0
    }).catch((err) => log(`persona: opencode commit ${id}: ${err.message}`))
    return s.chain
  }

  let commitTimers = new Map()
  /** Commit soon (a burst of events is one commit). */
  function commitSoon(id, ms = 120, key = id) {
    if (commitTimers.has(key)) return
    const t = setTimeout(() => {
      commitTimers.delete(key)
      void commit(id)
    }, ms)
    t.unref?.()
    commitTimers.set(key, t)
  }

  // ---- control ----

  function ctxFor(directory) {
    for (const l of locations) if (l.directory === directory) return l.ctx
    return primary?.ctx ?? [...locations][0]?.ctx
  }

  async function control(id, fn) {
    const [r] = await rows('select directory from session_v2 where id = ?', id)
    const ctx = ctxFor(r?.directory ?? '')
    if (!ctx) throw new RpcError('opencode 没有在运行', 'not_running')
    try {
      return await fn(ctx)
    } catch (err) {
      throw new RpcError(`opencode：${err?.message ?? err}`, /not ?found/i.test(String(err?.message)) ? 'not_found' : 'opencode')
    }
  }

  function content(params) {
    const files = (params.images ?? []).filter((i) => i?.data).map((i, n) => {
      const mime = i.mime || 'image/jpeg'
      return { uri: `data:${mime};base64,${i.data}`, name: `image-${n + 1}.${mime.split('/')[1] || 'jpg'}` }
    })
    const text = typeof params.text === 'string' ? params.text : ''
    if (!text.trim() && files.length === 0) throw new RpcError('消息是空的', 'bad_request')
    return { text, files }
  }

  async function send(params) {
    const id = params.session_id
    const { text, files } = content(params) // once per client_msg_id: the SDK's
    // Into the running turn while it works (opencode's steer), a new turn when idle: its default.
    await control(id, (ctx) => ctx.session.prompt({ sessionID: id, text, ...(files.length ? { files } : {}) }))
  }

  async function interrupt(params) {
    await control(params.session_id, (ctx) => ctx.session.interrupt({ sessionID: params.session_id }))
  }

  async function rename(params) {
    const title = typeof params.title === 'string' ? params.title.trim() : ''
    if (!title) throw new RpcError('名字是空的', 'bad_request')
    await control(params.session_id, (ctx) => ctx.session.update({ sessionID: params.session_id, title }))
  }

  // ---- what happens in the server ----

  async function refreshModels(ctx) {
    if (now() - modelsAt < MODELS_EVERY) return
    modelsAt = now()
    try {
      for (const m of await ctx.model.list()) {
        const w = m?.limit?.context
        if (w > 0) {
          windows.set(`${m.providerID}\0${m.id}`, w)
          windows.set(m.id, w)
        }
      }
    } catch (err) {
      log(`persona: opencode models: ${err.message}`)
      modelsAt = 0
    }
  }

  /** A turn is over: the row is done, nothing waits for it, and what it wrote is pushed. */
  function ended(id) {
    rt.streamEnd(id)
    running(id, false)
    for (const [rid] of st(id).blocked) resolvePermission(id, rid) // nothing waits for a turn that is over
    commitSoon(id, 0)
    commitSoon(id, SETTLE_MS + 200, `${id}:late`) // a step that was cut off settles a moment later
  }

  function running(id, on) {
    const s = st(id)
    if (s.running === on) return
    s.running = on
    if (on) s.runningSince = now()
    clearInterval(s.poll)
    s.poll = undefined
    if (on) {
      s.poll = setInterval(() => rt.watching(id) && commitSoon(id, 0), POLL_EVERY)
      s.poll.unref?.()
    } else {
      s.texts.clear()
      s.textMessage = ''
    }
    listCache.at = 0
    rt.sessionsChanged(0)
  }

  /** The permission a session asked for: a card on the phone while the opencode dialog waits too; first answer wins. */
  async function onPermission(ctx, data) {
    const { root } = await rootOf(data.sessionID)
    const s = st(root)
    const ac = new AbortController()
    s.blocked.set(data.id, ac)
    listCache.at = 0
    rt.sessionsChanged(0)
    if (!rt.phoneFirst(root)) return // the dialog in the TUI is where it is answered
    const card = permissionCard(data)
    const save = Array.isArray(data.save) ? data.save.map(String) : []
    let res
    try {
      res = await rt.ask({
        id: interactionId('opencode', root, 'permission', data.id), kind: 'permission', session_id: root,
        tool_use_id: data.source?.id, ...card, ...(save.length ? { always_label: `总是允许 ${save.join('、')}` } : {}),
      }, { signal: ac.signal })
    } catch (err) {
      if (!ac.signal.aborted) log(`persona: opencode permission card: ${err.message}`)
      return
    }
    if (ac.signal.aborted || !s.blocked.has(data.id)) return // answered in the TUI meanwhile
    let decision
    let message
    if (res?.allow === true) decision = res.always === true && save.length ? 'always' : 'once'
    else if (res?.allow === false) {
      decision = 'reject'
      message = res.message?.trim() || undefined
    } else return // handed back to the TUI
    try {
      await ctx.permission.reply({ sessionID: data.sessionID, requestID: data.id, decision, ...(message ? { message } : {}) })
    } catch (err) {
      log(`persona: opencode permission reply: ${err.message}`)
    }
  }

  /**
   * A session deleted in opencode: nothing of it stays behind. Its polling and pending commits stop (they would make
   * it again), and its cards leave the phone (an aborted ask withdraws its card).
   */
  function forget(id) {
    const s = sessions.get(id)
    if (s) {
      clearInterval(s.poll)
      for (const ac of s.blocked.values()) ac.abort()
    }
    for (const key of [id, `${id}:late`]) {
      clearTimeout(commitTimers.get(key))
      commitTimers.delete(key)
    }
    sessions.delete(id)
    roots.delete(id)
    derived.delete(id)
    listCache.at = 0
    rt.streamEnd(id)
    rt.sessionsChanged()
  }

  function resolvePermission(root, requestID) {
    const s = sessions.get(root)
    const ac = s?.blocked.get(requestID)
    if (!ac) return
    ac.abort()
    s.blocked.delete(requestID)
    listCache.at = 0
    rt.sessionsChanged(0)
  }

  async function onEvent(ctx, ev) {
    if (ev?.id) {
      if (seen.has(ev.id)) return
      seen.add(ev.id)
      if (seen.size > 4000) seen.delete(seen.values().next().value)
    }
    const d = ev?.data ?? {}
    const id = d.sessionID
    if (typeof id !== 'string') return
    switch (ev.type) {
      case 'permission.asked':
        return void onPermission(ctx, d)
      case 'permission.replied':
      case 'form.replied':
      case 'form.cancelled': {
        const { root } = await rootOf(id)
        return resolvePermission(root, d.requestID ?? d.formID ?? d.id)
      }
      case 'form.created': { // a question: the dialog in the TUI answers it (the plugin API cannot); the row says "needs you"
        const { root } = await rootOf(id)
        const s = st(root)
        s.blocked.set(d.id ?? d.formID ?? `form-${now()}`, new AbortController())
        listCache.at = 0
        return rt.sessionsChanged(0)
      }
    }
    // Everything below is about a session the user works in; a subagent's own turns are not (its root's are).
    const { root } = await rootOf(id)
    if (root !== id) return
    if (ev.type === 'session.deleted') return forget(id)
    if (st(id).lastSeq === undefined) void commit(id) // the conversation as it is now is the baseline of what is pushed
    switch (ev.type) {
      case 'session.created':
      case 'session.renamed':
        if (ev.type === 'session.renamed' && typeof d.title === 'string') st(id).title = d.title
        listCache.at = 0
        return rt.sessionsChanged()
      case 'session.inbox.enqueued':
        if (d.item?.type === 'user') running(id, true)
        return
      case 'session.execution.started':
        return running(id, true)
      case 'session.inbox.delivered':
      case 'session.tool.success':
      case 'session.tool.failed':
        return commitSoon(id)
      case 'session.text.started':
        if (d.assistantMessageID !== st(id).textMessage) {
          st(id).texts.clear()
          st(id).textMessage = d.assistantMessageID
        }
        return
      case 'session.text.delta': {
        const s = st(id)
        if (d.assistantMessageID !== s.textMessage) {
          s.texts.clear()
          s.textMessage = d.assistantMessageID
        }
        s.texts.set(d.ordinal ?? 0, (s.texts.get(d.ordinal ?? 0) ?? '') + (d.delta ?? ''))
        if (!s.running) running(id, true)
        return rt.stream(id, liveText(s.texts))
      }
      case 'session.text.ended':
      case 'session.step.ended':
      case 'session.step.failed':
        rt.streamEnd(id)
        return commitSoon(id)
      case 'session.execution.succeeded':
      case 'session.execution.failed':
      case 'session.execution.interrupted':
        return ended(id)
    }
  }

  async function consume(entry) {
    let delay = 1000
    while (!entry.abort.signal.aborted && !stopped) {
      try {
        for await (const ev of entry.ctx.event.subscribe({ signal: entry.abort.signal })) {
          if (entry.abort.signal.aborted) break
          delay = 1000
          onEvent(entry.ctx, ev).catch((err) => log(`persona: opencode event ${ev?.type}: ${err.message}`))
        }
      } catch (err) {
        if (!entry.abort.signal.aborted) log(`persona: opencode events: ${err.message}; reconnecting`)
      }
      if (entry.abort.signal.aborted || stopped) break
      await new Promise((r) => {
        const t = setTimeout(r, delay)
        t.unref?.()
      })
      delay = Math.min(delay * 2, 30_000)
    }
  }

  function elect() {
    primary = [...locations].find((l) => !l.abort.signal.aborted)
    if (primary && !primary.reading) {
      primary.reading = true
      void refreshModels(primary.ctx)
      void consume(primary)
    }
  }

  function stopHost() {
    stopped = true
    started = false
    for (const l of locations) l.abort.abort()
    locations.clear()
    primary = undefined
    for (const t of commitTimers.values()) clearTimeout(t)
    commitTimers = new Map()
    for (const s of sessions.values()) {
      clearInterval(s.poll)
      for (const ac of s.blocked.values()) ac.abort()
    }
    rt.stop()
    try {
      db?.close()
    } catch {}
    db = undefined
  }

  return {
    /**
     * A location's plugin context (opencode calls the plugin once per project directory in the server process).
     * Returns what to call when the location goes away.
     */
    register(ctx) {
      const entry = { ctx, directory: ctx.location?.directory ?? '', abort: new AbortController(), reading: false }
      locations.add(entry)
      if (!started) {
        started = true
        stopped = false
        rt.start()
      }
      if (!primary) elect()
      return () => {
        entry.abort.abort()
        locations.delete(entry)
        if (primary === entry) {
          primary = undefined
          elect()
        }
        if (locations.size === 0) stopHost()
      }
    },
    stop: stopHost,
    runtime: rt,
    sessions,
    /** Handle one event (tests). */
    onEvent,
    commit,
  }
}
