// A fake Managed Agents server for tests: sessions with an event log, the data-plane routes the runtime uses, an
// event stream with deltas, and a scriptable agent. It checks the key on every request, records what each request
// carried (headers, bodies) so tests can see where a key went, and never needs a network.

import { createServer } from 'node:http'

/**
 * @param {object} o
 * @param {string} [o.key] the key every request must carry
 * @param {'bearer'|'x-api-key'} [o.auth]
 * @param {string} [o.prefix] the path prefix (/api/v3)
 * @param {boolean} [o.list] whether GET <prefix>/sessions exists
 * @param {(session, text, api) => void|Promise<void>} [o.agent] how the agent answers a user message
 */
export async function startFakeMA(o = {}) {
  const key = o.key ?? 'test-key-0123456789'
  const auth = o.auth ?? 'bearer'
  const prefix = o.prefix ?? '/api/v3'
  const sessions = new Map()
  const requests = []
  const streams = new Set()
  let n = 0
  const id = (p) => `${p}-${String(++n).padStart(6, '0')}`
  const now = () => new Date(Date.now() + n).toISOString()

  const api = {
    sessions,
    requests,
    key,
    /** Appends an event to a session's log (and to open streams). */
    emit(sid, ev) {
      const s = sessions.get(sid)
      const full = { id: ev.id ?? id('sevt'), processed_at: now(), ...ev }
      s.events.push(full)
      s.updated_at = full.processed_at
      if (ev.type === 'session.status_running') s.status = 'running'
      if (ev.type === 'session.status_idle') s.status = 'idle'
      if (ev.type === 'session.status_terminated') s.status = 'terminated'
      for (const st of streams) if (st.sid === sid) st.res.write(`data: ${JSON.stringify(full)}\n\n`)
      return full
    },
    /** Sends a live delta frame for an event not yet persisted. */
    delta(sid, eventId, text, eventType = 'agent.message') {
      for (const st of streams) if (st.sid === sid) st.res.write(`data: ${JSON.stringify({ type: 'event_delta', event_id: eventId, event_type: eventType, delta: { content: [{ type: 'text', text }] } })}\n\n`)
    },
    streamCount: () => streams.size,
    create(body = {}) {
      const sid = id('sesn')
      const s = { id: sid, title: body.title ?? '', status: 'idle', agent: { id: body.agent ?? 'agent-a', name: 'Helper', model: { id: 'model-x' } }, created_at: now(), updated_at: now(), events: [] }
      sessions.set(sid, s)
      return s
    },
  }

  const agent = o.agent ?? (async (s, text) => {
    api.emit(s.id, { type: 'session.status_running' })
    api.emit(s.id, { type: 'agent.message', content: [{ type: 'text', text: `echo: ${text}` }] })
    api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } })
  })

  const pub = (s) => ({ id: s.id, title: s.title, status: s.status, agent: s.agent, created_at: s.created_at, updated_at: s.updated_at })
  const send = (res, code, body) => {
    res.writeHead(code, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify(body))
  }

  const server = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://x')
    let body = ''
    for await (const c of req) body += c
    requests.push({ method: req.method, path: url.pathname, query: Object.fromEntries(url.searchParams), headers: req.headers, body })
    const given = auth === 'bearer' ? req.headers.authorization : req.headers['x-api-key']
    if (given !== (auth === 'bearer' ? `Bearer ${key}` : key)) return send(res, 401, { error: { code: 'AuthenticationError', message: 'The API key does not exist.' } })
    if (!url.pathname.startsWith(prefix)) return send(res, 404, { error: { code: 'NotFound', message: 'no route' } })
    const parts = url.pathname.slice(prefix.length).split('/').filter(Boolean).map(decodeURIComponent)
    if (parts[0] !== 'sessions') return send(res, 404, { error: { code: 'NotFound', message: 'no route' } })
    if (parts.length === 1 && req.method === 'GET') {
      if (o.list === false) return send(res, 404, { error: { code: 'NotFound', message: 'no route' } })
      const all = [...sessions.values()].sort((a, b) => b.updated_at.localeCompare(a.updated_at))
      return send(res, 200, { data: all.slice(0, Number(url.searchParams.get('limit') ?? 20)).map(pub) })
    }
    if (parts.length === 1 && req.method === 'POST') return send(res, 200, pub(api.create(JSON.parse(body || '{}'))))
    const s = sessions.get(parts[1])
    if (!s) return send(res, 404, { error: { code: 'NotFound', message: `The session ${parts[1]} does not exist` } })
    if (parts.length === 2) return send(res, 200, pub(s))
    if (parts[2] === 'events' && parts.length === 3 && req.method === 'GET') {
      const pageNo = Number(url.searchParams.get('page') ?? 0)
      const after = url.searchParams.get('after')
      let list = s.events
      if (after && !url.searchParams.get('page')) {
        const i = list.findIndex((e) => e.id === after)
        list = i >= 0 ? list.slice(i + 1) : list
      }
      const size = Math.min(Number(url.searchParams.get('limit') ?? 100), o.pageSize ?? 100)
      const start = pageNo * size
      const pageItems = list.slice(start, start + size)
      return send(res, 200, { data: pageItems, ...(start + size < list.length ? { next_page: String(pageNo + 1) } : {}) })
    }
    if (parts[2] === 'events' && parts.length === 3 && req.method === 'POST') {
      const { events = [] } = JSON.parse(body || '{}')
      if (events.length !== 1) return send(res, 400, { error: { code: 'InvalidParameter', message: 'one event per request' } })
      const ev = events[0]
      const stored = api.emit(s.id, ev)
      if (ev.type === 'user.message') {
        const text = (ev.content ?? []).filter((b) => b.type === 'text').map((b) => b.text).join('')
        setTimeout(() => void agent(s, text, api), 5)
      } else if (ev.type === 'user.interrupt') {
        setTimeout(() => api.emit(s.id, { type: 'session.status_idle', stop_reason: { type: 'end_turn' } }), 5)
      } else if (ev.type === 'user.tool_confirmation' || ev.type === 'user.custom_tool_result') {
        setTimeout(() => void o.onAnswer?.(s, ev, api), 5)
      }
      return send(res, 200, { data: [stored] })
    }
    if (parts[2] === 'events' && parts[3] === 'stream') {
      res.writeHead(200, { 'Content-Type': 'text/event-stream' })
      res.write(': ok\n\n')
      const st = { sid: s.id, res }
      streams.add(st)
      req.on('close', () => streams.delete(st))
      return
    }
    return send(res, 404, { error: { code: 'NotFound', message: 'no route' } })
  })
  await new Promise((r) => server.listen(0, '127.0.0.1', r))
  api.url = `http://127.0.0.1:${server.address().port}`
  api.close = () => {
    for (const st of streams) st.res.end()
    server.closeAllConnections?.()
    server.close()
  }
  return api
}
